package engine

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
)

func TestExecuteRejectsApprovedSubmission(t *testing.T) {
	app := &fakeApp{}
	e, _ := newEngine(t, app)
	e.approvedApp = e.app
	request := modelRequest("vm1")
	request.Submission = &model.NodeDeploymentSubmission{ID: "8e5ee8f7-1cc8-40cb-b5a8-8820a4dba680"}
	body, _ := json.Marshal(request)
	run, err := e.Execute(context.Background(), "etri", WorkflowDeploy, body)
	if !errors.Is(err, ErrInvalidInput) || run != nil || app.count(toolCreateDeployment) != 0 {
		t.Fatalf("ordinary workflow accepted an approved submission: run=%+v, err=%v", run, err)
	}
}

func TestApprovedDeploymentStagesOneExactRevisionAcrossRestart(t *testing.T) {
	ctx := context.Background()
	app := &fakeApp{dryRun: true}
	e, _ := newEngine(t, app)
	dir := t.TempDir()
	registry, err := NewApprovedDeploymentTools(dir, e, nil)
	if err != nil {
		t.Fatal(err)
	}
	input := approvedDeploymentRequest{CorrelationID: "flow-1", Revision: 1, Deployment: modelRequest("vm1")}
	body, _ := json.Marshal(input)
	if _, err := registry.Call(ctx, "other", "stage_approved_deployment", body); err == nil {
		t.Fatal("target outside the approval namespace was accepted")
	}
	got, err := registry.Call(ctx, "etri", "stage_approved_deployment", body)
	if err != nil {
		t.Fatal(err)
	}
	receipt := got.(approvedDeploymentReceipt)
	if receipt.Status != "blocked" || receipt.RunID == "" || receipt.DeploymentID != "dep-1" {
		t.Fatalf("receipt = %+v, want blocked with engine and dry-run deployment IDs", receipt)
	}
	registry, err = NewApprovedDeploymentTools(dir, e, nil)
	if err != nil {
		t.Fatal(err)
	}
	again, err := registry.Call(ctx, "etri", "stage_approved_deployment", body)
	if err != nil || again != got {
		t.Fatalf("replay = %+v, %v, want %+v", again, err, got)
	}
	query := json.RawMessage(`{"correlationId":"flow-1"}`)
	stored, err := registry.Call(ctx, "etri", "get_approved_deployment", query)
	if err != nil || stored != got {
		t.Fatalf("stored = %+v, %v, want %+v", stored, err, got)
	}
	if _, err := registry.Call(ctx, "other", "get_approved_deployment", query); err == nil {
		t.Fatal("other namespace read the receipt")
	}
	input.Deployment.Targets[0].NodeID = "vm2"
	body, _ = json.Marshal(input)
	if _, err := registry.Call(ctx, "etri", "stage_approved_deployment", body); err == nil {
		t.Fatal("changed deployment reused the approval")
	}
	input.Revision = 2
	body, _ = json.Marshal(input)
	if _, err := registry.Call(ctx, "etri", "stage_approved_deployment", body); err == nil {
		t.Fatal("the initial deployment tool accepted a later Revision")
	}
	if len(app.requests) != 1 || !app.requests[0].RequireDryRun {
		t.Fatalf("create_deployment received %+v, want one dry-run-only request", app.requests)
	}
}

func TestApprovedDeploymentCannotRunAgainstLiveAIApp(t *testing.T) {
	app := &fakeApp{dryRun: false}
	e, _ := newEngine(t, app)
	registry, err := NewApprovedDeploymentTools(t.TempDir(), e, nil)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(approvedDeploymentRequest{CorrelationID: "flow-1", Revision: 1, Deployment: modelRequest("vm1")})
	result, err := registry.Call(context.Background(), "etri", "stage_approved_deployment", body)
	if err != nil || result.(approvedDeploymentReceipt).Status != "failed" || app.count(toolCreateDeployment) != 0 {
		t.Fatalf("live AI-App returned %+v, %v and received %d create calls", result, err, app.count(toolCreateDeployment))
	}
}

func TestApprovedDeploymentDoesNotResubmitAnUnconfirmedCreate(t *testing.T) {
	ctx := context.Background()
	app := &fakeApp{dryRun: true, fail: toolCreateDeployment}
	e, _ := newEngine(t, app)
	dir := t.TempDir()
	body, _ := json.Marshal(approvedDeploymentRequest{CorrelationID: "flow-1", Revision: 1, Deployment: modelRequest("vm1")})
	for attempt := range 2 {
		if attempt == 1 {
			e.approvedApp = e.app
		}
		registry, err := NewApprovedDeploymentTools(dir, e, nil)
		if err != nil {
			t.Fatal(err)
		}
		result, err := registry.Call(ctx, "etri", "stage_approved_deployment", body)
		if err != nil || result.(approvedDeploymentReceipt).Status != "confirmation_pending" {
			t.Fatalf("uncertain deployment returned %+v, %v", result, err)
		}
	}
	if app.count(toolCreateDeployment) != 1 || app.count(toolGetDeployment) != 0 {
		t.Fatal("uncertain deployment was submitted again")
	}
}
