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
)

const (
	modelID    = "qwen3-vl-8b"
	artifactID = "ollama-gguf"
	appName    = "open-webui"
)

// fakeApp is an AI-App MCP server whose tool answers the test controls.
type fakeApp struct {
	mu     sync.Mutex
	dryRun bool
	// progress is the status each get_deployment read of a created deployment
	// answers in turn; the last one repeats. Empty answers succeeded.
	progress []string
	// records are deployments that exist before the test, by id.
	records map[string]model.NodeDeployment
	// created counts create_deployment calls; ids are dep-1, dep-2 and so on.
	created int
	// control is the status control_node gives every node. Empty answers
	// blocked under dry run and done otherwise.
	control string
	// fail names a tool that answers with an error.
	fail  string
	calls []string
	ns    []string
	// requests keeps the input of every create_deployment call.
	requests []model.NodeDeploymentReq
}

func (f *fakeApp) record(nsID, tool string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, tool)
	f.ns = append(f.ns, nsID)
	if f.fail == tool {
		return fmt.Errorf("%s refused by the fake AI-App", tool)
	}
	return nil
}

func (f *fakeApp) calledTools() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.calls...)
}

func (f *fakeApp) count(tool string) int {
	n := 0
	for _, call := range f.calledTools() {
		if call == tool {
			n++
		}
	}
	return n
}

func (f *fakeApp) nextStatus() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.progress) == 0 {
		return model.NodeDeploySucceeded
	}
	status := f.progress[0]
	if len(f.progress) > 1 {
		f.progress = f.progress[1:]
	}
	return status
}

// deployment builds a record of req whose nodes match status: a partial one
// succeeded on the first node only.
func deployment(id, status string, dryRun bool, req model.NodeDeploymentReq) model.NodeDeployment {
	d := model.NodeDeployment{ID: id, Type: req.Type, Status: status, DryRun: dryRun, Request: req}
	for i, t := range req.Targets {
		n := model.NodeDeployResult{Target: t, Status: status}
		switch status {
		case model.NodeDeploySucceeded:
			n.ServiceURL = "http://10.0.0." + fmt.Sprint(i+1) + ":11434/v1"
		case model.NodeDeployPartial:
			n.Status = model.NodeDeployFailed
			if i == 0 {
				n.Status = model.NodeDeploySucceeded
			}
		}
		if n.Status == model.NodeDeployFailed {
			n.Error = "compose-up.yml: task ended with status error"
		}
		d.Nodes = append(d.Nodes, n)
	}
	return d
}

func (f *fakeApp) tool(name string, grade tools.Grade, handle func(input json.RawMessage) (any, error)) tools.Tool {
	return tools.Tool{
		Name:        name,
		Grade:       grade,
		Description: "fake " + name,
		InputSchema: map[string]any{"type": "object"},
		Handle: func(ctx context.Context, nsID string, input json.RawMessage) (any, error) {
			if err := f.record(nsID, name); err != nil {
				return nil, err
			}
			return handle(input)
		},
	}
}

