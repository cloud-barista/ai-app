package semaphore

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ish-hcc/ai-agent-proto/internal/config"
)

const (
	testKeyBody = "b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQ"
	// The PEM label is split so that secret scanners do not take this fake
	// key for a real one.
	testKeyLabel = "OPENSSH " + "PRIVATE KEY"
	testKey      = "-----BEGIN " + testKeyLabel + "-----\n" + testKeyBody + "\n-----END " + testKeyLabel + "-----\n"
	testToken    = "api-token"
)

// fakeSemaphore keeps the resources of one Semaphore instance in memory and
// answers the v2.12 routes Run uses, with the delete rules of the real one.
type fakeSemaphore struct {
	mu sync.Mutex

	// useToken makes the fake require the bearer token instead of a session.
	useToken bool
	// statuses are the task statuses returned by successive GET task calls;
	// the last one repeats.
	statuses []string
	// failDelete makes deleting this kind of resource answer 500.
	failDelete string
	// failCreate makes creating this kind of resource answer 400.
	failCreate string
	// expireSessionOnce answers 401 to the first authenticated call after login.
	expireSessionOnce bool

	nextID      int
	sessions    map[string]bool
	logins      int
	projects    map[int]string
	keys        map[int]map[string]any
	repos       map[int]map[string]any
	envs        map[int]map[string]any
	inventories map[int]map[string]any
	templates   map[int]map[string]any
	tasks       map[int]map[string]any
	taskPolls   int
	stopped     bool
	calls       []string
}

func newFakeSemaphore() *fakeSemaphore {
	return &fakeSemaphore{
		statuses:    []string{StatusRunning, StatusSuccess},
		nextID:      1,
		sessions:    map[string]bool{},
		projects:    map[int]string{},
		keys:        map[int]map[string]any{},
		repos:       map[int]map[string]any{},
		envs:        map[int]map[string]any{},
		inventories: map[int]map[string]any{},
		templates:   map[int]map[string]any{},
		tasks:       map[int]map[string]any{},
	}
}

var projectRoute = regexp.MustCompile(`^/api/project/(\d+)/([a-z]+)(?:/(\d+))?(?:/([a-z]+))?$`)

