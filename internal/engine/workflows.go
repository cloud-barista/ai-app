package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/tumblebug"
)

// controlActions are the lifecycle actions the AI-App control tool accepts.
var controlActions = map[string]bool{
	"suspend":   true,
	"resume":    true,
	"reboot":    true,
	"terminate": true,
}

// Heal checks whether a deployment answers and, when it does not, applies the
// application to it again once and checks again.
func (e *Engine) Heal(ctx context.Context, nsID string, in HealInput) (*Run, error) {
	if in.DeploymentID == "" {
		return nil, required("deploymentId")
	}
	r, err := e.start(ctx, nsID, WorkflowHeal, in)
	if err != nil {
		return nil, err
	}

	input := map[string]string{"infraId": in.DeploymentID}
	if in.AppID != "" {
		input["appId"] = in.AppID
	}
	var health model.ServingHealth
	step, ok := r.call(ctx, "check the deployment answers", "check_serving_health", input, &health)
	if !ok {
		return r.finish(), nil
	}
	step.Message = health.Message
	if health.Ready {
		r.message = "the deployment answers on every node; nothing to heal"
		return r.finish(), nil
	}

	appID := in.AppID
	if appID == "" {
		appID = health.AppID
	}
	updateInput := map[string]string{"infraId": in.DeploymentID, "appId": appID}
	var update model.ProvisionResult
	step, ok = r.call(ctx, "apply the application again", "update_deployment", updateInput, &update)
	if !ok {
		r.add("check the deployment answers after the update", "check_serving_health", input, StepSkipped, r.skipMessage())
		return r.finish(), nil
	}
	step.Message = update.Message
	if update.DryRun {
		step.Status = StepBlocked
		r.add("check the deployment answers after the update", "check_serving_health", input, StepWaiting,
			"waits for update_deployment to run (blocked by dry run)")
		return r.finish(), nil
	}

	if r.waitHealthy(ctx, "check the deployment answers after the update", in.DeploymentID, appID) {
		r.message = "the deployment did not answer and answers after one update"
	}
	return r.finish(), nil
}

// Control applies a lifecycle action to a deployment.
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

	var result model.ControlResult
	step, ok := r.call(ctx, in.Action+" the deployment", "control_deployment",
		map[string]string{"infraId": in.DeploymentID, "action": in.Action}, &result)
	if ok {
		step.Message = result.Message
		if result.DryRun {
			step.Status = StepBlocked
		}
	}
	return r.finish(), nil
}

// Status reads the node status, the serving health and the accelerator of a
// deployment. The three are independent reads, so one failing does not keep
// the others from running.
func (e *Engine) Status(ctx context.Context, nsID string, in StatusInput) (*Run, error) {
	if in.DeploymentID == "" {
		return nil, required("deploymentId")
	}
	r, err := e.start(ctx, nsID, WorkflowStatus, in)
	if err != nil {
		return nil, err
	}

	withApp := map[string]string{"infraId": in.DeploymentID}
	if in.AppID != "" {
		withApp["appId"] = in.AppID
	}
	summary := make([]string, 0, 3)

	var status tumblebug.InfraStatusView
	if step, ok := r.call(ctx, "read the node status", "get_deployment_status",
		map[string]string{"infraId": in.DeploymentID}, &status); ok {
		step.Message = fmt.Sprintf("status %s, %d of %d nodes running",
			status.Status, status.StatusCount.CountRunning, status.StatusCount.CountTotal)
		summary = append(summary, "nodes: "+step.Message)
	} else {
		summary = append(summary, "nodes: "+step.Error)
	}

	var health model.ServingHealth
	if step, ok := r.call(ctx, "check the deployment answers", "check_serving_health", withApp, &health); ok {
		step.Message = health.Message
		summary = append(summary, fmt.Sprintf("serving: ready=%t, %s", health.Ready, health.Message))
	} else {
		summary = append(summary, "serving: "+step.Error)
	}

	var accelerator model.AcceleratorReport
	if step, ok := r.call(ctx, "probe the accelerator", "probe_accelerator", withApp, &accelerator); ok {
		step.Message = accelerator.Message
		summary = append(summary, "accelerator: "+accelerator.Message)
	} else {
		summary = append(summary, "accelerator: "+step.Error)
	}

	r.message = strings.Join(summary, "; ")
	return r.finish(), nil
}

func decodeRaw(raw json.RawMessage, out any) error {
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("unreadable result: %w", err)
	}
	return nil
}
