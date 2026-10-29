package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ish-hcc/ai-agent-proto/internal/appstore"
	"github.com/ish-hcc/ai-agent-proto/internal/model"
)

func newAppRegistry(t *testing.T) *Registry {
	t.Helper()
	store, err := appstore.Open(t.TempDir(), 0)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ctx := context.Background()
	for _, script := range []string{"echo one\n", "echo two\n"} {
		files := []appstore.File{{Name: "docker-compose.yml", Content: strings.NewReader("services: {}\n")}}
		if _, err := store.Register(ctx, "open-webui", script, files); err != nil {
			t.Fatalf("Register: %v", err)
		}
	}
	registry := NewRegistry(true)
	RegisterApps(registry, store)
	return registry
}

func TestAppToolsAreRead(t *testing.T) {
	registry := newAppRegistry(t)
	for _, name := range []string{"list_apps", "get_app"} {
		if grade := registry.Grade(name); grade != GradeRead {
			t.Errorf("%s grade = %q, want read", name, grade)
		}
	}
}

func TestListAndGetApps(t *testing.T) {
	registry := newAppRegistry(t)
	ctx := context.Background()

	out, err := registry.Call(ctx, "default", "list_apps", nil)
	if err != nil {
		t.Fatalf("list_apps: %v", err)
	}
	apps := out.(model.AppListResp).Apps
	if len(apps) != 1 || len(apps[0].Versions) != 2 {
		t.Fatalf("list_apps = %+v, want one application with two versions", apps)
	}

	out, err = registry.Call(ctx, "default", "get_app", json.RawMessage(`{"name":"open-webui"}`))
	if err != nil {
		t.Fatalf("get_app: %v", err)
	}
	if app := out.(*model.App); len(app.Versions) != 2 {
		t.Errorf("get_app versions = %d, want 2", len(app.Versions))
	}

	out, err = registry.Call(ctx, "default", "get_app", json.RawMessage(`{"name":"open-webui","version":2}`))
	if err != nil {
		t.Fatalf("get_app version 2: %v", err)
	}
	if v := out.(*model.AppVersion); v.Version != 2 || v.Script != "echo two\n" {
		t.Errorf("get_app version 2 = %+v", v)
	}

	for _, tc := range []struct {
		input string
		want  error
	}{
		{`{"name":"nope"}`, appstore.ErrNotFound},
		{`{"name":"open-webui","version":3}`, appstore.ErrVersionNotFound},
	} {
		if _, err := registry.Call(ctx, "default", "get_app", json.RawMessage(tc.input)); !errors.Is(err, tc.want) {
			t.Errorf("get_app %s error = %v, want %v", tc.input, err, tc.want)
		}
	}
	if _, err := registry.Call(ctx, "default", "get_app", json.RawMessage(`{}`)); err == nil {
		t.Error("get_app without a name succeeded")
	}
}
