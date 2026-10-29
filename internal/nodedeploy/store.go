package nodedeploy

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
)

const (
	dirPerm  = 0o750
	filePerm = 0o640
)

// Store holds deployment records in memory and, when a file path is given,
// writes them all to one JSON file after every change, the way the model
// catalog is kept.
type Store struct {
	mu      sync.RWMutex
	path    string
	records map[string]*model.NodeDeployment
}

// storeFile is the layout of the store file.
type storeFile struct {
	Deployments []model.NodeDeployment `json:"deployments"`
}

// OpenStore builds a Store backed by the JSON file at path. An empty path keeps
// records in memory only.
//
// A record left pending or running by a previous process is marked
// interrupted: nothing follows it any more, and what its nodes ended up with is
// unknown.
func OpenStore(path string) (*Store, error) {
	s := &Store{path: path, records: make(map[string]*model.NodeDeployment)}
	if path == "" {
		return s, nil
	}

	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read deployment records %q: %w", path, err)
	}
	var body storeFile
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, fmt.Errorf("failed to decode deployment records %q: %w", path, err)
	}

	interrupted := 0
	now := time.Now().UTC()
	for i := range body.Deployments {
		d := body.Deployments[i]
		if d.ID == "" {
			return nil, fmt.Errorf("deployment records %q hold a record without an id", path)
		}
		if markInterrupted(&d, now) {
			interrupted++
		}
		s.records[d.ID] = &d
	}
	if interrupted > 0 {
		if err := s.save(); err != nil {
			return nil, err
		}
		log.Warn().Int("count", interrupted).Msg("Marked deployments left running by a previous start as interrupted")
	}
	log.Info().Str("file", path).Int("count", len(s.records)).Msg("Loaded deployment records")
	return s, nil
}

// markInterrupted marks an unfinished record and its unfinished nodes
// interrupted, and reports whether it changed anything.
func markInterrupted(d *model.NodeDeployment, now time.Time) bool {
	if d.Status != model.NodeDeployPending && d.Status != model.NodeDeployRunning {
		return false
	}
	d.Status = model.NodeDeployInterrupted
	if d.Error == "" {
		d.Error = "the service stopped while the deployment ran; the state of its nodes is unknown"
	}
	d.FinishedAt = &now
	for i := range d.Nodes {
		n := &d.Nodes[i]
		if n.Status == model.NodeDeployPending || n.Status == model.NodeDeployRunning {
			n.Status = model.NodeDeployInterrupted
			if n.FinishedAt == nil {
				n.FinishedAt = &now
			}
		}
	}
	return true
}

// Create adds a new record.
func (s *Store) Create(d model.NodeDeployment) error {
	copied, err := clone(&d)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.records[d.ID]; ok {
		return fmt.Errorf("deployment %s already exists", d.ID)
	}
	s.records[d.ID] = copied
	if err := s.save(); err != nil {
		delete(s.records, d.ID)
		return err
	}
	return nil
}

// Update changes one record with change and writes the store. The record is
// kept in memory even when the file cannot be written, since the deployment
// it follows goes on; the write error is returned for the caller to log.
func (s *Store) Update(id string, change func(*model.NodeDeployment)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.records[id]
	if !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	change(d)
	return s.save()
}

// Get reads a copy of one record.
func (s *Store) Get(id string) (*model.NodeDeployment, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	d, ok := s.records[id]
	if !ok {
		return nil, false
	}
	copied, err := clone(d)
	if err != nil {
		return nil, false
	}
	return copied, true
}

// List reads a copy of every record, newest first.
func (s *Store) List() []model.NodeDeployment {
	s.mu.RLock()
	out := make([]model.NodeDeployment, 0, len(s.records))
	for _, d := range s.records {
		if copied, err := clone(d); err == nil {
			out = append(out, *copied)
		}
	}
	s.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// save writes every record to the store file. The caller holds the write
// lock. The file is written beside its final path and renamed over it, so a
// crash mid-write leaves the previous records.
func (s *Store) save() error {
	if s.path == "" {
		return nil
	}
	body := storeFile{Deployments: make([]model.NodeDeployment, 0, len(s.records))}
	for _, d := range s.records {
		body.Deployments = append(body.Deployments, *d)
	}
	sort.Slice(body.Deployments, func(i, j int) bool {
		return body.Deployments[i].CreatedAt.Before(body.Deployments[j].CreatedAt)
	})
	encoded, err := json.MarshalIndent(body, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to encode deployment records: %w", err)
	}

	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return fmt.Errorf("failed to prepare deployment record directory %q: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(s.path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("failed to write deployment records: %w", err)
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
		return fmt.Errorf("failed to write deployment records %q: %w", s.path, writeErr)
	}
	return nil
}

// clone deep copies a record through JSON, the form it is stored in.
func clone(d *model.NodeDeployment) (*model.NodeDeployment, error) {
	raw, err := json.Marshal(d)
	if err != nil {
		return nil, fmt.Errorf("failed to copy deployment record: %w", err)
	}
	out := &model.NodeDeployment{}
	if err := json.Unmarshal(raw, out); err != nil {
		return nil, fmt.Errorf("failed to copy deployment record: %w", err)
	}
	return out, nil
}
