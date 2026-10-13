package engine

import (
	"sort"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/redact"
	"github.com/ish-hcc/ai-agent-proto/internal/registration"
	"github.com/ish-hcc/ai-agent-proto/internal/tools"
)

// Identity of the engine as an agent registry sees it.
const (
	CardName        = "ai-mcmp-app-automation-engine"
	CardDisplayName = "AI 응용 관리 자동화 에이전트 (자동배포/자동제어 엔진)"
	CardVersion     = "0.1.0"
	CardOwner       = "innogrid"
	CardFramework   = "AI-Ops"
	CardServes      = "AI-App"
	CardDescription = "Runs fixed deploy, heal, control and status workflows as ordered AI-App tool calls over MCP, " +
		"deciding each next step from the last result, and archives every run. Uses no LLM."
	// WorkflowEndpoint is the route a workflow is started on.
	WorkflowEndpoint = "/aiapp/engine/workflows/{workflow}?ns={nsId}"
	// MCPEndpoint is where the engine publishes its workflows as MCP tools.
	MCPEndpoint = "/aiapp/engine/mcp"
	// RegistryEnv is the setting that turns registration on.
	RegistryEnv = "AIAPP_ENGINE_REGISTRY_URL"
)

// Card describes the engine.
type Card struct {
	Name            string `json:"name" example:"ai-mcmp-app-automation-engine"`
	DisplayName     string `json:"displayName"`
	Version         string `json:"version" example:"0.1.0"`
	Owner           string `json:"owner" example:"innogrid"`
	Framework       string `json:"framework" example:"AI-Ops"`
	ServesFramework string `json:"servesFramework" example:"AI-App"`
	Description     string `json:"description"`
	// UsesLLM is always false: the workflows are fixed.
	UsesLLM          bool          `json:"usesLLM"`
	Workflows        []string      `json:"workflows" example:"deploy,heal,control,status"`
	WorkflowEndpoint string        `json:"workflowEndpoint"`
	MCPEndpoint      string        `json:"mcpEndpoint"`
	MCP              CardMCP       `json:"mcp"`
	Tools            CardToolGroup `json:"tools"`
	// DryRun is the AI-App dry-run state, as of the current MCP session.
	DryRun bool `json:"dryRun"`
	// Registration is how the last registry registration went. It is left out
	// of the card sent to the registry.
	Registration *model.AgentRegistration `json:"registration,omitempty"`
}

// CardMCP is the AI-App MCP server the engine calls.
type CardMCP struct {
	AppServer CardMCPServer `json:"appServer"`
}

// CardMCPServer is one MCP server and whether the engine holds a session to it.
type CardMCPServer struct {
	URL       string `json:"url" example:"http://127.0.0.1:8090/aiapp/mcp"`
	Connected bool   `json:"connected"`
	ToolCount int    `json:"toolCount"`
}

// CardToolGroup lists the tools the engine publishes and the AI-App tools it
// calls.
type CardToolGroup struct {
	Exposed []model.AgentCardTool `json:"exposed"`
	// Uses is empty when the engine is not connected to the AI-App service.
	Uses []model.AgentCardTool `json:"uses"`
}

// CardBuilder builds the engine card from the current state.
type CardBuilder struct {
	engine    *Engine
	exposed   *tools.Registry
	registrar *registration.Registrar
}

// NewCardBuilder builds a CardBuilder. registrar may be nil, which reports
// registration as disabled.
func NewCardBuilder(e *Engine, exposed *tools.Registry, registrar *registration.Registrar) *CardBuilder {
	return &CardBuilder{engine: e, exposed: exposed, registrar: registrar}
}

// Build returns the card with the current registration state.
func (b *CardBuilder) Build() Card {
	card := b.describe()
	state := registration.New("", "", RegistryEnv).State()
	if b.registrar != nil {
		state = b.registrar.State()
	}
	card.Registration = &state
	return card
}

// Subject is what the registrar posts: the card without its registration
// state, after connecting the AI-App MCP server so the card lists its tools.
func (b *CardBuilder) Subject() registration.Subject {
	return registration.Subject{
		Name:     "engine card",
		Describe: func() any { return b.describe() },
		Preparer: b.engine.app,
	}
}

func (b *CardBuilder) describe() Card {
	grades, dryRun, connected := b.engine.app.Cached()
	uses := gradedTools(grades)

	exposed := make([]model.AgentCardTool, 0, len(b.exposed.Names()))
	for _, name := range b.exposed.Names() {
		grade := b.exposed.Grade(name)
		exposed = append(exposed, model.AgentCardTool{
			Name:            name,
			Grade:           string(grade),
			GuardedByDryRun: grade.GuardedByDryRun(),
		})
	}
	if !connected {
		// Unknown is reported as gated, the same reading DryRun gives.
		dryRun = true
	}

	return Card{
		Name:             CardName,
		DisplayName:      CardDisplayName,
		Version:          CardVersion,
		Owner:            CardOwner,
		Framework:        CardFramework,
		ServesFramework:  CardServes,
		Description:      CardDescription,
		UsesLLM:          false,
		Workflows:        append([]string{}, Workflows...),
		WorkflowEndpoint: WorkflowEndpoint,
		MCPEndpoint:      MCPEndpoint,
		MCP: CardMCP{AppServer: CardMCPServer{
			URL:       redact.URL(b.engine.app.Endpoint()),
			Connected: connected,
			ToolCount: len(uses),
		}},
		Tools:  CardToolGroup{Exposed: exposed, Uses: uses},
		DryRun: dryRun,
	}
}

func gradedTools(grades map[string]tools.Grade) []model.AgentCardTool {
	names := make([]string, 0, len(grades))
	for name := range grades {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]model.AgentCardTool, 0, len(names))
	for _, name := range names {
		out = append(out, model.AgentCardTool{
			Name:            name,
			Grade:           string(grades[name]),
			GuardedByDryRun: grades[name].GuardedByDryRun(),
		})
	}
	return out
}
