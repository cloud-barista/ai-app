package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ish-hcc/ai-agent-proto/internal/catalog"
	"github.com/ish-hcc/ai-agent-proto/internal/config"
	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/tumblebug"
)

const testToken = "test-token"

// capturedRequest is one request the fake placement service received.
type capturedRequest struct {
	Path          string
	Authorization string
	Body          map[string]any
}

// fakePlacement answers the proposed recommender and provisioner contract with
// fixed bodies and records what it was sent.
type fakePlacement struct {
	mu       sync.Mutex
	requests []capturedRequest
	// replies maps a path to the status and body it answers with.
	replies map[string]fakeReply
}

type fakeReply struct {
	status int
	body   string
}

func newFakePlacement(t *testing.T, replies map[string]fakeReply) (*fakePlacement, *httptest.Server) {
	t.Helper()
	fake := &fakePlacement{replies: replies}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body := map[string]any{}
		_ = json.Unmarshal(raw, &body)
		fake.mu.Lock()
		fake.requests = append(fake.requests, capturedRequest{
			Path:          r.Method + " " + r.URL.Path,
			Authorization: r.Header.Get("Authorization"),
			Body:          body,
		})
		fake.mu.Unlock()

		reply, ok := fake.replies[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(reply.status)
		_, _ = io.WriteString(w, reply.body)
	}))
	t.Cleanup(srv.Close)
	return fake, srv
}

func (f *fakePlacement) request(t *testing.T, path string) capturedRequest {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, req := range f.requests {
		if req.Path == path {
			return req
		}
	}
	t.Fatalf("fake placement service never received %s; got %v", path, f.requests)
	return capturedRequest{}
}

func remoteBackend(url, token string) config.BackendConfig {
	return config.BackendConfig{Kind: config.BackendRemote, URL: url, Token: token, Timeout: 5 * time.Second}
}

func seededCatalog(t *testing.T) *catalog.Store {
	t.Helper()
	store := catalog.NewStore()
	if err := catalog.Seed(context.Background(), store); err != nil {
		t.Fatalf("seed catalog: %v", err)
	}
	return store
}

