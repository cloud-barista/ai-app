// Package engine is the AI application management automation agent of AI-Ops:
// the automatic deploy and control engine.
//
// It runs fixed workflows, deploy, heal, control and status, as ordered calls
// to the AI-App tools over MCP, and decides each next step from the JSON result
// of the last one. No model takes part. Every run is archived with every step,
// what it was given, what it got back and whether the dry-run gate held it.
package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/ish-hcc/ai-agent-proto/internal/archive"
	"github.com/ish-hcc/ai-agent-proto/internal/tools"
)

// Workflow names.
const (
	WorkflowDeploy  = "deploy"
	WorkflowHeal    = "heal"
	WorkflowControl = "control"
	WorkflowStatus  = "status"
)

// Workflows lists every workflow the engine runs, in the order they are
// described.
var Workflows = []string{WorkflowDeploy, WorkflowHeal, WorkflowControl, WorkflowStatus}

const (
	// defaultHealthTimeout and defaultHealthInterval bound how long a step
	// waiting for an application to answer keeps asking, and how often.
	defaultHealthTimeout  = 15 * time.Minute
	defaultHealthInterval = 20 * time.Second
	// defaultRunLimit bounds a run listing.
	defaultRunLimit = 20
)

var (
	// ErrUnknownWorkflow is returned for a workflow name the engine does not run.
	ErrUnknownWorkflow = errors.New("unknown workflow")
	// ErrInvalidInput is returned when a workflow input is malformed or incomplete.
	ErrInvalidInput = errors.New("invalid workflow input")
	// ErrRunNotFound is returned when no archived run has the asked identifier.
	ErrRunNotFound = errors.New("run not found")
)

// AppClient is the AI-App MCP client the engine works through.
// mcpclient.Client implements it.
type AppClient interface {
	Prepare(ctx context.Context) error
	Call(ctx context.Context, nsID, name string, input json.RawMessage) (any, error)
	DryRun() bool
	Endpoint() string
	Cached() (grades map[string]tools.Grade, dryRun, connected bool)
}

// Engine runs workflows against the AI-App service.
type Engine struct {
	app            AppClient
	runs           *archive.Log[Run]
	healthTimeout  time.Duration
	healthInterval time.Duration
}

// Option configures an Engine.
type Option func(*Engine)

// WithHealthWait sets how long a step waiting for an application to answer
// keeps asking, and how often.
func WithHealthWait(timeout, interval time.Duration) Option {
	return func(e *Engine) {
		e.healthTimeout = timeout
		e.healthInterval = interval
	}
}

// New builds an Engine calling app and archiving into runs.
func New(app AppClient, runs *archive.Log[Run], opts ...Option) *Engine {
	e := &Engine{
		app:            app,
		runs:           runs,
		healthTimeout:  defaultHealthTimeout,
		healthInterval: defaultHealthInterval,
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// DryRun reports the dry-run state of the AI-App service, connecting once if
// there is no session yet. It reads true when the service cannot be reached,
// so nothing advertises a write path that is not known to be open.
func (e *Engine) DryRun(ctx context.Context) bool {
	if _, dryRun, connected := e.app.Cached(); connected {
		return dryRun
	}
	if err := e.app.Prepare(ctx); err != nil {
		return true
	}
	return e.app.DryRun()
}

// Execute runs the workflow named workflow in nsID with its JSON input, and
// archives the run.
//
// A run whose steps failed or were blocked is still a run and is returned
// without error. An error means no run took place: the workflow is unknown, the
// input is invalid, or the AI-App service is not reachable.
func (e *Engine) Execute(ctx context.Context, nsID, workflow string, input json.RawMessage) (*Run, error) {
	if len(input) == 0 {
		input = json.RawMessage("{}")
	}
	switch workflow {
	case WorkflowDeploy:
		var in DeployInput
		if err := decodeInput(input, &in); err != nil {
			return nil, err
		}
		return e.Deploy(ctx, nsID, in)
	case WorkflowHeal:
		var in HealInput
		if err := decodeInput(input, &in); err != nil {
			return nil, err
		}
		return e.Heal(ctx, nsID, in)
	case WorkflowControl:
		var in ControlInput
		if err := decodeInput(input, &in); err != nil {
			return nil, err
		}
		return e.Control(ctx, nsID, in)
	case WorkflowStatus:
		var in StatusInput
		if err := decodeInput(input, &in); err != nil {
			return nil, err
		}
		return e.Status(ctx, nsID, in)
	}
	return nil, fmt.Errorf("%w %q: want one of %s", ErrUnknownWorkflow, workflow, strings.Join(Workflows, ", "))
}

// ListApplications answers a catalog lookup from the AI-App service: the
// ranked candidates of find_ai_apps when in has a query, every application in
// short form from list_ai_apps otherwise. It is a read and runs no workflow, so
// nothing is archived.
func (e *Engine) ListApplications(ctx context.Context, nsID string, in CatalogQuery) (json.RawMessage, error) {
	if err := e.app.Prepare(ctx); err != nil {
		return nil, err
	}
	query := strings.TrimSpace(in.Query)
	if query == "" {
		return callApp(ctx, e.app, nsID, "list_ai_apps", map[string]any{})
	}
	args := map[string]any{"query": query}
	if in.Limit > 0 {
		args["limit"] = in.Limit
	}
	return callApp(ctx, e.app, nsID, "find_ai_apps", args)
}

// ListRuns reads the most recent runs, newest first.
func (e *Engine) ListRuns(limit int) ([]Run, error) {
	if limit <= 0 {
		limit = defaultRunLimit
	}
	return e.runs.List(limit)
}

// GetRun reads one archived run.
func (e *Engine) GetRun(runID string) (*Run, error) {
	run, found, err := e.runs.Find(func(r *Run) bool { return r.RunID == runID })
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("%w: %s", ErrRunNotFound, runID)
	}
	return run, nil
}

// start connects to the AI-App service and opens a run.
func (e *Engine) start(ctx context.Context, nsID, workflow string, input any) (*runner, error) {
	if err := e.app.Prepare(ctx); err != nil {
		return nil, err
	}
	return &runner{
		engine: e,
		nsID:   nsID,
		run: &Run{
			RunID:     newRunID(),
			Workflow:  workflow,
			Namespace: nsID,
			Input:     input,
			Steps:     []Step{},
			DryRun:    e.app.DryRun(),
			StartedAt: time.Now().UTC(),
		},
	}, nil
}

// runner is one run in progress.
type runner struct {
	engine *Engine
	nsID   string
	run    *Run
	// steps are kept by pointer so a step can still be marked after later
	// steps were added.
	steps []*Step
	// failed is set by the first failed step. Every step after it is skipped.
	failed      bool
	failedIndex int
	// message overrides the run message derived from the step outcomes.
	message string
}

// call runs one AI-App tool as a step and decodes its result into out, when
// out is not nil. It reports false, with the step recorded as failed, when the
// call or the decoding fails.
func (r *runner) call(ctx context.Context, name, tool string, input any, out any) (*Step, bool) {
	step := r.add(name, tool, input, StepSucceeded, "")
	started := time.Now().UTC()
	step.StartedAt = &started

	raw, err := r.invoke(ctx, tool, input)
	ended := time.Now().UTC()
	step.EndedAt = &ended
	step.Output = raw
	if err != nil {
		r.fail(step, err.Error())
		return step, false
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			r.fail(step, fmt.Sprintf("unreadable %s result: %v", tool, err))
			return step, false
		}
	}
	return step, true
}

