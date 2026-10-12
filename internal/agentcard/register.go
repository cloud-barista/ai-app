package agentcard

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/redact"
)

// Registration states reported in the card.
const (
	StatusDisabled   = "disabled"
	StatusPending    = "pending"
	StatusRegistered = "registered"
	StatusFailed     = "failed"
)

const (
	// connectAttempts is how many times the MCP connection is tried before the
	// card is registered without tools.
	connectAttempts = 3
	// registerAttempts is how many times the card is posted before giving up.
	registerAttempts = 3
	// registerInterval is the wait between two attempts, of connecting or of
	// posting.
	registerInterval = 5 * time.Second
	// registerTimeout bounds one attempt.
	registerTimeout = 10 * time.Second
	// replyLimit caps how much of a registry reply is read. The reply is never
	// kept in the card or logged, only its length.
	replyLimit = 4096
)

// Registrar posts the agent card to an agent registry and remembers the outcome.
type Registrar struct {
	url        string
	display    string
	token      string
	httpClient *http.Client
	connects   int
	attempts   int
	interval   time.Duration

	mu    sync.Mutex
	state model.AgentRegistration
}

// NewRegistrar builds a Registrar for the registry at registryURL. An empty
// registryURL leaves registration disabled. A non-empty token is sent as a
// bearer token.
func NewRegistrar(registryURL, token string) *Registrar {
	r := &Registrar{
		url:        strings.TrimRight(registryURL, "/"),
		token:      token,
		httpClient: &http.Client{Timeout: registerTimeout},
		connects:   connectAttempts,
		attempts:   registerAttempts,
		interval:   registerInterval,
	}
	if r.url == "" {
		r.state = disabledRegistration()
	} else {
		r.display = redact.URL(r.url)
		r.state = model.AgentRegistration{RegistryURL: r.display, Status: StatusPending}
	}
	return r
}

// Enabled reports whether a registry is configured.
func (r *Registrar) Enabled() bool { return r.url != "" }

// State returns the outcome of the last registration.
func (r *Registrar) State() model.AgentRegistration {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state
}

// Preparer is implemented by a ToolCache that can connect. Register calls it
// before posting, so a card sent while the AI-App service is still starting
// lists the tools once it is up.
type Preparer interface {
	Prepare(ctx context.Context) error
}

// Register posts the card of c to the registry, retrying a failed attempt up to
// the attempt limit. A failure is recorded and logged; it never stops the agent.
//
// It first makes sure the MCP session exists, trying up to the connect limit.
// When it still does not, the card is registered as not connected: there is
// no second registration later.
func (r *Registrar) Register(ctx context.Context, c *Card) {
	if !r.Enabled() {
		return
	}
	if !r.connect(ctx, c) {
		return
	}

	var message string
	for attempt := 1; attempt <= r.attempts; attempt++ {
		if attempt > 1 {
			select {
			case <-ctx.Done():
				r.finish(StatusFailed, "registration cancelled: "+message)
				return
			case <-time.After(r.interval):
			}
		}

		ok, msg := r.post(ctx, c.describe())
		message = msg
		if ok {
			log.Info().Str("registry", r.display).Int("attempt", attempt).Msg("Registered the agent card")
			r.finish(StatusRegistered, msg)
			return
		}
		log.Warn().Str("registry", r.display).Int("attempt", attempt).Int("attempts", r.attempts).
			Str("reason", msg).Msg("Failed to register the agent card")
	}
	log.Error().Str("registry", r.display).Str("reason", message).
		Msg("Gave up registering the agent card; the agent keeps running unregistered")
	r.finish(StatusFailed, message)
}

// connect tries to establish the MCP session of c before the card is built. It
// reports false only when ctx ended, in which case registration is recorded as
// cancelled.
func (r *Registrar) connect(ctx context.Context, c *Card) bool {
	preparer, ok := c.tools.(Preparer)
	if !ok {
		return true
	}

	var err error
	for attempt := 1; attempt <= r.connects; attempt++ {
		if attempt > 1 {
			select {
			case <-ctx.Done():
				r.finish(StatusFailed, "registration cancelled while connecting to the AI-App MCP server")
				return false
			case <-time.After(r.interval):
			}
		}
		attemptCtx, cancel := context.WithTimeout(ctx, registerTimeout)
		err = preparer.Prepare(attemptCtx)
		cancel()
		if err == nil {
			return true
		}
	}
	endpoint := c.tools.Endpoint()
	log.Warn().Str("error", redact.Error(err, endpoint)).Str("url", redact.URL(endpoint)).Int("attempts", r.connects).
		Msg("AI-App MCP server is still not reachable: registering the agent card without tools")
	return true
}

// post sends card once and reports whether the registry accepted it, with a
// message describing the reply.
func (r *Registrar) post(ctx context.Context, card model.AgentCard) (bool, string) {
	body, err := json.Marshal(card)
	if err != nil {
		return false, fmt.Sprintf("encode card: %v", err)
	}
	base, err := url.Parse(r.url)
	if err != nil {
		return false, "build request: invalid registry URL " + r.display
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base.JoinPath("agents").String(), bytes.NewReader(body))
	if err != nil {
		return false, "build request: " + redact.Error(err, r.url)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if r.token != "" {
		req.Header.Set("Authorization", "Bearer "+r.token)
	}

	resp, err := r.httpClient.Do(req)
	if err != nil {
		return false, redact.Error(err, r.url)
	}
	defer func() { _ = resp.Body.Close() }()

	replyBytes, _ := io.Copy(io.Discard, io.LimitReader(resp.Body, replyLimit))
	log.Debug().Str("registry", r.display).Int("status", resp.StatusCode).Int64("replyBytes", replyBytes).
		Msg("Agent registry replied")
	message := strings.TrimSpace(fmt.Sprintf("HTTP %d %s", resp.StatusCode, http.StatusText(resp.StatusCode)))
	return resp.StatusCode >= 200 && resp.StatusCode < 300, message
}

func (r *Registrar) finish(status, message string) {
	now := time.Now().UTC()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.state = model.AgentRegistration{RegistryURL: r.display, Status: status, At: &now, Message: message}
}

func disabledRegistration() model.AgentRegistration {
	return model.AgentRegistration{Status: StatusDisabled, Message: "AIAPP_AGENT_REGISTRY_URL is not set"}
}
