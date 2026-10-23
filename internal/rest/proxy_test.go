package rest

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPartnerNLProxyKeepsPathAndReports502(t *testing.T) {
	var gotPath, gotQuery, gotAuth string
	agentSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery, gotAuth = r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))

	proxy := newPartnerNLProxy(agentSrv.URL)
	for _, path := range []string{"/aiapp/partner/nl/runs", "/aiapp/partner/nl/ns/default/intents"} {
		req := httptest.NewRequest(http.MethodGet, path+"?limit=5", nil)
		req.Header.Set("Authorization", "Bearer t")
		rec := httptest.NewRecorder()
		proxy.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || gotPath != path || gotQuery != "limit=5" || gotAuth != "Bearer t" {
			t.Errorf("forwarded %d path=%q query=%q auth=%q, want path %q", rec.Code, gotPath, gotQuery, gotAuth, path)
		}
	}

	agentSrv.Close()
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/aiapp/partner/nl/readyz", nil))
	if rec.Code != http.StatusBadGateway {
		t.Errorf("status with the agent down = %d, want 502", rec.Code)
	}
}

func TestOldAgentRoutesAreGone(t *testing.T) {
	s := newTestServers(t, "")
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/aiapp/ns/default/intents"},
		{http.MethodGet, "/aiapp/agent/card"},
		{http.MethodGet, "/aiapp/agent/archive"},
	} {
		if rec := do(s, tc.method, tc.path, ""); rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s = %d, want it no longer routed", tc.method, tc.path, rec.Code)
		}
	}
}
