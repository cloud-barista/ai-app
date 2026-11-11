package infrarec

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ish-hcc/ai-agent-proto/internal/config"
	"github.com/ish-hcc/ai-agent-proto/internal/tumblebug"
)

// gpuApp is a model service that needs one GPU with at least 22 GiB.
func gpuApp() *App {
	return &App{
		ID:          "vllm-llama31-8b",
		Accelerator: AcceleratorRequirement{Type: "gpu", MinCount: 1, MinMemoryGiB: 22},
		Resources:   ResourceRequirement{MinVCPU: 8, MinMemoryGiB: 32},
	}
}

// cpuApp is an application that needs no accelerator.
func cpuApp() *App {
	return &App{
		ID:        "open-webui-llama31",
		Resources: ResourceRequirement{MinVCPU: 2, MinMemoryGiB: 4},
	}
}

func TestSpecRequestWithoutAcceleratorFiltersOnlyCPUAndMemory(t *testing.T) {
	req := buildSpecRequest(cpuApp(), 5)
	metrics := map[string]string{}
	for _, cond := range req.Filter.Policy {
		metrics[cond.Metric] = cond.Condition[0].Operand
	}
	for _, metric := range []string{metricAcceleratorType, metricAcceleratorCount, metricAcceleratorModel, metricAcceleratorMemory} {
		if _, ok := metrics[metric]; ok {
			t.Errorf("filter carries %s for an application without an accelerator", metric)
		}
	}
	if metrics[metricVCPU] != "2" || metrics[metricMemoryGiB] != "4" || len(metrics) != 2 {
		t.Errorf("filter = %v, want vCPU>=2 and memoryGiB>=4 only", metrics)
	}

	if buildSpecRequest(gpuApp(), 5).Filter.Policy[0].Metric != metricAcceleratorType {
		t.Error("a model service lost its accelerator type filter")
	}
}

// fakeTumblebug answers the spec recommendation, spec read and image search
// calls of CB-Tumblebug with fixed catalogs.
func fakeTumblebug(t *testing.T, specs []catalogSpec, images []tumblebug.ImageInfo) *tumblebug.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/recommendSpec":
			_ = json.NewEncoder(w).Encode(specs)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/ns/system/resources/spec/"):
			id := strings.TrimPrefix(r.URL.Path, "/ns/system/resources/spec/")
			for _, spec := range specs {
				if spec.ID == id {
					_ = json.NewEncoder(w).Encode(spec)
					return
				}
			}
			http.Error(w, `{"message":"no spec"}`, http.StatusNotFound)
		case r.Method == http.MethodPost && r.URL.Path == "/ns/system/resources/searchImage":
			_ = json.NewEncoder(w).Encode(tumblebug.SearchImageResp{ImageCount: len(images), ImageList: images})
		default:
			http.Error(w, `{"message":"unexpected call"}`, http.StatusInternalServerError)
		}
	}))
	t.Cleanup(srv.Close)
	return tumblebug.NewClient(config.TumblebugConfig{BaseURL: srv.URL, Timeout: 5 * time.Second})
}

func testRecommender(t *testing.T, images []tumblebug.ImageInfo) *Recommender {
	t.Helper()
	specs := []catalogSpec{
		{SpecInfo: tumblebug.SpecInfo{ID: "aws+us-east-1+g6.2xlarge", ProviderName: "aws", RegionName: "us-east-1", AcceleratorType: "gpu",
			AcceleratorModel: "NVIDIA L4", AcceleratorCount: 1, AcceleratorMemoryGB: 24, VCPU: 8, MemoryGiB: 32, CostPerHour: 0.98}},
		{SpecInfo: tumblebug.SpecInfo{ID: "azure+koreacentral+amd-gpu", AcceleratorType: "gpu", AcceleratorModel: "AMD Instinct", AcceleratorCount: 1,
			AcceleratorMemoryGB: 32, VCPU: 8, MemoryGiB: 32, CostPerHour: 1.2}},
	}
	cfg := config.TumblebugConfig{ImageNamespace: "system", SpecNamespace: "system", DefaultOSType: "ubuntu 22.04"}
	return New(fakeTumblebug(t, specs, images), cfg)
}

