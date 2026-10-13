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

// Log appends records of one type to a JSON Lines file.
//
// JSON Lines keeps the archive append-only and readable with standard tools,
// which suits a prototype whose records are meant to be inspected by hand.
type Log[T any] struct {
	mu   sync.Mutex
	path string
}

// NewLog prepares the archive directory and returns a Log writing runs.jsonl
// in it.
func NewLog[T any](dir string) (*Log[T], error) {
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return nil, fmt.Errorf("failed to prepare archive directory %q: %w", dir, err)
	}
	return &Log[T]{path: filepath.Join(dir, fileName)}, nil
}

// Append writes one record.
func (l *Log[T]) Append(record T) error {
	encoded, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("failed to encode archive record: %w", err)
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	file, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, filePerm)
	if err != nil {
		return fmt.Errorf("failed to open archive: %w", err)
	}
	defer func() { _ = file.Close() }()

	if _, err := file.Write(append(encoded, '\n')); err != nil {
		return fmt.Errorf("failed to append archive record: %w", err)
	}
	return nil
}

// List reads the most recent records, newest first. A limit of zero or less
// returns every record.
//
// A prototype archive stays small enough to scan; replacing this with a query
// against the large scale repository is second-year work.
func (l *Log[T]) List(limit int) ([]T, error) {
	records, err := l.readAll()
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

// Find returns the newest record match accepts, and false when none does.
func (l *Log[T]) Find(match func(*T) bool) (*T, bool, error) {
	records, err := l.readAll()
	if err != nil {
		return nil, false, err
	}
	for i := len(records) - 1; i >= 0; i-- {
		if match(&records[i]) {
			found := records[i]
			return &found, true, nil
		}
	}
	return nil, false, nil
}

func (l *Log[T]) readAll() ([]T, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	file, err := os.Open(l.path)
	if err != nil {
		if os.IsNotExist(err) {
			// Nothing archived yet is a normal state, not a failure.
			return []T{}, nil
		}
		return nil, fmt.Errorf("failed to open archive: %w", err)
	}
	defer func() { _ = file.Close() }()

	records := []T{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64<<10), maxScanLine)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var record T
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

// Store appends agent run and direct call records.
type Store struct {
	log *Log[model.ArchiveRecord]
}

// NewStore prepares the archive directory and returns a Store.
func NewStore(dir string) (*Store, error) {
	runs, err := NewLog[model.ArchiveRecord](dir)
	if err != nil {
		return nil, err
	}
	return &Store{log: runs}, nil
}

// Append writes one run record.
func (s *Store) Append(ctx context.Context, record model.ArchiveRecord) error {
	if err := s.log.Append(record); err != nil {
		return err
	}
	log.Info().Str("runId", record.RunID).Str("kind", record.Kind).
		Int("steps", len(record.Steps)).Msg("Archived run")
	return nil
}

// List reads the most recent records, newest first.
func (s *Store) List(ctx context.Context, limit int) ([]model.ArchiveRecord, error) {
	return s.log.List(limit)
}

// Get reads one record by run ID.
func (s *Store) Get(ctx context.Context, runID string) (*model.ArchiveRecord, error) {
	record, found, err := s.log.Find(func(r *model.ArchiveRecord) bool { return r.RunID == runID })
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("archive record %q not found", runID)
	}
	return record, nil
}