func (f *fakeApp) registry() *tools.Registry {
	registry := tools.NewRegistry(f.dryRun)
	qwen := model.Model{ID: modelID, Name: "Qwen3-VL-8B-Instruct", Tasks: []string{"vision-chat"},
		InputModalities: []string{"text", "image"}, Artifacts: []model.Artifact{
			{ID: "hf-bf16", Source: "huggingface", Ref: "Qwen/Qwen3-VL-8B-Instruct", Format: "hf",
				Engines: []model.EngineCompat{{Engine: "vllm"}}},
			{ID: artifactID, Source: "ollama", Ref: "qwen3-vl:8b", Format: "gguf",
				Engines: []model.EngineCompat{{Engine: "ollama"}}},
		}}
	webui := model.App{Name: appName, Versions: []model.AppVersion{
		{Version: 1, Script: "docker compose up -d", Files: []model.AppFile{{Name: "docker-compose.yml"}}},
		{Version: 2, Script: "docker compose up -d", Files: []model.AppFile{{Name: "docker-compose.yml"}, {Name: "x"}}},
	}}
	idOf := func(input json.RawMessage) string {
		var a struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(input, &a)
		return a.ID
	}
	find := func(id string) (model.NodeDeployment, bool) {
		f.mu.Lock()
		defer f.mu.Unlock()
		d, ok := f.records[id]
		return d, ok
	}

	registry.Add(f.tool("get_model", tools.GradeRead, func(input json.RawMessage) (any, error) {
		var a struct {
			ModelID string `json:"modelId"`
		}
		_ = json.Unmarshal(input, &a)
		if a.ModelID != modelID {
			return nil, errors.New("model not found")
		}
		return qwen, nil
	}))
	registry.Add(f.tool("get_app", tools.GradeRead, func(input json.RawMessage) (any, error) {
		var a struct {
			Name    string `json:"name"`
			Version int    `json:"version"`
		}
		_ = json.Unmarshal(input, &a)
		if a.Name != appName || a.Version > len(webui.Versions) {
			return nil, errors.New("application not found")
		}
		if a.Version > 0 {
			return webui.Versions[a.Version-1], nil
		}
		return webui, nil
	}))
	registry.Add(f.tool("create_deployment", tools.GradeWrite, func(input json.RawMessage) (any, error) {
		var req model.NodeDeploymentReq
		if err := json.Unmarshal(input, &req); err != nil {
			return nil, err
		}
		f.mu.Lock()
		f.created++
		id := fmt.Sprintf("dep-%d", f.created)
		f.requests = append(f.requests, req)
		f.mu.Unlock()
		if f.dryRun {
			return deployment(id, model.NodeDeployDryRun, true, req), nil
		}
		d := deployment(id, model.NodeDeployPending, false, req)
		f.mu.Lock()
		if f.records == nil {
			f.records = map[string]model.NodeDeployment{}
		}
		f.records[id] = d
		f.mu.Unlock()
		return d, nil
	}))
	registry.Add(f.tool("get_deployment", tools.GradeRead, func(input json.RawMessage) (any, error) {
		id := idOf(input)
		d, ok := find(id)
		if !ok {
			return nil, errors.New("deployment not found: " + id)
		}
		if strings.HasPrefix(id, "dep-") {
			return deployment(id, f.nextStatus(), false, d.Request), nil
		}
		return d, nil
	}))
	registry.Add(f.tool("get_deployment_logs", tools.GradeRead, func(input json.RawMessage) (any, error) {
		d, ok := find(idOf(input))
		if !ok {
			return nil, errors.New("deployment not found")
		}
		logs := model.NodeDeploymentLogs{ID: d.ID, Status: d.Status}
		for _, n := range d.Nodes {
			logs.Nodes = append(logs.Nodes, model.NodeDeployNodeLogs{Target: n.Target, Status: n.Status,
				Steps: []model.NodeDeployStep{
					{Playbook: "install-docker.yml", Status: "success", Output: []string{"docker ok"}},
					{Playbook: "compose-up.yml", Status: "success",
						Output: []string{"l1", "l2", "l3", "l4", "l5", "l6", "PLAY RECAP ok=6 failed=0"}},
				}})
		}
		return logs, nil
	}))
	registry.Add(f.tool("control_node", tools.GradeWrite, func(input json.RawMessage) (any, error) {
		var a struct {
			ID     string `json:"id"`
			Action string `json:"action"`
		}
		_ = json.Unmarshal(input, &a)
		d, ok := find(a.ID)
		if !ok {
			return nil, errors.New("deployment not found")
		}
		status := f.control
		if status == "" {
			status = "done"
			if f.dryRun {
				status = "blocked"
			}
		}
		resp := model.NodeControlResp{ID: a.ID, Action: a.Action}
		for _, n := range d.Nodes {
			resp.Nodes = append(resp.Nodes, model.NodeControlResult{Target: n.Target, Status: status,
				Message: "fake " + status})
		}
		return resp, nil
	}))
	registry.Add(f.tool("list_models", tools.GradeRead, func(input json.RawMessage) (any, error) {
		var a map[string]string
		_ = json.Unmarshal(input, &a)
		if a["modality"] != "" && a["modality"] != "image" {
			return model.ModelListResp{Models: []model.Model{}}, nil
		}
		return model.ModelListResp{Models: []model.Model{qwen}}, nil
	}))
	registry.Add(f.tool("list_apps", tools.GradeRead, func(json.RawMessage) (any, error) {
		return model.AppListResp{Apps: []model.App{webui}}, nil
	}))
	// The engine never calls this one, so its card must not list it.
	registry.Add(f.tool("list_node_deployments", tools.GradeRead, func(json.RawMessage) (any, error) {
		return model.NodeDeploymentListResp{}, nil
	}))
	return registry
}

