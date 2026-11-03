package sources

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/ish-hcc/ai-agent-proto/internal/catalogsync"
)

// TestLiveSources hits the real sources with Limit 3. It runs only with
// AIAPP_LIVE_SOURCES=1:
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
	client := &http.Client{Timeout: 60 * time.Second}
	for _, src := range srcs {
		t.Run(src.Name(), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			models, err := src.Fetch(ctx, catalogsync.Options{Limit: 3, Client: client})
			if err != nil {
				t.Fatal(err)
			}
			mustValid(t, src.Name(), models)
			artifacts, deployable := 0, 0
			for _, m := range models {
				for _, a := range m.Artifacts {
					artifacts++
					if len(a.Runtimes) > 0 {
						deployable++
					}
				}
				t.Logf("  %s (%s) tasks=%v artifacts=%d", m.ID, m.Name, m.Tasks, len(m.Artifacts))
			}
			t.Logf("%s: %d models, %d artifacts, %d with a runtime", src.Name(), len(models), artifacts, deployable)
			if len(models) == 0 {
				t.Errorf("%s returned no models", src.Name())
			}
		})
	}
}
