// Command ai-app-server serves the AI-App service: the AI application catalog,
// deployment and lifecycle through CB-Tumblebug, the tools as an MCP server, the
// operator console and the deploy and control archive.
//
// The automation engine runs as cmd/ai-automation-engine and reaches the tools
// over MCP. Natural language instructions are the partner institution's area;
// its agent, a mock under mock/partner/nlagent until the real one exists, calls
// the engine. Both are forwarded from here so that the console keeps one
// address.
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
	"github.com/ish-hcc/ai-agent-proto/internal/modelcat"
	"github.com/ish-hcc/ai-agent-proto/internal/rest"
	"github.com/ish-hcc/ai-agent-proto/internal/tools"
	"github.com/ish-hcc/ai-agent-proto/internal/tumblebug"
)

const shutdownTimeout = 10 * time.Second

// @title AI-MCMP AI-App Service (Prototype)
// @version 0.1.0
// @description Registers AI application metadata, deploys the application onto AI semiconductor nodes through CB-Tumblebug, publishes the same capabilities as MCP tools and archives every deploy and control call. Automation engine routes and the partner natural language agent routes are forwarded to their own processes.
// @license.name Apache 2.0
// @license.url http://www.apache.org/licenses/LICENSE-2.0.html
// @BasePath /aiapp
// @securityDefinitions.apikey BearerAuth
// @in header
// @name Authorization
// @description "Bearer <token>". Required on every route but readiness, the console and this documentation when AIAPP_AUTH_TOKEN is set.
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

	modelCatalog, loaded, err := modelcat.Open(cfg.Model.StoreFile)
	if err != nil {
		log.Error().Err(err).Msg("Failed to open AI model catalog")
		os.Exit(1)
	}
	if !loaded {
		if err := modelcat.Seed(ctx, modelCatalog); err != nil {
			log.Error().Err(err).Msg("Failed to seed AI model catalog")
			os.Exit(1)
		}
	}
	if cfg.Model.StoreFile == "" {
		log.Warn().Msg("AIAPP_MODEL_STORE_FILE is not set: the AI model catalog is kept in memory and registered models are lost on restart")
	}

	tumblebugClient := tumblebug.NewClient(cfg.Tumblebug)
	deployOptions := append(deploy.OptionsFromConfig(cfg.Recommender, cfg.Provisioner),
		deploy.WithHealthWait(cfg.Health.Timeout, cfg.Health.Interval))
	deployService := deploy.NewService(tumblebugClient, appCatalog, cfg.Tumblebug, deployOptions...)

	registry := tools.NewRegistry(cfg.Tumblebug.DryRun)
	tools.Register(registry, appCatalog, deployService)
	tools.RegisterModels(registry, modelCatalog)

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
	recommender := cfg.Recommender.URL
	if recommender == "" {
		recommender = config.BackendNone
	}
	log.Info().Str("recommender", recommender).
		Str("provisioner", cfg.Provisioner.Kind).Str("provisionerUrl", cfg.Provisioner.URL).
		Msg("Placement backends")
	if cfg.Recommender.URL == "" {
		log.Warn().Msg("AIAPP_RECOMMENDER_URL is not set: no partner recommender API, so every plan must pin specId and imageId")
	}
	if cfg.Tumblebug.DryRun {
		log.Warn().Msg("Dry run is on: deploy, control, update and delete calls are planned but never sent to the cloud; direct API calls are archived here, MCP calls by the calling agent")
	}
	if cfg.Auth.Token == "" {
		log.Warn().Msg("AIAPP_AUTH_TOKEN is not set: authentication is off and every route is open")
	}

	server := rest.NewAppServer(cfg, rest.AppDependencies{
		Catalog: appCatalog,
		Deploy:  deployService,
		Archive: archiveStore,
		Tools:   registry,
		Models:  modelCatalog,
	})
	rest.MarkReady()

	go func() {
		log.Info().Int("port", server.Port()).Str("basePath", rest.BasePath).
			Int("tools", len(registry.Names())).Str("engineUrl", cfg.Engine.URL).Str("partnerNlUrl", cfg.PartnerNL.URL).
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
