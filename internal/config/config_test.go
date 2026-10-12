package config

import (
	"strings"
	"testing"
)

// clearBackendEnv makes the test independent of the shell it runs in.
func clearBackendEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"AIAPP_RECOMMENDER", "AIAPP_RECOMMENDER_URL", "AIAPP_RECOMMENDER_TOKEN",
		"AIAPP_PROVISIONER", "AIAPP_PROVISIONER_URL", "AIAPP_PROVISIONER_TOKEN",
	} {
		t.Setenv(name, "")
	}
}

func TestBackendsDefaultToLocal(t *testing.T) {
	clearBackendEnv(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Recommender.Kind != BackendLocal || cfg.Provisioner.Kind != BackendLocal {
		t.Errorf("kinds = %q, %q, want local", cfg.Recommender.Kind, cfg.Provisioner.Kind)
	}
}

func TestRemoteBackendReadsURLAndToken(t *testing.T) {
	clearBackendEnv(t)
	t.Setenv("AIAPP_RECOMMENDER", "remote")
	t.Setenv("AIAPP_RECOMMENDER_URL", "http://placement.example/api/")
	t.Setenv("AIAPP_RECOMMENDER_TOKEN", "secret")
	t.Setenv("AIAPP_PROVISIONER", "Remote")
	t.Setenv("AIAPP_PROVISIONER_URL", "http://provision.example")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Recommender.Kind != BackendRemote || cfg.Recommender.URL != "http://placement.example/api" ||
		cfg.Recommender.Token != "secret" || cfg.Recommender.Timeout <= 0 {
		t.Errorf("recommender = %+v", cfg.Recommender)
	}
	if cfg.Provisioner.Kind != BackendRemote || cfg.Provisioner.Timeout != cfg.Tumblebug.Timeout {
		t.Errorf("provisioner = %+v", cfg.Provisioner)
	}
}

func TestRemoteBackendWithoutURLFails(t *testing.T) {
	for _, name := range []string{"AIAPP_RECOMMENDER", "AIAPP_PROVISIONER"} {
		t.Run(name, func(t *testing.T) {
			clearBackendEnv(t)
			t.Setenv(name, "remote")
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), name+"_URL") {
				t.Errorf("Load error = %v, want one naming %s_URL", err, name)
			}
		})
	}
}

func TestUnknownBackendFails(t *testing.T) {
	clearBackendEnv(t)
	t.Setenv("AIAPP_PROVISIONER", "khu")
	if _, err := Load(); err == nil {
		t.Error("Load accepted an unknown provisioner kind")
	}
}
