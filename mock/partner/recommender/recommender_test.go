package recommender

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
func fakeTumblebug(t *testing.T, specs []tumblebug.SpecInfo, images []tumblebug.ImageInfo) *tumblebug.Client {
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
	specs := []tumblebug.SpecInfo{
		{ID: "aws+us-east-1+g6.2xlarge", ProviderName: "aws", RegionName: "us-east-1", AcceleratorType: "gpu",
			AcceleratorModel: "NVIDIA L4", AcceleratorCount: 1, AcceleratorMemoryGB: 24, VCPU: 8, MemoryGiB: 32, CostPerHour: 0.98},
		{ID: "aws+us-east-1+g4ad.xlarge", AcceleratorType: "gpu", AcceleratorModel: "AMD Radeon Pro V520", AcceleratorCount: 1},
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
		resp.Candidates[0].AcceleratorMemoryGB != 24 || resp.Candidates[0].CostPerHour != 0.98 {
		t.Errorf("candidates = %+v", resp.Candidates)
	}
	if !strings.HasPrefix(resp.Reason, ReasonPrefix) || !strings.Contains(resp.Reason, "acceleratorType=gpu") {
		t.Errorf("reason = %q, want the mock prefix and the filter", resp.Reason)
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
	}{
		{"aws+us-east-1+g6.2xlarge", "nvidia-dlami"},
		{"aws+us-east-1+g4ad.xlarge", "amd-rocm"},
	} {
		rec := post(t, handler, "/recommendations/image", "", map[string]any{"app": gpuApp(), "specId": tc.specID})
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, body %s", tc.specID, rec.Code, rec.Body)
		}
		var resp imageResp
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		if resp.ImageID != tc.want || resp.Source != "cb-tumblebug:system" {
			t.Errorf("%s: image = %+v, want %s", tc.specID, resp, tc.want)
		}
		if !strings.HasPrefix(resp.Reason, ReasonPrefix+"accelerator image") {
			t.Errorf("%s: reason = %q", tc.specID, resp.Reason)
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
	if choice.ID != "plain-ubuntu" || choice.Vendor != "nvidia" || !strings.Contains(choice.Reason, "no nvidia accelerator image") {
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
