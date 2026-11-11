package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strconv"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/ish-hcc/ai-agent-proto/internal/archive"
	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/nodedeploy"
	"github.com/ish-hcc/ai-agent-proto/internal/tools"
)

const (
	applyQueued    = "queued"
	applyPending   = "confirmation_pending"
	applySucceeded = "succeeded"
	applyFailed    = "failed"
)

type approvedApplyRequest struct {
	CorrelationID    string      `json:"correlationId"`
	Revision         int         `json:"revision"`
	PreviousRevision int         `json:"previousRevision"`
	Inference        DeployInput `json:"inference"`
	Application      DeployInput `json:"application"`
}

type applyStep struct {
	Status       string `json:"status"`
	RunID        string `json:"engineRunId,omitempty"`
	DeploymentID string `json:"deploymentId,omitempty"`
}

type applyReceipt struct {
	CorrelationID    string    `json:"correlationId"`
	Revision         int       `json:"revision"`
	PreviousRevision int       `json:"previousRevision"`
	Namespace        string    `json:"nsId"`
	Target           string    `json:"target"`
	Status           string    `json:"status"`
	Inference        applyStep `json:"inference"`
	Application      applyStep `json:"application"`
	UpdatedAt        time.Time `json:"updatedAt"`
}

type applyRecord struct {
	applyReceipt
	Request             approvedApplyRequest            `json:"request"`
	RequestHash         string                          `json:"requestHash"`
	InferenceSubmission *model.NodeDeploymentSubmission `json:"inferenceSubmission,omitempty"`
	AppSubmission       *model.NodeDeploymentSubmission `json:"appSubmission,omitempty"`
}

type approvedApplies struct {
	ctx     context.Context
	engine  *Engine
	stages  *archive.Log[approvedDeploymentRecord]
	records *archive.Log[applyRecord]
	// shortcut: one engine owns the archive and reservations, use a shared
	// transactional store before running engine replicas.
	mu     sync.Mutex
	active map[string]string
}

func registerApprovedApplies(ctx context.Context, registry *tools.Registry, e *Engine, stages *archive.Log[approvedDeploymentRecord], dir string) error {
	records, err := archive.NewLog[applyRecord](dir)
	if err != nil {
		return err
	}
	s := &approvedApplies{ctx: ctx, engine: e, stages: stages, records: records, active: make(map[string]string)}
	registry.Add(tools.Tool{
		Name: "apply_approved_revision", Grade: tools.GradeWrite,
		Description: "Queue a single-node approved deployment revision. Poll get_applied_revision for both engine and deployment IDs.",
		InputSchema: object(map[string]any{
			"correlationId": stringProp("Approval flow ID"), "revision": map[string]any{"type": "integer", "minimum": 1},
			"previousRevision": map[string]any{"type": "integer", "minimum": 0},
			"inference":        deployRequestSchema(), "application": deployRequestSchema(),
		}, "correlationId", "revision", "previousRevision", "inference", "application"),
		Handle: s.submit,
	})
	registry.Add(tools.Tool{
		Name: "get_applied_revision", Grade: tools.GradeRead,
		Description: "Read one approved revision's deployment receipt and phase statuses.",
		InputSchema: object(map[string]any{
			"correlationId": stringProp("Approval flow ID"), "revision": map[string]any{"type": "integer", "minimum": 1},
		}, "correlationId", "revision"),
		Handle: s.get,
	})
	if e.telemetry != nil {
		s.registerObservation(registry)
	}
	if e.proposalDir != "" {
		if e.telemetry == nil {
			return errors.New("optimization proposals require inference observations")
		}
		s.registerOptimization(registry)
	}
	return nil
}

func (s *approvedApplies) submit(_ context.Context, nsID string, raw json.RawMessage) (any, error) {
	var req approvedApplyRequest
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || dec.Decode(new(any)) != io.EOF {
		return nil, errors.New("supply one approved apply request")
	}
	if err := validateApprovedApply(nsID, req); err != nil {
		return nil, err
	}
	digest, err := approvalDigest(req)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx.Err() != nil {
		return nil, errors.New("the apply worker is stopping")
	}
	record, found, err := s.records.Find(func(r *applyRecord) bool { return r.CorrelationID == req.CorrelationID })
	if err != nil {
		return nil, errors.New("the apply journal could not be read")
	}
	target := req.Inference.Targets[0].Key()
	if found && (record.Namespace != nsID || record.Target != target ||
		record.Request.Inference.Inference.RuntimeID != req.Inference.Inference.RuntimeID ||
		record.Request.Application.App.Name != req.Application.App.Name ||
		!reflect.DeepEqual(record.Request.Inference.PlacementReview, req.Inference.PlacementReview)) {
		return nil, errors.New("the revision does not match the approved placement")
	}
	if found && record.Revision == req.Revision {
		if digest != record.RequestHash {
			return nil, errors.New("the revision has a different approved body")
		}
		if record.Status == applySucceeded || record.Status == applyFailed || s.active[target] == req.CorrelationID {
			return record.applyReceipt, nil
		}
	} else {
		if (found && (record.Revision != req.PreviousRevision || (record.Status != applySucceeded && record.Status != applyFailed))) ||
			(!found && req.Revision != 1) {
			return nil, errors.New("approve the next revision after the current apply has settled")
		}
		if !found {
			if err := s.requireStagedInference(nsID, req); err != nil {
				return nil, err
			}
		}
		record, err = makeApplyRecord(nsID, req, digest, record)
		if err != nil {
			return nil, err
		}
	}
	if s.active[target] != "" {
		return nil, errors.New("the node has an apply in progress")
	}
	record.Status = applyQueued
	if err := s.save(record); err != nil {
		return nil, err
	}
	s.active[target] = req.CorrelationID
	go s.apply(*record)
	return record.applyReceipt, nil
}

