package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ish-hcc/ai-agent-proto/internal/archive"
	"github.com/ish-hcc/ai-agent-proto/internal/mcpclient"
	"github.com/ish-hcc/ai-agent-proto/internal/mcpserver"
	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/tools"
	"github.com/ish-hcc/ai-agent-proto/internal/tumblebug"
)

const (
	modelServiceID   = "vllm-llama31-8b"
	applicationID    = "open-webui-llama31"
	recommendedSpec  = "aws+us-west-2+g6.xlarge"
	recommendedImage = "ubuntu22.04-gpu"
)

// fakeApp is an AI-App MCP server whose tool answers the test controls.
type fakeApp struct {
	mu          sync.Mutex
	dryRun      bool
	deployments []tumblebug.InfraInfo
	// ready is the answer of each check_serving_health call in turn; the last
	// one repeats.
	ready []bool
	// fail names a tool, optionally with the appId it fails for as
	// "tool:appId", that answers with an error.
	fail  string
	calls []string
	ns    []string
	// specs records the specId every review and deploy call carried.
	specs []string
	// updateNodes is whether each node of an update succeeded. Nil answers
	// one node that succeeded.
	updateNodes []bool
}

func (f *fakeApp) record(nsID, tool string, input json.RawMessage) error {
	var args struct {
		AppID  string `json:"appId"`
		SpecID string `json:"specId"`
	}
	_ = json.Unmarshal(input, &args)

	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, tool+":"+args.AppID)
	f.ns = append(f.ns, nsID)
	if tool == "review_deployment" || tool == "deploy_ai_app" {
		f.specs = append(f.specs, args.SpecID)
	}
	if f.fail == tool || f.fail == tool+":"+args.AppID {
		return fmt.Errorf("%s refused by the fake AI-App", tool)
	}
	return nil
}

func (f *fakeApp) calledTools() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.calls...)
}

func (f *fakeApp) nextReady() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.ready) == 0 {
		return false
	}
	ready := f.ready[0]
	if len(f.ready) > 1 {
		f.ready = f.ready[1:]
	}
	return ready
}

func (f *fakeApp) tool(name string, grade tools.Grade, handle func(nsID string, input json.RawMessage) (any, error)) tools.Tool {
	return tools.Tool{
		Name:        name,
		Grade:       grade,
		Description: "fake " + name,
		InputSchema: map[string]any{"type": "object"},
		Handle: func(ctx context.Context, nsID string, input json.RawMessage) (any, error) {
			if err := f.record(nsID, name, input); err != nil {
				return nil, err
			}
			return handle(nsID, input)
		},
	}
}

