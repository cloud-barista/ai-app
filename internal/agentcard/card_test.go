package agentcard

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ish-hcc/ai-agent-proto/internal/catalog"
	"github.com/ish-hcc/ai-agent-proto/internal/config"
	"github.com/ish-hcc/ai-agent-proto/internal/deploy"
	"github.com/ish-hcc/ai-agent-proto/internal/mcpclient"
	"github.com/ish-hcc/ai-agent-proto/internal/mcpserver"
	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/tools"
	"github.com/ish-hcc/ai-agent-proto/internal/tumblebug"
)

var _ ToolCache = (*mcpclient.Client)(nil)

// testLLM carries userinfo, a path and a query on purpose: the card must keep
// only the scheme and host. The URL is assembled rather than written out so the
// fixture does not read as a leaked credential.
var testLLM = config.LLMConfig{
	BaseURL: (&url.URL{
		Scheme:   "https",
		User:     url.UserPassword("user", "pass"),
		Host:     "llm.example:8443",
		Path:     "/v1/proxy",
		RawQuery: "key=abc",
	}).String(),
	APIKey: "sk-secret",
	Model:  "glm-4.7-flash",
}

// newRegistry builds the AI-App tool registry with the dry-run guard on.
func newRegistry() *tools.Registry {
	cfg := config.TumblebugConfig{DryRun: true}
	apps := catalog.NewStore()
	registry := tools.NewRegistry(cfg.DryRun)
	tools.Register(registry, apps, deploy.NewService(tumblebug.NewClient(cfg), apps, cfg))
	return registry
}

// connectedClient returns an MCP client with a live session to registry.
func connectedClient(t *testing.T, registry *tools.Registry) *mcpclient.Client {
	t.Helper()
	srv := httptest.NewServer(mcpserver.NewHandler(registry))
	t.Cleanup(srv.Close)

	client := mcpclient.New(srv.URL, "")
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Prepare(context.Background()); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	return client
}

// fakeRegistry records every card posted to /agents and answers with status.
type fakeRegistry struct {
	status int
	reply  string

	mu     sync.Mutex
	paths  []string
	auths  []string
	bodies [][]byte
}

func (f *fakeRegistry) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.paths = append(f.paths, r.Method+" "+r.URL.Path)
	f.auths = append(f.auths, r.Header.Get("Authorization"))
	f.bodies = append(f.bodies, body)
	f.mu.Unlock()
	w.WriteHeader(f.status)
	_, _ = io.WriteString(w, f.reply)
}

func (f *fakeRegistry) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.bodies)
}

func TestCardWhenConnected(t *testing.T) {
	registry := newRegistry()
	client := connectedClient(t, registry)

	card := New(client, testLLM, nil).Build()

	if card.Name != Name || card.DisplayName != DisplayName || card.Version != Version || card.Owner != Owner ||
		card.Framework != Framework || card.ServesFramework != ServesFramework || card.IntentEndpoint != IntentEndpoint {
		t.Errorf("identity = %+v", card)
	}
	if len(card.MCPServers) != 1 || card.MCPServers[0].URL != client.Endpoint() ||
		!card.MCPServers[0].Connected || card.MCPServers[0].ToolCount != 14 {
		t.Errorf("mcpServers = %+v", card.MCPServers)
	}
	if len(card.Tools) != 14 {
		t.Fatalf("tools = %d, want 14", len(card.Tools))
	}
	want := map[string]model.ToolDescriptor{}
	for _, d := range registry.Descriptors() {
		want[d.Name] = d
	}
	for _, tool := range card.Tools {
		d, ok := want[tool.Name]
		if !ok {
			t.Errorf("unexpected tool %q", tool.Name)
			continue
		}
		if tool.Grade != d.Grade || tool.GuardedByDryRun != d.GuardedByDryRun {
			t.Errorf("%s = %s/%v, want %s/%v", tool.Name, tool.Grade, tool.GuardedByDryRun, d.Grade, d.GuardedByDryRun)
		}
	}
	if !card.DryRun {
		t.Errorf("dryRun = false, want the AI-App setting true")
	}
	if card.LLM.BaseURL != "https://llm.example:8443" || card.LLM.Model != "glm-4.7-flash" ||
		strings.Join(card.LLM.Requires, ",") != "tool_use,anthropic-messages" {
		t.Errorf("llm = %+v", card.LLM)
	}
	raw, _ := json.Marshal(card)
	for _, secret := range []string{"sk-secret", "pass", "key=abc", "/v1/proxy"} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("card carries %q: %s", secret, raw)
		}
	}
	if card.Registration == nil || card.Registration.Status != StatusDisabled {
		t.Errorf("registration = %+v, want disabled", card.Registration)
	}
}

func TestCardWhenNotConnected(t *testing.T) {
	client := mcpclient.New("http://127.0.0.1:1/aiapp/mcp", "")

	card := New(client, testLLM, nil).Build()

	if len(card.MCPServers) != 1 || card.MCPServers[0].Connected || card.MCPServers[0].ToolCount != 0 {
		t.Errorf("mcpServers = %+v", card.MCPServers)
	}
	raw, _ := json.Marshal(card)
	if !strings.Contains(string(raw), `"tools":[]`) {
		t.Errorf("tools is not an empty array: %s", raw)
	}
}