func validateApprovedApply(nsID string, req approvedApplyRequest) error {
	inf, app := req.Inference, req.Application
	if !correlationIDPattern.MatchString(req.CorrelationID) || req.Revision < 1 || req.PreviousRevision != req.Revision-1 ||
		inf.Type != model.NodeDeployTypeInference || app.Type != model.NodeDeployTypeApp ||
		inf.Inference == nil || inf.Inference.RuntimeID == "" || app.App == nil || app.App.Version < 1 ||
		len(inf.Targets) != 1 || len(app.Targets) != 1 || inf.Targets[0] != app.Targets[0] || inf.Targets[0].NsID != nsID ||
		inf.Submission != nil || app.Submission != nil || inf.RequireDryRun || app.RequireDryRun {
		return errors.New("supply a pinned inference and app for one node in the approved namespace")
	}
	if !reflect.DeepEqual(inf.PlacementReview, app.PlacementReview) {
		return errors.New("inference and application must use the same placement review")
	}
	if len(inf.Inference.Env) != 0 || inf.Inference.APIKey != "" || len(app.App.Env) != 0 ||
		app.App.InferenceBinding == nil || app.App.InferenceBinding.DeploymentID != "" {
		return errors.New("use server-side secret references and a connection binding without a deployment ID")
	}
	if b := app.App.InferenceBinding; b.Native && inf.Inference.RuntimeID != model.RuntimeOllama {
		return errors.New("native connection requires Ollama")
	}
	args := inf.Inference.Args
	if len(args) > 0 {
		if inf.Inference.RuntimeID != model.RuntimeVLLM || len(args) != 2 || args[0] != "--max-num-seqs" {
			return errors.New("approved runtime changes support --max-num-seqs on vLLM")
		}
		value, err := strconv.Atoi(args[1])
		if err != nil || value < 1 {
			return errors.New("--max-num-seqs requires a positive integer")
		}
	}
	return inf.Validate()
}

func (s *approvedApplies) requireStagedInference(nsID string, req approvedApplyRequest) error {
	stage := approvedDeploymentRequest{CorrelationID: req.CorrelationID, Revision: 1, Deployment: req.Inference}
	stage.Deployment.RequireDryRun = true
	digest, err := approvalDigest(stage)
	if err != nil {
		return err
	}
	record, found, err := s.stages.Find(func(r *approvedDeploymentRecord) bool {
		return r.CorrelationID == req.CorrelationID && r.Namespace == nsID
	})
	if err != nil || !found || record.Status != "blocked" || record.RequestHash != digest {
		return errors.New("stage the exact initial inference request before live apply")
	}
	return nil
}

func makeApplyRecord(nsID string, req approvedApplyRequest, digest string, previous *applyRecord) (*applyRecord, error) {
	r := &applyRecord{
		applyReceipt: applyReceipt{
			CorrelationID: req.CorrelationID, Revision: req.Revision, PreviousRevision: req.PreviousRevision,
			Namespace: nsID, Target: req.Inference.Targets[0].Key(),
			Inference: applyStep{Status: "waiting"}, Application: applyStep{Status: "waiting"},
		},
		Request: req, RequestHash: digest,
	}
	var infPrevious, appPrevious string
	if previous != nil {
		infPrevious = lastAdmitted(previous.Inference, previous.InferenceSubmission)
		appPrevious = lastAdmitted(previous.Application, previous.AppSubmission)
		if previous.Inference.Status == applySucceeded && sameDeployInput(req.Inference, previous.Request.Inference) {
			r.Inference = previous.Inference
			if previous.Application.Status == applySucceeded && sameDeployInput(req.Application, previous.Request.Application) {
				return nil, errors.New("the approved configuration already serves on this node")
			}
		}
	}
	if r.Inference.Status != applySucceeded {
		id, err := nodedeploy.NewDeploymentID()
		if err != nil {
			return nil, err
		}
		r.InferenceSubmission = &model.NodeDeploymentSubmission{ID: id, PreviousDeploymentID: infPrevious}
	}
	appID, err := nodedeploy.NewDeploymentID()
	if err != nil {
		return nil, err
	}
	r.AppSubmission = &model.NodeDeploymentSubmission{ID: appID, PreviousDeploymentID: appPrevious}
	boundID := r.Inference.DeploymentID
	if r.InferenceSubmission != nil {
		boundID = r.InferenceSubmission.ID
	}
	app := r.appRequest(boundID)
	if err := app.Validate(); err != nil {
		return nil, err
	}
	return r, nil
}