func (f *fakeApp) registry() *tools.Registry {
	registry := tools.NewRegistry(f.dryRun)
	apps := map[string]model.AppSpec{
		modelServiceID: {ID: modelServiceID, Kind: model.KindModelService},
		applicationID: {ID: applicationID, Kind: model.KindApplication,
			Requires: []model.AppRequirement{{AppID: modelServiceID, Env: "OPENAI_API_BASE_URL"}}},
	}
	args := func(input json.RawMessage) deployArgs {
		var a deployArgs
		_ = json.Unmarshal(input, &a)
		return a
	}
	planOf := func(input json.RawMessage) *model.DeploymentPlan {
		a := args(input)
		plan := &model.DeploymentPlan{AppID: a.AppID, InfraName: a.InfraName, SpecID: a.SpecID, ImageID: a.ImageID,
			SpecSource: "requested", ImageSource: "requested"}
		if plan.SpecID == "" {
			plan.SpecID, plan.SpecSource = recommendedSpec, "recommended"
		}
		if plan.ImageID == "" {
			plan.ImageID, plan.ImageSource = recommendedImage, "resolved"
		}
		return plan
	}

	registry.Add(f.tool("get_ai_app", tools.GradeRead, func(_ string, input json.RawMessage) (any, error) {
		app, ok := apps[args(input).AppID]
		if !ok {
			return nil, errors.New("application not found")
		}
		return app, nil
	}))
	registry.Add(f.tool("list_deployments", tools.GradeRead, func(string, json.RawMessage) (any, error) {
		return map[string]any{"deployments": f.deployments}, nil
	}))
	registry.Add(f.tool("plan_deployment", tools.GradeRead, func(_ string, input json.RawMessage) (any, error) {
		return planOf(input), nil
	}))
	registry.Add(f.tool("review_deployment", tools.GradeReview, func(_ string, input json.RawMessage) (any, error) {
		return map[string]any{"plan": planOf(input), "review": model.ReviewSummary{
			CreationViable: true, OverallStatus: "Ready", EstimatedCost: "$0.80/hour"}}, nil
	}))
	registry.Add(f.tool("deploy_ai_app", tools.GradeWrite, func(_ string, input json.RawMessage) (any, error) {
		if f.dryRun {
			return model.DeploymentResult{Plan: planOf(input), DryRun: true, Message: "Dry run: request planned but not sent"}, nil
		}
		return model.DeploymentResult{Plan: planOf(input), Message: "Deployed"}, nil
	}))
	registry.Add(f.tool("check_serving_health", tools.GradeRead, func(_ string, input json.RawMessage) (any, error) {
		var a struct {
			InfraID string `json:"infraId"`
			AppID   string `json:"appId"`
		}
		_ = json.Unmarshal(input, &a)
		if a.AppID == "" {
			a.AppID = modelServiceID
		}
		ready := f.nextReady()
		message := "0 of 1 nodes answer on port 8000"
		if ready {
			message = "1 of 1 nodes answer on port 8000"
		}
		return model.ServingHealth{InfraID: a.InfraID, AppID: a.AppID, Ready: ready, Message: message}, nil
	}))
	registry.Add(f.tool("update_deployment", tools.GradeWrite, func(string, json.RawMessage) (any, error) {
		if f.dryRun {
			return model.ProvisionResult{DryRun: true, Message: "update planned"}, nil
		}
		outcomes := f.updateNodes
		if outcomes == nil {
			outcomes = []bool{true}
		}
		nodes := make([]model.NodeUpdate, 0, len(outcomes))
		for i, ok := range outcomes {
			node := model.NodeUpdate{NodeID: fmt.Sprintf("n%d", i+1), Succeeded: ok}
			if !ok {
				node.Error = "exit status 1"
			}
			nodes = append(nodes, node)
		}
		return model.ProvisionResult{Nodes: nodes, Message: "update applied"}, nil
	}))
	registry.Add(f.tool("control_deployment", tools.GradeWrite, func(string, json.RawMessage) (any, error) {
		return model.ControlResult{DryRun: f.dryRun, Message: "control planned"}, nil
	}))
	registry.Add(f.tool("get_deployment_status", tools.GradeRead, func(string, json.RawMessage) (any, error) {
		return tumblebug.InfraStatusView{ID: "d1", Status: "Running:1 (R:1/1)",
			StatusCount: tumblebug.StatusCountInfo{CountTotal: 1, CountRunning: 1}}, nil
	}))
	registry.Add(f.tool("probe_accelerator", tools.GradeRead, func(string, json.RawMessage) (any, error) {
		return model.AcceleratorReport{Probed: true, Message: "1 GPU found"}, nil
	}))
	registry.Add(f.tool("list_ai_apps", tools.GradeRead, func(string, json.RawMessage) (any, error) {
		return map[string]any{"apps": []string{modelServiceID, applicationID}}, nil
	}))
	registry.Add(f.tool("find_ai_apps", tools.GradeRead, func(_ string, input json.RawMessage) (any, error) {
		var a struct {
			Query string `json:"query"`
			Limit int    `json:"limit"`
		}
		_ = json.Unmarshal(input, &a)
		return map[string]any{"candidates": []string{applicationID}, "query": a.Query, "limit": a.Limit}, nil
	}))
	// The engine never calls this one, so its card must not list it.
	registry.Add(f.tool("delete_ai_app", tools.GradeDestructive, func(string, json.RawMessage) (any, error) {
		return map[string]any{}, nil
	}))
	return registry
}

// newEngine serves fake over MCP and returns an engine calling it.
func newEngine(t *testing.T, fake *fakeApp) (*Engine, string) {
	t.Helper()
	srv := httptest.NewServer(mcpserver.NewHandler(fake.registry()))
	t.Cleanup(srv.Close)

	client := mcpclient.New(srv.URL, "")
	t.Cleanup(func() { _ = client.Close() })
	dir := t.TempDir()
	runs, err := archive.NewLog[Run](dir)
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	return New(client, runs, WithHealthWait(time.Second, time.Millisecond)), dir
}

func execute(t *testing.T, e *Engine, workflow string, input any) *Run {
	t.Helper()
	body, _ := json.Marshal(input)
	run, err := e.Execute(context.Background(), "ns-test", workflow, body)
	if err != nil {
		t.Fatalf("Execute %s: %v", workflow, err)
	}
	return run
}

