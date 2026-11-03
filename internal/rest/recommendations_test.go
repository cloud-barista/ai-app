package rest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/ish-hcc/ai-agent-proto/internal/appstore"
	"github.com/ish-hcc/ai-agent-proto/internal/config"
	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/modelcat"
	"github.com/ish-hcc/ai-agent-proto/internal/recommend"
	"github.com/ish-hcc/ai-agent-proto/internal/tools"
)

// fakeModels recommends the first runtime of every artifact of every model
// that has one of the asked tasks, read from the catalog on each request.
type fakeModels struct{ catalog recommend.Catalog }

func (f *fakeModels) Recommend(ctx context.Context, req *recommend.ModelRecommendReq) (*recommend.ModelRecommendResp, error) {
	models := f.catalog.Models(ctx)
	runtimes := f.catalog.Runtimes()
	resp := &recommend.ModelRecommendResp{}
	for _, m := range models {
		if len(req.Tasks) > 0 && !slices.ContainsFunc(req.Tasks, m.HasTask) {
			continue
		}
		for _, artifact := range m.Artifacts {
			resp.Candidates = append(resp.Candidates, recommend.ModelCandidate{
				ModelID:    m.ID,
				ArtifactID: artifact.ID,
				RuntimeID:  artifact.Runtimes[0].RuntimeID,
				Score:      1,
				Reason:     fmt.Sprintf("read %d models and %d runtimes", len(models), len(runtimes)),
			})
		}
	}
	return resp, nil
}

// fakeApps recommends the latest version of every uploaded application.
type fakeApps struct{ catalog recommend.Catalog }

func (f *fakeApps) Recommend(ctx context.Context, req *recommend.AppRecommendReq) (*recommend.AppRecommendResp, error) {
	resp := &recommend.AppRecommendResp{}
	for _, app := range f.catalog.Apps(ctx) {
		latest := app.Versions[len(app.Versions)-1]
		resp.Candidates = append(resp.Candidates, recommend.AppCandidate{
			Name: app.Name, Version: latest.Version, Score: 1, Reason: "uploaded",
		})
	}
	return resp, nil
}

// strayModels names a model the catalog does not hold.
type strayModels struct{}

func (strayModels) Recommend(context.Context, *recommend.ModelRecommendReq) (*recommend.ModelRecommendResp, error) {
	return &recommend.ModelRecommendResp{Candidates: []recommend.ModelCandidate{
		{ModelID: "nowhere", ArtifactID: "hf-bf16", RuntimeID: model.RuntimeVLLM},
	}}, nil
}

func init() {
	recommend.RegisterModel("fake", func(catalog recommend.Catalog) (recommend.ModelRecommender, error) {
		return &fakeModels{catalog: catalog}, nil
	})
	recommend.RegisterApp("fake", func(catalog recommend.Catalog) (recommend.AppRecommender, error) {
		return &fakeApps{catalog: catalog}, nil
	})
	recommend.RegisterModel("stray", func(recommend.Catalog) (recommend.ModelRecommender, error) {
		return strayModels{}, nil
	})
}

func newRecommendServer(t *testing.T, modelName, appName string) *Server {
	t.Helper()
	ctx := context.Background()
	models, _, err := modelcat.Open("")
	if err != nil {
		t.Fatalf("Open models: %v", err)
	}
	if err := modelcat.Seed(ctx, models); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	apps, err := appstore.Open(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatalf("Open apps: %v", err)
	}
	if _, err := apps.Register(ctx, "open-webui", "docker compose up -d", nil,
		[]appstore.File{{Name: "docker-compose.yml", Content: strings.NewReader("services: {}\n")}}); err != nil {
		t.Fatalf("Register app: %v", err)
	}

	cfg := &config.Config{
		Engine:    config.EngineConfig{URL: "http://127.0.0.1:1"},
		PartnerNL: config.PartnerNLConfig{URL: "http://127.0.0.1:1"},
	}
	deps := AppDependencies{Models: models, Apps: apps, Tools: tools.NewRegistry(true)}
	if modelName != "" || appName != "" {
		deps.Recommend, err = recommend.Open(modelName, appName, recommend.NewCatalog(models, apps))
		if err != nil {
			t.Fatalf("recommend.Open: %v", err)
		}
	}
	return NewAppServer(cfg, deps)
}

func TestRecommendationsNotConfigured(t *testing.T) {
	s := newRecommendServer(t, "", "")
	for _, path := range []string{"/aiapp/recommendations/models", "/aiapp/recommendations/apps"} {
		rec := postJSON(s, path, `{"tasks":["chat"]}`)
		if rec.Code != http.StatusNotImplemented {
			t.Fatalf("POST %s = %d, want 501: %s", path, rec.Code, rec.Body.String())
		}
		var msg model.SimpleMsg
		if err := json.Unmarshal(rec.Body.Bytes(), &msg); err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		if !strings.Contains(msg.Message, "no recommender is configured") {
			t.Errorf("POST %s message = %q, want one saying no recommender is configured", path, msg.Message)
		}
	}

	// A malformed request is refused before the missing recommender is.
	for path, body := range map[string]string{
		"/aiapp/recommendations/models": `{"runtimeId":"nowhere"}`,
		"/aiapp/recommendations/apps":   `{"inference":[{"modelId":"nowhere"}]}`,
	} {
		if rec := postJSON(s, path, body); rec.Code != http.StatusBadRequest {
			t.Errorf("POST %s %s = %d, want 400: %s", path, body, rec.Code, rec.Body.String())
		}
	}
}

