package rest

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ish-hcc/ai-agent-proto/internal/appstore"
	"github.com/ish-hcc/ai-agent-proto/internal/archive"
	"github.com/ish-hcc/ai-agent-proto/internal/config"
	"github.com/ish-hcc/ai-agent-proto/internal/engine"
	"github.com/ish-hcc/ai-agent-proto/internal/mcpclient"
	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/modelcat"
	"github.com/ish-hcc/ai-agent-proto/internal/nodedeploy"
	"github.com/ish-hcc/ai-agent-proto/internal/semaphore"
	"github.com/ish-hcc/ai-agent-proto/internal/tools"
	"github.com/ish-hcc/ai-agent-proto/internal/tumblebug"
)

type applyFixtureRunner struct {
	mu        sync.Mutex
	playbooks []string
	failApp   bool
}

func (r *applyFixtureRunner) Run(_ context.Context, spec semaphore.RunSpec) (*semaphore.RunResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.playbooks = append(r.playbooks, spec.Playbook)
	if r.failApp && spec.Playbook == nodedeploy.PlaybookCopyAndRun {
		r.failApp = false
		return nil, errors.New("fixture application failed")
	}
	return &semaphore.RunResult{TaskID: len(r.playbooks), Status: semaphore.StatusSuccess}, nil
}

func (r *applyFixtureRunner) runs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.playbooks)
}

type applyFixtureClient struct {
	*mcpclient.Client
	dropCreate *atomic.Bool
	dropRead   *atomic.Bool
}

func (c applyFixtureClient) Call(ctx context.Context, nsID, name string, input json.RawMessage) (any, error) {
	if name == "get_deployment" && c.dropRead != nil && c.dropRead.Load() {
		return nil, mcpclient.ErrUnavailable
	}
	result, err := c.Client.Call(ctx, nsID, name, input)
	if err == nil && name == "create_deployment" && c.dropCreate != nil && c.dropCreate.Swap(false) {
		return nil, mcpclient.ErrUnavailable
	}
	return result, err
}

