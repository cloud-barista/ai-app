package catalogsync

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/modelcat"
)

// ErrRunning is returned when a run is asked for while one is in progress.
var ErrRunning = errors.New("a catalog sync is already running")

// SkippedUnreachable is the reason recorded for a source whose host did not
// answer the reachability check.
const SkippedUnreachable = "unreachable"

// CheckTimeout bounds one reachability check.
const CheckTimeout = 5 * time.Second

// offlineBackoff is how long the schedule waits after consecutive runs that
// reached no source, each capped at the interval. The last step repeats.
var offlineBackoff = []time.Duration{5 * time.Minute, 15 * time.Minute, time.Hour}

// probeURLs are the hosts checked before each source runs, by origin. A
// source that implements Prober names its own.
var probeURLs = map[string]string{
	model.OriginHuggingFace:       "https://huggingface.co",
	model.OriginOllama:            "https://ollama.com",
	model.OriginDockerModelRunner: "https://hub.docker.com",
	model.OriginNGC:               "https://api.ngc.nvidia.com",
	model.OriginKaggle:            "https://www.kaggle.com",
}

// Prober is a Source that names the URL its reachability is checked against.
type Prober interface {
	ProbeURL() string
}

// Checker reports whether a source can be reached now. A nil error means it
// can.
type Checker func(ctx context.Context, src Source, opt Options) error

// CheckReachable is the default Checker. It sends one HEAD request to the
// source's probe URL through opt's client and counts any HTTP response, of any
// status, as reachable. A source with no known probe URL is taken as
// reachable, and its fetch reports what goes wrong.
func CheckReachable(ctx context.Context, src Source, opt Options) error {
	url := probeURLs[src.Name()]
	if p, ok := src.(Prober); ok {
		url = p.ProbeURL()
	}
	if url == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, CheckTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return fmt.Errorf("failed to build the reachability check of %s: %w", url, err)
	}
	resp, err := opt.HTTPClient().Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
}

// SourceResult is how the last run of one source went.
type SourceResult struct {
	// Name is the source, the origin its models are stored with.
	Name string `json:"name" example:"huggingface"`
	// Reachable is whether the source's host answered the check before the
	// last run, and CheckedAt is when that check was made. Both are absent
	// until the source has been checked once.
	Reachable *bool      `json:"reachable,omitempty" example:"true"`
	CheckedAt *time.Time `json:"checkedAt,omitempty" example:"2026-11-04T03:00:00Z"`
	// Skipped is why the last run did not fetch from the source:
	// "unreachable" when its host did not answer. The catalog is not touched
	// then. Empty when the source was fetched.
	Skipped string `json:"skipped,omitempty" enums:"unreachable" example:""`
	// StartedAt and FinishedAt bound the last run of the source. Both are
	// absent until the source has run once.
	StartedAt  *time.Time `json:"startedAt,omitempty" example:"2026-11-04T03:00:00Z"`
	FinishedAt *time.Time `json:"finishedAt,omitempty" example:"2026-11-04T03:00:12Z"`
	// Fetched is how many models the source returned.
	Fetched int `json:"fetched" example:"96"`
	// Counts is what the returned models did to the catalog. All zero when
	// the source was skipped or its fetch failed: the catalog is not touched
	// then.
	Counts modelcat.SyncCounts `json:"counts"`
	// Error is why the last fetch from the source failed. Empty when it did
	// not.
	Error string `json:"error,omitempty" example:"huggingface: GET /api/models: 503 Service Unavailable"`
}

