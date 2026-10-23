package mcpclient

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/ish-hcc/ai-agent-proto/internal/mcpserver"
	"github.com/ish-hcc/ai-agent-proto/internal/tools"
)

// urlSecrets are the pieces secretEndpoint puts into a URL.
var urlSecrets = []string{"mcp-user", "mcp-pass", "mcp-token"}

// secretEndpoint returns base with user information and a query token added,
// the way an MCP endpoint with credentials may be configured.
func secretEndpoint(t *testing.T, base string) string {
	t.Helper()
	u, err := url.Parse(base)
	if err != nil {
		t.Fatalf("parse %q: %v", base, err)
	}
	u.User = url.UserPassword("mcp-user", "mcp-pass")
	u.RawQuery = "token=mcp-token"
	return u.String()
}

// captureLog sends the global logger to a buffer for the rest of the test.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	saved := log.Logger
	log.Logger = zerolog.New(&buf)
	t.Cleanup(func() { log.Logger = saved })
	return &buf
}

func assertNoSecrets(t *testing.T, what, text string) {
	t.Helper()
	for _, secret := range urlSecrets {
		if strings.Contains(text, secret) {
			t.Errorf("%s carries %q: %s", what, secret, text)
		}
	}
}

func TestConnectFailureHidesEndpointSecrets(t *testing.T) {
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	defer failing.Close()

	bases := map[string]string{
		"closed port":    "http://127.0.0.1:1/aiapp/mcp",
		"server refuses": failing.URL + "/aiapp/mcp",
	}
	for name, base := range bases {
		t.Run(name, func(t *testing.T) {
			buf := captureLog(t)
			client := New(secretEndpoint(t, base), "")
			defer func() { _ = client.Close() }()

			err := client.Prepare(context.Background())
			if err == nil {
				t.Fatal("Prepare succeeded")
			}
			assertNoSecrets(t, "error", err.Error())
			if !strings.Contains(err.Error(), base) {
				t.Errorf("error does not name the endpoint %s: %v", base, err)
			}

			if defs := client.Definitions(); defs != nil {
				t.Fatalf("Definitions = %v", defs)
			}
			logged := buf.String()
			if !strings.Contains(logged, "Failed to read the MCP tool list") {
				t.Fatalf("no warning logged: %s", logged)
			}
			assertNoSecrets(t, "log", logged)
		})
	}
}

func TestConnectedLogHidesEndpointSecrets(t *testing.T) {
	srv := httptest.NewServer(mcpserver.NewHandler(tools.NewRegistry(true)))
	defer srv.Close()

	buf := captureLog(t)
	client := New(secretEndpoint(t, srv.URL+"/aiapp/mcp"), "")
	defer func() { _ = client.Close() }()

	if err := client.Prepare(context.Background()); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	logged := buf.String()
	if !strings.Contains(logged, "Connected to the MCP server") ||
		!strings.Contains(logged, srv.URL+"/aiapp/mcp") {
		t.Fatalf("connect not logged with the endpoint: %s", logged)
	}
	assertNoSecrets(t, "log", logged)
}
