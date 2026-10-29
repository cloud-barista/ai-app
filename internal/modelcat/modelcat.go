// Package modelcat stores the AI model catalog: models, the artifacts each is
// published as, and the inference engines those artifacts run on.
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
// Task and Modality are properties of the model. Format, Engine and Source are
// properties of an artifact, and a model passes only when one artifact matches
// all three of them at once: a model with an HF artifact for vLLM and a GGUF
// artifact for Ollama does not pass format=gguf&engine=vllm.
type Filter struct {
	Task     string
	Modality string
	Format   string
	Engine   string
	Source   string
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
	for i := range body.Models {
		m := body.Models[i]
		if err := m.Validate(); err != nil {
			return nil, false, fmt.Errorf("model catalog %q holds an invalid model %q: %w", path, m.ID, err)
		}
		s.models[m.ID] = m
	}
	log.Info().Str("file", path).Int("count", len(s.models)).Msg("Loaded AI model catalog")
	return s, true, nil
}

// Register adds or replaces a model. Replacing keeps the ID and takes every
// other field from the new record.
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
	if f.Modality != "" && !containsFold(m.InputModalities, f.Modality) {
		return false
	}
	if f.Format == "" && f.Engine == "" && f.Source == "" {
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
	if f.Engine != "" && !slices.ContainsFunc(a.Engines, func(c model.EngineCompat) bool {
		return strings.EqualFold(c.Engine, f.Engine)
	}) {
		return false
	}
	return true
}

func containsFold(values []string, want string) bool {
	return slices.ContainsFunc(values, func(v string) bool { return strings.EqualFold(v, want) })
}

// clone copies a model with its slices, so a caller that changes what it was
// handed does not change the stored record.
func clone(m model.Model) model.Model {
	m.Tasks = slices.Clone(m.Tasks)
	m.InputModalities = slices.Clone(m.InputModalities)
	artifacts := make([]model.Artifact, len(m.Artifacts))
	for i, a := range m.Artifacts {
		a.Engines = slices.Clone(a.Engines)
		artifacts[i] = a
	}
	m.Artifacts = artifacts
	return m
}
