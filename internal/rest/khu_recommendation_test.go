package rest

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ish-hcc/ai-agent-proto/internal/config"
	"github.com/ish-hcc/ai-agent-proto/internal/infrarec"
	"github.com/ish-hcc/ai-agent-proto/internal/tools"
	"github.com/ish-hcc/ai-agent-proto/internal/tumblebug"
)

func TestKHUInfrastructureRecommendationsUse8090Contract(t *testing.T) {
	cb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		const spec = `{"id":"aws+test+g6","acceleratorType":"gpu","acceleratorModel":"NVIDIA L4","acceleratorCount":1,"acceleratorMemoryGB":24,"availabilityStatus":"Available"}`
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/recommendSpec":
			_, _ = w.Write([]byte("[" + spec + "]"))
		case r.Method == http.MethodGet && r.URL.Path == "/ns/system/resources/spec/aws+test+g6":
			_, _ = w.Write([]byte(spec))
		case r.Method == http.MethodPost && r.URL.Path == "/ns/system/resources/searchImage":
			_, _ = w.Write([]byte(`{"imageList":[{"id":"nvidia-image","isGPUImage":true}]}`))
		default:
			t.Errorf("unexpected CB-Tumblebug request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer cb.Close()

	cfg := &config.Config{
		Auth: config.AuthConfig{Token: testToken},
		Tumblebug: config.TumblebugConfig{
			BaseURL: cb.URL, Timeout: time.Second, SpecNamespace: "system", ImageNamespace: "system",
		},
	}
	recommender := infrarec.New(tumblebug.NewClient(cfg.Tumblebug), cfg.Tumblebug)
	partner := httptest.NewServer(infrarec.NewHandler(recommender, testToken))
	defer partner.Close()
	cfg.PartnerRec.URL = partner.URL
	s := NewAppServer(cfg, AppDependencies{Tools: tools.NewRegistry(true)})
	const specs = partnerRecPrefix + "/recommendations/specs"
	if rec := do(s, http.MethodPost, specs, ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("without bearer status = %d, want 401", rec.Code)
	}
	for _, tc := range []struct{ path, body, want string }{
		{specs, `{"app":{"id":"demo","accelerator":{"type":"gpu","minMemoryGiB":22}}}`, `"availabilityStatus":"Available"`},
		{partnerRecPrefix + "/recommendations/image", `{"app":{"id":"demo","accelerator":{"type":"gpu"}},"specId":"aws+test+g6"}`, `"driverStatus":"matched"`},
	} {
		req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+testToken)
		rec := httptest.NewRecorder()
		s.echo.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), tc.want) ||
			!strings.Contains(rec.Body.String(), `"gpuRuntimeVendorStatus":"Compatible"`) || strings.Contains(rec.Body.String(), "[partner mock]") {
			t.Errorf("%s = %d %s, want 200 with %s and a compatible GPU vendor", tc.path, rec.Code, rec.Body, tc.want)
		}
	}
}
