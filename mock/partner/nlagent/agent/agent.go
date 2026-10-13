// Package agent is the partner institution's natural language agent; mock
// implementation for integration, not part of the Innogrid prototype.
//
// Natural language interpretation (LLM operation management, the natural
// language support agent) is the partner institution's area. This mock turns an
// operator instruction into calls to the tools of the Innogrid automation
// engine over MCP, so the integration point between the two can be exercised
// until the partner agent exists. The loop is iterative because a later call
// depends on an earlier answer: the catalog lookup decides the appId, and the
// engine run of a deployment decides what to tell the operator.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/ish-hcc/ai-agent-proto/internal/archive"
	"github.com/ish-hcc/ai-agent-proto/internal/engine"
	"github.com/ish-hcc/ai-agent-proto/internal/tools"
	"github.com/ish-hcc/ai-agent-proto/mock/partner/nlagent/llm"
)

// defaultRunLimit bounds a run listing.
const defaultRunLimit = 20

// ToolSource is the tool surface a run works through: the MCP client of the
// automation engine.
type ToolSource interface {
	Definitions() []tools.Definition
	DryRun() bool
	Grade(name string) tools.Grade
	Call(ctx context.Context, nsID, name string, input json.RawMessage) (any, error)
}

// Preparer is implemented by a ToolSource that has to reach something before a
// run can start. Run calls it first, so an unreachable source fails the run
// instead of handing the model an empty tool list.
type Preparer interface {
	Prepare(ctx context.Context) error
}

// Service runs operator intents against the engine tools.
type Service struct {
	llm      *llm.Client
	tools    ToolSource
	runs     *archive.Log[Run]
	maxSteps int
}

// NewService builds a Service that stops a run after maxSteps model turns.
func NewService(client *llm.Client, source ToolSource, runs *archive.Log[Run], maxSteps int) *Service {
	return &Service{llm: client, tools: source, runs: runs, maxSteps: maxSteps}
}

// Enabled reports whether the planning model is configured.
func (s *Service) Enabled() bool { return s.llm.Enabled() }

// Run drives one operator intent to an answer.
//
// Every iteration and every tool argument is archived, even when the run fails:
// an aborted run is exactly the case worth reading later.
func (s *Service) Run(ctx context.Context, nsID, intent string) (*IntentResp, error) {
	if !s.llm.Enabled() {
		return nil, llm.ErrDisabled
	}
	if preparer, ok := s.tools.(Preparer); ok {
		if err := preparer.Prepare(ctx); err != nil {
			return nil, err
		}
	}

	runID := newRunID()
	record := Run{
		RunID:     runID,
		Source:    Source,
		StartedAt: time.Now().UTC(),
		Namespace: nsID,
		Kind:      kindIntent,
		Intent:    intent,
		Steps:     []Step{},
		DryRun:    s.tools.DryRun(),
	}

	messages := []llm.Message{{
		Role:    llm.RoleUser,
		Content: []llm.Block{llm.TextBlock(intent)},
	}}
	definitions := toolDefs(s.tools.Definitions())
	system := s.systemPrompt(nsID)

	log.Info().Str("runId", runID).Str("nsId", nsID).Int("tools", len(definitions)).
		Bool("dryRun", record.DryRun).Msg(Marker + " Starting natural language agent run")

	usage := &TokenUsage{}
	answer := ""
	var runErr error

	for step := 1; step <= s.maxSteps; step++ {
		response, err := s.llm.CreateMessage(ctx, system, messages, definitions)
		if err != nil {
			runErr = fmt.Errorf("failed to plan step %d: %w", step, err)
			break
		}
		usage.InputTokens += response.Usage.InputTokens
		usage.OutputTokens += response.Usage.OutputTokens

		calls := response.ToolCalls()
		if response.StopReason != llm.StopReasonToolUse || len(calls) == 0 {
			answer = Marker + " " + response.Text()
			break
		}

		messages = append(messages, llm.Message{Role: llm.RoleAssistant, Content: response.Content})

		// Every tool result of one assistant turn goes back in a single user
		// message; splitting them teaches the model to stop batching calls.
		results := make([]llm.Block, 0, len(calls))
		for _, call := range calls {
			stepRecord, block := s.runTool(ctx, nsID, len(record.Steps)+1, call)
			record.Steps = append(record.Steps, stepRecord)
			results = append(results, block)
		}
		messages = append(messages, llm.Message{Role: llm.RoleUser, Content: results})

		if step == s.maxSteps {
			runErr = fmt.Errorf("agent stopped after %d steps without a final answer", s.maxSteps)
		}
	}

	record.EndedAt = time.Now().UTC()
	record.Answer = answer
	record.Usage = usage
	if runErr != nil {
		record.Error = runErr.Error()
	}
	if err := s.runs.Append(record); err != nil {
		log.Error().Err(err).Str("runId", runID).Msg(Marker + " Failed to archive agent run")
	}

	if runErr != nil {
		return nil, runErr
	}

	log.Info().Str("runId", runID).Int("steps", len(record.Steps)).
		Int("inputTokens", usage.InputTokens).Int("outputTokens", usage.OutputTokens).
		Msg(Marker + " Finished natural language agent run")

	return &IntentResp{
		RunID:  runID,
		Source: Source,
		Answer: answer,
		Steps:  record.Steps,
		DryRun: record.DryRun,
		Usage:  usage,
	}, nil
}

