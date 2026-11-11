package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/ish-hcc/ai-agent-proto/internal/archive"
	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/nodedeploy"
	"github.com/ish-hcc/ai-agent-proto/internal/tools"
)

const ApprovedDeploymentServerName = "ai-mcmp-app-approved-deployment"

const stagePending = "confirmation_pending"

var correlationIDPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)

type approvedDeploymentRequest struct {
	CorrelationID string                  `json:"correlationId"`
	Revision      int                     `json:"revision"`
	Deployment    model.NodeDeploymentReq `json:"deployment"`
}

type approvedDeploymentReceipt struct {
	CorrelationID string    `json:"correlationId"`
	Revision      int       `json:"revision"`
	Namespace     string    `json:"nsId"`
	Target        string    `json:"target"`
	Status        string    `json:"status"`
	RunID         string    `json:"engineRunId,omitempty"`
	DeploymentID  string    `json:"deploymentId,omitempty"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

type approvedDeploymentRecord struct {
	approvedDeploymentReceipt
	RequestHash          string `json:"requestHash"`
	ReservedDeploymentID string `json:"reservedDeploymentId,omitempty"`
}

type approvedDeployments struct {
	engine  *Engine
	records *archive.Log[approvedDeploymentRecord]
	// shortcut: one engine process owns this archive, use shared transactional
	// storage before running engine replicas.
	mu sync.Mutex
}

// NewApprovedDeploymentTools exposes the initial approved Revision on a
// separate authenticated MCP endpoint for the AI-Ops approval service.
func NewApprovedDeploymentTools(dir string, e *Engine, applyCtx context.Context) (*tools.Registry, error) {
	records, err := archive.NewLog[approvedDeploymentRecord](dir)
	if err != nil {
		return nil, err
	}
	s := &approvedDeployments{engine: e, records: records}
	registry := tools.NewRegistryFunc(func() bool {
		return applyCtx == nil || e.DryRun(context.Background())
	})
	registry.Add(tools.Tool{
		Name:        "stage_approved_deployment",
		Grade:       tools.GradeWrite,
		Description: "Stage one approved single-node inference request under AI-App dry-run. Use the prepared request with no environment values or API key.",
		InputSchema: object(map[string]any{
			"correlationId": stringProp("ID assigned by the AI-Ops approval service"),
			"revision":      map[string]any{"type": "integer", "const": 1},
			"deployment":    deployRequestSchema(),
		}, "correlationId", "revision", "deployment"),
		Handle: s.submit,
	})
	registry.Add(tools.Tool{
		Name:        "get_approved_deployment",
		Grade:       tools.GradeRead,
		Description: "Read the saved receipt for an approved deployment.",
		InputSchema: object(map[string]any{
			"correlationId": stringProp("ID of the approved deployment"),
		}, "correlationId"),
		Handle: s.get,
	})
	if applyCtx != nil {
		if e.approvedApp == nil {
			return nil, errors.New("live apply requires the approved AI-App submission client")
		}
		if err := registerApprovedApplies(applyCtx, registry, e, records, filepath.Join(dir, "apply")); err != nil {
			return nil, err
		}
	}
	return registry, nil
}

func (s *approvedDeployments) submit(ctx context.Context, nsID string, input json.RawMessage) (any, error) {
	var req approvedDeploymentRequest
	dec := json.NewDecoder(bytes.NewReader(input))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || dec.Decode(new(any)) != io.EOF {
		return nil, errors.New("supply one approved deployment request")
	}
	deployment := &req.Deployment
	if !correlationIDPattern.MatchString(req.CorrelationID) || req.Revision != 1 || deployment.Submission != nil ||
		deployment.Type != model.NodeDeployTypeInference || deployment.Inference == nil ||
		deployment.Inference.RuntimeID == "" || len(deployment.Targets) != 1 {
		return nil, errors.New("initial Revision 1 requires one node and a pinned inference runtime")
	}
	if deployment.Targets[0].NsID != nsID {
		return nil, errors.New("approved target must be in the request namespace")
	}
	if len(deployment.Inference.Env) != 0 || deployment.Inference.APIKey != "" {
		return nil, errors.New("prepared inference cannot contain environment values or an API key")
	}
	if err := deployment.Validate(); err != nil {
		return nil, err
	}
	deployment.RequireDryRun = true
	digest, err := approvalDigest(req)
	if err != nil {
		return nil, err
	}
	record := approvedDeploymentRecord{
		approvedDeploymentReceipt: approvedDeploymentReceipt{
			CorrelationID: req.CorrelationID, Revision: req.Revision, Namespace: nsID,
			Target: deployment.Targets[0].Key(), Status: stagePending, UpdatedAt: time.Now().UTC(),
		},
		RequestHash: digest,
	}
	previous, err := s.reserve(&record)
	if err != nil {
		return nil, err
	}
	if previous != nil {
		return s.reconcile(ctx, *previous)
	}
	if record.ReservedDeploymentID != "" {
		deployment.Submission = &model.NodeDeploymentSubmission{ID: record.ReservedDeploymentID}
	}

	run, err := s.engine.Deploy(ctx, nsID, *deployment)
	if err != nil {
		log.Warn().Str("correlationId", req.CorrelationID).Msg("Approved deployment did not return an engine run")
		if errors.Is(err, ErrInvalidInput) {
			record.Status = "failed"
		}
	} else {
		record.RunID = run.RunID
		createMayHaveBeenAccepted := false
		for _, step := range run.Steps {
			if step.Tool != toolCreateDeployment || step.StartedAt == nil || step.createRejected {
				continue
			}
			createMayHaveBeenAccepted = true
			var created struct {
				ID string `json:"id"`
			}
			if json.Unmarshal(step.Output, &created) == nil &&
				(record.ReservedDeploymentID == "" || record.ReservedDeploymentID == created.ID) {
				record.DeploymentID = created.ID
			}
		}
		if run.Status == RunBlocked && run.DryRun && record.DeploymentID != "" {
			record.Status = string(RunBlocked)
		} else if run.Status == RunFailed && (!createMayHaveBeenAccepted || record.DeploymentID != "") {
			record.Status = string(RunFailed)
		}
	}
	return s.save(record)
}

func (s *approvedDeployments) reserve(record *approvedDeploymentRecord) (*approvedDeploymentRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous, found, err := s.records.Find(func(r *approvedDeploymentRecord) bool {
		return r.CorrelationID == record.CorrelationID
	})
	if err == nil && found {
		if previous.Namespace != record.Namespace || previous.RequestHash != record.RequestHash {
			return nil, errors.New("correlationId already has a different approved request")
		}
		return previous, nil
	}
	if err == nil && s.engine.approvedApp != nil {
		record.ReservedDeploymentID, err = nodedeploy.NewDeploymentID()
	}
	if err == nil {
		err = s.records.Append(*record)
	}
	if err != nil {
		log.Error().Err(err).Str("correlationId", record.CorrelationID).Msg("Failed to reserve approved deployment")
		return nil, errors.New("approved deployment could not be recorded")
	}
	return nil, nil
}

func (s *approvedDeployments) reconcile(ctx context.Context, record approvedDeploymentRecord) (any, error) {
	id := record.ReservedDeploymentID
	if record.Status != stagePending || id == "" {
		return record.approvedDeploymentReceipt, nil
	}
	r, err := s.engine.start(ctx, record.Namespace, WorkflowStatus, StatusInput{DeploymentID: id})
	if err != nil {
		return record.approvedDeploymentReceipt, nil
	}
	var d model.NodeDeployment
	step, ok := r.call(ctx, "read the approved dry-run", toolGetDeployment, map[string]string{"id": id}, &d)
	if !ok || d.ID != id || !d.DryRun || d.Request.Submission == nil ||
		*d.Request.Submission != (model.NodeDeploymentSubmission{ID: id}) {
		return record.approvedDeploymentReceipt, nil
	}
	staged := approvedDeploymentRequest{CorrelationID: record.CorrelationID, Revision: record.Revision, Deployment: d.Request}
	staged.Deployment.Submission = nil
	if digest, err := approvalDigest(staged); err != nil || digest != record.RequestHash {
		return record.approvedDeploymentReceipt, nil
	}
	switch d.Status {
	case model.NodeDeployDryRun:
		record.Status = string(RunBlocked)
		step.Message = outcome(&d)
	case model.NodeDeployFailed, model.NodeDeployInterrupted, model.NodeDeployRemoved, model.NodeDeployRemoveFailed:
		record.Status = string(RunFailed)
		r.fail(step, outcome(&d))
	default:
		return record.approvedDeploymentReceipt, nil
	}
	record.RunID, record.DeploymentID = r.finish().RunID, id
	return s.save(record)
}

func (s *approvedDeployments) save(record approvedDeploymentRecord) (any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, found, err := s.records.Find(func(r *approvedDeploymentRecord) bool {
		return r.CorrelationID == record.CorrelationID
	})
	if err == nil && found {
		// Recovery can finish while the original call is still returning.
		if current.Status != stagePending {
			return current.approvedDeploymentReceipt, nil
		}
		record.UpdatedAt = time.Now().UTC()
		err = s.records.Append(record)
	}
	if err != nil || !found {
		log.Error().Err(err).Str("correlationId", record.CorrelationID).Msg("Failed to save approved deployment receipt")
		return nil, errors.New("deployment receipt could not be saved")
	}
	return record.approvedDeploymentReceipt, nil
}

func (s *approvedDeployments) get(_ context.Context, nsID string, input json.RawMessage) (any, error) {
	var req struct {
		CorrelationID string `json:"correlationId"`
	}
	if err := decodeInput(input, &req); err != nil || !correlationIDPattern.MatchString(req.CorrelationID) {
		return nil, errors.New("supply an approved correlationId")
	}
	record, found, err := s.records.Find(func(r *approvedDeploymentRecord) bool {
		return r.CorrelationID == req.CorrelationID && r.Namespace == nsID
	})
	if err != nil {
		log.Error().Err(err).Str("correlationId", req.CorrelationID).Msg("Failed to read approved deployment receipt")
		return nil, errors.New("approved deployment receipt could not be read")
	}
	if !found {
		return nil, errors.New("approved deployment was not found")
	}
	return record.approvedDeploymentReceipt, nil
}
