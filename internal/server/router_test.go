package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"src.solsynth.dev/sosys/personality/internal/config"
)

func TestAuthMiddleware_OfflineUsesDefaultAccountID(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{
		Auth: config.AuthConfig{
			Offline:          true,
			OfflineAccountID: "local-dev",
		},
	}

	r := gin.New()
	r.Use(authMiddleware(cfg))
	r.GET("/check", func(c *gin.Context) {
		accountID, _ := c.Get("account_id")
		c.String(http.StatusOK, accountID.(string))
	})

	req := httptest.NewRequest(http.MethodGet, "/check", nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.Code)
	}
	if resp.Body.String() != "local-dev" {
		t.Fatalf("expected local-dev, got %q", resp.Body.String())
	}
}

func TestAuthMiddleware_OfflineIgnoresHeaderOverride(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{
		Auth: config.AuthConfig{
			Offline:          true,
			OfflineAccountID: "local-dev",
		},
	}

	r := gin.New()
	r.Use(authMiddleware(cfg))
	r.GET("/check", func(c *gin.Context) {
		accountID, _ := c.Get("account_id")
		c.String(http.StatusOK, accountID.(string))
	})

	req := httptest.NewRequest(http.MethodGet, "/check", nil)
	req.Header.Set("X-Account-Id", "user-override")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.Code)
	}
	if resp.Body.String() != "local-dev" {
		t.Fatalf("expected local-dev, got %q", resp.Body.String())
	}
}

func TestAutonomousSecretMiddlewareRejectsMissingSecret(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{
		Auth: config.AuthConfig{
			AutonomousSecret: "secret-123",
		},
	}

	r := gin.New()
	r.Use(autonomousSecretMiddleware(cfg))
	r.POST("/internal/check", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/internal/check", nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.Code)
	}
}

func TestAutonomousSecretMiddlewareAcceptsValidSecret(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{
		Auth: config.AuthConfig{
			AutonomousSecret: "secret-123",
		},
	}

	r := gin.New()
	r.Use(autonomousSecretMiddleware(cfg))
	r.POST("/internal/check", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/internal/check", nil)
	req.Header.Set("X-Autonomous-Secret", "secret-123")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.Code)
	}
}
