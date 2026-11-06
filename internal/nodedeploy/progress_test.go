package nodedeploy

import (
	"context"
	"testing"
	"time"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/semaphore"
)

func TestProgressFollowsARunningPlaybookThenTheEnd(t *testing.T) {
	f := newFixture(t, false)
	f.runner.block = make(chan struct{})
	created, err := f.svc.Create(context.Background(), modelReq("vm1"))
	if err != nil {
		t.Fatal(err)
	}

	var p *model.NodeDeploymentProgress
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		p, err = f.svc.Progress(context.Background(), created.ID)
		if err != nil {
			t.Fatal(err)
		}
		if p.Nodes[0].Step != nil && p.Nodes[0].Step.Status == semaphore.StatusRunning {
			break
		}
		time.Sleep(time.Millisecond)
	}
	n := p.Nodes[0]
	if p.Done || p.Running || p.Count.InProgress != 1 || p.Count.Running != 0 || n.Done {
		t.Errorf("while running: %+v", p)
	}
	if n.Phase != "install-docker" || n.PhaseStartedAt == nil || n.StepsDone != 0 || n.StepsTotal != 2 {
		t.Errorf("node while running: phase %q steps %d/%d", n.Phase, n.StepsDone, n.StepsTotal)
	}
	if n.Step.TaskID == 0 || n.Step.TaskURL == "" || n.Step.CurrentTask != "Wait until the service answers" || n.Step.Output != nil {
		t.Errorf("running step = %+v", n.Step)
	}
	logs, _ := f.svc.Logs(context.Background(), created.ID)
	if len(logs.Nodes[0].Steps[0].Output) != 2 {
		t.Errorf("live log = %q, want the log so far", logs.Nodes[0].Steps[0].Output)
	}

	close(f.runner.block)
	waitFinished(t, f.svc, created.ID)
	p, _ = f.svc.Progress(context.Background(), created.ID)
	n = p.Nodes[0]
	if !p.Done || !p.Running || p.Status != model.NodeDeployRunning || p.Count.Running != 1 || p.Count.Failed != 0 || p.FinishedAt == nil {
		t.Errorf("after the end: %+v", p)
	}
	if n.Phase != PhaseDone || n.StepsDone != 2 || n.StepsTotal != 2 || n.Step.Playbook != PlaybookComposeUp ||
		n.Step.Status != semaphore.StatusSuccess || n.Step.CurrentTask != "" || n.ServiceURL == "" || n.Host == "" {
		t.Errorf("node after the end = %+v / step %+v", n, n.Step)
	}
}

func TestProgressKeepsThePhaseAFailedNodeStoppedIn(t *testing.T) {
	f := newFixture(t, false)
	f.runner.statuses = map[string]string{"203.0.113.11 " + PlaybookComposeUp: semaphore.StatusError}
	created, err := f.svc.Create(context.Background(), modelReq("vm1"))
	if err != nil {
		t.Fatal(err)
	}
	waitFinished(t, f.svc, created.ID)
	p, _ := f.svc.Progress(context.Background(), created.ID)
	n := p.Nodes[0]
	if !p.Done || p.Running || p.Count.Failed != 1 || p.Count.Running != 0 {
		t.Errorf("progress = %+v", p)
	}
	if n.Phase != "compose-up" || n.StepsDone != 1 || n.Error == "" || n.Step.Status != semaphore.StatusError ||
		n.Step.CurrentTask != PlaybookComposeUp {
		t.Errorf("failed node = %+v / step %+v", n, n.Step)
	}
}

func TestProgressOfARemoval(t *testing.T) {
	f := newFixture(t, false)
	created, err := f.svc.Create(context.Background(), appReq("vm1"))
	if err != nil {
		t.Fatal(err)
	}
	waitFinished(t, f.svc, created.ID)
	if _, err := f.svc.Remove(context.Background(), created.ID); err != nil {
		t.Fatal(err)
	}
	var p *model.NodeDeploymentProgress
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		p, _ = f.svc.Progress(context.Background(), created.ID)
		if p.Done {
			break
		}
		time.Sleep(time.Millisecond)
	}
	n := p.Nodes[0]
	if p.Status != model.NodeDeployRemoved || p.Running || p.Count.Removed != 1 || p.Count.Running != 0 || p.RemovedAt == nil {
		t.Errorf("removal progress = %+v", p)
	}
	if n.Phase != PhaseDone || n.StepsDone != 1 || n.StepsTotal != 1 || n.Step.Playbook != PlaybookComposeDown {
		t.Errorf("removed node = %+v / step %+v", n, n.Step)
	}
}

func TestProgressOfADryRunHasNoSteps(t *testing.T) {
	f := newFixture(t, true)
	created, err := f.svc.Create(context.Background(), appReq("vm1"))
	if err != nil {
		t.Fatal(err)
	}
	p, _ := f.svc.Progress(context.Background(), created.ID)
	if !p.Done || p.Running || p.Count.DryRun != 1 || p.Count.Running != 0 || p.Nodes[0].StepsTotal != 0 || p.Nodes[0].Phase != PhaseStaging {
		t.Errorf("dry run progress = %+v", p)
	}
}

func TestCurrentTaskReadsTheLastTaskLine(t *testing.T) {
	cases := map[string][]string{
		"":                               {"PLAY [node] ***", "ok: [node]"},
		"Wait until the service answers": {"TASK [Place the files] ***", "ok", "TASK [Wait until the service answers] *******", "FAILED - RETRYING"},
		"docker : Install the packages":  {"TASK [docker : Install the packages]"},
	}
	for want, lines := range cases {
		if got := currentTask(lines); got != want {
			t.Errorf("currentTask(%q) = %q, want %q", lines, got, want)
		}
	}
}
