package agent

import (
	"errors"
	"time"
)

// Marker labels every answer and every archived run of this mock, so that
// neither reads as the work of the Innogrid prototype.
const Marker = "[partner mock]"

// Source names who produced a run, in every run record.
const Source = Marker + " partner institution's natural language agent (mock implementation)"

// kindIntent labels a run record produced from a natural language instruction.
const kindIntent = "intent"

// ErrEmptyIntent is returned for an instruction with no text.
var ErrEmptyIntent = errors.New("intent is required")

// IntentReq is a natural language instruction for the agent.
type IntentReq struct {
	// Intent is what the operator wants, in their own words.
	Intent string `json:"intent" example:"Open WebUI 챗봇을 띄워줘"`
}

// Validate reports why an intent cannot be run.
func (r *IntentReq) Validate() error {
	if r.Intent == "" {
		return ErrEmptyIntent
	}
	return nil
}

// IntentResp is the outcome of one agent run.
type IntentResp struct {
	RunID string `json:"runId"`
	// Source says this answer comes from the partner mock.
	Source string `json:"source"`
	// Answer is what the agent reports back to the operator, starting with Marker.
	Answer string `json:"answer"`
	// Steps is every engine tool the agent chose, in order.
	Steps []Step `json:"steps"`
	// DryRun is the AI-App dry-run state the engine reported for the run.
	DryRun bool        `json:"dryRun"`
	Usage  *TokenUsage `json:"usage,omitempty"`
}

// Step is one engine tool the agent called during a run.
type Step struct {
	Index int    `json:"index"`
	Tool  string `json:"tool"`
	// Grade is the risk grade the engine announced for the tool.
	Grade string `json:"grade"`
	Input any    `json:"input"`
	// Executed is false when the call failed or the dry-run gate held a write.
	Executed bool   `json:"executed"`
	Output   any    `json:"output,omitempty"`
	Error    string `json:"error,omitempty"`
	// EngineRunID and EngineStatus identify the engine run a workflow tool
	// started, so the step can be traced to the engine run archive.
	EngineRunID  string    `json:"engineRunId,omitempty"`
	EngineStatus string    `json:"engineStatus,omitempty"`
	CalledAt     time.Time `json:"calledAt"`
	Duration     string    `json:"duration"`
}

// TokenUsage is the planning cost of one run.
type TokenUsage struct {
	InputTokens  int `json:"inputTokens"`
	OutputTokens int `json:"outputTokens"`
}

// Run is one archived agent run.
type Run struct {
	RunID     string      `json:"runId"`
	Source    string      `json:"source"`
	StartedAt time.Time   `json:"startedAt"`
	EndedAt   time.Time   `json:"endedAt"`
	Namespace string      `json:"nsId"`
	Kind      string      `json:"kind"`
	Intent    string      `json:"intent"`
	Steps     []Step      `json:"steps"`
	Answer    string      `json:"answer,omitempty"`
	DryRun    bool        `json:"dryRun"`
	Error     string      `json:"error,omitempty"`
	Usage     *TokenUsage `json:"usage,omitempty"`
}

// RunListResp is the run listing body.
type RunListResp struct {
	Runs []Run `json:"runs"`
}
