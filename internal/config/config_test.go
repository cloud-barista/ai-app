package config

import (
	"strings"
	"testing"
	"time"
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

func TestBackendsDefault(t *testing.T) {
	clearBackendEnv(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Recommender.Kind != BackendNone || cfg.Provisioner.Kind != BackendLocal {
		t.Errorf("kinds = %q, %q, want none and local", cfg.Recommender.Kind, cfg.Provisioner.Kind)
	}
}

func TestRecommenderKindIgnoresTheOldSetting(t *testing.T) {
	for _, kind := range []string{"local", "remote"} {
		t.Run(kind, func(t *testing.T) {
			clearBackendEnv(t)
			t.Setenv("AIAPP_RECOMMENDER", kind)
			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.Recommender.Kind != BackendNone {
				t.Errorf("kind = %q without a URL, want none", cfg.Recommender.Kind)
			}
		})
	}
}

func TestRemoteBackendReadsURLAndToken(t *testing.T) {
	clearBackendEnv(t)
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

func TestRemoteProvisionerWithoutURLFails(t *testing.T) {
	clearBackendEnv(t)
	t.Setenv("AIAPP_PROVISIONER", "remote")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "AIAPP_PROVISIONER_URL") {
		t.Errorf("Load error = %v, want one naming AIAPP_PROVISIONER_URL", err)
	}
}

func TestUnknownBackendFails(t *testing.T) {
	clearBackendEnv(t)
	t.Setenv("AIAPP_PROVISIONER", "partner")
	if _, err := Load(); err == nil {
		t.Error("Load accepted an unknown provisioner kind")
	}
}

func TestAuthTokensReadFromEnv(t *testing.T) {
	clearBackendEnv(t)
	t.Setenv("AIAPP_AUTH_TOKEN", "")
	t.Setenv("AIAPP_APP_TOKEN", "")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Auth.Token != "" || cfg.App.Token != "" {
		t.Errorf("tokens = %q, %q, want empty by default", cfg.Auth.Token, cfg.App.Token)
	}

	t.Setenv("AIAPP_AUTH_TOKEN", "served")
	t.Setenv("AIAPP_APP_TOKEN", "sent")
	cfg, err = Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Auth.Token != "served" || cfg.App.Token != "sent" {
		t.Errorf("tokens = %q, %q, want served, sent", cfg.Auth.Token, cfg.App.Token)
	}
}

func TestAgentRegistryReadsURLAndToken(t *testing.T) {
	t.Setenv("AIAPP_AGENT_REGISTRY_URL", "http://registry.example/api/")
	t.Setenv("AIAPP_AGENT_REGISTRY_TOKEN", "secret")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Agent.RegistryURL != "http://registry.example/api" || cfg.Agent.RegistryToken != "secret" {
		t.Errorf("agent = %+v", cfg.Agent)
	}
}

func TestHealthWaitDefaultsAndOverrides(t *testing.T) {
	clearBackendEnv(t)
	t.Setenv("AIAPP_HEALTH_TIMEOUT", "")
	t.Setenv("AIAPP_HEALTH_INTERVAL", "")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Health.Timeout != 15*time.Minute || cfg.Health.Interval != 20*time.Second {
		t.Errorf("health = %+v, want 15m and 20s by default", cfg.Health)
	}

	t.Setenv("AIAPP_HEALTH_TIMEOUT", "2m")
	t.Setenv("AIAPP_HEALTH_INTERVAL", "5s")
	cfg, err = Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Health.Timeout != 2*time.Minute || cfg.Health.Interval != 5*time.Second {
		t.Errorf("health = %+v, want 2m and 5s", cfg.Health)
	}

	t.Setenv("AIAPP_HEALTH_INTERVAL", "0s")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "health interval") {
		t.Errorf("Load error = %v, want one naming the health interval", err)
	}
}
