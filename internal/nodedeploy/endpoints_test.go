package nodedeploy

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/semaphore"
)

func endpointsOf(f *fixture, filter EndpointFilter) []model.Endpoint {
	return f.svc.Endpoints(context.Background(), filter)
}

// endpointKeys names each endpoint <nodeId> <workKey> <deploymentId>.
func endpointKeys(list []model.Endpoint) []string {
	out := make([]string, len(list))
	for i, e := range list {
		out[i] = e.NodeID + " " + e.WorkKey + " " + e.DeploymentID
	}
	return out
}

func TestEndpointsListTheInferenceAndTheAppServing(t *testing.T) {
	f := newFixture(t, false)
	inf := deployed(t, f, modelReq("vm1"))
	time.Sleep(2 * time.Millisecond)
	app := deployed(t, f, appReq("vm1"))
	if inf.Status != model.NodeDeployRunning || app.Status != model.NodeDeployRunning {
		t.Fatalf("deploy = %q, %q", inf.Status, app.Status)
	}

	list := endpointsOf(f, EndpointFilter{})
	want := []string{"vm1 app-web " + app.ID, "vm1 ms-ollama " + inf.ID}
	if got := endpointKeys(list); !slices.Equal(got, want) {
		t.Fatalf("endpoints = %v, want %v", got, want)
	}
	a, m := list[0], list[1]
	if a.Type != model.NodeDeployTypeApp || a.Name != "web" || a.Version != 2 || a.Host != "203.0.113.11" ||
		a.CSP != "aws" || a.Region != "ap-northeast-2" || a.Zone != "ap-northeast-2a" || a.NsID != "etri" ||
		a.InfraID != "travel" || !slices.Equal(a.Ports, []int{3000, 8080}) || !a.PortsOpened || a.PortsError != "" ||
		len(a.OpenedPorts) != 2 || a.URL != "" || a.RuntimeID != "" || a.DeployedAt.IsZero() {
		t.Errorf("app endpoint = %+v", a)
	}
	if !slices.Equal(a.URLs, []model.EndpointURL{
		{Port: 3000, URL: "http://203.0.113.11:3000"}, {Port: 8080, URL: "http://203.0.113.11:8080"},
	}) {
		t.Errorf("app urls = %+v", a.URLs)
	}
	if m.Type != model.NodeDeployTypeInference || m.RuntimeID != model.RuntimeOllama || m.ModelID != "tiny" ||
		m.ArtifactID != "gguf" || m.URL != "http://203.0.113.11:11434/v1" || m.HealthURL != "http://203.0.113.11:11434/" ||
		!slices.Equal(m.Ports, []int{11434}) || !m.PortsOpened || m.Name != "" || len(m.URLs) != 0 {
		t.Errorf("inference endpoint = %+v", m)
	}

	for filter, want := range map[EndpointFilter]int{
		{Type: model.NodeDeployTypeInference}:            1,
		{Type: model.NodeDeployTypeApp}:                  1,
		{NsID: "etri", InfraID: "travel", NodeID: "vm1"}: 2,
		{NodeID: "vm2"}:    0,
		{NsID: "other"}:    0,
		{InfraID: "other"}: 0,
		{NsID: "etri", Type: model.NodeDeployTypeApp}:        1,
		{NodeID: "vm1", Type: model.NodeDeployTypeInference}: 1,
	} {
		if got := len(endpointsOf(f, filter)); got != want {
			t.Errorf("filter %+v = %d endpoints, want %d", filter, got, want)
		}
	}
}

func TestRedeployingAWorkKeyKeepsOneEndpointOfTheNewDeployment(t *testing.T) {
	f := newFixture(t, false)
	inf := deployed(t, f, modelReq("vm1"))
	time.Sleep(2 * time.Millisecond)
	deployed(t, f, appReq("vm1", "vm2"))
	time.Sleep(2 * time.Millisecond)
	again := deployed(t, f, appReq("vm1"))
	if again.Status != model.NodeDeployRunning {
		t.Fatalf("redeploy = %q", again.Status)
	}
	list := endpointsOf(f, EndpointFilter{})
	got := endpointKeys(list)
	if len(got) != 3 || got[0] != "vm1 app-web "+again.ID || got[1] != "vm1 ms-ollama "+inf.ID || list[2].NodeID != "vm2" {
		t.Errorf("endpoints = %v, want the new app on vm1, the inference on vm1 and the older app on vm2", got)
	}
}

