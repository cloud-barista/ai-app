// Package optimization verifies published model evidence against observations.
package optimization

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"math"
	"os"
	"regexp"
	"time"

	"github.com/ish-hcc/ai-agent-proto/internal/observation"
)

const (
	SchemaVersion      = "khu-inference-optimization/v1"
	modelVersion       = "khu-vllm-max-num-seqs/v2"
	contextVersion     = "khu-vllm-max-num-seqs/v3"
	selectionVersion   = "khu-vllm-max-num-seqs/v4"
	neighborVersion    = "khu-vllm-max-num-seqs/v5"
	legacyModelVersion = "khu-vllm-max-num-seqs/v1"
	maxProposal        = 128 << 10
)

var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type Scores map[string]float64

const (
	latency  = "p95Seconds"
	goodput  = "goodputRps"
	vram     = "peakVRAMMiB"
	failures = "errorRate"
)

type Workload struct {
	ID              string  `json:"workloadId"`
	RuntimeImage    string  `json:"runtimeImage"`
	PromptTokensP50 int     `json:"promptTokensP50"`
	MaxOutputTokens int     `json:"maxOutputTokens"`
	OfferedRPS      float64 `json:"offeredRps"`
}

type LoadReport struct {
	Workload
	ObservationID string             `json:"observationId"`
	Window        observation.Window `json:"window"`
	SLOSeconds    float64            `json:"sloSeconds"`
	Attempted     int                `json:"attempted"`
	Failed        int                `json:"failed"`
	WithinSLO     int                `json:"withinSLO"`
}

type Guard struct {
	P95LimitSeconds          float64 `json:"p95LimitSeconds"`
	PeakVRAMLimitMiB         float64 `json:"peakVRAMLimitMiB"`
	ErrorRateLimit           float64 `json:"errorRateLimit"`
	MinimumGainFraction      float64 `json:"minimumGainFraction"`
	EvaluationHorizonSeconds float64 `json:"evaluationHorizonSeconds"`
	MaxUpdateDowntimeSeconds float64 `json:"maxUpdateDowntimeSeconds"`
}

type Decision struct {
	Mode                string `json:"mode"`
	Action              string `json:"action"`
	Reason              string `json:"reason"`
	DataKind            string `json:"dataKind"`
	ModelVersion        string `json:"modelVersion"`
	ArtifactSHA256      string `json:"artifactSha256"`
	DatasetSHA256       string `json:"datasetSha256"`
	HoldoutPassed       bool   `json:"holdoutPassed"`
	CurrentMaxNumSeqs   int    `json:"currentMaxNumSeqs"`
	CandidateMaxNumSeqs int    `json:"candidateMaxNumSeqs"`
	Forecast            Scores `json:"forecast"`
	CurrentForecast     Scores `json:"currentForecast"`
	ValidationMargin    Scores `json:"validationMargin"`
}

// TransitionEvidence identifies an approved change measured at the application endpoint.
type TransitionEvidence struct {
	CorrelationID                    string    `json:"correlationId"`
	PreviousRevision                 int       `json:"previousRevision"`
	Revision                         int       `json:"revision"`
	BaselineApplicationDeploymentID  string    `json:"baselineApplicationDeploymentId"`
	CandidateApplicationDeploymentID string    `json:"candidateApplicationDeploymentId"`
	RuntimeImage                     string    `json:"runtimeImage"`
	GPUUUIDs                         []string  `json:"gpuUUIDs"`
	LastBaselineResponseAt           time.Time `json:"lastBaselineResponseAt"`
	FirstCandidateResponseAt         time.Time `json:"firstCandidateResponseAt"`
}

type Proposal struct {
	SchemaVersion string              `json:"schemaVersion"`
	Observation   observation.Record  `json:"observation"`
	Load          LoadReport          `json:"load"`
	Guard         Guard               `json:"guard"`
	Decision      Decision            `json:"decision"`
	Transition    *TransitionEvidence `json:"transitionEvidence,omitempty"`
}

