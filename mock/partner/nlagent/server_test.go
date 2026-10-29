package nlagent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ish-hcc/ai-agent-proto/internal/archive"
	"github.com/ish-hcc/ai-agent-proto/internal/engine"
	"github.com/ish-hcc/ai-agent-proto/internal/mcpclient"
	"github.com/ish-hcc/ai-agent-proto/internal/mcpserver"
	"github.com/ish-hcc/ai-agent-proto/internal/tools"
	"github.com/ish-hcc/ai-agent-proto/mock/partner/nlagent/agent"
	"github.com/ish-hcc/ai-agent-proto/mock/partner/nlagent/card"
	"github.com/ish-hcc/ai-agent-proto/mock/partner/nlagent/llm"
)

var _ agent.ToolSource = (*mcpclient.Client)(nil)
var _ agent.Preparer = (*mcpclient.Client)(nil)
var _ card.ToolCache = (*mcpclient.Client)(nil)

const testToken = "s3cret"

// fakeEngine serves the engine tool names over MCP with the dry-run gate on.
// deploy_application answers with a blocked engine run.
func fakeEngine(t *testing.T, gotNS *[]string) string {
	t.Helper()
	var mu sync.Mutex
	record := func(nsID string) {
		mu.Lock()
		defer mu.Unlock()
		*gotNS = append(*gotNS, nsID)
	}
	registry := tools.NewRegistry(true)
	registry.Add(tools.Tool{
		Name: engine.ToolApplications, Grade: tools.GradeRead, Description: "catalog lookup",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
		Handle: func(_ context.Context, nsID string, _ json.RawMessage) (any, error) {
			record(nsID)
			return engine.Catalog{Models: []engine.CatalogModel{{ID: "qwen3-vl-8b", Artifacts: []engine.CatalogArtifact{
				{ID: "ollama-gguf", Source: "ollama", Ref: "qwen3-vl:8b", Format: "gguf", Engines: []string{"ollama"}}}}},
				Apps: []engine.CatalogApp{{Name: "open-webui", Versions: []int{1}, Latest: 1}}}, nil
		},
	})
	registry.Add(tools.Tool{
		Name: engine.ToolDeploy, Grade: tools.GradeWrite, Description: "deploy workflow",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
		Handle: func(_ context.Context, nsID string, _ json.RawMessage) (any, error) {
			record(nsID)
			return engine.Run{RunID: "eng-1", Workflow: engine.WorkflowDeploy, Namespace: nsID,
				Status: engine.RunBlocked, DryRun: true, Steps: []engine.Step{}}, nil
		},
	})
	srv := httptest.NewServer(mcpserver.NewNamedHandler(engine.MCPServerName, registry))
	t.Cleanup(srv.Close)
	return srv.URL
}

// fakeLLM answers the Messages API with a lookup, a deployment and an answer,
// in that order, and keeps every request.
type fakeLLM struct {
	mu       sync.Mutex
	requests []llm.Request
}

func (f *fakeLLM) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req llm.Request
	_ = json.NewDecoder(r.Body).Decode(&req)
	f.mu.Lock()
	f.requests = append(f.requests, req)
	turn := len(f.requests)
	f.mu.Unlock()

	resp := llm.Response{Role: llm.RoleAssistant, StopReason: llm.StopReasonToolUse,
		Usage: llm.Usage{InputTokens: 10, OutputTokens: 2}}
	switch turn {
	case 1:
		resp.Content = []llm.Block{{Type: llm.BlockToolUse, ID: "t1", Name: engine.ToolApplications,
			Input: json.RawMessage(`{"modality":"image"}`)}}
	case 2:
		resp.Content = []llm.Block{{Type: llm.BlockToolUse, ID: "t2", Name: engine.ToolDeploy,
			Input: json.RawMessage(`{"type":"model-service","targets":[{"kind":"node","nsId":"etri","infraId":"travel",` +
				`"nodeId":"vm2"}],"modelService":{"modelId":"qwen3-vl-8b","artifactId":"ollama-gguf"}}`)}}
	default:
		resp.StopReason = "end_turn"
		resp.Content = []llm.Block{llm.TextBlock("Planned, blocked by dry run.")}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func newMock(t *testing.T) (*httptest.Server, *fakeLLM, *[]string) {
	t.Helper()
	gotNS := &[]string{}
	engineURL := fakeEngine(t, gotNS)

	model := &fakeLLM{}
	llmSrv := httptest.NewServer(model)
	t.Cleanup(llmSrv.Close)
	llmCfg := llm.Config{BaseURL: llmSrv.URL, APIKey: "k", Model: "test-model", MaxTokens: 100, Timeout: 0}

	client := mcpclient.NewNamed("test", engineURL, "")
	t.Cleanup(func() { _ = client.Close() })
	runs, err := archive.NewLog[agent.Run](t.TempDir())
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	service := agent.NewService(llm.NewClient(llmCfg), client, runs, 5)
	handler, e := NewHandler(service, card.New(client, llmCfg, nil), testToken)
	handler.MarkReady()
	srv := httptest.NewServer(e)
	t.Cleanup(srv.Close)
	return srv, model, gotNS
}

func call(t *testing.T, srv *httptest.Server, method, path, body, token string) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	var raw json.RawMessage
	_ = json.NewDecoder(resp.Body).Decode(&raw)
	return resp, raw
}

