package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
)

// Names of the AI-App tools the engine calls. Every call the workflows and the
// catalog lookup make names one of these, so appToolsUsed is the whole set.
const (
	toolGetModel          = "get_model"
	toolGetApp            = "get_app"
	toolCreateDeployment  = "create_deployment"
	toolGetDeployment     = "get_deployment"
	toolGetDeploymentLogs = "get_deployment_logs"
	toolControlNode       = "control_node"
	toolListModels        = "list_models"
	toolListApps          = "list_apps"
)

// appToolsUsed lists the AI-App tools the engine calls, in name order. The
// engine card reports these and no other AI-App tool.
var appToolsUsed = []string{
	toolControlNode,
	toolCreateDeployment,
	toolGetApp,
	toolGetDeployment,
	toolGetDeploymentLogs,
	toolGetModel,
	toolListApps,
	toolListModels,
}

// controlActions are the lifecycle actions control_node accepts.
var controlActions = map[string]bool{
	"suspend":   true,
	"resume":    true,
	"reboot":    true,
	"terminate": true,
}

// Statuses control_node gives each node.
const (
	controlDone    = "done"
	controlFailed  = "failed"
	controlBlocked = "blocked"
)

// statusLogLines is how many of the last task output lines of each node the
// status workflow reports.
const statusLogLines = 5

// Heal reads a deployment and, when it did not succeed on every node, runs
// the request it was made with once more as a new deployment and follows that
// one until it ends. A deployment still running is not healed.
func (e *Engine) Heal(ctx context.Context, nsID string, in HealInput) (*Run, error) {
	if in.DeploymentID == "" {
		return nil, required("deploymentId")
	}
	r, err := e.start(ctx, nsID, WorkflowHeal, in)
	if err != nil {
		return nil, err
	}

	var d model.NodeDeployment
	step, ok := r.call(ctx, "read the deployment", toolGetDeployment, map[string]string{"id": in.DeploymentID}, &d)
	if !ok {
		r.add("run the deployment request again", toolCreateDeployment, nil, StepSkipped, r.skipMessage())
		r.add(waitStepName, toolGetDeployment, nil, StepSkipped, r.skipMessage())
		return r.finish(), nil
	}
	step.Message = outcome(&d)
	switch d.Status {
	case model.NodeDeploySucceeded:
		r.message = fmt.Sprintf("deployment %s succeeded on every node; nothing to heal", d.ID)
		return r.finish(), nil
	case model.NodeDeployPending, model.NodeDeployRunning:
		r.fail(step, fmt.Sprintf("deployment %s is still %s; heal it after it ends", d.ID, d.Status))
		r.add("run the deployment request again", toolCreateDeployment, d.Request, StepSkipped, r.skipMessage())
		r.add(waitStepName, toolGetDeployment, nil, StepSkipped, r.skipMessage())
		return r.finish(), nil
	}

	if again, ok := r.createAndWait(ctx, d.Request); ok {
		r.message = fmt.Sprintf("deployment %s was %s and succeeded on every node when run again as %s",
			d.ID, d.Status, again.ID)
	} else {
		r.note = fmt.Sprintf("deployment %s was %s before", d.ID, d.Status)
	}
	return r.finish(), nil
}

// Control applies a lifecycle action to every node of a deployment. Nodes the
// dry-run gate held make the step blocked; a node that failed fails it.
func (e *Engine) Control(ctx context.Context, nsID string, in ControlInput) (*Run, error) {
	switch {
	case in.DeploymentID == "":
		return nil, required("deploymentId")
	case !controlActions[in.Action]:
		return nil, fmt.Errorf("%w: action must be suspend, resume, reboot or terminate", ErrInvalidInput)
	}
	r, err := e.start(ctx, nsID, WorkflowControl, in)
	if err != nil {
		return nil, err
	}

	var result model.NodeControlResp
	step, ok := r.call(ctx, in.Action+" the nodes of the deployment", toolControlNode,
		map[string]string{"id": in.DeploymentID, "action": in.Action}, &result)
	if !ok {
		return r.finish(), nil
	}
	done, blocked := 0, 0
	var failed []string
	for _, n := range result.Nodes {
		switch n.Status {
		case controlDone:
			done++
		case controlBlocked:
			blocked++
		default:
			failed = append(failed, n.Target.Key()+": "+n.Message)
		}
	}
	switch {
	case len(failed) > 0:
		r.fail(step, fmt.Sprintf("%s failed on %d of %d nodes: %s", in.Action, len(failed), len(result.Nodes),
			strings.Join(failed, "; ")))
	case blocked > 0:
		step.Status = StepBlocked
		step.Message = fmt.Sprintf("%s held on %d of %d nodes: %s", in.Action, blocked, len(result.Nodes),
			result.Nodes[0].Message)
	default:
		step.Message = fmt.Sprintf("%s sent to %d of %d nodes", in.Action, done, len(result.Nodes))
	}
	return r.finish(), nil
}

