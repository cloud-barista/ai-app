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

// storeVersion is the layout version of the store file that save writes.
//
// Version 2 names a deployment whose playbooks run deploying and one whose
// every node was deployed running. A file without a version is version 1,
// which named them running and succeeded; OpenStore renames them on load.
const storeVersion = 2

// storeFile is the layout of the store file.
type storeFile struct {
	Version     int                    `json:"version"`
	Deployments []model.NodeDeployment `json:"deployments"`
}

// v1Statuses maps the deployment and node statuses of a version 1 store file
// to the ones of the current version.
var v1Statuses = map[string]string{
	"running":   model.NodeDeployDeploying,
	"succeeded": model.NodeDeployRunning,
}

// OpenStore builds a Store backed by the JSON file at path. An empty path keeps
// records in memory only.
//
// A file of an older version is migrated to the current one and written back
// in it; a file of a newer version is refused.
//
// A record left pending or deploying by a previous process is marked
// interrupted: nothing follows it any more, and what its nodes ended up with is
// unknown. A record left removing is marked remove-failed, so that it can be
// taken down again.
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
	version := body.Version
	if version == 0 {
		version = 1
	}
	if version > storeVersion {
		return nil, fmt.Errorf("deployment records %q are of version %d; this service reads up to version %d",
			path, version, storeVersion)
	}

	interrupted := 0
	now := time.Now().UTC()
	for i := range body.Deployments {
		d := body.Deployments[i]
		if d.ID == "" {
			return nil, fmt.Errorf("deployment records %q hold a record without an id", path)
		}
		if version == 1 {
			migrateV1(&d)
		}
		if markInterrupted(&d, now) {
			interrupted++
		}
		s.records[d.ID] = &d
	}
	if interrupted > 0 || version < storeVersion {
		if err := s.save(); err != nil {
			return nil, err
		}
	}
	if version < storeVersion {
		log.Info().Str("file", path).Int("from", version).Int("to", storeVersion).Msg("Migrated deployment records")
	}
	if interrupted > 0 {
		log.Warn().Int("count", interrupted).Msg("Marked deployments left deploying by a previous start as interrupted")
	}
	log.Info().Str("file", path).Int("count", len(s.records)).Msg("Loaded deployment records")
	return s, nil
}

// migrateV1 renames the statuses of a record read from a version 1 store file.
func migrateV1(d *model.NodeDeployment) {
	if status, ok := v1Statuses[d.Status]; ok {
		d.Status = status
	}
	for i := range d.Nodes {
		if status, ok := v1Statuses[d.Nodes[i].Status]; ok {
			d.Nodes[i].Status = status
		}
	}
}

// markInterrupted marks an unfinished record and its unfinished nodes
// interrupted, and reports whether it changed anything.
func markInterrupted(d *model.NodeDeployment, now time.Time) bool {
	if d.Status == model.NodeDeployRemoving {
		d.Status = model.NodeDeployRemoveFailed
		d.RemovedAt = &now
		for i := range d.Nodes {
			n := &d.Nodes[i]
			if n.Status == model.NodeDeployRemoving {
				n.Status = model.NodeDeployRemoveFailed
				n.Error = "the service stopped while the node was taken down; the state of its containers is unknown"
			}
		}
		return true
	}
	if d.Status != model.NodeDeployPending && d.Status != model.NodeDeployDeploying {
		return false
	}
	d.Status = model.NodeDeployInterrupted
	if d.Error == "" {
		d.Error = "the service stopped while the deployment ran; the state of its nodes is unknown"
	}
	d.FinishedAt = &now
	for i := range d.Nodes {
		n := &d.Nodes[i]
		if n.Status == model.NodeDeployPending || n.Status == model.NodeDeployDeploying {
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
	if submission := d.Request.Submission; submission != nil && !d.DryRun {
		previous := s.latestPlacement(&d.Request)
		var previousID string
		if previous != nil && previous.Status != model.NodeDeployRemoved {
			switch previous.Status {
			case model.NodeDeployPending, model.NodeDeployDeploying, model.NodeDeployRemoving:
				return &model.ValidationError{Message: "the placement is still changing"}
			}
			previousID = previous.ID
		}
		if submission.PreviousDeploymentID != previousID {
			return &model.ValidationError{Message: "the approved deployment does not match the current placement revision"}
		}
	}
	s.records[d.ID] = copied
	if err := s.save(); err != nil {
		delete(s.records, d.ID)
		return err
	}
	return nil
}

// latestPlacement returns the last accepted live request for the work key.
// The caller holds the store lock.
func (s *Store) latestPlacement(req *model.NodeDeploymentReq) *model.NodeDeployment {
	place, err := placementFor(req)
	if err != nil {
		return nil
	}
	target := req.Targets[0].Key()
	var latest *model.NodeDeployment
	for _, other := range s.records {
		if other.DryRun || other.Type != req.Type {
			continue
		}
		if (other.Request.Type == model.NodeDeployTypeInference && other.Request.Inference == nil) ||
			(other.Request.Type == model.NodeDeployTypeApp && other.Request.App == nil) {
			continue
		}
		otherPlace, err := placementFor(&other.Request)
		if err != nil || otherPlace.WorkKey != place.WorkKey {
			continue
		}
		for _, node := range other.Nodes {
			if node.Target.Key() == target && (latest == nil || other.CreatedAt.After(latest.CreatedAt) ||
				(other.CreatedAt.Equal(latest.CreatedAt) && other.ID > latest.ID)) {
				latest = other
			}
		}
	}
	return latest
}

func (s *Store) currentSubmission(req *model.NodeDeploymentReq) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	latest := s.latestPlacement(req)
	return latest != nil && latest.ID == req.Submission.ID
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
	body := storeFile{Version: storeVersion, Deployments: make([]model.NodeDeployment, 0, len(s.records))}
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