func TestATakenDownDeploymentLeavesNoEndpoint(t *testing.T) {
	f := newFixture(t, false)
	inf := deployed(t, f, modelReq("vm1"))
	time.Sleep(2 * time.Millisecond)
	app := deployed(t, f, appReq("vm1"))
	if _, err := f.svc.Remove(context.Background(), app.ID); err != nil {
		t.Fatal(err)
	}
	if done := waitRemoved(t, f.svc, app.ID); done.Status != model.NodeDeployRemoved {
		t.Fatalf("remove = %q", done.Status)
	}
	if got := endpointKeys(endpointsOf(f, EndpointFilter{})); !slices.Equal(got, []string{"vm1 ms-ollama " + inf.ID}) {
		t.Errorf("endpoints = %v, want the inference only", got)
	}
}

func TestTakingDownTheLatestDoesNotBringBackAnOlderDeployment(t *testing.T) {
	f := newFixture(t, false)
	deployed(t, f, appReq("vm1"))
	time.Sleep(2 * time.Millisecond)
	newer := deployed(t, f, appReq("vm1"))
	if _, err := f.svc.Remove(context.Background(), newer.ID); err != nil {
		t.Fatal(err)
	}
	waitRemoved(t, f.svc, newer.ID)
	if got := endpointsOf(f, EndpointFilter{}); len(got) != 0 {
		t.Errorf("endpoints = %v, want none: compose-down of the project stopped the older containers too", endpointKeys(got))
	}
}

func TestAFailedLatestDeploymentLeavesNoEndpoint(t *testing.T) {
	f := newFixture(t, false)
	older := deployed(t, f, modelReq("vm1", "vm2"))
	time.Sleep(2 * time.Millisecond)
	// On vm1 compose-up fails, so the project was recreated and the older
	// containers are gone. On vm2 install-docker fails before the service
	// is touched, so the older deployment still serves there.
	f.runner.statuses["203.0.113.11 "+PlaybookComposeUp] = semaphore.StatusError
	f.runner.statuses["198.51.100.12 "+PlaybookInstallDocker] = semaphore.StatusError
	failed := deployed(t, f, modelReq("vm1", "vm2"))
	if failed.Status != model.NodeDeployFailed {
		t.Fatalf("later = %q", failed.Status)
	}
	if got := endpointKeys(endpointsOf(f, EndpointFilter{})); !slices.Equal(got, []string{"vm2 ms-ollama " + older.ID}) {
		t.Errorf("endpoints = %v, want the older deployment on vm2 only", got)
	}
}

func TestDryRunDeploymentsHaveNoEndpoint(t *testing.T) {
	f := newFixture(t, true)
	if _, err := f.svc.Create(context.Background(), modelReq("vm1")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Create(context.Background(), appReq("vm1")); err != nil {
		t.Fatal(err)
	}
	if got := endpointsOf(f, EndpointFilter{}); len(got) != 0 {
		t.Errorf("endpoints = %v, want none", endpointKeys(got))
	}
}

func TestPortsNotOpenedAreFlagged(t *testing.T) {
	f := newFixture(t, false)
	f.tb.failRules = "sg-aws"
	d := deployed(t, f, appReq("vm1"))
	if d.Status != model.NodeDeployRunning {
		t.Fatalf("deploy = %q", d.Status)
	}
	list := endpointsOf(f, EndpointFilter{})
	if len(list) != 1 || list[0].PortsOpened || list[0].PortsError == "" || len(list[0].URLs) != 2 {
		t.Errorf("endpoints = %+v, want one with portsOpened false and portsError", list)
	}
}