func sameDeployInput(a, b DeployInput) bool {
	encodedA, errA := json.Marshal(a)
	encodedB, errB := json.Marshal(b)
	return errA == nil && errB == nil && bytes.Equal(encodedA, encodedB)
}

func lastAdmitted(step applyStep, submission *model.NodeDeploymentSubmission) string {
	if step.DeploymentID != "" {
		return step.DeploymentID
	}
	if submission != nil {
		return submission.PreviousDeploymentID
	}
	return ""
}

func (r *applyRecord) appRequest(inferenceID string) DeployInput {
	req := r.Request.Application
	app := *req.App
	binding := *app.InferenceBinding
	binding.DeploymentID = inferenceID
	app.InferenceBinding = &binding
	req.App, req.Submission = &app, r.AppSubmission
	return req
}

func (s *approvedApplies) apply(record applyRecord) {
	defer func() {
		s.mu.Lock()
		delete(s.active, record.Target)
		s.mu.Unlock()
	}()
	ctx, cancel := context.WithTimeout(s.ctx, 2*s.engine.waitTimeout+time.Minute)
	defer cancel()
	if record.Inference.Status != applySucceeded {
		req := record.Request.Inference
		req.Submission = record.InferenceSubmission
		record.Inference = s.deploy(ctx, record.Namespace, req)
		record.Status = record.Inference.Status
		if record.Status == applySucceeded {
			record.Status = applyQueued
		}
		if err := s.save(&record); err != nil || record.Inference.Status != applySucceeded {
			return
		}
	}
	record.Application = s.deploy(ctx, record.Namespace, record.appRequest(record.Inference.DeploymentID))
	record.Status = record.Application.Status
	_ = s.save(&record)
}

func (s *approvedApplies) deploy(ctx context.Context, nsID string, req DeployInput) applyStep {
	result := applyStep{Status: applyPending}
	run, err := s.engine.Deploy(ctx, nsID, req)
	if err != nil {
		if errors.Is(err, ErrInvalidInput) {
			result.Status = applyFailed
		}
		return result
	}
	result.RunID = run.RunID
	for _, step := range run.Steps {
		if step.Unconfirmed {
			return result
		}
		if step.Tool != toolCreateDeployment && step.Tool != toolGetDeployment {
			continue
		}
		var d model.NodeDeployment
		if json.Unmarshal(step.Output, &d) != nil || d.ID != req.Submission.ID || d.DryRun {
			continue
		}
		result.DeploymentID = d.ID
		// A replay can return a running deployment before the status read succeeds.
		if ended(d.Status) && d.Status != model.NodeDeployRunning && d.Status != model.NodeDeployInterrupted {
			result.Status = applyFailed
		}
	}
	switch {
	case run.Status == RunSucceeded && result.DeploymentID != "":
		result.Status = applySucceeded
	case run.Status == RunFailed && result.DeploymentID == "":
		result.Status = applyFailed
	}
	return result
}

func (s *approvedApplies) save(r *applyRecord) error {
	r.UpdatedAt = time.Now().UTC()
	if err := s.records.Append(*r); err != nil {
		log.Error().Err(err).Str("correlationId", r.CorrelationID).Msg("Could not save approved apply state")
		return errors.New("the apply journal could not be saved")
	}
	return nil
}

func (s *approvedApplies) get(_ context.Context, nsID string, input json.RawMessage) (any, error) {
	var req struct {
		CorrelationID string `json:"correlationId"`
		Revision      int    `json:"revision"`
	}
	if err := decodeInput(input, &req); err != nil || !correlationIDPattern.MatchString(req.CorrelationID) || req.Revision < 1 {
		return nil, errors.New("supply an approved flow and revision")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, found, err := s.records.Find(func(r *applyRecord) bool {
		return r.CorrelationID == req.CorrelationID && r.Revision == req.Revision && r.Namespace == nsID
	})
	if err != nil || !found {
		return nil, errors.New("the apply revision could not be read")
	}
	if record.Status == applyQueued && s.active[record.Target] != record.CorrelationID {
		record.Status = applyPending
	}
	return record.applyReceipt, nil
}

func approvalDigest(req any) (string, error) {
	canonical, err := json.Marshal(req)
	if err != nil {
		return "", errors.New("approved request could not be encoded")
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}
