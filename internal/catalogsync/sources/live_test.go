package sources

import (
	"context"
	"net/http"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/ish-hcc/ai-agent-proto/internal/catalogsync"
)

// TestLiveSources hits the real sources with Limit 3, or with
// AIAPP_LIVE_SOURCES_LIMIT when set. It runs only with AIAPP_LIVE_SOURCES=1:
//
//	AIAPP_LIVE_SOURCES=1 go test ./internal/catalogsync/sources -run TestLiveSources -v
func TestLiveSources(t *testing.T) {
	if os.Getenv("AIAPP_LIVE_SOURCES") != "1" {
		t.Skip("set AIAPP_LIVE_SOURCES=1 to hit the real sources")
	}
	srcs, err := New(Names())
	if err != nil {
		t.Fatal(err)
	}
	limit := 3
	if v := os.Getenv("AIAPP_LIVE_SOURCES_LIMIT"); v != "" {
		if limit, err = strconv.Atoi(v); err != nil || limit <= 0 {
			t.Fatalf("AIAPP_LIVE_SOURCES_LIMIT=%q is not a positive number", v)
		}
	}
	client := &http.Client{Timeout: 60 * time.Second}
	for _, src := range srcs {
		t.Run(src.Name(), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			start := time.Now()
			models, err := src.Fetch(ctx, catalogsync.Options{Limit: limit, Client: client})
			took := time.Since(start)
			if err != nil {
				t.Fatal(err)
			}
			mustValid(t, src.Name(), models)
			artifacts, deployable := 0, 0
			for _, m := range models {
				t.Logf("  %s (%s) tasks=%v artifacts=%d description=%q", m.ID, m.Name, m.Tasks, len(m.Artifacts), m.Description)
				for _, a := range m.Artifacts {
					artifacts++
					if len(a.Runtimes) > 0 {
						deployable++
					}
					t.Logf("    %s parameters=%q sizeGiB=%v runtimes=%d", a.ID, a.Parameters, a.SizeGiB, len(a.Runtimes))
				}
			}
			t.Logf("%s: limit %d, %d models, %d artifacts, %d with a runtime, %s", src.Name(), limit, len(models), artifacts, deployable, took.Round(time.Millisecond))
			if len(models) == 0 {
				t.Errorf("%s returned no models", src.Name())
			}
		})
	}
}