// shape renders the steps as "tool:status" for comparison.
func shape(run *Run) string {
	parts := make([]string, 0, len(run.Steps))
	for _, step := range run.Steps {
		parts = append(parts, step.Tool+":"+string(step.Status))
	}
	return strings.Join(parts, " ")
}

func expectShape(t *testing.T, run *Run, want string) {
	t.Helper()
	if got := shape(run); got != want {
		t.Errorf("steps =\n  %s\nwant\n  %s", got, want)
		for _, step := range run.Steps {
			t.Logf("  %d %s %s %s err=%q msg=%q", step.Index, step.Name, step.Tool, step.Status, step.Error, step.Message)
		}
	}
}

func TestDeployModelServiceDryRunBlocksDeployAndHoldsHealth(t *testing.T) {
	fake := &fakeApp{dryRun: true}
	e, _ := newEngine(t, fake)

	run := execute(t, e, WorkflowDeploy, DeployInput{AppID: modelServiceID, DeploymentName: "demo-vllm"})

	expectShape(t, run, "get_ai_app:succeeded plan_deployment:succeeded review_deployment:succeeded deploy_ai_app:blocked "+
		"check_serving_health:waiting")
	if run.Status != RunBlocked || !run.DryRun || !strings.Contains(run.Message, "1 later steps wait") {
		t.Errorf("run status = %s dryRun = %t message %q, want blocked under dry run", run.Status, run.DryRun, run.Message)
	}
	health := run.Steps[4]
	if health.Message != "waits for demo-vllm to be provisioned (blocked by dry run)" || health.StartedAt != nil {
		t.Errorf("health step message = %q started = %v, want it held and not run", health.Message, health.StartedAt)
	}
	if in := health.Input.(map[string]string); in["infraId"] != "demo-vllm" || in["appId"] != modelServiceID {
		t.Errorf("health step input = %v", in)
	}
	for _, call := range fake.calledTools() {
		if strings.HasPrefix(call, "check_serving_health") {
			t.Errorf("health was checked under dry run: %v", fake.calledTools())
		}
	}
	// The review and the deployment use what the plan chose.
	if len(fake.specs) != 2 || fake.specs[0] != recommendedSpec || fake.specs[1] != recommendedSpec {
		t.Errorf("review and deploy specId = %v, want the planned %s", fake.specs, recommendedSpec)
	}
	if run.Steps[3].StartedAt == nil || run.Steps[3].Message == "" {
		t.Errorf("blocked step = %+v, want it run and explained", run.Steps[3])
	}
	for _, ns := range fake.ns {
		if ns != "ns-test" {
			t.Errorf("namespace header = %q, want ns-test", ns)
		}
	}
}

func TestDeployApplicationDeploysMissingModelServiceFirst(t *testing.T) {
	fake := &fakeApp{dryRun: true}
	e, _ := newEngine(t, fake)

	run := execute(t, e, WorkflowDeploy, DeployInput{AppID: applicationID, DeploymentName: "demo-webui"})

	expectShape(t, run, "get_ai_app:succeeded list_deployments:succeeded "+
		"plan_deployment:succeeded review_deployment:succeeded deploy_ai_app:blocked check_serving_health:waiting "+
		"plan_deployment:waiting review_deployment:waiting deploy_ai_app:waiting check_serving_health:waiting")
	modelPlan := run.Steps[2].Input.(deployArgs)
	if modelPlan.AppID != modelServiceID || modelPlan.InfraName != "demo-webui-"+modelServiceID {
		t.Errorf("model service plan input = %+v, want %s as demo-webui-%s", modelPlan, modelServiceID, modelServiceID)
	}
	want := "waits for model service " + modelServiceID + " to be running (blocked by dry run)"
	for _, step := range run.Steps[6:9] {
		if step.Message != want || step.StartedAt != nil {
			t.Errorf("step %d message = %q started = %v, want %q and not run", step.Index, step.Message, step.StartedAt, want)
		}
		if step.Input.(deployArgs).AppID != applicationID {
			t.Errorf("step %d input = %+v, want the application", step.Index, step.Input)
		}
	}
	healthWant := map[int]string{
		5: "waits for demo-webui-" + modelServiceID + " to be provisioned (blocked by dry run)",
		9: "waits for demo-webui to be provisioned (blocked by dry run)",
	}
	for i, want := range healthWant {
		if step := run.Steps[i]; step.Message != want || step.StartedAt != nil {
			t.Errorf("step %d message = %q started = %v, want %q and not run", step.Index, step.Message, step.StartedAt, want)
		}
	}
	for _, call := range fake.calledTools() {
		if strings.HasSuffix(call, ":"+applicationID) && !strings.HasPrefix(call, "get_ai_app") {
			t.Errorf("application step reached the AI-App service: %s", call)
		}
		if strings.HasPrefix(call, "check_serving_health") {
			t.Errorf("health was checked under dry run: %s", call)
		}
	}
	if run.Status != RunBlocked || !strings.Contains(run.Message, "5 later steps wait") {
		t.Errorf("run = %s %q", run.Status, run.Message)
	}
}

