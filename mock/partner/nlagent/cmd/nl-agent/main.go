// Command nl-agent serves the partner institution's natural language agent;
// mock implementation for integration, not part of the Innogrid prototype.
//
// It turns an operator instruction into calls to the tools of the Innogrid
// automation engine through an LLM. The tools are reached over the engine MCP
// server, so this process holds no catalog and no CB-Tumblebug client; it keeps
// only the archive of its own runs. Its settings are read here, apart from the
// Innogrid configuration.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/ish-hcc/ai-agent-proto/internal/archive"
	"github.com/ish-hcc/ai-agent-proto/internal/config"
	"github.com/ish-hcc/ai-agent-proto/internal/mcpclient"
	"github.com/ish-hcc/ai-agent-proto/internal/redact"
	"github.com/ish-hcc/ai-agent-proto/mock/partner/nlagent"
	"github.com/ish-hcc/ai-agent-proto/mock/partner/nlagent/agent"
	"github.com/ish-hcc/ai-agent-proto/mock/partner/nlagent/card"
	"github.com/ish-hcc/ai-agent-proto/mock/partner/nlagent/llm"
)

const (
	shutdownTimeout = 10 * time.Second
	// probeTimeout bounds the startup connection attempt to the engine.
	probeTimeout = 5 * time.Second
	// mcpClientName is how the mock introduces itself to the engine MCP server.
	mcpClientName = "partner-mock-nl-agent"

	defaultPort         = 8093
	defaultEngineMCPURL = "http://127.0.0.1:8091/aiapp/engine/mcp"
	defaultArchiveDir   = "./data/partner-nl-archive"
	defaultMaxSteps     = 12
	defaultLLMBaseURL   = "https://api.anthropic.com"
	defaultLLMModel     = "claude-opus-5"
	defaultLLMMaxTokens = 16000
	defaultLLMTimeout   = 5 * time.Minute
	envPort             = "AIAPP_MOCK_NL_PORT"
	envEngineMCPURL     = "AIAPP_MOCK_NL_ENGINE_MCP_URL"
	envEngineToken      = "AIAPP_MOCK_NL_ENGINE_TOKEN"
	envAppToken         = "AIAPP_APP_TOKEN"
	envAuthToken        = "AIAPP_AUTH_TOKEN"
	envArchiveDir       = "AIAPP_MOCK_NL_ARCHIVE_DIR"
	envMaxSteps         = "AIAPP_MOCK_NL_MAX_STEPS"
	envRegistryToken    = "AIAPP_MOCK_NL_REGISTRY_TOKEN"
	envLLMBaseURL       = "AIAPP_LLM_BASE_URL"
	envLLMAPIKey        = "AIAPP_LLM_API_KEY"
	envLLMModel         = "AIAPP_LLM_MODEL"
	envLLMMaxTokens     = "AIAPP_LLM_MAX_TOKENS"
	envLLMTimeout       = "AIAPP_LLM_TIMEOUT"

	// subject is how the logs name this process.
	subject = agent.Marker + " Partner institution natural language agent (mock implementation)"
)

// settings are the mock's own settings, read from the environment.
type settings struct {
	port          int
	engineMCPURL  string
	engineToken   string
	authToken     string
	archiveDir    string
	maxSteps      int
	registryURL   string
	registryToken string
	llm           llm.Config
}

