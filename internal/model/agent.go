package model

import "time"

// IntentReq is a natural language instruction for the deployment agent.
type IntentReq struct {
	// Intent is what the operator wants, in their own words.
	Intent string `json:"intent" validate:"required" example:"Llama 3.1 8B 추론 서버를 GPU 한 장짜리 노드에 올려줘"`
}

// Validate reports why an intent cannot be run.
func (r *IntentReq) Validate() error {
	if r.Intent == "" {
		return errRequired("intent")
	}
	return nil
}

// IntentResp is the outcome of one agent run.
type IntentResp struct {
	RunID string `json:"runId"`
	// Answer is what the agent reports back to the operator.
	Answer string `json:"answer"`
	// Steps is every tool the agent chose, in order, with what the guard did.
	Steps []ToolStep `json:"steps"`
	// DryRun reflects the guard setting the run executed under.
	DryRun bool        `json:"dryRun"`
	Usage  *TokenUsage `json:"usage,omitempty"`
}

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

// AgentCard describes the automation agent to an agent registry: who it is,
// which framework it serves, where its tools come from and what it needs of a
// model.
type AgentCard struct {
	Name            string `json:"name" example:"ai-mcmp-app-automation-agent"`
	DisplayName     string `json:"displayName" example:"AI 응용 관리 자동화 에이전트"`
	Version         string `json:"version" example:"0.1.0"`
	Owner           string `json:"owner" example:"innogrid"`
	Framework       string `json:"framework" example:"AI-Ops"`
	ServesFramework string `json:"servesFramework" example:"AI-App"`
	Description     string `json:"description"`
	// IntentEndpoint is where an instruction is handed to the agent.
	IntentEndpoint string           `json:"intentEndpoint" example:"/aiapp/ns/{nsId}/intents"`
	MCPServers     []AgentMCPServer `json:"mcpServers"`
	// Tools is what the agent can call, as of its current MCP session. Empty
	// when it is not connected.
	Tools  []AgentCardTool `json:"tools"`
	LLM    AgentCardLLM    `json:"llm"`
	DryRun bool            `json:"dryRun"`
	// Registration is how the last registry registration went. It is left out
	// of the card sent to the registry.
	Registration *AgentRegistration `json:"registration,omitempty"`
}

// AgentMCPServer is one MCP server the agent takes tools from.
type AgentMCPServer struct {
	URL       string `json:"url" example:"http://127.0.0.1:8090/aiapp/mcp"`
	Connected bool   `json:"connected"`
	ToolCount int    `json:"toolCount"`
}

// AgentCardTool is one tool the agent can call.
type AgentCardTool struct {
	Name string `json:"name"`
	// Grade is one of read, review, write, destructive.
	Grade           string `json:"grade"`
	GuardedByDryRun bool   `json:"guardedByDryRun"`
}

// AgentCardLLM is the planning model the agent needs. It never carries the key.
type AgentCardLLM struct {
	// BaseURL is the scheme and host of the model API, without path or credentials.
	BaseURL  string   `json:"baseUrl" example:"https://api.anthropic.com"`
	Model    string   `json:"model" example:"claude-opus-5"`
	Requires []string `json:"requires" example:"tool_use,anthropic-messages"`
}

// AgentRegistration is the outcome of registering the agent card.
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
