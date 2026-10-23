package card

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

	"github.com/ish-hcc/ai-agent-proto/internal/engine"
	"github.com/ish-hcc/ai-agent-proto/internal/mcpclient"
	"github.com/ish-hcc/ai-agent-proto/internal/mcpserver"
	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/tools"
	"github.com/ish-hcc/ai-agent-proto/mock/partner/nlagent/llm"
)

var _ ToolCache = (*mcpclient.Client)(nil)

// testLLM carries userinfo, a path and a query on purpose: the card must keep
// only the scheme and host. The URL is assembled rather than written out so the
// fixture does not read as a leaked credential.
var testLLM = llm.Config{
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

// toolCount is how many tools newRegistry holds.
const toolCount = 5

// newRegistry builds a stand-in for the engine tool registry, with the
// engine's tool names and grades and the dry-run gate on.
func newRegistry() *tools.Registry {
	registry := tools.NewRegistry(true)
	grades := map[string]tools.Grade{
		engine.ToolDeploy:       tools.GradeWrite,
		engine.ToolHeal:         tools.GradeWrite,
		engine.ToolControl:      tools.GradeWrite,
		engine.ToolStatus:       tools.GradeRead,
		engine.ToolApplications: tools.GradeRead,
	}
	for name, grade := range grades {
		registry.Add(tools.Tool{
			Name:        name,
			Description: "stand-in " + name,
			Grade:       grade,
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
			Handle: func(context.Context, string, json.RawMessage) (any, error) {
				return map[string]string{"tool": name}, nil
			},
		})
	}
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
		!card.MCPServers[0].Connected || card.MCPServers[0].ToolCount != toolCount {
		t.Errorf("mcpServers = %+v", card.MCPServers)
	}
	if len(card.Tools) != toolCount {
		t.Fatalf("tools = %d, want %d", len(card.Tools), toolCount)
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
	if !card.DryRun || !card.Mock {
		t.Errorf("dryRun = %v, mock = %v, want both true", card.DryRun, card.Mock)
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
	client := mcpclient.New("http://127.0.0.1:1/aiapp/engine/mcp", "")

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
	var sent AgentCard
	if err := json.Unmarshal(fake.bodies[0], &sent); err != nil {
		t.Fatalf("decode sent card: %v", err)
	}
	if sent.Name != Name || len(sent.Tools) != toolCount || !sent.MCPServers[0].Connected || sent.Registration != nil {
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
	registrar.SetInterval(time.Millisecond)
	card := New(mcpclient.New("http://127.0.0.1:1/aiapp/engine/mcp", ""), testLLM, registrar)

	registrar.Register(context.Background(), card)

	if fake.calls() != 3 {
		t.Errorf("registry called %d times, want 3", fake.calls())
	}
	if fake.auths[0] != "" {
		t.Errorf("Authorization = %q without a token", fake.auths[0])
	}
	state := card.Build().Registration
	if state.Status != StatusFailed || state.At == nil || state.Message != "HTTP 503 Service Unavailable" {
		t.Errorf("registration = %+v", state)
	}
}

// secretURL returns base with user information and a query token added, the
// way a URL with credentials may be configured.
func secretURL(t *testing.T, base, user, password, query string) string {
	t.Helper()
	u, err := url.Parse(base)
	if err != nil {
		t.Fatalf("parse %q: %v", base, err)
	}
	u.User = url.UserPassword(user, password)
	u.RawQuery = query
	u.Fragment = "frag"
	return u.String()
}

func TestCardAndRegistrationCarryNoURLSecrets(t *testing.T) {
	registry := newRegistry()
	mcpSrv := httptest.NewServer(mcpserver.NewHandler(registry))
	defer mcpSrv.Close()
	mcpURL := secretURL(t, mcpSrv.URL+"/aiapp/engine/mcp", "mu", "mpw", "token=mtok")
	client := mcpclient.New(mcpURL, "")
	defer func() { _ = client.Close() }()

	fake := &fakeRegistry{
		status: http.StatusUnauthorized,
		reply:  `{"error":"bad token","echo":"Authorization: Bearer reg-token"}`,
	}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	registryURL := secretURL(t, srv.URL+"/registry", "ru", "rpw", "key=rtok")

	registrar := NewRegistrar(registryURL, "reg-token")
	registrar.SetInterval(time.Millisecond)
	card := New(client, testLLM, registrar)

	registrar.Register(context.Background(), card)

	if fake.calls() != 3 || fake.paths[0] != "POST /registry/agents" {
		t.Fatalf("registry saw %v", fake.paths)
	}
	secrets := []string{"mu:", "mpw", "mtok", "ru:", "rpw", "rtok", "frag", "reg-token", "bad token", "Bearer"}
	for _, body := range fake.bodies {
		for _, secret := range secrets {
			if strings.Contains(string(body), secret) {
				t.Errorf("registry received %q: %s", secret, body)
			}
		}
	}

	built := card.Build()
	raw, _ := json.Marshal(built)
	for _, secret := range secrets {
		if strings.Contains(string(raw), secret) {
			t.Errorf("card carries %q: %s", secret, raw)
		}
	}
	if built.MCPServers[0].URL != mcpSrv.URL+"/aiapp/engine/mcp" {
		t.Errorf("mcpServers[0].url = %q", built.MCPServers[0].URL)
	}
	state := built.Registration
	if state.RegistryURL != srv.URL+"/registry" || state.Status != StatusFailed ||
		state.Message != "HTTP 401 Unauthorized" {
		t.Errorf("registration = %+v", state)
	}
}

func TestRegistryNotSetIsDisabled(t *testing.T) {
	registrar := NewRegistrar("", "")
	card := New(mcpclient.New("http://127.0.0.1:1/aiapp/engine/mcp", ""), testLLM, registrar)

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
	// The engine MCP server answers 503 during the first two connection
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
	registrar.SetInterval(time.Millisecond)
	card := New(client, testLLM, registrar)

	registrar.Register(context.Background(), card)

	if got := client.prepares.Load(); got != 3 {
		t.Errorf("connection attempts = %d, want 3", got)
	}
	if fake.calls() != 1 {
		t.Fatalf("registry called %d times, want 1", fake.calls())
	}
	var sent AgentCard
	if err := json.Unmarshal(fake.bodies[0], &sent); err != nil {
		t.Fatalf("decode sent card: %v", err)
	}
	if !sent.MCPServers[0].Connected || sent.MCPServers[0].ToolCount != toolCount || len(sent.Tools) != toolCount {
		t.Errorf("sent card mcpServers = %+v, tools = %d", sent.MCPServers, len(sent.Tools))
	}
}

func TestRegisterWithoutMCPServerSendsUnconnectedCard(t *testing.T) {
	fake := &fakeRegistry{status: http.StatusCreated}
	srv := httptest.NewServer(fake)
	defer srv.Close()

	client := &countingClient{Client: mcpclient.New("http://127.0.0.1:1/aiapp/engine/mcp", "")}
	registrar := NewRegistrar(srv.URL, "")
	registrar.SetInterval(time.Millisecond)
	card := New(client, testLLM, registrar)

	registrar.Register(context.Background(), card)

	if got := client.prepares.Load(); got != 3 {
		t.Errorf("connection attempts = %d, want 3", got)
	}
	var sent AgentCard
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
