package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
)

func TestListToolsTakeLimitAndOffset(t *testing.T) {
	registries := map[string]*Registry{
		"list_models":           newModelRegistry(t),
		"list_apps":             newAppRegistry(t),
		"list_node_deployments": newNodeDeployRegistry(t),
	}
	registries["list_endpoints"] = registries["list_node_deployments"]
	ctx := context.Background()

	for name, registry := range registries {
		var props map[string]any
		for _, def := range registry.Definitions() {
			if def.Name == name {
				schema, _ := def.InputSchema.(map[string]any)
				props, _ = schema["properties"].(map[string]any)
			}
		}
		for _, key := range []string{"limit", "offset"} {
			if prop, _ := props[key].(map[string]any); prop["type"] != "integer" {
				t.Errorf("%s input %s = %v, want an integer", name, key, props[key])
			}
		}

		for _, bad := range []string{`{"limit":0}`, `{"limit":1001}`, `{"limit":-1}`, `{"offset":-1}`} {
			_, err := registry.Call(ctx, "default", name, json.RawMessage(bad))
			if !errors.Is(err, model.ErrValidation) {
				t.Errorf("%s %s err = %v, want a validation error", name, bad, err)
			}
		}
		if _, err := registry.Call(ctx, "default", name, json.RawMessage(`{"limit":1001}`)); err == nil ||
			!strings.Contains(err.Error(), "from 1 to 1000") {
			t.Errorf("%s limit 1001 err = %v, want the bound in the message", name, err)
		}
	}

	pageOf := func(out any) (int, model.Page) {
		switch resp := out.(type) {
		case model.ModelListResp:
			return len(resp.Models), resp.Page
		case model.AppListResp:
			return len(resp.Apps), resp.Page
		case model.NodeDeploymentListResp:
			return len(resp.Deployments), resp.Page
		case model.EndpointListResp:
			return len(resp.Endpoints), resp.Page
		}
		t.Fatalf("unexpected result %T", out)
		return 0, model.Page{}
	}
	for _, tc := range []struct {
		name, input string
		items       int
		page        model.Page
	}{
		{"list_models", ``, 3, model.Page{Total: 3, Limit: 100}},
		{"list_models", `{"limit":2,"offset":1}`, 2, model.Page{Total: 3, Limit: 2, Offset: 1}},
		{"list_models", `{"modality":"image","limit":1}`, 1, model.Page{Total: 2, Limit: 1}},
		{"list_models", `{"offset":3}`, 0, model.Page{Total: 3, Limit: 100, Offset: 3}},
		{"list_apps", `{}`, 1, model.Page{Total: 1, Limit: 100}},
		{"list_apps", `{"offset":1}`, 0, model.Page{Total: 1, Limit: 100, Offset: 1}},
		{"list_node_deployments", `{"limit":1000}`, 0, model.Page{Limit: 1000}},
		{"list_endpoints", `{"limit":5,"offset":2}`, 0, model.Page{Limit: 5, Offset: 2}},
	} {
		out, err := registries[tc.name].Call(ctx, "default", tc.name, json.RawMessage(tc.input))
		if err != nil {
			t.Fatalf("%s %s: %v", tc.name, tc.input, err)
		}
		if items, page := pageOf(out); items != tc.items || page != tc.page {
			t.Errorf("%s %s = %d items, %+v, want %d, %+v", tc.name, tc.input, items, page, tc.items, tc.page)
		}
	}
	got, _ := registries["list_models"].Call(ctx, "default", "list_models", json.RawMessage(`{"limit":1,"offset":1}`))
	if models := got.(model.ModelListResp).Models; len(models) != 1 || models[0].ID != "qwen3-embedding-0.6b" {
		t.Errorf("list_models limit 1 offset 1 = %+v, want the second model by id", models)
	}
}
