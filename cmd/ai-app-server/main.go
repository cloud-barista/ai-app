// Command ai-app-server serves the AI-App service: the AI application catalog,
// deployment and lifecycle through CB-Tumblebug, the tools as an MCP server, the
// operator console and the deploy and control archive.
//
// The automation agent runs as cmd/ai-agent and reaches the tools over MCP. Its
// routes are forwarded from here so that the console keeps one address.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/ish-hcc/ai-agent-proto/internal/archive"
	"github.com/ish-hcc/ai-agent-proto/internal/catalog"
	"github.com/ish-hcc/ai-agent-proto/internal/config"
	"github.com/ish-hcc/ai-agent-proto/internal/deploy"
	"github.com/ish-hcc/ai-agent-proto/internal/rest"
	"github.com/ish-hcc/ai-agent-proto/internal/tools"
	"github.com/ish-hcc/ai-agent-proto/internal/tumblebug"
)

const shutdownTimeout = 10 * time.Second

// @title AI-MCMP AI-App Service (Prototype)
// @version 0.1.0
// @description Registers AI application metadata, deploys the application onto AI semiconductor nodes through CB-Tumblebug, publishes the same capabilities as MCP tools and archives every deploy and control call. Natural language intents are forwarded to the automation agent.
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
	deployService := deploy.NewService(tumblebugClient, appCatalog, cfg.Tumblebug,
		deploy.OptionsFromConfig(cfg.Recommender, cfg.Provisioner)...)

	registry := tools.NewRegistry(cfg.Tumblebug.DryRun)
	tools.Register(registry, appCatalog, deployService)

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
	log.Info().Str("recommender", cfg.Recommender.Kind).Str("recommenderUrl", cfg.Recommender.URL).
		Str("provisioner", cfg.Provisioner.Kind).Str("provisionerUrl", cfg.Provisioner.URL).
		Msg("Placement backends")
	if cfg.Tumblebug.DryRun {
		log.Warn().Msg("Dry run is on: deploy, control and delete calls are planned and archived but never sent")
	}

	server := rest.NewAppServer(cfg, rest.AppDependencies{
		Catalog: appCatalog,
		Deploy:  deployService,
		Archive: archiveStore,
		Tools:   registry,
	})
	rest.MarkReady()

	go func() {
		log.Info().Int("port", server.Port()).Str("basePath", rest.BasePath).
			Int("tools", len(registry.Names())).Str("agentUrl", cfg.Agent.URL).
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
