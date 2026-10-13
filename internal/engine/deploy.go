package engine

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/tumblebug"
)

const (
	// appIDLabel is the label the AI-App service puts on every deployment it
	// creates, naming the application. It is how the AI-App service itself
	// finds a running model service for an application, so the engine reads
	// the same label to decide whether one has to be deployed first.
	appIDLabel = "ai-mcmp.app-id"
	// statusRunning is the CB-Tumblebug status word of a serving deployment or
	// node. A deployment status carries counts after it, so it is matched as a
	// substring there.
	statusRunning = "Running"
)

// deployArgs is the input of plan_deployment, review_deployment and
// deploy_ai_app.
type deployArgs struct {
	AppID     string `json:"appId"`
	InfraName string `json:"infraName"`
	SpecID    string `json:"specId,omitempty"`
	ImageID   string `json:"imageId,omitempty"`
}

// target is one deployment the deploy workflow makes.
type target struct {
	appID   string
	kind    model.AppKind
	name    string
	specID  string
	imageID string
	// needs are the model services deployed earlier in this run that this
	// target cannot be planned without.
	needs []string
}

func (t target) label() string {
	return fmt.Sprintf("%s %s", t.kind, t.appID)
}

// Deploy deploys an AI application. For an application, every model service
// it requires that is not running in the namespace is deployed first, under a
// name derived from DeploymentName. Each deployment is planned, reviewed,
// deployed and, unless the dry-run gate held the deployment, checked until it
// answers.
func (e *Engine) Deploy(ctx context.Context, nsID string, in DeployInput) (*Run, error) {
	switch {
	case in.AppID == "":
		return nil, required("appId")
	case in.DeploymentName == "":
		return nil, required("deploymentName")
	}
	r, err := e.start(ctx, nsID, WorkflowDeploy, in)
	if err != nil {
		return nil, err
	}

	var app model.AppSpec
	if _, ok := r.call(ctx, "read application "+in.AppID, "get_ai_app", map[string]string{"appId": in.AppID}, &app); !ok {
		return r.finish(), nil
	}

	targets := []target{}
	var needs []string
	if app.EffectiveKind() == model.KindApplication && len(app.Requires) > 0 {
		var list struct {
			Deployments []tumblebug.InfraInfo `json:"deployments"`
		}
		step, ok := r.call(ctx, "find running model services", "list_deployments", map[string]string{}, &list)
		if !ok {
			return r.finish(), nil
		}
		notes := make([]string, 0, len(app.Requires))
		for _, req := range app.Requires {
			if infraID := runningDeployment(list.Deployments, req.AppID); infraID != "" {
				notes = append(notes, fmt.Sprintf("model service %s runs as %s", req.AppID, infraID))
				continue
			}
			name := in.DeploymentName + "-" + req.AppID
			notes = append(notes, fmt.Sprintf("model service %s is not running, deploying it first as %s", req.AppID, name))
			targets = append(targets, target{appID: req.AppID, kind: model.KindModelService, name: name})
			needs = append(needs, req.AppID)
		}
		step.Message = strings.Join(notes, "; ")
	}
	targets = append(targets, target{
		appID:   app.ID,
		kind:    app.EffectiveKind(),
		name:    in.DeploymentName,
		specID:  in.SpecID,
		imageID: in.ImageID,
		needs:   needs,
	})

	outcome := map[string]StepStatus{}
	for _, t := range targets {
		outcome[t.appID] = e.deployTarget(ctx, r, t, outcome)
	}
	return r.finish(), nil
}

