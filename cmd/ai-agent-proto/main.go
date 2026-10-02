// Command ai-agent-proto serves the AI application deployment automation agent.
//
// It is the first-year prototype of two AI-MCMP work items owned by Innogrid:
// "AI application deployment and operation" and "AI application automation agent".
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/ish-hcc/ai-agent-proto/internal/agent"
	"github.com/ish-hcc/ai-agent-proto/internal/archive"
	"github.com/ish-hcc/ai-agent-proto/internal/catalog"
	"github.com/ish-hcc/ai-agent-proto/internal/config"
	"github.com/ish-hcc/ai-agent-proto/internal/deploy"
	"github.com/ish-hcc/ai-agent-proto/internal/llm"
	"github.com/ish-hcc/ai-agent-proto/internal/rest"
	"github.com/ish-hcc/ai-agent-proto/internal/tools"
	"github.com/ish-hcc/ai-agent-proto/internal/tumblebug"
)

const shutdownTimeout = 10 * time.Second

// @title AI-MCMP AI Application Automation Agent (Prototype)
// @version 0.1.0
// @description Registers AI application metadata, turns an operator instruction into CB-Tumblebug calls through an LLM, deploys the application onto AI semiconductor nodes and archives every step.
// @license.name Apache 2.0
// @license.url http://www.apache.org/licenses/LICENSE-2.0.html
// @BasePath /aiapp
func main() {
	ctx := context.Background()

	// The settings file is optional. Every setting has a default, so a checkout
	// with no file starts and serves everything that does not touch a cloud.
	if err := config.LoadEnvFile(config.DefaultEnvFile); err != nil {
		log.Error().Err(err).Str("file", config.DefaultEnvFile).Msg("Failed to read the settings file")
		os.Exit(1)
	}

	cfg, err := config.Load()
	if err != nil {
		log.Error().Err(err).Msg("Failed to load configuration")
		os.Exit(1)
	}

	archiveStore, err := archive.NewStore(cfg.Archive.Dir)
	if err != nil {
		log.Error().Err(err).Msg("Failed to open archive")
		os.Exit(1)
	}

	appCatalog := catalog.NewStore()
	if err := catalog.Seed(ctx, appCatalog); err != nil {
		log.Error().Err(err).Msg("Failed to seed AI application catalog")
		os.Exit(1)
	}

	tumblebugClient := tumblebug.NewClient(cfg.Tumblebug)
	deployService := deploy.NewService(tumblebugClient, appCatalog, cfg.Tumblebug)

	registry := tools.NewRegistry(cfg.Tumblebug.DryRun)
	tools.Register(registry, appCatalog, deployService)

	llmClient := llm.NewClient(cfg.LLM)
	agentService := agent.NewService(llmClient, registry, archiveStore, cfg.Agent)

	// Which CB-Tumblebug this talks to is worth one line at startup whatever the
	// answer, because a service pointed at the wrong platform behaves normally
	// until the first deployment and then fails somewhere far from the cause.
	if cfg.Tumblebug.BaseURLFromDefault {
		log.Warn().Str("url", cfg.Tumblebug.BaseURL).Str("source", "default").
			Msg("CB-Tumblebug base URL was not configured, so deployment calls will not reach a real platform")
	} else {
		log.Info().Str("url", cfg.Tumblebug.BaseURL).Str("source", "environment").
			Msg("Using CB-Tumblebug")
	}
	if !llmClient.Enabled() {
		// Everything except the natural language endpoint still works, so this is
		// a warning rather than a startup failure.
		log.Warn().Msg("Planning model is not configured: the intent endpoint will report 503")
	}
	if cfg.Tumblebug.DryRun {
		log.Warn().Msg("Dry run is on: deploy, control and delete calls are planned and archived but never sent")
	}

	server := rest.NewServer(cfg, rest.Dependencies{
		Catalog: appCatalog,
		Deploy:  deployService,
		Agent:   agentService,
		Archive: archiveStore,
		Tools:   registry,
	})
	rest.MarkReady()

	go func() {
		log.Info().Int("port", server.Port()).Str("basePath", rest.BasePath).
			Str("model", llmClient.Model()).Int("tools", len(registry.Names())).
			Msg("Starting server")
		if err := server.Start(); err != nil {
			log.Error().Err(err).Msg("Server stopped unexpectedly")
			os.Exit(1)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Error().Err(err).Msg("Graceful shutdown failed")
		os.Exit(1)
	}
	log.Info().Msg("Server stopped")
}
