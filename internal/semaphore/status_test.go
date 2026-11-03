package semaphore

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ish-hcc/ai-agent-proto/internal/config"
)

var checkPlaybooks = []string{"install-docker.yml", "copy-and-run.yml"}

// writeCalls lists the calls of fake that would change something, login aside.
func writeCalls(fake *fakeSemaphore) []string {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	var writes []string
	for _, call := range fake.calls {
		if !strings.HasPrefix(call, http.MethodGet+" ") && call != http.MethodPost+" /api/auth/login" {
			writes = append(writes, call)
		}
	}
	return writes
}

func TestCheckNotConfigured(t *testing.T) {
	h := NewClient(config.SemaphoreConfig{Project: "aiapp"}).Check(context.Background(), checkPlaybooks)
	if h.Configured || h.Reachable || !errors.Is(h.Err, ErrNotConfigured) {
		t.Errorf("health = %+v, want not configured", h)
	}
	if len(h.Templates) != 2 || h.Templates[0].Name != "aiapp-install-docker" || h.Templates[0].ID != 0 {
		t.Errorf("templates = %+v, want both listed as missing", h.Templates)
	}
}

func TestCheckSemaphoreDown(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()

	c := NewClient(config.SemaphoreConfig{URL: url, Username: "admin", Password: "pw", Project: "aiapp"})
	h := c.Check(context.Background(), checkPlaybooks)
	if !h.Configured || h.Reachable || h.Authenticated || h.Err == nil || h.Latency != 0 {
		t.Errorf("health = %+v, want configured and unreachable", h)
	}
}

func TestCheckWrongPassword(t *testing.T) {
	fake := newFakeSemaphore()
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	wrong := "w-" + rand.Text()
	c := NewClient(config.SemaphoreConfig{URL: srv.URL, Username: "admin", Password: wrong, Project: "aiapp"})
	h := c.Check(context.Background(), checkPlaybooks)
	if !h.Reachable || h.Authenticated || h.Err == nil || !strings.Contains(h.Err.Error(), "refused the configured credentials") {
		t.Errorf("health = %+v, want reachable and not authenticated", h)
	}
	if strings.Contains(h.Err.Error(), wrong) {
		t.Errorf("error %q holds the password", h.Err)
	}
}

func TestCheckNoCredentials(t *testing.T) {
	fake := newFakeSemaphore()
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	h := NewClient(config.SemaphoreConfig{URL: srv.URL, Project: "aiapp"}).Check(context.Background(), checkPlaybooks)
	if !h.Reachable || h.Authenticated || !errors.Is(h.Err, ErrNoCredentials) {
		t.Errorf("health = %+v, want reachable without credentials", h)
	}
}

func TestCheckCreatesNothing(t *testing.T) {
	fake := newFakeSemaphore()
	c := newTestClient(t, fake, 5*time.Second)

	h := c.Check(context.Background(), checkPlaybooks)
	if h.Err != nil || !h.Reachable || !h.Authenticated || h.Version != "v2.12.17-5767d93-1741607759" {
		t.Fatalf("health = %+v, want reachable, authenticated, with the version", h)
	}
	if h.ProjectID != 0 || h.Templates[0].ID != 0 || h.Templates[1].ID != 0 {
		t.Errorf("health = %+v, want no project and no templates yet", h)
	}
	if writes := writeCalls(fake); len(writes) != 0 {
		t.Errorf("Check sent %v, want only reads", writes)
	}

	// After a run the project and the template of its playbook exist.
	if _, err := c.Run(context.Background(), testSpec()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	fake.mu.Lock()
	fake.calls = nil
	fake.mu.Unlock()

	h = c.Check(context.Background(), checkPlaybooks)
	if h.Err != nil || h.ProjectID == 0 {
		t.Fatalf("health = %+v, want the project found", h)
	}
	if h.Templates[0].ID != 0 || h.Templates[1].Name != "aiapp-copy-and-run" || h.Templates[1].ID == 0 {
		t.Errorf("templates = %+v, want only aiapp-copy-and-run found", h.Templates)
	}
	if writes := writeCalls(fake); len(writes) != 0 {
		t.Errorf("Check sent %v, want only reads", writes)
	}
}

func TestCheckWithToken(t *testing.T) {
	fake := newFakeSemaphore()
	fake.useToken = true
	c := newTestClient(t, fake, 5*time.Second)
	if h := c.Check(context.Background(), checkPlaybooks); h.Err != nil || !h.Authenticated {
		t.Errorf("health = %+v, want authenticated with the token", h)
	}
}

func TestRunLinksTheTaskPage(t *testing.T) {
	fake := newFakeSemaphore()
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	cfg := config.SemaphoreConfig{
		URL: srv.URL, Username: "admin", Password: "pw", Project: "aiapp", PlaybookDir: "/ansible",
		PollInterval: 5 * time.Millisecond, Timeout: 5 * time.Second, UIURL: "http://203.0.113.5:3001/",
	}

	result, err := NewClient(cfg).Run(context.Background(), testSpec())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	projectID := 0
	fake.mu.Lock()
	for id := range fake.projects {
		projectID = id
	}
	fake.mu.Unlock()
	want := fmt.Sprintf("http://203.0.113.5:3001/project/%d/history?t=%d", projectID, result.TaskID)
	if result.TaskID == 0 || result.TaskURL != want {
		t.Errorf("task url = %q, want %q", result.TaskURL, want)
	}

	cfg.UIURL = ""
	result, err = NewClient(cfg).Run(context.Background(), testSpec())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.TaskURL != "" {
		t.Errorf("task url = %q without a UI URL, want none", result.TaskURL)
	}
}