func TestDeployApplicationUsesRunningModelService(t *testing.T) {
	fake := &fakeApp{dryRun: true, deployments: []tumblebug.InfraInfo{
		{ID: "other", Status: "Running:1 (R:1/1)", Label: map[string]string{appIDLabel: "something-else"},
			Node: []tumblebug.NodeInfo{{PublicIP: "203.0.113.9", Status: "Running"}}},
		{ID: "vllm-01", Status: "Running:1 (R:1/1)", Label: map[string]string{appIDLabel: modelServiceID},
			Node: []tumblebug.NodeInfo{{PublicIP: "203.0.113.10", Status: "Running"}}},
	}}
	e, _ := newEngine(t, fake)

	run := execute(t, e, WorkflowDeploy, DeployInput{AppID: applicationID, DeploymentName: "demo-webui", SpecID: "pinned-spec"})

	expectShape(t, run, "get_ai_app:succeeded list_deployments:succeeded "+
		"plan_deployment:succeeded review_deployment:succeeded deploy_ai_app:blocked check_serving_health:waiting")
	if got := run.Steps[1].Message; got != "model service "+modelServiceID+" runs as vllm-01" {
		t.Errorf("list step message = %q", got)
	}
	if args := run.Steps[2].Input.(deployArgs); args.AppID != applicationID || args.InfraName != "demo-webui" || args.SpecID != "pinned-spec" {
		t.Errorf("plan input = %+v", args)
	}
}

func TestDeployChecksHealthWhenNotDryRun(t *testing.T) {
	fake := &fakeApp{dryRun: false, ready: []bool{false, true}}
	e, _ := newEngine(t, fake)

	run := execute(t, e, WorkflowDeploy, DeployInput{AppID: modelServiceID, DeploymentName: "demo-vllm"})

	expectShape(t, run, "get_ai_app:succeeded plan_deployment:succeeded review_deployment:succeeded "+
		"deploy_ai_app:succeeded check_serving_health:succeeded")
	if run.Status != RunSucceeded || run.DryRun {
		t.Errorf("run = %s dryRun %t", run.Status, run.DryRun)
	}
	if got := run.Steps[4].Message; !strings.HasPrefix(got, "ready after 2 checks") {
		t.Errorf("health message = %q, want ready after 2 checks", got)
	}
}

func TestDeployFailureSkipsEveryLaterStep(t *testing.T) {
	fake := &fakeApp{dryRun: true, fail: "plan_deployment:" + modelServiceID}
	e, _ := newEngine(t, fake)

	run := execute(t, e, WorkflowDeploy, DeployInput{AppID: applicationID, DeploymentName: "demo-webui"})

	expectShape(t, run, "get_ai_app:succeeded list_deployments:succeeded "+
		"plan_deployment:failed review_deployment:skipped deploy_ai_app:skipped check_serving_health:skipped "+
		"plan_deployment:skipped review_deployment:skipped deploy_ai_app:skipped check_serving_health:skipped")
	if run.Status != RunFailed || !strings.Contains(run.Steps[2].Error, "refused by the fake AI-App") {
		t.Errorf("run = %s, failed step error %q", run.Status, run.Steps[2].Error)
	}
	if got := run.Steps[9].Message; got != "not run because step 3 failed" {
		t.Errorf("skipped message = %q", got)
	}
	if calls := fake.calledTools(); len(calls) != 3 {
		t.Errorf("AI-App calls = %v, want get_ai_app, list_deployments and the failed plan only", calls)
	}
}

func TestDeployHealthNeverReadyFails(t *testing.T) {
	fake := &fakeApp{dryRun: false, ready: []bool{false}}
	e, _ := newEngine(t, fake)
	e.healthTimeout = 20 * time.Millisecond

	run := execute(t, e, WorkflowDeploy, DeployInput{AppID: applicationID, DeploymentName: "demo-webui"})

	expectShape(t, run, "get_ai_app:succeeded list_deployments:succeeded "+
		"plan_deployment:succeeded review_deployment:succeeded deploy_ai_app:succeeded check_serving_health:failed "+
		"plan_deployment:skipped review_deployment:skipped deploy_ai_app:skipped check_serving_health:skipped")
}