func (f *fakeSemaphore) id() int {
	f.nextID++
	return f.nextID
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (f *fakeSemaphore) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)

	body := map[string]any{}
	raw, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(raw, &body)

	if r.URL.Path == "/api/auth/login" {
		if body["auth"] != "admin" || body["password"] != "pw" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.logins++
		session := "s" + strconv.Itoa(f.logins)
		f.sessions[session] = true
		http.SetCookie(w, &http.Cookie{Name: "semaphore", Value: session, Path: "/"})
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if f.useToken {
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
	} else {
		cookie, err := r.Cookie("semaphore")
		if err != nil || !f.sessions[cookie.Value] {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if f.expireSessionOnce {
			f.expireSessionOnce = false
			delete(f.sessions, cookie.Value)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
	}

	if r.URL.Path == "/api/projects" {
		switch r.Method {
		case http.MethodGet:
			list := []map[string]any{}
			for id, name := range f.projects {
				list = append(list, map[string]any{"id": id, "name": name})
			}
			writeJSON(w, http.StatusOK, list)
		case http.MethodPost:
			id := f.id()
			f.projects[id] = body["name"].(string)
			// Semaphore seeds a new project with a none key and an empty environment.
			f.keys[f.id()] = map[string]any{"name": "None", "type": "none"}
			f.envs[f.id()] = map[string]any{"name": "Empty", "json": "{}"}
			writeJSON(w, http.StatusCreated, map[string]any{"id": id, "name": body["name"]})
		}
		return
	}

	m := projectRoute.FindStringSubmatch(r.URL.Path)
	if m == nil {
		http.NotFound(w, r)
		return
	}
	kind, sub := m[2], m[4]
	var itemID int
	if m[3] != "" {
		itemID, _ = strconv.Atoi(m[3])
	}
	store := map[string]map[int]map[string]any{
		"keys": f.keys, "repositories": f.repos, "environment": f.envs,
		"inventory": f.inventories, "templates": f.templates, "tasks": f.tasks,
	}[kind]
	if store == nil {
		http.NotFound(w, r)
		return
	}

	switch {
	case r.Method == http.MethodGet && itemID == 0:
		list := []map[string]any{}
		for id, item := range store {
			out := map[string]any{"id": id}
			for k, v := range item {
				if k != "ssh" {
					out[k] = v
				}
			}
			list = append(list, out)
		}
		writeJSON(w, http.StatusOK, list)

	case r.Method == http.MethodPost && itemID == 0:
		if f.failCreate == kind {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "rejected " + kind})
			return
		}
		if kind == "templates" && body["environment_id"] == nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "no environment"})
			return
		}
		id := f.id()
		if kind == "tasks" {
			body["status"] = StatusWaiting
		}
		delete(body, "id")
		store[id] = body
		out := map[string]any{}
		for k, v := range body {
			if k != "ssh" {
				out[k] = v
			}
		}
		out["id"] = id
		writeJSON(w, http.StatusCreated, out)

	case r.Method == http.MethodGet && kind == "tasks" && sub == "":
		task, ok := f.tasks[itemID]
		if !ok {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if f.stopped {
			task["status"] = StatusStopped
		} else {
			i := f.taskPolls
			if i >= len(f.statuses) {
				i = len(f.statuses) - 1
			}
			task["status"] = f.statuses[i]
			f.taskPolls++
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": itemID, "status": task["status"]})

	case r.Method == http.MethodGet && kind == "tasks" && sub == "output":
		if _, ok := f.tasks[itemID]; !ok {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, []map[string]any{
			{"task_id": itemID, "time": "2026-10-29T11:34:29Z", "output": "Task added to queue"},
			{"task_id": itemID, "time": "2026-10-29T11:34:30Z", "output": "\x1b[0;32m  - hello from node\x1b[0m\n"},
			{"task_id": itemID, "time": "2026-10-29T11:34:31Z", "output": "key line " + testKeyBody},
		})

	case r.Method == http.MethodPost && kind == "tasks" && sub == "stop":
		f.stopped = true
		w.WriteHeader(http.StatusNoContent)

	case r.Method == http.MethodDelete && itemID != 0:
		if _, ok := store[itemID]; !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if f.failDelete == kind {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "cannot delete"})
			return
		}
		if inUse := f.inUse(kind, itemID); inUse {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "in use", "inUse": true})
			return
		}
		delete(store, itemID)
		if kind == "templates" {
			for id, task := range f.tasks {
				if int(task["template_id"].(float64)) == itemID {
					delete(f.tasks, id)
				}
			}
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		http.NotFound(w, r)
	}
}

// inUse mirrors the Semaphore refusal to delete a key or an inventory that
// something still references.
func (f *fakeSemaphore) inUse(kind string, id int) bool {
	refs := map[string][]map[int]map[string]any{
		"keys":      {f.inventories},
		"inventory": {f.templates},
	}[kind]
	field := map[string]string{"keys": "ssh_key_id", "inventory": "inventory_id"}[kind]
	for _, store := range refs {
		for _, item := range store {
			if v, ok := item[field].(float64); ok && int(v) == id {
				return true
			}
		}
	}
	return false
}

func (f *fakeSemaphore) count(kind string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch kind {
	case "keys":
		n := 0
		for _, k := range f.keys {
			if k["type"] == keyTypeSSH {
				n++
			}
		}
		return n
	case "inventory":
		return len(f.inventories)
	case "templates":
		return len(f.templates)
	}
	return -1
}

