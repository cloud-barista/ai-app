package model

import "time"

// ArchiveRecord is one archived agent run or direct deployment action.
//
// The record holds the operator instruction, every tool the agent chose, what it
// answered, and what the guard did. Keeping the intermediate steps is what makes
// the archive reusable as feedback for later planning, not just an audit log.
type ArchiveRecord struct {
	RunID     string      `json:"runId"`
	StartedAt time.Time   `json:"startedAt"`
	EndedAt   time.Time   `json:"endedAt"`
	Namespace string      `json:"nsId"`
	Kind      string      `json:"kind" example:"intent"`
	Intent    string      `json:"intent,omitempty"`
	Steps     []ToolStep  `json:"steps"`
	Answer    string      `json:"answer,omitempty"`
	Error     string      `json:"error,omitempty"`
	Usage     *TokenUsage `json:"usage,omitempty"`
}

// ToolStep is one tool the agent called during a run.
type ToolStep struct {
	Index int    `json:"index"`
	Tool  string `json:"tool"`
	// Grade is the tool risk grade that decided whether the call was executed.
	Grade string `json:"grade"`
	Input any    `json:"input"`
	// Executed is false when the dry-run guard blocked an infrastructure change.
	Executed bool      `json:"executed"`
	Output   any       `json:"output,omitempty"`
	Error    string    `json:"error,omitempty"`
	CalledAt time.Time `json:"calledAt"`
	Duration string    `json:"duration"`
}

// TokenUsage is the planning cost of one agent run.
type TokenUsage struct {
	InputTokens  int `json:"inputTokens"`
	OutputTokens int `json:"outputTokens"`
}

// ArchiveListResp is the archive listing body.
type ArchiveListResp struct {
	Records []ArchiveRecord `json:"records"`
}
