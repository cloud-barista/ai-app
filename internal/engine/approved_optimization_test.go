package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ish-hcc/ai-agent-proto/internal/archive"
	"github.com/ish-hcc/ai-agent-proto/internal/mcpclient"
	"github.com/ish-hcc/ai-agent-proto/internal/mcpserver"
	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/observation"
	"github.com/ish-hcc/ai-agent-proto/internal/optimization"
	"github.com/ish-hcc/ai-agent-proto/internal/tools"
)

func TestApprovedOptimizationRechecksEvidenceAndComparesAppliedRevision(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	runs, err := archive.NewLog[Run](filepath.Join(dir, "runs"))
	if err != nil {
		t.Fatal(err)
	}
	approvals := filepath.Join(dir, "approvals")
	records, err := archive.NewLog[applyRecord](filepath.Join(approvals, "apply"))
	if err != nil {
		t.Fatal(err)
	}
	request := modelRequest("vm1")
	request.Inference.RuntimeID = model.RuntimeVLLM
	target := request.Targets[0]
	appRequest := model.NodeDeploymentReq{
		Type: model.NodeDeployTypeApp, Targets: request.Targets, App: &model.NodeAppReq{Name: "langflow", Version: 1},
	}
	var mu sync.Mutex
	var endpoints []model.Endpoint
	var servingID string
	appTools := tools.NewRegistry(false)
	appTools.Add(tools.Tool{
		Name: "list_endpoints", Grade: tools.GradeRead, InputSchema: map[string]any{"type": "object"},
		Handle: func(_ context.Context, _ string, _ json.RawMessage) (any, error) {
			mu.Lock()
			defer mu.Unlock()
			return model.EndpointListResp{Endpoints: slices.Clone(endpoints)}, nil
		},
	})
	appHTTP := httptest.NewServer(mcpserver.NewHandler(appTools))
	defer appHTTP.Close()
	appClient := mcpclient.New(appHTTP.URL, "")
	defer func() { _ = appClient.Close() }()
	prom := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		id := servingID
		mu.Unlock()
		writeOptimizationMetrics(t, w, r, target, id)
	}))
	defer prom.Close()
	source, err := observation.NewPrometheus(prom.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	engine := New(appClient, runs, WithApprovedApp(appClient), WithObservation(source), WithOptimizationProposals(dir))
	registry, err := NewApprovedDeploymentTools(approvals, engine, ctx)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(mcpserver.NewNamedHandler(ApprovedDeploymentServerName, registry))
	defer server.Close()
	mcp := mcpclient.New(server.URL, "")
	defer func() { _ = mcp.Close() }()
	if err := mcp.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	call := func(ns, tool string, input any, output any) error {
		t.Helper()
		body, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		out, err := mcp.Call(ctx, ns, tool, body)
		if err != nil {
			return err
		}
		return json.Unmarshal(out.(json.RawMessage), output)
	}
	const flow = "optimization-fixture"
	const previousFlow = "earlier-optimization-fixture"
	settled := time.Now().UTC().Truncate(time.Second).Add(-9 * time.Minute)
	seedRevision := func(flowID string, revision, setting int, at time.Time, inferenceID, appID string) observationQuery {
		t.Helper()
		inf := *request.Inference
		inf.Args = []string{"--max-num-seqs", strconv.Itoa(setting)}
		req := request
		req.Inference = &inf
		writeRun := func(name, id string, input model.NodeDeploymentReq) applyStep {
			d := deployment(id, model.NodeDeployRunning, false, input)
			d.FinishedAt = &at
			d.Nodes[0].GPU = input.Type == model.NodeDeployTypeInference
			d.Nodes[0].Node = &model.NodeDeployNode{SpecID: "fixture-spec"}
			body, err := json.Marshal(d)
			if err != nil {
				t.Fatal(err)
			}
			if err := runs.Append(Run{
				RunID: name, Namespace: target.NsID, Workflow: WorkflowDeploy, Status: RunSucceeded, EndedAt: at,
				Steps: []Step{{Tool: toolGetDeployment, Status: StepSucceeded, Output: body}},
			}); err != nil {
				t.Fatal(err)
			}
			return applyStep{Status: applySucceeded, RunID: name, DeploymentID: id}
		}
		record := applyRecord{
			applyReceipt: applyReceipt{
				CorrelationID: flowID, Revision: revision, PreviousRevision: revision - 1,
				Namespace: target.NsID, Target: target.Key(), Status: applySucceeded, UpdatedAt: at,
				Inference:   writeRun(flowID+"-inf-"+strconv.Itoa(revision), inferenceID, req),
				Application: writeRun(flowID+"-app-"+strconv.Itoa(revision), appID, appRequest),
			},
			Request: approvedApplyRequest{CorrelationID: flowID, Revision: revision, PreviousRevision: revision - 1, Inference: req, Application: appRequest},
		}
		if err := records.Append(record); err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		endpoints = []model.Endpoint{
			{NsID: target.NsID, InfraID: target.InfraID, NodeID: target.NodeID, Type: model.NodeDeployTypeInference, DeploymentID: inferenceID, Available: true},
			{NsID: target.NsID, InfraID: target.InfraID, NodeID: target.NodeID, Type: model.NodeDeployTypeApp, DeploymentID: appID, Available: true},
		}
		servingID = inferenceID
		mu.Unlock()
		return observationQuery{CorrelationID: flowID, Revision: revision,
			Window: observation.Window{Start: at.Add(time.Minute), End: at.Add(3 * time.Minute)}}
	}
	const earlierApp = "10000000-0000-4000-8000-000000000002"
	const laterApp = "10000000-0000-4000-8000-000000000004"
	laterAppliedAt := settled.Add(-4 * time.Minute)
	seedRevision(previousFlow, 1, 16, laterAppliedAt.Add(-4*time.Minute), "10000000-0000-4000-8000-000000000001", earlierApp)
	seedRevision(previousFlow, 2, 32, laterAppliedAt, "10000000-0000-4000-8000-000000000003", laterApp)
	beforeQuery := seedRevision(flow, 1, 16, settled, "b415bc6a-740d-4d0e-8ab7-1c1c161156e4", "8e5ee8f7-1cc8-40cb-b5a8-8820a4dba680")
	var before observation.Record
	if err := call(target.NsID, "get_inference_observation", beforeQuery, &before); err != nil || !before.Metrics.Complete {
		t.Fatalf("pre-apply observation = %+v, %v", before, err)
	}
	load := optimization.LoadReport{
		Workload:      optimization.Workload{ID: "fixture-workload", RuntimeImage: "vllm-pinned", PromptTokensP50: 128, MaxOutputTokens: 64, OfferedRPS: 4},
		ObservationID: before.ObservationID, Window: before.Window, SLOSeconds: .7, Attempted: 480, WithinSLO: 120,
	}
	p := optimization.Proposal{
		SchemaVersion: optimization.SchemaVersion, Observation: before, Load: load,
		Guard: optimization.Guard{P95LimitSeconds: .7, PeakVRAMLimitMiB: 9000, ErrorRateLimit: .02,
			MinimumGainFraction: .1, EvaluationHorizonSeconds: 600, MaxUpdateDowntimeSeconds: 60},
		Decision: optimization.Decision{
			Mode: "shadow", Action: "UPDATE", Reason: "test fixture", DataKind: "measurement", HoldoutPassed: true,
			ModelVersion: "khu-vllm-max-num-seqs/v1", ArtifactSHA256: strings.Repeat("a", 64), DatasetSHA256: strings.Repeat("b", 64),
			CurrentMaxNumSeqs: 16, CandidateMaxNumSeqs: 32,
			Forecast:         optimization.Scores{"p95Seconds": .5, "goodputRps": 2, "peakVRAMMiB": 8000, "errorRate": .005},
			CurrentForecast:  optimization.Scores{"p95Seconds": .5, "goodputRps": 1, "peakVRAMMiB": 7000, "errorRate": 0},
			ValidationMargin: optimization.Scores{"p95Seconds": .05, "goodputRps": .1, "peakVRAMMiB": 300, "errorRate": .005},
		},
	}
	publish := func(proposal optimization.Proposal) string {
		t.Helper()
		body, err := json.Marshal(proposal)
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(body)
		id := hex.EncodeToString(digest[:])
		if err := os.WriteFile(filepath.Join(dir, id+".json"), body, 0600); err != nil {
			t.Fatal(err)
		}
		return id
	}
	legacyID := publish(p)
	lookup := optimizationLookup{ProposalID: legacyID, CheckCurrent: true}
	var review optimizationReview
	if err := call(target.NsID, "get_inference_optimization", lookup, &review); err != nil || review.Eligible ||
		review.Reason != "transition_evidence_required" || review.ProposalID != legacyID {
		t.Fatalf("proposal without measured transition evidence: eligible=%t, reason=%q, err=%v", review.Eligible, review.Reason, err)
	}
	evidence := optimization.TransitionEvidence{
		CorrelationID: previousFlow, PreviousRevision: 1, Revision: 2,
		BaselineApplicationDeploymentID: earlierApp, CandidateApplicationDeploymentID: laterApp,
		RuntimeImage: load.RuntimeImage, GPUUUIDs: []string{"GPU-fixture"},
		LastBaselineResponseAt: laterAppliedAt.Add(-30 * time.Second), FirstCandidateResponseAt: laterAppliedAt.Add(20 * time.Second),
	}
	for _, test := range []struct {
		name, reason string
		change       func(*optimization.TransitionEvidence)
	}{
		{"over budget", "transition_downtime_exceeds_budget", func(e *optimization.TransitionEvidence) {
			e.FirstCandidateResponseAt = e.LastBaselineResponseAt.Add(153*time.Second + 400*time.Millisecond)
		}},
		{"runtime health before the application settled", "transition_evidence_invalid", func(e *optimization.TransitionEvidence) {
			e.FirstCandidateResponseAt = laterAppliedAt.Add(-time.Second)
		}},
		{"wrong application deployment", "transition_evidence_invalid", func(e *optimization.TransitionEvidence) {
			e.CandidateApplicationDeploymentID = earlierApp
		}},
		{"wrong GPU", "transition_evidence_invalid", func(e *optimization.TransitionEvidence) {
			e.GPUUUIDs = []string{"another-GPU"}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := evidence
			test.change(&changed)
			proposal := p
			proposal.Transition = &changed
			q := optimizationLookup{ProposalID: publish(proposal), CheckCurrent: true}
			var got optimizationReview
			if err := call(target.NsID, "get_inference_optimization", q, &got); err != nil || got.Eligible || got.Reason != test.reason {
				t.Fatalf("transition evidence: eligible=%t, reason=%q, err=%v", got.Eligible, got.Reason, err)
			}
		})
	}
	p.Transition = &evidence
	lookup.ProposalID = publish(p)
	if err := call(target.NsID, "get_inference_optimization", lookup, &review); err != nil || !review.Eligible || review.ProposalID != lookup.ProposalID {
		t.Fatalf("verified transition: eligible=%t, reason=%q, err=%v", review.Eligible, review.Reason, err)
	}
	if err := call("other", "get_inference_optimization", lookup, &review); err == nil {
		t.Fatal("an outside namespace read the proposal")
	}
	afterAppliedAt := settled.Add(5 * time.Minute)
	afterQuery := seedRevision(flow, 2, 32, afterAppliedAt,
		"e82cb140-c4d1-468b-a819-16e262c00bc8", "cc8d14b1-cd6e-47dc-b807-39396014ae6b")
	if err := call(target.NsID, "get_inference_optimization", lookup, &review); err != nil || review.Eligible {
		t.Fatalf("replaced revision still eligible: %+v, %v", review, err)
	}
	var after observation.Record
	if err := call(target.NsID, "get_inference_observation", afterQuery, &after); err != nil || !after.Metrics.Complete {
		t.Fatalf("post-apply observation = %+v, %v", after, err)
	}
	load.ObservationID, load.Window, load.WithinSLO = after.ObservationID, after.Window, 240
	downtime := 35.0
	input := optimizationComparisonRequest{ProposalID: legacyID, Load: load, DowntimeSeconds: &downtime}
	var comparison optimizationComparison
	if err := call(target.NsID, "compare_inference_optimization", input, &comparison); err != nil ||
		comparison.Status != "KEEP" || comparison.Observation.MaxNumSeqs != 32 {
		t.Fatalf("comparison = %+v, %v", comparison, err)
	}
	modern := input
	modern.ProposalID = lookup.ProposalID
	if err := call(target.NsID, "compare_inference_optimization", modern, &comparison); err == nil {
		t.Error("a current proposal accepted a duration without application response evidence")
	}
	transition := optimization.TransitionEvidence{
		CorrelationID: flow, PreviousRevision: 1, Revision: 2,
		BaselineApplicationDeploymentID: before.ApplicationDeploymentID, CandidateApplicationDeploymentID: after.ApplicationDeploymentID,
		RuntimeImage: load.RuntimeImage, GPUUUIDs: evidence.GPUUUIDs,
		LastBaselineResponseAt:   afterAppliedAt.Add(-2 * time.Minute),
		FirstCandidateResponseAt: afterAppliedAt.Add(33*time.Second + 400*time.Millisecond),
	}
	compareTransition := func(e optimization.TransitionEvidence) error {
		return call(target.NsID, "compare_inference_optimization", map[string]any{
			"proposalId": lookup.ProposalID, "load": load, "transitionEvidence": e,
		}, &comparison)
	}
	if err := compareTransition(transition); err != nil || comparison.Status != "ROLLBACK_REVIEW" || comparison.DowntimeSeconds != 153.4 {
		t.Errorf("153.4s application response gap: status=%s seconds=%v err=%v", comparison.Status, comparison.DowntimeSeconds, err)
	}
	healthOnly := transition
	healthOnly.FirstCandidateResponseAt = healthOnly.LastBaselineResponseAt.Add(45 * time.Second)
	if err := compareTransition(healthOnly); err == nil {
		t.Error("runtime health was accepted before the application revision settled")
	}
	wrongApplication := transition
	wrongApplication.CandidateApplicationDeploymentID = before.ApplicationDeploymentID
	if err := compareTransition(wrongApplication); err == nil {
		t.Error("response from a different deployment was compared")
	}
	transition.LastBaselineResponseAt = afterAppliedAt.Add(-30 * time.Second)
	transition.FirstCandidateResponseAt = afterAppliedAt.Add(15 * time.Second)
	if err := compareTransition(transition); err != nil || comparison.Status != "KEEP" || comparison.DowntimeSeconds != 45 {
		t.Errorf("45s application response gap: status=%s seconds=%v err=%v", comparison.Status, comparison.DowntimeSeconds, err)
	}
	input.Load.ID = "different-workload"
	if err := call(target.NsID, "compare_inference_optimization", input, &comparison); err == nil {
		t.Fatal("an unrelated load report was compared")
	}
}

