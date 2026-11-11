package engine

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/observation"
	"github.com/ish-hcc/ai-agent-proto/internal/tools"
)

// WithObservation enables metric reads on the approved deployment MCP path.
func WithObservation(source *observation.Prometheus) Option {
	return func(e *Engine) { e.telemetry = source }
}

type observationQuery struct {
	CorrelationID string             `json:"correlationId"`
	Revision      int                `json:"revision"`
	Window        observation.Window `json:"window"`
}

func (s *approvedApplies) registerObservation(registry *tools.Registry) {
	registry.Add(tools.Tool{
		Name: "get_inference_observation", Grade: tools.GradeRead,
		Description: "Join the currently serving approved revision to a completed vLLM and GPU metrics window. Returns metric values and data quality.",
		InputSchema: object(map[string]any{
			"correlationId": stringProp("Approved flow ID"),
			"revision":      map[string]any{"type": "integer", "minimum": 1},
			"window": object(map[string]any{
				"start": stringProp("Window start in RFC3339, at or after revision apply"),
				"end":   stringProp("Window end in RFC3339"),
			}, "start", "end"),
		}, "correlationId", "revision", "window"),
		Handle: s.observe,
	})
}

func (s *approvedApplies) observe(ctx context.Context, nsID string, input json.RawMessage) (any, error) {
	var q observationQuery
	if err := decodeInput(input, &q); err != nil {
		return nil, errors.New("supply an approved flow, revision and measurement window")
	}
	return s.observeWindow(ctx, nsID, q)
}

func (s *approvedApplies) observeWindow(ctx context.Context, nsID string, q observationQuery) (observation.Record, error) {
	if !correlationIDPattern.MatchString(q.CorrelationID) || q.Revision < 1 {
		return observation.Record{}, errors.New("supply an approved flow and revision")
	}
	if err := q.Window.Validate(time.Now()); err != nil {
		return observation.Record{}, err
	}
	record, err := s.observedApply(nsID, q)
	if err != nil {
		return observation.Record{}, err
	}
	inf := record.Request.Inference
	app := record.Request.Application
	if inf.Inference == nil || inf.Inference.RuntimeID != model.RuntimeVLLM || app.App == nil || len(inf.Targets) != 1 {
		return observation.Record{}, errors.New("observations require an approved single-node vLLM deployment")
	}
	target := inf.Targets[0]
	inference, err := s.archivedDeployment(record, record.Inference, target, model.NodeDeployTypeInference)
	if err != nil {
		return observation.Record{}, err
	}
	if !inference.GPU {
		return observation.Record{}, errors.New("observations require a GPU inference deployment")
	}
	if _, err := s.archivedDeployment(record, record.Application, target, model.NodeDeployTypeApp); err != nil {
		return observation.Record{}, err
	}
	place := observation.Placement{
		Namespace: nsID, InfraID: target.InfraID, NodeID: target.NodeID, DeploymentID: record.Inference.DeploymentID,
	}
	q.Window.Start, q.Window.End = q.Window.Start.UTC(), q.Window.End.UTC()
	id, err := approvalDigest(struct {
		Namespace   string
		Observation observationQuery
	}{nsID, q})
	if err != nil {
		return observation.Record{}, err
	}
	out := observation.Record{
		ObservationID: id, CorrelationID: q.CorrelationID, Revision: q.Revision,
		Placement: place, InferenceRunID: record.Inference.RunID,
		ApplicationDeploymentID: record.Application.DeploymentID, ApplicationRunID: record.Application.RunID,
		ApplicationName: app.App.Name, ApplicationVersion: app.App.Version,
		RuntimeID: inf.Inference.RuntimeID, ModelID: inf.Inference.ModelID, ArtifactID: inf.Inference.ArtifactID,
		Window: q.Window, AppliedAt: record.UpdatedAt,
	}
	if inference.Node != nil {
		out.SpecID = inference.Node.SpecID
	}
	if len(inf.Inference.Args) == 2 && inf.Inference.Args[0] == "--max-num-seqs" {
		out.MaxNumSeqs, _ = strconv.Atoi(inf.Inference.Args[1])
	}
	out.Metrics = s.engine.telemetry.Read(ctx, place, q.Window)
	out.CollectedAt = time.Now().UTC()
	if err := s.requireServingPair(ctx, record, place); err != nil {
		return observation.Record{}, err
	}
	if _, err := s.observedApply(nsID, q); err != nil {
		return observation.Record{}, err
	}
	return out, nil
}

