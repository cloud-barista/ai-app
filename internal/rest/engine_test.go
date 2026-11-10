package rest

import (
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
