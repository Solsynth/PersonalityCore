package app

import (
	"context"
	"fmt"
	"net"
	"net/http"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"

	"src.solsynth.dev/sosys/personality/internal/agent"
	"src.solsynth.dev/sosys/personality/internal/config"
	"src.solsynth.dev/sosys/personality/internal/database"
	"src.solsynth.dev/sosys/personality/internal/grpcsvc"
	"src.solsynth.dev/sosys/personality/internal/logging"
	"src.solsynth.dev/sosys/personality/internal/server"
	"src.solsynth.dev/sosys/personality/internal/service"

	gen "src.solsynth.dev/sosys/go/proto"
)

type App struct {
	cfg     *config.Config
	db      *database.DB
	httpSrv *http.Server
	grpcSrv *grpc.Server
	grpcLn  net.Listener
}

func New(cfg *config.Config) (*App, error) {
	registry, err := agent.NewRegistry(cfg.Agents.Items)
	if err != nil {
		return nil, err
	}
	if len(registry.List()) == 0 {
		return nil, fmt.Errorf("at least one enabled agent is required")
	}

	db, err := database.Open(cfg)
	if err != nil {
		return nil, err
	}
	if err := db.AutoMigrate(); err != nil {
		return nil, err
	}

	executor := agent.NewExecutor(cfg)
	conversations := service.NewConversationService(db, cfg, registry, executor)
	router := server.NewRouter(cfg, conversations)
	httpSrv := &http.Server{
		Addr:    ":" + cfg.HTTP.Port,
		Handler: router,
	}

	grpcOpts := []grpc.ServerOption{}
	if cfg.GRPC.UseTLS {
		if cfg.GRPC.CertFile == "" || cfg.GRPC.KeyFile == "" {
			return nil, fmt.Errorf("grpc tls requires cert and key files")
		}
		creds, err := credentials.NewServerTLSFromFile(cfg.GRPC.CertFile, cfg.GRPC.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("load grpc tls credentials: %w", err)
		}
		grpcOpts = append(grpcOpts, grpc.Creds(creds))
	}
	grpcSrv := grpc.NewServer(grpcOpts...)
	healthServer := health.NewServer()
	healthServer.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	healthpb.RegisterHealthServer(grpcSrv, healthServer)
	gen.RegisterDyPersonalityServiceServer(grpcSrv, grpcsvc.New(conversations))
	reflection.Register(grpcSrv)

	return &App{cfg: cfg, db: db, httpSrv: httpSrv, grpcSrv: grpcSrv}, nil
}

func (a *App) Start(context.Context) error {
	ln, err := net.Listen("tcp", ":"+a.cfg.GRPC.Port)
	if err != nil {
		return err
	}
	a.grpcLn = ln

	go func() {
		if err := a.grpcSrv.Serve(ln); err != nil {
			logging.Log.Error().Err(err).Msg("grpc server stopped")
		}
	}()
	go func() {
		if err := a.httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logging.Log.Error().Err(err).Msg("http server stopped")
		}
	}()

	logging.Log.Info().
		Str("http", a.cfg.HTTP.Port).
		Str("grpc", a.cfg.GRPC.Port).
		Msg("personality core started")
	return nil
}

func (a *App) Stop(ctx context.Context) error {
	if a.httpSrv != nil {
		_ = a.httpSrv.Shutdown(ctx)
	}
	if a.grpcSrv != nil {
		a.grpcSrv.GracefulStop()
	}
	if a.grpcLn != nil {
		_ = a.grpcLn.Close()
	}
	return nil
}