func (s *approvedApplies) observedApply(nsID string, q observationQuery) (*applyRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, found, err := s.records.Find(func(r *applyRecord) bool {
		return r.CorrelationID == q.CorrelationID && r.Namespace == nsID
	})
	if err != nil || !found || record.Revision != q.Revision || record.Status != applySucceeded || s.active[record.Target] != "" {
		return nil, errors.New("the requested revision is not the currently settled apply")
	}
	if record.UpdatedAt.IsZero() || q.Window.Start.Before(record.UpdatedAt) {
		return nil, errors.New("the measurement window starts before the revision settled")
	}
	return record, nil
}

func (s *approvedApplies) archivedDeployment(record *applyRecord, phase applyStep, target model.NodeDeployTarget, kind string) (*model.NodeDeployResult, error) {
	invalid := errors.New("the archived engine run does not confirm the applied deployment")
	if phase.Status != applySucceeded || !model.ValidDeploymentID(phase.DeploymentID) || phase.RunID == "" {
		return nil, invalid
	}
	run, err := s.engine.GetRun(phase.RunID)
	if err != nil || run.Namespace != record.Namespace || run.Status != RunSucceeded || run.DryRun ||
		run.Workflow != WorkflowDeploy || run.EndedAt.IsZero() || run.EndedAt.After(record.UpdatedAt) {
		return nil, invalid
	}
	for _, step := range run.Steps {
		if step.Tool != toolGetDeployment || step.Status != StepSucceeded {
			continue
		}
		var d model.NodeDeployment
		if json.Unmarshal(step.Output, &d) == nil && d.ID == phase.DeploymentID && d.Type == kind &&
			d.Status == model.NodeDeployRunning && !d.DryRun && d.FinishedAt != nil &&
			!d.FinishedAt.After(record.UpdatedAt) && len(d.Nodes) == 1 &&
			d.Nodes[0].Target.Key() == target.Key() && d.Nodes[0].Status == model.NodeDeployRunning {
			return &d.Nodes[0], nil
		}
	}
	return nil, invalid
}

func (s *approvedApplies) requireServingPair(ctx context.Context, r *applyRecord, place observation.Placement) error {
	app := s.engine.app
	if err := app.Prepare(ctx); err != nil {
		return errors.New("the serving placement could not be checked")
	}
	raw, err := callApp(ctx, app, place.Namespace, "list_endpoints", map[string]any{
		"nsId": place.Namespace, "infraId": place.InfraID, "nodeId": place.NodeID,
		"available": true, "limit": model.MaxPageLimit,
	})
	var endpoints model.EndpointListResp
	if err != nil || json.Unmarshal(raw, &endpoints) != nil {
		return errors.New("the serving placement could not be checked")
	}
	var infServing, appServing bool
	for _, endpoint := range endpoints.Endpoints {
		if !endpoint.Available || endpoint.NsID != place.Namespace || endpoint.InfraID != place.InfraID || endpoint.NodeID != place.NodeID {
			continue
		}
		switch endpoint.Type {
		case model.NodeDeployTypeInference:
			infServing = infServing || endpoint.DeploymentID == r.Inference.DeploymentID
		case model.NodeDeployTypeApp:
			appServing = appServing || endpoint.DeploymentID == r.Application.DeploymentID
		}
	}
	if !infServing || !appServing {
		return errors.New("the approved deployment pair is no longer serving on the node")
	}
	return nil
}
