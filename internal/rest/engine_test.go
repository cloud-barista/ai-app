package rest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ish-hcc/ai-agent-proto/internal/archive"
	"github.com/ish-hcc/ai-agent-proto/internal/config"
	"github.com/ish-hcc/ai-agent-proto/internal/engine"
	"github.com/ish-hcc/ai-agent-proto/internal/mcpclient"
	"github.com/ish-hcc/ai-agent-proto/internal/tools"
)

// newTestEngineServer builds an engine whose AI-App service is not reachable.
func newTestEngineServer(t *testing.T, token string) *Server {
	t.Helper()
	MarkReady()
	runs, err := archive.NewLog[engine.Run](t.TempDir())
	if err != nil {
		t.Fatalf("engine archive: %v", err)
	}
	automation := engine.New(mcpclient.New("http://127.0.0.1:1/aiapp/mcp", ""), runs)
	registry := engine.NewRegistry(automation)
	return NewEngineServer(&config.Config{Auth: config.AuthConfig{Token: token}}, EngineDependencies{
		Engine: automation,
		Card:   engine.NewCardBuilder(automation, registry, nil),
		Tools:  registry,
	})
}

func TestEngineRoutes(t *testing.T) {
	s := newTestEngineServer(t, testToken)
	bearer := "Bearer " + testToken

	cases := []struct {
		method, path, auth, body string
		want                     int
	}{
		{http.MethodGet, "/aiapp/readyz", "", "", http.StatusOK},
		{http.MethodGet, "/aiapp/engine/readyz", "", "", http.StatusOK},
		{http.MethodGet, "/aiapp/engine/card", "", "", http.StatusUnauthorized},
		{http.MethodPost, "/aiapp/engine/workflows/deploy", "", `{}`, http.StatusUnauthorized},
		{http.MethodGet, "/aiapp/engine/card", bearer, "", http.StatusOK},
		{http.MethodGet, "/aiapp/engine/runs", bearer, "", http.StatusOK},
		{http.MethodGet, "/aiapp/engine/runs/eng-missing", bearer, "", http.StatusNotFound},
		{http.MethodPost, "/aiapp/engine/workflows/rollout", bearer, `{}`, http.StatusNotFound},
		{http.MethodPost, "/aiapp/engine/workflows/deploy?ns=lab", bearer, `{"appId":"x"}`, http.StatusBadRequest},
		{http.MethodPost, "/aiapp/engine/workflows/status?ns=lab", bearer, `{"deploymentId":"x"}`, http.StatusServiceUnavailable},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		if tc.auth != "" {
			req.Header.Set("Authorization", tc.auth)
		}
		rec := httptest.NewRecorder()
		s.echo.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Errorf("%s %s auth=%t: %d %s, want %d", tc.method, tc.path, tc.auth != "", rec.Code, rec.Body.String(), tc.want)
		}
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/aiapp/engine/card", nil)
	req.Header.Set("Authorization", bearer)
	s.echo.ServeHTTP(rec, req)
	var card engine.Card
	if err := json.Unmarshal(rec.Body.Bytes(), &card); err != nil || card.UsesLLM || card.Name != engine.CardName {
		t.Errorf("card = %+v, err %v", card, err)
	}
}

func TestEngineProxyKeepsThePath(t *testing.T) {
	var gotPath, gotQuery string
	engineSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		w.WriteHeader(http.StatusOK)
	}))
	defer engineSrv.Close()

	s := NewAppServer(&config.Config{Engine: config.EngineConfig{URL: engineSrv.URL}}, AppDependencies{Tools: tools.NewRegistry(true)})
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/aiapp/engine/workflows/deploy?ns=lab", strings.NewReader(`{}`)))
	if rec.Code != http.StatusOK || gotPath != "/aiapp/engine/workflows/deploy" || gotQuery != "ns=lab" {
		t.Errorf("forwarded %d path=%q query=%q", rec.Code, gotPath, gotQuery)
	}

	engineSrv.Close()
	rec = httptest.NewRecorder()
	s.echo.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/aiapp/engine/card", nil))
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "Automation engine is not reachable") {
		t.Errorf("engine down: %d %s", rec.Code, rec.Body.String())
	}
}

