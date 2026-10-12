package deploy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ish-hcc/ai-agent-proto/internal/config"
	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/tumblebug"
)

func TestLocalUpdateRunsInstallCommandsOnEveryNode(t *testing.T) {
	var sent tumblebug.InfraCmdReq
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.Method + " " + r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&sent)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[
			{"infraId":"vllm-lab-01","nodeId":"serving-1","nodeIp":"203.0.113.10",
			 "stderr":{"0":"","1":"pulled"},"err":""},
			{"infraId":"vllm-lab-01","nodeId":"serving-2","nodeIp":"203.0.113.11",
			 "stderr":{"1":"second","0":"first"},"err":"exit status 1"}]}`))
	}))
	t.Cleanup(srv.Close)

	client := tumblebug.NewClient(config.TumblebugConfig{BaseURL: srv.URL, Timeout: 5 * time.Second})
	app := &model.AppSpec{ID: "vllm-llama31-8b", Version: "0.2.0", Install: model.InstallSpec{
		Commands: []string{"docker pull vllm", "systemctl restart vllm"}, UserName: "cb-user", TimeoutMinutes: 15,
	}}

	result, err := (&localProvisioner{tumblebug: client}).Update(context.Background(), "default", "vllm-lab-01", app)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}

	if path != "POST /ns/default/cmd/infra/vllm-lab-01" {
		t.Errorf("called %s, want the CB-Tumblebug infra command route", path)
	}
	if len(sent.Command) != 2 || sent.Command[1] != "systemctl restart vllm" || sent.UserName != "cb-user" || sent.TimeoutMinutes != 15 {
		t.Errorf("command request = %+v", sent)
	}
	if result.InfraID != "vllm-lab-01" || result.AppVersion != "0.2.0" || len(result.Nodes) != 2 {
		t.Fatalf("result = %+v", result)
	}
	if !result.Nodes[0].Succeeded || result.Nodes[0].Stderr != "pulled" {
		t.Errorf("node 1 = %+v", result.Nodes[0])
	}
	if result.Nodes[1].Succeeded || result.Nodes[1].Error != "exit status 1" || result.Nodes[1].Stderr != "first\nsecond" {
		t.Errorf("node 2 = %+v", result.Nodes[1])
	}
	if result.Message != "vllm-llama31-8b 0.2.0 applied on 1 of 2 nodes of vllm-lab-01" {
		t.Errorf("message = %q", result.Message)
	}
}
