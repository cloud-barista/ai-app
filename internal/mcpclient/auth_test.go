package mcpclient_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/ish-hcc/ai-agent-proto/internal/mcpclient"
	"github.com/ish-hcc/ai-agent-proto/internal/mcpserver"
	"github.com/ish-hcc/ai-agent-proto/internal/rest"
	"github.com/ish-hcc/ai-agent-proto/internal/tools"
)

// The rest package imports this one, so a test that puts the MCP server behind
// the rest middleware lives outside it.
func TestTokenReachesProtectedServer(t *testing.T) {
	var gotNS string
	registry := tools.NewRegistry(true)
	registry.Add(tools.Tool{
		Name:        "echo_ns",
		Description: "Echo the namespace.",
		Grade:       tools.GradeRead,
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
		Handle: func(ctx context.Context, nsID string, input json.RawMessage) (any, error) {
			gotNS = nsID
			return map[string]string{"nsId": nsID}, nil
		},
	})

	e := echo.New()
	e.Use(rest.RequireBearer("s3cret"))
	e.Any(rest.BasePath+"/mcp", echo.WrapHandler(mcpserver.NewHandler(registry)))
	srv := httptest.NewServer(e)
	t.Cleanup(srv.Close)
	endpoint := srv.URL + rest.BasePath + "/mcp"

	client := mcpclient.New(endpoint, "s3cret")
	t.Cleanup(func() { _ = client.Close() })
	if defs := client.Definitions(); len(defs) != 1 {
		t.Fatalf("Definitions with the token returned %d tools, want 1", len(defs))
	}
	if _, err := client.Call(context.Background(), "team-d", "echo_ns", nil); err != nil {
		t.Fatalf("call with the token: %v", err)
	}
	if gotNS != "team-d" {
		t.Errorf("handler nsID = %q, want team-d", gotNS)
	}

	for _, token := range []string{"", "wrong"} {
		client := mcpclient.New(endpoint, token)
		if err := client.Prepare(context.Background()); !errors.Is(err, mcpclient.ErrUnavailable) {
			t.Errorf("Prepare with token %q: err = %v, want ErrUnavailable", token, err)
		}
		_ = client.Close()
	}
}