func TestIntentCallsEngineToolsAndMarksTheMock(t *testing.T) {
	srv, model, gotNS := newMock(t)

	resp, body := call(t, srv, http.MethodPost, Prefix+"/ns/team-a/intents", `{"intent":"Open WebUI 챗봇을 띄워줘"}`, testToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("intent = %d: %s", resp.StatusCode, body)
	}
	var out agent.IntentResp
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.HasPrefix(out.Answer, agent.Marker+" ") || out.Source != agent.Source || !out.DryRun {
		t.Errorf("answer = %q, source = %q, dryRun = %v", out.Answer, out.Source, out.DryRun)
	}
	if len(out.Steps) != 2 || out.Steps[0].Tool != engine.ToolApplications || !out.Steps[0].Executed ||
		out.Steps[1].Tool != engine.ToolDeploy || out.Steps[1].Executed || out.Steps[1].Grade != "write" ||
		out.Steps[1].EngineRunID != "eng-1" || out.Steps[1].EngineStatus != string(engine.RunBlocked) {
		t.Errorf("steps = %+v", out.Steps)
	}
	for _, ns := range *gotNS {
		if ns != "team-a" {
			t.Errorf("engine saw namespace %q, want team-a", ns)
		}
	}

	first := model.requests[0]
	names := map[string]bool{}
	for _, tool := range first.Tools {
		names[tool.Name] = true
	}
	if !names[engine.ToolDeploy] || !names[engine.ToolApplications] || len(first.Tools) != 2 {
		t.Errorf("model was offered %v", names)
	}
	if !strings.Contains(first.System, "team-a") || !strings.Contains(first.System, engine.ToolApplications) ||
		!strings.Contains(first.System, "dry-run gate is on") {
		t.Errorf("system prompt misses the namespace, the catalog tool or the gate: %s", first.System)
	}

	resp, body = call(t, srv, http.MethodGet, Prefix+"/runs", "", testToken)
	var list agent.RunListResp
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &list) != nil || len(list.Runs) != 1 ||
		list.Runs[0].RunID != out.RunID || list.Runs[0].Source != agent.Source || list.Runs[0].Kind != "intent" ||
		!strings.HasPrefix(list.Runs[0].Answer, agent.Marker) {
		t.Fatalf("runs = %d: %s", resp.StatusCode, body)
	}
	if resp, body = call(t, srv, http.MethodGet, Prefix+"/runs/"+out.RunID, "", testToken); resp.StatusCode != http.StatusOK {
		t.Errorf("run = %d: %s", resp.StatusCode, body)
	}
	if resp, _ = call(t, srv, http.MethodGet, Prefix+"/runs/nl-missing", "", testToken); resp.StatusCode != http.StatusNotFound {
		t.Errorf("missing run = %d, want 404", resp.StatusCode)
	}

	resp, body = call(t, srv, http.MethodGet, Prefix+"/card", "", testToken)
	var c card.AgentCard
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &c) != nil || !c.Mock ||
		c.IntentEndpoint != card.IntentEndpoint || len(c.Tools) != 2 || !c.MCPServers[0].Connected {
		t.Errorf("card = %d: %s", resp.StatusCode, body)
	}
}

func TestTokenAndReadiness(t *testing.T) {
	srv, _, _ := newMock(t)

	if resp, _ := call(t, srv, http.MethodGet, ReadyPath, "", ""); resp.StatusCode != http.StatusOK {
		t.Errorf("readyz without a token = %d, want 200", resp.StatusCode)
	}
	for _, path := range []string{Prefix + "/runs", Prefix + "/card"} {
		if resp, _ := call(t, srv, http.MethodGet, path, "", ""); resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("GET %s without a token = %d, want 401", path, resp.StatusCode)
		}
	}
	if resp, _ := call(t, srv, http.MethodPost, Prefix+"/ns/default/intents", `{"intent":""}`, testToken); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("empty intent = %d, want 400", resp.StatusCode)
	}
}

func TestEngineDownReports503(t *testing.T) {
	llmCfg := llm.Config{BaseURL: "http://127.0.0.1:1", APIKey: "k", Model: "m", MaxTokens: 1}
	client := mcpclient.NewNamed("test", "http://127.0.0.1:1/aiapp/engine/mcp", "")
	runs, err := archive.NewLog[agent.Run](t.TempDir())
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	_, e := NewHandler(agent.NewService(llm.NewClient(llmCfg), client, runs, 1), card.New(client, llmCfg, nil), "")
	srv := httptest.NewServer(e)
	defer srv.Close()

	resp, body := call(t, srv, http.MethodPost, Prefix+"/ns/default/intents", `{"intent":"x"}`, "")
	if resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(string(body), "tool server") ||
		!strings.Contains(string(body), agent.Marker) {
		t.Errorf("engine down = %d: %s", resp.StatusCode, body)
	}
}