func TestHealReadyEndsAfterOneStep(t *testing.T) {
	fake := &fakeApp{dryRun: false, ready: []bool{true}}
	e, _ := newEngine(t, fake)

	run := execute(t, e, WorkflowHeal, HealInput{DeploymentID: "vllm-01"})

	expectShape(t, run, "check_serving_health:succeeded")
	if run.Status != RunSucceeded || !strings.Contains(run.Message, "nothing to heal") {
		t.Errorf("run = %s %q", run.Status, run.Message)
	}
}

func TestHealNotReadyUpdatesOnceAndChecksAgain(t *testing.T) {
	fake := &fakeApp{dryRun: false, ready: []bool{false, true}}
	e, _ := newEngine(t, fake)

	run := execute(t, e, WorkflowHeal, HealInput{DeploymentID: "vllm-01"})

	expectShape(t, run, "check_serving_health:succeeded update_deployment:succeeded check_serving_health:succeeded")
	if args := run.Steps[1].Input.(map[string]string); args["appId"] != modelServiceID {
		t.Errorf("update input = %v, want the appId the health check reported", args)
	}
	if run.Status != RunSucceeded {
		t.Errorf("run = %s %q", run.Status, run.Message)
	}
}

func TestHealUpdateOnNoNodeFails(t *testing.T) {
	fake := &fakeApp{dryRun: false, ready: []bool{false}, updateNodes: []bool{false, false}}
	e, _ := newEngine(t, fake)

	run := execute(t, e, WorkflowHeal, HealInput{DeploymentID: "vllm-01", AppID: modelServiceID})

	expectShape(t, run, "check_serving_health:succeeded update_deployment:failed check_serving_health:skipped")
	if got := run.Steps[1].Error; got != "applied on 0 of 2 nodes" {
		t.Errorf("update error = %q, want applied on 0 of 2 nodes", got)
	}
	if run.Status != RunFailed || !strings.Contains(run.Message, "applied on 0 of 2 nodes") {
		t.Errorf("run = %s %q", run.Status, run.Message)
	}
	healthCalls := 0
	for _, call := range fake.calledTools() {
		if strings.HasPrefix(call, "check_serving_health") {
			healthCalls++
		}
	}
	if healthCalls != 1 {
		t.Errorf("health checked %d times, want only before the update", healthCalls)
	}
}

func TestHealPartialUpdateSaysSo(t *testing.T) {
	const partial = "applied on 1 of 2 nodes (partial success, failed on n2)"

	fake := &fakeApp{dryRun: false, ready: []bool{false, true}, updateNodes: []bool{true, false}}
	e, _ := newEngine(t, fake)
	run := execute(t, e, WorkflowHeal, HealInput{DeploymentID: "vllm-01", AppID: modelServiceID})

	expectShape(t, run, "check_serving_health:succeeded update_deployment:succeeded check_serving_health:succeeded")
	if got := run.Steps[1].Message; !strings.HasPrefix(got, partial) {
		t.Errorf("update message = %q, want it to start with %q", got, partial)
	}
	if run.Status != RunSucceeded || !strings.Contains(run.Message, partial) {
		t.Errorf("run = %s %q, want succeeded naming %q", run.Status, run.Message, partial)
	}

	fake = &fakeApp{dryRun: false, ready: []bool{false}, updateNodes: []bool{true, false}}
	e, _ = newEngine(t, fake)
	e.healthTimeout = 20 * time.Millisecond
	run = execute(t, e, WorkflowHeal, HealInput{DeploymentID: "vllm-01", AppID: modelServiceID})

	expectShape(t, run, "check_serving_health:succeeded update_deployment:succeeded check_serving_health:failed")
	if run.Status != RunFailed || !strings.HasPrefix(run.Message, "step 3 ") ||
		!strings.HasSuffix(run.Message, "; the update "+partial) {
		t.Errorf("run = %s %q, want the health failure and the partial update", run.Status, run.Message)
	}
}

func TestHealUnderDryRunBlocksUpdate(t *testing.T) {
	fake := &fakeApp{dryRun: true, ready: []bool{false}}
	e, _ := newEngine(t, fake)

	run := execute(t, e, WorkflowHeal, HealInput{DeploymentID: "vllm-01", AppID: modelServiceID})

	expectShape(t, run, "check_serving_health:succeeded update_deployment:blocked check_serving_health:waiting")
	if run.Status != RunBlocked {
		t.Errorf("run = %s", run.Status)
	}
}

