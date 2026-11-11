package engine

import (
	"maps"
	"slices"
	"time"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/observation"
	"github.com/ish-hcc/ai-agent-proto/internal/optimization"
)

func (s *approvedApplies) optimizationTransitionReason(nsID string, query observationQuery, p optimization.Proposal) string {
	const invalid = "transition_evidence_invalid"
	evidence := p.Transition
	if evidence == nil {
		return "transition_evidence_required"
	}
	if !correlationIDPattern.MatchString(evidence.CorrelationID) || evidence.Revision < 2 ||
		evidence.Revision-1 != evidence.PreviousRevision || !matchesTransitionRuntime(evidence, p.Load.RuntimeImage, p.Observation) {
		return invalid
	}
	current, err := s.observedApply(nsID, query)
	if err != nil || len(current.Request.Inference.Targets) != 1 {
		return invalid
	}
	target := current.Request.Inference.Targets[0]
	find := func(revision int, appID string) *applyRecord {
		record, found, err := s.records.Find(func(r *applyRecord) bool {
			return r.Namespace == nsID && r.CorrelationID == evidence.CorrelationID && r.Revision == revision
		})
		if err != nil || !found || record.Status != applySucceeded || record.Application.DeploymentID != appID {
			return nil
		}
		inference, err := s.archivedDeployment(record, record.Inference, target, model.NodeDeployTypeInference)
		if err != nil || !inference.GPU || inference.Node == nil || inference.Node.SpecID != p.Observation.SpecID {
			return nil
		}
		if _, err := s.archivedDeployment(record, record.Application, target, model.NodeDeployTypeApp); err != nil {
			return nil
		}
		return record
	}
	baseline := find(evidence.PreviousRevision, evidence.BaselineApplicationDeploymentID)
	candidate := find(evidence.Revision, evidence.CandidateApplicationDeploymentID)
	downtime, ok := applicationResponseGap(evidence, baseline, candidate, query.Window.Start)
	if !ok ||
		baseline.Inference.DeploymentID == candidate.Inference.DeploymentID ||
		baseline.Application.DeploymentID == candidate.Application.DeploymentID ||
		!sameDeployInput(baseline.Request.Inference, current.Request.Inference) ||
		!sameDeployInput(baseline.Request.Application, current.Request.Application) ||
		!matchesOptimizationConfig(baseline.Request, candidate.Request, p.Decision.CandidateMaxNumSeqs) {
		return invalid
	}
	if downtime > p.Guard.MaxUpdateDowntimeSeconds {
		return "transition_downtime_exceeds_budget"
	}
	return ""
}

func matchesTransitionRuntime(evidence *optimization.TransitionEvidence, image string, observed observation.Record) bool {
	return evidence.RuntimeImage == image &&
		slices.Equal(slices.Sorted(slices.Values(evidence.GPUUUIDs)), slices.Sorted(maps.Keys(observed.Metrics.GPUs)))
}

func applicationResponseGap(evidence *optimization.TransitionEvidence, baseline, candidate *applyRecord, nextWindowStart time.Time) (float64, bool) {
	if baseline == nil || candidate == nil ||
		evidence.CorrelationID != baseline.CorrelationID || evidence.CorrelationID != candidate.CorrelationID ||
		evidence.PreviousRevision != baseline.Revision || evidence.Revision != candidate.Revision || candidate.PreviousRevision != baseline.Revision ||
		evidence.BaselineApplicationDeploymentID != baseline.Application.DeploymentID ||
		evidence.CandidateApplicationDeploymentID != candidate.Application.DeploymentID ||
		evidence.LastBaselineResponseAt.Before(baseline.UpdatedAt) || evidence.LastBaselineResponseAt.After(candidate.UpdatedAt) ||
		evidence.FirstCandidateResponseAt.Before(candidate.UpdatedAt) || evidence.FirstCandidateResponseAt.After(nextWindowStart) ||
		!evidence.FirstCandidateResponseAt.After(evidence.LastBaselineResponseAt) {
		return 0, false
	}
	return evidence.FirstCandidateResponseAt.Sub(evidence.LastBaselineResponseAt).Seconds(), true
}
