package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
)

// waitStepName names the step that follows a deployment until it ends.
const waitStepName = "wait for the deployment to end"

// Deploy deploys a model service or an uploaded application onto existing
// nodes. It reads what the request names, creates the deployment and follows
// it until every node ended. The model service health is checked by the
// compose-up playbook inside the deployment, so the wait step reports it.
//
// A deployment the dry-run gate holds is recorded as blocked and the wait as
// waiting. A deployment that succeeded on some nodes only fails the run.
func (e *Engine) Deploy(ctx context.Context, nsID string, in DeployInput) (*Run, error) {
	if err := in.Validate(); err != nil {
		var validation *model.ValidationError
		if errors.As(err, &validation) {
			return nil, fmt.Errorf("%w: %s", ErrInvalidInput, validation.Message)
		}
		return nil, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	r, err := e.start(ctx, nsID, WorkflowDeploy, in)
	if err != nil {
		return nil, err
	}

	if !r.readTarget(ctx, in) {
		r.add("create the deployment", toolCreateDeployment, in, StepSkipped, r.skipMessage())
		r.add(waitStepName, toolGetDeployment, nil, StepSkipped, r.skipMessage())
		return r.finish(), nil
	}
	if d, ok := r.createAndWait(ctx, in); ok {
		r.message = fmt.Sprintf("deployment %s succeeded on every node", d.ID)
	}
	return r.finish(), nil
}

// readTarget records the step that reads the model or the application the
// request names, and reports whether it is there.
func (r *runner) readTarget(ctx context.Context, in DeployInput) bool {
	if in.Type == model.NodeDeployTypeApp {
		args := map[string]any{"name": in.App.Name}
		if in.App.Version > 0 {
			args["version"] = in.App.Version
			var version model.AppVersion
			step, ok := r.call(ctx, "read application "+in.App.Name, toolGetApp, args, &version)
			if ok {
				step.Message = fmt.Sprintf("version %d, %d files", version.Version, len(version.Files))
			}
			return ok
		}
		var app model.App
		step, ok := r.call(ctx, "read application "+in.App.Name, toolGetApp, args, &app)
		if !ok {
			return false
		}
		if len(app.Versions) == 0 {
			r.fail(step, "the application has no version")
			return false
		}
		latest := app.Versions[len(app.Versions)-1]
		step.Message = fmt.Sprintf("latest version %d, %d files", latest.Version, len(latest.Files))
		return true
	}

	ms := in.ModelService
	var m model.Model
	step, ok := r.call(ctx, "read model "+ms.ModelID, toolGetModel, map[string]string{"modelId": ms.ModelID}, &m)
	if !ok {
		return false
	}
	ids := make([]string, 0, len(m.Artifacts))
	for _, artifact := range m.Artifacts {
		if artifact.ID != ms.ArtifactID {
			ids = append(ids, artifact.ID)
			continue
		}
		engines := make([]string, 0, len(artifact.Engines))
		for _, compat := range artifact.Engines {
			engines = append(engines, compat.Engine)
		}
		step.Message = fmt.Sprintf("artifact %s: %s %s (%s), engines %s",
			artifact.ID, artifact.Source, artifact.Ref, artifact.Format, strings.Join(engines, ", "))
		return true
	}
	r.fail(step, fmt.Sprintf("model %s has no artifact %s; it has %s", ms.ModelID, ms.ArtifactID, strings.Join(ids, ", ")))
	return false
}

// createAndWait records the create_deployment step for req and the step that
// follows the deployment until it ends. It returns the last record read and
// whether the deployment succeeded on every node.
func (r *runner) createAndWait(ctx context.Context, req model.NodeDeploymentReq) (*model.NodeDeployment, bool) {
	var created model.NodeDeployment
	step, ok := r.call(ctx, "create the deployment", toolCreateDeployment, req, &created)
	if !ok {
		r.add(waitStepName, toolGetDeployment, nil, StepSkipped, r.skipMessage())
		return nil, false
	}
	waitInput := map[string]string{"id": created.ID}

	switch {
	case created.Status == model.NodeDeployDryRun:
		step.Status = StepBlocked
		step.Message = fmt.Sprintf("deployment %s is a dry run: the files were placed on %d nodes and nothing ran",
			created.ID, len(created.Nodes))
		r.add(waitStepName, toolGetDeployment, waitInput, StepWaiting,
			"waits for create_deployment to run (blocked by dry run)")
		return &created, false
	case created.DryRun:
		// A dry run that could not place the files on every node.
		r.fail(step, "dry run: "+outcome(&created))
		r.add(waitStepName, toolGetDeployment, waitInput, StepSkipped, r.skipMessage())
		return &created, false
	}
	step.Message = fmt.Sprintf("deployment %s accepted for %d nodes, status %s", created.ID, len(created.Nodes), created.Status)
	return r.waitDeployment(ctx, created.ID)
}

// ended reports whether a deployment status is final.
func ended(status string) bool {
	switch status {
	case model.NodeDeployPending, model.NodeDeployRunning:
		return false
	}
	return true
}

// waitDeployment records one get_deployment step that asks until the
// deployment ended, at most once per wait interval and at most as many times
// as the wait timeout holds intervals. A failed read is asked again too. It
// returns the last record read and whether every node succeeded.
func (r *runner) waitDeployment(ctx context.Context, id string) (*model.NodeDeployment, bool) {
	input := map[string]string{"id": id}
	step := r.add(waitStepName, toolGetDeployment, input, StepSucceeded, "")
	started := time.Now().UTC()
	step.StartedAt = &started

	interval := r.engine.waitInterval
	maxPolls := int(r.engine.waitTimeout/interval) + 1
	var last *model.NodeDeployment
	var lastErr error
	polls := 0
poll:
	for polls < maxPolls {
		if polls > 0 {
			select {
			case <-ctx.Done():
				lastErr = ctx.Err()
				break poll
			case <-time.After(interval):
			}
		}
		polls++
		raw, err := r.invoke(ctx, toolGetDeployment, input)
		step.Output = raw
		if err != nil {
			lastErr = err
			continue
		}
		var d model.NodeDeployment
		if err := decodeRaw(raw, &d); err != nil {
			lastErr = err
			continue
		}
		last, lastErr = &d, nil
		if ended(d.Status) {
			break
		}
	}
	endedAt := time.Now().UTC()
	step.EndedAt = &endedAt
	took := endedAt.Sub(started).Round(time.Second)

	switch {
	case last != nil && last.Status == model.NodeDeploySucceeded:
		step.Message = fmt.Sprintf("%s after %d reads over %s", outcome(last), polls, took)
		return last, true
	case last != nil && ended(last.Status):
		r.fail(step, outcome(last))
		return last, false
	case lastErr != nil:
		r.fail(step, fmt.Sprintf("not ended after %d reads over %s: %v", polls, took, lastErr))
	default:
		r.fail(step, fmt.Sprintf("still %s after %d reads over %s; follow it with %s",
			last.Status, polls, took, ToolStatus))
	}
	return last, false
}

// outcome says how a deployment ended, naming every node that did not
// succeed and the service URL of every node that did.
func outcome(d *model.NodeDeployment) string {
	succeeded := 0
	var good, bad []string
	for _, n := range d.Nodes {
		key := n.Target.Key()
		switch n.Status {
		case model.NodeDeploySucceeded:
			succeeded++
			if n.ServiceURL != "" {
				good = append(good, key+" "+n.ServiceURL)
			}
		case model.NodeDeployDryRun:
		default:
			reason := n.Status
			if n.Error != "" {
				reason += ": " + n.Error
			}
			bad = append(bad, key+" "+reason)
		}
	}
	message := fmt.Sprintf("deployment %s %s, %d of %d nodes succeeded", d.ID, d.Status, succeeded, len(d.Nodes))
	if len(bad) > 0 {
		message += "; not succeeded on " + strings.Join(bad, "; ")
	}
	if len(good) > 0 {
		message += "; serving at " + strings.Join(good, ", ")
	}
	if d.Error != "" {
		message += "; " + d.Error
	}
	return message
}
