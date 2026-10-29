package nodedeploy

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/ish-hcc/ai-agent-proto/internal/appstore"
)

// Names of the files this service writes into a stage folder. Those it adds
// next to the files of an application start with "." so that no uploaded
// file, whose name cannot, takes their place.
const (
	// EnvFile holds the env of the request, read by docker compose and
	// exported before an application script runs.
	EnvFile = ".env"
	// ScriptFile is the application script, run by copy-and-run.yml.
	ScriptFile = ".aiapp-run.sh"
	// ComposeFile is the compose file of a model service.
	ComposeFile = "docker-compose.yml"
	// ManifestFile lists the other files of the stage folder, one name per
	// line. It is copied to the node with them, and the next deployment of
	// the same work key removes the files the previous manifest lists and the
	// new one does not.
	ManifestFile = ".aiapp-files"
)

// Stage files are read by Semaphore, which runs as another user (uid 1001 in
// its image), so they are world readable. They hold no key.
const (
	stageDirPerm    = 0o755
	stageFilePerm   = 0o644
	stageScriptPerm = 0o755
)

// cspPattern is what a cloud name read from CB-Tumblebug may look like. It
// becomes part of a folder name, so anything else is refused rather than
// cleaned.
var cspPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// stageDir is <root>/<deployment id>/<csp>-<node id>. It refuses a cloud name
// that is not a plain name, and any result that is not a folder two levels
// below root.
func stageDir(root, deploymentID, csp, nodeID string) (string, error) {
	if !cspPattern.MatchString(csp) {
		return "", fmt.Errorf("node %q reports cloud %q, which is not a plain name ([a-z0-9][a-z0-9-]*)", nodeID, csp)
	}
	dir := filepath.Join(root, deploymentID, csp+"-"+nodeID)
	rel, err := filepath.Rel(filepath.Clean(root), dir)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) ||
		len(strings.Split(rel, string(filepath.Separator))) != 2 {
		return "", fmt.Errorf("stage folder %q is not a folder of a deployment under %q", dir, root)
	}
	return dir, nil
}

// makeStageDir creates dir and its deployment folder readable by Semaphore.
func makeStageDir(root, dir string) error {
	if err := os.MkdirAll(dir, stageDirPerm); err != nil {
		return fmt.Errorf("failed to create stage folder %q: %w", dir, err)
	}
	// MkdirAll applies the umask; the folders between root and dir are made
	// readable explicitly.
	for p := dir; p != root && strings.HasPrefix(p, root); p = filepath.Dir(p) {
		if err := os.Chmod(p, stageDirPerm); err != nil {
			return fmt.Errorf("failed to open stage folder %q to Semaphore: %w", p, err)
		}
	}
	return nil
}

// writeStageFile writes one file with perm, whatever the umask.
func writeStageFile(path string, content []byte, perm os.FileMode) error {
	if err := os.WriteFile(path, content, perm); err != nil {
		return fmt.Errorf("failed to write stage file %q: %w", path, err)
	}
	if err := os.Chmod(path, perm); err != nil {
		return fmt.Errorf("failed to set the mode of stage file %q: %w", path, err)
	}
	return nil
}

// envFileContent renders env as a .env file, one NAME='value' per line in
// name order. Values hold no line break and no single quote (checked by
// model.ValidateDeployEnv), so single quotes keep docker compose from
// interpolating them and bash from expanding them.
func envFileContent(deploymentID string, env map[string]string) []byte {
	names := make([]string, 0, len(env))
	for name := range env {
		names = append(names, name)
	}
	sort.Strings(names)
	var b strings.Builder
	fmt.Fprintf(&b, "# Written by the AI-App service for deployment %s.\n", deploymentID)
	for _, name := range names {
		fmt.Fprintf(&b, "%s='%s'\n", name, env[name])
	}
	return []byte(b.String())
}

// writeManifest lists the files of dir, except the manifest itself, in
// ManifestFile.
func writeManifest(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("failed to read stage folder %q: %w", dir, err)
	}
	var b strings.Builder
	for _, entry := range entries {
		if entry.Name() == ManifestFile {
			continue
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("stage folder %q holds %q, which is not a regular file", dir, entry.Name())
		}
		b.WriteString(entry.Name())
		b.WriteByte('\n')
	}
	return writeStageFile(filepath.Join(dir, ManifestFile), []byte(b.String()), stageFilePerm)
}

// stageApp copies the files and the script of an application version from its
// folder in the application store into dir.
func stageApp(versionDir, dir string) error {
	filesDir := filepath.Join(versionDir, appstore.FilesDir)
	entries, err := os.ReadDir(filesDir)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to read application files %q: %w", filesDir, err)
	}
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			return fmt.Errorf("application file %q is not a regular file", entry.Name())
		}
		if err := copyFile(filepath.Join(filesDir, entry.Name()), filepath.Join(dir, entry.Name()), stageFilePerm); err != nil {
			return err
		}
	}
	return copyFile(filepath.Join(versionDir, appstore.ScriptFile), filepath.Join(dir, ScriptFile), stageScriptPerm)
}

func copyFile(src, dst string, perm os.FileMode) (err error) {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("failed to read %q: %w", src, err)
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)
	if err != nil {
		return fmt.Errorf("failed to create stage file %q: %w", dst, err)
	}
	defer func() {
		if cerr := out.Close(); err == nil && cerr != nil {
			err = fmt.Errorf("failed to write stage file %q: %w", dst, cerr)
		}
	}()
	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("failed to write stage file %q: %w", dst, err)
	}
	if err := os.Chmod(dst, perm); err != nil {
		return fmt.Errorf("failed to set the mode of stage file %q: %w", dst, err)
	}
	return nil
}