func TestModelRecommendationWithFake(t *testing.T) {
	s := newRecommendServer(t, "fake", "fake")

	recommendModels := func(body string) recommend.ModelRecommendResp {
		t.Helper()
		rec := postJSON(s, "/aiapp/recommendations/models", body)
		if rec.Code != http.StatusOK {
			t.Fatalf("POST models %s = %d: %s", body, rec.Code, rec.Body.String())
		}
		var resp recommend.ModelRecommendResp
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		return resp
	}

	resp := recommendModels(`{"tasks":["embedding"]}`)
	if resp.Recommender != "fake" {
		t.Errorf("recommender = %q, want fake", resp.Recommender)
	}
	if len(resp.Candidates) != 1 || resp.Candidates[0].ModelID != "qwen3-embedding-0.6b" ||
		resp.Candidates[0].ArtifactID != "hf-bf16" || resp.Candidates[0].RuntimeID != model.RuntimeVLLM {
		t.Fatalf("candidates = %+v, want qwen3-embedding-0.6b/hf-bf16/vllm", resp.Candidates)
	}
	if resp.Candidates[0].Reason != "read 3 models and 2 runtimes" {
		t.Errorf("reason = %q, want the fake to have read the seeded catalog", resp.Candidates[0].Reason)
	}

	// The fake reads the catalog on each request, so a model registered after
	// startup is a candidate.
	const added = `{"id":"added","name":"Added","tasks":["embedding"],"artifacts":[{"id":"a",` +
		`"source":"huggingface","ref":"org/added","format":"hf","runtimes":[{"runtimeId":"vllm"}]}]}`
	if rec := postJSON(s, "/aiapp/models", added); rec.Code != http.StatusOK {
		t.Fatalf("POST /models = %d: %s", rec.Code, rec.Body.String())
	}
	if resp := recommendModels(`{"tasks":["embedding"]}`); len(resp.Candidates) != 2 || resp.Candidates[0].ModelID != "added" {
		t.Errorf("candidates after registering = %+v, want added first of 2", resp.Candidates)
	}

	if resp := recommendModels(`{"maxCandidates":1}`); len(resp.Candidates) != 1 {
		t.Errorf("maxCandidates 1 = %d candidates", len(resp.Candidates))
	}
	if resp := recommendModels(``); len(resp.Candidates) == 0 {
		t.Errorf("empty request = no candidates, want every artifact")
	}

	for _, body := range []string{
		`{"tasks":[""]}`,
		`{"gpuCount":-1}`,
		`{"gpuMemoryGiB":-2}`,
		`{"maxCandidates":51}`,
		`{"targetNodes":[{"nodeId":"vm1"}]}`,
		`{"targetNodes":[{"nsId":"etri","nodeId":"vm1"}]}`,
		`{"tasks":"chat"}`,
	} {
		if rec := postJSON(s, "/aiapp/recommendations/models", body); rec.Code != http.StatusBadRequest {
			t.Errorf("POST models %s = %d, want 400: %s", body, rec.Code, rec.Body.String())
		}
	}
}

func TestAppRecommendationWithFake(t *testing.T) {
	s := newRecommendServer(t, "", "fake")

	rec := postJSON(s, "/aiapp/recommendations/apps", `{"tasks":["chat-ui"],"inference":[{"modelId":"qwen3-vl-8b"}],"requirements":{"budget":"low"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST apps = %d: %s", rec.Code, rec.Body.String())
	}
	var resp recommend.AppRecommendResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Recommender != "fake" || len(resp.Candidates) != 1 ||
		resp.Candidates[0].Name != "open-webui" || resp.Candidates[0].Version != 1 {
		t.Errorf("resp = %+v, want fake recommending open-webui version 1", resp)
	}

	// Only the application recommender is configured.
	if rec := postJSON(s, "/aiapp/recommendations/models", `{}`); rec.Code != http.StatusNotImplemented {
		t.Errorf("POST models = %d, want 501", rec.Code)
	}
}

func TestRecommendationCandidateNotInCatalog(t *testing.T) {
	s := newRecommendServer(t, "stray", "")
	rec := postJSON(s, "/aiapp/recommendations/models", `{}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("POST models = %d, want 500: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "nowhere") {
		t.Errorf("body = %s, want the recommender's detail kept out", rec.Body.String())
	}
}