func (f *fakeSemaphore) only(kind string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	var store map[int]map[string]any
	switch kind {
	case "keys":
		for _, k := range f.keys {
			if k["type"] == keyTypeSSH {
				return k
			}
		}
		return nil
	case "inventory":
		store = f.inventories
	case "templates":
		store = f.templates
	case "tasks":
		store = f.tasks
	}
	for _, item := range store {
		return item
	}
	return nil
}

func newTestClient(t *testing.T, fake *fakeSemaphore, timeout time.Duration) *Client {
	t.Helper()
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	cfg := config.SemaphoreConfig{
		URL: srv.URL, Username: "admin", Password: "pw", Project: "aiapp", PlaybookDir: "/ansible",
		PollInterval: 5 * time.Millisecond, Timeout: timeout,
	}
	if fake.useToken {
		cfg.Username, cfg.Password, cfg.Token = "", "", testToken
	}
	return NewClient(cfg)
}

func testSpec() RunSpec {
	return RunSpec{
		Name: "dep-1/aws-vm1", Host: "203.0.113.10", User: "cb-user", PrivateKey: testKey,
		Playbook:  "copy-and-run.yml",
		ExtraVars: map[string]any{"stage_dir": "/var/lib/aiapp/stage/dep-1/aws-vm1", "deployment_id": "dep-1", "script": "run.sh"},
	}
}

func assertCleanedUp(t *testing.T, fake *fakeSemaphore) {
	t.Helper()
	for _, kind := range []string{"keys", "inventory", "templates"} {
		if n := fake.count(kind); n != 0 {
			t.Errorf("%d %s left in Semaphore, want 0", n, kind)
		}
	}
}

func TestRunRegistersRunsAndCleansUp(t *testing.T) {
	fake := newFakeSemaphore()
	c := newTestClient(t, fake, 5*time.Second)

	// Hold the task in waiting until the registrations are inspected.
	fake.statuses = []string{StatusWaiting}
	done := make(chan struct{})
	var result *RunResult
	var runErr error
	go func() {
		defer close(done)
		result, runErr = c.Run(context.Background(), testSpec())
	}()

	deadline := time.Now().Add(2 * time.Second)
	for fake.only("tasks") == nil && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	key, inv, tpl, task := fake.only("keys"), fake.only("inventory"), fake.only("templates"), fake.only("tasks")
	fake.mu.Lock()
	fake.statuses = []string{StatusRunning, StatusSuccess}
	fake.taskPolls = 0
	fake.mu.Unlock()
	<-done

	if runErr != nil {
		t.Fatalf("Run: %v", runErr)
	}
	if key == nil || inv == nil || tpl == nil || task == nil {
		t.Fatalf("registrations = %v %v %v %v", key, inv, tpl, task)
	}
	ssh, _ := key["ssh"].(map[string]any)
	if ssh["private_key"] != testKey || ssh["login"] != "cb-user" {
		t.Errorf("key ssh = %v", ssh)
	}
	if !strings.HasPrefix(key["name"].(string), "aiapp-dep-1-aws-vm1-") {
		t.Errorf("key name = %v", key["name"])
	}
	wantInv := "node ansible_host=203.0.113.10 ansible_port=22 ansible_user=cb-user "
	if inv["type"] != inventoryStatic || !strings.HasPrefix(inv["inventory"].(string), wantInv) {
		t.Errorf("inventory = %v", inv)
	}
	if tpl["playbook"] != "copy-and-run.yml" || tpl["app"] != appAnsible || tpl["environment_id"] == nil {
		t.Errorf("template = %v", tpl)
	}
	vars := map[string]any{}
	if err := json.Unmarshal([]byte(task["environment"].(string)), &vars); err != nil || vars["script"] != "run.sh" {
		t.Errorf("task environment = %v (%v)", task["environment"], err)
	}

	if result.Status != StatusSuccess || !result.Succeeded() || result.TaskID == 0 {
		t.Errorf("result = %+v", result)
	}
	joined := strings.Join(result.Output, "\n")
	if !strings.Contains(joined, "  - hello from node") || strings.Contains(joined, "\x1b") {
		t.Errorf("output = %q, want the hello line without colour codes", result.Output)
	}
	if strings.Contains(joined, testKeyBody) || !strings.Contains(joined, "key line <redacted>") {
		t.Errorf("output = %q, want the key masked", result.Output)
	}
	if len(result.CleanupErrors) != 0 {
		t.Errorf("cleanup errors = %v", result.CleanupErrors)
	}
	assertCleanedUp(t, fake)
}

