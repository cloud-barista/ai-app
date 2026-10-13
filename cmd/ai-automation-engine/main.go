// Command ai-automation-engine serves the AI application management automation
// agent of AI-Ops: the automatic deploy and control engine.
//
// It runs fixed workflows as ordered calls to the AI-App tools over MCP and
// uses no LLM. The workflows are published over REST and as MCP tools, and
// every run is archived apart from the AI-App archive.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/ish-hcc/ai-agent-proto/internal/archive"
	"github.com/ish-hcc/ai-agent-proto/internal/config"
	"github.com/ish-hcc/ai-agent-proto/internal/engine"
	"github.com/ish-hcc/ai-agent-proto/internal/mcpclient"
	"github.com/ish-hcc/ai-agent-proto/internal/redact"
	"github.com/ish-hcc/ai-agent-proto/internal/registration"
	"github.com/ish-hcc/ai-agent-proto/internal/rest"
)

const (
	shutdownTimeout = 10 * time.Second
	// probeTimeout bounds the startup connection attempt to the AI-App service.
	probeTimeout = 5 * time.Second
	// mcpClientName is how the engine introduces itself to the AI-App MCP server.
	mcpClientName = "ai-mcmp-app-automation-engine"
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

	runs, err := archive.NewLog[engine.Run](cfg.Engine.ArchiveDir)
	if err != nil {
		log.Error().Err(err).Msg("Failed to open archive")
		os.Exit(1)
	}

	appClient := mcpclient.NewNamed(mcpClientName, cfg.App.MCPURL, cfg.App.Token)
	automation := engine.New(appClient, runs, engine.WithHealthWait(cfg.Health.Timeout, cfg.Health.Interval))
	registry := engine.NewRegistry(automation)
	registrar := registration.New(cfg.Engine.RegistryURL, cfg.Engine.RegistryToken, engine.RegistryEnv)
	card := engine.NewCardBuilder(automation, registry, registrar)

	if cfg.Auth.Token == "" {
		log.Warn().Msg("AIAPP_AUTH_TOKEN is not set: authentication is off and every route is open")
	}

	// The AI-App service may come up after this one, so an unreachable tool
	// server is reported but does not stop startup. Each run connects again.
	probeCtx, cancelProbe := context.WithTimeout(context.Background(), probeTimeout)
	if err := appClient.Prepare(probeCtx); err != nil {
		log.Warn().Str("error", redact.Error(err, appClient.Endpoint())).
			Str("url", redact.URL(appClient.Endpoint())).
			Msg("AI-App MCP server is not reachable yet: workflows will report 503 until it is")
	}
	cancelProbe()

	server := rest.NewEngineServer(cfg, rest.EngineDependencies{
		Engine: automation,
		Card:   card,
		Tools:  registry,
	})
	rest.MarkReady()

	registerCtx, cancelRegister := context.WithCancel(context.Background())
	defer cancelRegister()
	if registrar.Enabled() {
		go registrar.Register(registerCtx, card.Subject())
	} else {
		log.Info().Msg(engine.RegistryEnv + " is not set: the engine card is not registered anywhere")
	}

	go func() {
		log.Info().Int("port", server.Port()).Str("basePath", rest.BasePath).
			Str("mcpUrl", redact.URL(appClient.Endpoint())).Strs("workflows", engine.Workflows).
			Int("tools", len(registry.Names())).
			Msg("Starting automation engine; it uses no LLM")
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
	_ = appClient.Close()
	log.Info().Msg("Automation engine stopped")
}
