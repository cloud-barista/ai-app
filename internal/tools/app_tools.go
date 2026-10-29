package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ish-hcc/ai-agent-proto/internal/appstore"
	"github.com/ish-hcc/ai-agent-proto/internal/model"
)

// RegisterApps adds the uploaded AI application tools to the registry. They
// only read; applications are uploaded over REST, not through a tool.
func RegisterApps(registry *Registry, apps *appstore.Store) {
	registry.Add(listAppBundlesTool(apps))
	registry.Add(getAppBundleTool(apps))
}

func listAppBundlesTool(apps *appstore.Store) Tool {
	return Tool{
		Name:  "list_apps",
		Grade: GradeRead,
		Description: "List the uploaded AI applications, ordered by name, with every version: its number, the " +
			"deployment script, the files it ships (name, size and SHA-256) and when it was uploaded. A node " +
			"receiving a version gets the files in one folder and runs the script there.",
		InputSchema: object(map[string]any{}),
		Handle: func(ctx context.Context, nsID string, input json.RawMessage) (any, error) {
			return model.AppListResp{Apps: apps.List(ctx)}, nil
		},
	}
}

func getAppBundleTool(apps *appstore.Store) Tool {
	return Tool{
		Name:  "get_app",
		Grade: GradeRead,
		Description: "Read one uploaded AI application with every version, or only the version given: the " +
			"deployment script and the name, size and SHA-256 of every file.",
		InputSchema: object(map[string]any{
			"name":    stringProp("Name of the uploaded application"),
			"version": integerProp("Version number, from 1. Omit to read every version"),
		}, "name"),
		Handle: func(ctx context.Context, nsID string, input json.RawMessage) (any, error) {
			var args struct {
				Name    string `json:"name"`
				Version int    `json:"version"`
			}
			if err := decode(input, &args); err != nil {
				return nil, err
			}
			if args.Name == "" {
				return nil, fmt.Errorf("name required")
			}
			if args.Version < 0 {
				return nil, fmt.Errorf("version must be at least 1")
			}
			if args.Version > 0 {
				return apps.GetVersion(ctx, args.Name, args.Version)
			}
			return apps.Get(ctx, args.Name)
		},
	}
}