func target(nodeID string) model.NodeDeployTarget {
	return model.NodeDeployTarget{Kind: model.NodeTargetKindNode, NsID: "etri", InfraID: "travel", NodeID: nodeID}
}

func modelRequest(nodes ...string) DeployInput {
	req := DeployInput{Type: model.NodeDeployTypeModelService,
		ModelService: &model.NodeModelServiceReq{ModelID: modelID, ArtifactID: artifactID, Engine: "ollama"}}
	for _, n := range nodes {
		req.Targets = append(req.Targets, target(n))
	}
	return req
}

func appRequest(version int, nodes ...string) DeployInput {
	req := DeployInput{Type: model.NodeDeployTypeApp, App: &model.NodeAppReq{Name: appName, Version: version,
		Env: map[string]string{"OLLAMA_BASE_URL": "http://10.0.0.1:11434"}}}
	for _, n := range nodes {
		req.Targets = append(req.Targets, target(n))
	}
	return req
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
	return New(client, runs, WithDeploymentWait(time.Second, time.Millisecond)), dir
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

func TestDeployModelServiceDryRunBlocksCreateAndHoldsWait(t *testing.T) {
	fake := &fakeApp{dryRun: true}
	e, _ := newEngine(t, fake)

	run := execute(t, e, WorkflowDeploy, modelRequest("vm2"))

	expectShape(t, run, "get_model:succeeded create_deployment:blocked get_deployment:waiting")
	if run.Status != RunBlocked || !run.DryRun || !strings.Contains(run.Message, "1 later steps wait") {
		t.Errorf("run status = %s dryRun = %t message %q, want blocked under dry run", run.Status, run.DryRun, run.Message)
	}
	if got := run.Steps[0].Message; got != "artifact ollama-gguf: ollama qwen3-vl:8b (gguf), engines ollama" {
		t.Errorf("read step message = %q", got)
	}
	created := run.Steps[1]
	if created.StartedAt == nil || !strings.Contains(created.Message, "dep-1 is a dry run") {
		t.Errorf("blocked step = %+v, want it run and explained", created)
	}
	wait := run.Steps[2]
	if wait.Message != "waits for create_deployment to run (blocked by dry run)" || wait.StartedAt != nil {
		t.Errorf("wait step message = %q started = %v, want it held and not run", wait.Message, wait.StartedAt)
	}
	if in := wait.Input.(map[string]string); in["id"] != "dep-1" {
		t.Errorf("wait step input = %v", in)
	}
	if fake.count("get_deployment") != 0 {
		t.Errorf("the deployment was followed under dry run: %v", fake.calledTools())
	}
	if len(fake.requests) != 1 || fake.requests[0].ModelService.ArtifactID != artifactID ||
		fake.requests[0].Targets[0].NodeID != "vm2" {
		t.Errorf("create_deployment input = %+v, want the request as given", fake.requests)
	}
	for _, ns := range fake.ns {
		if ns != "ns-test" {
			t.Errorf("namespace header = %q, want ns-test", ns)
		}
	}
}

func TestDeployAppDryRun(t *testing.T) {
	fake := &fakeApp{dryRun: true}
	e, _ := newEngine(t, fake)

	run := execute(t, e, WorkflowDeploy, appRequest(0, "vm2"))
	expectShape(t, run, "get_app:succeeded create_deployment:blocked get_deployment:waiting")
	if got := run.Steps[0].Message; got != "latest version 2, 2 files" {
		t.Errorf("read step message = %q", got)
	}

	run = execute(t, e, WorkflowDeploy, appRequest(1, "vm2"))
	expectShape(t, run, "get_app:succeeded create_deployment:blocked get_deployment:waiting")
	if got := run.Steps[0].Message; got != "version 1, 1 files" {
		t.Errorf("read step message = %q", got)
	}
}

func TestDeploySucceeds(t *testing.T) {
	fake := &fakeApp{progress: []string{"running", "running", "succeeded"}}
	e, _ := newEngine(t, fake)

	run := execute(t, e, WorkflowDeploy, modelRequest("vm1", "vm2"))

	expectShape(t, run, "get_model:succeeded create_deployment:succeeded get_deployment:succeeded")
	if run.Status != RunSucceeded || run.DryRun || run.Message != "deployment dep-1 succeeded on every node" {
		t.Errorf("run = %s dryRun %t %q", run.Status, run.DryRun, run.Message)
	}
	wait := run.Steps[2]
	if !strings.HasPrefix(wait.Message, "deployment dep-1 succeeded, 2 of 2 nodes succeeded; serving at "+
		"etri/travel/vm1 http://10.0.0.1:11434/v1") || !strings.Contains(wait.Message, "after 3 reads") {
		t.Errorf("wait message = %q", wait.Message)
	}
	if fake.count("get_deployment") != 3 {
		t.Errorf("get_deployment called %d times, want 3", fake.count("get_deployment"))
	}
}

func TestDeployFailedAndPartialFailTheRun(t *testing.T) {
	for status, want := range map[string]string{
		model.NodeDeployFailed:  "deployment dep-1 failed, 0 of 2 nodes succeeded; not succeeded on etri/travel/vm1 failed: compose-up.yml",
		model.NodeDeployPartial: "deployment dep-1 partial, 1 of 2 nodes succeeded; not succeeded on etri/travel/vm2 failed: compose-up.yml",
	} {
		fake := &fakeApp{progress: []string{"running", status}}
		e, _ := newEngine(t, fake)

		run := execute(t, e, WorkflowDeploy, modelRequest("vm1", "vm2"))

		expectShape(t, run, "get_model:succeeded create_deployment:succeeded get_deployment:failed")
		if run.Status != RunFailed || !strings.HasPrefix(run.Steps[2].Error, want) {
			t.Errorf("%s: run = %s, wait error %q, want prefix %q", status, run.Status, run.Steps[2].Error, want)
		}
	}
}

func TestDeployStopsAtThePollLimit(t *testing.T) {
	fake := &fakeApp{progress: []string{"running"}}
	e, _ := newEngine(t, fake)
	e.waitTimeout, e.waitInterval = 5*time.Millisecond, time.Millisecond

	run := execute(t, e, WorkflowDeploy, modelRequest("vm1"))

	expectShape(t, run, "get_model:succeeded create_deployment:succeeded get_deployment:failed")
	if got := fake.count("get_deployment"); got != 6 {
		t.Errorf("get_deployment called %d times, want 6 (5ms / 1ms + 1)", got)
	}
	if err := run.Steps[2].Error; !strings.HasPrefix(err, "still running after 6 reads") ||
		!strings.Contains(err, ToolStatus) {
		t.Errorf("wait error = %q", err)
	}
}

func TestDeployUnknownArtifactSkipsEveryLaterStep(t *testing.T) {
	fake := &fakeApp{}
	e, _ := newEngine(t, fake)
	req := modelRequest("vm1")
	req.ModelService.ArtifactID = "gguf-q8"

	run := execute(t, e, WorkflowDeploy, req)

	expectShape(t, run, "get_model:failed create_deployment:skipped get_deployment:skipped")
	if got := run.Steps[0].Error; got != "model qwen3-vl-8b has no artifact gguf-q8; it has hf-bf16, ollama-gguf" {
		t.Errorf("read error = %q", got)
	}
	if got := run.Steps[2].Message; got != "not run because step 1 failed" {
		t.Errorf("skipped message = %q", got)
	}
	if calls := fake.calledTools(); len(calls) != 1 {
		t.Errorf("AI-App calls = %v, want get_model only", calls)
	}
}

func TestDeployCreateFailureSkipsWait(t *testing.T) {
	fake := &fakeApp{fail: "create_deployment"}
	e, _ := newEngine(t, fake)

	run := execute(t, e, WorkflowDeploy, appRequest(0, "vm1"))

	expectShape(t, run, "get_app:succeeded create_deployment:failed get_deployment:skipped")
	if run.Status != RunFailed || !strings.Contains(run.Steps[1].Error, "refused by the fake AI-App") {
		t.Errorf("run = %s, create error %q", run.Status, run.Steps[1].Error)
	}
}

func TestHealSucceededDoesNothing(t *testing.T) {
	fake := &fakeApp{records: map[string]model.NodeDeployment{
		"old": deployment("old", model.NodeDeploySucceeded, false, modelRequest("vm1"))}}
	e, _ := newEngine(t, fake)

	run := execute(t, e, WorkflowHeal, HealInput{DeploymentID: "old"})

	expectShape(t, run, "get_deployment:succeeded")
	if run.Status != RunSucceeded || !strings.Contains(run.Message, "nothing to heal") {
		t.Errorf("run = %s %q", run.Status, run.Message)
	}
	if fake.count("create_deployment") != 0 {
		t.Errorf("a succeeded deployment was run again: %v", fake.calledTools())
	}
}

func TestHealRunsTheSameRequestAgainOnce(t *testing.T) {
	for _, status := range []string{model.NodeDeployFailed, model.NodeDeployPartial, model.NodeDeployInterrupted} {
		original := appRequest(2, "vm1", "vm2")
		fake := &fakeApp{progress: []string{"running", "succeeded"}, records: map[string]model.NodeDeployment{
			"old": deployment("old", status, false, original)}}
		e, _ := newEngine(t, fake)

		run := execute(t, e, WorkflowHeal, HealInput{DeploymentID: "old"})

		expectShape(t, run, "get_deployment:succeeded create_deployment:succeeded get_deployment:succeeded")
		if run.Status != RunSucceeded || run.Message !=
			"deployment old was "+status+" and succeeded on every node when run again as dep-1" {
			t.Errorf("%s: run = %s %q", status, run.Status, run.Message)
		}
		if len(fake.requests) != 1 || fake.requests[0].App.Version != 2 || len(fake.requests[0].Targets) != 2 ||
			fake.requests[0].App.Env["OLLAMA_BASE_URL"] == "" {
			t.Errorf("%s: create input = %+v, want the original request", status, fake.requests)
		}
	}
}

func TestHealAgainFailedFailsTheRun(t *testing.T) {
	fake := &fakeApp{progress: []string{"failed"}, records: map[string]model.NodeDeployment{
		"old": deployment("old", model.NodeDeployFailed, false, modelRequest("vm1"))}}
	e, _ := newEngine(t, fake)

	run := execute(t, e, WorkflowHeal, HealInput{DeploymentID: "old"})

	expectShape(t, run, "get_deployment:succeeded create_deployment:succeeded get_deployment:failed")
	if run.Status != RunFailed || !strings.HasSuffix(run.Message, "; deployment old was failed before") {
		t.Errorf("run = %s %q", run.Status, run.Message)
	}
	if fake.count("create_deployment") != 1 {
		t.Errorf("create_deployment called %d times, want once", fake.count("create_deployment"))
	}
}

func TestHealLeavesARunningDeployment(t *testing.T) {
	fake := &fakeApp{records: map[string]model.NodeDeployment{
		"old": deployment("old", model.NodeDeployRunning, false, modelRequest("vm1"))}}
	e, _ := newEngine(t, fake)

	run := execute(t, e, WorkflowHeal, HealInput{DeploymentID: "old"})

	expectShape(t, run, "get_deployment:failed create_deployment:skipped get_deployment:skipped")
	if got := run.Steps[0].Error; got != "deployment old is still running; heal it after it ends" {
		t.Errorf("read error = %q", got)
	}
}

func TestHealUnderDryRunBlocksCreate(t *testing.T) {
	fake := &fakeApp{dryRun: true, records: map[string]model.NodeDeployment{
		"old": deployment("old", model.NodeDeployFailed, false, modelRequest("vm1"))}}
	e, _ := newEngine(t, fake)

	run := execute(t, e, WorkflowHeal, HealInput{DeploymentID: "old"})

	expectShape(t, run, "get_deployment:succeeded create_deployment:blocked get_deployment:waiting")
	if run.Status != RunBlocked {
		t.Errorf("run = %s", run.Status)
	}
}

func TestControl(t *testing.T) {
	records := map[string]model.NodeDeployment{
		"old": deployment("old", model.NodeDeploySucceeded, false, modelRequest("vm1", "vm2"))}
	cases := []struct {
		fake  *fakeApp
		shape string
		text  string
	}{
		{&fakeApp{dryRun: true}, "control_node:blocked", "reboot held on 2 of 2 nodes: fake blocked"},
		{&fakeApp{}, "control_node:succeeded", "reboot sent to 2 of 2 nodes"},
		{&fakeApp{control: "failed"}, "control_node:failed", "reboot failed on 2 of 2 nodes: etri/travel/vm1: fake failed"},
	}
	for _, tc := range cases {
		tc.fake.records = records
		e, _ := newEngine(t, tc.fake)
		run := execute(t, e, WorkflowControl, ControlInput{DeploymentID: "old", Action: "reboot"})
		expectShape(t, run, tc.shape)
		if got := run.Steps[0].Message + run.Steps[0].Error; !strings.HasPrefix(got, tc.text) {
			t.Errorf("control step = %q, want prefix %q", got, tc.text)
		}
		if in := run.Steps[0].Input.(map[string]string); in["id"] != "old" || in["action"] != "reboot" {
			t.Errorf("control input = %v", in)
		}
	}

	e, _ := newEngine(t, &fakeApp{})
	if _, err := e.Execute(context.Background(), "ns", WorkflowControl,
		json.RawMessage(`{"deploymentId":"old","action":"delete"}`)); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("control with action delete: err = %v, want invalid input", err)
	}
}

