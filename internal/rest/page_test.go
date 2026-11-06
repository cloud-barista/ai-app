package rest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ish-hcc/ai-agent-proto/internal/archive"
	"github.com/ish-hcc/ai-agent-proto/internal/config"
	"github.com/ish-hcc/ai-agent-proto/internal/engine"
	"github.com/ish-hcc/ai-agent-proto/internal/mcpclient"
	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/tools"
)

// listPage GETs a list and reads the ids of one array field and the page.
func listPage(t *testing.T, s *Server, path, field, id string) ([]string, model.Page) {
	t.Helper()
	rec := sendNodeJSON(s, http.MethodGet, path, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d: %s", path, rec.Code, rec.Body.String())
	}
	var body map[string]json.RawMessage
	var page model.Page
	var items []map[string]any
	if json.Unmarshal(rec.Body.Bytes(), &body) != nil || json.Unmarshal(rec.Body.Bytes(), &page) != nil ||
		json.Unmarshal(body[field], &items) != nil || items == nil {
		t.Fatalf("GET %s: %s, want %s, total, limit and offset", path, rec.Body.String(), field)
	}
	for _, key := range []string{"total", "limit", "offset"} {
		if _, ok := body[key]; !ok {
			t.Errorf("GET %s has no %s: %s", path, key, rec.Body.String())
		}
	}
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, fmt.Sprint(item[id]))
	}
	return ids, page
}

// checkPaging reads a list whole and in pages and compares the pages with the
// whole list, which has want items.
func checkPaging(t *testing.T, s *Server, path, field, id string, want int) []string {
	t.Helper()
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	all, page := listPage(t, s, path, field, id)
	if len(all) != want || page != (model.Page{Total: want, Limit: model.DefaultPageLimit}) {
		t.Fatalf("GET %s = %v, %+v, want %d items with total %d, limit 100 and offset 0", path, all, page, want, want)
	}
	got, page := listPage(t, s, path+sep+"limit=1&offset=1", field, id)
	if !slices.Equal(got, all[1:2]) || page != (model.Page{Total: want, Limit: 1, Offset: 1}) {
		t.Errorf("GET %s limit=1&offset=1 = %v, %+v, want %v with total %d", path, got, page, all[1:2], want)
	}
	got, page = listPage(t, s, path+sep+"offset=1", field, id)
	if !slices.Equal(got, all[1:]) || page.Total != want {
		t.Errorf("GET %s offset=1 = %v, %+v, want %v", path, got, page, all[1:])
	}
	got, page = listPage(t, s, path+sep+fmt.Sprintf("offset=%d", want+5), field, id)
	if len(got) != 0 || page.Total != want {
		t.Errorf("GET %s offset past the end = %v, %+v, want none with total %d", path, got, page, want)
	}
	for _, bad := range []string{"limit=0", "limit=-1", "limit=1001", "limit=x", "offset=-1", "offset=x"} {
		rec := sendNodeJSON(s, http.MethodGet, path+sep+bad, "")
		var msg model.SimpleMsg
		if rec.Code != http.StatusBadRequest || json.Unmarshal(rec.Body.Bytes(), &msg) != nil || msg.Message == "" {
			t.Errorf("GET %s %s = %d %s, want 400 with a message", path, bad, rec.Code, rec.Body.String())
		}
	}
	return all
}

func TestModelListPages(t *testing.T) {
	s := newModelServer(t, filepath.Join(t.TempDir(), "models.json"))
	all := checkPaging(t, s, "/aiapp/models", "models", "id", 3)
	if !slices.IsSorted(all) {
		t.Errorf("models = %v, want id order", all)
	}
	got, page := listPage(t, s, "/aiapp/models?modality=image&limit=1", "models", "id")
	if !slices.Equal(got, []string{"ministral-3-8b"}) || page.Total != 2 {
		t.Errorf("modality=image limit=1 = %v, %+v, want the first of 2 matches", got, page)
	}
	rec := sendNodeJSON(s, http.MethodGet, "/aiapp/models?limit=1001", "")
	var msg model.SimpleMsg
	if json.Unmarshal(rec.Body.Bytes(), &msg) != nil || msg.Message != "limit must be a whole number from 1 to 1000" {
		t.Errorf("limit=1001 message = %s", rec.Body.String())
	}
}