func TestRunReportsAFailedTaskWithoutError(t *testing.T) {
	fake := newFakeSemaphore()
	fake.statuses = []string{StatusRunning, StatusError}
	c := newTestClient(t, fake, 5*time.Second)

	result, err := c.Run(context.Background(), testSpec())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != StatusError || result.Succeeded() || len(result.Output) == 0 {
		t.Errorf("result = %+v, want error status with output", result)
	}
	assertCleanedUp(t, fake)
}

func TestRunTimesOutStopsAndCleansUp(t *testing.T) {
	fake := newFakeSemaphore()
	fake.statuses = []string{StatusRunning}
	c := newTestClient(t, fake, 50*time.Millisecond)

	result, err := c.Run(context.Background(), testSpec())
	if err == nil || !strings.Contains(err.Error(), "did not end within") {
		t.Fatalf("Run error = %v, want a timeout", err)
	}
	fake.mu.Lock()
	stopped := fake.stopped
	fake.mu.Unlock()
	if !stopped || result.Status != StatusStopped {
		t.Errorf("stopped = %v, status = %q, want the task stopped", stopped, result.Status)
	}
	if len(result.Output) == 0 {
		t.Error("output was not read after the timeout")
	}
	assertCleanedUp(t, fake)
}

func TestRunCancelledStillCleansUp(t *testing.T) {
	fake := newFakeSemaphore()
	fake.statuses = []string{StatusRunning}
	c := newTestClient(t, fake, 5*time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	result, err := c.Run(ctx, testSpec())
	if err == nil || !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("Run error = %v, want the context error", err)
	}
	if result.Status != StatusStopped {
		t.Errorf("status = %q, want stopped", result.Status)
	}
	assertCleanedUp(t, fake)
}

func TestRunCleansUpWhatWasRegisteredBeforeAFailure(t *testing.T) {
	fake := newFakeSemaphore()
	fake.failCreate = "templates"
	c := newTestClient(t, fake, 5*time.Second)

	result, err := c.Run(context.Background(), testSpec())
	if err == nil || !strings.Contains(err.Error(), "rejected templates") {
		t.Fatalf("Run error = %v, want the template rejection", err)
	}
	if result.TaskID != 0 || result.Status != "" {
		t.Errorf("result = %+v, want no task", result)
	}
	assertCleanedUp(t, fake)
}

func TestRunReportsCleanupFailures(t *testing.T) {
	fake := newFakeSemaphore()
	fake.failDelete = "keys"
	c := newTestClient(t, fake, 5*time.Second)

	result, err := c.Run(context.Background(), testSpec())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(result.CleanupErrors) != 1 || !strings.Contains(result.CleanupErrors[0], "failed to delete semaphore key") {
		t.Errorf("cleanup errors = %v, want the key", result.CleanupErrors)
	}
	if fake.count("inventory") != 0 || fake.count("templates") != 0 {
		t.Error("inventory or template left behind")
	}
}

func TestRunWithTokenDoesNotLogIn(t *testing.T) {
	fake := newFakeSemaphore()
	fake.useToken = true
	c := newTestClient(t, fake, 5*time.Second)

	result, err := c.Run(context.Background(), testSpec())
	if err != nil || !result.Succeeded() {
		t.Fatalf("Run = %+v, %v", result, err)
	}
	if fake.logins != 0 {
		t.Errorf("logged in %d times with a token", fake.logins)
	}
	assertCleanedUp(t, fake)
}

