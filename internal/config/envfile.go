package config

import (
	"bufio"
	"errors"
	"io/fs"
	"os"
	"strings"
)

// DefaultEnvFile is the optional settings file loaded before the environment is
// read. It is the file the setup template is copied to.
const DefaultEnvFile = "conf/setup.env"

// LoadEnvFile applies the settings in path to the process environment.
//
// A missing file is not an error. The file is a convenience for a developer who
// wants persistent settings, not a requirement: every setting has a default, so a
// checkout with no file at all still starts.
//
// A variable already present in the environment wins over the file, so a
// container or a shell can override one setting without having to rewrite the
// file or delete it.
func LoadEnvFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	defer func() { _ = file.Close() }()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		key, value, ok := parseEnvLine(scanner.Text())
		if !ok {
			continue
		}
		if _, set := os.LookupEnv(key); set {
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			return err
		}
	}

	return scanner.Err()
}

// parseEnvLine reads one assignment. The file is written to be sourced by a
// shell, so a leading "export" and surrounding quotes are both expected.
func parseEnvLine(line string) (key, value string, ok bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", false
	}

	line = strings.TrimPrefix(line, "export ")

	key, value, found := strings.Cut(line, "=")
	if !found {
		return "", "", false
	}

	key = strings.TrimSpace(key)
	if key == "" {
		return "", "", false
	}

	value = strings.TrimSpace(value)
	if len(value) >= 2 {
		if (value[0] == '"' && value[len(value)-1] == '"') ||
			(value[0] == '\'' && value[len(value)-1] == '\'') {
			value = value[1 : len(value)-1]
		}
	}

	return key, value, true
}
