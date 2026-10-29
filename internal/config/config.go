// Package config loads runtime configuration from environment variables.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/viper"
)

const envPrefix = "AIAPP"

// DefaultTumblebugBaseURL is where the service looks for CB-Tumblebug when
// nothing says otherwise.
//
// It is a default rather than a required value so that the service starts with
// no configuration at all. Everything that does not touch a cloud - the
// application catalog, the matcher, the tool listing, the archive, the API
// documentation - works without CB-Tumblebug, and refusing to start took all of
// that away to enforce a setting the deployment path would have reported anyway.
// The startup log says which URL is in use and whether it came from the
// environment, so a service pointed at the wrong place says so on the first line
// instead of at the first deployment.
const DefaultTumblebugBaseURL = "http://localhost:1323/tumblebug"

// Backend kinds of the recommender and the provisioner.
const (
	// BackendLocal works against CB-Tumblebug directly. Only the provisioner
	// has a local implementation.
	BackendLocal = "local"
	// BackendRemote calls an external placement service over HTTP.
	BackendRemote = "remote"
	// BackendNone means no recommender API is configured, so a plan must pin
	// its spec and image.
	BackendNone = "none"
)

// recommenderTimeout bounds a remote recommendation. A recommendation reads
// catalogs and creates nothing, so it has no reason to take minutes.
const recommenderTimeout = time.Minute

// Config holds all runtime configuration for the service.
type Config struct {
	Server    ServerConfig
	Tumblebug TumblebugConfig
	Engine    EngineConfig
	// PartnerNL is where the partner natural language agent is reached.
	PartnerNL PartnerNLConfig
	App       AppConfig
	Archive   ArchiveConfig
	Model     ModelConfig
	AppStore  AppStoreConfig
	Auth      AuthConfig
	Health    HealthConfig
	// Recommender is the partner recommender API that picks the spec and image
	// of a plan. Its kind is remote when AIAPP_RECOMMENDER_URL is set and none
	// otherwise; infrastructure recommendation is not done in this service.
	Recommender BackendConfig
	// Provisioner selects who reviews, creates and updates Infras.
	Provisioner BackendConfig
	// Semaphore runs the playbooks that install onto a node.
	Semaphore SemaphoreConfig
	// Stage is the shared folder deployment files are placed in for Semaphore.
	Stage StageConfig
	// NodeDeploy holds where node deployment records are kept.
	NodeDeploy NodeDeployConfig
}

// BackendConfig selects the local or a remote implementation of one AI-App
// placement step.
type BackendConfig struct {
	// Kind is BackendLocal, BackendRemote or, for the recommender, BackendNone.
	Kind string
	// URL is the base URL of the remote service. Unused when Kind is local.
	URL string
	// Token is sent as a bearer token when set.
	Token string
	// Timeout bounds one remote call. The provisioner shares the CB-Tumblebug
	// timeout because a remote create blocks for as long as a local one does.
	Timeout time.Duration
}

// ServerConfig holds the HTTP settings of the AI-App service.
type ServerConfig struct {
	Port int
}

// TumblebugConfig holds CB-Tumblebug connection settings.
//
// Creating an Infra blocks until provisioning finishes, so Timeout must stay
// generous: cancelling mid-flight aborts provisioning and can leave billable
// orphan resources behind.
type TumblebugConfig struct {
	BaseURL          string
	Username         string
	Password         string
	Timeout          time.Duration
	CredentialHolder string
	// ImageNamespace holds the shared node image catalog, which is not the
	// namespace a deployment is created in.
	ImageNamespace string
	// SpecNamespace holds the shared node spec catalog. It is separate from
	// ImageNamespace because the two catalogs are separate resource types, even
	// though a default install keeps both in the same namespace.
	SpecNamespace string
	// DefaultOSType is the image family used when an application does not name one.
	DefaultOSType string
	// DryRun keeps every infrastructure-changing tool from reaching CB-Tumblebug.
	// It defaults to true so that an agent run can never provision by accident.
	DryRun bool
	// BaseURLFromDefault records that nothing configured BaseURL. The caller
	// reports it at startup: a service quietly pointed at a built-in address is
	// the one failure here that would otherwise only surface at deployment time.
	BaseURLFromDefault bool
}