// invoke calls one AI-App tool and returns its JSON result.
func (r *runner) invoke(ctx context.Context, tool string, input any) (json.RawMessage, error) {
	return callApp(ctx, r.engine.app, r.nsID, tool, input)
}

// callApp calls one AI-App tool in nsID and returns its JSON result.
func callApp(ctx context.Context, app AppClient, nsID, tool string, input any) (json.RawMessage, error) {
	args, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("encode %s input: %w", tool, err)
	}
	result, err := app.Call(ctx, nsID, tool, args)
	if err != nil {
		return nil, err
	}
	switch v := result.(type) {
	case json.RawMessage:
		return v, nil
	case []byte:
		return json.RawMessage(v), nil
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("encode %s result: %w", tool, err)
	}
	return encoded, nil
}

// add appends a step without running it.
func (r *runner) add(name, tool string, input any, status StepStatus, message string) *Step {
	step := &Step{
		Index:   len(r.steps) + 1,
		Name:    name,
		Tool:    tool,
		Input:   input,
		Status:  status,
		Message: message,
	}
	r.steps = append(r.steps, step)
	return step
}

// fail marks step failed. The first failure makes every later step skipped.
func (r *runner) fail(step *Step, reason string) {
	step.Status = StepFailed
	step.Error = reason
	if !r.failed {
		r.failed = true
		r.failedIndex = step.Index
	}
}

// skipMessage says why a step after a failure was not run.
func (r *runner) skipMessage() string {
	return fmt.Sprintf("not run because step %d failed", r.failedIndex)
}

// finish closes the run, derives its status and archives it.
func (r *runner) finish() *Run {
	run := r.run
	run.EndedAt = time.Now().UTC()
	run.Steps = make([]Step, 0, len(r.steps))
	for _, step := range r.steps {
		run.Steps = append(run.Steps, *step)
	}

	var failed, blocked *Step
	waiting := 0
	for i := range run.Steps {
		step := &run.Steps[i]
		switch step.Status {
		case StepFailed:
			if failed == nil {
				failed = step
			}
		case StepBlocked:
			if blocked == nil {
				blocked = step
			}
		case StepWaiting:
			waiting++
		}
	}

	switch {
	case failed != nil:
		run.Status = RunFailed
		run.Message = fmt.Sprintf("step %d (%s) failed: %s", failed.Index, failed.Name, failed.Error)
	case blocked != nil:
		run.Status = RunBlocked
		run.Message = fmt.Sprintf("step %d (%s) was blocked by the AI-App dry-run gate", blocked.Index, blocked.Name)
		if waiting > 0 {
			run.Message += fmt.Sprintf("; %d later steps wait for it", waiting)
		}
	default:
		run.Status = RunSucceeded
		run.Message = fmt.Sprintf("all %d steps succeeded", len(run.Steps))
	}
	if r.message != "" {
		run.Message = r.message
	}

	if err := r.engine.runs.Append(*run); err != nil {
		// The run already happened; losing its record costs the archive an
		// input, so it is logged loudly rather than failing the caller.
		log.Error().Err(err).Str("runId", run.RunID).Msg("Failed to archive engine run")
	} else {
		log.Info().Str("runId", run.RunID).Str("workflow", run.Workflow).Str("nsId", run.Namespace).
			Str("status", string(run.Status)).Int("steps", len(run.Steps)).Msg("Archived engine run")
	}
	return run
}

func decodeInput(input json.RawMessage, out any) error {
	if err := json.Unmarshal(input, out); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	return nil
}

func required(field string) error {
	return fmt.Errorf("%w: %s is required", ErrInvalidInput, field)
}

func newRunID() string {
	return "eng-" + strconv.FormatInt(time.Now().UTC().UnixNano(), 36)
}
