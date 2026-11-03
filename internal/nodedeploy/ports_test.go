package nodedeploy

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/semaphore"
	"github.com/ish-hcc/ai-agent-proto/internal/tumblebug"
)

// rulesBySG maps each security group to the ports of the rule sent to it, and
// fails the test on a group sent more than once or a rule that is not one TCP
// inbound rule from anywhere.
func rulesBySG(t *testing.T, calls []rulesCall) map[string]string {
	t.Helper()
	out := make(map[string]string, len(calls))
	for _, c := range calls {
		if _, dup := out[c.SecurityGroupID]; dup {
			t.Errorf("security group %s was sent rules more than once", c.SecurityGroupID)
		}
		if len(c.Body.FirewallRules) != 1 {
			t.Fatalf("rules to %s = %+v, want one rule", c.SecurityGroupID, c.Body.FirewallRules)
		}
		r := c.Body.FirewallRules[0]
		if r.Direction != "inbound" || r.Protocol != "TCP" || r.CIDR != "0.0.0.0/0" {
			t.Errorf("rule to %s = %+v, want TCP inbound from 0.0.0.0/0", c.SecurityGroupID, r)
		}
		out[c.SecurityGroupID] = r.Ports
	}
	return out
}

func openedOf(n model.NodeDeployResult) map[string]string {
	out := map[string]string{}
	for _, o := range n.OpenedPorts {
		if o.Protocol != "TCP" {
			out["bad protocol"] = o.Protocol
		}
		out[o.SecurityGroupID+":"+strconv.Itoa(o.Port)] = o.Result
	}
	return out
}

