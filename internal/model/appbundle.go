package model

import (
	"fmt"
	"regexp"
	"time"
)

// MaxAppScriptBytes bounds the deployment script of an application version.
const MaxAppScriptBytes = 64 << 10

// MaxAppFiles bounds how many files one application version holds.
const MaxAppFiles = 256

// appFileNamePattern is what an uploaded file name may look like. Files are
// kept flat in one folder on the store and on the node, and the script refers
// to them by name, so a name is one path element without separators,
// whitespace or quoting needs. A leading "." is refused: that keeps "." and
// ".." out, and keeps .env free for the environment a deployment request
// writes beside the files.
var appFileNamePattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._+-]{0,254}$`)

// App is one registered AI application with every version uploaded under its
// name.
type App struct {
	// Name identifies the application: lower case letters, digits, "_" and
	// "-", at most 63 characters. Uploading again under the same name adds a
	// version.
	Name string `json:"name" example:"open-webui"`
	// Versions are the uploaded versions, oldest first.
	Versions []AppVersion `json:"versions"`
}

// AppVersion is one upload of an application: the files a node receives and
// the script it runs.
type AppVersion struct {
	// Version numbers the uploads of one name from 1, one higher than the
	// highest version present when it was uploaded.
	Version int `json:"version" example:"1"`
	// Script is the shell script run on the node from the folder the files
	// are copied to. At most 64 KiB.
	Script string `json:"script" example:"docker compose up -d"`
	// Files are the uploaded files, in upload order. They are copied flat into
	// the folder the script runs in.
	Files []AppFile `json:"files"`
	// CreatedAt is when the version was registered.
	CreatedAt time.Time `json:"createdAt" example:"2026-10-29T12:00:00Z"`
}

// AppFile is one uploaded file of an application version.
type AppFile struct {
	// Name is the file name on the node: letters, digits, ".", "_", "+" and
	// "-", not starting with ".", at most 255 characters.
	Name string `json:"name" example:"docker-compose.yml"`
	// SizeBytes is the file size in bytes.
	SizeBytes int64 `json:"sizeBytes" example:"812"`
	// SHA256 is the hex encoded SHA-256 digest of the file content.
	SHA256 string `json:"sha256" example:"9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"`
}

// AppListResp is the application listing body.
type AppListResp struct {
	Apps []App `json:"apps"`
}

// appNamePattern is what an application name may look like. Unlike a catalog
// identifier it holds no ".", because the name becomes part of a docker
// compose project name on the node, and compose refuses a ".".
var appNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

// ValidateAppName reports why name cannot name an application.
func ValidateAppName(name string) error {
	switch {
	case name == "":
		return errRequired("name")
	case !appNamePattern.MatchString(name):
		return errInvalid("name", "must match [a-z0-9][a-z0-9_-]* and be at most 63 characters")
	}
	return nil
}

// ValidateAppScript reports why script cannot be the script of an application
// version.
func ValidateAppScript(script string) error {
	switch {
	case script == "":
		return errRequired("script")
	case len(script) > MaxAppScriptBytes:
		return errInvalid("script", fmt.Sprintf("must be at most %d bytes", MaxAppScriptBytes))
	}
	return nil
}

// ValidateAppFileNames reports why names cannot be the file names of one
// application version.
func ValidateAppFileNames(names []string) error {
	if len(names) > MaxAppFiles {
		return errInvalid("files", fmt.Sprintf("must hold at most %d files", MaxAppFiles))
	}
	seen := make(map[string]bool, len(names))
	for i, name := range names {
		field := fmt.Sprintf("files[%d]", i)
		switch {
		case name == "":
			return errRequired(field + ".name")
		case !appFileNamePattern.MatchString(name):
			return errInvalid(field+".name", fmt.Sprintf("%q must be one file name of letters, digits, \".\", \"_\", \"+\" and \"-\", not start with \".\" and be at most 255 characters", name))
		case seen[name]:
			return errInvalid(field+".name", fmt.Sprintf("%q is uploaded more than once", name))
		}
		seen[name] = true
	}
	return nil
}

// Validate reports why a stored version is not one this service could have
// written.
func (v *AppVersion) Validate() error {
	if v.Version < 1 {
		return errInvalid("version", "must be at least 1")
	}
	if err := ValidateAppScript(v.Script); err != nil {
		return err
	}
	names := make([]string, len(v.Files))
	for i, f := range v.Files {
		names[i] = f.Name
	}
	return ValidateAppFileNames(names)
}

// Version finds one version of the application.
func (a *App) Version(version int) (*AppVersion, bool) {
	for i := range a.Versions {
		if a.Versions[i].Version == version {
			return &a.Versions[i], true
		}
	}
	return nil, false
}