// EngineConfig holds the automation engine settings: the AI application
// management automation agent of AI-Ops, which runs fixed workflows without a
// model.
type EngineConfig struct {
	// Port is the HTTP port the engine process listens on.
	Port int
	// URL is where the AI-App service forwards engine requests.
	URL string
	// ArchiveDir holds the engine run archive, apart from the other archives.
	ArchiveDir string
	// RegistryURL is the agent registry the engine card is posted to at
	// startup. Empty leaves registration off.
	RegistryURL string
	// RegistryToken is sent to the registry as a bearer token when set.
	RegistryToken string
}

// PartnerNLConfig holds where the AI-App service forwards /aiapp/partner/nl/*:
// the partner institution's natural language agent. Natural language
// interpretation is the partner institution's area; until its agent exists,
// this points at the mock of it.
type PartnerNLConfig struct {
	URL string
}

// AppConfig holds how the engine reaches the AI-App service.
type AppConfig struct {
	// MCPURL is the Streamable HTTP endpoint of the AI-App MCP server.
	MCPURL string
	// Token is sent as a bearer token to the AI-App MCP server when set.
	Token string
}

// AuthConfig holds the bearer token this process requires of its callers.
type AuthConfig struct {
	// Token is required on every route but readiness, the console and the API
	// documentation. Empty turns authentication off.
	Token string
}

// HealthConfig holds how long a deployment told to wait for its application
// keeps asking whether it answers, and how often.
type HealthConfig struct {
	Timeout  time.Duration
	Interval time.Duration
}

// SemaphoreConfig holds how the AI-App service reaches Semaphore UI, which runs
// the deployment playbooks against a node over SSH.
type SemaphoreConfig struct {
	// URL is the Semaphore base URL, without /api. Empty leaves node
	// deployment unavailable; everything else works.
	URL string
	// Token is a Semaphore API token. When set it is used instead of the
	// user name and password. Missing credentials are reported when a
	// deployment reaches Semaphore, not at startup, for the same reason the
	// CB-Tumblebug URL has a default.
	Token    string
	Username string
	Password string
	// Project is the Semaphore project deployments are registered in. It is
	// created when it does not exist.
	Project string
	// PlaybookDir is where the playbooks are mounted inside the Semaphore
	// container. It is registered as the project's repository.
	PlaybookDir string
	// PollInterval is how often a running task is asked for its status.
	PollInterval time.Duration
	// Timeout bounds one task, from start to a final status.
	Timeout time.Duration
}

// StageConfig holds the shared folder of deployment files.
type StageConfig struct {
	// Root is mounted at the same path in the AI-App and the Semaphore
	// containers, so a path under it means the same files to both. It must be
	// absolute because it is passed to Semaphore as is.
	Root string
}

// NodeDeployConfig holds where the records of deployments onto existing nodes
// are kept.
type NodeDeployConfig struct {
	// StoreFile is the JSON file every deployment record is written to after
	// each change and read from at startup. Empty keeps the records in memory,
	// so they are lost on restart.
	StoreFile string
}

// ArchiveConfig holds the deployment/control archive location.
type ArchiveConfig struct {
	Dir string
}

// ModelConfig holds where the AI model catalog is kept.
type ModelConfig struct {
	// StoreFile is the JSON file the model catalog is written to after every
	// change and read from at startup. The built-in models are seeded only
	// when the file does not exist yet. Empty keeps the catalog in memory, so
	// it starts from the built-in models on every start.
	StoreFile string
}

// AppStoreConfig holds where uploaded AI applications are kept.
type AppStoreConfig struct {
	// Dir is the directory every uploaded version is written under and read
	// back from at startup.
	Dir string
	// UploadMaxBytes bounds the script and files of one upload together.
	UploadMaxBytes int64
}

