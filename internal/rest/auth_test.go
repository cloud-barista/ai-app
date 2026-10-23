package rest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/ish-hcc/ai-agent-proto/internal/archive"
	"github.com/ish-hcc/ai-agent-proto/internal/catalog"
	"github.com/ish-hcc/ai-agent-proto/internal/config"
	"github.com/ish-hcc/ai-agent-proto/internal/deploy"
	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/tools"
	"github.com/ish-hcc/ai-agent-proto/internal/tumblebug"
)

const testToken = "s3cret"

// newTestServers starts a stand-in for the partner natural language agent
// behind httptest and builds an AI-App server that forwards to it, both
// requiring token.
func newTestServers(t *testing.T, token string) *Server {
	t.Helper()
	MarkReady()

	partner := echo.New()
	partner.Use(RequireBearer(token, partnerNLPrefix+"/readyz"))
	partner.Any(partnerNLPrefix+"/*", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"path": c.Request().URL.Path})
	})
	agentSrv := httptest.NewServer(partner)
	t.Cleanup(agentSrv.Close)

	appArchive, err := archive.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("app archive: %v", err)
	}
	tbCfg := config.TumblebugConfig{DryRun: true}
	apps := catalog.NewStore()
	registry := tools.NewRegistry(tbCfg.DryRun)
	deployService := deploy.NewService(tumblebug.NewClient(tbCfg), apps, tbCfg)
	tools.Register(registry, apps, deployService)

	appCfg := &config.Config{
		PartnerNL: config.PartnerNLConfig{URL: agentSrv.URL},
		Auth:      config.AuthConfig{Token: token},
	}
	return NewAppServer(appCfg, AppDependencies{
		Catalog: apps,
		Deploy:  deployService,
		Archive: appArchive,
		Tools:   registry,
	})
}

func do(s *Server, method, path, auth string) *httptest.ResponseRecorder {
	var body *strings.Reader
	if method == http.MethodPost {
		body = strings.NewReader("{}")
	} else {
		body = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, body)
	req.Header.Set("Content-Type", "application/json")
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)
	return rec
}

func TestProtectedRoutesNeedToken(t *testing.T) {
	s := newTestServers(t, testToken)

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/aiapp/apps"},
		{http.MethodGet, "/aiapp/tools"},
		{http.MethodGet, "/aiapp/archive"},
		{http.MethodPost, "/aiapp/mcp"},
		{http.MethodGet, "/aiapp/partner/nl/runs"},
		{http.MethodGet, "/aiapp/partner/nl/card"},
		{http.MethodPost, "/aiapp/partner/nl/ns/default/intents"},
	} {
		for _, auth := range []string{"", "Bearer wrong", "Basic " + testToken, testToken} {
			rec := do(s, tc.method, tc.path, auth)
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("%s %s with %q = %d, want 401", tc.method, tc.path, auth, rec.Code)
				continue
			}
			if got := rec.Header().Get("WWW-Authenticate"); got != "Bearer" {
				t.Errorf("%s %s: WWW-Authenticate = %q, want Bearer", tc.method, tc.path, got)
			}
			var msg model.SimpleMsg
			if err := json.Unmarshal(rec.Body.Bytes(), &msg); err != nil || msg.Message != "Unauthorized" {
				t.Errorf("%s %s: body = %s", tc.method, tc.path, rec.Body.String())
			}
		}
	}

	for _, path := range []string{"/aiapp/apps", "/aiapp/tools", "/aiapp/archive", "/aiapp/partner/nl/runs", "/aiapp/partner/nl/card"} {
		if rec := do(s, http.MethodGet, path, "Bearer "+testToken); rec.Code != http.StatusOK {
			t.Errorf("GET %s with the token = %d, want 200: %s", path, rec.Code, rec.Body.String())
		}
	}
	if rec := do(s, http.MethodGet, "/aiapp/apps", "bearer "+testToken); rec.Code != http.StatusOK {
		t.Errorf("lower case scheme = %d, want 200", rec.Code)
	}
}

func TestOpenRoutesNeedNoToken(t *testing.T) {
	s := newTestServers(t, testToken)

	for _, path := range []string{"/aiapp/readyz", "/aiapp/ui", "/aiapp/api/index.html", "/aiapp/api/doc.json", "/aiapp/partner/nl/readyz"} {
		if rec := do(s, http.MethodGet, path, ""); rec.Code != http.StatusOK {
			t.Errorf("GET %s without a token = %d, want 200", path, rec.Code)
		}
	}
	// An encoded path is judged by the path the router matches on.
	if rec := do(s, http.MethodGet, "/aiapp/api%2F..%2Fapps", ""); rec.Code == http.StatusOK {
		t.Errorf("encoded path reached a route without a token")
	}
}

func TestNoTokenLeavesEverythingOpen(t *testing.T) {
	s := newTestServers(t, "")

	for _, path := range []string{"/aiapp/apps", "/aiapp/tools", "/aiapp/archive", "/aiapp/partner/nl/runs", "/aiapp/readyz", "/aiapp/partner/nl/readyz"} {
		if rec := do(s, http.MethodGet, path, ""); rec.Code != http.StatusOK {
			t.Errorf("GET %s with authentication off = %d, want 200", path, rec.Code)
		}
	}
	if rec := do(s, http.MethodPost, "/aiapp/mcp", ""); rec.Code == http.StatusUnauthorized {
		t.Errorf("POST /aiapp/mcp with authentication off = 401")
	}
}
