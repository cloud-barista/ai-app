// Package agentcard describes the automation agent to an agent registry and
// registers it there.
//
// The card is built on every request from what the agent holds at that moment:
// the tools come from the current MCP session, so a card read before the AI-App
// service is reachable says so instead of listing tools the agent cannot call.
package agentcard

import (
	"net/url"
	"sort"

	"github.com/ish-hcc/ai-agent-proto/internal/config"
	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/redact"
	"github.com/ish-hcc/ai-agent-proto/internal/tools"
)

// Identity of this agent as a registry sees it.
const (
	Name            = "ai-mcmp-app-automation-agent"
	DisplayName     = "AI 응용 관리 자동화 에이전트"
	Version         = "0.1.0"
	Owner           = "innogrid"
	Framework       = "AI-Ops"
	ServesFramework = "AI-App"
	Description     = "Turns an operator instruction into AI-App tool calls through a planning model: " +
		"resolves the AI application, recommends an accelerator spec, reviews the request and, " +
		"when the dry-run guard is off, deploys and controls it"
	// IntentEndpoint is the route an instruction is posted to.
	IntentEndpoint = "/aiapp/ns/{nsId}/intents"
)

// llmRequires lists what the agent needs of a planning model.
var llmRequires = []string{"tool_use", "anthropic-messages"}

// ToolCache is the part of the MCP client the card reads. It must not connect.
type ToolCache interface {
	Endpoint() string
	Cached() (grades map[string]tools.Grade, dryRun, connected bool)
}

// Card builds the agent card.
type Card struct {
	tools     ToolCache
	llm       config.LLMConfig
	registrar *Registrar
}

// New builds a Card. registrar may be nil, which reports registration as
// disabled.
func New(cache ToolCache, llmCfg config.LLMConfig, registrar *Registrar) *Card {
	return &Card{tools: cache, llm: llmCfg, registrar: registrar}
}

// Build returns the card with the current registration state.
func (c *Card) Build() model.AgentCard {
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
func (c *Card) describe() model.AgentCard {
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

	return model.AgentCard{
		Name:            Name,
		DisplayName:     DisplayName,
		Version:         Version,
		Owner:           Owner,
		Framework:       Framework,
		ServesFramework: ServesFramework,
		Description:     Description,
		IntentEndpoint:  IntentEndpoint,
		MCPServers: []model.AgentMCPServer{{
			URL:       redact.URL(c.tools.Endpoint()),
			Connected: connected,
			ToolCount: len(cardTools),
		}},
		Tools: cardTools,
		LLM: model.AgentCardLLM{
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
