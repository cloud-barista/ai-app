package tools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/modelcat"
	"github.com/ish-hcc/ai-agent-proto/internal/nodedeploy"
)

func newNodeDeployRegistry(t *testing.T) *Registry {
	t.Helper()
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
	registry := NewRegistry(true)
	RegisterNodeDeploy(registry, svc)
	return registry
}

func TestNodeDeployToolGrades(t *testing.T) {
	registry := newNodeDeployRegistry(t)
	for name, want := range map[string]Grade{
		"create_deployment":     GradeWrite,
		"control_node":          GradeWrite,
		"get_deployment":        GradeRead,
		"list_node_deployments": GradeRead,
		"get_deployment_logs":   GradeRead,
	} {
		if got := registry.Grade(name); got != want {
			t.Errorf("%s grade = %q, want %q", name, got, want)
		}
	}
}

func TestNodeDeployToolsCheckTheirInput(t *testing.T) {
	registry := newNodeDeployRegistry(t)
	ctx := context.Background()

	k8s := `{"type":"app","targets":[{"kind":"k8s","nsId":"etri"}],"app":{"name":"web"}}`
	if _, err := registry.Call(ctx, "default", "create_deployment", json.RawMessage(k8s)); !errors.Is(err, model.ErrUnsupportedTarget) {
		t.Errorf("k8s target = %v", err)
	}
	if _, err := registry.Call(ctx, "default", "get_deployment", json.RawMessage(`{}`)); err == nil {
		t.Error("get_deployment without id was accepted")
	}
	if _, err := registry.Call(ctx, "default", "get_deployment", json.RawMessage(`{"id":"missing"}`)); !errors.Is(err, nodedeploy.ErrNotFound) {
		t.Errorf("get_deployment missing = %v", err)
	}
	out, err := registry.Call(ctx, "default", "list_node_deployments", json.RawMessage(`{}`))
	if list, ok := out.(model.NodeDeploymentListResp); err != nil || !ok || len(list.Deployments) != 0 {
		t.Errorf("list = %v, %v", out, err)
	}
}