// Status reads the deployment record and the end of the task output of every
// node. The two are independent reads, so one failing does not keep the other
// from running.
func (e *Engine) Status(ctx context.Context, nsID string, in StatusInput) (*Run, error) {
	if in.DeploymentID == "" {
		return nil, required("deploymentId")
	}
	r, err := e.start(ctx, nsID, WorkflowStatus, in)
	if err != nil {
		return nil, err
	}
	input := map[string]string{"id": in.DeploymentID}
	summary := make([]string, 0, 2)

	var d model.NodeDeployment
	if step, ok := r.call(ctx, "read the deployment", toolGetDeployment, input, &d); ok {
		step.Message = outcome(&d)
		summary = append(summary, step.Message)
	} else {
		summary = append(summary, "deployment: "+step.Error)
	}

	var logs model.NodeDeploymentLogs
	if step, ok := r.call(ctx, "read the task output", toolGetDeploymentLogs, input, &logs); ok {
		step.Message = logTail(&logs, statusLogLines)
		summary = append(summary, "output: "+step.Message)
	} else {
		summary = append(summary, "output: "+step.Error)
	}

	r.message = strings.Join(summary, "; ")
	return r.finish(), nil
}

// logTail gives, for every node, the playbook of its last step that has
// output and the last n lines of that output.
func logTail(logs *model.NodeDeploymentLogs, n int) string {
	parts := make([]string, 0, len(logs.Nodes))
	for _, node := range logs.Nodes {
		part := node.Target.Key() + " " + node.Status
		for i := len(node.Steps) - 1; i >= 0; i-- {
			lines := node.Steps[i].Output
			if len(lines) == 0 {
				continue
			}
			if len(lines) > n {
				lines = lines[len(lines)-n:]
			}
			part += fmt.Sprintf(" (%s): %s", node.Steps[i].Playbook, strings.Join(lines, " | "))
			break
		}
		parts = append(parts, part)
	}
	if len(parts) == 0 {
		return "no node"
	}
	return strings.Join(parts, "; ")
}

// summarizeCatalog keeps what a caller needs to write a deployment request.
func summarizeCatalog(models []model.Model, apps []model.App) *Catalog {
	out := &Catalog{Models: make([]CatalogModel, 0, len(models)), Apps: make([]CatalogApp, 0, len(apps))}
	for _, m := range models {
		entry := CatalogModel{ID: m.ID, Name: m.Name, Tasks: m.Tasks, InputModalities: m.InputModalities,
			Artifacts: make([]CatalogArtifact, 0, len(m.Artifacts))}
		for _, a := range m.Artifacts {
			engines := make([]string, 0, len(a.Engines))
			for _, compat := range a.Engines {
				engines = append(engines, compat.Engine)
			}
			entry.Artifacts = append(entry.Artifacts, CatalogArtifact{ID: a.ID, Source: a.Source, Ref: a.Ref,
				Format: a.Format, Precision: a.Precision, SizeGiB: a.SizeGiB, Engines: engines})
		}
		out.Models = append(out.Models, entry)
	}
	for _, a := range apps {
		entry := CatalogApp{Name: a.Name, Versions: make([]int, 0, len(a.Versions))}
		for _, v := range a.Versions {
			entry.Versions = append(entry.Versions, v.Version)
			if v.Version > entry.Latest {
				entry.Latest = v.Version
			}
		}
		out.Apps = append(out.Apps, entry)
	}
	return out
}

func decodeRaw(raw json.RawMessage, out any) error {
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("unreadable result: %w", err)
	}
	return nil
}