// Load reads configuration from environment variables and validates it.
func Load() (*Config, error) {
	v := viper.New()
	v.SetEnvPrefix(envPrefix)
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	v.SetDefault("server.port", 8090)
	v.SetDefault("tumblebug.base_url", DefaultTumblebugBaseURL)
	v.SetDefault("tumblebug.timeout", 20*time.Minute)
	v.SetDefault("tumblebug.credential_holder", "")
	v.SetDefault("tumblebug.image_namespace", "system")
	v.SetDefault("tumblebug.spec_namespace", "system")
	v.SetDefault("tumblebug.default_os_type", "ubuntu 22.04")
	v.SetDefault("tumblebug.dry_run", true)
	v.SetDefault("engine.port", 8091)
	v.SetDefault("engine.url", "http://127.0.0.1:8091")
	v.SetDefault("engine.archive_dir", "./data/engine-archive")
	v.SetDefault("mock_nl.url", "http://127.0.0.1:8093")
	v.SetDefault("app.mcp_url", "http://127.0.0.1:8090/aiapp/mcp")
	v.SetDefault("archive.dir", "./data/archive")
	v.SetDefault("app.store_dir", "./data/apps")
	v.SetDefault("app.upload_max_bytes", 256<<20)
	v.SetDefault("health.timeout", 15*time.Minute)
	v.SetDefault("health.interval", 20*time.Second)
	v.SetDefault("placement.provisioner", BackendLocal)
	v.SetDefault("semaphore.project", "aiapp")
	v.SetDefault("semaphore.playbook_dir", "/ansible")
	v.SetDefault("semaphore.poll_interval", 5*time.Second)
	v.SetDefault("semaphore.timeout", 30*time.Minute)
	v.SetDefault("stage.root", "/var/lib/aiapp/stage")

	// AutomaticEnv only resolves keys that viper already knows about.
	for _, key := range []string{
		"server.port",
		"tumblebug.base_url",
		"tumblebug.username",
		"tumblebug.password",
		"tumblebug.timeout",
		"tumblebug.credential_holder",
		"tumblebug.image_namespace",
		"tumblebug.spec_namespace",
		"tumblebug.default_os_type",
		"tumblebug.dry_run",
		"engine.port",
		"engine.url",
		"engine.archive_dir",
		"engine.registry_url",
		"engine.registry_token",
		"mock_nl.url",
		"app.mcp_url",
		"app.token",
		"archive.dir",
		"model.store_file",
		"app.store_dir",
		"app.upload_max_bytes",
		"auth.token",
		"health.timeout",
		"health.interval",
		"semaphore.url",
		"semaphore.token",
		"semaphore.username",
		"semaphore.password",
		"semaphore.project",
		"semaphore.playbook_dir",
		"semaphore.poll_interval",
		"semaphore.timeout",
		"stage.root",
		"deployment.store_file",
	} {
		if err := v.BindEnv(key); err != nil {
			return nil, fmt.Errorf("failed to bind env for %q: %w", key, err)
		}
	}
	// The backend variables are bound by name. Nesting them under a
	// "provisioner" key would not work: a set AIAPP_PROVISIONER shadows every
	// key below "provisioner" and they all read empty. AIAPP_RECOMMENDER is no
	// longer read: the recommender is remote exactly when its URL is set.
	for key, env := range map[string]string{
		"placement.recommender_url":   envPrefix + "_RECOMMENDER_URL",
		"placement.recommender_token": envPrefix + "_RECOMMENDER_TOKEN",
		"placement.provisioner":       envPrefix + "_PROVISIONER",
		"placement.provisioner_url":   envPrefix + "_PROVISIONER_URL",
		"placement.provisioner_token": envPrefix + "_PROVISIONER_TOKEN",
	} {
		if err := v.BindEnv(key, env); err != nil {
			return nil, fmt.Errorf("failed to bind env for %q: %w", key, err)
		}
	}

	cfg := &Config{
		Server: ServerConfig{
			Port: v.GetInt("server.port"),
		},
		Tumblebug: TumblebugConfig{
			BaseURL:          strings.TrimRight(v.GetString("tumblebug.base_url"), "/"),
			Username:         v.GetString("tumblebug.username"),
			Password:         v.GetString("tumblebug.password"),
			Timeout:          v.GetDuration("tumblebug.timeout"),
			CredentialHolder: v.GetString("tumblebug.credential_holder"),
			ImageNamespace:   v.GetString("tumblebug.image_namespace"),
			SpecNamespace:    v.GetString("tumblebug.spec_namespace"),
			DefaultOSType:    v.GetString("tumblebug.default_os_type"),
			DryRun:           v.GetBool("tumblebug.dry_run"),
			// AutomaticEnv resolves the key whether or not the variable is set,
			// so the environment is the thing to ask.
			BaseURLFromDefault: os.Getenv(envPrefix+"_TUMBLEBUG_BASE_URL") == "",
		},
		Engine: EngineConfig{
			Port:          v.GetInt("engine.port"),
			URL:           strings.TrimRight(v.GetString("engine.url"), "/"),
			ArchiveDir:    v.GetString("engine.archive_dir"),
			RegistryURL:   strings.TrimRight(v.GetString("engine.registry_url"), "/"),
			RegistryToken: v.GetString("engine.registry_token"),
		},
		PartnerNL: PartnerNLConfig{
			URL: strings.TrimRight(v.GetString("mock_nl.url"), "/"),
		},
		App: AppConfig{
			MCPURL: v.GetString("app.mcp_url"),
			Token:  v.GetString("app.token"),
		},
		Archive: ArchiveConfig{
			Dir: v.GetString("archive.dir"),
		},
		Model: ModelConfig{
			StoreFile: strings.TrimSpace(v.GetString("model.store_file")),
		},
		AppStore: AppStoreConfig{
			Dir:            strings.TrimSpace(v.GetString("app.store_dir")),
			UploadMaxBytes: v.GetInt64("app.upload_max_bytes"),
		},
		Auth: AuthConfig{
			Token: v.GetString("auth.token"),
		},
		Health: HealthConfig{
			Timeout:  v.GetDuration("health.timeout"),
			Interval: v.GetDuration("health.interval"),
		},
		Recommender: BackendConfig{
			URL:     strings.TrimRight(strings.TrimSpace(v.GetString("placement.recommender_url")), "/"),
			Token:   v.GetString("placement.recommender_token"),
			Timeout: recommenderTimeout,
		},
		Provisioner: BackendConfig{
			Kind:    strings.ToLower(strings.TrimSpace(v.GetString("placement.provisioner"))),
			URL:     strings.TrimRight(v.GetString("placement.provisioner_url"), "/"),
			Token:   v.GetString("placement.provisioner_token"),
			Timeout: v.GetDuration("tumblebug.timeout"),
		},
		Semaphore: SemaphoreConfig{
			URL:          strings.TrimRight(strings.TrimSpace(v.GetString("semaphore.url")), "/"),
			Token:        v.GetString("semaphore.token"),
			Username:     v.GetString("semaphore.username"),
			Password:     v.GetString("semaphore.password"),
			Project:      strings.TrimSpace(v.GetString("semaphore.project")),
			PlaybookDir:  strings.TrimRight(strings.TrimSpace(v.GetString("semaphore.playbook_dir")), "/"),
			PollInterval: v.GetDuration("semaphore.poll_interval"),
			Timeout:      v.GetDuration("semaphore.timeout"),
		},
		Stage: StageConfig{
			Root: filepath.Clean(strings.TrimSpace(v.GetString("stage.root"))),
		},
		NodeDeploy: NodeDeployConfig{
			StoreFile: strings.TrimSpace(v.GetString("deployment.store_file")),
		},
	}

	cfg.Recommender.Kind = BackendNone
	if cfg.Recommender.URL != "" {
		cfg.Recommender.Kind = BackendRemote
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) validate() error {
	if c.Server.Port <= 0 || c.Server.Port > 65535 {
		return fmt.Errorf("invalid server port: %d", c.Server.Port)
	}
	if c.Tumblebug.BaseURL == "" {
		return fmt.Errorf("%s_TUMBLEBUG_BASE_URL must not be set to an empty value", envPrefix)
	}
	if c.Tumblebug.Timeout <= 0 {
		return fmt.Errorf("invalid tumblebug timeout: %s", c.Tumblebug.Timeout)
	}
	if c.Tumblebug.ImageNamespace == "" {
		return fmt.Errorf("%s_TUMBLEBUG_IMAGE_NAMESPACE is required", envPrefix)
	}
	if c.Tumblebug.SpecNamespace == "" {
		return fmt.Errorf("%s_TUMBLEBUG_SPEC_NAMESPACE is required", envPrefix)
	}
	if c.Tumblebug.DefaultOSType == "" {
		return fmt.Errorf("%s_TUMBLEBUG_DEFAULT_OS_TYPE is required", envPrefix)
	}
	if c.Engine.Port <= 0 || c.Engine.Port > 65535 {
		return fmt.Errorf("invalid engine port: %d", c.Engine.Port)
	}
	if c.Engine.URL == "" {
		return fmt.Errorf("%s_ENGINE_URL is required", envPrefix)
	}
	if c.Engine.ArchiveDir == "" {
		return fmt.Errorf("%s_ENGINE_ARCHIVE_DIR is required", envPrefix)
	}
	if c.PartnerNL.URL == "" {
		return fmt.Errorf("%s_MOCK_NL_URL is required", envPrefix)
	}
	if c.App.MCPURL == "" {
		return fmt.Errorf("%s_APP_MCP_URL is required", envPrefix)
	}
	if c.Archive.Dir == "" {
		return fmt.Errorf("%s_ARCHIVE_DIR is required", envPrefix)
	}
	if c.AppStore.Dir == "" {
		return fmt.Errorf("%s_APP_STORE_DIR is required", envPrefix)
	}
	if c.AppStore.UploadMaxBytes <= 0 {
		return fmt.Errorf("%s_APP_UPLOAD_MAX_BYTES must be a positive number of bytes", envPrefix)
	}
	if c.Health.Timeout <= 0 {
		return fmt.Errorf("invalid health timeout: %s", c.Health.Timeout)
	}
	if c.Health.Interval <= 0 {
		return fmt.Errorf("invalid health interval: %s", c.Health.Interval)
	}
	if err := c.Provisioner.validate(envPrefix + "_PROVISIONER"); err != nil {
		return err
	}
	if err := c.Semaphore.validate(); err != nil {
		return err
	}
	if !filepath.IsAbs(c.Stage.Root) {
		return fmt.Errorf("%s_STAGE_ROOT must be an absolute path, got %q", envPrefix, c.Stage.Root)
	}
	return nil
}

func (s SemaphoreConfig) validate() error {
	if s.Project == "" {
		return fmt.Errorf("%s_SEMAPHORE_PROJECT is required", envPrefix)
	}
	if !strings.HasPrefix(s.PlaybookDir, "/") {
		return fmt.Errorf("%s_SEMAPHORE_PLAYBOOK_DIR must be an absolute path, got %q", envPrefix, s.PlaybookDir)
	}
	if s.PollInterval <= 0 {
		return fmt.Errorf("invalid semaphore poll interval: %s", s.PollInterval)
	}
	if s.Timeout <= 0 {
		return fmt.Errorf("invalid semaphore timeout: %s", s.Timeout)
	}
	return nil
}

func (b BackendConfig) validate(env string) error {
	switch b.Kind {
	case BackendLocal:
		return nil
	case BackendRemote:
		if b.URL == "" {
			return fmt.Errorf("%s_URL is required when %s=%s", env, env, BackendRemote)
		}
		return nil
	default:
		return fmt.Errorf("%s must be %s or %s, got %q", env, BackendLocal, BackendRemote, b.Kind)
	}
}