func ReadProposal(dir, id string) (Proposal, error) {
	var p Proposal
	invalid := errors.New("the published inference optimization proposal could not be read")
	if !digestPattern.MatchString(id) {
		return p, invalid
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return p, invalid
	}
	defer root.Close()
	file, err := root.Open(id + ".json")
	if err != nil {
		return p, invalid
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxProposal {
		return p, invalid
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxProposal+1))
	if err != nil || len(raw) > maxProposal {
		return p, invalid
	}
	sum := sha256.Sum256(raw)
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if id != hex.EncodeToString(sum[:]) || dec.Decode(&p) != nil || dec.Decode(new(any)) != io.EOF ||
		p.SchemaVersion != SchemaVersion || !digestPattern.MatchString(p.Observation.ObservationID) ||
		p.Observation.CorrelationID == "" || p.Observation.Revision < 1 || p.Observation.Placement.Namespace == "" ||
		p.Decision.Mode != "shadow" || (p.Decision.ModelVersion != modelVersion && p.Decision.ModelVersion != legacyModelVersion && p.Decision.ModelVersion != contextVersion && p.Decision.ModelVersion != selectionVersion && p.Decision.ModelVersion != neighborVersion) ||
		!digestPattern.MatchString(p.Decision.ArtifactSHA256) || !digestPattern.MatchString(p.Decision.DatasetSHA256) ||
		(p.Decision.DataKind != "measurement" && p.Decision.DataKind != "fixture") ||
		(p.Decision.Action != "KEEP" && p.Decision.Action != "UPDATE" && p.Decision.Action != "ABSTAIN") {
		return Proposal{}, invalid
	}
	return p, nil
}

// UpdateEvidence evaluates the model and load guard independent of how the
// current deployment and metric window are fetched.
func (p Proposal) UpdateEvidence() (Scores, error) {
	d, g, obs := p.Decision, p.Guard, p.Observation
	if d.Action != "UPDATE" || d.DataKind != "measurement" || !d.HoldoutPassed ||
		d.CurrentMaxNumSeqs != obs.MaxNumSeqs || d.CurrentMaxNumSeqs < 1 ||
		d.CandidateMaxNumSeqs < 1 || d.CandidateMaxNumSeqs == d.CurrentMaxNumSeqs ||
		!validScores(d.Forecast) || !validScores(d.CurrentForecast) || !validScores(d.ValidationMargin) {
		return nil, errors.New("only a validated update from measured data is eligible")
	}
	if g.P95LimitSeconds <= 0 || g.PeakVRAMLimitMiB <= 0 || g.ErrorRateLimit < 0 || g.ErrorRateLimit > 1 ||
		g.MinimumGainFraction <= 0 || g.EvaluationHorizonSeconds <= 0 || g.MaxUpdateDowntimeSeconds <= 0 ||
		g.MaxUpdateDowntimeSeconds >= g.EvaluationHorizonSeconds {
		return nil, errors.New("the update requires explicit SLO, gain and transition budget limits")
	}
	before, err := measured(obs, p.Load, g.P95LimitSeconds)
	if err != nil {
		return nil, err
	}
	conservative := Scores{}
	for _, metric := range []string{latency, vram, failures} {
		conservative[metric] = d.Forecast[metric] + d.ValidationMargin[metric]
	}
	conservative[goodput] = (d.Forecast[goodput] - d.ValidationMargin[goodput]) *
		(1 - g.MaxUpdateDowntimeSeconds/g.EvaluationHorizonSeconds)
	currentUpper := max(before[goodput], d.CurrentForecast[goodput]+d.ValidationMargin[goodput])
	if !g.withinLimits(conservative) || conservative[goodput] <= currentUpper*(1+g.MinimumGainFraction) {
		return nil, errors.New("the forecast does not meet the guard after validation error and downtime")
	}
	return before, nil
}

func validScores(s Scores) bool {
	if len(s) != 4 {
		return false
	}
	for _, key := range []string{latency, goodput, vram, failures} {
		value, ok := s[key]
		if !ok || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			return false
		}
	}
	return s[failures] <= 1
}

func (g Guard) withinLimits(s Scores) bool {
	return s[latency] <= g.P95LimitSeconds && s[vram] <= g.PeakVRAMLimitMiB && s[failures] <= g.ErrorRateLimit
}

