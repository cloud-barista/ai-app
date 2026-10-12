package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/ish-hcc/ai-agent-proto/internal/agent"
	"github.com/ish-hcc/ai-agent-proto/internal/catalog"
	"github.com/ish-hcc/ai-agent-proto/internal/config"
	"github.com/ish-hcc/ai-agent-proto/internal/deploy"
	"github.com/ish-hcc/ai-agent-proto/internal/mcpserver"
	"github.com/ish-hcc/ai-agent-proto/internal/tools"
	"github.com/ish-hcc/ai-agent-proto/internal/tumblebug"
)

var _ agent.ToolSource = (*Client)(nil)
var _ agent.Preparer = (*Client)(nil)

// serve publishes the registry over Streamable HTTP and returns a client for it.
func serve(t *testing.T, registry *tools.Registry) *Client {
	t.Helper()
	srv := httptest.NewServer(mcpserver.NewHandler(registry))
	t.Cleanup(srv.Close)

	client := New(srv.URL, "")
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func TestDefinitionsGradesAndDryRun(t *testing.T) {
	cfg := config.TumblebugConfig{DryRun: true}
	apps := catalog.NewStore()
	registry := tools.NewRegistry(cfg.DryRun)
	tools.Register(registry, apps, deploy.NewService(tumblebug.NewClient(cfg), apps, cfg))

	client := serve(t, registry)
	if err := client.Prepare(context.Background()); err != nil {
		t.Fatalf("prepare: %v", err)
	}

	defs := client.Definitions()
	if len(defs) != 15 {
		t.Fatalf("Definitions returned %d tools, want 15", len(defs))
	}
	want := map[string]bool{}
	for _, def := range registry.Definitions() {
		want[def.Name] = true
	}
	for _, def := range defs {
		if !want[def.Name] {
			t.Errorf("unexpected tool %q", def.Name)
		}
		if def.Description == "" || def.InputSchema == nil {
			t.Errorf("%s: description or input schema missing", def.Name)
		}
		if got, wantGrade := client.Grade(def.Name), registry.Grade(def.Name); got != wantGrade {
			t.Errorf("%s: grade = %q, want %q", def.Name, got, wantGrade)
		}
	}
	if got := client.Grade("check_serving_health"); got != tools.GradeRead {
		t.Errorf("check_serving_health grade = %q, want read", got)
	}
	if !client.DryRun() {
		t.Error("DryRun = false, want true")
	}
}

// fakeRegistry holds one read tool that echoes its namespace and one that fails.
func fakeRegistry(gotNS *string) *tools.Registry {
	registry := tools.NewRegistry(true)
	registry.Add(tools.Tool{
		Name:        "echo_ns",
		Description: "Echo the namespace.",
		Grade:       tools.GradeRead,
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
		Handle: func(ctx context.Context, nsID string, input json.RawMessage) (any, error) {
			*gotNS = nsID
			return map[string]string{"nsId": nsID}, nil
		},
	})
	registry.Add(tools.Tool{
		Name:        "always_fails",
		Description: "Fail.",
		Grade:       tools.GradeRead,
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
		Handle: func(ctx context.Context, nsID string, input json.RawMessage) (any, error) {
			return nil, errors.New("boom")
		},
	})
	return registry
}

func TestCallCarriesNamespace(t *testing.T) {
	var gotNS string
	client := serve(t, fakeRegistry(&gotNS))
	if err := client.Prepare(context.Background()); err != nil {
		t.Fatalf("prepare: %v", err)
	}

	for _, ns := range []string{"team-a", "team-b"} {
		out, err := client.Call(context.Background(), ns, "echo_ns", nil)
		if err != nil {
			t.Fatalf("call: %v", err)
		}
		if gotNS != ns {
			t.Errorf("handler nsID = %q, want %q", gotNS, ns)
		}
		raw, ok := out.(json.RawMessage)
		if !ok {
			t.Fatalf("result is %T, want json.RawMessage", out)
		}
		var body map[string]string
		if err := json.Unmarshal(raw, &body); err != nil || body["nsId"] != ns {
			t.Errorf("result = %s, want JSON with nsId %q", raw, ns)
		}
	}
}

func TestCallToolErrorIsError(t *testing.T) {
	var gotNS string
	client := serve(t, fakeRegistry(&gotNS))
	if err := client.Prepare(context.Background()); err != nil {
		t.Fatalf("prepare: %v", err)
	}

	_, err := client.Call(context.Background(), "default", "always_fails", nil)
	if err == nil || err.Error() != "boom" {
		t.Fatalf("err = %v, want boom", err)
	}
	if errors.Is(err, ErrUnavailable) {
		t.Error("a tool failure must not read as an unavailable server")
	}
}

func TestPrepareUnreachable(t *testing.T) {
	srv := httptest.NewServer(nil)
	endpoint := srv.URL
	srv.Close()

	client := New(endpoint, "")
	if err := client.Prepare(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	if defs := client.Definitions(); defs != nil {
		t.Errorf("Definitions = %d tools, want none", len(defs))
	}
}

func TestPrepareReconnectsAfterRestart(t *testing.T) {
	var gotNS string
	registry := fakeRegistry(&gotNS)
	var current atomic.Value
	current.Store(mcpserver.NewHandler(registry))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current.Load().(http.Handler).ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	client := New(srv.URL, "")
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Prepare(context.Background()); err != nil {
		t.Fatalf("prepare: %v", err)
	}

	// A new handler at the same address forgets every session, as a restarted
	// AI-App service would.
	current.Store(mcpserver.NewHandler(registry))

	if err := client.Prepare(context.Background()); err != nil {
		t.Fatalf("prepare after restart: %v", err)
	}
	if _, err := client.Call(context.Background(), "team-c", "echo_ns", nil); err != nil {
		t.Fatalf("call after restart: %v", err)
	}
	if gotNS != "team-c" {
		t.Errorf("handler nsID = %q, want team-c", gotNS)
	}
}
