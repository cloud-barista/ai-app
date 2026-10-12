// Command ai-agent serves the AI application automation agent.
//
// It turns an operator instruction into tool calls through an LLM. The tools
// belong to the AI-App service and are reached over MCP, so this process holds
// no catalog and no CB-Tumblebug client of its own; it keeps only the archive of
// its own runs.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/ish-hcc/ai-agent-proto/internal/agent"
	"github.com/ish-hcc/ai-agent-proto/internal/agentcard"
	"github.com/ish-hcc/ai-agent-proto/internal/archive"
	"github.com/ish-hcc/ai-agent-proto/internal/config"
	"github.com/ish-hcc/ai-agent-proto/internal/llm"
	"github.com/ish-hcc/ai-agent-proto/internal/mcpclient"
	"github.com/ish-hcc/ai-agent-proto/internal/redact"
	"github.com/ish-hcc/ai-agent-proto/internal/rest"
)

const (
	shutdownTimeout = 10 * time.Second
	// probeTimeout bounds the startup connection attempt to the AI-App service.
	probeTimeout = 5 * time.Second
)

func main() {
	if err := config.LoadEnvFile(config.DefaultEnvFile); err != nil {
		log.Error().Err(err).Str("file", config.DefaultEnvFile).Msg("Failed to read the settings file")
		os.Exit(1)
	}

	cfg, err := config.Load()
	if err != nil {
		log.Error().Err(err).Msg("Failed to load configuration")
		os.Exit(1)
	}

	archiveStore, err := archive.NewStore(cfg.Agent.ArchiveDir)
	if err != nil {
		log.Error().Err(err).Msg("Failed to open archive")
		os.Exit(1)
	}

	llmClient := llm.NewClient(cfg.LLM)
	toolSource := mcpclient.New(cfg.App.MCPURL, cfg.App.Token)
	agentService := agent.NewService(llmClient, toolSource, archiveStore, cfg.Agent)
	registrar := agentcard.NewRegistrar(cfg.Agent.RegistryURL, cfg.Agent.RegistryToken)
	card := agentcard.New(toolSource, cfg.LLM, registrar)

	if !llmClient.Enabled() {
		log.Warn().Msg("Planning model is not configured: the intent endpoint will report 503")
	}
	if cfg.Auth.Token == "" {
		log.Warn().Msg("AIAPP_AUTH_TOKEN is not set: authentication is off and every route is open")
	}

	// The AI-App service may come up after this one, so an unreachable tool
	// server is reported but does not stop startup. Each run connects again.
	probeCtx, cancelProbe := context.WithTimeout(context.Background(), probeTimeout)
	if err := toolSource.Prepare(probeCtx); err != nil {
		log.Warn().Str("error", redact.Error(err, toolSource.Endpoint())).
			Str("url", redact.URL(toolSource.Endpoint())).
			Msg("AI-App MCP server is not reachable yet: intents will report 503 until it is")
	}
	cancelProbe()

	server := rest.NewAgentServer(cfg, rest.AgentDependencies{
		Agent:   agentService,
		Archive: archiveStore,
		Card:    card,
	})
	rest.MarkReady()

	// Registration connects to the AI-App MCP server first, retrying, so the
	// card it sends lists the tools even when that service came up late. It
	// retries on its own and never stops the agent.
	registerCtx, cancelRegister := context.WithCancel(context.Background())
	defer cancelRegister()
	if registrar.Enabled() {
		go registrar.Register(registerCtx, card)
	} else {
		log.Info().Msg("AIAPP_AGENT_REGISTRY_URL is not set: the agent card is not registered anywhere")
	}

	go func() {
		log.Info().Int("port", server.Port()).Str("basePath", rest.BasePath).
			Str("model", llmClient.Model()).Str("mcpUrl", toolSource.Endpoint()).
			Msg("Starting agent")
		if err := server.Start(); err != nil {
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
	log.Info().Msg("Agent stopped")
}