func writeOptimizationMetrics(t *testing.T, w http.ResponseWriter, r *http.Request, target model.NodeDeployTarget, deploymentID string) {
	end, err := time.Parse(time.RFC3339, r.URL.Query().Get("time"))
	if err != nil {
		t.Error(err)
		return
	}
	lines := []map[string]any{}
	job := "dcgm"
	if strings.Contains(r.URL.Query().Get("query"), `job="vllm"`) {
		job = "vllm"
	}
	add := func(name string, labels map[string]string, base, delta int) {
		metric := map[string]string{"__name__": name, "job": job, "nsId": target.NsID, "infraId": target.InfraID, "nodeId": target.NodeID}
		for key, value := range labels {
			metric[key] = value
		}
		values := [][2]any{}
		for i := 0; i < 4; i++ {
			values = append(values, [2]any{end.Unix() - int64(90-30*i), strconv.Itoa(base + i*delta)})
		}
		if job == "vllm" {
			metric["deploymentId"] = deploymentID
		}
		lines = append(lines, map[string]any{"metric": metric, "values": values})
	}
	if job == "vllm" {
		add("vllm:e2e_request_latency_seconds_bucket", map[string]string{"le": "0.5"}, 100, 60)
		add("vllm:e2e_request_latency_seconds_bucket", map[string]string{"le": "+Inf"}, 100, 60)
		add("vllm:generation_tokens_total", nil, 1000, 500)
		add("vllm:num_requests_waiting", nil, 2, 0)
		add("vllm:request_success_total", map[string]string{"finished_reason": "error"}, 0, 0)
		add("vllm:request_success_total", map[string]string{"finished_reason": "abort"}, 0, 0)
	} else {
		gpu := map[string]string{"UUID": "GPU-fixture"}
		add("DCGM_FI_DEV_GPU_UTIL", gpu, 50, 0)
		add("DCGM_FI_DEV_FB_USED", gpu, 7000, 0)
		add("DCGM_FI_DEV_FB_FREE", gpu, 8000, 0)
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"resultType": "matrix", "result": lines}})
}
