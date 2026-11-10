package catalogsync

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/modelcat"
)

// fakeSource returns models, or err, and blocks on gate when it is set.
type fakeSource struct {
	name   string
	models []model.Model
	err    error
	gate   chan struct{}
	calls  atomic.Int32
	limit  atomic.Int32
}

// stubChecker answers the reachability check from a set of unreachable
// source names, so that no test dials out.
type stubChecker struct {
	mu          sync.Mutex
	unreachable map[string]bool
}

func (c *stubChecker) set(names ...string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.unreachable = make(map[string]bool, len(names))
	for _, n := range names {
		c.unreachable[n] = true
	}
}

func (c *stubChecker) check(ctx context.Context, src Source, opt Options) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.unreachable[src.Name()] {
		return errors.New("no route to host")
	}
	return nil
}

func newTestRunner(sources []Source, interval time.Duration) (*Runner, *stubChecker, *modelcat.Store) {
	store, _, _ := modelcat.Open("")
	r := NewRunner(store, sources, Options{Limit: 7}, interval)
	stub := &stubChecker{}
	r.SetChecker(stub.check)
	return r, stub, store
}

func (f *fakeSource) Name() string { return f.name }

func (f *fakeSource) Fetch(ctx context.Context, opt Options) ([]model.Model, error) {
	f.calls.Add(1)
	f.limit.Store(int32(opt.Limit))
	if f.gate != nil {
		select {
		case <-f.gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return f.models, f.err
}

func fakeModel(origin, id string) model.Model {
	return model.Model{ID: id, Name: id, Origin: origin, Artifacts: []model.Artifact{{
		ID: "a", Source: origin, Ref: "org/" + id, Format: "hf", Runtimes: []model.RuntimeCompat{},
	}}}
}

func waitIdle(t *testing.T, r *Runner) Status {
	t.Helper()
	for i := 0; i < 200; i++ {
		if st := r.Status(); !st.Running {
			return st
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the run did not finish within 2s")
	return Status{}
}

func TestRunRecordsResultsAndKeepsEntriesOfAFailedSource(t *testing.T) {
	ctx := context.Background()
	hf := &fakeSource{name: model.OriginHuggingFace, models: []model.Model{
		fakeModel(model.OriginHuggingFace, "hf-a"), fakeModel(model.OriginHuggingFace, "hf-b")}}
	ollama := &fakeSource{name: model.OriginOllama, models: []model.Model{fakeModel(model.OriginOllama, "ollama-a")}}
	r, _, store := newTestRunner([]Source{hf, ollama}, time.Hour)

	if st := r.Status(); !st.Enabled || st.Running || st.Interval != "1h0m0s" || len(st.Sources) != 2 ||
		st.Sources[0].Name != model.OriginHuggingFace || st.Sources[0].StartedAt != nil {
		t.Errorf("status before a run = %+v", st)
	}
	if err := r.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if hf.limit.Load() != 7 {
		t.Errorf("limit passed = %d, want 7", hf.limit.Load())
	}

	// Ollama is unreachable on the second run: its entry must not turn stale.
	ollama.models, ollama.err = nil, errors.New("registry unreachable")
	hf.models = hf.models[:1]
	if err := r.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	st := r.Status()
	if got := st.Sources[0]; got.Error != "" || got.Fetched != 1 ||
		got.Counts != (modelcat.SyncCounts{Updated: 1, Staled: 1}) || got.StartedAt == nil || got.FinishedAt == nil ||
		got.FinishedAt.Before(*got.StartedAt) {
		t.Errorf("huggingface result = %+v", got)
	}
	if got := st.Sources[1]; got.Error != "registry unreachable" || got.Counts != (modelcat.SyncCounts{}) || got.FinishedAt == nil {
		t.Errorf("ollama result = %+v", got)
	}
	if m, _ := store.Get(ctx, "ollama-a"); m == nil || m.Stale {
		t.Errorf("ollama-a = %+v, want kept and not stale after a failed fetch", m)
	}
	if m, _ := store.Get(ctx, "hf-b"); m == nil || !m.Stale {
		t.Errorf("hf-b = %+v, want stale", m)
	}
}

func TestOneRunAtATime(t *testing.T) {
	src := &fakeSource{name: model.OriginKaggle, gate: make(chan struct{})}
	r, _, _ := newTestRunner([]Source{src}, time.Hour)

	if !r.Trigger() {
		t.Fatal("first Trigger = false, want started")
	}
	if !r.Status().Running {
		t.Error("status after Trigger: running = false")
	}
	if r.Trigger() {
		t.Error("second Trigger while running = true, want refused")
	}
	if err := r.Run(context.Background()); !errors.Is(err, ErrRunning) {
		t.Errorf("Run while running = %v, want ErrRunning", err)
	}
	close(src.gate)
	waitIdle(t, r)
	if src.calls.Load() != 1 {
		t.Errorf("fetches = %d, want 1", src.calls.Load())
	}
	if !r.Trigger() {
		t.Error("Trigger after the run ended = false, want started")
	}
	waitIdle(t, r)
}

func TestStartRunsAtOnceAndOnTheInterval(t *testing.T) {
	src := &fakeSource{name: model.OriginNGC}
	r, _, _ := newTestRunner([]Source{src}, 20*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx)
	for i := 0; i < 200 && src.calls.Load() < 3; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if got := src.calls.Load(); got < 3 {
		t.Fatalf("fetches = %d, want the startup run and at least two scheduled ones", got)
	}
	waitIdle(t, r)
	settled := src.calls.Load()
	time.Sleep(60 * time.Millisecond)
	if got := src.calls.Load(); got != settled {
		t.Errorf("fetches went from %d to %d after ctx ended", settled, got)
	}
}

func TestUnreachableSourceIsSkippedAndKeepsItsEntries(t *testing.T) {
	ctx := context.Background()
	hf := &fakeSource{name: model.OriginHuggingFace, models: []model.Model{fakeModel(model.OriginHuggingFace, "hf-a")}}
	ollama := &fakeSource{name: model.OriginOllama, models: []model.Model{fakeModel(model.OriginOllama, "ollama-a")}}
	r, stub, store := newTestRunner([]Source{hf, ollama}, time.Hour)
	if err := r.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	stub.set(model.OriginOllama)
	if err := r.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	st := r.Status()
	if st.Offline {
		t.Error("offline = true with huggingface reachable")
	}
	got := st.Sources[1]
	if got.Skipped != SkippedUnreachable || got.Error != "" || got.Reachable == nil || *got.Reachable ||
		got.CheckedAt == nil || got.Counts != (modelcat.SyncCounts{}) {
		t.Errorf("ollama result = %+v, want skipped as unreachable with no error", got)
	}
	if ollama.calls.Load() != 1 {
		t.Errorf("ollama fetches = %d, want 1: an unreachable source is not fetched", ollama.calls.Load())
	}
	if hfResult := st.Sources[0]; hfResult.Reachable == nil || !*hfResult.Reachable || hfResult.Skipped != "" {
		t.Errorf("huggingface result = %+v, want reachable", hfResult)
	}
	if m, _ := store.Get(ctx, "ollama-a"); m == nil || m.Stale {
		t.Errorf("ollama-a = %+v, want kept and not stale", m)
	}

	stub.set(model.OriginHuggingFace, model.OriginOllama)
	if err := r.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !r.Status().Offline {
		t.Error("offline = false with no source reachable")
	}
}

func TestOfflineBackoff(t *testing.T) {
	r, _, _ := newTestRunner(nil, 30*time.Minute)
	for runs, want := range []time.Duration{30 * time.Minute, 5 * time.Minute, 15 * time.Minute, 30 * time.Minute, 30 * time.Minute} {
		if got := r.nextWait(runs); got != want {
			t.Errorf("nextWait(%d) = %s, want %s", runs, got, want)
		}
	}
	r.interval = 24 * time.Hour
	if got := r.nextWait(7); got != time.Hour {
		t.Errorf("nextWait(7) = %s, want 1h", got)
	}
}

func TestScheduleRetriesSoonerWhileOffline(t *testing.T) {
	src := &fakeSource{name: model.OriginNGC}
	r, stub, _ := newTestRunner([]Source{src}, time.Hour)
	r.backoff = []time.Duration{10 * time.Millisecond}
	stub.set(model.OriginNGC)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.Start(ctx)

	var st Status
	for i := 0; i < 200; i++ {
		st = r.Status()
		if st.Sources[0].CheckedAt != nil && st.NextRunAt != nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !st.Offline || st.NextRunAt == nil || time.Until(*st.NextRunAt) > time.Minute {
		t.Fatalf("status while offline = %+v, want a retry well before the 1h interval", st)
	}

	// Back online, the next retry fetches and the schedule returns to the
	// interval.
	stub.set()
	for i := 0; i < 200 && src.calls.Load() == 0; i++ {
		time.Sleep(5 * time.Millisecond)
	}
	if src.calls.Load() == 0 {
		t.Fatal("the source was not fetched after it became reachable")
	}
	for i := 0; i < 200; i++ {
		st = r.Status()
		if !st.Offline && st.NextRunAt != nil && time.Until(*st.NextRunAt) > 30*time.Minute {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Errorf("status back online = %+v, want the next run about 1h away", st)
}

// probeSource names its probe URL, as a source with a host of its own does.
type probeSource struct {
	fakeSource
	url string
}

func (p *probeSource) ProbeURL() string { return p.url }

func TestCheckReachable(t *testing.T) {
	var method string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		method = req.Method
		w.WriteHeader(http.StatusMethodNotAllowed)
	}))
	opt := Options{Client: srv.Client()}
	up := &probeSource{fakeSource: fakeSource{name: model.OriginKaggle}, url: srv.URL}
	if err := CheckReachable(context.Background(), up, opt); err != nil {
		t.Errorf("CheckReachable on a server answering 405 = %v, want reachable", err)
	}
	if method != http.MethodHead {
		t.Errorf("method = %q, want HEAD", method)
	}
	srv.Close()
	if err := CheckReachable(context.Background(), up, opt); err == nil {
		t.Error("CheckReachable on a closed server = nil, want unreachable")
	}
	unknown := &fakeSource{name: "local"}
	if err := CheckReachable(context.Background(), unknown, opt); err != nil {
		t.Errorf("CheckReachable on a source with no probe URL = %v, want reachable", err)
	}
}

type panicSource struct{}

func (panicSource) Name() string { return model.OriginKaggle }
func (panicSource) Fetch(context.Context, Options) ([]model.Model, error) {
	panic("unexpected response")
}

func TestSafeFetchTurnsAPanicIntoAnError(t *testing.T) {
	models, err := safeFetch(context.Background(), panicSource{}, Options{})
	if err == nil || models != nil {
		t.Fatalf("safeFetch = %v, %v; want nil and an error", models, err)
	}
	if want := "source kaggle panicked: unexpected response"; err.Error() != want {
		t.Fatalf("error = %q, want %q", err, want)
	}
}

func TestUnreachableSourceRecordsWhy(t *testing.T) {
	r, _, _ := newTestRunner([]Source{&fakeSource{name: model.OriginNGC}}, time.Hour)
	r.SetChecker(func(context.Context, Source, Options) error {
		return errors.New("x509: certificate signed by unknown authority")
	})
	if err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := r.Status().Sources[0]
	if got.Skipped != SkippedUnreachable || got.Unreachable != "x509: certificate signed by unknown authority" {
		t.Fatalf("result = %+v, want skipped unreachable with the check error", got)
	}
}

// TestRunRemovesEntriesOfSourcesNotConfigured deletes the entries of sync
// origins left out of the configured sources, counts them by origin and
// leaves seed, manual and configured sources alone, a failing or unreachable
// configured source included.
func TestRunRemovesEntriesOfSourcesNotConfigured(t *testing.T) {
	hf := &fakeSource{name: model.OriginHuggingFace, err: errors.New("503")}
	ollama := &fakeSource{name: model.OriginOllama, models: []model.Model{fakeModel(model.OriginOllama, "ollama-a")}}
	r, stub, store := newTestRunner([]Source{hf, ollama}, time.Hour)
	ctx := context.Background()

	for _, m := range []model.Model{fakeModel(model.OriginSeed, "seed-a"), fakeModel(model.OriginManual, "manual-a")} {
		if _, err := store.Register(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	at := time.Now()
	for origin, ids := range map[string][]string{
		model.OriginHuggingFace:       {"hf-a"},
		model.OriginOllama:            {"ollama-old"},
		model.OriginKaggle:            {"kaggle-a", "kaggle-b"},
		model.OriginDockerModelRunner: {"dmr-a"},
	} {
		var models []model.Model
		for _, id := range ids {
			models = append(models, fakeModel(origin, id))
		}
		if _, err := store.ApplySync(ctx, origin, models, at); err != nil {
			t.Fatal(err)
		}
	}

	// huggingface fails its fetch on this run: it keeps its entry.
	if err := r.Run(ctx); err != nil {
		t.Fatal(err)
	}
	st := r.Status()
	if len(st.Removed) != 2 || st.Removed[model.OriginKaggle] != 2 || st.Removed[model.OriginDockerModelRunner] != 1 {
		t.Errorf("removed = %v, want kaggle 2 and docker-model-runner 1", st.Removed)
	}
	var ids []string
	for _, m := range store.List(ctx, modelcat.Filter{}) {
		ids = append(ids, m.ID)
	}
	if got := strings.Join(ids, ","); got != "hf-a,manual-a,ollama-a,ollama-old,seed-a" {
		t.Errorf("left = %s", got)
	}

	// ollama is unreachable on the next run: it keeps its entries, and a
	// run that removes nothing reports no counts.
	stub.set(model.OriginOllama)
	if err := r.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if st := r.Status(); st.Removed != nil {
		t.Errorf("removed on a run with nothing to remove = %v, want absent", st.Removed)
	}
	if n := len(store.List(ctx, modelcat.Filter{Origin: model.OriginOllama})); n != 2 {
		t.Errorf("ollama entries after an unreachable run = %d, want 2", n)
	}
}