func post(t *testing.T, handler http.Handler, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(encoded))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestSpecsContract(t *testing.T) {
	handler := NewHandler(testRecommender(t, nil), "")
	rec := post(t, handler, "/recommendations/specs", "", map[string]any{"app": gpuApp(), "limit": 2})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	var resp specsResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Candidates) != 2 || resp.Candidates[0].ID != "aws+us-east-1+g6.2xlarge" ||
		resp.Candidates[0].AcceleratorMemoryGB != 24 || resp.Candidates[0].CostPerHour != 0.98 ||
		resp.Candidates[0].GPUVendorStatus != "Compatible" || resp.Candidates[1].GPUVendorStatus != "Unsupported" {
		t.Errorf("candidates = %+v", resp.Candidates)
	}
	if !strings.Contains(resp.Reason, "acceleratorType=gpu") {
		t.Errorf("reason = %q, want the accelerator filter", resp.Reason)
	}
	if !strings.Contains(resp.Reason, "requires NVIDIA (1 compatible, 1 unsupported, 0 unverified)") {
		t.Errorf("missing runtime vendor compatibility: %s", rec.Body)
	}
}

func TestImageContractMatchesVendor(t *testing.T) {
	images := []tumblebug.ImageInfo{
		{ID: "plain-ubuntu", OSType: "ubuntu 22.04"},
		{ID: "amd-rocm", OSType: "ubuntu 22.04", IsGPUImage: true, OSDistribution: "AMD ROCm GPU image"},
		{ID: "nvidia-dlami", OSType: "ubuntu 22.04", IsGPUImage: true, OSDistribution: "Deep Learning Nvidia Driver GPU AMI"},
	}
	handler := NewHandler(testRecommender(t, images), "")

	for _, tc := range []struct {
		specID string
		want   string
		status string
	}{
		{"aws+us-east-1+g6.2xlarge", "nvidia-dlami", "Compatible"},
		{"azure+koreacentral+amd-gpu", "amd-rocm", "Unsupported"},
	} {
		rec := post(t, handler, "/recommendations/image", "", map[string]any{"app": gpuApp(), "specId": tc.specID})
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, body %s", tc.specID, rec.Code, rec.Body)
		}
		var resp imageResp
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		if resp.ImageID != tc.want || resp.Source != "cb-tumblebug:system" || resp.DriverStatus != "matched" {
			t.Errorf("%s: image = %+v, want %s", tc.specID, resp, tc.want)
		}
		if !strings.HasPrefix(resp.Reason, "accelerator image") {
			t.Errorf("%s: reason = %q", tc.specID, resp.Reason)
		}
		if resp.GPUVendorStatus != tc.status ||
			(tc.status == "Unsupported" && !strings.Contains(resp.Reason, "requires NVIDIA")) {
			t.Errorf("%s: missing runtime vendor compatibility: %s", tc.specID, rec.Body)
		}
	}
}

func TestImageFallsBackToPlainImageOnVendorMismatch(t *testing.T) {
	images := []tumblebug.ImageInfo{
		{ID: "plain-ubuntu", OSType: "ubuntu 22.04"},
		{ID: "amd-rocm", OSType: "ubuntu 22.04", IsGPUImage: true, OSDistribution: "AMD ROCm GPU image"},
	}
	choice, err := testRecommender(t, images).ResolveImage(context.Background(), gpuApp(), "aws+us-east-1+g6.2xlarge")
	if err != nil {
		t.Fatal(err)
	}
	if choice.ID != "plain-ubuntu" || choice.Vendor != "nvidia" || choice.DriverStatus != "review_required" ||
		!strings.Contains(choice.Reason, "no nvidia accelerator image") {
		t.Errorf("choice = %+v", choice)
	}
}

