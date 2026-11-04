package nodedeploy

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/semaphore"
)

// Phases of a node, recorded in nodes[].phase.
const (
	PhaseReadingNode     = "reading-node"
	PhaseStaging         = "staging"
	PhaseCheckingSSH     = "checking-ssh"
	PhaseWaitingNodeLock = "waiting-node-lock"
	PhaseOpeningPorts    = "opening-ports"
	PhaseDone            = "done"
	// A playbook phase is the playbook name without .yml: install-docker,
	// compose-up, copy-and-run or compose-down.
)

// taskLinePattern finds the Ansible task name in a log line such as
// "TASK [Wait until the service answers] ******".
var taskLinePattern = regexp.MustCompile(`TASK \[(.+)\]\s*\**\s*$`)

func playbookPhase(playbook string) string {
	return strings.TrimSuffix(strings.TrimSuffix(playbook, ".yml"), ".yaml")
}

func setPhase(n *model.NodeDeployResult, phase string, at time.Time) {
	if n.Phase == phase {
		return
	}
	n.Phase = phase
	n.PhaseStartedAt = &at
}

func (s *Service) setPhase(id string, idx int, phase string) {
	now := time.Now().UTC()
	s.updateNode(id, idx, func(n *model.NodeDeployResult) { setPhase(n, phase, now) })
}

// currentTask is the Ansible task of the last TASK line of output, or "".
func currentTask(output []string) string {
	for i := len(output) - 1; i >= 0; i-- {
		if m := taskLinePattern.FindStringSubmatch(strings.TrimSpace(output[i])); m != nil {
			return strings.TrimSpace(m[1])
		}
	}
	return ""
}

// inProgress reports whether the service still works on a deployment or node
// in status.
func inProgress(status string) bool {
	switch status {
	case model.NodeDeployPending, model.NodeDeployDeploying, model.NodeDeployRemoving:
		return true
	}
	return false
}

func isRemovalStatus(status string) bool {
	switch status {
	case model.NodeDeployRemoving, model.NodeDeployRemoved, model.NodeDeployRemoveFailed:
		return true
	}
	return false
}

// Progress reads the short progress of one deployment.
func (s *Service) Progress(ctx context.Context, id string) (*model.NodeDeploymentProgress, error) {
	d, ok := s.store.Get(id)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return progressOf(d, time.Now().UTC()), nil
}

func progressOf(d *model.NodeDeployment, now time.Time) *model.NodeDeploymentProgress {
	out := &model.NodeDeploymentProgress{
		ID: d.ID, Type: d.Type, Status: d.Status, Error: d.Error,
		Done:      !inProgress(d.Status),
		Succeeded: d.Status == model.NodeDeployRunning || d.Status == model.NodeDeployDryRun,
		CreatedAt: d.CreatedAt, FinishedAt: d.FinishedAt, RemovedAt: d.RemovedAt,
		Nodes: make([]model.NodeProgress, 0, len(d.Nodes)),
	}
	end := now
	switch {
	case !out.Done:
	case isRemovalStatus(d.Status) && d.RemovedAt != nil:
		end = *d.RemovedAt
	case d.FinishedAt != nil:
		end = *d.FinishedAt
	}
	if secs := int64(end.Sub(d.CreatedAt) / time.Second); secs > 0 {
		out.ElapsedSeconds = secs
	}

	out.Count.Total = len(d.Nodes)
	for i := range d.Nodes {
		n := &d.Nodes[i]
		switch n.Status {
		case model.NodeDeployPending, model.NodeDeployDeploying, model.NodeDeployRemoving:
			out.Count.InProgress++
		case model.NodeDeployRunning, model.NodeDeployDryRun, model.NodeDeployRemoved:
			out.Count.Succeeded++
		default:
			out.Count.Failed++
		}
		np := model.NodeProgress{
			Target: n.Target, Status: n.Status, Done: !inProgress(n.Status),
			Phase: n.Phase, PhaseStartedAt: n.PhaseStartedAt, Error: n.Error,
			ServiceURL: n.ServiceURL, Ports: n.Ports,
		}
		if n.Node != nil {
			np.Host = n.Node.Host
		}
		if len(n.Steps) > 0 {
			last := n.Steps[len(n.Steps)-1]
			last.Output = nil
			np.Step = &last
		}
		np.StepsDone, np.StepsTotal = countSteps(d, n)
		out.Nodes = append(out.Nodes, np)
	}
	return out
}

// countSteps counts the playbooks of the deployment or of its removal that
// succeeded on n, and how many there are.
func countSteps(d *model.NodeDeployment, n *model.NodeDeployResult) (done, total int) {
	planned := map[string]bool{PlaybookInstallDocker: true, PlaybookComposeUp: true, PlaybookCopyAndRun: true}
	total = 2
	if isRemovalStatus(n.Status) {
		planned = map[string]bool{PlaybookComposeDown: true}
		total = 0
		if n.Status == model.NodeDeployRemoving {
			total = 1
		}
	}
	for _, st := range n.Steps {
		if !planned[st.Playbook] {
			continue
		}
		if st.Playbook == PlaybookComposeDown {
			total = 1
		}
		if st.Status == semaphore.StatusSuccess {
			done++
		}
	}
	if d.DryRun && !isRemovalStatus(n.Status) {
		total = 0
	}
	if done > total {
		done = total
	}
	return done, total
}
