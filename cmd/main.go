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

	logging.Log.Info().
		Str("config_path", *configPath).
		Bool("pretty", *pretty).
		Msg("PersonalityCore starting")

	if *configPath == "" {
		logging.Log.Fatal().Msg("config path is empty; set --config flag or CONFIG_PATH env")
	}

	logging.Log.Info().Msg("loading config...")
	cfg, err := config.Load(*configPath)
	if err != nil {
		logging.Log.Fatal().Err(err).Str("path", *configPath).Msg("failed to load config")
	}
	logging.Log.Info().
		Int("providers", len(cfg.Providers)).
		Int("agents", len(cfg.Agents.Items)).
		Str("http_port", cfg.HTTP.Port).
		Str("grpc_port", cfg.GRPC.Port).
		Msg("config loaded")

	logging.Log.Info().Msg("creating runtime...")
	runtime, err := app.New(cfg)
	if err != nil {
		logging.Log.Fatal().Err(err).Msg("failed to create runtime")
	}
	logging.Log.Info().Msg("runtime created")

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	logging.Log.Info().Msg("starting services...")
	if err := runtime.Start(ctx); err != nil {
		logging.Log.Fatal().Err(err).Msg("runtime failed")
	}
	logging.Log.Info().Msg("PersonalityCore ready")

	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := runtime.Stop(shutdownCtx); err != nil {
		logging.Log.Error().Err(err).Msg("shutdown failed")
	}
}
