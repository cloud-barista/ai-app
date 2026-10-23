package model

import "time"

// ToolDescriptor describes one tool the agent may call.
//
// The list is exposed over REST so that another framework can adopt the same
// tool surface without reading this service's source.
type ToolDescriptor struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// Grade is one of read, review, write, destructive.
	Grade string `json:"grade"`
	// GuardedByDryRun is true when the dry-run setting can block this tool.
	GuardedByDryRun bool `json:"guardedByDryRun"`
	InputSchema     any  `json:"inputSchema"`
}

// AgentCardTool is one tool listed in a card, with its grade.
type AgentCardTool struct {
	Name string `json:"name"`
	// Grade is one of read, review, write, destructive.
	Grade           string `json:"grade"`
	GuardedByDryRun bool   `json:"guardedByDryRun"`
}

// AgentRegistration is the outcome of registering a card with an agent registry.
type AgentRegistration struct {
	RegistryURL string `json:"registryUrl"`
	// Status is disabled, pending, registered or failed.
	Status  string     `json:"status" example:"registered"`
	At      *time.Time `json:"at,omitempty"`
	Message string     `json:"message,omitempty"`
}

// ToolListResp is the tool catalog body.
type ToolListResp struct {
	DryRun bool             `json:"dryRun"`
	Tools  []ToolDescriptor `json:"tools"`
}
