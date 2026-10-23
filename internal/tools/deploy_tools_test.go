package tools

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ish-hcc/ai-agent-proto/internal/catalog"
	"github.com/ish-hcc/ai-agent-proto/internal/config"
	"github.com/ish-hcc/ai-agent-proto/internal/deploy"
	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/tumblebug"
)

// fakeCreator reviews every plan as viable and creates a one node Infra.
type fakeCreator struct{}

func (fakeCreator) Review(ctx context.Context, plan *model.DeploymentPlan) (*model.ReviewSummary, error) {
	return &model.ReviewSummary{CreationViable: true}, nil
}

func (fakeCreator) Create(ctx context.Context, nsID string, plan *model.DeploymentPlan) (*model.ProvisionResult, error) {
	infra := &tumblebug.InfraInfo{ID: plan.InfraName, Node: []tumblebug.NodeInfo{{ID: "n1"}}}
	return &model.ProvisionResult{InfraID: infra.ID, Infra: infra}, nil
}

func (fakeCreator) Update(ctx context.Context, nsID, infraID string, app *model.AppSpec) (*model.ProvisionResult, error) {
	return nil, errors.New("not used")
}

// The agent calls deploy_ai_app inside a model loop, so the tool must not wait
// for the application: it checks once and points at check_serving_health.
func TestDeployToolChecksHealthOnceAndPointsAtTheCheckTool(t *testing.T) {
	var healthChecks atomic.Int32
	tb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/ns/default/infra":
			_, _ = w.Write([]byte(`{"infra":[]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/ns/default/cmd/infra/vllm-lab-01":
			var req tumblebug.InfraCmdReq
			_ = json.NewDecoder(r.Body).Decode(&req)
			stdout := ""
			if strings.Contains(req.Command[0], "curl -s -o /dev/null") {
				healthChecks.Add(1)
				stdout = "000"
			}
			_ = json.NewEncoder(w).Encode(tumblebug.CommandResults{Results: []tumblebug.CommandResult{
				{NodeID: "n1", Stdout: map[string]string{"0": stdout}},
			}})
		default:
			http.Error(w, `{"message":"unexpected call"}`, http.StatusInternalServerError)
		}
	}))
	t.Cleanup(tb.Close)

	cfg := config.TumblebugConfig{BaseURL: tb.URL, Timeout: 5 * time.Second, DryRun: false}
	apps := catalog.NewStore()
	if err := catalog.Seed(context.Background(), apps); err != nil {
		t.Fatalf("seed: %v", err)
	}
	service := deploy.NewService(tumblebug.NewClient(cfg), apps, cfg,
		deploy.WithProvisioner(fakeCreator{}), deploy.WithHealthWait(time.Second, time.Millisecond))
	registry := NewRegistry(cfg.DryRun)
	Register(registry, apps, service)

	if got := registry.Grade("check_serving_health"); got != GradeRead {
		t.Errorf("check_serving_health grade = %q, want read", got)
	}

	input := json.RawMessage(`{"appId":"vllm-llama31-8b","infraName":"vllm-lab-01",` +
		`"specId":"aws+us-west-2+g6.xlarge","imageId":"ubuntu22.04"}`)
	out, err := registry.Call(context.Background(), "default", "deploy_ai_app", input)
	if err != nil {
		t.Fatalf("deploy_ai_app: %v", err)
	}
	result := out.(*model.DeploymentResult)
	if result.Health == nil || result.Health.Ready || result.Health.Attempts != 1 || healthChecks.Load() != 1 {
		t.Errorf("health = %+v after %d checks, want one not ready check", result.Health, healthChecks.Load())
	}
	if !strings.Contains(result.Message, "check again with check_serving_health") {
		t.Errorf("message = %q, want it to point at check_serving_health", result.Message)
	}
}