func TestInferenceOpensTheRuntimePortInEverySecurityGroup(t *testing.T) {
	f := newFixture(t, false)
	created, err := f.svc.Create(context.Background(), modelReq("vm1", "vm2"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	d := waitFinished(t, f.svc, created.ID)
	if d.Status != model.NodeDeploySucceeded {
		t.Fatalf("status = %q, nodes = %+v", d.Status, d.Nodes)
	}

	got := rulesBySG(t, f.tb.rulesCalls())
	want := map[string]string{"sg-aws": "11434", "sg-azure-a": "11434", "sg-azure-b": "11434"}
	if !maps.Equal(got, want) {
		t.Errorf("rules sent = %v, want %v", got, want)
	}
	vm1, vm2 := d.Nodes[0], d.Nodes[1]
	if !slices.Equal(vm1.Ports, []int{11434}) || !slices.Equal(vm2.Ports, []int{11434}) {
		t.Errorf("ports = %v and %v, want the ollama port", vm1.Ports, vm2.Ports)
	}
	if o := openedOf(vm1); !maps.Equal(o, map[string]string{"sg-aws:11434": "added"}) {
		t.Errorf("vm1 opened = %v", o)
	}
	if o := openedOf(vm2); !maps.Equal(o, map[string]string{"sg-azure-a:11434": "added", "sg-azure-b:11434": "added"}) {
		t.Errorf("vm2 opened = %v", o)
	}
	if vm1.PortsError != "" || len(vm1.Notes) != 0 {
		t.Errorf("vm1 ports error %q notes %v", vm1.PortsError, vm1.Notes)
	}
}

func TestAppOpensItsUploadedPortsAndSkipsOpenOnes(t *testing.T) {
	f := newFixture(t, false)
	created, err := f.svc.Create(context.Background(), appReq("vm2"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	d := waitFinished(t, f.svc, created.ID)
	if d.Status != model.NodeDeploySucceeded {
		t.Fatalf("status = %q, nodes = %+v", d.Status, d.Nodes)
	}

	got := rulesBySG(t, f.tb.rulesCalls())
	want := map[string]string{"sg-azure-a": "3000,8080", "sg-azure-b": "3000"}
	if !maps.Equal(got, want) {
		t.Errorf("rules sent = %v, want %v (8080 is already open in sg-azure-b)", got, want)
	}
	n := d.Nodes[0]
	if !slices.Equal(n.Ports, []int{3000, 8080}) {
		t.Errorf("ports = %v, want the ports of app version 2", n.Ports)
	}
	wantOpened := map[string]string{
		"sg-azure-a:3000": "added", "sg-azure-a:8080": "added",
		"sg-azure-b:3000": "added", "sg-azure-b:8080": "existing",
	}
	if o := openedOf(n); !maps.Equal(o, wantOpened) {
		t.Errorf("opened = %v, want %v", o, wantOpened)
	}
}

func TestAppRequestPortsReplaceTheUploadedOnes(t *testing.T) {
	f := newFixture(t, false)
	req := appReq("vm1")
	req.App.Ports = []int{9443}
	created, err := f.svc.Create(context.Background(), req)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	d := waitFinished(t, f.svc, created.ID)
	if d.Status != model.NodeDeploySucceeded {
		t.Fatalf("status = %q, nodes = %+v", d.Status, d.Nodes)
	}
	if got := rulesBySG(t, f.tb.rulesCalls()); !maps.Equal(got, map[string]string{"sg-aws": "9443"}) {
		t.Errorf("rules sent = %v, want 9443 to sg-aws", got)
	}

	req.App.Ports = []int{70000}
	if _, err := f.svc.Create(context.Background(), req); err == nil || !strings.Contains(err.Error(), "app.ports") {
		t.Errorf("port 70000 = %v, want a validation error on app.ports", err)
	}
}

func TestDryRunOpensNoPort(t *testing.T) {
	f := newFixture(t, true)
	for _, req := range []model.NodeDeploymentReq{modelReq("vm1", "vm2"), appReq("vm2")} {
		d, err := f.svc.Create(context.Background(), req)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if d.Status != model.NodeDeployDryRun {
			t.Fatalf("status = %q", d.Status)
		}
		for _, n := range d.Nodes {
			if len(n.Ports) == 0 || len(n.OpenedPorts) != 0 || n.PortsError != "" {
				t.Errorf("%s: ports %v opened %v error %q, want the ports listed and none opened",
					n.Target.NodeID, n.Ports, n.OpenedPorts, n.PortsError)
			}
		}
	}
	if calls := f.tb.rulesCalls(); len(calls) != 0 {
		t.Errorf("rules sent in dry run = %+v, want none", calls)
	}
}

func TestPortsThatCannotBeOpenedLeaveTheNodeSucceeded(t *testing.T) {
	f := newFixture(t, false)
	f.tb.failRules = "sg-azure-a"
	created, err := f.svc.Create(context.Background(), modelReq("vm1", "vm2"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	d := waitFinished(t, f.svc, created.ID)
	if d.Status != model.NodeDeploySucceeded {
		t.Fatalf("status = %q, want succeeded although a port could not be opened; nodes = %+v", d.Status, d.Nodes)
	}
	vm1, vm2 := d.Nodes[0], d.Nodes[1]
	if vm1.PortsError != "" {
		t.Errorf("vm1 ports error = %q", vm1.PortsError)
	}
	if vm2.Status != model.NodeDeploySucceeded || !strings.Contains(vm2.PortsError, "sg-azure-a") ||
		!strings.Contains(vm2.PortsError, "cannot change") {
		t.Errorf("vm2 status %q ports error %q, want succeeded with the group and the reason", vm2.Status, vm2.PortsError)
	}
	if !slices.ContainsFunc(vm2.Notes, func(n string) bool { return strings.Contains(n, "portsError") }) {
		t.Errorf("vm2 notes = %v, want one pointing to portsError", vm2.Notes)
	}
	if o := openedOf(vm2); !maps.Equal(o, map[string]string{"sg-azure-b:11434": "added"}) {
		t.Errorf("vm2 opened = %v, want the group that could be changed", o)
	}

	line := readArchiveLine(t, f, created.ID)
	if !strings.Contains(line, `"portsError"`) || !strings.Contains(line, `"openedPorts"`) {
		t.Errorf("archive line holds no port outcome: %s", line)
	}
}

func TestFailedPlaybookOpensNoPort(t *testing.T) {
	f := newFixture(t, false)
	f.runner.statuses["203.0.113.11 "+PlaybookComposeUp] = semaphore.StatusError
	created, err := f.svc.Create(context.Background(), modelReq("vm1"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	d := waitFinished(t, f.svc, created.ID)
	if d.Status != model.NodeDeployFailed {
		t.Fatalf("status = %q", d.Status)
	}
	if calls := f.tb.rulesCalls(); len(calls) != 0 {
		t.Errorf("rules sent for a failed node = %+v, want none", calls)
	}
}

func TestPortOpeningOffOnlyNotes(t *testing.T) {
	f := newFixture(t, false)
	f.svc.opts.OpenPorts = false
	created, err := f.svc.Create(context.Background(), modelReq("vm1"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	d := waitFinished(t, f.svc, created.ID)
	if d.Status != model.NodeDeploySucceeded {
		t.Fatalf("status = %q", d.Status)
	}
	if calls := f.tb.rulesCalls(); len(calls) != 0 {
		t.Errorf("rules sent with port opening off = %+v", calls)
	}
	n := d.Nodes[0]
	if len(n.OpenedPorts) != 0 || len(n.Notes) != 1 || !strings.Contains(n.Notes[0], "AIAPP_DEPLOYMENT_OPEN_PORTS") {
		t.Errorf("opened %v notes %v, want none opened and a note", n.OpenedPorts, n.Notes)
	}
}

func TestOpensPort(t *testing.T) {
	rule := func(direction, protocol, port, cidr string) tumblebug.FirewallRuleInfo {
		return tumblebug.FirewallRuleInfo{Direction: direction, Protocol: protocol, Port: port, CIDR: cidr}
	}
	for _, tc := range []struct {
		rule tumblebug.FirewallRuleInfo
		port int
		want bool
	}{
		{rule("inbound", "TCP", "8000", "0.0.0.0/0"), 8000, true},
		{rule("inbound", "tcp", "8000-9000", "0.0.0.0/0"), 8080, true},
		{rule("inbound", "ALL", "1-65535", "0.0.0.0/0"), 3000, true},
		{rule("inbound", "TCP", "8000-9000", "0.0.0.0/0"), 9001, false},
		{rule("outbound", "TCP", "8000", "0.0.0.0/0"), 8000, false},
		{rule("inbound", "UDP", "8000", "0.0.0.0/0"), 8000, false},
		{rule("inbound", "TCP", "8000", "198.51.100.0/24"), 8000, false},
		{rule("inbound", "", "", "0.0.0.0/0"), 8000, false},
	} {
		if got := opensPort(tc.rule, tc.port); got != tc.want {
			t.Errorf("opensPort(%+v, %d) = %v, want %v", tc.rule, tc.port, got, tc.want)
		}
	}
}

// readArchiveLine reads the archive record of a deployment as JSON.
func readArchiveLine(t *testing.T, f *fixture, id string) string {
	t.Helper()
	record, err := f.archive.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