// deployTarget runs the steps of one deployment and reports how it ended:
// succeeded, failed, blocked, waiting or skipped.
func (e *Engine) deployTarget(ctx context.Context, r *runner, t target, outcome map[string]StepStatus) StepStatus {
	args := deployArgs{AppID: t.appID, InfraName: t.name, SpecID: t.specID, ImageID: t.imageID}
	label := t.label()
	pending := []struct{ name, tool string }{
		{"plan " + label, "plan_deployment"},
		{"review " + label, "review_deployment"},
		{"deploy " + label, "deploy_ai_app"},
	}

	if r.failed {
		for _, p := range pending {
			r.add(p.name, p.tool, args, StepSkipped, r.skipMessage())
		}
		return StepSkipped
	}

	var waitsFor []string
	for _, need := range t.needs {
		if status := outcome[need]; status == StepBlocked || status == StepWaiting {
			waitsFor = append(waitsFor, need)
		}
	}
	if len(waitsFor) > 0 {
		message := fmt.Sprintf("waits for model service %s to be running (blocked by dry run)", strings.Join(waitsFor, ", "))
		for _, p := range pending {
			r.add(p.name, p.tool, args, StepWaiting, message)
		}
		return StepWaiting
	}

	var plan model.DeploymentPlan
	step, ok := r.call(ctx, pending[0].name, pending[0].tool, args, &plan)
	if !ok {
		r.skipRest(pending[1:], args)
		return StepFailed
	}
	// The rest of this deployment uses what the plan chose, so a recommender
	// that would answer differently the next time cannot make the review and
	// the deployment disagree with the plan.
	args.SpecID, args.ImageID = plan.SpecID, plan.ImageID
	step.Message = fmt.Sprintf("spec %s (%s), image %s (%s)", plan.SpecID, plan.SpecSource, plan.ImageID, plan.ImageSource)

	var reviewed struct {
		Review *model.ReviewSummary `json:"review"`
	}
	step, ok = r.call(ctx, pending[1].name, pending[1].tool, args, &reviewed)
	if !ok {
		r.skipRest(pending[2:], args)
		return StepFailed
	}
	if reviewed.Review == nil || !reviewed.Review.CreationViable {
		reason := "the review returned no verdict"
		if reviewed.Review != nil {
			reason = fmt.Sprintf("the review says creation is not viable: %s %s",
				reviewed.Review.OverallStatus, reviewed.Review.OverallMessage)
		}
		r.fail(step, reason)
		r.skipRest(pending[2:], args)
		return StepFailed
	}
	step.Message = fmt.Sprintf("creation viable, estimated cost %s", reviewed.Review.EstimatedCost)

	var result model.DeploymentResult
	step, ok = r.call(ctx, pending[2].name, pending[2].tool, args, &result)
	if !ok {
		return StepFailed
	}
	if result.DryRun {
		step.Status = StepBlocked
		step.Message = result.Message
		return StepBlocked
	}
	step.Message = result.Message

	if !r.waitHealthy(ctx, "check "+label+" answers", t.name, t.appID) {
		return StepFailed
	}
	return StepSucceeded
}

// skipRest records the steps of pending as skipped after a failure.
func (r *runner) skipRest(pending []struct{ name, tool string }, args deployArgs) {
	for _, p := range pending {
		r.add(p.name, p.tool, args, StepSkipped, r.skipMessage())
	}
}

// waitHealthy records one check_serving_health step that asks until the
// deployment answers on every node or the health timeout passes. A call that
// fails is asked again too: right after provisioning a node may not take
// remote commands yet. It reports whether the deployment answered.
func (r *runner) waitHealthy(ctx context.Context, name, infraID, appID string) bool {
	input := map[string]string{"infraId": infraID}
	if appID != "" {
		input["appId"] = appID
	}
	step := r.add(name, "check_serving_health", input, StepSucceeded, "")
	started := time.Now().UTC()
	step.StartedAt = &started

	deadline := started.Add(r.engine.healthTimeout)
	attempts := 0
	var lastErr error
	for {
		attempts++
		raw, err := r.invoke(ctx, "check_serving_health", input)
		step.Output = raw
		lastErr = err
		if err == nil {
			var health model.ServingHealth
			if derr := decodeRaw(raw, &health); derr != nil {
				lastErr = derr
			} else if health.Ready {
				ended := time.Now().UTC()
				step.EndedAt = &ended
				step.Message = fmt.Sprintf("ready after %d checks: %s", attempts, health.Message)
				return true
			} else {
				lastErr = fmt.Errorf("not ready: %s", health.Message)
			}
		}
		if !time.Now().Add(r.engine.healthInterval).Before(deadline) {
			break
		}
		select {
		case <-ctx.Done():
			lastErr = ctx.Err()
			deadline = time.Time{}
		case <-time.After(r.engine.healthInterval):
		}
		if deadline.IsZero() {
			break
		}
	}
	ended := time.Now().UTC()
	step.EndedAt = &ended
	r.fail(step, fmt.Sprintf("did not answer after %d checks over %s: %v",
		attempts, ended.Sub(started).Round(time.Second), lastErr))
	return false
}

// runningDeployment returns the ID of the first running deployment of appID
// that has a running node with a public address, the same test the AI-App
// service applies when it wires an application to its model service, or "".
func runningDeployment(infras []tumblebug.InfraInfo, appID string) string {
	for _, infra := range infras {
		if infra.Label[appIDLabel] != appID || !strings.Contains(infra.Status, statusRunning) {
			continue
		}
		for _, node := range infra.Node {
			if node.PublicIP != "" && (node.Status == "" || node.Status == statusRunning) {
				return infra.ID
			}
		}
	}
	return ""
}
