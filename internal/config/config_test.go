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

func TestEngineAndPartnerNLDefaults(t *testing.T) {
	for _, env := range []string{"AIAPP_ENGINE_PORT", "AIAPP_ENGINE_URL", "AIAPP_ENGINE_ARCHIVE_DIR", "AIAPP_MOCK_NL_URL"} {
		t.Setenv(env, "")
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Engine.Port != 8091 || cfg.Engine.URL != "http://127.0.0.1:8091" || cfg.Engine.ArchiveDir != "./data/engine-archive" {
		t.Errorf("engine = %+v, want port 8091 and its archive", cfg.Engine)
	}
	if cfg.PartnerNL.URL != "http://127.0.0.1:8093" {
		t.Errorf("partner nl = %+v, want port 8093", cfg.PartnerNL)
	}

	t.Setenv("AIAPP_MOCK_NL_URL", "http://nl.example:8093/")
	cfg, err = Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PartnerNL.URL != "http://nl.example:8093" {
		t.Errorf("partner nl = %q, want the configured URL without the trailing slash", cfg.PartnerNL.URL)
	}

	t.Setenv("AIAPP_ENGINE_REGISTRY_URL", "http://registry.example/api/")
	t.Setenv("AIAPP_ENGINE_REGISTRY_TOKEN", "secret")
	cfg, err = Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Engine.RegistryURL != "http://registry.example/api" || cfg.Engine.RegistryToken != "secret" {
		t.Errorf("engine registry = %q, %q", cfg.Engine.RegistryURL, cfg.Engine.RegistryToken)
	}
}
