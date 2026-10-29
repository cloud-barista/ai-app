// Package appstore stores AI applications as uploaded: per name, numbered
// versions that each hold the uploaded files and the script a node runs.
//
// Everything lives on disk under one root directory and is read back at
// startup:
//
//	<root>/<name>/<version>/files/<file>    uploaded files
//	<root>/<name>/<version>/script.sh       deployment script
//	<root>/<name>/<version>/meta.json       version, file digests, creation time
//
// A version is written in a staging directory under the root and renamed into
// place, so a crash mid-upload never leaves a half written version behind.
package appstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
)

// Names inside a version directory.
const (
	// FilesDir holds the uploaded files of a version.
	FilesDir = "files"
	// ScriptFile holds the deployment script of a version.
	ScriptFile = "script.sh"
	// MetaFile holds the version number, file digests and creation time.
	MetaFile = "meta.json"
)

// DefaultMaxUploadBytes bounds one upload when the caller sets no bound.
const DefaultMaxUploadBytes int64 = 256 << 20

const (
	dirPerm    = 0o750
	filePerm   = 0o640
	scriptPerm = 0o750
	// stagingDir holds versions being written and applications being
	// deleted. Its leading "." keeps it apart from application names.
	stagingDir = ".staging"
)

// ErrNotFound is returned when no application has a name.
var ErrNotFound = errors.New("ai application not found")

// ErrVersionNotFound is returned when an application has no version of a
// number.
var ErrVersionNotFound = errors.New("ai application version not found")

// ErrTooLarge is returned when an upload exceeds the upload bound.
var ErrTooLarge = errors.New("ai application upload too large")

// File is one file of an upload.
type File struct {
	// Name is the file name, checked with model.ValidateAppFileNames.
	Name string
	// Content is read to its end once.
	Content io.Reader
}

// Store holds the registered applications, keyed by name.
type Store struct {
	mu        sync.RWMutex
	root      string
	maxUpload int64
	apps      map[string][]model.AppVersion
}

// metaBody is the layout of a version's meta file. The script is kept in its
// own file so a node receives it unchanged.
type metaBody struct {
	Version   int             `json:"version"`
	Files     []model.AppFile `json:"files"`
	CreatedAt time.Time       `json:"createdAt"`
}

// Open builds a Store on the directory root, creating it when missing, and
// reads back every version under it. maxUploadBytes bounds the script and files
// of one upload together; zero or less takes DefaultMaxUploadBytes.
//
// Versions left in staging by an interrupted upload or deletion are removed.
func Open(root string, maxUploadBytes int64) (*Store, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("ai application store directory is required")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve ai application store directory %q: %w", root, err)
	}
	if maxUploadBytes <= 0 {
		maxUploadBytes = DefaultMaxUploadBytes
	}
	s := &Store{root: abs, maxUpload: maxUploadBytes, apps: make(map[string][]model.AppVersion)}

	if err := os.MkdirAll(abs, dirPerm); err != nil {
		return nil, fmt.Errorf("failed to prepare ai application store %q: %w", abs, err)
	}
	staging := filepath.Join(abs, stagingDir)
	if err := os.RemoveAll(staging); err != nil {
		return nil, fmt.Errorf("failed to clear ai application staging %q: %w", staging, err)
	}
	if err := os.MkdirAll(staging, dirPerm); err != nil {
		return nil, fmt.Errorf("failed to prepare ai application staging %q: %w", staging, err)
	}

	entries, err := os.ReadDir(abs)
	if err != nil {
		return nil, fmt.Errorf("failed to read ai application store %q: %w", abs, err)
	}
	versionCount := 0
	for _, entry := range entries {
		if entry.Name() == stagingDir {
			continue
		}
		if !entry.IsDir() {
			return nil, fmt.Errorf("ai application store %q holds %q, which is not an application directory", abs, entry.Name())
		}
		versions, err := s.load(entry.Name())
		if err != nil {
			return nil, err
		}
		if len(versions) > 0 {
			s.apps[entry.Name()] = versions
			versionCount += len(versions)
		}
	}
	log.Info().Str("dir", abs).Int("apps", len(s.apps)).Int("versions", versionCount).
		Int64("maxUploadBytes", maxUploadBytes).Msg("Loaded AI application store")
	return s, nil
}

// load reads every version of one application directory, oldest first.
func (s *Store) load(name string) ([]model.AppVersion, error) {
	if err := model.ValidateAppName(name); err != nil {
		return nil, fmt.Errorf("ai application store %q holds an invalid application directory %q: %w", s.root, name, err)
	}
	dir := filepath.Join(s.root, name)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("failed to read ai application %q: %w", dir, err)
	}
	versions := make([]model.AppVersion, 0, len(entries))
	for _, entry := range entries {
		number, err := strconv.Atoi(entry.Name())
		if err != nil || !entry.IsDir() || number < 1 || strconv.Itoa(number) != entry.Name() {
			return nil, fmt.Errorf("ai application %q holds %q, which is not a version directory", dir, entry.Name())
		}
		v, err := readVersion(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		if v.Version != number {
			return nil, fmt.Errorf("ai application version %q records version %d", filepath.Join(dir, entry.Name()), v.Version)
		}
		versions = append(versions, *v)
	}
	sort.Slice(versions, func(i, j int) bool { return versions[i].Version < versions[j].Version })
	return versions, nil
}