func TestControlAndStatus(t *testing.T) {
	fake := &fakeApp{dryRun: true, ready: []bool{true}, fail: "probe_accelerator"}
	e, _ := newEngine(t, fake)

	run := execute(t, e, WorkflowControl, ControlInput{DeploymentID: "vllm-01", Action: "suspend"})
	expectShape(t, run, "control_deployment:blocked")

	if _, err := e.Execute(context.Background(), "ns", WorkflowControl,
		json.RawMessage(`{"deploymentId":"vllm-01","action":"delete"}`)); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("control with action delete: err = %v, want invalid input", err)
	}

	run = execute(t, e, WorkflowStatus, StatusInput{DeploymentID: "vllm-01"})
	expectShape(t, run, "get_deployment_status:succeeded check_serving_health:succeeded probe_accelerator:failed")
	if run.Status != RunFailed || !strings.Contains(run.Message, "serving: ready=true") ||
		!strings.Contains(run.Message, "accelerator: probe_accelerator refused") {
		t.Errorf("status run = %s %q", run.Status, run.Message)
	}
}

func TestExecuteRejectsBadRequests(t *testing.T) {
	e, _ := newEngine(t, &fakeApp{dryRun: true})
	cases := map[string]struct {
		workflow string
		input    string
		want     error
	}{
		"unknown":       {"rollout", `{}`, ErrUnknownWorkflow},
		"no app":        {WorkflowDeploy, `{"deploymentName":"x"}`, ErrInvalidInput},
		"no name":       {WorkflowDeploy, `{"appId":"x"}`, ErrInvalidInput},
		"malformed":     {WorkflowHeal, `{"deploymentId":`, ErrInvalidInput},
		"no deployment": {WorkflowStatus, `{}`, ErrInvalidInput},
	}
	for name, tc := range cases {
		if _, err := e.Execute(context.Background(), "ns", tc.workflow, json.RawMessage(tc.input)); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", name, err, tc.want)
		}
	}
}

func TestUnreachableAppServerRunsNothing(t *testing.T) {
	runs, _ := archive.NewLog[Run](t.TempDir())
	client := mcpclient.New("http://127.0.0.1:1/aiapp/mcp", "")
	e := New(client, runs)

	_, err := e.Execute(context.Background(), "ns", WorkflowStatus, json.RawMessage(`{"deploymentId":"x"}`))
	if !errors.Is(err, mcpclient.ErrUnavailable) {
		t.Errorf("err = %v, want unavailable", err)
	}
	if !e.DryRun(context.Background()) {
		t.Error("DryRun with no AI-App service = false, want true")
	}
}

func TestRunsAreArchived(t *testing.T) {
	fake := &fakeApp{dryRun: true, ready: []bool{true}}
	e, dir := newEngine(t, fake)

	first := execute(t, e, WorkflowDeploy, DeployInput{AppID: modelServiceID, DeploymentName: "demo-vllm"})
	second := execute(t, e, WorkflowHeal, HealInput{DeploymentID: "demo-vllm"})

	runs, err := e.ListRuns(0)
	if err != nil || len(runs) != 2 || runs[0].RunID != second.RunID || runs[1].RunID != first.RunID {
		t.Fatalf("ListRuns = %d runs, err %v, want the two runs newest first", len(runs), err)
	}
	got, err := e.GetRun(first.RunID)
	if err != nil || got.Workflow != WorkflowDeploy || len(got.Steps) != 5 || got.Steps[3].Status != StepBlocked {
		t.Fatalf("GetRun = %+v, %v", got, err)
	}
	if len(got.Steps[2].Output) == 0 || !strings.Contains(string(got.Steps[2].Output), recommendedSpec) {
		t.Errorf("archived review output = %s, want the tool result", got.Steps[2].Output)
	}
	if _, err := e.GetRun("eng-missing"); !errors.Is(err, ErrRunNotFound) {
		t.Errorf("GetRun missing: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "runs.jsonl"))
	if err != nil || strings.Count(string(raw), "\n") != 2 {
		t.Errorf("runs.jsonl = %d bytes, err %v, want two lines", len(raw), err)
	}
}

func TestEngineMCPPublishesWorkflowAndCatalogToolsWithGrades(t *testing.T) {
	fake := &fakeApp{dryRun: true, ready: []bool{true}}
	e, _ := newEngine(t, fake)
	srv := httptest.NewServer(mcpserver.NewNamedHandler(MCPServerName, NewRegistry(e)))
	t.Cleanup(srv.Close)

	header := http.Header{mcpserver.NamespaceHeader: []string{"ns-mcp"}}
	transport := &mcp.StreamableClientTransport{
		Endpoint:   srv.URL,
		HTTPClient: &http.Client{Transport: headerTransport{header: header, base: http.DefaultTransport}},
	}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).
		Connect(context.Background(), transport, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	init := session.InitializeResult()
	if init.ServerInfo.Name != MCPServerName || init.Meta[mcpserver.MetaDryRun] != true {
		t.Errorf("initialize = %s, dryRun %v", init.ServerInfo.Name, init.Meta[mcpserver.MetaDryRun])
	}

	res, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	want := map[string]string{ToolDeploy: "write", ToolHeal: "write", ToolControl: "write", ToolStatus: "read",
		ToolApplications: "read"}
	if len(res.Tools) != len(want) {
		t.Fatalf("tools/list returned %d tools, want %d", len(res.Tools), len(want))
	}
	for _, tool := range res.Tools {
		if got := tool.Meta[mcpserver.MetaGrade]; got != want[tool.Name] {
			t.Errorf("%s grade = %v, want %s", tool.Name, got, want[tool.Name])
		}
		if guarded := tool.Meta[mcpserver.MetaGuardedByDryRun]; guarded != (want[tool.Name] == "write") {
			t.Errorf("%s guardedByDryRun = %v", tool.Name, guarded)
		}
	}

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: ToolStatus, Arguments: map[string]any{"deploymentId": "vllm-01"}})
	if err != nil || result.IsError {
		t.Fatalf("tools/call %s: %v %+v", ToolStatus, err, result)
	}
	var run Run
	if err := json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &run); err != nil {
		t.Fatalf("decode run: %v", err)
	}
	if run.Workflow != WorkflowStatus || run.Namespace != "ns-mcp" || len(run.Steps) != 3 {
		t.Errorf("run over MCP = %s in %s with %d steps", run.Workflow, run.Namespace, len(run.Steps))
	}
	for _, ns := range fake.ns {
		if ns != "ns-mcp" {
			t.Errorf("AI-App saw namespace %q, want ns-mcp", ns)
		}
	}
}

