// Package mcpclient calls the tools of an MCP server of this prototype: the
// AI-App service, for the automation engine.
//
// The tool list and the dry-run state are read once per connection and cached:
// they change only when the AI-App service restarts, and a restart drops the
// MCP session, which is what makes the client fetch them again.
package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rs/zerolog/log"

	"github.com/ish-hcc/ai-agent-proto/internal/mcpserver"
	"github.com/ish-hcc/ai-agent-proto/internal/redact"
	"github.com/ish-hcc/ai-agent-proto/internal/tools"
)

const (
	clientName    = "ai-mcmp-mcp-client"
	clientVersion = "0.1.0"

	// connectTimeout bounds a connection attempt made outside of a run, where
	// no caller context exists to bound it.
	connectTimeout = 10 * time.Second
)

// ErrUnavailable is returned when the MCP server cannot be reached.
var ErrUnavailable = errors.New("MCP tool server is unavailable")

type namespaceKey struct{}

// WithNamespace returns a context whose MCP requests carry nsID in the
// namespace header.
func WithNamespace(ctx context.Context, nsID string) context.Context {
	return context.WithValue(ctx, namespaceKey{}, nsID)
}

// namespaceTransport sets the namespace header from the request context, so a
// single session serves calls for any namespace. It also sets the bearer token,
// when there is one, on every request.
type namespaceTransport struct {
	base  http.RoundTripper
	token string
}

func (t namespaceTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	nsID, _ := req.Context().Value(namespaceKey{}).(string)
	if nsID == "" && t.token == "" {
		return t.base.RoundTrip(req)
	}
	req = req.Clone(req.Context())
	if nsID != "" {
		req.Header.Set(mcpserver.NamespaceHeader, nsID)
	}
	if t.token != "" {
		req.Header.Set("Authorization", "Bearer "+t.token)
	}
	return t.base.RoundTrip(req)
}

// Client is a tool source backed by an MCP server of this prototype.
type Client struct {
	name       string
	endpoint   string
	display    string
	httpClient *http.Client

	mu          sync.Mutex
	session     *mcp.ClientSession
	definitions []tools.Definition
	grades      map[string]tools.Grade
	dryRun      bool
}

// New builds a Client for the Streamable HTTP endpoint. It does not connect.
// A non-empty token is sent as a bearer token.
func New(endpoint, token string) *Client {
	return NewNamed(clientName, endpoint, token)
}

// NewNamed builds a Client that introduces itself to the server as name.
func NewNamed(name, endpoint, token string) *Client {
	return &Client{
		name:       name,
		endpoint:   endpoint,
		display:    redact.URL(endpoint),
		httpClient: &http.Client{Transport: namespaceTransport{base: http.DefaultTransport, token: token}},
	}
}

// Endpoint reports the MCP endpoint this client talks to.
func (c *Client) Endpoint() string { return c.endpoint }

// Prepare makes sure a live session exists before a run. A session that no
// longer answers is dropped and replaced once.
func (c *Client) Prepare(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.session != nil {
		if err := c.session.Ping(ctx, nil); err == nil {
			return nil
		}
		log.Warn().Str("endpoint", c.display).Msg("MCP session stopped answering, reconnecting")
		c.dropLocked()
	}
	return c.connectLocked(ctx)
}

// Definitions returns the cached tool list, connecting once if there is none.
func (c *Client) Definitions() []tools.Definition {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.session == nil {
		ctx, cancel := context.WithTimeout(context.Background(), connectTimeout)
		defer cancel()
		if err := c.connectLocked(ctx); err != nil {
			log.Warn().Err(err).Str("endpoint", c.display).Msg("Failed to read the MCP tool list")
			return nil
		}
	}
	return c.definitions
}

// DryRun reports the dry-run state the AI-App service announced at initialize.
func (c *Client) DryRun() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dryRun
}

// Cached reports the tool grades and the dry-run state of the current session
// without connecting. connected is false, and grades empty, when there is no
// session.
func (c *Client) Cached() (grades map[string]tools.Grade, dryRun, connected bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	grades = make(map[string]tools.Grade, len(c.grades))
	if c.session == nil {
		return grades, false, false
	}
	for name, grade := range c.grades {
		grades[name] = grade
	}
	return grades, c.dryRun, true
}

// Grade reports the risk grade the AI-App service announced for a tool.
func (c *Client) Grade(name string) tools.Grade {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.grades[name]
}

// Call runs one tool in nsID and returns its JSON result.
//
// A failed call is not retried: a write tool may already have taken effect on
// the server, so repeating it is not safe. The session is dropped instead, and
// the next run reconnects.
func (c *Client) Call(ctx context.Context, nsID, name string, input json.RawMessage) (any, error) {
	c.mu.Lock()
	session := c.session
	c.mu.Unlock()
	if session == nil {
		return nil, fmt.Errorf("%w: not connected", ErrUnavailable)
	}

	if len(input) == 0 {
		input = json.RawMessage("{}")
	}
	result, err := session.CallTool(WithNamespace(ctx, nsID), &mcp.CallToolParams{Name: name, Arguments: input})
	if err != nil {
		c.mu.Lock()
		if c.session == session {
			c.dropLocked()
		}
		c.mu.Unlock()
		return nil, fmt.Errorf("%w: tools/call %s: %s", ErrUnavailable, name, redact.Error(err, c.endpoint))
	}

	text := textOf(result)
	if result.IsError {
		return nil, errors.New(text)
	}
	return json.RawMessage(text), nil
}

// Close ends the session, if any.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.session == nil {
		return nil
	}
	err := c.session.Close()
	c.session = nil
	return err
}

func (c *Client) connectLocked(ctx context.Context) error {
	transport := &mcp.StreamableClientTransport{
		Endpoint:   c.endpoint,
		HTTPClient: c.httpClient,
		// The agent only ever asks; it does not wait for server notifications.
		DisableStandaloneSSE: true,
	}
	client := mcp.NewClient(&mcp.Implementation{Name: c.name, Version: clientVersion}, nil)
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return fmt.Errorf("%w: connect %s: %s", ErrUnavailable, c.display, redact.Error(err, c.endpoint))
	}

	definitions := []tools.Definition{}
	grades := map[string]tools.Grade{}
	for tool, err := range session.Tools(ctx, nil) {
		if err != nil {
			_ = session.Close()
			return fmt.Errorf("%w: tools/list: %s", ErrUnavailable, redact.Error(err, c.endpoint))
		}
		definitions = append(definitions, tools.Definition{
			Name:        tool.Name,
			Description: tool.Description,
			InputSchema: tool.InputSchema,
		})
		grade, _ := tool.Meta[mcpserver.MetaGrade].(string)
		grades[tool.Name] = tools.Grade(grade)
	}

	dryRun := false
	if init := session.InitializeResult(); init != nil {
		dryRun, _ = init.Meta[mcpserver.MetaDryRun].(bool)
	}

	c.session = session
	c.definitions = definitions
	c.grades = grades
	c.dryRun = dryRun

	log.Info().Str("endpoint", c.display).Int("tools", len(definitions)).Bool("dryRun", dryRun).
		Msg("Connected to the MCP server")
	return nil
}

func (c *Client) dropLocked() {
	if c.session != nil {
		_ = c.session.Close()
	}
	c.session = nil
}

// textOf joins the text contents of a tool result. The AI-App server returns
// exactly one.
func textOf(result *mcp.CallToolResult) string {
	parts := make([]string, 0, len(result.Content))
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			parts = append(parts, text.Text)
		}
	}
	return strings.Join(parts, "\n")
}