func readVersion(dir string) (*model.AppVersion, error) {
	raw, err := os.ReadFile(filepath.Join(dir, MetaFile))
	if err != nil {
		return nil, fmt.Errorf("failed to read ai application version %q: %w", dir, err)
	}
	var meta metaBody
	if err := json.Unmarshal(raw, &meta); err != nil {
		return nil, fmt.Errorf("failed to decode ai application version %q: %w", dir, err)
	}
	script, err := os.ReadFile(filepath.Join(dir, ScriptFile))
	if err != nil {
		return nil, fmt.Errorf("failed to read ai application script %q: %w", dir, err)
	}
	v := model.AppVersion{Version: meta.Version, Script: string(script), Files: meta.Files, CreatedAt: meta.CreatedAt}
	if v.Files == nil {
		v.Files = []model.AppFile{}
	}
	if err := v.Validate(); err != nil {
		return nil, fmt.Errorf("ai application version %q is invalid: %w", dir, err)
	}
	for _, f := range v.Files {
		info, err := os.Stat(filepath.Join(dir, FilesDir, f.Name))
		if err != nil {
			return nil, fmt.Errorf("ai application version %q misses file %q: %w", dir, f.Name, err)
		}
		if info.Size() != f.SizeBytes {
			return nil, fmt.Errorf("ai application version %q file %q is %d bytes, meta says %d", dir, f.Name, info.Size(), f.SizeBytes)
		}
	}
	return &v, nil
}

// MaxUploadBytes reports the bound on the script and files of one upload.
func (s *Store) MaxUploadBytes() int64 {
	return s.maxUpload
}

// Register stores script and files as the next version of the application
// name: version 1 for a new name, one above its highest version otherwise.
// Files keep their order. The script and files together may not exceed
// MaxUploadBytes.
func (s *Store) Register(ctx context.Context, name, script string, files []File) (*model.AppVersion, error) {
	if err := model.ValidateAppName(name); err != nil {
		return nil, err
	}
	if err := model.ValidateAppScript(script); err != nil {
		return nil, err
	}
	names := make([]string, len(files))
	for i, f := range files {
		names[i] = f.Name
	}
	if err := model.ValidateAppFileNames(names); err != nil {
		return nil, err
	}
	remaining := s.maxUpload - int64(len(script))
	if remaining < 0 {
		return nil, s.tooLarge()
	}

	staged, err := os.MkdirTemp(filepath.Join(s.root, stagingDir), name+"-*")
	if err != nil {
		return nil, fmt.Errorf("failed to stage ai application %q: %w", name, err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(staged)
		}
	}()

	if err := os.Mkdir(filepath.Join(staged, FilesDir), dirPerm); err != nil {
		return nil, fmt.Errorf("failed to stage ai application %q: %w", name, err)
	}
	stored := make([]model.AppFile, 0, len(files))
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		written, err := writeFile(filepath.Join(staged, FilesDir, f.Name), f.Content, remaining)
		if err != nil {
			if errors.Is(err, ErrTooLarge) {
				return nil, s.tooLarge()
			}
			return nil, fmt.Errorf("failed to stage ai application %q file %q: %w", name, f.Name, err)
		}
		remaining -= written.SizeBytes
		written.Name = f.Name
		stored = append(stored, written)
	}
	if err := os.WriteFile(filepath.Join(staged, ScriptFile), []byte(script), scriptPerm); err != nil {
		return nil, fmt.Errorf("failed to stage ai application %q script: %w", name, err)
	}
	// MkdirTemp creates 0700; the version directory gets the store's mode.
	if err := os.Chmod(staged, dirPerm); err != nil {
		return nil, fmt.Errorf("failed to stage ai application %q: %w", name, err)
	}

	v := model.AppVersion{Script: script, Files: stored, CreatedAt: time.Now().UTC().Truncate(time.Second)}

	s.mu.Lock()
	defer s.mu.Unlock()
	existing := s.apps[name]
	v.Version = 1
	if len(existing) > 0 {
		v.Version = existing[len(existing)-1].Version + 1
	}
	meta, err := json.MarshalIndent(metaBody{Version: v.Version, Files: v.Files, CreatedAt: v.CreatedAt}, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to encode ai application %q meta: %w", name, err)
	}
	if err := os.WriteFile(filepath.Join(staged, MetaFile), append(meta, '\n'), filePerm); err != nil {
		return nil, fmt.Errorf("failed to stage ai application %q meta: %w", name, err)
	}
	appDir := filepath.Join(s.root, name)
	if err := os.MkdirAll(appDir, dirPerm); err != nil {
		return nil, fmt.Errorf("failed to prepare ai application %q: %w", appDir, err)
	}
	if err := os.Rename(staged, filepath.Join(appDir, strconv.Itoa(v.Version))); err != nil {
		return nil, fmt.Errorf("failed to store ai application %q version %d: %w", name, v.Version, err)
	}
	committed = true
	s.apps[name] = append(existing, v)

	log.Info().Str("app", name).Int("version", v.Version).Int("files", len(v.Files)).
		Msg("Registered AI application version")
	out := cloneVersion(v)
	return &out, nil
}

