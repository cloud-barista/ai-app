package appstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
)

func files(pairs ...string) []File {
	out := make([]File, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, File{Name: pairs[i], Content: strings.NewReader(pairs[i+1])})
	}
	return out
}

func TestRegisterVersionsAndReload(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	s, err := Open(root, 0)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if s.MaxUploadBytes() != DefaultMaxUploadBytes {
		t.Errorf("max upload = %d, want default", s.MaxUploadBytes())
	}

	v1, err := s.Register(ctx, "open-webui", "docker compose up -d\n", files("docker-compose.yml", "services: {}\n"))
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if v1.Version != 1 || len(v1.Files) != 1 || v1.Files[0].SizeBytes != 13 {
		t.Fatalf("first version = %+v", v1)
	}
	if sum := sha256.Sum256([]byte("services: {}\n")); v1.Files[0].SHA256 != hex.EncodeToString(sum[:]) {
		t.Errorf("sha256 = %q", v1.Files[0].SHA256)
	}
	v2, err := s.Register(ctx, "open-webui", "echo two\n", nil)
	if err != nil {
		t.Fatalf("Register again: %v", err)
	}
	if v2.Version != 2 || len(v2.Files) != 0 {
		t.Fatalf("second version = %+v", v2)
	}

	dir, err := s.Path("open-webui", 1)
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	if raw, err := os.ReadFile(filepath.Join(dir, FilesDir, "docker-compose.yml")); err != nil || string(raw) != "services: {}\n" {
		t.Errorf("stored file = %q, %v", raw, err)
	}
	if raw, err := os.ReadFile(filepath.Join(dir, ScriptFile)); err != nil || string(raw) != "docker compose up -d\n" {
		t.Errorf("stored script = %q, %v", raw, err)
	}
	if _, err := s.Path("open-webui", 3); !errors.Is(err, ErrVersionNotFound) {
		t.Errorf("Path of a missing version = %v", err)
	}

	reopened, err := Open(root, 1024)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	app, err := reopened.Get(ctx, "open-webui")
	if err != nil {
		t.Fatalf("Get after reopen: %v", err)
	}
	if len(app.Versions) != 2 || app.Versions[0].Version != 1 || app.Versions[1].Script != "echo two\n" {
		t.Errorf("reloaded app = %+v", app)
	}
	if app.Versions[0].Files[0] != v1.Files[0] {
		t.Errorf("reloaded file = %+v, want %+v", app.Versions[0].Files[0], v1.Files[0])
	}
	got, err := reopened.GetVersion(ctx, "open-webui", 1)
	if err != nil || got.Script != "docker compose up -d\n" {
		t.Errorf("GetVersion = %+v, %v", got, err)
	}
	if _, err := reopened.GetVersion(ctx, "open-webui", 9); !errors.Is(err, ErrVersionNotFound) {
		t.Errorf("GetVersion of a missing version = %v", err)
	}
	if list := reopened.List(ctx); len(list) != 1 || list[0].Name != "open-webui" {
		t.Errorf("List = %+v", list)
	}
}

func TestDeleteRemovesEveryVersion(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	s, err := Open(root, 0)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for range 2 {
		if _, err := s.Register(ctx, "langflow", "true\n", files("a.txt", "a")); err != nil {
			t.Fatalf("Register: %v", err)
		}
	}
	if err := s.Delete(ctx, "langflow"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "langflow")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("application directory still there: %v", err)
	}
	if _, err := s.Get(ctx, "langflow"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get after Delete = %v", err)
	}
	if err := s.Delete(ctx, "langflow"); !errors.Is(err, ErrNotFound) {
		t.Errorf("second Delete = %v", err)
	}
	staged, err := os.ReadDir(filepath.Join(root, stagingDir))
	if err != nil || len(staged) != 0 {
		t.Errorf("staging after delete = %v, %v", staged, err)
	}

	v, err := s.Register(ctx, "langflow", "true\n", nil)
	if err != nil || v.Version != 1 {
		t.Errorf("Register after Delete = %+v, %v; want version 1", v, err)
	}
}

func TestRegisterRefusesBadInput(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	s, err := Open(root, 16)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for _, tc := range []struct {
		name, app, script string
		files             []File
	}{
		{"empty name", "", "true", nil},
		{"upper case name", "Dify", "true", nil},
		{"dot in name", "dify.v2", "true", nil},
		{"no script", "dify", "", nil},
		{"script too long", "dify", strings.Repeat("x", model.MaxAppScriptBytes+1), nil},
		{"parent path", "dify", "true", files("../x", "x")},
		{"nested path", "dify", "true", files("conf/x", "x")},
		{"hidden file", "dify", "true", files(".env", "x")},
		{"dot dot", "dify", "true", files("..", "x")},
		{"space", "dify", "true", files("a b", "x")},
		{"duplicate", "dify", "true", files("a", "x", "a", "y")},
	} {
		if _, err := s.Register(ctx, tc.app, tc.script, tc.files); !errors.Is(err, model.ErrValidation) {
			t.Errorf("%s: err = %v, want a validation error", tc.name, err)
		}
	}

	for _, tc := range []struct {
		name, script string
		files        []File
	}{
		{"one file", "true", files("big", strings.Repeat("x", 13))},
		{"files together", "true", files("a", "12345678", "b", "123456")},
	} {
		if _, err := s.Register(ctx, "dify", tc.script, tc.files); !errors.Is(err, ErrTooLarge) {
			t.Errorf("%s: err = %v, want ErrTooLarge", tc.name, err)
		}
	}
	if _, err := s.Register(ctx, "dify", "true", files("a", "123456789012")); err != nil {
		t.Errorf("upload at the bound: %v", err)
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Errorf("store holds %d entries, want staging and dify only", len(entries))
	}
	staged, err := os.ReadDir(filepath.Join(root, stagingDir))
	if err != nil || len(staged) != 0 {
		t.Errorf("refused uploads left staging behind: %v, %v", staged, err)
	}
}

func TestOpenRefusesForeignEntries(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "stray.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(root, 0); err == nil {
		t.Error("Open accepted a file at the store root")
	}
	if _, err := Open("", 0); err == nil {
		t.Error("Open accepted an empty directory")
	}
}

func TestOpenClearsStaging(t *testing.T) {
	root := t.TempDir()
	leftover := filepath.Join(root, stagingDir, "dify-123", FilesDir)
	if err := os.MkdirAll(leftover, 0o750); err != nil {
		t.Fatal(err)
	}
	s, err := Open(root, 0)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if list := s.List(context.Background()); len(list) != 0 {
		t.Errorf("staged upload was loaded: %+v", list)
	}
	if _, err := os.Stat(leftover); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("staging left over: %v", err)
	}
}
