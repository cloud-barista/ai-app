// Package tools exposes the deployment capabilities as functions a model can call.
//
// A tool carries a risk grade, and the grade decides whether a call is executed
// or only planned. Execution is never decided by the model: a model that is sure
// it should provision still cannot, because the guard lives here.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/ish-hcc/ai-agent-proto/internal/llm"
	"github.com/ish-hcc/ai-agent-proto/internal/model"
)

// Grade is how much damage a tool can do.
type Grade string

// Tool risk grades, in increasing order of consequence.
const (
	// GradeRead only reads state. Always executed.
	GradeRead Grade = "read"
	// GradeReview validates a request upstream without creating anything. Always executed.
	GradeReview Grade = "review"
	// GradeWrite creates or changes billable resources. Guarded by dry run.
	GradeWrite Grade = "write"
	// GradeDestructive removes resources and cannot be undone. Guarded by dry run.
	GradeDestructive Grade = "destructive"
)

// GuardedByDryRun reports whether the dry-run setting can block this grade.
func (g Grade) GuardedByDryRun() bool {
	return g == GradeWrite || g == GradeDestructive
}

// Handler runs one tool call. Namespace comes from the request path, not from
// the model, so a run cannot wander into another tenant's namespace.
type Handler func(ctx context.Context, nsID string, input json.RawMessage) (any, error)

// Tool is one callable capability.
type Tool struct {
	Name        string
	Description string
	Grade       Grade
	InputSchema map[string]any
	Handle      Handler
}

// Registry holds every tool the agent may call.
type Registry struct {
	tools map[string]Tool
	// dryRun mirrors the deploy service setting, so the tool surface reports the
	// same guard state that the service enforces.
	dryRun bool
}

// NewRegistry builds an empty Registry.
func NewRegistry(dryRun bool) *Registry {
	return &Registry{tools: make(map[string]Tool), dryRun: dryRun}
}

// Add registers one tool.
func (r *Registry) Add(tool Tool) {
	r.tools[tool.Name] = tool
}

// Names returns every registered tool name, sorted.
//
// The order is stable so that the tool list sent to the model does not change
// between requests for no reason.
func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Definitions renders the tool list in the shape the Messages API expects.
func (r *Registry) Definitions() []llm.ToolDef {
	names := r.Names()
	defs := make([]llm.ToolDef, 0, len(names))
	for _, name := range names {
		tool := r.tools[name]
		defs = append(defs, llm.ToolDef{
			Name:        tool.Name,
			Description: r.describe(tool),
			InputSchema: tool.InputSchema,
		})
	}
	return defs
}

// Descriptors renders the tool list for API callers.
func (r *Registry) Descriptors() []model.ToolDescriptor {
	names := r.Names()
	out := make([]model.ToolDescriptor, 0, len(names))
	for _, name := range names {
		tool := r.tools[name]
		out = append(out, model.ToolDescriptor{
			Name:            tool.Name,
			Description:     tool.Description,
			Grade:           string(tool.Grade),
			GuardedByDryRun: tool.Grade.GuardedByDryRun(),
			InputSchema:     tool.InputSchema,
		})
	}
	return out
}

// Grade reports the risk grade of a tool.
func (r *Registry) Grade(name string) Grade {
	if tool, ok := r.tools[name]; ok {
		return tool.Grade
	}
	return ""
}

// DryRun reports whether guarded tools are blocked.
func (r *Registry) DryRun() bool { return r.dryRun }

// Call runs one tool by name.
//
// Guarded tools still reach their handler when dry run is on: the handlers route
// through the deploy service, which reports what it would have done instead of
// doing it. Keeping one path means a dry run and a real run archive the same shape.
func (r *Registry) Call(ctx context.Context, nsID, name string, input json.RawMessage) (any, error) {
	tool, ok := r.tools[name]
	if !ok {
		return nil, fmt.Errorf("unknown tool %q", name)
	}
	return tool.Handle(ctx, nsID, input)
}

// describe appends the guard state to a tool description so the model knows,
// before it plans, that a guarded call will not take effect.
func (r *Registry) describe(tool Tool) string {
	if r.dryRun && tool.Grade.GuardedByDryRun() {
		return tool.Description + " NOTE: dry run is on, so this call is planned and archived but never sent to the cloud."
	}
	return tool.Description
}

// object is a small helper for the JSON Schema literals below.
func object(properties map[string]any, required ...string) map[string]any {
	if required == nil {
		required = []string{}
	}
	return map[string]any{
		"type":       "object",
		"properties": properties,
		"required":   required,
	}
}

func stringProp(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

func integerProp(description string) map[string]any {
	return map[string]any{"type": "integer", "description": description}
}

func enumProp(description string, values ...string) map[string]any {
	return map[string]any{"type": "string", "description": description, "enum": values}
}