// writeFile copies content to path, refusing more than limit bytes, and
// reports its size and digest.
func writeFile(path string, content io.Reader, limit int64) (model.AppFile, error) {
	out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, filePerm)
	if err != nil {
		return model.AppFile{}, err
	}
	digest := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(out, digest), io.LimitReader(content, limit+1))
	closeErr := out.Close()
	if copyErr != nil {
		return model.AppFile{}, copyErr
	}
	if closeErr != nil {
		return model.AppFile{}, closeErr
	}
	if n > limit {
		return model.AppFile{}, ErrTooLarge
	}
	return model.AppFile{SizeBytes: n, SHA256: hex.EncodeToString(digest.Sum(nil))}, nil
}

func (s *Store) tooLarge() error {
	return fmt.Errorf("%w: the script and files together must be at most %d bytes", ErrTooLarge, s.maxUpload)
}

// Get reads one application with every version.
func (s *Store) Get(ctx context.Context, name string) (*model.App, error) {
	s.mu.RLock()
	versions, ok := s.apps[name]
	s.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	app := cloneApp(name, versions)
	return &app, nil
}

// GetVersion reads one version of an application.
func (s *Store) GetVersion(ctx context.Context, name string, version int) (*model.AppVersion, error) {
	app, err := s.Get(ctx, name)
	if err != nil {
		return nil, err
	}
	v, ok := app.Version(version)
	if !ok {
		return nil, fmt.Errorf("%w: %s version %d", ErrVersionNotFound, name, version)
	}
	return v, nil
}

// List reads every application, ordered by name.
func (s *Store) List(ctx context.Context) []model.App {
	s.mu.RLock()
	out := make([]model.App, 0, len(s.apps))
	for name, versions := range s.apps {
		out = append(out, cloneApp(name, versions))
	}
	s.mu.RUnlock()

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Delete removes an application with every version, from memory and disk.
func (s *Store) Delete(ctx context.Context, name string) error {
	s.mu.Lock()
	if _, ok := s.apps[name]; !ok {
		s.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	// The directory is moved out of the store first so that it disappears at
	// once; removing its content can then fail without leaving a partial
	// application, and Open clears what is left in staging.
	trash, err := os.MkdirTemp(filepath.Join(s.root, stagingDir), "deleted-"+name+"-*")
	if err != nil {
		s.mu.Unlock()
		return fmt.Errorf("failed to delete ai application %q: %w", name, err)
	}
	if err := os.Rename(filepath.Join(s.root, name), filepath.Join(trash, name)); err != nil {
		_ = os.Remove(trash)
		s.mu.Unlock()
		return fmt.Errorf("failed to delete ai application %q: %w", name, err)
	}
	delete(s.apps, name)
	s.mu.Unlock()

	if err := os.RemoveAll(trash); err != nil {
		log.Warn().Err(err).Str("dir", trash).Msg("Failed to remove deleted AI application files; they are removed at the next start")
	}
	log.Info().Str("app", name).Msg("Deleted AI application")
	return nil
}

// Path reports the directory of one version: FilesDir and ScriptFile inside it
// are what a node receives. The directory belongs to the store; a caller copies
// from it and changes nothing in it.
func (s *Store) Path(name string, version int) (string, error) {
	s.mu.RLock()
	versions, ok := s.apps[name]
	s.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	if !slices.ContainsFunc(versions, func(v model.AppVersion) bool { return v.Version == version }) {
		return "", fmt.Errorf("%w: %s version %d", ErrVersionNotFound, name, version)
	}
	return filepath.Join(s.root, name, strconv.Itoa(version)), nil
}

func cloneApp(name string, versions []model.AppVersion) model.App {
	app := model.App{Name: name, Versions: make([]model.AppVersion, len(versions))}
	for i, v := range versions {
		app.Versions[i] = cloneVersion(v)
	}
	return app
}

func cloneVersion(v model.AppVersion) model.AppVersion {
	v.Files = slices.Clone(v.Files)
	if v.Files == nil {
		v.Files = []model.AppFile{}
	}
	return v
}