func TestListApplicationsPicksTheCatalogTool(t *testing.T) {
	fake := &fakeApp{dryRun: true}
	e, dir := newEngine(t, fake)
	registry := NewRegistry(e)

	if registry.Grade(ToolApplications) != tools.GradeRead {
		t.Errorf("%s grade = %q, want read", ToolApplications, registry.Grade(ToolApplications))
	}

	out, err := registry.Call(context.Background(), "ns-cat", ToolApplications,
		json.RawMessage(`{"query":"Open WebUI chatbot","limit":3}`))
	if err != nil {
		t.Fatalf("lookup with query: %v", err)
	}
	raw, _ := json.Marshal(out)
	if !strings.Contains(string(raw), applicationID) || !strings.Contains(string(raw), `"query":"Open WebUI chatbot"`) ||
		!strings.Contains(string(raw), `"limit":3`) {
		t.Errorf("lookup with query = %s", raw)
	}

	out, err = registry.Call(context.Background(), "ns-cat", ToolApplications, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("lookup without query: %v", err)
	}
	raw, _ = json.Marshal(out)
	if !strings.Contains(string(raw), `"apps"`) || !strings.Contains(string(raw), modelServiceID) {
		t.Errorf("lookup without query = %s", raw)
	}

	if calls := strings.Join(fake.calledTools(), ","); calls != "find_ai_apps:,list_ai_apps:" {
		t.Errorf("AI-App calls = %s, want find_ai_apps then list_ai_apps", calls)
	}
	for _, ns := range fake.ns {
		if ns != "ns-cat" {
			t.Errorf("AI-App saw namespace %q, want ns-cat", ns)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "runs.jsonl")); !os.IsNotExist(err) {
		t.Errorf("a catalog lookup archived a run: %v", err)
	}
}

