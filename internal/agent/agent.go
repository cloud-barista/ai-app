// Package agent runs the plan-and-call loop between the model and the tools.
//
// The loop is iterative rather than one-shot because a deployment is a chain
// whose later steps depend on earlier answers: which specs came back decides
// which one to pin, and the review verdict decides whether to provision at all.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/ish-hcc/ai-agent-proto/internal/archive"
	"github.com/ish-hcc/ai-agent-proto/internal/config"
	"github.com/ish-hcc/ai-agent-proto/internal/llm"
	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/tools"
)

// kindIntent labels an archive record produced by a natural language run.
const kindIntent = "intent"

// ToolSource is the tool surface a run works through. The in-process registry
// and the MCP client of the AI-App service both satisfy it.
type ToolSource interface {
	Definitions() []llm.ToolDef
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

// Service runs operator intents against a tool source.
type Service struct {
	llm      *llm.Client
	tools    ToolSource
	archive  *archive.Store
	maxSteps int
}

// NewService builds a Service.
func NewService(client *llm.Client, source ToolSource, store *archive.Store, cfg config.AgentConfig) *Service {
	return &Service{llm: client, tools: source, archive: store, maxSteps: cfg.MaxSteps}
}

// Enabled reports whether the planning model is reachable.
func (s *Service) Enabled() bool { return s.llm.Enabled() }

// Run drives one operator intent to an answer.
//
// Every iteration, every tool argument and every guard verdict is archived, even
// when the run fails: an aborted run is exactly the case a later planner needs to
// learn from, so it must not be the one case that leaves no record.
func (s *Service) Run(ctx context.Context, nsID, intent string) (*model.IntentResp, error) {
	if !s.llm.Enabled() {
		return nil, llm.ErrDisabled
	}
	if preparer, ok := s.tools.(Preparer); ok {
		if err := preparer.Prepare(ctx); err != nil {
			return nil, err
		}
	}

	runID := newRunID()
	started := time.Now()
	record := model.ArchiveRecord{
		RunID:     runID,
		StartedAt: started,
		Namespace: nsID,
		Kind:      kindIntent,
		Intent:    intent,
		Steps:     []model.ToolStep{},
	}

	messages := []llm.Message{{
		Role:    llm.RoleUser,
		Content: []llm.Block{llm.TextBlock(intent)},
	}}
	definitions := s.tools.Definitions()
	system := s.systemPrompt(nsID)

	log.Info().Str("runId", runID).Str("nsId", nsID).Int("tools", len(definitions)).
		Bool("dryRun", s.tools.DryRun()).Msg("Starting agent run")

	usage := &model.TokenUsage{}
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
			answer = response.Text()
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

	record.EndedAt = time.Now()
	record.Answer = answer
	record.Usage = usage
	if runErr != nil {
		record.Error = runErr.Error()
	}
	if err := s.archive.Append(ctx, record); err != nil {
		// A failed archive write must not hide the run result, but it does mean
		// the second-year feedback loop lost an input, so it is logged loudly.
		log.Error().Err(err).Str("runId", runID).Msg("Failed to archive agent run")
	}

	if runErr != nil {
		return nil, runErr
	}

	log.Info().Str("runId", runID).Int("steps", len(record.Steps)).
		Int("inputTokens", usage.InputTokens).Int("outputTokens", usage.OutputTokens).
		Msg("Finished agent run")

	return &model.IntentResp{
		RunID:  runID,
		Answer: answer,
		Steps:  record.Steps,
		DryRun: s.tools.DryRun(),
		Usage:  usage,
	}, nil
}

// runTool executes one tool call and renders both the archive step and the block
// that goes back to the model.
func (s *Service) runTool(ctx context.Context, nsID string, index int, call llm.Block) (model.ToolStep, llm.Block) {
	grade := s.tools.Grade(call.Name)
	calledAt := time.Now()

	step := model.ToolStep{
		Index:    index,
		Tool:     call.Name,
		Grade:    string(grade),
		Input:    rawOrString(call.Input),
		CalledAt: calledAt,
	}

	output, err := s.tools.Call(ctx, nsID, call.Name, call.Input)
	step.Duration = time.Since(calledAt).Round(time.Millisecond).String()

	if err != nil {
		step.Executed = false
		step.Error = err.Error()
		log.Warn().Err(err).Str("nsId", nsID).Str("tool", call.Name).Str("grade", string(grade)).
			Msg("Tool call failed")
		return step, llm.ToolResultBlock(call.ID, err.Error(), true)
	}

	// A guarded tool under dry run still returns through the deploy service, and
	// that result carries DryRun: the step is recorded as not executed so the
	// archive shows what the guard stopped.
	blockedByGuard := s.tools.DryRun() && grade.GuardedByDryRun()
	step.Executed = !blockedByGuard
	step.Output = output

	encoded, err := json.Marshal(output)
	if err != nil {
		message := fmt.Sprintf("tool %s produced an unreadable result", call.Name)
		step.Error = message
		return step, llm.ToolResultBlock(call.ID, message, true)
	}

	log.Info().Str("nsId", nsID).Str("tool", call.Name).Str("grade", string(grade)).
		Bool("executed", step.Executed).Msg("Tool call completed")

	return step, llm.ToolResultBlock(call.ID, string(encoded), false)
}

// systemPrompt states the operating rules of the run.
//
// It says what the guard does but does not ask the model to respect it: the
// guard is enforced in the tool layer, and telling the model only saves it from
// promising an operator something that will not happen.
func (s *Service) systemPrompt(nsID string) string {
	guard := "Infrastructure changing tools are live: a deploy, control or delete call creates, changes or removes billable cloud resources."
	if s.tools.DryRun() {
		guard = "Dry run is on. Deploy, control and delete calls are planned and archived but never sent to any cloud. Say so plainly in your answer instead of claiming the work is done."
	}

	return "You are the AI application deployment agent of the AI-MCMP platform.\n" +
		"You place AI inference applications (vLLM, Triton, Ollama and the like) onto AI semiconductor nodes " +
		"across heterogeneous clouds, by calling the tools you are given.\n\n" +
		"Namespace for this run: " + nsID + ". Tools already operate in it, so never ask for it.\n\n" +
		"How to work:\n" +
		"- Resolve the application first. If the operator names it in words, call find_ai_apps with their own\n" +
		"  wording and take the ranked candidates; do not scan the catalog yourself. No candidate means the\n" +
		"  catalog holds nothing that fits, so say that instead of deploying the nearest entry. When two\n" +
		"  candidates score close together, ask which one rather than guessing.\n" +
		"- The accelerator requirement of the application drives spec selection. Recommend specs rather than guessing a spec id.\n" +
		"- Review before you deploy. A review costs nothing and reports whether creation is viable and what it will cost per hour.\n" +
		"- Pick a deployment name that reflects the application, and reuse it if the operator names one.\n" +
		"- If a tool fails, read the error and adjust. Do not repeat the same call unchanged.\n\n" +
		guard + "\n\n" +
		"Answer in the language the operator used. State what you did, which spec you chose and why, " +
		"the estimated cost when you have it, and what the operator has to do next."
}

func rawOrString(input json.RawMessage) any {
	if len(input) == 0 {
		return map[string]any{}
	}
	var decoded any
	if err := json.Unmarshal(input, &decoded); err != nil {
		// Keeping the raw text is better than dropping the argument: an archive
		// that cannot explain why a call failed is not worth much.
		return string(input)
	}
	return decoded
}

func newRunID() string {
	return "run-" + strconv.FormatInt(time.Now().UTC().UnixNano(), 36)
}
