// Package modelcat stores the AI model catalog: models, the artifacts each is
// published as, and the inference runtimes those artifacts run on.
//
// Models are held in memory and, when a file path is given, written to one JSON
// file after every change so that a restart keeps what was registered.
package modelcat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
)

const (
	dirPerm  = 0o750
	filePerm = 0o640
)

// ErrNotFound is returned when no model matches an identifier.
var ErrNotFound = errors.New("ai model not found")

// ErrArtifactNotFound is returned when a model has no artifact of an identifier.
var ErrArtifactNotFound = errors.New("model artifact not found")

// Filter narrows a model listing. Every set field must match; empty fields
// match anything. Comparison ignores case.
//
// Task and Modality are properties of the model. Format, RuntimeID and Source
// are properties of an artifact, and a model passes only when one artifact
// matches all three of them at once: a model with an HF artifact for vLLM and a
// GGUF artifact for Ollama does not pass format=gguf&runtimeId=vllm.
//
// Origin matches the model, an absent origin as manual. Verified, when set,
// keeps the models for which some artifact has some runtime with verifiedAt
// set (true) or none has (false).
type Filter struct {
	Task      string
	Modality  string
	Format    string
	RuntimeID string
	Source    string
	Origin    string
	Verified  *bool
}

// Store holds registered models, keyed by ID.
type Store struct {
	mu     sync.RWMutex
	path   string
	models map[string]model.Model
}

// fileBody is the layout of the store file.
type fileBody struct {
	Models []model.Model `json:"models"`
}

// Open builds a Store backed by the JSON file at path and reports whether the
// file already existed. An empty path keeps the store in memory only and
// reports false.
//
// A caller seeds the store only when the file did not exist, so models an
// operator deleted do not come back on the next start.
func Open(path string) (*Store, bool, error) {
	s := &Store{path: path, models: make(map[string]model.Model)}
	if path == "" {
		return s, false, nil
	}

	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("failed to read model catalog %q: %w", path, err)
	}

	var body fileBody
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, false, fmt.Errorf("failed to decode model catalog %q: %w", path, err)
	}
	if err := refuseEngines(path, raw); err != nil {
		return nil, false, err
	}
	for i := range body.Models {
		m := body.Models[i]
		if err := m.Validate(); err != nil {
			return nil, false, fmt.Errorf("model catalog %q holds an invalid model %q: %w", path, m.ID, err)
		}
		s.models[m.ID] = clone(m)
	}
	log.Info().Str("file", path).Int("count", len(s.models)).Msg("Loaded AI model catalog")
	return s, true, nil
}