func TestApprovedDeploymentMCPStagesThroughAIAppGateway(t *testing.T) {
	const approvalToken = "approval-test-only"
	ctx := context.Background()
	app, runner, controls := newNodeDeployServer(t, true)
	appHTTP := httptest.NewServer(app.echo)
	t.Cleanup(appHTTP.Close)
	appMCP := mcpclient.New(appHTTP.URL+BasePath+"/mcp", "")
	t.Cleanup(func() { _ = appMCP.Close() })

	runs, err := archive.NewLog[engine.Run](t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	automation := engine.New(appMCP, runs)
	approved, err := engine.NewApprovedDeploymentTools(t.TempDir(), automation, nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Auth:   config.AuthConfig{Token: testToken},
		Engine: config.EngineConfig{ApprovalToken: approvalToken},
	}
	engineServer := NewEngineServer(cfg, EngineDependencies{
		Engine: automation, Tools: engine.NewRegistry(automation), ApprovedDeploymentTools: approved,
	})
	engineHTTP := httptest.NewServer(engineServer.echo)
	t.Cleanup(engineHTTP.Close)
	cfg.Engine.URL = engineHTTP.URL
	gateway := NewAppServer(cfg, AppDependencies{Tools: tools.NewRegistry(true)})
	gatewayHTTP := httptest.NewServer(gateway.echo)
	t.Cleanup(gatewayHTTP.Close)

	path := BasePath + approvedDeploymentPath
	for _, server := range []*Server{gateway, engineServer} {
		if rec := do(server, http.MethodPost, path, "Bearer "+testToken); rec.Code != http.StatusUnauthorized {
			t.Fatalf("regular bearer reached approval tool, status = %d", rec.Code)
		}
		if rec := do(server, http.MethodPost, BasePath+"/engine/mcp", "Bearer "+approvalToken); rec.Code != http.StatusUnauthorized {
			t.Fatalf("approval bearer reached general engine tools, status = %d", rec.Code)
		}
	}

	client := mcpclient.New(gatewayHTTP.URL+path, approvalToken)
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	result, err := client.Call(ctx, "etri", "stage_approved_deployment", json.RawMessage(`{
		"correlationId":"approval-gateway-test","revision":1,
		"deployment":{
			"type":"inference",
			"targets":[{"kind":"node","nsId":"etri","infraId":"travel","nodeId":"vm1"}],
			"inference":{"modelId":"qwen3-vl-8b","artifactId":"ollama-gguf","runtimeId":"ollama","gpu":true}
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	var receipt struct {
		Status       string `json:"status"`
		RunID        string `json:"engineRunId"`
		DeploymentID string `json:"deploymentId"`
	}
	if err := json.Unmarshal(result.(json.RawMessage), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Status != "blocked" || receipt.RunID == "" || receipt.DeploymentID == "" {
		t.Fatalf("approval receipt = %+v", receipt)
	}
	run, err := automation.GetRun(receipt.RunID)
	if err != nil || !run.DryRun || run.Status != engine.RunBlocked {
		t.Fatalf("engine run = %+v, %v", run, err)
	}
	deploy := do(app, http.MethodGet, BasePath+"/deployments/"+receipt.DeploymentID, "")
	if deploy.Code != http.StatusOK || !strings.Contains(deploy.Body.String(), `"status":"dry-run"`) {
		t.Fatalf("dry-run deployment = %d %s", deploy.Code, deploy.Body)
	}
	if runner.count() != 0 || len(*controls) != 0 {
		t.Fatal("dry-run called a playbook or VM control")
	}
}
