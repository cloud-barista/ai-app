package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/ish-hcc/ai-agent-proto/internal/config"
	"github.com/ish-hcc/ai-agent-proto/internal/infrarec"
	"github.com/ish-hcc/ai-agent-proto/internal/tumblebug"
)

func main() {
	if err := run(); err != nil {
		log.Error().Err(err).Msg("KHU infrastructure recommender stopped")
		os.Exit(1)
	}
}

func run() error {
	if err := config.LoadEnvFile(config.DefaultEnvFile); err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	address := strings.TrimSpace(os.Getenv("AIAPP_KHU_REC_ADDR"))
	if address == "" {
		address = "127.0.0.1:8094"
	}
	recommender := infrarec.New(tumblebug.NewClient(cfg.Tumblebug), cfg.Tumblebug)
	server := &http.Server{
		Addr:              address,
		Handler:           infrarec.NewHandler(recommender, cfg.Auth.Token),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan error, 1)
	go func() {
		done <- server.ListenAndServe()
	}()
	log.Info().Str("address", address).Msg("KHU infrastructure recommender started")
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return server.Shutdown(shutdown)
	}
}
