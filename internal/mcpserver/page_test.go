package mcpserver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/modelcat"
	"github.com/ish-hcc/ai-agent-proto/internal/tools"
)

func TestListToolPagesOverMCP(t *testing.T) {
	models, _, err := modelcat.Open("")
	if err != nil {
		t.Fatal(err)
	}
	if err := modelcat.Seed(context.Background(), models); err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry(true)
	tools.RegisterModels(registry, models)
	session := connect(t, registry, nil)
	ctx := context.Background()

	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "list_models", Arguments: map[string]any{"limit": 2, "offset": 1}})
	if err != nil || res.IsError {
		t.Fatalf("list_models limit 2 offset 1: %v %+v", err, res)
	}
	var resp model.ModelListResp
	if err := json.Unmarshal([]byte(res.Content[0].(*mcp.TextContent).Text), &resp); err != nil {
		t.Fatalf("list_models result: %v", err)
	}
	if len(resp.Models) != 2 || resp.Models[0].ID != "qwen3-embedding-0.6b" ||
		resp.Page != (model.Page{Total: 3, Limit: 2, Offset: 1}) {
		t.Errorf("list_models limit 2 offset 1 = %d models, %+v, want 2 from the second with total 3", len(resp.Models), resp.Page)
	}

	res, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "list_models", Arguments: map[string]any{"limit": 0}})
	if err != nil || !res.IsError || !strings.Contains(res.Content[0].(*mcp.TextContent).Text, "from 1 to 1000") {
		t.Errorf("list_models limit 0 = %v %+v, want a tool error naming the bound", err, res)
	}
}
