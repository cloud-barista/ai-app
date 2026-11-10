// Package registration posts a self description, an agent card or an engine
// card, to an agent registry and remembers how that went.
//
// What is posted is up to the caller. This package owns only the HTTP side: the
// retries, the bearer token, keeping registry replies and URL credentials out
// of logs and of the reported state.
package registration

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

// Registration states reported in a card.
const (
	StatusDisabled   = "disabled"
	StatusPending    = "pending"
	StatusRegistered = "registered"
	StatusFailed     = "failed"
)

const (
	// connectAttempts is how many times the tool server connection is tried
	// before the card is registered without tools.
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

// Preparer connects the tool server a card lists tools from. Register calls it
// before posting, so a card sent while that server is still starting lists the
// tools once it is up.
type Preparer interface {
	Prepare(ctx context.Context) error
	Endpoint() string
}

// Subject is what gets registered.
type Subject struct {
	// Name is how logs call the card, for example "agent card".
	Name string
	// Describe returns the body posted to the registry.
	Describe func() any
	// Preparer, when set, is connected before the card is described.
	Preparer Preparer
}

// Registrar posts a card to an agent registry and remembers the outcome.
type Registrar struct {
	url        string
	display    string
	token      string
	unsetEnv   string
	httpClient *http.Client
	connects   int
	attempts   int
	interval   time.Duration

	mu    sync.Mutex
	state model.AgentRegistration
}

// New builds a Registrar for the registry at registryURL. An empty registryURL
// leaves registration disabled, and the reported state names unsetEnv as the
// setting to fill in. A non-empty token is sent as a bearer token.
func New(registryURL, token, unsetEnv string) *Registrar {
	r := &Registrar{
		url:        strings.TrimRight(registryURL, "/"),
		token:      token,
		unsetEnv:   unsetEnv,
		httpClient: &http.Client{Timeout: registerTimeout},
		connects:   connectAttempts,
		attempts:   registerAttempts,
		interval:   registerInterval,
	}
	if r.url == "" {
		r.state = r.disabled()
	} else {
		r.display = redact.URL(r.url)
		r.state = model.AgentRegistration{RegistryURL: r.display, Status: StatusPending}
	}
	return r
}

// SetInterval changes the wait between two attempts.
func (r *Registrar) SetInterval(d time.Duration) { r.interval = d }

// Enabled reports whether a registry is configured.
func (r *Registrar) Enabled() bool { return r.url != "" }

// State returns the outcome of the last registration.
func (r *Registrar) State() model.AgentRegistration {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state
}

// Register posts the card of s to the registry, retrying a failed attempt up to
// the attempt limit. A failure is recorded and logged; it never stops the
// caller.
//
// It first makes sure the tool server connection exists, trying up to the
// connect limit. When it still does not, the card is registered as not
// connected: there is no second registration later.
func (r *Registrar) Register(ctx context.Context, s Subject) {
	if !r.Enabled() {
		return
	}
	if !r.connect(ctx, s) {
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

		ok, msg := r.post(ctx, s.Describe())
		message = msg
		if ok {
			log.Info().Str("registry", r.display).Int("attempt", attempt).Msg("Registered the " + s.Name)
			r.finish(StatusRegistered, msg)
			return
		}
		log.Warn().Str("registry", r.display).Int("attempt", attempt).Int("attempts", r.attempts).
			Str("reason", msg).Msg("Failed to register the " + s.Name)
	}
	log.Error().Str("registry", r.display).Str("reason", message).
		Msg("Gave up registering the " + s.Name + "; the process keeps running unregistered")
	r.finish(StatusFailed, message)
}

// connect tries to establish the tool server connection of s before the card
// is built. It reports false only when ctx ended, in which case registration is
// recorded as cancelled.
func (r *Registrar) connect(ctx context.Context, s Subject) bool {
	if s.Preparer == nil {
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
		err = s.Preparer.Prepare(attemptCtx)
		cancel()
		if err == nil {
			return true
		}
	}
	endpoint := s.Preparer.Endpoint()
	log.Warn().Str("error", redact.Error(err, endpoint)).Str("url", redact.URL(endpoint)).Int("attempts", r.connects).
		Msg("AI-App MCP server is still not reachable: registering the " + s.Name + " without tools")
	return true
}

// post sends card once and reports whether the registry accepted it, with a
// message describing the reply.
func (r *Registrar) post(ctx context.Context, card any) (bool, string) {
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

func (r *Registrar) disabled() model.AgentRegistration {
	return model.AgentRegistration{Status: StatusDisabled, Message: r.unsetEnv + " is not set"}
}
