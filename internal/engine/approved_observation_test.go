package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ish-hcc/ai-agent-proto/internal/archive"
	"github.com/ish-hcc/ai-agent-proto/internal/mcpclient"
	"github.com/ish-hcc/ai-agent-proto/internal/mcpserver"
	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/observation"
	"github.com/ish-hcc/ai-agent-proto/internal/tools"
)

func TestApprovedObservationRejectsChangedPlacementAndMixedRevision(t *testing.T) {
	ctx := context.Background()
	runtimeID := "b415bc6a-740d-4d0e-8ab7-1c1c161156e4"
	appID := "8e5ee8f7-1cc8-40cb-b5a8-8820a4dba680"
	dir := t.TempDir()
	archiveAt := time.Now().UTC().Truncate(time.Second).Add(-8 * time.Minute)
	req := modelRequest("vm1")
	req.Inference.RuntimeID = model.RuntimeVLLM
	req.Inference.Args = []string{"--max-num-seqs", "16"}
	req.Inference.Env = map[string]string{"SECRET": "hidden-value"}
	target := req.Targets[0]
	appReq := model.NodeDeploymentReq{
		Type: model.NodeDeployTypeApp, Targets: req.Targets,
		App: &model.NodeAppReq{Name: "langflow", Version: 2},
	}
	endpoints := []model.Endpoint{
		{DeploymentID: runtimeID, Type: model.NodeDeployTypeInference, NsID: "etri", InfraID: target.InfraID, NodeID: "vm1", Available: true,
			Host: "203.0.113.42", APIKey: "hidden-value"},
		{DeploymentID: appID, Type: model.NodeDeployTypeApp, NsID: "etri", InfraID: target.InfraID, NodeID: "vm1", Available: true},
	}
	var endpointMu sync.Mutex
	appTools := tools.NewRegistry(false)
	appTools.Add(tools.Tool{
		Name: "list_endpoints", Grade: tools.GradeRead, InputSchema: map[string]any{"type": "object"},
		Handle: func(_ context.Context, nsID string, _ json.RawMessage) (any, error) {
			if nsID != "etri" {
				return nil, fmt.Errorf("unexpected namespace %s", nsID)
			}
			endpointMu.Lock()
			defer endpointMu.Unlock()
			return model.EndpointListResp{Endpoints: slices.Clone(endpoints)}, nil
		},
	})
	appHTTP := httptest.NewServer(mcpserver.NewHandler(appTools))
	defer appHTTP.Close()
	client := mcpclient.New(appHTTP.URL, "")
	defer func() { _ = client.Close() }()
	runs, err := archive.NewLog[Run](filepath.Join(dir, "runs"))
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		run, id string
		req     model.NodeDeploymentReq
	}{{"eng-inf", runtimeID, req}, {"eng-app", appID, appReq}} {
		d := deployment(row.id, model.NodeDeployRunning, false, row.req)
		d.FinishedAt = &archiveAt
		d.Nodes[0].GPU = row.req.Type == model.NodeDeployTypeInference
		body, err := json.Marshal(d)
		if err != nil {
			t.Fatal(err)
		}
		if err := runs.Append(Run{
			RunID: row.run, Namespace: "etri", Status: RunSucceeded, Workflow: WorkflowDeploy, EndedAt: archiveAt,
			Steps: []Step{{Tool: toolGetDeployment, Status: StepSucceeded, Output: body}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	prom := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[]}}`))
	}))
	defer prom.Close()
	source, err := observation.NewPrometheus(prom.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	e := New(client, runs, WithApprovedApp(client), WithObservation(source))
	approvals := filepath.Join(dir, "approvals")
	records, err := archive.NewLog[applyRecord](filepath.Join(approvals, "apply"))
	if err != nil {
		t.Fatal(err)
	}
	record := applyRecord{
		applyReceipt: applyReceipt{
			CorrelationID: "fixture-flow", Revision: 1, Status: applySucceeded, Namespace: "etri",
			Inference:   applyStep{Status: applySucceeded, RunID: "eng-inf", DeploymentID: runtimeID},
			Application: applyStep{Status: applySucceeded, RunID: "eng-app", DeploymentID: appID},
			UpdatedAt:   archiveAt.Add(time.Second), Target: target.Key(),
		},
		Request: approvedApplyRequest{Inference: req, Application: appReq},
	}
	if err := records.Append(record); err != nil {
		t.Fatal(err)
	}
	registry, err := NewApprovedDeploymentTools(approvals, e, ctx)
	if err != nil {
		t.Fatal(err)
	}
	q := observationQuery{
		CorrelationID: "fixture-flow", Revision: 1,
		Window: observation.Window{Start: archiveAt.Add(time.Minute), End: archiveAt.Add(4 * time.Minute)},
	}
	call := func(ns string, q observationQuery) (observation.Record, error) {
		t.Helper()
		body, err := json.Marshal(q)
		if err != nil {
			t.Fatal(err)
		}
		got, err := registry.Call(ctx, ns, "get_inference_observation", body)
		if err != nil {
			return observation.Record{}, err
		}
		return got.(observation.Record), nil
	}
	result, err := call("etri", q)
	if err != nil || result.Placement.DeploymentID != runtimeID || result.InferenceRunID != "eng-inf" ||
		result.ApplicationDeploymentID != appID || result.ApplicationVersion != 2 || result.MaxNumSeqs != 16 ||
		result.Metrics.Complete || result.ObservationID == "" {
		t.Fatalf("observed = %+v, %v", result, err)
	}
	body, _ := json.Marshal(result)
	if strings.Contains(string(body), "hidden-value") || strings.Contains(string(body), "203.0.113") {
		t.Fatalf("observation exposed source details: %s", body)
	}
	endpointMu.Lock()
	endpoints[0].Host = "203.0.113.55"
	endpointMu.Unlock()
	if again, err := call("etri", q); err != nil || again.ObservationID != result.ObservationID {
		t.Fatalf("address change altered the record identity: %+v, %v", again, err)
	}
	if _, err := call("outside", q); err == nil {
		t.Fatal("cross-namespace read passed")
	}
	q.Window.Start = archiveAt
	if _, err := call("etri", q); err == nil {
		t.Fatal("window containing a deployment transition passed")
	}
	q.Window.Start = archiveAt.Add(time.Minute)
	endpointMu.Lock()
	endpoints[0].DeploymentID = "legacy-replacement"
	endpointMu.Unlock()
	if _, err := call("etri", q); err == nil {
		t.Fatal("a legacy replacement reused the approved identity")
	}
	endpointMu.Lock()
	endpoints[0].DeploymentID = runtimeID
	endpointMu.Unlock()
	record.Revision = 2
	record.Status = applyQueued
	if err := records.Append(record); err != nil {
		t.Fatal(err)
	}
	if _, err := call("etri", q); err == nil {
		t.Fatal("a newer queued apply did not close the preceding revision")
	}
}