func TestStatus(t *testing.T) {
	fake := &fakeApp{records: map[string]model.NodeDeployment{
		"old": deployment("old", model.NodeDeploySucceeded, false, modelRequest("vm1"))}}
	e, _ := newEngine(t, fake)

	run := execute(t, e, WorkflowStatus, StatusInput{DeploymentID: "old"})

	expectShape(t, run, "get_deployment:succeeded get_deployment_logs:succeeded")
	want := "etri/travel/vm1 succeeded (compose-up.yml): l3 | l4 | l5 | l6 | PLAY RECAP ok=6 failed=0"
	if run.Steps[1].Message != want {
		t.Errorf("logs message = %q, want %q", run.Steps[1].Message, want)
	}
	if run.Status != RunSucceeded || !strings.HasPrefix(run.Message, "deployment old succeeded, 1 of 1 nodes") ||
		!strings.HasSuffix(run.Message, "output: "+want) {
		t.Errorf("status run = %s %q", run.Status, run.Message)
	}

	fake.mu.Lock()
	fake.fail = "get_deployment"
	fake.mu.Unlock()
	run = execute(t, e, WorkflowStatus, StatusInput{DeploymentID: "old"})
	expectShape(t, run, "get_deployment:failed get_deployment_logs:succeeded")
	if run.Status != RunFailed || !strings.HasPrefix(run.Message, "deployment: get_deployment refused") {
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
		"no type":       {WorkflowDeploy, `{"targets":[{"kind":"node","nsId":"a","infraId":"b","nodeId":"c"}]}`, ErrInvalidInput},
		"no targets":    {WorkflowDeploy, `{"type":"app","app":{"name":"open-webui"}}`, ErrInvalidInput},
		"k8s target":    {WorkflowDeploy, `{"type":"app","app":{"name":"open-webui"},"targets":[{"kind":"k8s","nsId":"a"}]}`, ErrInvalidInput},
		"no artifact":   {WorkflowDeploy, `{"type":"model-service","modelService":{"modelId":"m"},"targets":[{"kind":"node","nsId":"a","infraId":"b","nodeId":"c"}]}`, ErrInvalidInput},
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
	fake := &fakeApp{dryRun: true, records: map[string]model.NodeDeployment{
		"old": deployment("old", model.NodeDeploySucceeded, false, modelRequest("vm1"))}}
	e, dir := newEngine(t, fake)

	first := execute(t, e, WorkflowDeploy, modelRequest("vm1"))
	second := execute(t, e, WorkflowHeal, HealInput{DeploymentID: "old"})

	runs, err := e.ListRuns(0)
	if err != nil || len(runs) != 2 || runs[0].RunID != second.RunID || runs[1].RunID != first.RunID {
		t.Fatalf("ListRuns = %d runs, err %v, want the two runs newest first", len(runs), err)
	}
	got, err := e.GetRun(first.RunID)
	if err != nil || got.Workflow != WorkflowDeploy || len(got.Steps) != 3 || got.Steps[1].Status != StepBlocked {
		t.Fatalf("GetRun = %+v, %v", got, err)
	}
	if len(got.Steps[1].Output) == 0 || !strings.Contains(string(got.Steps[1].Output), `"status":"dry-run"`) {
		t.Errorf("archived create output = %s, want the tool result", got.Steps[1].Output)
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
	fake := &fakeApp{dryRun: true, records: map[string]model.NodeDeployment{
		"old": deployment("old", model.NodeDeploySucceeded, false, modelRequest("vm1"))}}
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
		if tool.Name == ToolDeploy {
			schema, _ := json.Marshal(tool.InputSchema)
			for _, field := range []string{`"required":["type","targets"]`, `"modelService"`, `"app"`, `"artifactId"`} {
				if !strings.Contains(string(schema), field) {
					t.Errorf("%s input schema misses %s: %s", ToolDeploy, field, schema)
				}
			}
		}
	}

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: ToolStatus, Arguments: map[string]any{"deploymentId": "old"}})
	if err != nil || result.IsError {
		t.Fatalf("tools/call %s: %v %+v", ToolStatus, err, result)
	}
	var run Run
	if err := json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &run); err != nil {
		t.Fatalf("decode run: %v", err)
	}
	if run.Workflow != WorkflowStatus || run.Namespace != "ns-mcp" || len(run.Steps) != 2 || run.Status != RunSucceeded {
		t.Errorf("run over MCP = %s in %s with %d steps", run.Workflow, run.Namespace, len(run.Steps))
	}
	for _, ns := range fake.ns {
		if ns != "ns-mcp" {
			t.Errorf("AI-App saw namespace %q, want ns-mcp", ns)
		}
	}
}