func measured(obs observation.Record, load LoadReport, slo float64) (Scores, error) {
	invalid := errors.New("the load report and complete metric window must describe the same observation")
	duration := obs.Window.End.Sub(obs.Window.Start)
	if !obs.Metrics.Complete || obs.RuntimeID != "vllm" || obs.SpecID == "" || len(obs.Metrics.GPUs) == 0 ||
		load.ObservationID != obs.ObservationID || !load.Window.Start.Equal(obs.Window.Start) ||
		!load.Window.End.Equal(obs.Window.End) || duration < 2*time.Minute || duration > 30*time.Minute ||
		load.ID == "" || load.RuntimeImage == "" || load.PromptTokensP50 < 1 || load.MaxOutputTokens < 1 ||
		load.OfferedRPS <= 0 || load.SLOSeconds != slo || load.Attempted < 1 || load.Failed < 0 ||
		load.WithinSLO < 0 || load.Failed > load.Attempted || load.WithinSLO > load.Attempted-load.Failed {
		return nil, invalid
	}
	p95 := obs.Metrics.Serving["latencyP95"]
	if p95.Quality != "measured" || p95.Value == nil {
		return nil, invalid
	}
	out := Scores{latency: *p95.Value, goodput: float64(load.WithinSLO) / duration.Seconds(),
		vram: 0, failures: float64(load.Failed) / float64(load.Attempted)}
	for id, gpu := range obs.Metrics.GPUs {
		peak := gpu["peakUsedVRAM"]
		if id == "" || peak.Quality != "measured" || peak.Value == nil || *peak.Value < 0 {
			return nil, invalid
		}
		out[vram] = max(out[vram], *peak.Value)
	}
	if !validScores(out) {
		return nil, invalid
	}
	return out, nil
}

type Comparison struct {
	Status           string             `json:"status"`
	Before           Scores             `json:"before"`
	After            Scores             `json:"after"`
	Observation      observation.Record `json:"observation"`
	DowntimeSeconds  float64            `json:"downtimeSeconds"`
	EffectiveGoodput float64            `json:"effectiveGoodputRps"`
	Guard            Guard              `json:"guard"`
}

func (p Proposal) Compare(after observation.Record, load LoadReport, downtime float64) (Comparison, error) {
	before, err := p.UpdateEvidence()
	if err != nil {
		return Comparison{}, err
	}
	b := p.Observation
	if after.CorrelationID != b.CorrelationID || after.Revision != b.Revision+1 ||
		after.Placement.Namespace != b.Placement.Namespace || after.Placement.InfraID != b.Placement.InfraID ||
		after.Placement.NodeID != b.Placement.NodeID || after.Placement.DeploymentID == b.Placement.DeploymentID ||
		after.ApplicationDeploymentID == b.ApplicationDeploymentID || after.RuntimeID != b.RuntimeID ||
		after.ModelID != b.ModelID || after.ArtifactID != b.ArtifactID || after.SpecID != b.SpecID ||
		after.ApplicationName != b.ApplicationName || after.ApplicationVersion != b.ApplicationVersion ||
		after.MaxNumSeqs != p.Decision.CandidateMaxNumSeqs || load.Workload != p.Load.Workload ||
		!maps.EqualFunc(after.Metrics.GPUs, b.Metrics.GPUs, func(_, _ map[string]observation.Measurement) bool { return true }) ||
		downtime < 0 || math.IsNaN(downtime) || math.IsInf(downtime, 0) {
		return Comparison{}, errors.New("comparison requires the proposed revision on the same node, GPU set and workload")
	}
	current, err := measured(after, load, p.Guard.P95LimitSeconds)
	if err != nil {
		return Comparison{}, err
	}
	g := p.Guard
	effective := current[goodput] * max(0, 1-downtime/g.EvaluationHorizonSeconds)
	status := "KEEP"
	if downtime > g.MaxUpdateDowntimeSeconds || !g.withinLimits(current) || effective <= before[goodput]*(1+g.MinimumGainFraction) {
		status = "ROLLBACK_REVIEW"
	}
	return Comparison{
		Status: status, Before: before, After: current, Observation: after,
		DowntimeSeconds: downtime, EffectiveGoodput: effective, Guard: g,
	}, nil
}
