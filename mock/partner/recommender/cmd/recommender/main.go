// Command recommender serves the mock of the partner institution's
// infrastructure recommendation API.
//
// Requirement analysis and infrastructure recommendation are the partner
// institution's area. This mock is not part of the Innogrid prototype. The
// AI-App service no longer calls it; it is kept as an example of the
// recommendation contract.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/ish-hcc/ai-agent-proto/internal/config"
	"github.com/ish-hcc/ai-agent-proto/internal/tumblebug"
	"github.com/ish-hcc/ai-agent-proto/mock/partner/recommender"
)

const (
	defaultPort     = 8092
	portEnv         = "AIAPP_MOCK_RECOMMENDER_PORT"
	tokenEnv        = "AIAPP_MOCK_RECOMMENDER_TOKEN"
	shutdownTimeout = 10 * time.Second
)

func main() {
	if err := config.LoadEnvFile(config.DefaultEnvFile); err != nil {
		log.Error().Err(err).Str("file", config.DefaultEnvFile).Msg("Failed to read the settings file")
		os.Exit(1)
	}

	// Only the CB-Tumblebug settings are used; the rest of the AI-App
	// configuration is loaded for its defaults and validation.
	cfg, err := config.Load()
	if err != nil {
		log.Error().Err(err).Msg("Failed to load configuration")
		os.Exit(1)
	}

	port, err := portFromEnv()
	if err != nil {
		log.Error().Err(err).Msg("Invalid port")
		os.Exit(1)
	}
	token := os.Getenv(tokenEnv)

	rec := recommender.New(tumblebug.NewClient(cfg.Tumblebug), cfg.Tumblebug)
	server := &http.Server{
		Addr:              fmt.Sprintf(":%d", port),
		Handler:           recommender.NewHandler(rec, token),
		ReadHeaderTimeout: 10 * time.Second,
	}

	log.Info().Int("port", port).Str("tumblebugUrl", cfg.Tumblebug.BaseURL).
		Str("specNamespace", cfg.Tumblebug.SpecNamespace).Str("imageNamespace", cfg.Tumblebug.ImageNamespace).
		Bool("auth", token != "").
		Msg("Partner institution recommender (mock implementation) starting; not part of the Innogrid prototype")
	if cfg.Tumblebug.BaseURLFromDefault {
		log.Warn().Str("url", cfg.Tumblebug.BaseURL).
			Msg("CB-Tumblebug base URL was not configured, so recommendations will not reach a real platform")
	}

	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error().Err(err).Msg("Server stopped unexpectedly")
			os.Exit(1)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig

	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		log.Error().Err(err).Msg("Graceful shutdown failed")
		os.Exit(1)
	}
	log.Info().Msg("Partner institution recommender (mock implementation) stopped")
}

func portFromEnv() (int, error) {
	raw := os.Getenv(portEnv)
	if raw == "" {
		return defaultPort, nil
	}
	port, err := strconv.Atoi(raw)
	if err != nil || port <= 0 || port > 65535 {
		return 0, fmt.Errorf("%s must be a port number, got %q", portEnv, raw)
	}
	return port, nil
}