func TestRemoteRecommenderDrivesPlan(t *testing.T) {
	fake, srv := newFakePlacement(t, map[string]fakeReply{
		"/recommendations/specs": {http.StatusOK, `{"candidates":[
			{"id":"khu+gpu+a100","providerName":"khu","regionName":"seoul","acceleratorType":"gpu",
			 "acceleratorModel":"A100","acceleratorCount":1,"acceleratorMemoryGB":80,"vCPU":16,
			 "memoryGiB":128,"costPerHour":1.5},
			{"id":"khu+gpu+t4","acceleratorType":"gpu","acceleratorCount":1}],
			"reason":"cheapest with enough memory"}`},
		"/recommendations/image": {http.StatusOK,
			`{"imageId":"khu-ubuntu22-cuda12","source":"khu-catalog","reason":"ships the CUDA 12 driver"}`},
	})

	// No CB-Tumblebug client at all: planning with a remote recommender must not
	// need one.
	service := NewService(nil, seededCatalog(t), config.TumblebugConfig{DryRun: true},
		WithRecommender(NewRemoteRecommender(remoteBackend(srv.URL, testToken))))

	plan, err := service.Plan(context.Background(), "default",
		&model.DeployAppReq{AppID: "vllm-llama31-8b", InfraName: "vllm-lab-01"})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	if plan.SpecID != "khu+gpu+a100" {
		t.Errorf("SpecID = %q, want the first remote candidate", plan.SpecID)
	}
	if plan.ImageID != "khu-ubuntu22-cuda12" {
		t.Errorf("ImageID = %q, want the remote image", plan.ImageID)
	}
	if plan.SpecSource != "recommended:remote" {
		t.Errorf("SpecSource = %q, want recommended:remote", plan.SpecSource)
	}
	if plan.ImageSource != "resolved:remote" {
		t.Errorf("ImageSource = %q, want resolved:remote", plan.ImageSource)
	}
	if plan.ImageReason != "ships the CUDA 12 driver" {
		t.Errorf("ImageReason = %q", plan.ImageReason)
	}
	if plan.AcceleratorSummary != "gpu A100 x1 (80 GB)" {
		t.Errorf("AcceleratorSummary = %q", plan.AcceleratorSummary)
	}
	infraReq, ok := plan.Request.(*tumblebug.InfraDynamicReq)
	if !ok || infraReq.NodeGroups[0].SpecID != "khu+gpu+a100" || infraReq.NodeGroups[0].ImageID != "khu-ubuntu22-cuda12" {
		t.Errorf("provisioning request does not carry the remote spec and image: %+v", plan.Request)
	}

	specs := fake.request(t, "POST /recommendations/specs")
	if specs.Authorization != "Bearer "+testToken {
		t.Errorf("specs Authorization = %q", specs.Authorization)
	}
	if app, _ := specs.Body["app"].(map[string]any); app["id"] != "vllm-llama31-8b" {
		t.Errorf("specs body app = %v", specs.Body["app"])
	}
	if specs.Body["limit"] != float64(defaultSpecCandidates) {
		t.Errorf("specs body limit = %v, want %d", specs.Body["limit"], defaultSpecCandidates)
	}

	image := fake.request(t, "POST /recommendations/image")
	if image.Body["specId"] != "khu+gpu+a100" {
		t.Errorf("image body specId = %v", image.Body["specId"])
	}
	if app, _ := image.Body["app"].(map[string]any); app["id"] != "vllm-llama31-8b" {
		t.Errorf("image body app = %v", image.Body["app"])
	}
	if image.Authorization != "Bearer "+testToken {
		t.Errorf("image Authorization = %q", image.Authorization)
	}
}

func TestRemoteRecommenderPinnedSpecSkipsRecommendation(t *testing.T) {
	fake, srv := newFakePlacement(t, map[string]fakeReply{
		"/recommendations/image": {http.StatusOK, `{"imageId":"img-1","source":"s","reason":"r"}`},
	})
	service := NewService(nil, seededCatalog(t), config.TumblebugConfig{DryRun: true},
		WithRecommender(NewRemoteRecommender(remoteBackend(srv.URL, ""))))

	plan, err := service.Plan(context.Background(), "default",
		&model.DeployAppReq{AppID: "vllm-llama31-8b", InfraName: "x", SpecID: "pinned-spec"})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.SpecSource != "requested" || plan.SpecID != "pinned-spec" {
		t.Errorf("spec = %q from %q, want the pinned spec as requested", plan.SpecID, plan.SpecSource)
	}
	if len(fake.requests) != 1 {
		t.Errorf("remote calls = %d, want only the image call", len(fake.requests))
	}
	if got := fake.request(t, "POST /recommendations/image").Authorization; got != "" {
		t.Errorf("Authorization = %q, want none without a token", got)
	}
}

func testPlan() *model.DeploymentPlan {
	return &model.DeploymentPlan{
		AppID:     "vllm-llama31-8b",
		Namespace: "default",
		InfraName: "vllm-lab-01",
		SpecID:    "khu+gpu+a100",
		ImageID:   "img-1",
		NodeCount: 1,
		Request: &tumblebug.InfraDynamicReq{
			Name: "vllm-lab-01",
			NodeGroups: []tumblebug.CreateNodeGroupDynamicReq{{
				Name: "serving", NodeGroupSize: 1, SpecID: "khu+gpu+a100", ImageID: "img-1",
			}},
			PostCommands: []tumblebug.PostCommandReq{{Command: []string{"echo install"}}},
		},
	}
}

