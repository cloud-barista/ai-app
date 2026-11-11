package optimization

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ish-hcc/ai-agent-proto/internal/observation"
)

func optimizationFixture() Proposal {
	start := time.Date(2026, 11, 10, 3, 0, 0, 0, time.UTC)
	window := observation.Window{Start: start, End: start.Add(2 * time.Minute)}
	p95, peak := 0.3, 7000.0
	obs := observation.Record{
		ObservationID: strings.Repeat("a", 64), CorrelationID: "fixture-flow", Revision: 2,
		Placement: observation.Placement{Namespace: "etri", InfraID: "travel", NodeID: "vm1", DeploymentID: "inference-before"},
		RuntimeID: "vllm", SpecID: "fixture-spec", ModelID: "fixture-model", ArtifactID: "fp8",
		ApplicationDeploymentID: "app-before", ApplicationName: "langflow", ApplicationVersion: 1,
		MaxNumSeqs: 16, Window: window,
		Metrics: observation.Metrics{
			Complete: true,
			Serving:  map[string]observation.Measurement{"latencyP95": {Quality: "measured", Value: &p95}},
			GPUs:     map[string]map[string]observation.Measurement{"GPU-1": {"peakUsedVRAM": {Quality: "measured", Value: &peak}}},
		},
	}
	return Proposal{
		SchemaVersion: SchemaVersion, Observation: obs,
		Load: LoadReport{
			Workload:      Workload{ID: "repeat-v1", RuntimeImage: "pinned-vllm", PromptTokensP50: 128, MaxOutputTokens: 64, OfferedRPS: 4},
			ObservationID: obs.ObservationID, Window: window, SLOSeconds: .5, Attempted: 480, WithinSLO: 120,
		},
		Guard: Guard{P95LimitSeconds: .5, PeakVRAMLimitMiB: 9000, ErrorRateLimit: .02,
			MinimumGainFraction: .1, EvaluationHorizonSeconds: 600, MaxUpdateDowntimeSeconds: 60},
		Decision: Decision{
			Mode: "shadow", Action: "UPDATE", Reason: "estimated_gain_passes_validation_margin", DataKind: "measurement",
			ModelVersion: modelVersion, ArtifactSHA256: strings.Repeat("b", 64), DatasetSHA256: strings.Repeat("c", 64),
			HoldoutPassed: true, CurrentMaxNumSeqs: 16, CandidateMaxNumSeqs: 32,
			Forecast:         Scores{latency: .35, goodput: 2, vram: 7900, failures: .005},
			CurrentForecast:  Scores{latency: .3, goodput: 1, vram: 7000, failures: 0},
			ValidationMargin: Scores{latency: .05, goodput: .1, vram: 300, failures: .005},
		},
	}
}

func TestPublishedProposalVerifiesDigestAndUpdateGuard(t *testing.T) {
	p := optimizationFixture()
	dir := t.TempDir()
	body, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	id := hex.EncodeToString(sum[:])
	path := filepath.Join(dir, id+".json")
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	published, err := ReadProposal(dir, id)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := published.UpdateEvidence(); err != nil || got[goodput] != 1 {
		t.Fatalf("baseline = %+v, %v", got, err)
	}
	if err := os.WriteFile(path, append(body, ' '), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadProposal(dir, id); err == nil {
		t.Fatal("modified proposal kept its old identity")
	}
	for name, change := range map[string]func(*Proposal){
		"fixture data":    func(p *Proposal) { p.Decision.DataKind = "fixture" },
		"missing budget":  func(p *Proposal) { p.Guard.MaxUpdateDowntimeSeconds = 0 },
		"downtime cost":   func(p *Proposal) { p.Guard.MaxUpdateDowntimeSeconds = 300 },
		"weak holdout":    func(p *Proposal) { p.Decision.HoldoutPassed = false },
		"unsafe latency":  func(p *Proposal) { p.Decision.Forecast[latency] = .49 },
		"incomplete load": func(p *Proposal) { p.Load.ObservationID = "wrong" },
	} {
		t.Run(name, func(t *testing.T) {
			p := optimizationFixture()
			change(&p)
			if _, err := p.UpdateEvidence(); err == nil {
				t.Fatal("the proposal passed an incomplete or unsafe update guard")
			}
		})
	}
}

func TestOptimizationComparisonUsesSameWorkloadAndAccountsForDowntime(t *testing.T) {
	p := optimizationFixture()
	after := p.Observation
	after.ObservationID, after.Revision, after.MaxNumSeqs = strings.Repeat("d", 64), 3, 32
	after.Placement.DeploymentID, after.ApplicationDeploymentID = "inference-after", "app-after"
	after.Window.Start, after.Window.End = after.Window.Start.Add(4*time.Minute), after.Window.End.Add(4*time.Minute)
	load := p.Load
	load.ObservationID, load.Window, load.WithinSLO = after.ObservationID, after.Window, 240
	got, err := p.Compare(after, load, 30)
	if err != nil || got.Status != "KEEP" || got.Before[goodput] != 1 || got.After[goodput] != 2 || got.EffectiveGoodput != 1.9 {
		t.Fatalf("comparison = %+v, %v", got, err)
	}
	got, err = p.Compare(after, load, 70)
	if err != nil || got.Status != "ROLLBACK_REVIEW" {
		t.Fatalf("downtime breach = %+v, %v", got, err)
	}
	load.Failed = 20
	got, err = p.Compare(after, load, 30)
	if err != nil || got.Status != "ROLLBACK_REVIEW" {
		t.Fatalf("error rate breach = %+v, %v", got, err)
	}
	load.OfferedRPS = 8
	if _, err := p.Compare(after, load, 30); err == nil {
		t.Fatal("a different workload was compared")
	}
}