// Status is the state of the catalog sync.
type Status struct {
	// Enabled is false when AIAPP_CATALOG_SYNC_ENABLED is off.
	Enabled bool `json:"enabled" example:"true"`
	// Running is true while a run is in progress.
	Running bool `json:"running" example:"false"`
	// Offline is true when the last run reached no source. The schedule then
	// retries sooner than the interval: after 5m, 15m, then every 1h, never
	// later than the interval.
	Offline bool `json:"offline" example:"false"`
	// Interval is how long the sync waits between scheduled runs while a
	// source is reachable, as a Go duration. Empty when the sync is disabled.
	Interval string `json:"interval,omitempty" example:"24h0m0s"`
	// NextRunAt is when the next scheduled run starts. Absent while a run is
	// in progress, before the schedule has started and when the sync is
	// disabled.
	NextRunAt *time.Time `json:"nextRunAt,omitempty" example:"2026-11-05T03:00:00Z"`
	// Sources holds the last result of each configured source, in the
	// configured order.
	Sources []SourceResult `json:"sources"`
}

// Runner runs the configured sources against the model catalog, once at
// startup, on a schedule and on demand, one run at a time.
type Runner struct {
	store    *modelcat.Store
	sources  []Source
	opt      Options
	interval time.Duration
	check    Checker
	backoff  []time.Duration

	mu        sync.Mutex
	running   bool
	offline   bool
	nextRunAt *time.Time
	results   []SourceResult
	// base is the context runs started by Trigger use, so that they outlive
	// the request that asked for them and end with the service.
	base context.Context
}

// NewRunner builds a Runner over store that checks each source with
// CheckReachable before fetching from it. interval must be positive.
func NewRunner(store *modelcat.Store, sources []Source, opt Options, interval time.Duration) *Runner {
	results := make([]SourceResult, len(sources))
	for i, src := range sources {
		results[i] = SourceResult{Name: src.Name()}
	}
	return &Runner{
		store:    store,
		sources:  sources,
		opt:      opt,
		interval: interval,
		check:    CheckReachable,
		backoff:  offlineBackoff,
		results:  results,
		base:     context.Background(),
	}
}

// SetChecker replaces the reachability check. Call it before Start.
func (r *Runner) SetChecker(check Checker) {
	r.mu.Lock()
	r.check = check
	r.mu.Unlock()
}

// Start runs every source once in the background and then on the schedule
// until ctx ends: every interval while some source is reachable, sooner while
// none is. A scheduled run that finds one in progress is skipped.
func (r *Runner) Start(ctx context.Context) {
	r.mu.Lock()
	r.base = ctx
	r.mu.Unlock()

	go func() {
		offlineRuns := 0
		for {
			reached, err := r.run(ctx)
			switch {
			case errors.Is(err, ErrRunning):
				log.Info().Msg("Skipped a scheduled catalog sync: one is already running")
			case reached:
				offlineRuns = 0
			default:
				offlineRuns++
			}
			wait := r.nextWait(offlineRuns)
			if offlineRuns > 0 {
				log.Warn().Str("retryIn", wait.String()).Msg("Catalog sync is offline: no source answered")
			}
			next := time.Now().UTC().Add(wait)
			r.mu.Lock()
			r.nextRunAt = &next
			r.mu.Unlock()

			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			r.mu.Lock()
			r.nextRunAt = nil
			r.mu.Unlock()
		}
	}()
}

// nextWait is how long the schedule waits after offlineRuns consecutive runs
// that reached no source.
func (r *Runner) nextWait(offlineRuns int) time.Duration {
	if offlineRuns == 0 || len(r.backoff) == 0 {
		return r.interval
	}
	step := r.backoff[min(offlineRuns, len(r.backoff))-1]
	return min(step, r.interval)
}

// Trigger starts a run in the background and reports whether it did. It
// reports false when a run is already in progress.
func (r *Runner) Trigger() bool {
	r.mu.Lock()
	if r.running {
		r.mu.Unlock()
		return false
	}
	r.running = true
	ctx := r.base
	r.mu.Unlock()

	go r.runAll(ctx)
	return true
}

// Run runs every source once, one after another, and returns when all have
// finished. It returns ErrRunning when a run is already in progress.
//
// Each source is checked for reachability first. One that does not answer is
// recorded as skipped, and one whose fetch fails is recorded with its error;
// the catalog is not touched for either, so its entries are not marked stale
// by a source that was merely unreachable.
func (r *Runner) Run(ctx context.Context) error {
	_, err := r.run(ctx)
	return err
}

