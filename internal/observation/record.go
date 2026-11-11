// Package observation joins a deployment identity to a read-only metric window.
package observation

import (
	"errors"
	"time"
)

type Window struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

func (w Window) Validate(now time.Time) error {
	duration := w.End.Sub(w.Start)
	if w.End.After(now) || duration < 2*time.Minute || duration > 30*time.Minute ||
		w.Start.Nanosecond() != 0 || w.End.Nanosecond() != 0 {
		return errors.New("supply a completed 2 to 30 minute window with second precision")
	}
	return nil
}

type Placement struct {
	Namespace    string `json:"nsId"`
	InfraID      string `json:"infraId"`
	NodeID       string `json:"nodeId"`
	DeploymentID string `json:"deploymentId"`
}

type Record struct {
	ObservationID           string    `json:"observationId"`
	CorrelationID           string    `json:"correlationId"`
	Revision                int       `json:"revision"`
	Placement               Placement `json:"placement"`
	InferenceRunID          string    `json:"inferenceRunId"`
	ApplicationDeploymentID string    `json:"applicationDeploymentId"`
	ApplicationRunID        string    `json:"applicationRunId"`
	ApplicationName         string    `json:"applicationName"`
	ApplicationVersion      int       `json:"applicationVersion"`
	RuntimeID               string    `json:"runtimeId"`
	ModelID                 string    `json:"modelId"`
	ArtifactID              string    `json:"artifactId"`
	SpecID                  string    `json:"specId,omitempty"`
	MaxNumSeqs              int       `json:"maxNumSeqs,omitempty"`
	AppliedAt               time.Time `json:"appliedAt"`
	Window                  Window    `json:"window"`
	CollectedAt             time.Time `json:"collectedAt"`
	Metrics                 Metrics   `json:"metrics"`
}

type Measurement struct {
	Value       *float64   `json:"value,omitempty"`
	Unit        string     `json:"unit"`
	Source      string     `json:"source"`
	Quality     string     `json:"quality"`
	FirstSample *time.Time `json:"firstSample,omitempty"`
	LastSample  *time.Time `json:"lastSample,omitempty"`
	Samples     int        `json:"samples,omitempty"`
}

type Metrics struct {
	Serving  map[string]Measurement            `json:"serving"`
	GPUs     map[string]map[string]Measurement `json:"gpus"`
	Issues   []string                          `json:"issues"`
	Complete bool                              `json:"complete"`
}
