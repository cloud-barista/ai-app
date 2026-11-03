package rest

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
)

const nodeInferenceBody = `{"type":"inference","targets":[{"kind":"node","nsId":"etri","infraId":"travel","nodeId":"vm1"}],` +
	`"inference":{"modelId":"qwen3-embedding-0.6b","artifactId":"hf-bf16","gpu":false}}`

// waitNodeDeployment polls a deployment while its status is one of busy.
func waitNodeDeployment(t *testing.T, s *Server, id string, busy ...string) model.NodeDeployment {
	t.Helper()
	var got model.NodeDeployment
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		rec := sendNodeJSON(s, http.MethodGet, "/aiapp/deployments/"+id, "")
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil {
			t.Fatalf("GET = %d: %s", rec.Code, rec.Body.String())
		}
		still := false
		for _, b := range busy {
			still = still || got.Status == b
		}
		if !still {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("deployment %s stayed %s", id, got.Status)
	return got
}

func postNodeDeployment(t *testing.T, s *Server, body string) model.NodeDeployment {
	t.Helper()
	rec := sendNodeJSON(s, http.MethodPost, "/aiapp/deployments", body)
	var created model.NodeDeployment
	if rec.Code != http.StatusAccepted || json.Unmarshal(rec.Body.Bytes(), &created) != nil {
		t.Fatalf("POST /deployments = %d: %s", rec.Code, rec.Body.String())
	}
	got := waitNodeDeployment(t, s, created.ID, model.NodeDeployPending, model.NodeDeployRunning)
	if got.Status != model.NodeDeploySucceeded {
		t.Fatalf("deployment %s = %q", created.ID, got.Status)
	}
	return got
}

func getEndpoints(t *testing.T, s *Server, query string) model.EndpointListResp {
	t.Helper()
	rec := sendNodeJSON(s, http.MethodGet, "/aiapp/endpoints"+query, "")
	var list model.EndpointListResp
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &list) != nil || list.Endpoints == nil {
		t.Fatalf("GET /endpoints%s = %d: %s", query, rec.Code, rec.Body.String())
	}
	t.Logf("GET /aiapp/endpoints%s = %s", query, rec.Body.String())
	return list
}

func TestEndpointRoute(t *testing.T) {
	s, _, _ := newNodeDeployServer(t, false)
	if list := getEndpoints(t, s, ""); len(list.Endpoints) != 0 {
		t.Fatalf("endpoints before any deployment = %+v", list.Endpoints)
	}

	inf := postNodeDeployment(t, s, nodeInferenceBody)
	time.Sleep(2 * time.Millisecond)
	app := postNodeDeployment(t, s, nodeAppBody)

	list := getEndpoints(t, s, "")
	if len(list.Endpoints) != 2 {
		t.Fatalf("endpoints = %+v, want 2", list.Endpoints)
	}
	a, m := list.Endpoints[0], list.Endpoints[1]
	if a.Type != model.NodeDeployTypeApp || a.DeploymentID != app.ID || a.WorkKey != "app-web" || a.Host != "203.0.113.21" {
		t.Errorf("app endpoint = %+v", a)
	}
	if m.Type != model.NodeDeployTypeInference || m.DeploymentID != inf.ID || m.WorkKey != "ms-vllm" ||
		m.URL != "http://203.0.113.21:8000/v1" || m.HealthURL != "http://203.0.113.21:8000/health" ||
		m.PortsOpened || len(m.Notes) == 0 {
		t.Errorf("inference endpoint = %+v", m)
	}
	for query, want := range map[string]int{
		"?type=inference":                      1,
		"?type=app":                            1,
		"?nsId=etri&infraId=travel&nodeId=vm1": 2,
		"?nodeId=vm2":                          0,
		"?nsId=other":                          0,
	} {
		if got := len(getEndpoints(t, s, query).Endpoints); got != want {
			t.Errorf("GET /endpoints%s = %d, want %d", query, got, want)
		}
	}

	rec := sendNodeJSON(s, http.MethodDelete, "/aiapp/deployments/"+app.ID, "")
	if rec.Code != http.StatusAccepted && rec.Code != http.StatusOK {
		t.Fatalf("DELETE = %d: %s", rec.Code, rec.Body.String())
	}
	if got := waitNodeDeployment(t, s, app.ID, model.NodeDeployRemoving); got.Status != model.NodeDeployRemoved {
		t.Fatalf("removed = %q", got.Status)
	}
	list = getEndpoints(t, s, "")
	if len(list.Endpoints) != 1 || list.Endpoints[0].DeploymentID != inf.ID {
		t.Errorf("endpoints after DELETE = %+v, want the inference only", list.Endpoints)
	}
}