func (r *Runner) run(ctx context.Context) (bool, error) {
	r.mu.Lock()
	if r.running {
		r.mu.Unlock()
		return false, ErrRunning
	}
	r.running = true
	r.mu.Unlock()

	return r.runAll(ctx), nil
}

// runAll runs every source and reports whether some source was reachable. The
// caller has set running.
func (r *Runner) runAll(ctx context.Context) bool {
	reached := false
	defer func() {
		r.mu.Lock()
		r.running = false
		r.offline = !reached
		r.mu.Unlock()
	}()

	r.mu.Lock()
	check := r.check
	r.mu.Unlock()

	log.Info().Int("sources", len(r.sources)).Int("limit", r.opt.Limit).Msg("Starting catalog sync")
	var unreachable []string
	for i, src := range r.sources {
		if ctx.Err() != nil {
			log.Info().Msg("Catalog sync stopped: the service is shutting down")
			// A run cut short says nothing about the network.
			reached = true
			return reached
		}
		result := r.runOne(ctx, src, check)
		if result.Skipped == SkippedUnreachable {
			unreachable = append(unreachable, result.Name)
		} else {
			reached = true
		}
		r.mu.Lock()
		r.results[i] = result
		r.mu.Unlock()
	}
	if len(unreachable) > 0 {
		log.Warn().Strs("sources", unreachable).Msg("Catalog sync skipped sources whose host did not answer; their entries are left as they were")
	}
	log.Info().Bool("offline", !reached).Msg("Finished catalog sync")
	return reached
}

// safeFetch runs one source's Fetch and turns a panic into an error, so that
// a source broken by an unexpected response cannot take the service down.
func safeFetch(ctx context.Context, src Source, opt Options) (models []model.Model, err error) {
	defer func() {
		if p := recover(); p != nil {
			models, err = nil, fmt.Errorf("source %s panicked: %v", src.Name(), p)
		}
	}()
	return src.Fetch(ctx, opt)
}

func (r *Runner) runOne(ctx context.Context, src Source, check Checker) SourceResult {
	started := time.Now().UTC()
	result := SourceResult{Name: src.Name(), StartedAt: &started}

	reachable := check(ctx, src, r.opt) == nil
	checked := time.Now().UTC()
	result.Reachable, result.CheckedAt = &reachable, &checked
	if !reachable {
		result.Skipped = SkippedUnreachable
		return finish(result)
	}

	models, err := safeFetch(ctx, src, r.opt)
	result.Fetched = len(models)
	if err != nil {
		result.Error = err.Error()
		log.Warn().Err(err).Str("source", result.Name).Msg("Catalog sync source failed; its entries are left as they were")
		return finish(result)
	}

	counts, err := r.store.ApplySync(ctx, src.Name(), models, started)
	if err != nil {
		result.Error = err.Error()
		log.Error().Err(err).Str("source", result.Name).Msg("Failed to apply catalog sync")
		return finish(result)
	}
	result.Counts = counts
	log.Info().Str("source", result.Name).Int("fetched", result.Fetched).Int("added", counts.Added).
		Int("updated", counts.Updated).Int("staled", counts.Staled).Int("skipped", counts.Skipped).
		Int("invalid", counts.Invalid).Msg("Catalog sync source finished")
	return finish(result)
}

// finish stamps the end of a result.
func finish(result SourceResult) SourceResult {
	finished := time.Now().UTC()
	result.FinishedAt = &finished
	return result
}

// Status reports whether a run is in progress, when the next one is due and
// the last result of every source.
func (r *Runner) Status() Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	sources := make([]SourceResult, len(r.results))
	copy(sources, r.results)
	var next *time.Time
	if r.nextRunAt != nil && !r.running {
		copied := *r.nextRunAt
		next = &copied
	}
	return Status{
		Enabled:   true,
		Running:   r.running,
		Offline:   r.offline,
		Interval:  r.interval.String(),
		NextRunAt: next,
		Sources:   sources,
	}
}