// ListRuns reads the most recent runs, newest first.
func (s *Service) ListRuns(limit int) ([]Run, error) {
	if limit <= 0 {
		limit = defaultRunLimit
	}
	return s.runs.List(limit)
}

// GetRun reads one archived run. found is false when no run has runID.
func (s *Service) GetRun(runID string) (run *Run, found bool, err error) {
	return s.runs.Find(func(r *Run) bool { return r.RunID == runID })
}

// runTool executes one tool call and renders both the archive step and the block
// that goes back to the model.
func (s *Service) runTool(ctx context.Context, nsID string, index int, call llm.Block) (Step, llm.Block) {
	grade := s.tools.Grade(call.Name)
	calledAt := time.Now().UTC()

	step := Step{
		Index:    index,
		Tool:     call.Name,
		Grade:    string(grade),
		Input:    rawOrString(call.Input),
		CalledAt: calledAt,
	}

	output, err := s.tools.Call(ctx, nsID, call.Name, call.Input)
	step.Duration = time.Since(calledAt).Round(time.Millisecond).String()

	if err != nil {
		step.Error = err.Error()
		log.Warn().Err(err).Str("nsId", nsID).Str("tool", call.Name).Str("grade", string(grade)).
			Msg(Marker + " Engine tool call failed")
		return step, llm.ToolResultBlock(call.ID, err.Error(), true)
	}

	encoded, err := json.Marshal(output)
	if err != nil {
		message := fmt.Sprintf("tool %s produced an unreadable result", call.Name)
		step.Error = message
		return step, llm.ToolResultBlock(call.ID, message, true)
	}
	step.Output = rawOrString(encoded)

	// A workflow tool answers with the engine run. Its status says whether the
	// dry-run gate held a step, which is what decides "executed" here.
	var engineRun struct {
		RunID    string `json:"runId"`
		Workflow string `json:"workflow"`
		Status   string `json:"status"`
	}
	if json.Unmarshal(encoded, &engineRun) == nil && engineRun.RunID != "" && engineRun.Workflow != "" {
		step.EngineRunID = engineRun.RunID
		step.EngineStatus = engineRun.Status
	}
	blocked := step.EngineStatus == string(engine.RunBlocked) ||
		(step.EngineStatus == "" && s.tools.DryRun() && grade.GuardedByDryRun())
	step.Executed = !blocked

	log.Info().Str("nsId", nsID).Str("tool", call.Name).Str("grade", string(grade)).
		Str("engineRunId", step.EngineRunID).Str("engineStatus", step.EngineStatus).
		Bool("executed", step.Executed).Msg(Marker + " Engine tool call completed")

	return step, llm.ToolResultBlock(call.ID, string(encoded), false)
}

