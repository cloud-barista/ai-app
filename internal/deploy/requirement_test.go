package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ish-hcc/ai-agent-proto/internal/config"
	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/tumblebug"
)

// fakeInfraListTumblebug answers a CB-Tumblebug namespace Infra listing with the
// given Infras and refuses every other call.
func fakeInfraListTumblebug(t *testing.T, infras []tumblebug.InfraInfo) *tumblebug.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/ns/default/infra" {
			http.Error(w, `{"message":"unexpected call"}`, http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"infra": infras})
	}))
	t.Cleanup(srv.Close)
	return tumblebug.NewClient(config.TumblebugConfig{BaseURL: srv.URL, Timeout: 5 * time.Second})
}

func openWebUIReq() *model.DeployAppReq {
	return &model.DeployAppReq{
		AppID: "open-webui-llama31", InfraName: "webui-lab-01",
		SpecID: "aws+us-west-2+t3.large", ImageID: "ubuntu22.04",
	}
}

func TestPlanInjectsRequiredModelServiceEndpoint(t *testing.T) {
	client := fakeInfraListTumblebug(t, []tumblebug.InfraInfo{
		{ID: "vllm-stopped", Status: "Suspended:1 (R:0/1)", Label: map[string]string{labelAppID: "vllm-llama31-8b"},
			Node: []tumblebug.NodeInfo{{ID: "n1", Status: "Suspended", PublicIP: "203.0.113.9"}}},
		{ID: "qwen-lab", Status: "Running:1 (R:1/1)", Label: map[string]string{labelAppID: "ollama-qwen25-7b"},
			Node: []tumblebug.NodeInfo{{ID: "n1", Status: "Running", PublicIP: "203.0.113.8"}}},
		{ID: "vllm-lab-01", Status: "Running:1 (R:1/1)", Label: map[string]string{labelAppID: "vllm-llama31-8b"},
			Node: []tumblebug.NodeInfo{{ID: "n1", Status: "Running", PublicIP: "203.0.113.10"}}},
	})
	store := seededCatalog(t)
	service := NewService(client, store, config.TumblebugConfig{DryRun: true})

	plan, err := service.Plan(context.Background(), "default", openWebUIReq())
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	want := model.ResolvedRequirement{
		AppID: "vllm-llama31-8b", Env: "OPENAI_API_BASE_URL",
		InfraID: "vllm-lab-01", Endpoint: "http://203.0.113.10:8000/v1",
	}
	if len(plan.Requirements) != 1 || plan.Requirements[0] != want {
		t.Fatalf("requirements = %+v, want [%+v]", plan.Requirements, want)
	}

	commands := plan.Request.(*tumblebug.InfraDynamicReq).PostCommands[0].Command
	prefix := "export OPENAI_API_BASE_URL=http://203.0.113.10:8000/v1 && "
	if !strings.HasPrefix(commands[0], prefix) {
		t.Errorf("first post command = %q, want prefix %q", commands[0], prefix)
	}
	app, _ := store.Get(context.Background(), "open-webui-llama31")
	if len(commands) != len(app.Install.Commands) {
		t.Fatalf("post commands = %d, want %d", len(commands), len(app.Install.Commands))
	}
	for i, command := range commands {
		if command != prefix+app.Install.Commands[i] {
			t.Errorf("post command %d = %q", i, command)
		}
	}
	if strings.HasPrefix(app.Install.Commands[0], "export") {
		t.Error("catalog entry was changed by planning")
	}
}

func TestPlanRefusesApplicationWithoutModelService(t *testing.T) {
	client := fakeInfraListTumblebug(t, []tumblebug.InfraInfo{
		{ID: "vllm-stopped", Status: "Suspended:1 (R:0/1)", Label: map[string]string{labelAppID: "vllm-llama31-8b"},
			Node: []tumblebug.NodeInfo{{ID: "n1", Status: "Suspended", PublicIP: "203.0.113.9"}}},
	})
	service := NewService(client, seededCatalog(t), config.TumblebugConfig{DryRun: true})

	_, err := service.Plan(context.Background(), "default", openWebUIReq())
	if !errors.Is(err, ErrRequirementNotDeployed) {
		t.Fatalf("Plan() = %v, want ErrRequirementNotDeployed", err)
	}
	want := "application open-webui-llama31 needs model service vllm-llama31-8b deployed in namespace default first"
	if err.Error() != want {
		t.Errorf("message = %q, want %q", err.Error(), want)
	}
}

func TestPlanModelServiceSkipsRequirementLookup(t *testing.T) {
	// A model service that pins its spec and image needs nothing from CB-Tumblebug.
	service := NewService(fakeInfraListTumblebug(t, nil), seededCatalog(t), config.TumblebugConfig{DryRun: true})
	plan, err := service.Plan(context.Background(), "default", &model.DeployAppReq{
		AppID: "vllm-llama31-8b", InfraName: "vllm-lab-02", SpecID: "s", ImageID: "i",
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(plan.Requirements) != 0 {
		t.Errorf("requirements = %+v, want none", plan.Requirements)
	}
	if got := plan.Request.(*tumblebug.InfraDynamicReq).PostCommands[0].Command[0]; got != "sudo apt-get update -y" {
		t.Errorf("first post command = %q", got)
	}
}

func TestSpecRequestWithoutAcceleratorFiltersOnlyCPUAndMemory(t *testing.T) {
	app, err := seededCatalog(t).Get(context.Background(), "open-webui-llama31")
	if err != nil {
		t.Fatal(err)
	}
	req := buildSpecRequest(app, 5)
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

	gpu, _ := seededCatalog(t).Get(context.Background(), "vllm-llama31-8b")
	if buildSpecRequest(gpu, 5).Filter.Policy[0].Metric != metricAcceleratorType {
		t.Error("a model service lost its accelerator type filter")
	}
}

func TestShellQuote(t *testing.T) {
	cases := map[string]string{
		"http://203.0.113.10:8000/v1": "http://203.0.113.10:8000/v1",
		"http://h/v1; id":             `'http://h/v1; id'`,
		"it's":                        `'it'\''s'`,
	}
	for in, want := range cases {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}