// refuseEngines fails when the store file is in the format written before
// artifacts listed runtimes, when they listed engines. An artifact may now list
// no runtime, so such a file would otherwise load with every artifact
// undeployable and nothing to say why.
func refuseEngines(path string, raw []byte) error {
	var probe struct {
		Models []struct {
			ID        string                       `json:"id"`
			Artifacts []map[string]json.RawMessage `json:"artifacts"`
		} `json:"models"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return fmt.Errorf("failed to decode model catalog %q: %w", path, err)
	}
	for _, m := range probe.Models {
		for i, a := range m.Artifacts {
			if _, ok := a["engines"]; ok {
				return fmt.Errorf("model catalog %q is in the format before runtimes: model %q artifacts[%d] lists engines; "+
					"remove the file or convert engines[].engine to runtimes[].runtimeId", path, m.ID, i)
			}
		}
	}
	return nil
}

// Register adds or replaces a model. Replacing keeps the ID and takes every
// other field from the new record. An absent origin is recorded as manual.
func (s *Store) Register(ctx context.Context, m model.Model) (*model.Model, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	m = clone(m)

	s.mu.Lock()
	previous, replaced := s.models[m.ID]
	s.models[m.ID] = m
	if err := s.save(); err != nil {
		if replaced {
			s.models[m.ID] = previous
		} else {
			delete(s.models, m.ID)
		}
		s.mu.Unlock()
		return nil, err
	}
	s.mu.Unlock()

	log.Info().Str("modelId", m.ID).Int("artifacts", len(m.Artifacts)).
		Bool("replaced", replaced).Msg("Registered AI model")

	stored := clone(m)
	return &stored, nil
}

// Get reads one model.
func (s *Store) Get(ctx context.Context, modelID string) (*model.Model, error) {
	s.mu.RLock()
	m, ok := s.models[modelID]
	s.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, modelID)
	}
	m = clone(m)
	return &m, nil
}

// Artifact reads one model and one of its artifacts.
func (s *Store) Artifact(ctx context.Context, modelID, artifactID string) (*model.Model, *model.Artifact, error) {
	m, err := s.Get(ctx, modelID)
	if err != nil {
		return nil, nil, err
	}
	artifact, ok := m.Artifact(artifactID)
	if !ok {
		return nil, nil, fmt.Errorf("%w: %s/%s", ErrArtifactNotFound, modelID, artifactID)
	}
	return m, artifact, nil
}

// List reads every model that passes filter, ordered by ID.
func (s *Store) List(ctx context.Context, filter Filter) []model.Model {
	s.mu.RLock()
	out := make([]model.Model, 0, len(s.models))
	for _, m := range s.models {
		if filter.matches(&m) {
			out = append(out, clone(m))
		}
	}
	s.mu.RUnlock()

	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Delete removes one model.
func (s *Store) Delete(ctx context.Context, modelID string) error {
	s.mu.Lock()
	previous, ok := s.models[modelID]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrNotFound, modelID)
	}
	delete(s.models, modelID)
	if err := s.save(); err != nil {
		s.models[modelID] = previous
		s.mu.Unlock()
		return err
	}
	s.mu.Unlock()

	log.Info().Str("modelId", modelID).Msg("Deleted AI model")
	return nil
}

// SyncCounts says what one ApplySync did to the catalog.
type SyncCounts struct {
	// Added is how many fetched models were new to the catalog.
	Added int `json:"added" example:"12"`
	// Updated is how many entries of the origin were replaced from the fetch.
	Updated int `json:"updated" example:"80"`
	// Staled is how many entries of the origin the fetch did not return. Each
	// is marked stale, whether or not it already was.
	Staled int `json:"staled" example:"3"`
	// Skipped is how many fetched models were left alone because the catalog
	// holds an entry of the same ID from another origin.
	Skipped int `json:"skipped" example:"1"`
	// Invalid is how many fetched models were dropped because they do not
	// validate or repeat an ID earlier in the same fetch.
	Invalid int `json:"invalid" example:"0"`
}

// syncOrigin reports whether origin is one the catalog sync collects from.
func syncOrigin(origin string) bool {
	return origin != model.OriginSeed && origin != model.OriginManual &&
		origin != "" && model.ValidOrigin(origin)
}

// ApplySync merges what one source returned into the catalog and writes the
// file once.
//
// A fetched model with a new ID is added. One whose entry has the same origin
// replaces it, keeping every runtimes[].verifiedAt recorded for the same
// artifact ID and runtime ID. One whose entry has another origin (seed,
// manual or another source) is skipped: the sync never changes what it did
// not put there. Added and replaced entries get origin, syncedAt at and stale
// false; a verifiedAt the fetch carries is dropped. Every entry of the origin the fetch did not return is marked stale
// and otherwise kept as it was. A fetched model that does not validate is
// dropped and counted; it does not fail the rest.
func (s *Store) ApplySync(ctx context.Context, origin string, models []model.Model, at time.Time) (SyncCounts, error) {
	var counts SyncCounts
	if !syncOrigin(origin) {
		return counts, fmt.Errorf("%q is not an origin the catalog sync collects from", origin)
	}
	at = at.UTC()

	s.mu.Lock()
	defer s.mu.Unlock()

	next := make(map[string]model.Model, len(s.models)+len(models))
	for id, m := range s.models {
		next[id] = m
	}
	seen := make(map[string]bool, len(models))
	for i := range models {
		m := clone(models[i])
		m.Origin = origin
		m.Stale = false
		synced := at
		m.SyncedAt = &synced
		clearVerified(&m)
		if err := m.Validate(); err != nil {
			counts.Invalid++
			log.Warn().Err(err).Str("origin", origin).Str("modelId", m.ID).Msg("Dropped an invalid model from the catalog sync")
			continue
		}
		if seen[m.ID] {
			counts.Invalid++
			log.Warn().Str("origin", origin).Str("modelId", m.ID).Msg("Dropped a model the catalog sync fetched twice")
			continue
		}
		seen[m.ID] = true

		previous, exists := next[m.ID]
		switch {
		case !exists:
			counts.Added++
		case previous.Origin != origin:
			counts.Skipped++
			continue
		default:
			keepVerified(&m, &previous)
			counts.Updated++
		}
		next[m.ID] = m
	}
	for id, m := range next {
		if m.Origin != origin || seen[id] {
			continue
		}
		m.Stale = true
		next[id] = m
		counts.Staled++
	}

	previous := s.models
	s.models = next
	if err := s.save(); err != nil {
		s.models = previous
		return SyncCounts{}, err
	}

	log.Info().Str("origin", origin).Int("added", counts.Added).Int("updated", counts.Updated).
		Int("staled", counts.Staled).Int("skipped", counts.Skipped).Int("invalid", counts.Invalid).
		Msg("Applied catalog sync")
	return counts, nil
}

// clearVerified drops every verifiedAt of m. Only a deployment on a node
// records one; a source cannot know it.
func clearVerified(m *model.Model) {
	for i := range m.Artifacts {
		for j := range m.Artifacts[i].Runtimes {
			m.Artifacts[i].Runtimes[j].VerifiedAt = nil
		}
	}
}

// keepVerified copies onto m every verifiedAt previous records for the same
// artifact ID and runtime ID: a source does not know which pairings were
// deployed, so a fetch must not erase that.
func keepVerified(m, previous *model.Model) {
	for i := range m.Artifacts {
		old, ok := previous.Artifact(m.Artifacts[i].ID)
		if !ok {
			continue
		}
		for j := range m.Artifacts[i].Runtimes {
			compat := &m.Artifacts[i].Runtimes[j]
			for _, was := range old.Runtimes {
				if was.RuntimeID == compat.RuntimeID && was.VerifiedAt != nil {
					compat.VerifiedAt = cloneTime(was.VerifiedAt)
					break
				}
			}
		}
	}
}

// save writes every model to the store file. The caller holds the write lock.
//
// The file is written next to its final path and renamed over it, so a crash
// mid-write leaves the previous catalog rather than a truncated one.
func (s *Store) save() error {
	if s.path == "" {
		return nil
	}

	body := fileBody{Models: make([]model.Model, 0, len(s.models))}
	for _, m := range s.models {
		body.Models = append(body.Models, m)
	}
	sort.Slice(body.Models, func(i, j int) bool { return body.Models[i].ID < body.Models[j].ID })
	encoded, err := json.MarshalIndent(body, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to encode model catalog: %w", err)
	}

	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return fmt.Errorf("failed to prepare model catalog directory %q: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(s.path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("failed to write model catalog: %w", err)
	}
	tmpName := tmp.Name()
	_, writeErr := tmp.Write(append(encoded, '\n'))
	closeErr := tmp.Close()
	if writeErr == nil {
		writeErr = closeErr
	}
	if writeErr == nil {
		writeErr = os.Chmod(tmpName, filePerm)
	}
	if writeErr == nil {
		writeErr = os.Rename(tmpName, s.path)
	}
	if writeErr != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("failed to write model catalog %q: %w", s.path, writeErr)
	}
	return nil
}

func (f Filter) matches(m *model.Model) bool {
	if f.Task != "" && !containsFold(m.Tasks, f.Task) {
		return false
	}
	if f.Origin != "" && !strings.EqualFold(m.Origin, f.Origin) {
		return false
	}
	if f.Verified != nil && m.Verified() != *f.Verified {
		return false
	}
	if f.Modality != "" && !containsFold(m.InputModalities, f.Modality) {
		return false
	}
	if f.Format == "" && f.RuntimeID == "" && f.Source == "" {
		return true
	}
	for i := range m.Artifacts {
		if f.matchesArtifact(&m.Artifacts[i]) {
			return true
		}
	}
	return false
}

func (f Filter) matchesArtifact(a *model.Artifact) bool {
	if f.Format != "" && !strings.EqualFold(a.Format, f.Format) {
		return false
	}
	if f.Source != "" && !strings.EqualFold(a.Source, f.Source) {
		return false
	}
	if f.RuntimeID != "" && !slices.ContainsFunc(a.Runtimes, func(c model.RuntimeCompat) bool {
		return strings.EqualFold(c.RuntimeID, f.RuntimeID)
	}) {
		return false
	}
	return true
}

func containsFold(values []string, want string) bool {
	return slices.ContainsFunc(values, func(v string) bool { return strings.EqualFold(v, want) })
}

// clone copies a model with its slices and times, so a caller that changes
// what it was handed does not change the stored record.
//
// It also fills in what an older or hand-written record leaves out: an absent
// origin becomes manual, and an artifact without runtimes holds an empty list
// so that it is written as [] rather than null.
func clone(m model.Model) model.Model {
	if m.Origin == "" {
		m.Origin = model.OriginManual
	}
	m.Tasks = slices.Clone(m.Tasks)
	m.InputModalities = slices.Clone(m.InputModalities)
	m.SyncedAt = cloneTime(m.SyncedAt)
	artifacts := make([]model.Artifact, len(m.Artifacts))
	for i, a := range m.Artifacts {
		a.Runtimes = slices.Clone(a.Runtimes)
		if a.Runtimes == nil {
			a.Runtimes = []model.RuntimeCompat{}
		}
		for j := range a.Runtimes {
			a.Runtimes[j].Args = slices.Clone(a.Runtimes[j].Args)
			a.Runtimes[j].VerifiedAt = cloneTime(a.Runtimes[j].VerifiedAt)
		}
		artifacts[i] = a
	}
	m.Artifacts = artifacts
	return m
}

func cloneTime(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	copied := *t
	return &copied
}
