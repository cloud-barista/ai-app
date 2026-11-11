package engine

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"time"

	"github.com/ish-hcc/ai-agent-proto/internal/observation"
	"github.com/ish-hcc/ai-agent-proto/internal/optimization"
	"github.com/ish-hcc/ai-agent-proto/internal/tools"
)

const optimizationMaxObservationAge = 30 * time.Minute

// WithOptimizationProposals enables reads from the operator-owned publication directory.
func WithOptimizationProposals(dir string) Option {
	return func(e *Engine) { e.proposalDir = dir }
}

type optimizationLookup struct {
	ProposalID   string `json:"proposalId"`
	CheckCurrent bool   `json:"checkCurrent"`
}

type optimizationReview struct {
	ProposalID string                `json:"proposalId"`
	Proposal   optimization.Proposal `json:"proposal"`
	Eligible   bool                  `json:"eligible"`
	Reason     string                `json:"reason,omitempty"`
}

type optimizationComparisonRequest struct {
	ProposalID      string                           `json:"proposalId"`
	Load            optimization.LoadReport          `json:"load"`
	DowntimeSeconds *float64                         `json:"downtimeSeconds,omitempty"`
	Transition      *optimization.TransitionEvidence `json:"transitionEvidence,omitempty"`
}

type optimizationComparison struct {
	ProposalID string                           `json:"proposalId"`
	Transition *optimization.TransitionEvidence `json:"transitionEvidence,omitempty"`
	optimization.Comparison
}

func (s *approvedApplies) registerOptimization(registry *tools.Registry) {
	registry.Add(tools.Tool{
		Name: "get_inference_optimization", Grade: tools.GradeRead,
		Description: "Read a published vLLM optimization proposal. Request checkCurrent to verify its live deployment and metric evidence.",
		InputSchema: object(map[string]any{
			"proposalId": stringProp("SHA-256 of the published proposal"), "checkCurrent": map[string]any{"type": "boolean"},
		}, "proposalId"),
		Handle: s.reviewOptimization,
	})
	registry.Add(tools.Tool{
		Name: "compare_inference_optimization", Grade: tools.GradeRead,
		Description: "Compare the applied next revision against its published proposal using the same workload.",
		InputSchema: object(map[string]any{
			"proposalId":         stringProp("SHA-256 of the published proposal"),
			"load":               map[string]any{"type": "object", "description": "Same workload and measured outcomes for the new observation window"},
			"downtimeSeconds":    map[string]any{"type": "number", "minimum": 0, "description": "Duration for a legacy proposal"},
			"transitionEvidence": map[string]any{"type": "object", "description": "Application response times and applied revision identities"},
		}, "proposalId", "load"),
		Handle: s.compareOptimization,
	})
}

func (s *approvedApplies) reviewOptimization(ctx context.Context, nsID string, input json.RawMessage) (any, error) {
	var q optimizationLookup
	if err := decodeInput(input, &q); err != nil {
		return nil, errors.New("supply a published proposal ID")
	}
	p, err := s.readOptimization(nsID, q.ProposalID)
	if err != nil {
		return nil, err
	}
	out := optimizationReview{ProposalID: q.ProposalID, Proposal: p, Reason: "current_evidence_unchecked"}
	if _, err := p.UpdateEvidence(); err != nil {
		out.Reason = err.Error()
		return out, nil
	}
	if !q.CheckCurrent {
		return out, nil
	}
	before := p.Observation
	if time.Since(before.Window.End) > optimizationMaxObservationAge {
		out.Reason = "observation_expired"
		return out, nil
	}
	currentQuery := observationQuery{
		CorrelationID: before.CorrelationID, Revision: before.Revision, Window: before.Window,
	}
	current, err := s.observeWindow(ctx, nsID, currentQuery)
	if err != nil {
		out.Reason = "current_observation_unavailable"
		return out, nil
	}
	before.CollectedAt, current.CollectedAt = time.Time{}, time.Time{}
	if !reflect.DeepEqual(before, current) {
		out.Reason = "current_observation_changed"
		return out, nil
	}
	if reason := s.optimizationTransitionReason(nsID, currentQuery, p); reason != "" {
		out.Reason = reason
		return out, nil
	}
	out.Eligible, out.Reason = true, ""
	return out, nil
}

