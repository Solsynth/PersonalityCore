package app

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sync"

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
	"src.solsynth.dev/sosys/personality/internal/solar_network"

	gen "src.solsynth.dev/sosys/go/proto"
)

type App struct {
	cfg           *config.Config
	db            *database.DB
	conversations *service.ConversationService
	httpSrv       *http.Server
	grpcSrv       *grpc.Server
	grpcLn        net.Listener
	sn            *solar_network.Manager
	autonomous    *service.AutonomousWakeScheduler
	scheduler     *service.TaskScheduler
	backgroundCtx context.Context
	cancel        context.CancelFunc
	wg            sync.WaitGroup
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

	executor, err := agent.NewExecutor(cfg)
	if err != nil {
		return nil, err
	}
	conversations := service.NewConversationService(db, cfg, registry, executor)
	snManager := solar_network.NewManager(
		cfg,
		registry,
		func(ctx context.Context, agentID string) ([]solar_network.TrackedRoomState, error) {
			rooms, err := conversations.ListTrackedSnRooms(ctx, agentID)
			if err != nil {
				return nil, err
			}
			out := make([]solar_network.TrackedRoomState, 0, len(rooms))
			for _, room := range rooms {
				out = append(out, solar_network.TrackedRoomState{
					RoomID:        room.RoomID,
					LastMessageAt: room.LastMessageAt,
				})
			}
			return out, nil
		},
		func(ctx context.Context, agentID string, msg solar_network.InboundMessage) error {
			return conversations.HandleSnInboundMessage(ctx, agentID, service.ExternalInboundMessage{
				RoomID:              msg.RoomID,
				RoomType:            msg.RoomType,
				MessageID:           msg.MessageID,
				MessageType:         msg.MessageType,
				Content:             msg.Content,
				Attachments:         append([]solar_network.ChatAttachment(nil), msg.Attachments...),
				SenderAccountID:     msg.SenderAccountID,
				SenderName:          msg.SenderName,
				SenderNick:          msg.SenderNick,
				MentionedBot:        msg.MentionedBot,
				RepliedMessageID:    msg.RepliedMessageID,
				RepliedMessageContent: msg.RepliedMessageContent,
				CreatedAt:           msg.CreatedAt,
			})
		},
	)
	conversations.SetSnChatBridge(snManager)
	scheduler := service.NewTaskScheduler(db, conversations, 0)
	autonomous := service.NewAutonomousWakeScheduler(conversations, registry)
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

	return &App{cfg: cfg, db: db, conversations: conversations, httpSrv: httpSrv, grpcSrv: grpcSrv, sn: snManager, autonomous: autonomous, scheduler: scheduler}, nil
}

func (a *App) Start(ctx context.Context) error {
	a.backgroundCtx, a.cancel = context.WithCancel(ctx)
	ln, err := net.Listen("tcp", ":"+a.cfg.GRPC.Port)
	if err != nil {
		return err
	}
	a.grpcLn = ln

	if a.sn != nil {
		if err := a.sn.Start(context.Background()); err != nil {
			return err
		}
	}

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
	if a.autonomous != nil {
		a.wg.Add(1)
		go func() {
			defer a.wg.Done()
			a.autonomous.Run(a.backgroundCtx)
		}()
	}
	a.scheduler.Start(a.backgroundCtx)

	logging.Log.Info().
		Str("http", a.cfg.HTTP.Port).
		Str("grpc", a.cfg.GRPC.Port).
		Msg("personality core started")
	return nil
}

func (a *App) Stop(ctx context.Context) error {
	if a.cancel != nil {
		a.cancel()
	}
	if a.httpSrv != nil {
		_ = a.httpSrv.Shutdown(ctx)
	}
	if a.grpcSrv != nil {
		a.grpcSrv.GracefulStop()
	}
	if a.grpcLn != nil {
		_ = a.grpcLn.Close()
	}
	if a.conversations != nil {
		_ = a.conversations.FlushSnInboundBatches(ctx)
	}
	if a.sn != nil {
		_ = a.sn.Stop(ctx)
	}
	a.scheduler.Stop()
	a.wg.Wait()
	return nil
}
