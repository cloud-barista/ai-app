// Package web holds the operator console served alongside the API.
//
// The page is embedded rather than read from disk so that the binary and the
// container carry it without a second artifact to deploy or a path to configure.
package web

import (
	"embed"
)

//go:embed index.html
var files embed.FS

// Index returns the console page.
func Index() ([]byte, error) {
	return files.ReadFile("index.html")
}
