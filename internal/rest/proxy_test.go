package rest

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAgentPath(t *testing.T) {
	cases := map[string]string{
		"/aiapp/agent/archive":       "/aiapp/archive",
		"/aiapp/agent/archive/run-1": "/aiapp/archive/run-1",
		"/aiapp/agent/readyz":        "/aiapp/readyz",
		"/aiapp/agent/card":          "/aiapp/agent/card",
		"/aiapp/agent/archived":      "/aiapp/agent/archived",
		"/aiapp/ns/default/intents":  "/aiapp/ns/default/intents",
	}
	for in, want := range cases {
		if got := agentPath(in); got != want {
			t.Errorf("agentPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAgentProxyForwardsAndReports502(t *testing.T) {
	var gotPath, gotQuery, gotAuth string
	agentSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery, gotAuth = r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))

	proxy := newAgentProxy(agentSrv.URL)
	req := httptest.NewRequest(http.MethodGet, "/aiapp/agent/archive?limit=5", nil)
	req.Header.Set("Authorization", "Bearer t")
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || gotPath != "/aiapp/archive" || gotQuery != "limit=5" || gotAuth != "Bearer t" {
		t.Errorf("forwarded %d path=%q query=%q auth=%q", rec.Code, gotPath, gotQuery, gotAuth)
	}

	agentSrv.Close()
	rec = httptest.NewRecorder()
	proxy.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/aiapp/agent/readyz", nil))
	if rec.Code != http.StatusBadGateway {
		t.Errorf("status with agent down = %d, want 502", rec.Code)
	}
}