func TestListApplicationsSummarizesModelsAndApps(t *testing.T) {
	fake := &fakeApp{dryRun: true}
	e, dir := newEngine(t, fake)
	registry := NewRegistry(e)

	if registry.Grade(ToolApplications) != tools.GradeRead {
		t.Errorf("%s grade = %q, want read", ToolApplications, registry.Grade(ToolApplications))
	}

	out, err := registry.Call(context.Background(), "ns-cat", ToolApplications, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	catalog := out.(*Catalog)
	if len(catalog.Models) != 1 || catalog.Models[0].ID != modelID || len(catalog.Models[0].Artifacts) != 2 ||
		catalog.Models[0].Artifacts[1].ID != artifactID || catalog.Models[0].Artifacts[1].Engines[0] != "ollama" {
		t.Errorf("models = %+v", catalog.Models)
	}
	if len(catalog.Apps) != 1 || catalog.Apps[0].Name != appName || catalog.Apps[0].Latest != 2 ||
		len(catalog.Apps[0].Versions) != 2 {
		t.Errorf("apps = %+v", catalog.Apps)
	}

	out, err = registry.Call(context.Background(), "ns-cat", ToolApplications, json.RawMessage(`{"modality":"audio"}`))
	if err != nil {
		t.Fatalf("lookup with a filter: %v", err)
	}
	if catalog := out.(*Catalog); len(catalog.Models) != 0 || len(catalog.Apps) != 1 {
		t.Errorf("filtered lookup = %+v, want no model and the app", catalog)
	}

	if calls := strings.Join(fake.calledTools(), ","); calls != "list_models,list_apps,list_models,list_apps" {
		t.Errorf("AI-App calls = %s, want list_models then list_apps, twice", calls)
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
	// The fake AI-App publishes 9 tools; the engine calls 8 of them.
	if !card.MCP.AppServer.Connected || card.MCP.AppServer.ToolCount != 9 || len(card.Tools.Uses) != 8 {
		t.Errorf("card mcp = %+v, uses %d", card.MCP, len(card.Tools.Uses))
	}
	for i, tool := range card.Tools.Uses {
		if tool.Name != appToolsUsed[i] || tool.Grade == "" {
			t.Errorf("uses[%d] = %+v, want %s with its grade", i, tool, appToolsUsed[i])
		}
		if tool.Name == "list_node_deployments" {
			t.Errorf("card lists %s, which the engine never calls", tool.Name)
		}
		if guarded := tool.Name == toolCreateDeployment || tool.Name == toolControlNode; tool.GuardedByDryRun != guarded {
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

	fake := &fakeApp{}
	e, _ := newEngine(t, fake)
	execute(t, e, WorkflowDeploy, modelRequest("vm1"))
	execute(t, e, WorkflowDeploy, appRequest(0, "vm1"))
	fake.mu.Lock()
	fake.records["old"] = deployment("old", model.NodeDeployFailed, false, modelRequest("vm1"))
	fake.mu.Unlock()
	execute(t, e, WorkflowHeal, HealInput{DeploymentID: "old"})
	execute(t, e, WorkflowControl, ControlInput{DeploymentID: "old", Action: "reboot"})
	execute(t, e, WorkflowStatus, StatusInput{DeploymentID: "old"})
	if _, err := NewRegistry(e).Call(context.Background(), "ns-test", ToolApplications, json.RawMessage(`{}`)); err != nil {
		t.Fatalf("lookup: %v", err)
	}

	called := map[string]bool{}
	for _, name := range fake.calledTools() {
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
