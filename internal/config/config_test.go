package config

import (
	"strings"
	"testing"
	"time"
)

func TestAuthTokensReadFromEnv(t *testing.T) {
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

func TestModelStoreFile(t *testing.T) {
	t.Setenv("AIAPP_MODEL_STORE_FILE", "")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Model.StoreFile != "" {
		t.Errorf("model store file = %q, want empty (memory only) by default", cfg.Model.StoreFile)
	}

	t.Setenv("AIAPP_MODEL_STORE_FILE", " ./data/models.json ")
	cfg, err = Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Model.StoreFile != "./data/models.json" {
		t.Errorf("model store file = %q, want ./data/models.json", cfg.Model.StoreFile)
	}
}

func TestDeploymentStoreFile(t *testing.T) {
	t.Setenv("AIAPP_DEPLOYMENT_STORE_FILE", "")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.NodeDeploy.StoreFile != "" {
		t.Errorf("deployment store file = %q, want empty (memory only) by default", cfg.NodeDeploy.StoreFile)
	}

	t.Setenv("AIAPP_DEPLOYMENT_STORE_FILE", " ./data/deployments.json ")
	cfg, err = Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.NodeDeploy.StoreFile != "./data/deployments.json" {
		t.Errorf("deployment store file = %q, want ./data/deployments.json", cfg.NodeDeploy.StoreFile)
	}
}

func TestAppStoreDefaultsAndOverrides(t *testing.T) {
	t.Setenv("AIAPP_APP_STORE_DIR", "")
	t.Setenv("AIAPP_APP_UPLOAD_MAX_BYTES", "")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.AppStore.Dir != "./data/apps" || cfg.AppStore.UploadMaxBytes != 256<<20 {
		t.Errorf("app store = %+v, want ./data/apps and 256 MiB by default", cfg.AppStore)
	}

	t.Setenv("AIAPP_APP_STORE_DIR", " /srv/aiapp/apps ")
	t.Setenv("AIAPP_APP_UPLOAD_MAX_BYTES", "1048576")
	cfg, err = Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.AppStore.Dir != "/srv/aiapp/apps" || cfg.AppStore.UploadMaxBytes != 1<<20 {
		t.Errorf("app store = %+v, want /srv/aiapp/apps and 1 MiB", cfg.AppStore)
	}

	for _, bad := range []string{"0", "-1", "256MiB"} {
		t.Setenv("AIAPP_APP_UPLOAD_MAX_BYTES", bad)
		if _, err := Load(); err == nil {
			t.Errorf("upload bound %q accepted", bad)
		}
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

// clearSemaphoreEnv makes the test independent of the shell it runs in.
func clearSemaphoreEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"AIAPP_SEMAPHORE_URL", "AIAPP_SEMAPHORE_TOKEN", "AIAPP_SEMAPHORE_USERNAME", "AIAPP_SEMAPHORE_PASSWORD",
		"AIAPP_SEMAPHORE_PROJECT", "AIAPP_SEMAPHORE_PLAYBOOK_DIR", "AIAPP_SEMAPHORE_POLL_INTERVAL",
		"AIAPP_SEMAPHORE_TIMEOUT", "AIAPP_STAGE_ROOT",
	} {
		t.Setenv(name, "")
	}
}

func TestSemaphoreAndStageDefaults(t *testing.T) {
	clearSemaphoreEnv(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := SemaphoreConfig{Project: "aiapp", PlaybookDir: "/ansible", PollInterval: 5 * time.Second, Timeout: 30 * time.Minute}
	if cfg.Semaphore != want {
		t.Errorf("semaphore = %+v, want %+v", cfg.Semaphore, want)
	}
	if cfg.Stage.Root != "/var/lib/aiapp/stage" {
		t.Errorf("stage root = %q, want /var/lib/aiapp/stage", cfg.Stage.Root)
	}
}

func TestSemaphoreAndStageOverrides(t *testing.T) {
	clearSemaphoreEnv(t)
	t.Setenv("AIAPP_SEMAPHORE_URL", "http://semaphore:3000/")
	t.Setenv("AIAPP_SEMAPHORE_USERNAME", "admin")
	t.Setenv("AIAPP_SEMAPHORE_PASSWORD", "pw")
	t.Setenv("AIAPP_SEMAPHORE_PROJECT", "etri")
	t.Setenv("AIAPP_SEMAPHORE_PLAYBOOK_DIR", "/playbooks/")
	t.Setenv("AIAPP_SEMAPHORE_POLL_INTERVAL", "2s")
	t.Setenv("AIAPP_SEMAPHORE_TIMEOUT", "1h")
	t.Setenv("AIAPP_STAGE_ROOT", "/srv/stage/")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := SemaphoreConfig{
		URL: "http://semaphore:3000", Username: "admin", Password: "pw", Project: "etri",
		PlaybookDir: "/playbooks", PollInterval: 2 * time.Second, Timeout: time.Hour,
	}
	if cfg.Semaphore != want {
		t.Errorf("semaphore = %+v, want %+v", cfg.Semaphore, want)
	}
	if cfg.Stage.Root != "/srv/stage" {
		t.Errorf("stage root = %q, want /srv/stage", cfg.Stage.Root)
	}

	t.Setenv("AIAPP_SEMAPHORE_USERNAME", "")
	t.Setenv("AIAPP_SEMAPHORE_TOKEN", "tok")
	cfg, err = Load()
	if err != nil {
		t.Fatalf("Load with a token: %v", err)
	}
	if cfg.Semaphore.Token != "tok" {
		t.Errorf("token = %q, want tok", cfg.Semaphore.Token)
	}
}

func TestSemaphoreAndStageInvalid(t *testing.T) {
	cases := map[string]struct {
		env  map[string]string
		want string
	}{
		"zero poll interval":    {map[string]string{"AIAPP_SEMAPHORE_POLL_INTERVAL": "0s"}, "poll interval"},
		"zero timeout":          {map[string]string{"AIAPP_SEMAPHORE_TIMEOUT": "0s"}, "semaphore timeout"},
		"relative playbook dir": {map[string]string{"AIAPP_SEMAPHORE_PLAYBOOK_DIR": "ansible"}, "AIAPP_SEMAPHORE_PLAYBOOK_DIR"},
		"relative stage root":   {map[string]string{"AIAPP_STAGE_ROOT": "data/stage"}, "AIAPP_STAGE_ROOT"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			clearSemaphoreEnv(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Load error = %v, want one containing %q", err, tc.want)
			}
		})
	}
}
