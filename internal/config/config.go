// Package config loads runtime configuration from environment variables.
package config

import (
	"fmt"
	"os"
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
	// BackendLocal works against CB-Tumblebug directly.
	BackendLocal = "local"
	// BackendRemote calls an external placement service over HTTP.
	BackendRemote = "remote"
)

// recommenderTimeout bounds a remote recommendation. A recommendation reads
// catalogs and creates nothing, so it has no reason to take minutes.
const recommenderTimeout = time.Minute

// Config holds all runtime configuration for the service.
type Config struct {
	Server    ServerConfig
	Tumblebug TumblebugConfig
	LLM       LLMConfig
	Agent     AgentConfig
	App       AppConfig
	Archive   ArchiveConfig
	Auth      AuthConfig
	Health    HealthConfig
	// Recommender selects who picks the spec and image of a plan.
	Recommender BackendConfig
	// Provisioner selects who reviews, creates and updates Infras.
	Provisioner BackendConfig
}

// BackendConfig selects the local or a remote implementation of one AI-App
// placement step.
type BackendConfig struct {
	// Kind is BackendLocal or BackendRemote.
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

// LLMConfig holds the Anthropic Messages API settings used for planning.
type LLMConfig struct {
	BaseURL   string
	APIKey    string
	Model     string
	MaxTokens int
	Timeout   time.Duration
}

// AgentConfig holds the automation agent settings.
type AgentConfig struct {
	// MaxSteps caps tool-use iterations so a looping model cannot run forever.
	MaxSteps int
	// Port is the HTTP port the agent process listens on.
	Port int
	// URL is where the AI-App service forwards intent and agent requests.
	URL string
	// ArchiveDir holds the intent run archive, apart from the AI-App archive.
	ArchiveDir string
	// RegistryURL is the agent registry the agent card is posted to at startup.
	// Empty leaves registration off.
	RegistryURL string
	// RegistryToken is sent to the registry as a bearer token when set.
	RegistryToken string
}

// AppConfig holds how the agent reaches the AI-App service.
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

// ArchiveConfig holds the deployment/control archive location.
type ArchiveConfig struct {
	Dir string
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
	v.SetDefault("llm.base_url", "https://api.anthropic.com")
	v.SetDefault("llm.model", "claude-opus-5")
	v.SetDefault("llm.max_tokens", 16000)
	v.SetDefault("llm.timeout", 5*time.Minute)
	v.SetDefault("agent.max_steps", 12)
	v.SetDefault("agent.port", 8091)
	v.SetDefault("agent.url", "http://127.0.0.1:8091")
	v.SetDefault("agent.archive_dir", "./data/agent-archive")
	v.SetDefault("app.mcp_url", "http://127.0.0.1:8090/aiapp/mcp")
	v.SetDefault("archive.dir", "./data/archive")
	v.SetDefault("health.timeout", 15*time.Minute)
	v.SetDefault("health.interval", 20*time.Second)
	v.SetDefault("placement.recommender", BackendLocal)
	v.SetDefault("placement.provisioner", BackendLocal)

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
		"llm.base_url",
		"llm.api_key",
		"llm.model",
		"llm.max_tokens",
		"llm.timeout",
		"agent.max_steps",
		"agent.port",
		"agent.url",
		"agent.archive_dir",
		"agent.registry_url",
		"agent.registry_token",
		"app.mcp_url",
		"app.token",
		"archive.dir",
		"auth.token",
		"health.timeout",
		"health.interval",
	} {
		if err := v.BindEnv(key); err != nil {
			return nil, fmt.Errorf("failed to bind env for %q: %w", key, err)
		}
	}
	// The backend variables are bound by name. Nesting them under a
	// "recommender" key would not work: a set AIAPP_RECOMMENDER shadows every
	// key below "recommender" and they all read empty.
	for key, env := range map[string]string{
		"placement.recommender":       envPrefix + "_RECOMMENDER",
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
		LLM: LLMConfig{
			BaseURL:   strings.TrimRight(v.GetString("llm.base_url"), "/"),
			APIKey:    v.GetString("llm.api_key"),
			Model:     v.GetString("llm.model"),
			MaxTokens: v.GetInt("llm.max_tokens"),
			Timeout:   v.GetDuration("llm.timeout"),
		},
		Agent: AgentConfig{
			MaxSteps:      v.GetInt("agent.max_steps"),
			Port:          v.GetInt("agent.port"),
			URL:           strings.TrimRight(v.GetString("agent.url"), "/"),
			ArchiveDir:    v.GetString("agent.archive_dir"),
			RegistryURL:   strings.TrimRight(v.GetString("agent.registry_url"), "/"),
			RegistryToken: v.GetString("agent.registry_token"),
		},
		App: AppConfig{
			MCPURL: v.GetString("app.mcp_url"),
			Token:  v.GetString("app.token"),
		},
		Archive: ArchiveConfig{
			Dir: v.GetString("archive.dir"),
		},
		Auth: AuthConfig{
			Token: v.GetString("auth.token"),
		},
		Health: HealthConfig{
			Timeout:  v.GetDuration("health.timeout"),
			Interval: v.GetDuration("health.interval"),
		},
		Recommender: BackendConfig{
			Kind:    strings.ToLower(strings.TrimSpace(v.GetString("placement.recommender"))),
			URL:     strings.TrimRight(v.GetString("placement.recommender_url"), "/"),
			Token:   v.GetString("placement.recommender_token"),
			Timeout: recommenderTimeout,
		},
		Provisioner: BackendConfig{
			Kind:    strings.ToLower(strings.TrimSpace(v.GetString("placement.provisioner"))),
			URL:     strings.TrimRight(v.GetString("placement.provisioner_url"), "/"),
			Token:   v.GetString("placement.provisioner_token"),
			Timeout: v.GetDuration("tumblebug.timeout"),
		},
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
	if c.LLM.BaseURL == "" {
		return fmt.Errorf("%s_LLM_BASE_URL is required", envPrefix)
	}
	if c.LLM.Model == "" {
		return fmt.Errorf("%s_LLM_MODEL is required", envPrefix)
	}
	if c.LLM.MaxTokens <= 0 {
		return fmt.Errorf("invalid llm max tokens: %d", c.LLM.MaxTokens)
	}
	if c.LLM.Timeout <= 0 {
		return fmt.Errorf("invalid llm timeout: %s", c.LLM.Timeout)
	}
	if c.Agent.MaxSteps <= 0 {
		return fmt.Errorf("invalid agent max steps: %d", c.Agent.MaxSteps)
	}
	if c.Agent.Port <= 0 || c.Agent.Port > 65535 {
		return fmt.Errorf("invalid agent port: %d", c.Agent.Port)
	}
	if c.Agent.URL == "" {
		return fmt.Errorf("%s_AGENT_URL is required", envPrefix)
	}
	if c.Agent.ArchiveDir == "" {
		return fmt.Errorf("%s_AGENT_ARCHIVE_DIR is required", envPrefix)
	}
	if c.App.MCPURL == "" {
		return fmt.Errorf("%s_APP_MCP_URL is required", envPrefix)
	}
	if c.Archive.Dir == "" {
		return fmt.Errorf("%s_ARCHIVE_DIR is required", envPrefix)
	}
	if c.Health.Timeout <= 0 {
		return fmt.Errorf("invalid health timeout: %s", c.Health.Timeout)
	}
	if c.Health.Interval <= 0 {
		return fmt.Errorf("invalid health interval: %s", c.Health.Interval)
	}
	if err := c.Recommender.validate(envPrefix + "_RECOMMENDER"); err != nil {
		return err
	}
	if err := c.Provisioner.validate(envPrefix + "_PROVISIONER"); err != nil {
		return err
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