func TestRemoteProvisionerContract(t *testing.T) {
	fake, srv := newFakePlacement(t, map[string]fakeReply{
		"/provisions/review": {http.StatusOK, `{"creationViable":true,"overallStatus":"Ready",
			"overallMessage":"ok","estimatedCost":"$1.50/hour","totalNodeCount":1,"recommendations":["none"]}`},
		"/provisions": {http.StatusOK, `{"infraId":"vllm-lab-01","message":"created",
			"infra":{"id":"vllm-lab-01","name":"vllm-lab-01","status":"Running",
			"node":[{"id":"serving-1","publicIP":"203.0.113.10","specId":"khu+gpu+a100"}]}}`},
		"/provisions/vllm-lab-01/update": {http.StatusOK, `{"infraId":"vllm-lab-01","appId":"vllm-llama31-8b",
			"appVersion":"0.2.0","nodes":[{"nodeId":"serving-1","succeeded":true}],"message":"updated"}`},
	})
	provisioner := NewRemoteProvisioner(remoteBackend(srv.URL, testToken))
	ctx := context.Background()
	plan := testPlan()

	review, err := provisioner.Review(ctx, plan)
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if !review.CreationViable || review.EstimatedCost != "$1.50/hour" || review.TotalNodeCount != 1 ||
		review.OverallStatus != "Ready" || len(review.Recommendations) != 1 {
		t.Errorf("review = %+v", review)
	}
	got := fake.request(t, "POST /provisions/review")
	if got.Body["nsId"] != "default" {
		t.Errorf("review nsId = %v", got.Body["nsId"])
	}
	assertPlanBody(t, "review", got.Body["plan"])
	if got.Authorization != "Bearer "+testToken {
		t.Errorf("review Authorization = %q", got.Authorization)
	}

	created, err := provisioner.Create(ctx, "ns-create", plan)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.InfraID != "vllm-lab-01" || created.Infra == nil || created.Infra.Status != "Running" ||
		len(created.Infra.Node) != 1 || created.Infra.Node[0].PublicIP != "203.0.113.10" {
		t.Errorf("create = %+v infra = %+v", created, created.Infra)
	}
	got = fake.request(t, "POST /provisions")
	if got.Body["nsId"] != "ns-create" {
		t.Errorf("create nsId = %v", got.Body["nsId"])
	}
	assertPlanBody(t, "create", got.Body["plan"])

	app := &model.AppSpec{ID: "vllm-llama31-8b", Version: "0.2.0",
		Install: model.InstallSpec{Commands: []string{"echo v2"}}}
	updated, err := provisioner.Update(ctx, "default", "vllm-lab-01", app)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.AppVersion != "0.2.0" || len(updated.Nodes) != 1 || !updated.Nodes[0].Succeeded {
		t.Errorf("update = %+v", updated)
	}
	got = fake.request(t, "POST /provisions/vllm-lab-01/update")
	if got.Body["nsId"] != "default" {
		t.Errorf("update nsId = %v", got.Body["nsId"])
	}
	body, _ := got.Body["app"].(map[string]any)
	install, _ := body["install"].(map[string]any)
	if body["id"] != "vllm-llama31-8b" || body["version"] != "0.2.0" || install == nil {
		t.Errorf("update app = %v", got.Body["app"])
	}
	if got.Authorization != "Bearer "+testToken {
		t.Errorf("update Authorization = %q", got.Authorization)
	}
}

// assertPlanBody checks that the plan went out whole, with the CB-Tumblebug
// request serialized as it is.
func assertPlanBody(t *testing.T, call string, raw any) {
	t.Helper()
	plan, _ := raw.(map[string]any)
	if plan["infraName"] != "vllm-lab-01" || plan["specId"] != "khu+gpu+a100" || plan["nsId"] != "default" {
		t.Errorf("%s plan = %v", call, raw)
		return
	}
	request, _ := plan["request"].(map[string]any)
	groups, _ := request["nodeGroups"].([]any)
	if request["name"] != "vllm-lab-01" || len(groups) != 1 || request["postCommands"] == nil {
		t.Errorf("%s plan.request = %v", call, plan["request"])
	}
}