func TestBadRequestsAndToken(t *testing.T) {
	handler := NewHandler(testRecommender(t, nil), "s3cret")

	if rec := post(t, handler, "/recommendations/specs", "", map[string]any{"app": gpuApp()}); rec.Code != http.StatusUnauthorized {
		t.Errorf("no token: status = %d, want 401", rec.Code)
	}
	if rec := post(t, handler, "/recommendations/specs", "wrong", map[string]any{"app": gpuApp()}); rec.Code != http.StatusUnauthorized {
		t.Errorf("wrong token: status = %d, want 401", rec.Code)
	}
	if rec := post(t, handler, "/recommendations/specs", "s3cret", map[string]any{"limit": 1}); rec.Code != http.StatusBadRequest {
		t.Errorf("no app: status = %d, want 400", rec.Code)
	}
	if rec := post(t, handler, "/recommendations/image", "s3cret", map[string]any{"app": gpuApp()}); rec.Code != http.StatusBadRequest {
		t.Errorf("no specId: status = %d, want 400", rec.Code)
	}
	if rec := post(t, handler, "/recommendations/specs", "s3cret", map[string]any{"app": gpuApp()}); rec.Code != http.StatusOK {
		t.Errorf("right token: status = %d, body %s", rec.Code, rec.Body)
	}
}

func TestProfileRecommendationFiltersAndRanksCatalogResults(t *testing.T) {
	expired := time.Now().Add(-time.Minute)
	cfg := config.TumblebugConfig{ImageNamespace: "system", SpecNamespace: "system"}
	specs := []catalogSpec{
		{SpecInfo: tumblebug.SpecInfo{ID: "low-vram", AcceleratorType: "gpu", AcceleratorCount: 2, AcceleratorMemoryGB: 12,
			VCPU: 8, MemoryGiB: 32, CostPerHour: .4}, AvailabilityStatus: "Available"},
		{SpecInfo: tumblebug.SpecInfo{ID: "sold-out", AcceleratorType: "gpu", AcceleratorCount: 2, AcceleratorMemoryGB: 24,
			VCPU: 8, MemoryGiB: 32, CostPerHour: .5}, AvailabilityStatus: "SoldOut"},
		{SpecInfo: tumblebug.SpecInfo{ID: "stale", AcceleratorType: "gpu", AcceleratorCount: 2, AcceleratorMemoryGB: 24,
			VCPU: 8, MemoryGiB: 32, CostPerHour: .6}, AvailabilityStatus: "Available", AvailabilityRefreshAfter: &expired},
		{SpecInfo: tumblebug.SpecInfo{ID: "ready", AcceleratorType: "gpu", AcceleratorCount: 2, AcceleratorMemoryGB: 24,
			VCPU: 8, MemoryGiB: 32, CostPerHour: .7}, AvailabilityStatus: "Available"},
		{SpecInfo: tumblebug.SpecInfo{ID: "unknown-price", AcceleratorType: "gpu", AcceleratorCount: 2, AcceleratorMemoryGB: 24,
			VCPU: 8, MemoryGiB: 32, CostPerHour: -1}},
	}
	handler := NewHandler(New(fakeTumblebug(t, specs, nil), cfg), "")
	body := json.RawMessage(`{"profile":{"app_id":"serve",
		"workload":{"expected_rps":5},
		"requirements":{
			"compute":{"cpu_cores_min":4,"memory_mib_min":24576,"storage_gib_min":60},
			"accelerator":{"required":true,"type":" GPU ","count_min":2,"memory_mib_min_per_device":20480},
			"slo":{"latency_p95_ms_max":1000,"throughput_rps_min":4},
			"cost":{"currency":"USD","cost_per_hour_max":1}
		}}}`)
	rec := post(t, handler, "/recommendations/specs", "", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	var got specsResp
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Candidates) != 3 || got.Candidates[0].ID != "ready" || got.Candidates[1].ID != "stale" ||
		got.Candidates[1].AvailabilityStatus != "Unverified" || got.Candidates[2].AvailabilityStatus != "SoldOut" {
		t.Errorf("candidates = %+v", got.Candidates)
	}
	for _, term := range []string{"acceleratorMemoryGB>=20", "memoryGiB>=24", "costPerHour<=1", "60 GiB", "SLO is unverified",
		"requires NVIDIA (0 compatible, 0 unsupported, 3 unverified)"} {
		if !strings.Contains(got.Reason, term) {
			t.Errorf("reason %q is missing %q", got.Reason, term)
		}
	}
}