// systemPrompt states the operating rules of the run.
//
// It says what the gate does but does not ask the model to respect it: the gate
// is enforced by the AI-App service under the engine, and telling the model only
// saves it from promising an operator something that will not happen.
func (s *Service) systemPrompt(nsID string) string {
	gate := "The AI-App write gate is open: a deploy, heal or control call creates, changes or removes billable cloud resources."
	if s.tools.DryRun() {
		gate = "The AI-App dry-run gate is on. Write steps are planned and reviewed but never sent to any cloud; " +
			"the engine reports them as blocked and the steps after them as waiting. Say so plainly in your answer " +
			"instead of claiming the work is done."
	}

	return "You are a natural language assistant for AI application operations on the AI-MCMP platform.\n" +
		"You turn an operator instruction into calls to the AI application automation engine, which deploys and " +
		"controls AI inference applications (vLLM, Ollama, Open WebUI and the like) on AI semiconductor nodes " +
		"across heterogeneous clouds. The engine runs each workflow as fixed steps; you choose which workflow " +
		"and with what input.\n\n" +
		"Namespace for this run: " + nsID + ". The tools already operate in it, so never ask for it.\n\n" +
		"Tools:\n" +
		"- " + engine.ToolApplications + ": look up the application catalog. Pass the operator's own wording as " +
		"query to get ranked candidates; pass no query to list every entry. Use it to choose the appId; never " +
		"invent one. No candidate means the catalog holds nothing that fits, so say that instead of deploying the " +
		"nearest entry. When two candidates score close together, ask which one rather than guessing.\n" +
		"- " + engine.ToolDeploy + ": deploy an application by appId under a deploymentName. The engine itself " +
		"deploys any model service the application requires first, recommends the spec and image, reviews and " +
		"checks that it answers, so do not deploy the model services yourself and do not pick specs.\n" +
		"- " + engine.ToolStatus + ": report node state, serving health and accelerators of a deployment.\n" +
		"- " + engine.ToolHeal + ": check a deployment and apply its application again once when it does not answer.\n" +
		"- " + engine.ToolControl + ": suspend, resume, reboot or terminate a deployment.\n\n" +
		"How to work:\n" +
		"- Pick a short deployment name that reflects the application (lower case, digits and hyphens), and reuse " +
		"the name the operator gives, if any.\n" +
		"- Every workflow tool returns the engine run: runId, status (succeeded, failed or blocked), message and " +
		"steps. Read the status and the messages of failed, blocked and waiting steps before you answer.\n" +
		"- If a tool fails, read the error and adjust. Do not repeat the same call unchanged.\n\n" +
		gate + "\n\n" +
		"Answer in the language the operator used. State which application you chose and why, the engine run id " +
		"and its status, what the engine did and did not do, and what the operator has to do next."
}

// toolDefs renders the engine tools in the shape the Messages API expects.
func toolDefs(defs []tools.Definition) []llm.ToolDef {
	out := make([]llm.ToolDef, 0, len(defs))
	for _, def := range defs {
		out = append(out, llm.ToolDef{Name: def.Name, Description: def.Description, InputSchema: def.InputSchema})
	}
	return out
}

func rawOrString(input json.RawMessage) any {
	if len(input) == 0 {
		return map[string]any{}
	}
	var decoded any
	if err := json.Unmarshal(input, &decoded); err != nil {
		// Keeping the raw text is better than dropping the argument: a record
		// that cannot explain why a call failed is not worth much.
		return string(input)
	}
	return decoded
}

func newRunID() string {
	return "nl-" + strconv.FormatInt(time.Now().UTC().UnixNano(), 36)
}