func TestRemoteServerErrorIsReturned(t *testing.T) {
	_, srv := newFakePlacement(t, map[string]fakeReply{
		"/recommendations/specs": {http.StatusServiceUnavailable, `{"message":"placement engine down"}`},
		"/provisions":            {http.StatusInternalServerError, `quota exhausted in region seoul`},
	})
	ctx := context.Background()
	app := &model.AppSpec{ID: "vllm-llama31-8b"}

	_, err := NewRemoteRecommender(remoteBackend(srv.URL, "")).RecommendSpecs(ctx, app, 3)
	var remoteErr *RemoteError
	if !errors.As(err, &remoteErr) || remoteErr.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("RecommendSpecs error = %v, want a 503 RemoteError", err)
	}
	if !strings.Contains(err.Error(), "placement engine down") {
		t.Errorf("error %q does not carry the response body", err)
	}

	_, err = NewRemoteProvisioner(remoteBackend(srv.URL, "")).Create(ctx, "default", testPlan())
	if !errors.As(err, &remoteErr) || remoteErr.StatusCode != http.StatusInternalServerError {
		t.Fatalf("Create error = %v, want a 500 RemoteError", err)
	}
	if !strings.Contains(err.Error(), "quota exhausted") {
		t.Errorf("error %q does not carry the response body", err)
	}
}

// fakeInfraTumblebug answers a CB-Tumblebug single Infra read with the given
// labels. A nil label map leaves the field out, as an Infra created elsewhere does.
func fakeInfraTumblebug(t *testing.T, labels map[string]string) *tumblebug.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/ns/default/infra/vllm-lab-01" {
			http.Error(w, `{"message":"unexpected call"}`, http.StatusInternalServerError)
			return
		}
		infra := tumblebug.InfraInfo{ID: "vllm-lab-01", Name: "vllm-lab-01", Status: "Running", Label: labels}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(infra)
	}))
	t.Cleanup(srv.Close)
	return tumblebug.NewClient(config.TumblebugConfig{BaseURL: srv.URL, Timeout: 5 * time.Second})
}

func TestServiceUpdateDelegatesToProvisioner(t *testing.T) {
	fake, srv := newFakePlacement(t, map[string]fakeReply{
		"/provisions/vllm-lab-01/update": {http.StatusOK, `{"infraId":"vllm-lab-01","message":"updated"}`},
	})
	store := seededCatalog(t)
	ctx := context.Background()

	// A dry run reads nothing, so it needs no CB-Tumblebug and says the
	// application was not checked.
	guarded := NewService(nil, store, config.TumblebugConfig{DryRun: true},
		WithProvisioner(NewRemoteProvisioner(remoteBackend(srv.URL, ""))))
	result, err := guarded.Update(ctx, "default", "vllm-lab-01", "vllm-llama31-8b")
	if err != nil {
		t.Fatalf("Update under dry run: %v", err)
	}
	if !result.DryRun || result.AppID != "vllm-llama31-8b" {
		t.Errorf("dry-run result = %+v", result)
	}
	if !strings.Contains(result.Message, "app id not checked in dry run") {
		t.Errorf("dry-run message = %q, want it to say the app id was not checked", result.Message)
	}
	if len(fake.requests) != 0 {
		t.Fatalf("dry run reached the provisioner: %v", fake.requests)
	}

	client := fakeInfraTumblebug(t, map[string]string{labelAppID: "vllm-llama31-8b"})
	live := NewService(client, store, config.TumblebugConfig{DryRun: false},
		WithProvisioner(NewRemoteProvisioner(remoteBackend(srv.URL, ""))))
	result, err = live.Update(ctx, "default", "vllm-lab-01", "vllm-llama31-8b")
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if result.DryRun || result.Message != "updated" {
		t.Errorf("result = %+v", result)
	}
	fake.request(t, "POST /provisions/vllm-lab-01/update")

	if _, err := live.Update(ctx, "default", "vllm-lab-01", "no-such-app"); !errors.Is(err, catalog.ErrNotFound) {
		t.Errorf("Update with an unknown app = %v, want catalog.ErrNotFound", err)
	}
}

