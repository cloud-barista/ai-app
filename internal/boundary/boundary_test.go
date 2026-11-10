// Package boundary checks that the Innogrid prototype does not depend on the
// mock implementations of partner institution areas.
package boundary

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// module is the module path every package here is imported under.
const module = "github.com/ish-hcc/ai-agent-proto"

// mockImport is what an import of a mock package contains.
const mockImport = module + "/mock/"

// TestInnogridCodeImportsNoMock walks every Go file, tests included, of
// internal/ and cmd/ and fails on any import of a package under mock/. The
// mocks may import Innogrid packages; the other way round would make a partner
// mock part of the prototype.
func TestInnogridCodeImportsNoMock(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("repository root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("no go.mod at %s: %v", root, err)
	}

	files := 0
	fset := token.NewFileSet()
	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() && d.Name() == "testdata" {
				return filepath.SkipDir
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
			if err != nil {
				return err
			}
			files++
			rel, _ := filepath.Rel(root, path)
			for _, spec := range file.Imports {
				imported, err := strconv.Unquote(spec.Path.Value)
				if err != nil {
					return err
				}
				if strings.HasPrefix(imported, mockImport) {
					t.Errorf("%s imports %s", rel, imported)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}
	}
	if files == 0 {
		t.Fatal("no Go file was checked")
	}
	t.Logf("checked the imports of %d Go files under internal/ and cmd/", files)
}
