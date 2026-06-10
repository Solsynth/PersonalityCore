package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"syscall"
	"time"

	"src.solsynth.dev/sosys/personality/internal/app"
	"src.solsynth.dev/sosys/personality/internal/config"
	"src.solsynth.dev/sosys/personality/internal/logging"
)

func main() {
	configPath := flag.String("config", os.Getenv("CONFIG_PATH"), "path to the main TOML config file")
	pretty := flag.Bool("pretty", os.Getenv("ZEROLOG_PRETTY") == "true", "enable pretty logging")
	flag.Parse()

	logging.Init(*pretty)

	cfg, err := config.Load(*configPath)
	if err != nil {
		logging.Log.Fatal().Err(err).Msg("failed to load config")
	}

	runtime, err := app.New(cfg)
	if err != nil {
		logging.Log.Fatal().Err(err).Msg("failed to create runtime")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := runtime.Start(ctx); err != nil {
		logging.Log.Fatal().Err(err).Msg("runtime failed")
	}

	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := runtime.Stop(shutdownCtx); err != nil {
		logging.Log.Error().Err(err).Msg("shutdown failed")
	}
}