func TestServiceUpdateRefusesAnotherApplication(t *testing.T) {
	store := seededCatalog(t)
	seeded, err := store.Get(context.Background(), "vllm-llama31-8b")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	other := *seeded
	other.ID = "other-app"
	if _, err := store.Register(context.Background(), other); err != nil {
		t.Fatalf("register: %v", err)
	}

	for _, tc := range []struct {
		name   string
		labels map[string]string
		appID  string
	}{
		{"label names another app", map[string]string{labelAppID: "vllm-llama31-8b"}, "other-app"},
		{"no label", nil, "vllm-llama31-8b"},
		{"empty label", map[string]string{labelAppID: ""}, "vllm-llama31-8b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake, srv := newFakePlacement(t, map[string]fakeReply{
				"/provisions/vllm-lab-01/update": {http.StatusOK, `{"infraId":"vllm-lab-01"}`},
			})
			service := NewService(fakeInfraTumblebug(t, tc.labels), store, config.TumblebugConfig{DryRun: false},
				WithProvisioner(NewRemoteProvisioner(remoteBackend(srv.URL, ""))))

			_, err := service.Update(context.Background(), "default", "vllm-lab-01", tc.appID)
			if !errors.Is(err, ErrAppMismatch) {
				t.Fatalf("Update error = %v, want ErrAppMismatch", err)
			}
			if len(fake.requests) != 0 {
				t.Errorf("a refused update reached the provisioner: %v", fake.requests)
			}
		})
	}
}

func TestServiceDefaultsToLocalBackends(t *testing.T) {
	cfg := config.TumblebugConfig{ImageNamespace: "system", SpecNamespace: "system", DefaultOSType: "ubuntu 22.04"}
	client := tumblebug.NewClient(cfg)

	service := NewService(client, catalog.NewStore(), cfg)
	recommender, ok := service.recommender.(*localRecommender)
	if !ok {
		t.Fatalf("recommender = %T, want *localRecommender", service.recommender)
	}
	if recommender.tumblebug != client || recommender.imageNamespace != "system" || recommender.defaultOSType != "ubuntu 22.04" {
		t.Errorf("local recommender = %+v", recommender)
	}
	if _, ok := service.provisioner.(*localProvisioner); !ok {
		t.Fatalf("provisioner = %T, want *localProvisioner", service.provisioner)
	}

	local := config.BackendConfig{Kind: config.BackendLocal}
	if opts := OptionsFromConfig(local, local); len(opts) != 0 {
		t.Errorf("local settings produced %d options, want none", len(opts))
	}
	service = NewService(client, catalog.NewStore(), cfg, OptionsFromConfig(local, local)...)
	if _, ok := service.recommender.(*localRecommender); !ok {
		t.Errorf("recommender = %T with local settings", service.recommender)
	}

	remote := remoteBackend("http://placement.invalid", "")
	service = NewService(client, catalog.NewStore(), cfg, OptionsFromConfig(remote, remote)...)
	if _, ok := service.recommender.(*remoteRecommender); !ok {
		t.Errorf("recommender = %T with remote settings", service.recommender)
	}
	if _, ok := service.provisioner.(*remoteProvisioner); !ok {
		t.Errorf("provisioner = %T with remote settings", service.provisioner)
	}

	if got := sourceOf("recommended", service.recommender); got != "recommended:remote" {
		t.Errorf("remote source = %q", got)
	}
	if got := sourceOf("recommended", &localRecommender{}); got != "recommended" {
		t.Errorf("local source = %q, want the unqualified value", got)
	}
}