func TestAppListPages(t *testing.T) {
	s := newAppBundleServer(t, t.TempDir(), 0)
	for _, name := range []string{"web-b", "web-a", "web-c"} {
		rec := postMultipart(t, s, part{field: "name", content: name}, part{field: "script", content: "true\n"})
		if rec.Code != http.StatusOK {
			t.Fatalf("POST /apps %s = %d: %s", name, rec.Code, rec.Body.String())
		}
	}
	all := checkPaging(t, s, "/aiapp/apps", "apps", "name", 3)
	if !slices.Equal(all, []string{"web-a", "web-b", "web-c"}) {
		t.Errorf("apps = %v, want name order", all)
	}
}

func TestDeploymentAndEndpointListPages(t *testing.T) {
	s, _, _ := newNodeDeployServer(t, false)
	inf := postNodeDeployment(t, s, nodeInferenceBody)
	time.Sleep(2 * time.Millisecond)
	app := postNodeDeployment(t, s, nodeAppBody)
	time.Sleep(2 * time.Millisecond)
	again := postNodeDeployment(t, s, nodeAppBody)

	all := checkPaging(t, s, "/aiapp/deployments", "deployments", "id", 3)
	if !slices.Equal(all, []string{again.ID, app.ID, inf.ID}) {
		t.Errorf("deployments = %v, want newest first", all)
	}
	got, page := listPage(t, s, "/aiapp/deployments?type=app&limit=1&offset=1", "deployments", "id")
	if !slices.Equal(got, []string{app.ID}) || page.Total != 2 {
		t.Errorf("type=app limit=1 offset=1 = %v, %+v, want the older app deployment of 2", got, page)
	}

	all = checkPaging(t, s, "/aiapp/endpoints", "endpoints", "workKey", 2)
	if !slices.Equal(all, []string{"app-web", "ms-vllm"}) {
		t.Errorf("endpoints = %v, want workKey order", all)
	}
	got, page = listPage(t, s, "/aiapp/endpoints?type=inference&offset=1", "endpoints", "workKey")
	if len(got) != 0 || page.Total != 1 {
		t.Errorf("type=inference offset=1 = %v, %+v, want none of 1", got, page)
	}
}

func TestArchiveListPages(t *testing.T) {
	store, err := archive.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"run-1", "run-2", "run-3"} {
		if err := store.Append(context.Background(), model.ArchiveRecord{RunID: id}); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &config.Config{
		Engine:    config.EngineConfig{URL: "http://127.0.0.1:1"},
		PartnerNL: config.PartnerNLConfig{URL: "http://127.0.0.1:1"},
	}
	s := NewAppServer(cfg, AppDependencies{Archive: store, Tools: tools.NewRegistry(true)})
	all := checkPaging(t, s, "/aiapp/archive", "records", "runId", 3)
	if !slices.Equal(all, []string{"run-3", "run-2", "run-1"}) {
		t.Errorf("archive = %v, want newest first", all)
	}
}

func TestEngineRunListPages(t *testing.T) {
	MarkReady()
	runs, err := archive.NewLog[engine.Run](t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"eng-1", "eng-2", "eng-3"} {
		if err := runs.Append(engine.Run{RunID: id}); err != nil {
			t.Fatal(err)
		}
	}
	automation := engine.New(mcpclient.New("http://127.0.0.1:1/aiapp/mcp", ""), runs)
	registry := engine.NewRegistry(automation)
	s := NewEngineServer(&config.Config{}, EngineDependencies{
		Engine: automation,
		Card:   engine.NewCardBuilder(automation, registry, nil),
		Tools:  registry,
	})
	all := checkPaging(t, s, "/aiapp/engine/runs", "runs", "runId", 3)
	if !slices.Equal(all, []string{"eng-3", "eng-2", "eng-1"}) {
		t.Errorf("engine runs = %v, want newest first", all)
	}
}
