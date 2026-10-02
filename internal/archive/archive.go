// Package archive persists what the agent decided and what it called.
//
// The first-year deliverable names this "AI application deploy/control
// information archiving", and the second-year deliverable feeds the same records
// back into planning. That second use is why a record keeps the intermediate
// tool calls and the guard verdict, not only the final outcome.
package archive

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/rs/zerolog/log"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
)

const (
	fileName    = "runs.jsonl"
	dirPerm     = 0o750
	filePerm    = 0o640
	maxScanLine = 4 << 20
)

// Store appends run records to a JSON Lines file.
//
// JSON Lines keeps the archive append-only and readable with standard tools,
// which suits a prototype whose records are meant to be inspected by hand.
type Store struct {
	mu   sync.Mutex
	path string
}

// NewStore prepares the archive directory and returns a Store.
func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return nil, fmt.Errorf("failed to prepare archive directory %q: %w", dir, err)
	}
	return &Store{path: filepath.Join(dir, fileName)}, nil
}

// Append writes one run record.
func (s *Store) Append(ctx context.Context, record model.ArchiveRecord) error {
	encoded, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("failed to encode archive record: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	file, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, filePerm)
	if err != nil {
		return fmt.Errorf("failed to open archive: %w", err)
	}
	defer func() { _ = file.Close() }()

	if _, err := file.Write(append(encoded, '\n')); err != nil {
		return fmt.Errorf("failed to append archive record: %w", err)
	}

	log.Info().Str("runId", record.RunID).Str("kind", record.Kind).
		Int("steps", len(record.Steps)).Msg("Archived run")
	return nil
}

// List reads the most recent records, newest first.
//
// A prototype archive stays small enough to scan; replacing this with a query
// against the large scale repository is second-year work.
func (s *Store) List(ctx context.Context, limit int) ([]model.ArchiveRecord, error) {
	records, err := s.readAll()
	if err != nil {
		return nil, err
	}

	// Reverse in place so the newest record comes first.
	for i, j := 0, len(records)-1; i < j; i, j = i+1, j-1 {
		records[i], records[j] = records[j], records[i]
	}
	if limit > 0 && len(records) > limit {
		records = records[:limit]
	}
	return records, nil
}

// Get reads one record by run ID.
func (s *Store) Get(ctx context.Context, runID string) (*model.ArchiveRecord, error) {
	records, err := s.readAll()
	if err != nil {
		return nil, err
	}
	for i := len(records) - 1; i >= 0; i-- {
		if records[i].RunID == runID {
			found := records[i]
			return &found, nil
		}
	}
	return nil, fmt.Errorf("archive record %q not found", runID)
}

func (s *Store) readAll() ([]model.ArchiveRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	file, err := os.Open(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			// Nothing archived yet is a normal state, not a failure.
			return []model.ArchiveRecord{}, nil
		}
		return nil, fmt.Errorf("failed to open archive: %w", err)
	}
	defer func() { _ = file.Close() }()

	records := []model.ArchiveRecord{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64<<10), maxScanLine)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var record model.ArchiveRecord
		if err := json.Unmarshal(line, &record); err != nil {
			return nil, fmt.Errorf("failed to decode archive record: %w", err)
		}
		records = append(records, record)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to read archive: %w", err)
	}
	return records, nil
}
