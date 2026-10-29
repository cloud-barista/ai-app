package rest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ish-hcc/ai-agent-proto/internal/appstore"
	"github.com/ish-hcc/ai-agent-proto/internal/config"
	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/tools"
)

func newAppBundleServer(t *testing.T, root string, maxUpload int64) *Server {
	t.Helper()
	store, err := appstore.Open(root, maxUpload)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	registry := tools.NewRegistry(true)
	tools.RegisterApps(registry, store)
	cfg := &config.Config{
		Engine:    config.EngineConfig{URL: "http://127.0.0.1:1"},
		PartnerNL: config.PartnerNLConfig{URL: "http://127.0.0.1:1"},
	}
	return NewAppServer(cfg, AppDependencies{Apps: store, Tools: registry})
}

// part is one multipart part: a field when fileName is empty, a file part
// otherwise. fileName is written as is, path included.
type part struct {
	field, fileName, content string
}

func postMultipart(t *testing.T, s *Server, parts ...part) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	for _, p := range parts {
		header := textproto.MIMEHeader{}
		if p.fileName == "" {
			header.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q`, p.field))
		} else {
			header.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, p.field, p.fileName))
			header.Set("Content-Type", "application/octet-stream")
		}
		pw, err := w.CreatePart(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pw.Write([]byte(p.content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/aiapp/apps", &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)
	return rec
}

func TestAppBundleRoutes(t *testing.T) {
	root := t.TempDir()
	s := newAppBundleServer(t, root, 0)

	rec := postMultipart(t, s,
		part{field: "name", content: "open-webui"},
		part{field: "script", content: "docker compose up -d\n"},
		part{field: "files", fileName: "docker-compose.yml", content: "services: {}\n"},
		part{field: "files", fileName: "README.txt", content: "hello\n"},
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("upload = %d: %s", rec.Code, rec.Body)
	}
	var v1 model.AppVersion
	if err := json.Unmarshal(rec.Body.Bytes(), &v1); err != nil {
		t.Fatal(err)
	}
	if v1.Version != 1 || len(v1.Files) != 2 || v1.Files[0].Name != "docker-compose.yml" {
		t.Errorf("first upload = %+v", v1)
	}

	rec = postMultipart(t, s,
		part{field: "name", content: "open-webui"},
		part{field: "script", fileName: "deploy.sh", content: "echo from a file part\n"},
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("second upload = %d: %s", rec.Code, rec.Body)
	}
	var v2 model.AppVersion
	if err := json.Unmarshal(rec.Body.Bytes(), &v2); err != nil {
		t.Fatal(err)
	}
	if v2.Version != 2 || v2.Script != "echo from a file part\n" || len(v2.Files) != 0 {
		t.Errorf("second upload = %+v", v2)
	}
	if raw, err := os.ReadFile(filepath.Join(root, "open-webui", "1", appstore.FilesDir, "docker-compose.yml")); err != nil || string(raw) != "services: {}\n" {
		t.Errorf("stored file = %q, %v", raw, err)
	}

	rec = do(s, http.MethodGet, "/aiapp/apps", "")
	var list model.AppListResp
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("list = %d %s", rec.Code, rec.Body)
	}
	if len(list.Apps) != 1 || len(list.Apps[0].Versions) != 2 {
		t.Errorf("list = %+v", list)
	}

	rec = do(s, http.MethodGet, "/aiapp/apps/open-webui/versions/1", "")
	var got model.AppVersion
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != http.StatusOK || got.Script != "docker compose up -d\n" {
		t.Errorf("version 1 = %d %s", rec.Code, rec.Body)
	}
	for path, want := range map[string]int{
		"/aiapp/apps/open-webui":            http.StatusOK,
		"/aiapp/apps/open-webui/versions/3": http.StatusNotFound,
		"/aiapp/apps/open-webui/versions/x": http.StatusBadRequest,
		"/aiapp/apps/open-webui/versions/0": http.StatusBadRequest,
		"/aiapp/apps/nope":                  http.StatusNotFound,
	} {
		if rec := do(s, http.MethodGet, path, ""); rec.Code != want {
			t.Errorf("GET %s = %d, want %d: %s", path, rec.Code, want, rec.Body)
		}
	}

	if rec := do(s, http.MethodDelete, "/aiapp/apps/open-webui", ""); rec.Code != http.StatusOK {
		t.Fatalf("delete = %d: %s", rec.Code, rec.Body)
	}
	if rec := do(s, http.MethodGet, "/aiapp/apps/open-webui", ""); rec.Code != http.StatusNotFound {
		t.Errorf("get after delete = %d, want 404", rec.Code)
	}
	if _, err := os.Stat(filepath.Join(root, "open-webui")); !os.IsNotExist(err) {
		t.Errorf("application directory after delete: %v", err)
	}
	if rec := do(s, http.MethodDelete, "/aiapp/apps/open-webui", ""); rec.Code != http.StatusNotFound {
		t.Errorf("second delete = %d, want 404", rec.Code)
	}
}

func TestAppBundleUploadRefusals(t *testing.T) {
	s := newAppBundleServer(t, t.TempDir(), 64)
	name := part{field: "name", content: "dify"}
	script := part{field: "script", content: "true\n"}

	for _, tc := range []struct {
		label string
		parts []part
		want  int
	}{
		{"no script", []part{name, {field: "files", fileName: "a.yml", content: "x"}}, http.StatusBadRequest},
		{"script twice", []part{name, script, {field: "script", fileName: "s.sh", content: "x"}}, http.StatusBadRequest},
		{"no name", []part{script}, http.StatusBadRequest},
		{"bad name", []part{{field: "name", content: "Dify!"}, script}, http.StatusBadRequest},
		{"dot in name", []part{{field: "name", content: "open.webui"}, script}, http.StatusBadRequest},
		{"parent path", []part{name, script, {field: "files", fileName: "../x", content: "x"}}, http.StatusBadRequest},
		{"nested path", []part{name, script, {field: "files", fileName: "conf/x", content: "x"}}, http.StatusBadRequest},
		{"hidden file", []part{name, script, {field: "files", fileName: ".env", content: "x"}}, http.StatusBadRequest},
		{"duplicate file", []part{name, script, {field: "files", fileName: "a", content: "x"}, {field: "files", fileName: "a", content: "y"}}, http.StatusBadRequest},
		{"files as text", []part{name, script, {field: "files", content: "x"}}, http.StatusBadRequest},
		{"over the bound", []part{name, script, {field: "files", fileName: "big", content: strings.Repeat("x", 64)}}, http.StatusRequestEntityTooLarge},
	} {
		rec := postMultipart(t, s, tc.parts...)
		if rec.Code != tc.want {
			t.Errorf("%s: status = %d, want %d: %s", tc.label, rec.Code, tc.want, rec.Body)
		}
	}

	req := httptest.NewRequest(http.MethodPost, "/aiapp/apps", strings.NewReader(`{"name":"dify"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("JSON body = %d, want 400", rec.Code)
	}

	if rec := do(s, http.MethodGet, "/aiapp/apps", ""); !strings.Contains(rec.Body.String(), `"apps":[]`) {
		t.Errorf("refused uploads were stored: %s", rec.Body)
	}
}
