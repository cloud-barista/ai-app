package nodedeploy

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/semaphore"
)

// waitRemoved polls a deployment until it leaves removing.
func waitRemoved(t *testing.T, svc *Service, id string) *model.NodeDeployment {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		d, err := svc.Get(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if d.Status != model.NodeDeployRemoving {
			return d
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("deployment %s was not taken down", id)
	return nil
}

func deployed(t *testing.T, f *fixture, req model.NodeDeploymentReq) *model.NodeDeployment {
	t.Helper()
	created, err := f.svc.Create(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	return waitFinished(t, f.svc, created.ID)
}

// downRuns returns the compose-down runs from index from on.
func downRuns(f *fixture, from int) []semaphore.RunSpec {
	f.runner.mu.Lock()
	defer f.runner.mu.Unlock()
	var out []semaphore.RunSpec
	for _, spec := range f.runner.runs[from:] {
		if spec.Playbook == PlaybookComposeDown {
			out = append(out, spec)
		}
	}
	return out
}

func TestRemoveTakesEveryNodeDown(t *testing.T) {
	f := newFixture(t, false)
	d := deployed(t, f, appReq("vm1", "vm2"))
	if d.Status != model.NodeDeploySucceeded {
		t.Fatalf("deploy = %q", d.Status)
	}
	before := f.runner.count()

	got, err := f.svc.Remove(context.Background(), d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != model.NodeDeployRemoving || got.Nodes[0].Status != model.NodeDeployRemoving {
		t.Fatalf("Remove returned %q / %q, want removing", got.Status, got.Nodes[0].Status)
	}
	done := waitRemoved(t, f.svc, d.ID)
	if done.Status != model.NodeDeployRemoved || done.RemovedAt == nil {
		t.Fatalf("status = %q removedAt %v, want removed", done.Status, done.RemovedAt)
	}
	for _, n := range done.Nodes {
		last := n.Steps[len(n.Steps)-1]
		if n.Status != model.NodeDeployRemoved || n.RemovedAt == nil || last.Playbook != PlaybookComposeDown ||
			last.Status != semaphore.StatusSuccess || len(n.Notes) != 0 {
			t.Errorf("node = %+v", n)
		}
	}
	runs := downRuns(f, before)
	if len(runs) != 2 || f.runner.count()-before != 2 {
		t.Fatalf("runs = %d (%d compose-down), want 2 compose-down only", f.runner.count()-before, len(runs))
	}
	for _, spec := range runs {
		v := spec.ExtraVars
		if v["node_dir"] != "/opt/aiapp/app-web" || v["compose_project"] != "aiapp-app-web" ||
			v["work_key"] != "app-web" || v["deployment_id"] != d.ID || v["stage_dir"] != nil ||
			spec.PrivateKey != testKey || !strings.Contains(spec.Name, "-down-") {
			t.Errorf("compose-down spec = %+v", spec)
		}
	}

	records, _ := f.archive.List(context.Background(), 0)
	var removal *model.ArchiveRecord
	for i := range records {
		if records[i].Kind == archiveKindRemove {
			removal = &records[i]
		}
	}
	if removal == nil || removal.Steps[0].Tool != "delete_deployment" || removal.Steps[0].Grade != "destructive" ||
		!removal.Steps[0].Executed || removal.Error != "" {
		t.Errorf("removal archive = %+v", removal)
	}

	// A removed deployment is returned as it is, with nothing run.
	again, err := f.svc.Remove(context.Background(), d.ID)
	if err != nil || again.Status != model.NodeDeployRemoved || f.runner.count()-before != 2 {
		t.Errorf("remove again = %v, %v, runs %d", again, err, f.runner.count()-before)
	}

	reopened, err := OpenStore(f.store)
	if err != nil {
		t.Fatal(err)
	}
	if r, ok := reopened.Get(d.ID); !ok || r.Status != model.NodeDeployRemoved {
		t.Errorf("reopened = %+v", r)
	}
}

func TestRemoveFailureIsRetriedOnTheNodesLeft(t *testing.T) {
	f := newFixture(t, false)
	d := deployed(t, f, modelReq("vm1", "vm2"))
	f.runner.statuses["203.0.113.11 "+PlaybookComposeDown] = semaphore.StatusError

	if _, err := f.svc.Remove(context.Background(), d.ID); err != nil {
		t.Fatal(err)
	}
	done := waitRemoved(t, f.svc, d.ID)
	if done.Status != model.NodeDeployRemoveFailed {
		t.Fatalf("status = %q, want remove-failed", done.Status)
	}
	if n := done.Nodes[0]; n.Status != model.NodeDeployRemoveFailed || !strings.Contains(n.Error, "compose-down.yml ended") || n.RemovedAt != nil {
		t.Errorf("vm1 = %s %q", n.Status, n.Error)
	}
	if n := done.Nodes[1]; n.Status != model.NodeDeployRemoved {
		t.Errorf("vm2 = %s", n.Status)
	}
	for _, list := range []string{model.NodeDeployRemoveFailed} {
		if got := f.svc.List(context.Background(), ListFilter{Status: list}); len(got) != 1 {
			t.Errorf("list status %s = %d", list, len(got))
		}
	}

	delete(f.runner.statuses, "203.0.113.11 "+PlaybookComposeDown)
	before := f.runner.count()
	if _, err := f.svc.Remove(context.Background(), d.ID); err != nil {
		t.Fatal(err)
	}
	done = waitRemoved(t, f.svc, d.ID)
	runs := downRuns(f, before)
	if done.Status != model.NodeDeployRemoved || len(runs) != 1 || runs[0].Host != "203.0.113.11" {
		t.Errorf("retry = %q with %d runs, want removed with vm1 only", done.Status, len(runs))
	}
	if done.Nodes[0].Error != "" {
		t.Errorf("vm1 error left after the retry: %q", done.Nodes[0].Error)
	}
}

func TestRemoveLeavesALaterDeploymentOfTheWorkKeyRunning(t *testing.T) {
	f := newFixture(t, false)
	older := deployed(t, f, appReq("vm1", "vm2"))
	// Another work key on vm2 does not replace the application there.
	deployed(t, f, modelReq("vm2"))
	time.Sleep(2 * time.Millisecond)
	newer := deployed(t, f, appReq("vm1"))
	before := f.runner.count()

	got, err := f.svc.Remove(context.Background(), older.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Nodes[0].Status != model.NodeDeployRemoved || got.Nodes[1].Status != model.NodeDeployRemoving {
		t.Errorf("at once vm1 = %q vm2 = %q, want removed and removing", got.Nodes[0].Status, got.Nodes[1].Status)
	}
	done := waitRemoved(t, f.svc, older.ID)
	vm1 := done.Nodes[0]
	if vm1.Status != model.NodeDeployRemoved || len(vm1.Notes) != 1 || !strings.Contains(vm1.Notes[0], newer.ID) ||
		vm1.Steps[len(vm1.Steps)-1].Playbook == PlaybookComposeDown {
		t.Errorf("vm1 = %+v, want removed untouched with a note naming %s", vm1, newer.ID)
	}
	runs := downRuns(f, before)
	if done.Status != model.NodeDeployRemoved || len(runs) != 1 || runs[0].Host != "198.51.100.12" ||
		runs[0].ExtraVars["compose_project"] != "aiapp-app-web" {
		t.Errorf("status %q, compose-down runs %+v, want vm2 only", done.Status, runs)
	}

	// The later deployment is live on vm1 and is taken down there.
	before = f.runner.count()
	if _, err := f.svc.Remove(context.Background(), newer.ID); err != nil {
		t.Fatal(err)
	}
	done = waitRemoved(t, f.svc, newer.ID)
	if runs := downRuns(f, before); done.Status != model.NodeDeployRemoved || len(runs) != 1 || runs[0].Host != "203.0.113.11" {
		t.Errorf("newer = %q, runs %+v", done.Status, runs)
	}
}

func TestALaterDeploymentThatNeverStartedLeavesTheOlderLive(t *testing.T) {
	f := newFixture(t, false)
	older := deployed(t, f, appReq("vm1"))
	time.Sleep(2 * time.Millisecond)
	f.runner.statuses["203.0.113.11 "+PlaybookInstallDocker] = semaphore.StatusError
	failed := deployed(t, f, appReq("vm1"))
	if failed.Status != model.NodeDeployFailed || startedService(&failed.Nodes[0]) {
		t.Fatalf("later = %q steps %+v", failed.Status, failed.Nodes[0].Steps)
	}
	before := f.runner.count()

	if _, err := f.svc.Remove(context.Background(), older.ID); err != nil {
		t.Fatal(err)
	}
	done := waitRemoved(t, f.svc, older.ID)
	if runs := downRuns(f, before); done.Status != model.NodeDeployRemoved || len(runs) != 1 {
		t.Errorf("older = %q with %d compose-down runs, want removed with 1", done.Status, len(runs))
	}

	// The failed one never started its service: nothing runs.
	before = f.runner.count()
	got, err := f.svc.Remove(context.Background(), failed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != model.NodeDeployRemoved || f.runner.count() != before ||
		len(got.Nodes[0].Notes) != 1 || got.Nodes[0].Notes[0] != noteRemoveNeverStarted {
		t.Errorf("never started = %q notes %v runs %d", got.Status, got.Nodes[0].Notes, f.runner.count()-before)
	}
}

func TestRemoveOfAFailedComposeUpStillTakesItDown(t *testing.T) {
	f := newFixture(t, false)
	f.runner.statuses["203.0.113.11 "+PlaybookComposeUp] = semaphore.StatusError
	d := deployed(t, f, modelReq("vm1"))
	if d.Status != model.NodeDeployFailed {
		t.Fatalf("deploy = %q", d.Status)
	}
	before := f.runner.count()
	got, err := f.svc.Remove(context.Background(), d.ID)
	if err != nil || got.Status != model.NodeDeployRemoving {
		t.Fatalf("Remove = %v, %v", got, err)
	}
	if done := waitRemoved(t, f.svc, d.ID); done.Status != model.NodeDeployRemoved || len(downRuns(f, before)) != 1 {
		t.Errorf("status %q, runs %d; a compose-up that failed may have left containers", done.Status, len(downRuns(f, before)))
	}
}

func TestRemoveOfADryRunDeploymentRunsNothing(t *testing.T) {
	f := newFixture(t, true)
	d, err := f.svc.Create(context.Background(), modelReq("vm1", "vm2"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := f.svc.Remove(context.Background(), d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != model.NodeDeployRemoved || got.RemovedAt == nil || f.runner.count() != 0 {
		t.Fatalf("Remove = %q, runs %d", got.Status, f.runner.count())
	}
	for _, n := range got.Nodes {
		if n.Status != model.NodeDeployRemoved || len(n.Notes) != 1 || n.Notes[0] != noteRemoveDryRunDeployment {
			t.Errorf("node = %+v", n)
		}
	}
	records, _ := f.archive.List(context.Background(), 0)
	if len(records) != 2 || records[0].Kind != archiveKindRemove || records[0].Steps[0].Executed {
		t.Errorf("archive = %+v, want a removal not executed", records)
	}
}

// storeRecord adds a finished deployment to the store as an earlier run of
// the service would have left it.
func storeRecord(t *testing.T, f *fixture, d model.NodeDeployment) {
	t.Helper()
	if err := f.svc.store.Create(d); err != nil {
		t.Fatal(err)
	}
}

func succeededApp(id string, created time.Time, nodes ...string) model.NodeDeployment {
	req := appReq(nodes...)
	req.App.Version = 2
	d := model.NodeDeployment{ID: id, Type: req.Type, Status: model.NodeDeploySucceeded, Request: req, CreatedAt: created}
	for _, n := range nodes {
		d.Nodes = append(d.Nodes, model.NodeDeployResult{
			Target: target(n), Status: model.NodeDeploySucceeded, WorkKey: "app-web", NodeDir: "/opt/aiapp/app-web",
			Steps: []model.NodeDeployStep{
				{Playbook: PlaybookInstallDocker, Status: semaphore.StatusSuccess},
				{Playbook: PlaybookCopyAndRun, Status: semaphore.StatusSuccess},
			},
		})
	}
	return d
}

func TestRemoveInADryRunServiceSendsNothingAndKeepsTheStatus(t *testing.T) {
	f := newFixture(t, true)
	storeRecord(t, f, succeededApp("real", time.Now().UTC(), "vm1"))

	got, err := f.svc.Remove(context.Background(), "real")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != model.NodeDeploySucceeded || got.Nodes[0].Status != model.NodeDeploySucceeded ||
		len(got.Nodes[0].Notes) != 1 || got.Nodes[0].Notes[0] != noteRemoveDryRunService || f.runner.count() != 0 {
		t.Errorf("dry-run service removal = %q / %+v, runs %d", got.Status, got.Nodes[0], f.runner.count())
	}
	// The note is not repeated.
	got, _ = f.svc.Remove(context.Background(), "real")
	if len(got.Nodes[0].Notes) != 1 {
		t.Errorf("notes = %v", got.Nodes[0].Notes)
	}
}

func TestRemoveOfANodeCBTumblebugNoLongerHas(t *testing.T) {
	f := newFixture(t, false)
	storeRecord(t, f, succeededApp("old", time.Now().UTC(), "gone", "vm9"))

	if _, err := f.svc.Remove(context.Background(), "old"); err != nil {
		t.Fatal(err)
	}
	done := waitRemoved(t, f.svc, "old")
	if n := done.Nodes[0]; n.Status != model.NodeDeployRemoved || len(n.Notes) != 1 || n.Notes[0] != noteRemoveNodeGone {
		t.Errorf("gone = %+v", n)
	}
	// Any other failure to read the node fails the node.
	if n := done.Nodes[1]; n.Status != model.NodeDeployRemoveFailed || n.Error == "" {
		t.Errorf("vm9 = %+v", n)
	}
	if done.Status != model.NodeDeployRemoveFailed || f.runner.count() != 0 {
		t.Errorf("status %q, runs %d", done.Status, f.runner.count())
	}
}

func TestRemoveIsRefusedWhileTheDeploymentRuns(t *testing.T) {
	f := newFixture(t, false)
	f.runner.block = make(chan struct{})
	created, err := f.svc.Create(context.Background(), appReq("vm1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Remove(context.Background(), created.ID); !errors.Is(err, ErrBusy) {
		t.Errorf("Remove while pending or running = %v, want ErrBusy", err)
	}
	close(f.runner.block)
	waitFinished(t, f.svc, created.ID)

	f.runner.mu.Lock()
	f.runner.block = make(chan struct{})
	f.runner.mu.Unlock()
	if _, err := f.svc.Remove(context.Background(), created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Remove(context.Background(), created.ID); !errors.Is(err, ErrBusy) {
		t.Errorf("Remove while removing = %v, want ErrBusy", err)
	}
	f.runner.mu.Lock()
	close(f.runner.block)
	f.runner.mu.Unlock()
	if done := waitRemoved(t, f.svc, created.ID); done.Status != model.NodeDeployRemoved {
		t.Errorf("status = %q", done.Status)
	}
	if _, err := f.svc.Remove(context.Background(), "missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing = %v", err)
	}
}

func TestShutdownDuringRemovalLeavesItRemoveFailed(t *testing.T) {
	f := newFixture(t, false)
	d := deployed(t, f, appReq("vm1"))
	f.runner.mu.Lock()
	f.runner.block = make(chan struct{})
	f.runner.mu.Unlock()
	before := f.runner.count()
	if _, err := f.svc.Remove(context.Background(), d.ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for f.runner.count() == before && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if err := f.svc.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := f.svc.Get(context.Background(), d.ID)
	if got.Status != model.NodeDeployRemoveFailed || got.Nodes[0].Status != model.NodeDeployRemoveFailed {
		t.Errorf("after shutdown = %q / %q, want remove-failed", got.Status, got.Nodes[0].Status)
	}
}

func TestOpenStoreMarksRemovingRecordsRemoveFailed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "d.json")
	body := `{"deployments":[
		{"id":"a","status":"removing","createdAt":"2026-10-29T12:00:00Z","nodes":[{"status":"removing"},{"status":"removed"}]}]}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := store.Get("a")
	if a.Status != model.NodeDeployRemoveFailed || a.Nodes[0].Status != model.NodeDeployRemoveFailed ||
		a.Nodes[0].Error == "" || a.Nodes[1].Status != model.NodeDeployRemoved || a.RemovedAt == nil {
		t.Errorf("a = %+v", a)
	}
}