func TestCardSaysNoLLM(t *testing.T) {
	fake := &fakeApp{dryRun: true}
	e, _ := newEngine(t, fake)
	if err := e.app.Prepare(context.Background()); err != nil {
		t.Fatalf("prepare: %v", err)
	}

	card := NewCardBuilder(e, NewRegistry(e), nil).Build()

	if card.UsesLLM || card.Name != CardName || card.Owner != "innogrid" || card.Framework != "AI-Ops" ||
		card.ServesFramework != "AI-App" || !card.DryRun {
		t.Errorf("card identity = %+v", card)
	}
	// The fake AI-App publishes 13 tools; the engine calls 12 of them.
	if !card.MCP.AppServer.Connected || card.MCP.AppServer.ToolCount != 13 || len(card.Tools.Uses) != len(appToolsUsed) {
		t.Errorf("card mcp = %+v, uses %d", card.MCP, len(card.Tools.Uses))
	}
	for i, tool := range card.Tools.Uses {
		if tool.Name != appToolsUsed[i] || tool.Grade == "" {
			t.Errorf("uses[%d] = %+v, want %s with its grade", i, tool, appToolsUsed[i])
		}
		if tool.Name == "delete_ai_app" {
			t.Errorf("card lists %s, which the engine never calls", tool.Name)
		}
		if guarded := tool.Name == toolDeployApp || tool.Name == toolUpdateDeployment ||
			tool.Name == toolControlDeployment; tool.GuardedByDryRun != guarded {
			t.Errorf("uses %s guardedByDryRun = %t", tool.Name, tool.GuardedByDryRun)
		}
	}
	if len(card.Tools.Exposed) != 5 || len(card.Workflows) != 4 {
		t.Errorf("card exposes %d tools and %d workflows, want 5 and 4", len(card.Tools.Exposed), len(card.Workflows))
	}
	if card.Registration == nil || card.Registration.Status != "disabled" ||
		card.Registration.Message != RegistryEnv+" is not set" {
		t.Errorf("registration = %+v", card.Registration)
	}
	body, _ := json.Marshal(card)
	if !strings.Contains(string(body), `"usesLLM":false`) {
		t.Errorf("card JSON has no usesLLM false: %s", body)
	}
}

// headerTransport adds fixed headers to every request.
type headerTransport struct {
	header http.Header
	base   http.RoundTripper
}

func (t headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	for k, v := range t.header {
		req.Header[k] = v
	}
	return t.base.RoundTrip(req)
}

func TestCardListsUsedToolsWithoutGradesWhenNotConnected(t *testing.T) {
	runs, _ := archive.NewLog[Run](t.TempDir())
	e := New(mcpclient.New("http://127.0.0.1:1/aiapp/mcp", ""), runs)

	card := NewCardBuilder(e, NewRegistry(e), nil).Build()

	if card.MCP.AppServer.Connected || card.MCP.AppServer.ToolCount != 0 || !card.DryRun {
		t.Errorf("card mcp = %+v dryRun %t, want disconnected and gated", card.MCP, card.DryRun)
	}
	if len(card.Tools.Uses) != len(appToolsUsed) {
		t.Fatalf("uses %d tools, want %d", len(card.Tools.Uses), len(appToolsUsed))
	}
	for i, tool := range card.Tools.Uses {
		if tool.Name != appToolsUsed[i] || tool.Grade != "" || tool.GuardedByDryRun {
			t.Errorf("uses[%d] = %+v, want %s without a grade", i, tool, appToolsUsed[i])
		}
	}
}

func TestAppToolsUsedCoversEveryCall(t *testing.T) {
	used := map[string]bool{}
	for _, name := range appToolsUsed {
		used[name] = true
	}
	if !sort.StringsAreSorted(appToolsUsed) || len(used) != len(appToolsUsed) {
		t.Errorf("appToolsUsed = %v, want sorted and unique", appToolsUsed)
	}

	fake := &fakeApp{dryRun: false, ready: []bool{false, true}}
	e, _ := newEngine(t, fake)
	execute(t, e, WorkflowDeploy, DeployInput{AppID: applicationID, DeploymentName: "demo-webui"})
	// Not ready first, so the heal goes on to the update.
	fake.mu.Lock()
	fake.ready = []bool{false, true}
	fake.mu.Unlock()
	execute(t, e, WorkflowHeal, HealInput{DeploymentID: "demo-webui"})
	execute(t, e, WorkflowControl, ControlInput{DeploymentID: "demo-webui", Action: "reboot"})
	execute(t, e, WorkflowStatus, StatusInput{DeploymentID: "demo-webui"})
	registry := NewRegistry(e)
	for _, input := range []string{`{}`, `{"query":"chat"}`} {
		if _, err := registry.Call(context.Background(), "ns-test", ToolApplications, json.RawMessage(input)); err != nil {
			t.Fatalf("lookup %s: %v", input, err)
		}
	}

	called := map[string]bool{}
	for _, call := range fake.calledTools() {
		name, _, _ := strings.Cut(call, ":")
		called[name] = true
		if !used[name] {
			t.Errorf("the engine called %s, which appToolsUsed does not list", name)
		}
	}
	for name := range used {
		if !called[name] {
			t.Errorf("appToolsUsed lists %s, which no workflow or lookup called", name)
		}
	}
}
