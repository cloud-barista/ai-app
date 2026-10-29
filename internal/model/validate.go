package model

import (
	"regexp"
	"strings"
	"unicode"
)

// envNamePattern is the shell variable name an environment entry is exported under.
var envNamePattern = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

// validateURLPath checks a path that is appended to "scheme://host:port".
//
// An "@" would turn everything before it into userinfo and send the request to
// another host, a "#" would cut the path short, and whitespace would split the
// command line the path is placed on.
func validateURLPath(field, path string) error {
	switch {
	case path == "":
		return nil
	case !strings.HasPrefix(path, "/"):
		return errInvalid(field, "must start with /")
	case strings.ContainsAny(path, "@#") || strings.IndexFunc(path, unicode.IsSpace) >= 0:
		return errInvalid(field, "must not contain @, # or whitespace")
	}
	return nil
}