func (s *approvedApplies) readOptimization(nsID, id string) (optimization.Proposal, error) {
	p, err := optimization.ReadProposal(s.engine.proposalDir, id)
	if err != nil || p.Observation.Placement.Namespace != nsID || !correlationIDPattern.MatchString(p.Observation.CorrelationID) {
		return optimization.Proposal{}, errors.New("the published optimization proposal is unavailable in this namespace")
	}
	return p, nil
}

func (s *approvedApplies) compareOptimization(ctx context.Context, nsID string, input json.RawMessage) (any, error) {
	var q optimizationComparisonRequest
	if err := decodeInput(input, &q); err != nil || (q.DowntimeSeconds == nil) == (q.Transition == nil) {
		return nil, errors.New("supply a proposal, a load report and one transition measurement")
	}
	p, err := s.readOptimization(nsID, q.ProposalID)
	if err != nil {
		return nil, err
	}
	if _, err := p.UpdateEvidence(); err != nil {
		return nil, err
	}
	if p.Transition != nil && q.Transition == nil {
		return nil, errors.New("the comparison requires application response evidence for the applied revision")
	}
	before := p.Observation
	query := observationQuery{CorrelationID: before.CorrelationID, Revision: before.Revision + 1, Window: q.Load.Window}
	after, err := s.observeWindow(ctx, nsID, query)
	if err != nil {
		return nil, err
	}
	previous, current, err := s.checkOptimizationConfig(nsID, before, after, p.Decision.CandidateMaxNumSeqs)
	if err != nil {
		return nil, err
	}
	var downtime float64
	if q.Transition != nil {
		gap, ok := applicationResponseGap(q.Transition, previous, current, after.Window.Start)
		if !ok || q.Transition.LastBaselineResponseAt.Before(before.Window.End) || !matchesTransitionRuntime(q.Transition, p.Load.RuntimeImage, before) {
			return nil, errors.New("application response evidence does not match the applied revision or measurement windows")
		}
		downtime = gap
	} else {
		downtime = *q.DowntimeSeconds
	}
	comparison, err := p.Compare(after, q.Load, downtime)
	if err != nil {
		return nil, err
	}
	return optimizationComparison{ProposalID: q.ProposalID, Transition: q.Transition, Comparison: comparison}, nil
}

func (s *approvedApplies) checkOptimizationConfig(nsID string, before, after observation.Record, candidate int) (*applyRecord, *applyRecord, error) {
	invalid := errors.New("the applied revision does not match the optimization configuration")
	previous, found, err := s.records.Find(func(r *applyRecord) bool {
		return r.CorrelationID == before.CorrelationID && r.Revision == before.Revision && r.Namespace == nsID
	})
	if err != nil || !found || previous.Status != applySucceeded ||
		previous.Inference.DeploymentID != before.Placement.DeploymentID ||
		previous.Application.DeploymentID != before.ApplicationDeploymentID {
		return nil, nil, invalid
	}
	current, err := s.observedApply(nsID, observationQuery{
		CorrelationID: after.CorrelationID, Revision: after.Revision, Window: after.Window,
	})
	if err != nil || current.Inference.DeploymentID != after.Placement.DeploymentID ||
		current.Application.DeploymentID != after.ApplicationDeploymentID ||
		!matchesOptimizationConfig(previous.Request, current.Request, candidate) {
		return nil, nil, invalid
	}
	return previous, current, nil
}

func matchesOptimizationConfig(before, after approvedApplyRequest, candidate int) bool {
	expected := before.Inference
	if expected.Inference == nil {
		return false
	}
	inference := *expected.Inference
	inference.Args = []string{"--max-num-seqs", strconv.Itoa(candidate)}
	expected.Inference = &inference
	return sameDeployInput(after.Inference, expected) && sameDeployInput(after.Application, before.Application)
}
