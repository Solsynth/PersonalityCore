package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"src.solsynth.dev/sosys/personality/internal/config"
	"src.solsynth.dev/sosys/personality/internal/database"
)

func openTestDBForOAuth(t *testing.T) *database.DB {
	t.Helper()
	raw, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite db: %v", err)
	}
	db := &database.DB{DB: raw}
	if err := db.AutoMigrate(); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	return db
}

func newTestOAuthService(t *testing.T, baseURL string) *OAuthService {
	t.Helper()
	cfg := &config.Config{
		OAuth: config.OAuthConfig{
			Enabled:  true,
			ClientID: "test-client-id",
			Scopes:   []string{"openid", "profile", "*"},
		},
		SolarNetwork: config.SolarNetworkConfig{
			BaseURL: baseURL,
		},
	}
	return NewOAuthService(openTestDBForOAuth(t), cfg)
}

// --- Stargate mock helpers ---

type deviceCodeResponse struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

type tokenResponse struct {
	AccessToken  string `json:"access_token,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
	ExpiresIn    int    `json:"expires_in,omitempty"`
	Scope        string `json:"scope,omitempty"`
	Error        string `json:"error,omitempty"`
}

func newMockStargate(t *testing.T, opts ...func(mux *http.ServeMux)) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	// Default: device code endpoint returns success
	mux.HandleFunc("/stargate/auth/open/device/code", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("device code: expected POST, got %s", r.Method)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if err := r.ParseForm(); err != nil {
			t.Errorf("device code: parse form: %v", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if r.Form.Get("client_id") == "" {
			t.Errorf("device code: missing client_id")
		}
		if r.Form.Get("scope") == "" {
			t.Errorf("device code: missing scope")
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(deviceCodeResponse{
			DeviceCode:              "test-device-code-123",
			UserCode:                "ABCD-EFGH",
			VerificationURI:         "https://stargate.example.com/device",
			VerificationURIComplete: "https://stargate.example.com/device?user_code=ABCD-EFGH",
			ExpiresIn:               600,
			Interval:                5,
		})
	})

	// Default: token endpoint returns authorization_pending
	mux.HandleFunc("/stargate/auth/open/token", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("token: expected POST, got %s", r.Method)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(tokenResponse{Error: "authorization_pending"})
	})

	// Apply overrides
	for _, opt := range opts {
		opt(mux)
	}

	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

// --- Tests ---

func TestOAuthDeviceFlowRequestContainsClientIDAndScopes(t *testing.T) {
	var capturedBody string
	var capturedPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		capturedBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(deviceCodeResponse{
			DeviceCode:              "dc-123",
			UserCode:                "WXYZ-1234",
			VerificationURIComplete: "https://example.com/verify",
			ExpiresIn:               300,
			Interval:                5,
		})
	}))
	defer ts.Close()

	svc := newTestOAuthService(t, ts.URL)
	info, err := svc.StartDeviceFlow(context.Background(), "agent-1", "acct-1")
	if err != nil {
		t.Fatalf("StartDeviceFlow() error = %v", err)
	}

	if capturedPath != "/stargate/auth/open/device/code" {
		t.Errorf("expected path /stargate/auth/open/device/code, got %s", capturedPath)
	}

	parsed, err := url.ParseQuery(capturedBody)
	if err != nil {
		t.Fatalf("parse request body: %v", err)
	}

	if got := parsed.Get("client_id"); got != "test-client-id" {
		t.Errorf("client_id = %q, want %q", got, "test-client-id")
	}
	if got := parsed.Get("scope"); got != "openid profile *" {
		t.Errorf("scope = %q, want %q", got, "openid profile *")
	}

	if info.UserCode != "WXYZ-1234" {
		t.Errorf("UserCode = %q, want %q", info.UserCode, "WXYZ-1234")
	}
	if info.VerificationURIComplete != "https://example.com/verify" {
		t.Errorf("VerificationURIComplete = %q, want %q", info.VerificationURIComplete, "https://example.com/verify")
	}
}

func TestOAuthPollSuccessPersistsSession(t *testing.T) {
	var mu sync.Mutex
	pollCount := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/stargate/auth/open/device/code":
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(deviceCodeResponse{
				DeviceCode:              "dc-poll-test",
				UserCode:                "POLL-CODE",
				VerificationURIComplete: "https://example.com/poll",
				ExpiresIn:               600,
				Interval:                5,
			})
		case "/stargate/auth/open/token":
			mu.Lock()
			pollCount++
			n := pollCount
			mu.Unlock()

			w.Header().Set("Content-Type", "application/json")
			if n <= 2 {
				// Return pending for first 2 polls
				json.NewEncoder(w).Encode(tokenResponse{Error: "authorization_pending"})
			} else {
				// Return success on 3rd poll
				json.NewEncoder(w).Encode(tokenResponse{
					AccessToken:  "new-access-token",
					RefreshToken: "new-refresh-token",
					ExpiresIn:    3600,
					Scope:        "openid profile *",
				})
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	svc := newTestOAuthService(t, ts.URL)

	// Start device flow
	_, err := svc.StartDeviceFlow(context.Background(), "agent-1", "acct-1")
	if err != nil {
		t.Fatalf("StartDeviceFlow() error = %v", err)
	}

	// Start the background poller
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	svc.Start(ctx)
	defer svc.Stop()

	// Check status is "pending" initially
	status, _, _, err := svc.Status(context.Background(), "agent-1", "acct-1")
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status != "pending" {
		t.Errorf("Status() = %q, want %q", status, "pending")
	}

	// Wait for the poller to process enough ticks
	// The poller runs every 1 second, and the interval is 5 seconds.
	// We need to wait long enough for the poller to reach the 3rd successful poll.
	// With interval=5s, the first poll happens immediately, then every 5s.
	// We need to wait ~15 seconds for the 3rd poll.
	time.Sleep(16 * time.Second)

	// Now check that the session was persisted
	status, scopes, _, err := svc.Status(context.Background(), "agent-1", "acct-1")
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status != "connected" {
		t.Errorf("Status() = %q, want %q", status, "connected")
	}
	if scopes != "openid profile *" {
		t.Errorf("scopes = %q, want %q", scopes, "openid profile *")
	}

	// Verify we can get an access token
	token, err := svc.UserAccessToken(context.Background(), "agent-1", "acct-1")
	if err != nil {
		t.Fatalf("UserAccessToken() error = %v", err)
	}
	if token != "new-access-token" {
		t.Errorf("UserAccessToken() = %q, want %q", token, "new-access-token")
	}
}

func TestOAuthRefreshRotationPersistsNewPair(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/stargate/auth/open/token":
			if err := r.ParseForm(); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			grantType := r.Form.Get("grant_type")
			if grantType == "refresh_token" {
				refreshToken := r.Form.Get("refresh_token")
				if refreshToken != "old-refresh-token" {
					t.Errorf("refresh_token = %q, want %q", refreshToken, "old-refresh-token")
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(tokenResponse{
					AccessToken:  "refreshed-access-token",
					RefreshToken: "refreshed-refresh-token",
					ExpiresIn:    3600,
					Scope:        "openid profile *",
				})
			} else {
				http.Error(w, "unexpected grant_type: "+grantType, http.StatusBadRequest)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	svc := newTestOAuthService(t, ts.URL)

	// Insert a session with an expired access token
	now := time.Now()
	expired := now.Add(-1 * time.Minute)
	refreshExpiry := now.Add(24 * time.Hour) // refresh still valid
	session := database.AgentOAuthSession{
		ID:               "test-id-1",
		AgentID:          "agent-1",
		AccountID:        "acct-1",
		Scopes:           "openid profile *",
		AccessToken:      "old-access-token",
		RefreshToken:     "old-refresh-token",
		AccessExpiresAt:  &expired,
		RefreshExpiresAt: &refreshExpiry,
	}
	if err := openTestDBForOAuth(t).Create(&session).Error; err != nil {
		t.Fatalf("seed session: %v", err)
	}

	// Request access token — should trigger refresh
	token, err := svc.UserAccessToken(context.Background(), "agent-1", "acct-1")
	if err != nil {
		t.Fatalf("UserAccessToken() error = %v", err)
	}
	if token != "refreshed-access-token" {
		t.Errorf("UserAccessToken() = %q, want %q", token, "refreshed-access-token")
	}

	// Verify the session was updated with the new tokens
	var updated database.AgentOAuthSession
	if err := openTestDBForOAuth(t).Where("agent_id = ? AND account_id = ?", "agent-1", "acct-1").First(&updated).Error; err != nil {
		t.Fatalf("load updated session: %v", err)
	}
	if updated.AccessToken != "refreshed-access-token" {
		t.Errorf("AccessToken = %q, want %q", updated.AccessToken, "refreshed-access-token")
	}
	if updated.RefreshToken != "refreshed-refresh-token" {
		t.Errorf("RefreshToken = %q, want %q", updated.RefreshToken, "refreshed-refresh-token")
	}
}

func TestOAuthExpiredAccessTriggersRefresh(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/stargate/auth/open/token" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(tokenResponse{
				AccessToken:  "new-access",
				RefreshToken: "new-refresh",
				ExpiresIn:    3600,
				Scope:        "openid",
			})
		} else {
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	svc := newTestOAuthService(t, ts.URL)

	// Session with access token expired 1 minute ago
	now := time.Now()
	expired := now.Add(-1 * time.Minute)
	refreshExpiry := now.Add(24 * time.Hour)
	session := database.AgentOAuthSession{
		ID:               "test-id-2",
		AgentID:          "agent-1",
		AccountID:        "acct-1",
		Scopes:           "openid",
		AccessToken:      "expired-token",
		RefreshToken:     "valid-refresh",
		AccessExpiresAt:  &expired,
		RefreshExpiresAt: &refreshExpiry,
	}
	if err := openTestDBForOAuth(t).Create(&session).Error; err != nil {
		t.Fatalf("seed session: %v", err)
	}

	// Should refresh
	token, err := svc.UserAccessToken(context.Background(), "agent-1", "acct-1")
	if err != nil {
		t.Fatalf("UserAccessToken() error = %v", err)
	}
	if token != "new-access" {
		t.Errorf("got %q, want %q", token, "new-access")
	}
}

func TestOAuthFailedRefreshDeletesSessionAndReturnsErr(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/stargate/auth/open/token" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(tokenResponse{Error: "invalid_grant"})
		} else {
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	svc := newTestOAuthService(t, ts.URL)

	// Session with expired access
	now := time.Now()
	expired := now.Add(-1 * time.Minute)
	refreshExpiry := now.Add(24 * time.Hour)
	session := database.AgentOAuthSession{
		ID:               "test-id-3",
		AgentID:          "agent-1",
		AccountID:        "acct-1",
		Scopes:           "openid",
		AccessToken:      "expired-token",
		RefreshToken:     "dead-refresh",
		AccessExpiresAt:  &expired,
		RefreshExpiresAt: &refreshExpiry,
	}
	if err := openTestDBForOAuth(t).Create(&session).Error; err != nil {
		t.Fatalf("seed session: %v", err)
	}

	// Should fail and delete session
	_, err := svc.UserAccessToken(context.Background(), "agent-1", "acct-1")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "user oauth authorization required") {
		t.Errorf("error = %q, want containing %q", err, "user oauth authorization required")
	}

	// Verify session was deleted
	var count int64
	openTestDBForOAuth(t).Model(&database.AgentOAuthSession{}).
		Where("agent_id = ? AND account_id = ?", "agent-1", "acct-1").Count(&count)
	if count != 0 {
		t.Errorf("expected session to be deleted, but found %d", count)
	}
}

func TestOAuthStatusReturnsCorrectStates(t *testing.T) {
	ts := newMockStargate(t)
	svc := newTestOAuthService(t, ts.URL)

	// Initially: none
	status, _, _, err := svc.Status(context.Background(), "agent-1", "acct-1")
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status != "none" {
		t.Errorf("Status() = %q, want %q", status, "none")
	}

		// Start device flow → pending
	_, err = svc.StartDeviceFlow(context.Background(), "agent-1", "acct-1")
	if err != nil {
		t.Fatalf("StartDeviceFlow() error = %v", err)
	}

	status, _, _, err = svc.Status(context.Background(), "agent-1", "acct-1")
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status != "pending" {
		t.Errorf("Status() = %q, want %q", status, "pending")
	}

	// Revoke the pending flow, then test connected state separately
	if err := svc.Revoke(context.Background(), "agent-1", "acct-1"); err != nil {
		t.Fatalf("Revoke() error = %v", err)
	}

	// Insert a connected session → connected
	now := time.Now()
	refreshExpiry := now.Add(24 * time.Hour)
	accessExpiry := now.Add(1 * time.Hour)
	session := database.AgentOAuthSession{
		ID:               "test-id-4",
		AgentID:          "agent-1",
		AccountID:        "acct-1",
		Scopes:           "openid profile *",
		AccessToken:      "valid-token",
		RefreshToken:     "valid-refresh",
		AccessExpiresAt:  &accessExpiry,
		RefreshExpiresAt: &refreshExpiry,
	}
	if err := openTestDBForOAuth(t).Create(&session).Error; err != nil {
		t.Fatalf("seed session: %v", err)
	}

	status, scopes, expiresAt, err := svc.Status(context.Background(), "agent-1", "acct-1")
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status != "connected" {
		t.Errorf("Status() = %q, want %q", status, "connected")
	}
	if scopes != "openid profile *" {
		t.Errorf("scopes = %q, want %q", scopes, "openid profile *")
	}
	if expiresAt == nil {
		t.Errorf("expiresAt should not be nil")
	}
}

func TestOAuthRevoke(t *testing.T) {
	ts := newMockStargate(t)
	svc := newTestOAuthService(t, ts.URL)

	// Start a device flow
	_, err := svc.StartDeviceFlow(context.Background(), "agent-1", "acct-1")
	if err != nil {
		t.Fatalf("StartDeviceFlow() error = %v", err)
	}

	// Verify it's pending
	status, _, _, err := svc.Status(context.Background(), "agent-1", "acct-1")
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status != "pending" {
		t.Errorf("Status() = %q, want %q", status, "pending")
	}

	// Revoke
	if err := svc.Revoke(context.Background(), "agent-1", "acct-1"); err != nil {
		t.Fatalf("Revoke() error = %v", err)
	}

	// Verify it's none now
	status, _, _, err = svc.Status(context.Background(), "agent-1", "acct-1")
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status != "none" {
		t.Errorf("Status() = %q, want %q", status, "none")
	}
}

func TestOAuthUserAccessTokenNoSessionReturnsErr(t *testing.T) {
	ts := newMockStargate(t)
	svc := newTestOAuthService(t, ts.URL)

	_, err := svc.UserAccessToken(context.Background(), "agent-1", "acct-1")
	if err != ErrUserAuthRequired {
		t.Errorf("UserAccessToken() error = %v, want %v", err, ErrUserAuthRequired)
	}
}

func TestOAuthUserAccessTokenNearExpiryTriggersRefresh(t *testing.T) {
	var mu sync.Mutex
	var refreshCalled bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/stargate/auth/open/token" {
			mu.Lock()
			refreshCalled = true
			mu.Unlock()

			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(tokenResponse{
				AccessToken:  "fresh-access",
				RefreshToken: "fresh-refresh",
				ExpiresIn:    3600,
			})
		} else {
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	svc := newTestOAuthService(t, ts.URL)

	// Session with access token expiring in 3 minutes (within 5-min margin)
	now := time.Now()
	nearExpiry := now.Add(3 * time.Minute)
	refreshExpiry := now.Add(24 * time.Hour)
	session := database.AgentOAuthSession{
		ID:               "test-id-5",
		AgentID:          "agent-1",
		AccountID:        "acct-1",
		Scopes:           "openid",
		AccessToken:      "still-valid-token",
		RefreshToken:     "still-valid-refresh",
		AccessExpiresAt:  &nearExpiry,
		RefreshExpiresAt: &refreshExpiry,
	}
	if err := openTestDBForOAuth(t).Create(&session).Error; err != nil {
		t.Fatalf("seed session: %v", err)
	}

	token, err := svc.UserAccessToken(context.Background(), "agent-1", "acct-1")
	if err != nil {
		t.Fatalf("UserAccessToken() error = %v", err)
	}

	mu.Lock()
	called := refreshCalled
	mu.Unlock()

	if !called {
		t.Error("expected refresh to be called for near-expiry token")
	}
	if token != "fresh-access" {
		t.Errorf("UserAccessToken() = %q, want %q", token, "fresh-access")
	}
}

func TestOAuthUserAccessTokenValidTokenNotRefreshed(t *testing.T) {
	var mu sync.Mutex
	var tokenEndpointCalled bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/stargate/auth/open/token" {
			mu.Lock()
			tokenEndpointCalled = true
			mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(tokenResponse{})
	}))
	defer ts.Close()

	svc := newTestOAuthService(t, ts.URL)

	// Session with access token valid for 1 hour
	now := time.Now()
	farFuture := now.Add(1 * time.Hour)
	refreshExpiry := now.Add(24 * time.Hour)
	session := database.AgentOAuthSession{
		ID:               "test-id-6",
		AgentID:          "agent-1",
		AccountID:        "acct-1",
		Scopes:           "openid",
		AccessToken:      "valid-token",
		RefreshToken:     "valid-refresh",
		AccessExpiresAt:  &farFuture,
		RefreshExpiresAt: &refreshExpiry,
	}
	if err := openTestDBForOAuth(t).Create(&session).Error; err != nil {
		t.Fatalf("seed session: %v", err)
	}

	token, err := svc.UserAccessToken(context.Background(), "agent-1", "acct-1")
	if err != nil {
		t.Fatalf("UserAccessToken() error = %v", err)
	}

	mu.Lock()
	called := tokenEndpointCalled
	mu.Unlock()

	if called {
		t.Error("token endpoint should not be called for valid token")
	}
	if token != "valid-token" {
		t.Errorf("UserAccessToken() = %q, want %q", token, "valid-token")
	}
}

func TestOAuthSlowDownDoublesInterval(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/stargate/auth/open/device/code":
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(deviceCodeResponse{
				DeviceCode:              "dc-slowdown",
				UserCode:                "SLOW-CODE",
				VerificationURIComplete: "https://example.com/slow",
				ExpiresIn:               600,
				Interval:                5,
			})
		case "/stargate/auth/open/token":
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(tokenResponse{Error: "slow_down"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	svc := newTestOAuthService(t, ts.URL)

	_, err := svc.StartDeviceFlow(context.Background(), "agent-1", "acct-1")
	if err != nil {
		t.Fatalf("StartDeviceFlow() error = %v", err)
	}

	// Check initial interval
	svc.mu.Lock()
	pf := svc.pending[pendingKey("agent-1", "acct-1")]
	if pf == nil {
		svc.mu.Unlock()
		t.Fatal("pending flow not found")
	}
	initialInterval := pf.Interval
	svc.mu.Unlock()

	// Start poller and wait for at least one poll
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	svc.Start(ctx)
	time.Sleep(2 * time.Second)
	svc.Stop()

	// Check interval doubled
	svc.mu.Lock()
	pf = svc.pending[pendingKey("agent-1", "acct-1")]
	svc.mu.Unlock()

	if pf == nil {
		t.Fatal("pending flow should still exist (not expired)")
	}
	if pf.Interval != initialInterval*2 {
		t.Errorf("Interval = %v, want %v (doubled)", pf.Interval, initialInterval*2)
	}
}