func TestRunRenewsAnExpiredSession(t *testing.T) {
	fake := newFakeSemaphore()
	fake.expireSessionOnce = true
	c := newTestClient(t, fake, 5*time.Second)

	result, err := c.Run(context.Background(), testSpec())
	if err != nil || !result.Succeeded() {
		t.Fatalf("Run = %+v, %v", result, err)
	}
	if fake.logins != 2 {
		t.Errorf("logins = %d, want 2", fake.logins)
	}
}

func TestRunRejectsBadSpecsBeforeRegistering(t *testing.T) {
	cases := map[string]func(*RunSpec){
		"host with inventory text": func(s *RunSpec) { s.Host = "10.0.0.1 ansible_user=root" },
		"user with a space":        func(s *RunSpec) { s.User = "cb user" },
		"port out of range":        func(s *RunSpec) { s.Port = 70000 },
		"no key":                   func(s *RunSpec) { s.PrivateKey = " " },
		"playbook outside":         func(s *RunSpec) { s.Playbook = "../etc/x.yml" },
		"not a playbook":           func(s *RunSpec) { s.Playbook = "run.sh" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			fake := newFakeSemaphore()
			c := newTestClient(t, fake, time.Second)
			spec := testSpec()
			mutate(&spec)
			if _, err := c.Run(context.Background(), spec); err == nil {
				t.Fatal("Run accepted the spec")
			}
			if len(fake.calls) != 0 {
				t.Errorf("calls = %v, want none", fake.calls)
			}
		})
	}
}

func TestRunNamesMissingSettings(t *testing.T) {
	fake := newFakeSemaphore()
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	noURL := NewClient(config.SemaphoreConfig{Username: "admin", Password: "pw", Project: "aiapp", PlaybookDir: "/ansible"})
	if _, err := noURL.Run(context.Background(), testSpec()); err == nil || !strings.Contains(err.Error(), "AIAPP_SEMAPHORE_URL") {
		t.Errorf("Run without a URL = %v, want one naming AIAPP_SEMAPHORE_URL", err)
	}

	noCreds := NewClient(config.SemaphoreConfig{URL: srv.URL, Project: "aiapp", PlaybookDir: "/ansible"})
	if _, err := noCreds.Run(context.Background(), testSpec()); err == nil || !strings.Contains(err.Error(), "AIAPP_SEMAPHORE_PASSWORD") {
		t.Errorf("Run without credentials = %v, want one naming the credential settings", err)
	}
	if len(fake.calls) != 0 {
		t.Errorf("calls = %v, want none: an empty password must never be sent", fake.calls)
	}
}

func TestRunMasksTheKeyInErrors(t *testing.T) {
	err := maskSecret("bad key "+testKeyBody+" here", testKey)
	if strings.Contains(err, testKeyBody) {
		t.Errorf("masked = %q", err)
	}
}

func TestRunReusesProjectRepositoryAndEnvironment(t *testing.T) {
	fake := newFakeSemaphore()
	c := newTestClient(t, fake, 5*time.Second)
	for i := 0; i < 2; i++ {
		fake.mu.Lock()
		fake.taskPolls = 0
		fake.mu.Unlock()
		if _, err := c.Run(context.Background(), testSpec()); err != nil {
			t.Fatalf("Run %d: %v", i, err)
		}
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.projects) != 1 || len(fake.repos) != 1 {
		t.Errorf("projects = %d, repositories = %d, want 1 each", len(fake.projects), len(fake.repos))
	}
	named := 0
	for _, e := range fake.envs {
		if e["name"] == EnvironmentName {
			named++
		}
	}
	if named != 1 {
		t.Errorf("%d environments named %s, want 1", named, EnvironmentName)
	}
	for _, r := range fake.repos {
		if r["git_url"] != "/ansible" {
			t.Errorf("repository = %v", r)
		}
	}
}
