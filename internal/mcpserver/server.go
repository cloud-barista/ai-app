// Package mcpserver publishes the tool registry as an MCP server.
//
// The registry stays the single source of tool names, descriptions, input
// schemas and grades. This package only translates it: a grade becomes MCP tool
// annotations plus _meta entries, so that any MCP client sees the same guard
// state the service enforces.
package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ish-hcc/ai-agent-proto/internal/tools"
)

const (
	// NamespaceHeader carries the namespace a tool call runs in. It is a header,
	// not a tool argument, so that the model cannot pick the namespace.
	NamespaceHeader = "X-AIMCMP-Namespace"
	// DefaultNamespace is used when a request carries no namespace header.
	DefaultNamespace = "default"

	// MetaGrade is the _meta key holding a tool's risk grade.
	MetaGrade = "ai-mcmp/grade"
	// MetaGuardedByDryRun is the _meta key telling whether dry run can block a tool.
	MetaGuardedByDryRun = "ai-mcmp/guardedByDryRun"
	// MetaDryRun is the initialize result _meta key holding the dry-run state.
	MetaDryRun = "ai-mcmp/dryRun"

	serverName    = "ai-mcmp-ai-app"
	serverVersion = "0.1.0"
)

// NewServer builds an MCP server exposing every tool in the registry.
func NewServer(registry *tools.Registry) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: serverName, Version: serverVersion}, nil)

	// Implementation has no _meta field, so the dry-run state rides on the
	// initialize (and discover) result instead.
	dryRun := registry.DryRun()
	server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			result, err := next(ctx, method, req)
			switch res := result.(type) {
			case *mcp.InitializeResult:
				res.Meta = withDryRun(res.Meta, dryRun)
			case *mcp.DiscoverResult:
				res.Meta = withDryRun(res.Meta, dryRun)
			}
			return result, err
		}
	})

	for _, def := range registry.Definitions() {
		grade := registry.Grade(def.Name)
		destructive := grade == tools.GradeDestructive
		server.AddTool(&mcp.Tool{
			Name:        def.Name,
			Description: def.Description,
			InputSchema: def.InputSchema,
			Annotations: &mcp.ToolAnnotations{
				ReadOnlyHint:    grade == tools.GradeRead || grade == tools.GradeReview,
				DestructiveHint: &destructive,
			},
			Meta: mcp.Meta{
				MetaGrade:           string(grade),
				MetaGuardedByDryRun: grade.GuardedByDryRun(),
			},
		}, callHandler(registry, def.Name))
	}
	return server
}

// NewHandler wraps the MCP server in a Streamable HTTP handler.
func NewHandler(registry *tools.Registry) http.Handler {
	server := NewServer(registry)
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
}

// callHandler runs one registry tool and renders its result as a single JSON
// text content. A handler error becomes a tool result with isError set, so the
// caller sees the message instead of a protocol failure.
func callHandler(registry *tools.Registry, name string) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		input := req.Params.Arguments
		if len(input) == 0 {
			input = json.RawMessage("{}")
		}
		out, err := registry.Call(ctx, namespace(req), name, input)
		if err != nil {
			result := &mcp.CallToolResult{}
			result.SetError(err)
			return result, nil
		}
		body, err := json.Marshal(out)
		if err != nil {
			result := &mcp.CallToolResult{}
			result.SetError(fmt.Errorf("failed to encode %s result: %w", name, err))
			return result, nil
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(body)}}}, nil
	}
}

func namespace(req *mcp.CallToolRequest) string {
	if req.Extra != nil {
		if ns := req.Extra.Header.Get(NamespaceHeader); ns != "" {
			return ns
		}
	}
	return DefaultNamespace
}

func withDryRun(meta mcp.Meta, dryRun bool) mcp.Meta {
	if meta == nil {
		meta = mcp.Meta{}
	}
	meta[MetaDryRun] = dryRun
	return meta
}