func TestCPURecommendationOmitsGPUVendorStatus(t *testing.T) {
	cfg := config.TumblebugConfig{ImageNamespace: "system", SpecNamespace: "system"}
	specs := []catalogSpec{{SpecInfo: tumblebug.SpecInfo{ID: "cpu-only", VCPU: 4}}}
	images := []tumblebug.ImageInfo{{ID: "plain-ubuntu", OSType: "ubuntu 22.04"}}
	handler := NewHandler(New(fakeTumblebug(t, specs, images), cfg), "")
	for _, path := range []string{"/recommendations/specs", "/recommendations/image"} {
		rec := post(t, handler, path, "", map[string]any{"app": App{ID: "cpu-app"}, "specId": "cpu-only"})
		if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "gpuRuntimeVendorStatus") {
			t.Errorf("%s = %d %s, want 200 without GPU vendor status", path, rec.Code, rec.Body)
		}
	}
}

func TestImageDistinguishesIncompatibleSpecImageAndUpstreamFailure(t *testing.T) {
	handler := NewHandler(testRecommender(t, []tumblebug.ImageInfo{
		{ID: "amd-rocm", IsGPUImage: true},
	}), "")
	app := gpuApp()
	for _, tc := range []struct {
		specID string
		app    App
		status int
	}{
		{"absent", *app, http.StatusNotFound},
		{"aws+us-east-1+g6.2xlarge", *app, http.StatusNotFound},
		{"aws+us-east-1+g6.2xlarge", App{ID: "larger", Accelerator: AcceleratorRequirement{Type: "gpu", MinMemoryGiB: 80}}, http.StatusConflict},
	} {
		rec := post(t, handler, "/recommendations/image", "", map[string]any{"app": tc.app, "specId": tc.specID})
		if rec.Code != tc.status {
			t.Errorf("%s for %s: status = %d, want %d, body %s", tc.specID, tc.app.ID, rec.Code, tc.status, rec.Body)
		}
	}
	for _, tc := range []struct {
		name, body     string
		upstreamStatus int
		want           int
	}{
		{"legacy missing spec", "The specKey aws+us-east-1+g6.2xlarge not found by any of ID, CspSpecName", http.StatusInternalServerError, http.StatusNotFound},
		{"database error", "spec lookup by id failed for aws+us-east-1+g6.2xlarge: connection unavailable", http.StatusInternalServerError, http.StatusBadGateway},
		{"unavailable upstream", "unavailable", http.StatusServiceUnavailable, http.StatusBadGateway},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.upstreamStatus)
				_ = json.NewEncoder(w).Encode(message{Message: tc.body})
			}))
			defer upstream.Close()
			cfg := config.TumblebugConfig{BaseURL: upstream.URL, SpecNamespace: "system"}
			handler := NewHandler(New(tumblebug.NewClient(cfg), cfg), "")
			rec := post(t, handler, "/recommendations/image", "", map[string]any{"app": app, "specId": "aws+us-east-1+g6.2xlarge"})
			if rec.Code != tc.want {
				t.Errorf("status = %d, want %d, body %s", rec.Code, tc.want, rec.Body)
			}
		})
	}
}
