package agentcard

import (
	"context"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/registration"
)

// Registration states reported in the card.
const (
	StatusDisabled   = registration.StatusDisabled
	StatusPending    = registration.StatusPending
	StatusRegistered = registration.StatusRegistered
	StatusFailed     = registration.StatusFailed
)

// registryEnv is the setting that turns registration on.
const registryEnv = "AIAPP_AGENT_REGISTRY_URL"

// Registrar posts the agent card to an agent registry and remembers the outcome.
type Registrar struct {
	*registration.Registrar
}

// NewRegistrar builds a Registrar for the registry at registryURL. An empty
// registryURL leaves registration disabled. A non-empty token is sent as a
// bearer token.
func NewRegistrar(registryURL, token string) *Registrar {
	return &Registrar{Registrar: registration.New(registryURL, token, registryEnv)}
}

// Register posts the card of c to the registry. It first connects the AI-App
// MCP server, when the tool cache can, so the card lists the tools; see
// registration.Registrar.Register.
func (r *Registrar) Register(ctx context.Context, c *Card) {
	subject := registration.Subject{
		Name:     "agent card",
		Describe: func() any { return c.describe() },
	}
	if preparer, ok := c.tools.(registration.Preparer); ok {
		subject.Preparer = preparer
	}
	r.Registrar.Register(ctx, subject)
}

func disabledRegistration() model.AgentRegistration {
	return registration.New("", "", registryEnv).State()
}