func main() {
	if err := config.LoadEnvFile(config.DefaultEnvFile); err != nil {
		log.Error().Err(err).Str("file", config.DefaultEnvFile).Msg("Failed to read the settings file")
		os.Exit(1)
	}

	s, err := loadSettings()
	if err != nil {
		log.Error().Err(err).Msg("Failed to load configuration")
		os.Exit(1)
	}

	runs, err := archive.NewLog[agent.Run](s.archiveDir)
	if err != nil {
		log.Error().Err(err).Msg("Failed to open archive")
		os.Exit(1)
	}

	llmClient := llm.NewClient(s.llm)
	toolSource := mcpclient.NewNamed(mcpClientName, s.engineMCPURL, s.engineToken)
	service := agent.NewService(llmClient, toolSource, runs, s.maxSteps)
	registrar := card.NewRegistrar(s.registryURL, s.registryToken)
	agentCard := card.New(toolSource, s.llm, registrar)

	if !llmClient.Enabled() {
		log.Warn().Msg(agent.Marker + " Planning model is not configured: the intent endpoint will report 503")
	}
	if s.authToken == "" {
		log.Warn().Msg(envAuthToken + " is not set: authentication is off and every route is open")
	}

	// The engine may come up after this one, so an unreachable tool server is
	// reported but does not stop startup. Each run connects again.
	probeCtx, cancelProbe := context.WithTimeout(context.Background(), probeTimeout)
	if err := toolSource.Prepare(probeCtx); err != nil {
		log.Warn().Str("error", redact.Error(err, toolSource.Endpoint())).
			Str("url", redact.URL(toolSource.Endpoint())).Msg(agent.Marker + " Automation engine MCP server is not reachable yet: intents will report 503 until it is")
	}
	cancelProbe()

	handler, e := nlagent.NewHandler(service, agentCard, s.authToken)
	server := &http.Server{
		Addr:              fmt.Sprintf(":%d", s.port),
		Handler:           e,
		ReadHeaderTimeout: 10 * time.Second,
	}
	handler.MarkReady()

	registerCtx, cancelRegister := context.WithCancel(context.Background())
	defer cancelRegister()
	if registrar.Enabled() {
		go registrar.Register(registerCtx, agentCard)
	} else {
		log.Info().Msg(card.RegistryEnv + " is not set: the agent card is not registered anywhere")
	}

	go func() {
		log.Info().Int("port", s.port).Str("prefix", nlagent.Prefix).
			Str("model", llmClient.Model()).Str("mcpUrl", redact.URL(toolSource.Endpoint())).
			Msg(subject + " starting; not part of the Innogrid prototype")
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error().Err(err).Msg("Server stopped unexpectedly")
			os.Exit(1)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	cancelRegister()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Error().Err(err).Msg("Graceful shutdown failed")
		os.Exit(1)
	}
	_ = toolSource.Close()
	log.Info().Msg(subject + " stopped")
}

func loadSettings() (*settings, error) {
	s := &settings{
		engineMCPURL:  envOr(envEngineMCPURL, defaultEngineMCPURL),
		engineToken:   os.Getenv(envEngineToken),
		authToken:     os.Getenv(envAuthToken),
		archiveDir:    envOr(envArchiveDir, defaultArchiveDir),
		registryURL:   strings.TrimRight(os.Getenv(card.RegistryEnv), "/"),
		registryToken: os.Getenv(envRegistryToken),
		llm: llm.Config{
			BaseURL: strings.TrimRight(envOr(envLLMBaseURL, defaultLLMBaseURL), "/"),
			APIKey:  os.Getenv(envLLMAPIKey),
			Model:   envOr(envLLMModel, defaultLLMModel),
		},
	}
	// The engine accepts the token the AI-App service shares with it, so the
	// token used toward the AI-App service is the fallback.
	if s.engineToken == "" {
		s.engineToken = os.Getenv(envAppToken)
	}

	var err error
	if s.port, err = intEnv(envPort, defaultPort); err != nil {
		return nil, err
	}
	if s.port <= 0 || s.port > 65535 {
		return nil, fmt.Errorf("%s must be a port number, got %d", envPort, s.port)
	}
	if s.maxSteps, err = intEnv(envMaxSteps, defaultMaxSteps); err != nil {
		return nil, err
	}
	if s.maxSteps <= 0 {
		return nil, fmt.Errorf("%s must be positive, got %d", envMaxSteps, s.maxSteps)
	}
	if s.llm.MaxTokens, err = intEnv(envLLMMaxTokens, defaultLLMMaxTokens); err != nil {
		return nil, err
	}
	if s.llm.MaxTokens <= 0 {
		return nil, fmt.Errorf("%s must be positive, got %d", envLLMMaxTokens, s.llm.MaxTokens)
	}
	s.llm.Timeout = defaultLLMTimeout
	if raw := os.Getenv(envLLMTimeout); raw != "" {
		if s.llm.Timeout, err = time.ParseDuration(raw); err != nil || s.llm.Timeout <= 0 {
			return nil, fmt.Errorf("%s must be a positive duration, got %q", envLLMTimeout, raw)
		}
	}
	return s, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func intEnv(key string, fallback int) (int, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be a whole number, got %q", key, raw)
	}
	return n, nil
}
