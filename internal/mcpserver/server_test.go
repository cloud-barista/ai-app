package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ish-hcc/ai-agent-proto/internal/modelcat"
	"github.com/ish-hcc/ai-agent-proto/internal/nodedeploy"
	"github.com/ish-hcc/ai-agent-proto/internal/tools"
)

// headerTransport adds fixed headers to every request, the way the agent sends
// its namespace.
type headerTransport struct {
	header http.Header
	base   http.RoundTripper
}

func (t headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	for k, v := range t.header {
		req.Header[k] = v
	}
	return t.base.RoundTrip(req)
}

// connect serves the registry over Streamable HTTP and returns a client session.
func connect(t *testing.T, registry *tools.Registry, header http.Header) *mcp.ClientSession {
	t.Helper()
	srv := httptest.NewServer(NewHandler(registry))
	t.Cleanup(srv.Close)

	transport := &mcp.StreamableClientTransport{
		Endpoint:   srv.URL,
		HTTPClient: &http.Client{Transport: headerTransport{header: header, base: http.DefaultTransport}},
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	session, err := client.Connect(context.Background(), transport, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

func TestListRegisteredTools(t *testing.T) {
	models, _, err := modelcat.Open("")
	if err != nil {
		t.Fatal(err)
	}
	store, err := nodedeploy.OpenStore("")
	if err != nil {
		t.Fatal(err)
	}
	svc := nodedeploy.NewService(nil, nil, models, nil, store, nil, nodedeploy.Options{StageRoot: t.TempDir(), DryRun: true})
	t.Cleanup(func() { _ = svc.Shutdown(context.Background()) })
	registry := tools.NewRegistry(true)
	tools.RegisterModels(registry, models)
	tools.RegisterNodeDeploy(registry, svc)

	session := connect(t, registry, nil)
	if got := session.InitializeResult().Meta[MetaDryRun]; got != true {
		t.Errorf("initialize _meta %s = %v, want true", MetaDryRun, got)
	}

	res, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	if len(res.Tools) != 8 {
		t.Fatalf("tools/list returned %d tools, want 8", len(res.Tools))
	}
	found := false
	for _, tool := range res.Tools {
		if tool.Name == "get_deployment" {
			found = true
			if got := tool.Meta[MetaGrade]; got != string(tools.GradeRead) {
				t.Errorf("get_deployment: _meta grade = %v, want read", got)
			}
		}
		grade := registry.Grade(tool.Name)
		if got := tool.Meta[MetaGrade]; got != string(grade) {
			t.Errorf("%s: _meta grade = %v, want %q", tool.Name, got, grade)
		}
		if got := tool.Meta[MetaGuardedByDryRun]; got != grade.GuardedByDryRun() {
			t.Errorf("%s: _meta guardedByDryRun = %v, want %v", tool.Name, got, grade.GuardedByDryRun())
		}
		wantReadOnly := grade == tools.GradeRead || grade == tools.GradeReview
		if tool.Annotations == nil || tool.Annotations.ReadOnlyHint != wantReadOnly {
			t.Errorf("%s: readOnlyHint wrong for grade %s", tool.Name, grade)
		}
		if tool.Annotations == nil || tool.Annotations.DestructiveHint == nil ||
			*tool.Annotations.DestructiveHint != (grade == tools.GradeDestructive) {
			t.Errorf("%s: destructiveHint wrong for grade %s", tool.Name, grade)
		}
	}
	if !found {
		t.Error("tools/list does not carry get_deployment")
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

func TestCallPassesNamespaceHeader(t *testing.T) {
	cases := []struct {
		header http.Header
		want   string
	}{
		{http.Header{NamespaceHeader: {"team-a"}}, "team-a"},
		{nil, DefaultNamespace},
	}
	for _, c := range cases {
		var gotNS string
		session := connect(t, fakeRegistry(&gotNS), c.header)
		res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "echo_ns"})
		if err != nil {
			t.Fatalf("tools/call: %v", err)
		}
		if res.IsError {
			t.Fatalf("tools/call returned isError: %+v", res.Content)
		}
		if gotNS != c.want {
			t.Errorf("handler nsID = %q, want %q", gotNS, c.want)
		}
		if len(res.Content) != 1 {
			t.Fatalf("got %d contents, want 1", len(res.Content))
		}
		text, ok := res.Content[0].(*mcp.TextContent)
		if !ok {
			t.Fatalf("content is %T, want text", res.Content[0])
		}
		var body map[string]string
		if err := json.Unmarshal([]byte(text.Text), &body); err != nil || body["nsId"] != c.want {
			t.Errorf("content = %q, want JSON with nsId %q", text.Text, c.want)
		}
	}
}

func TestCallErrorSetsIsError(t *testing.T) {
	var gotNS string
	session := connect(t, fakeRegistry(&gotNS), nil)
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "always_fails"})
	if err != nil {
		t.Fatalf("tools/call: %v", err)
	}
	if !res.IsError {
		t.Fatal("isError = false, want true")
	}
	if text, ok := res.Content[0].(*mcp.TextContent); !ok || text.Text != "boom" {
		t.Errorf("content = %+v, want text \"boom\"", res.Content)
	}
}