func TestRegisterPostsCardWithToken(t *testing.T) {
	fake := &fakeRegistry{status: http.StatusCreated, reply: `{"id":"agent-1"}`}
	srv := httptest.NewServer(fake)
	defer srv.Close()

	client := connectedClient(t, newRegistry())
	registrar := NewRegistrar(srv.URL+"/", "reg-token")
	card := New(client, testLLM, registrar)
	if got := card.Build().Registration.Status; got != StatusPending {
		t.Errorf("status before registering = %q, want pending", got)
	}

	registrar.Register(context.Background(), card)

	if fake.calls() != 1 || fake.paths[0] != "POST /agents" || fake.auths[0] != "Bearer reg-token" {
		t.Fatalf("registry saw %v with %v", fake.paths, fake.auths)
	}
	var sent model.AgentCard
	if err := json.Unmarshal(fake.bodies[0], &sent); err != nil {
		t.Fatalf("decode sent card: %v", err)
	}
	if sent.Name != Name || len(sent.Tools) != 14 || !sent.MCPServers[0].Connected || sent.Registration != nil {
		t.Errorf("sent card = %+v", sent)
	}

	state := card.Build().Registration
	if state.Status != StatusRegistered || state.RegistryURL != srv.URL || state.At == nil ||
		!strings.Contains(state.Message, "201") {
		t.Errorf("registration = %+v", state)
	}
}

func TestRegisterFailsAfterThreeAttempts(t *testing.T) {
	fake := &fakeRegistry{status: http.StatusServiceUnavailable, reply: "registry is down"}
	srv := httptest.NewServer(fake)
	defer srv.Close()

	registrar := NewRegistrar(srv.URL, "")
	registrar.interval = time.Millisecond
	card := New(mcpclient.New("http://127.0.0.1:1/aiapp/mcp", ""), testLLM, registrar)

	registrar.Register(context.Background(), card)

	if fake.calls() != registerAttempts || registerAttempts != 3 {
		t.Errorf("registry called %d times, want 3", fake.calls())
	}
	if fake.auths[0] != "" {
		t.Errorf("Authorization = %q without a token", fake.auths[0])
	}
	state := card.Build().Registration
	if state.Status != StatusFailed || state.At == nil ||
		!strings.Contains(state.Message, "503") || !strings.Contains(state.Message, "registry is down") {
		t.Errorf("registration = %+v", state)
	}
}

func TestRegistryNotSetIsDisabled(t *testing.T) {
	registrar := NewRegistrar("", "")
	card := New(mcpclient.New("http://127.0.0.1:1/aiapp/mcp", ""), testLLM, registrar)

	registrar.Register(context.Background(), card)

	if registrar.Enabled() {
		t.Errorf("Enabled = true without a registry URL")
	}
	if state := card.Build().Registration; state.Status != StatusDisabled || state.At != nil {
		t.Errorf("registration = %+v, want disabled", state)
	}
}

// countingClient counts the connection attempts made through it.
type countingClient struct {
	*mcpclient.Client
	prepares atomic.Int32
}

func (c *countingClient) Prepare(ctx context.Context) error {
	c.prepares.Add(1)
	return c.Client.Prepare(ctx)
}

func TestRegisterWaitsForLateMCPServer(t *testing.T) {
	// The AI-App MCP server answers 503 during the first two connection
	// attempts, as one that is still starting does.
	client := &countingClient{}
	mcpHandler := mcpserver.NewHandler(newRegistry())
	mcpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if client.prepares.Load() < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		mcpHandler.ServeHTTP(w, r)
	}))
	defer mcpSrv.Close()
	client.Client = mcpclient.New(mcpSrv.URL, "")
	defer func() { _ = client.Close() }()

	fake := &fakeRegistry{status: http.StatusCreated}
	srv := httptest.NewServer(fake)
	defer srv.Close()

	registrar := NewRegistrar(srv.URL, "")
	registrar.interval = time.Millisecond
	card := New(client, testLLM, registrar)

	registrar.Register(context.Background(), card)

	if got := client.prepares.Load(); got != 3 {
		t.Errorf("connection attempts = %d, want 3", got)
	}
	if fake.calls() != 1 {
		t.Fatalf("registry called %d times, want 1", fake.calls())
	}
	var sent model.AgentCard
	if err := json.Unmarshal(fake.bodies[0], &sent); err != nil {
		t.Fatalf("decode sent card: %v", err)
	}
	if !sent.MCPServers[0].Connected || sent.MCPServers[0].ToolCount != 14 || len(sent.Tools) != 14 {
		t.Errorf("sent card mcpServers = %+v, tools = %d", sent.MCPServers, len(sent.Tools))
	}
}

func TestRegisterWithoutMCPServerSendsUnconnectedCard(t *testing.T) {
	fake := &fakeRegistry{status: http.StatusCreated}
	srv := httptest.NewServer(fake)
	defer srv.Close()

	client := &countingClient{Client: mcpclient.New("http://127.0.0.1:1/aiapp/mcp", "")}
	registrar := NewRegistrar(srv.URL, "")
	registrar.interval = time.Millisecond
	card := New(client, testLLM, registrar)

	registrar.Register(context.Background(), card)

	if got := client.prepares.Load(); got != connectAttempts || connectAttempts != 3 {
		t.Errorf("connection attempts = %d, want 3", got)
	}
	var sent model.AgentCard
	if fake.calls() != 1 || json.Unmarshal(fake.bodies[0], &sent) != nil {
		t.Fatalf("registry called %d times", fake.calls())
	}
	if sent.MCPServers[0].Connected || len(sent.Tools) != 0 {
		t.Errorf("sent card = %+v", sent)
	}
	if state := card.Build().Registration; state.Status != StatusRegistered {
		t.Errorf("registration = %+v", state)
	}
}
