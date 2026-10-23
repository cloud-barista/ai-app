// Package card describes the partner institution's natural language agent;
// mock implementation for integration, not part of the Innogrid prototype.
//
// The card is built on every request from what the agent holds at that moment:
// the tools come from the current MCP session to the automation engine, so a
// card read before the engine is reachable says so instead of listing tools the
// agent cannot call. Agent registration is the partner institution's area; this
// mock only posts the card when a registry is configured.
package card

import (
	"net/url"
	"sort"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/redact"
	"github.com/ish-hcc/ai-agent-proto/internal/tools"
	"github.com/ish-hcc/ai-agent-proto/mock/partner/nlagent/llm"
)

// Identity of this agent as a registry sees it.
const (
	Name            = "partner-mock-nl-agent"
	DisplayName     = "[partner mock] 자연어 지원 에이전트 (협력 기관 담당 영역 모의 구현)"
	Version         = "0.1.0"
	Owner           = "partner institution (mock)"
	Framework       = "AI-Ops"
	ServesFramework = "AI-App"
	Description     = "[partner mock] Turns an operator instruction into calls to the Innogrid automation engine " +
		"tools through a planning model. Mock implementation of the partner institution's natural language agent " +
		"for integration, not part of the Innogrid prototype"
	// IntentEndpoint is the route an instruction is posted to.
	IntentEndpoint = "/aiapp/partner/nl/ns/{nsId}/intents"
)

// llmRequires lists what the agent needs of a planning model.
var llmRequires = []string{"tool_use", "anthropic-messages"}

// AgentCard describes the agent to an agent registry: who it is, where its
// tools come from and what it needs of a model.
type AgentCard struct {
	Name            string `json:"name" example:"partner-mock-nl-agent"`
	DisplayName     string `json:"displayName"`
	Version         string `json:"version" example:"0.1.0"`
	Owner           string `json:"owner"`
	Framework       string `json:"framework" example:"AI-Ops"`
	ServesFramework string `json:"servesFramework" example:"AI-App"`
	Description     string `json:"description"`
	// Mock is always true: this card describes a mock implementation.
	Mock bool `json:"mock"`
	// IntentEndpoint is where an instruction is handed to the agent.
	IntentEndpoint string      `json:"intentEndpoint"`
	MCPServers     []MCPServer `json:"mcpServers"`
	// Tools is what the agent can call, as of its current MCP session. Empty
	// when it is not connected.
	Tools  []model.AgentCardTool `json:"tools"`
	LLM    LLM                   `json:"llm"`
	DryRun bool                  `json:"dryRun"`
	// Registration is how the last registry registration went. It is left out
	// of the card sent to the registry.
	Registration *model.AgentRegistration `json:"registration,omitempty"`
}

// MCPServer is one MCP server the agent takes tools from.
type MCPServer struct {
	URL       string `json:"url" example:"http://127.0.0.1:8091/aiapp/engine/mcp"`
	Connected bool   `json:"connected"`
	ToolCount int    `json:"toolCount"`
}

// LLM is the planning model the agent needs. It never carries the key.
type LLM struct {
	// BaseURL is the scheme and host of the model API, without path or credentials.
	BaseURL  string   `json:"baseUrl" example:"https://api.anthropic.com"`
	Model    string   `json:"model" example:"claude-opus-5"`
	Requires []string `json:"requires" example:"tool_use,anthropic-messages"`
}

// ToolCache is the part of the MCP client the card reads. It must not connect.
type ToolCache interface {
	Endpoint() string
	Cached() (grades map[string]tools.Grade, dryRun, connected bool)
}

// Card builds the agent card.
type Card struct {
	tools     ToolCache
	llm       llm.Config
	registrar *Registrar
}

// New builds a Card. registrar may be nil, which reports registration as
// disabled.
func New(cache ToolCache, llmCfg llm.Config, registrar *Registrar) *Card {
	return &Card{tools: cache, llm: llmCfg, registrar: registrar}
}

// Build returns the card with the current registration state.
func (c *Card) Build() AgentCard {
	card := c.describe()
	registration := disabledRegistration()
	if c.registrar != nil {
		registration = c.registrar.State()
	}
	card.Registration = &registration
	return card
}

// describe returns the card without the registration state, which is what is
// sent to the registry.
func (c *Card) describe() AgentCard {
	grades, dryRun, connected := c.tools.Cached()

	names := make([]string, 0, len(grades))
	for name := range grades {
		names = append(names, name)
	}
	sort.Strings(names)

	cardTools := make([]model.AgentCardTool, 0, len(names))
	for _, name := range names {
		cardTools = append(cardTools, model.AgentCardTool{
			Name:            name,
			Grade:           string(grades[name]),
			GuardedByDryRun: grades[name].GuardedByDryRun(),
		})
	}

	return AgentCard{
		Name:            Name,
		DisplayName:     DisplayName,
		Version:         Version,
		Owner:           Owner,
		Framework:       Framework,
		ServesFramework: ServesFramework,
		Description:     Description,
		Mock:            true,
		IntentEndpoint:  IntentEndpoint,
		MCPServers: []MCPServer{{
			URL:       redact.URL(c.tools.Endpoint()),
			Connected: connected,
			ToolCount: len(cardTools),
		}},
		Tools: cardTools,
		LLM: LLM{
			BaseURL:  hostOnly(c.llm.BaseURL),
			Model:    c.llm.Model,
			Requires: append([]string{}, llmRequires...),
		},
		DryRun: dryRun,
	}
}

// hostOnly keeps the scheme and host of raw, so that no path, query or user
// information of the model endpoint leaves through the card.
func hostOnly(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	return (&url.URL{Scheme: u.Scheme, Host: u.Host}).String()
}