func TestKHUApprovedApplyThroughAIAppPreservesPlacement(t *testing.T) {
	const token = "fixture-approval-token"
	const secret = "fixture-only-app-password"
	t.Setenv("AIAPP_DEPLOY_SECRET_LANGFLOW_PASSWORD", secret)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	dir := t.TempDir()
	var amdSpec atomic.Bool
	tb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodGet {
			t.Errorf("fixture received a CB-Tumblebug write: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		switch r.URL.Path {
		case "/ns/etri/infra/travel/node/vm1":
			_, _ = w.Write([]byte(`{"id":"vm1","status":"Running","publicIP":"203.0.113.21","sshKeyId":"k",
				"nodeUserName":"cb-user","specId":"aws+gpu","imageId":"aws+image","connectionConfig":{"providerName":"AWS"}}`))
		case "/ns/etri/resources/sshKey/k":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "k", "privateKey": nodeTestKey})
		case "/ns/system/resources/spec/aws+gpu":
			device := "NVIDIA RTX PRO 6000"
			if amdSpec.Load() {
				device = "AMD Instinct MI250"
			}
			_ = json.NewEncoder(w).Encode(tumblebug.SpecInfo{ID: "aws+gpu", AcceleratorType: "gpu", AcceleratorModel: device})
		default:
			t.Errorf("unexpected CB-Tumblebug fixture path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(tb.Close)

	models, _, err := modelcat.Open("")
	if err != nil {
		t.Fatal(err)
	}
	if err := modelcat.Seed(ctx, models); err != nil {
		t.Fatal(err)
	}
	apps, err := appstore.Open(filepath.Join(dir, "apps"), 0)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := apps.Register(ctx, "langflow", "echo ready", []int{7860},
			[]appstore.File{{Name: "docker-compose.yml", Content: strings.NewReader("services: {}\n")}}); err != nil {
			t.Fatal(err)
		}
	}

	runner := &applyFixtureRunner{}
	approvedDir := filepath.Join(dir, "approved")
	runs, err := archive.NewLog[engine.Run](filepath.Join(dir, "engine"))
	if err != nil {
		t.Fatal(err)
	}
	var dropCreate, dropRead atomic.Bool
	start := func(dryRun bool) (*mcpclient.Client, *engine.Engine, *nodedeploy.Service, context.CancelFunc) {
		t.Helper()
		store, err := nodedeploy.OpenStore(filepath.Join(dir, "deployments.json"))
		if err != nil {
			t.Fatal(err)
		}
		svc := nodedeploy.NewService(tumblebug.NewClient(config.TumblebugConfig{BaseURL: tb.URL, Timeout: time.Second}),
			runner, models, apps, store, nil, nodedeploy.Options{StageRoot: filepath.Join(dir, "stage"), SpecNamespace: "system", DryRun: dryRun})
		t.Cleanup(func() { _ = svc.Shutdown(context.Background()) })
		appTools := tools.NewRegistry(dryRun)
		tools.RegisterModels(appTools, models)
		tools.RegisterApps(appTools, apps)
		tools.RegisterNodeDeploy(appTools, svc)
		appConfig := &config.Config{
			Auth: config.AuthConfig{Token: testToken}, Engine: config.EngineConfig{ApprovalToken: token},
		}
		backendServer := NewAppServer(appConfig, AppDependencies{Tools: appTools, Models: models, Apps: apps, NodeDeploy: svc})
		if rec := do(backendServer, http.MethodPost, BasePath+approvedSubmissionPath, "Bearer "+testToken); rec.Code != http.StatusUnauthorized {
			t.Fatalf("legacy token reached the approval submission: %d", rec.Code)
		}
		backend := httptest.NewServer(backendServer.echo)
		t.Cleanup(backend.Close)
		appClient := mcpclient.New(backend.URL+BasePath+"/mcp", testToken)
		t.Cleanup(func() { _ = appClient.Close() })
		submissionClient := mcpclient.New(backend.URL+BasePath+approvedSubmissionPath, token)
		t.Cleanup(func() { _ = submissionClient.Close() })
		e := engine.New(applyFixtureClient{Client: appClient, dropCreate: &dropCreate, dropRead: &dropRead}, runs,
			engine.WithDeploymentWait(2*time.Second, 5*time.Millisecond),
			engine.WithApprovedApp(applyFixtureClient{Client: submissionClient, dropCreate: &dropCreate}))
		workerCtx, stopWorker := context.WithCancel(ctx)
		t.Cleanup(stopWorker)
		var liveCtx context.Context
		if !dryRun {
			liveCtx = workerCtx
		}
		approvalTools, err := engine.NewApprovedDeploymentTools(approvedDir, e, liveCtx)
		if err != nil {
			t.Fatal(err)
		}
		cfg := &config.Config{Auth: config.AuthConfig{Token: testToken}, Engine: config.EngineConfig{ApprovalToken: token}}
		engineHTTP := httptest.NewServer(NewEngineServer(cfg, EngineDependencies{
			Engine: e, Tools: engine.NewRegistry(e), ApprovedDeploymentTools: approvalTools,
		}).echo)
		t.Cleanup(engineHTTP.Close)
		cfg.Engine.URL = engineHTTP.URL
		gateway := httptest.NewServer(NewAppServer(cfg, AppDependencies{Tools: tools.NewRegistry(dryRun)}).echo)
		t.Cleanup(gateway.Close)
		client := mcpclient.New(gateway.URL+BasePath+approvedDeploymentPath, token)
		t.Cleanup(func() { _ = client.Close() })
		if err := client.Prepare(ctx); err != nil {
			t.Fatal(err)
		}
		return client, e, svc, stopWorker
	}

	targets := []model.NodeDeployTarget{{Kind: "node", NsID: "etri", InfraID: "travel", NodeID: "vm1", SSHUser: "cb-user"}}
	gpu := true
	review := &model.NodePlacementReview{
		NsID: "etri", SpecID: "aws+gpu", ImageID: "aws+image", Status: "Ready",
		SpecStatus: "Available", ImageStatus: "Available", EstimatedCost: "$0.50/hour", ReviewedAt: time.Now().UTC(),
	}
	inference := model.NodeDeploymentReq{Type: "inference", Targets: targets, PlacementReview: review,
		Inference: &model.NodeInferenceReq{ModelID: "ministral-3-8b", ArtifactID: "hf-fp8", RuntimeID: "vllm", GPU: &gpu,
			Args: []string{"--max-num-seqs", "2"}}}
	const baselineArgument = `"--max-num-seqs", "2"`
	app := model.NodeDeploymentReq{Type: "app", Targets: targets, PlacementReview: review,
		App: &model.NodeAppReq{Name: "langflow", Version: 1,
			InferenceBinding: &model.NodeInferenceBinding{BaseURLEnv: "MODEL_BASE_URL", APIKeyEnv: "MODEL_API_KEY"},
			EnvFrom:          map[string]string{"LANGFLOW_SUPERUSER_PASSWORD": "AIAPP_DEPLOY_SECRET_LANGFLOW_PASSWORD"}}}
	const flow = "fixture-live-apply"
	call := func(client *mcpclient.Client, tool string, input any) (json.RawMessage, error) {
		t.Helper()
		body, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		out, err := client.Call(ctx, "etri", tool, body)
		if err != nil {
			return nil, err
		}
		return out.(json.RawMessage), nil
	}
	stageClient, stageEngine, dryService, stopStage := start(true)
	mismatched := inference
	mismatchedReview := *review
	mismatchedReview.ImageID = "aws+different-image"
	mismatched.PlacementReview = &mismatchedReview
	stageInput := map[string]any{
		"correlationId": "fixture-mismatched-image", "revision": 1, "deployment": mismatched,
	}
	var rejectedStage struct {
		Status, EngineRunID, DeploymentID string
	}
	raw, err := call(stageClient, "stage_approved_deployment", stageInput)
	if err != nil || json.Unmarshal(raw, &rejectedStage) != nil || rejectedStage.Status != "failed" ||
		rejectedStage.EngineRunID == "" || rejectedStage.DeploymentID != "" {
		t.Fatalf("mismatched placement dry-run = %s, %v", raw, err)
	}
	stageRun, err := stageEngine.GetRun(rejectedStage.EngineRunID)
	if err != nil || stageRun.Status != engine.RunFailed || !strings.Contains(stageRun.Message, "reviewed CB-Tumblebug spec and image") {
		t.Fatalf("mismatched placement engine run = %+v, %v", stageRun, err)
	}
	replay, err := call(stageClient, "stage_approved_deployment", stageInput)
	if err != nil || string(replay) != string(raw) || len(dryService.List(ctx, nodedeploy.ListFilter{})) != 0 || len(runner.runs()) != 0 {
		t.Fatalf("mismatched placement replay = %s, %v", replay, err)
	}
	amdSpec.Store(true)
	rejected, err := call(stageClient, "stage_approved_deployment", map[string]any{
		"correlationId": "fixture-incompatible-gpu", "revision": 1, "deployment": inference,
	})
	if err != nil || !strings.Contains(string(rejected), `"status":"failed"`) || len(runner.runs()) != 0 {
		t.Fatalf("incompatible GPU dry-run = %s, %v", rejected, err)
	}
	amdSpec.Store(false)
	before := len(dryService.List(ctx, nodedeploy.ListFilter{}))
	stageInput = map[string]any{"correlationId": flow, "revision": 1, "deployment": inference}
	dropCreate.Store(true)
	uncertain, err := call(stageClient, "stage_approved_deployment", stageInput)
	staged := dryService.List(ctx, nodedeploy.ListFilter{})
	if err != nil || !strings.Contains(string(uncertain), `"status":"confirmation_pending"`) || len(staged) != before+1 {
		t.Fatalf("lost dry-run response = %s, %v", uncertain, err)
	}
	stagedCompose, err := os.ReadFile(filepath.Join(staged[0].Nodes[0].StageDir, nodedeploy.ComposeFile))
	if err != nil || !strings.Contains(string(stagedCompose), baselineArgument) {
		t.Fatalf("initial dry-run Compose did not carry the baseline: %v", err)
	}
	dropRead.Store(true)
	replay, err = call(stageClient, "stage_approved_deployment", stageInput)
	if err != nil || string(replay) != string(uncertain) ||
		len(dryService.List(ctx, nodedeploy.ListFilter{})) != before+1 || len(runner.runs()) != 0 {
		t.Fatalf("lost dry-run response replay = %s, %v", replay, err)
	}
	out, err := call(stageClient, "stage_approved_deployment", map[string]any{
		"correlationId": flow + "-confirmed", "revision": 1, "deployment": inference,
	})
	if err != nil || !strings.Contains(string(out), `"status":"blocked"`) || len(runner.runs()) != 0 {
		t.Fatalf("separate dry-run with the same deployment body = %s, %v", out, err)
	}
	stopStage()
	if err := dryService.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}

	stageClient, stageEngine, dryService, stopStage = start(true)
	dropRead.Store(false)
	var recoveredStage struct {
		Status, EngineRunID, DeploymentID string
	}
	recovered, err := call(stageClient, "stage_approved_deployment", stageInput)
	if err != nil || json.Unmarshal(recovered, &recoveredStage) != nil || recoveredStage.Status != "blocked" ||
		recoveredStage.DeploymentID != staged[0].ID || recoveredStage.EngineRunID == "" ||
		len(dryService.List(ctx, nodedeploy.ListFilter{})) != before+2 || len(runner.runs()) != 0 {
		t.Fatalf("dry-run recovery after restart = %s, %v", recovered, err)
	}
	recoveryRun, err := stageEngine.GetRun(recoveredStage.EngineRunID)
	if err != nil || recoveryRun.Workflow != engine.WorkflowStatus || len(recoveryRun.Steps) != 1 ||
		recoveryRun.Steps[0].Tool != "get_deployment" || recoveryRun.Status != engine.RunSucceeded {
		t.Fatalf("dry-run recovery engine run = %+v, %v", recoveryRun, err)
	}
	stopStage()
	if err := dryService.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	client, e, svc, stopWorker := start(false)
	legacyApp := app
	request := *app.App
	binding := *request.InferenceBinding
	binding.DeploymentID = "b415bc6a-740d-4d0e-8ab7-1c1c161156e4"
	request.InferenceBinding = &binding
	legacyApp.App = &request
	legacyApp.Submission = &model.NodeDeploymentSubmission{ID: "8e5ee8f7-1cc8-40cb-b5a8-8820a4dba680"}
	if _, err := svc.Create(ctx, legacyApp); err == nil || !strings.Contains(err.Error(), "approval MCP tool") {
		t.Fatalf("legacy create accepted server-side binding: %v", err)
	}
	type receipt struct {
		Status      string                                     `json:"status"`
		Inference   struct{ DeploymentID, EngineRunID string } `json:"inference"`
		Application struct{ DeploymentID, EngineRunID string } `json:"application"`
	}
	submit := func(revision int) (json.RawMessage, error) {
		return call(client, "apply_approved_revision", map[string]any{
			"correlationId": flow, "revision": revision, "previousRevision": revision - 1,
			"inference": inference, "application": app,
		})
	}
	wait := func(revision int, expected string) receipt {
		t.Helper()
		for ctx.Err() == nil {
			raw, err := call(client, "get_applied_revision", map[string]any{"correlationId": flow, "revision": revision})
			var got receipt
			if err != nil || json.Unmarshal(raw, &got) != nil {
				t.Fatalf("revision %d = %s, %v", revision, raw, err)
			}
			if got.Status != "queued" {
				if got.Status != expected {
					t.Fatalf("revision %d = %s, want %s", revision, raw, expected)
				}
				return got
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for revision %d", revision)
		return receipt{}
	}
	if _, err := submit(1); err != nil {
		t.Fatal(err)
	}
	first := wait(1, "succeeded")
	want := []string{nodedeploy.PlaybookInstallDocker, nodedeploy.PlaybookComposeUp, nodedeploy.PlaybookInstallDocker, nodedeploy.PlaybookCopyAndRun}
	if !slices.Equal(runner.runs(), want) || first.Inference.DeploymentID == "" || first.Application.DeploymentID == "" {
		t.Fatalf("first apply = %+v, playbooks %v", first, runner.runs())
	}
	if _, err := submit(1); err != nil || !slices.Equal(runner.runs(), want) {
		t.Fatalf("duplicate approval repeated a playbook: %v", err)
	}
	deployed, err := svc.Get(ctx, first.Application.DeploymentID)
	if err != nil {
		t.Fatal(err)
	}
	env, err := os.ReadFile(filepath.Join(deployed.Nodes[0].StageDir, nodedeploy.EnvFile))
	if err != nil || !strings.Contains(string(env), "MODEL_BASE_URL='http://203.0.113.21:8000/v1'") ||
		!strings.Contains(string(env), "MODEL_API_KEY='") || !strings.Contains(string(env), "LANGFLOW_SUPERUSER_PASSWORD='"+secret+"'") {
		t.Fatal("the application did not receive the bound endpoint and secrets")
	}
	inferenceRecord, err := svc.Get(ctx, first.Inference.DeploymentID)
	if err != nil || inferenceRecord.Nodes[0].APIKey != "" || inferenceRecord.Nodes[0].Compose != "" {
		t.Fatal("the managed deployment record exposed the generated credentials")
	}
	liveCompose, err := os.ReadFile(filepath.Join(inferenceRecord.Nodes[0].StageDir, nodedeploy.ComposeFile))
	if err != nil || !strings.Contains(string(liveCompose), baselineArgument) {
		t.Fatalf("first applied Compose did not carry the approved baseline: %v", err)
	}
	appRun, err := e.GetRun(first.Application.EngineRunID)
	body, _ := json.Marshal(appRun)
	if err != nil || strings.Contains(string(body), secret) {
		t.Fatal("the engine run did not preserve secret references")
	}

	app.App.Version = 2
	runner.mu.Lock()
	runner.failApp = true
	runner.mu.Unlock()
	if _, err := submit(2); err != nil {
		t.Fatal(err)
	}
	failed := wait(2, "failed")
	if failed.Inference != first.Inference || failed.Application.DeploymentID == "" || len(runner.runs()) != 6 {
		t.Fatalf("version update did not preserve inference: %+v, playbooks %v", failed, runner.runs())
	}
	if _, err := submit(3); err != nil {
		t.Fatal(err)
	}
	repaired := wait(3, "succeeded")
	if repaired.Inference != first.Inference || repaired.Application.DeploymentID == failed.Application.DeploymentID ||
		len(runner.runs()) != 8 {
		t.Fatalf("repair did not resume from the application: %+v, playbooks %v", repaired, runner.runs())
	}
	inference.Inference.Args = []string{"--max-num-seqs", "16"}
	dropCreate.Store(true)
	if _, err := submit(4); err != nil {
		t.Fatal(err)
	}
	lost := wait(4, "confirmation_pending")
	run, err := e.GetRun(lost.Inference.EngineRunID)
	if err != nil || !slices.ContainsFunc(run.Steps, func(step engine.Step) bool { return step.Unconfirmed }) {
		t.Fatalf("lost create result was not archived: %+v, %v", lost, err)
	}
	input, err := json.Marshal(run.Input)
	var admitted model.NodeDeploymentReq
	if err != nil || json.Unmarshal(input, &admitted) != nil || admitted.Submission == nil {
		t.Fatalf("lost create was not tied to a submission: %+v, %v", run, err)
	}
	id := admitted.Submission.ID
	for {
		deployed, err := svc.Get(ctx, id)
		if err != nil || ctx.Err() != nil {
			t.Fatalf("the accepted create did not finish: %v, %v", err, ctx.Err())
		}
		if deployed.Status == model.NodeDeployRunning {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	restart := func() {
		t.Helper()
		stopWorker()
		if err := svc.Shutdown(ctx); err != nil {
			t.Fatal(err)
		}
		client, e, svc, stopWorker = start(false)
	}
	restart()
	dropRead.Store(true)
	if _, err := submit(4); err != nil {
		t.Fatal(err)
	}
	pending := wait(4, "confirmation_pending")
	if pending.Inference.DeploymentID != id || pending.Application.DeploymentID != "" || len(runner.runs()) != 10 {
		t.Fatalf("unconfirmed status read repeated a deployment: %+v, playbooks %v", pending, runner.runs())
	}
	dropRead.Store(false)
	restart()
	if _, err := submit(4); err != nil {
		t.Fatal(err)
	}
	tuned := wait(4, "succeeded")
	if tuned.Inference.DeploymentID != id || id == repaired.Inference.DeploymentID || len(runner.runs()) != 12 {
		t.Fatalf("runtime update = %+v, playbooks %v", tuned, runner.runs())
	}
	updated, err := svc.Get(ctx, tuned.Inference.DeploymentID)
	if err != nil || !slices.Equal(updated.Request.Inference.Args, inference.Inference.Args) {
		t.Fatal("the runtime update did not persist the approved argument")
	}
	if _, err := submit(3); err == nil || len(runner.runs()) != 12 {
		t.Fatal("a stale revision reached the deployment engine")
	}

	inference.Inference.Args = []string{"--max-num-seqs", "32"}
	amdSpec.Store(true)
	if _, err := submit(5); err != nil {
		t.Fatal(err)
	}
	incompatible := wait(5, "failed")
	rejectedDeployment, err := svc.Get(ctx, incompatible.Inference.DeploymentID)
	if err != nil || rejectedDeployment.Nodes[0].StageDir != "" ||
		incompatible.Application.DeploymentID != "" || len(runner.runs()) != 12 {
		t.Fatalf("incompatible revision reached staging or execution: %+v, %v", incompatible, err)
	}
	var inferenceServing, appServing bool
	for _, endpoint := range svc.Endpoints(ctx, nodedeploy.EndpointFilter{NsID: "etri"}) {
		inferenceServing = inferenceServing || (endpoint.Available && endpoint.DeploymentID == tuned.Inference.DeploymentID)
		appServing = appServing || (endpoint.Available && endpoint.DeploymentID == tuned.Application.DeploymentID)
	}
	if !inferenceServing || !appServing {
		t.Fatal("the rejected revision displaced a serving endpoint")
	}
}
