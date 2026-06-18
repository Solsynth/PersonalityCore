package server

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	sharedauth "src.solsynth.dev/sosys/go/pkg/auth"

	"src.solsynth.dev/sosys/personality/internal/config"
	"src.solsynth.dev/sosys/personality/internal/handler"
	"src.solsynth.dev/sosys/personality/internal/identity"
	"src.solsynth.dev/sosys/personality/internal/logging"
	"src.solsynth.dev/sosys/personality/internal/service"
)

func NewRouter(cfg *config.Config, conversations *service.ConversationService) *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(authMiddleware(cfg))

	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	api := r.Group("/api")
	handler.RegisterRoutes(api, conversations)
	internal := api.Group("/internal")
	internal.Use(autonomousSecretMiddleware(cfg))
	handler.RegisterInternalRoutes(internal, conversations)
	return r
}

func authMiddleware(cfg *config.Config) gin.HandlerFunc {
	var authenticator sharedauth.TokenAuthenticator
	if !cfg.Auth.Offline && cfg.Auth.Target != "" {
		client, err := sharedauth.NewGrpcTokenAuthenticator(sharedauth.GrpcAuthDialConfig{
			Target:        cfg.Auth.Target,
			UseTLS:        cfg.Auth.UseTLS,
			TLSSkipVerify: cfg.Auth.TLSSkipVerify,
		})
		if err != nil {
			logging.Log.Fatal().Err(err).Msg("failed to initialize auth client")
		}
		authenticator = client
	}

	return func(c *gin.Context) {
		if cfg.Auth.Offline {
			accountID := strings.TrimSpace(cfg.Auth.OfflineAccountID)
			if accountID != "" {
				identity.SetAccountID(c, accountID)
			}
			c.Next()
			return
		}

		if authenticator != nil {
			tokenInfo, tokenOK := sharedauth.ExtractToken(c.Request)
			result, err := sharedauth.AuthenticateRequest(c.Request.Context(), authenticator, c.Request)
			if err == nil && result != nil {
				if tokenOK {
					sharedauth.WithAuth(c, result, tokenInfo)
				}
				if accountID, ok := identity.ExtractAccountIDFromAuth(c); ok {
					identity.SetAccountID(c, accountID)
				}
				identity.SetPerkLevel(c, identity.ExtractPerkLevelFromAuth(c))
			}
		}

		if accountID, ok := identity.ExtractAccountIDFromAuth(c); ok {
			identity.SetAccountID(c, accountID)
		}

		if _, exists := c.Get("account_id"); !exists && cfg.Auth.AllowDevIDs {
			accountID := strings.TrimSpace(c.GetHeader("X-Account-Id"))
			if accountID != "" {
				identity.SetAccountID(c, accountID)
			}
		}

		c.Next()
	}
}

func autonomousSecretMiddleware(cfg *config.Config) gin.HandlerFunc {
	expected := strings.TrimSpace(cfg.Auth.AutonomousSecret)
	return func(c *gin.Context) {
		if expected == "" {
			c.JSON(http.StatusNotFound, gin.H{"error": "endpoint not configured"})
			c.Abort()
			return
		}
		provided := strings.TrimSpace(c.GetHeader("X-Autonomous-Secret"))
		if provided == "" || provided != expected {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid autonomous secret"})
			c.Abort()
			return
		}
		c.Next()
	}
}
