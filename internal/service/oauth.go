package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"src.solsynth.dev/sosys/personality/internal/config"
	"src.solsynth.dev/sosys/personality/internal/database"
	"src.solsynth.dev/sosys/personality/internal/logging"
)

var ErrUserAuthRequired = errors.New("user oauth authorization required")

// DeviceFlowInfo is the user-facing portion of a device-code authorization.
// The device_code itself is never exposed.
type DeviceFlowInfo struct {
	UserCode               string `json:"user_code"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn              int    `json:"expires_in"`
}

type pendingDeviceFlow struct {
	DeviceCode  string
	AccountID   string
	ExpiresAt   time.Time
	Interval    time.Duration
	CurrentPoll time.Time // last poll time; zero means poll immediately
}

// OAuthService manages the OIDC device-flow lifecycle for per-(agent, account)
// user tokens. Tokens are stored in Postgres and auto-refreshed through Stargate.
type OAuthService struct {
	db         *database.DB
	cfg        *config.Config
	httpClient *http.Client

	mu      sync.Mutex
	pending map[string]*pendingDeviceFlow // key = agentID + "\x00" + accountID

	cancel context.CancelFunc
	done   chan struct{}
}

func NewOAuthService(db *database.DB, cfg *config.Config) *OAuthService {
	return &OAuthService{
		db:         db,
		cfg:        cfg,
		httpClient: &http.Client{Timeout: 30 * time.Second},
		pending:    make(map[string]*pendingDeviceFlow),
	}
}

// Start launches the background goroutine that polls pending device flows.
func (s *OAuthService) Start(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.done = make(chan struct{})

	go func() {
		defer close(s.done)

		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				s.pollPending(ctx, now)
			}
		}
	}()
}

// Stop cancels the background goroutine and waits for it to exit.
func (s *OAuthService) Stop() {
	if s.cancel != nil {
		s.cancel()
	}
	if s.done != nil {
		<-s.done
	}
}

func pendingKey(agentID, accountID string) string {
	return agentID + "\x00" + accountID
}

// StartDeviceFlow initiates a new OIDC device-flow authorization.
func (s *OAuthService) StartDeviceFlow(ctx context.Context, agentID, accountID string) (*DeviceFlowInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	form := url.Values{}
	form.Set("client_id", s.cfg.OAuth.ClientID)
	form.Set("scope", strings.Join(s.cfg.OAuth.Scopes, " "))

	deviceCodeURL := s.cfg.SolarNetwork.BaseURL + "/stargate/auth/open/device/code"
	resp, err := s.postForm(ctx, deviceCodeURL, form)
	if err != nil {
		return nil, fmt.Errorf("device code request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read device code response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("device code endpoint returned %d: %s", resp.StatusCode, string(body))
	}

	var parsed struct {
		DeviceCode              string `json:"device_code"`
		UserCode                string `json:"user_code"`
		VerificationURI         string `json:"verification_uri"`
		VerificationURIComplete string `json:"verification_uri_complete"`
		ExpiresIn               int    `json:"expires_in"`
		Interval                int    `json:"interval"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("decode device code response: %w", err)
	}

	if parsed.DeviceCode == "" {
		return nil, fmt.Errorf("device code endpoint returned empty device_code")
	}

	interval := time.Duration(parsed.Interval) * time.Second
	if interval < 5*time.Second {
		interval = 5 * time.Second
	}

	key := pendingKey(agentID, accountID)
	s.pending[key] = &pendingDeviceFlow{
		DeviceCode: parsed.DeviceCode,
		AccountID:  accountID,
		ExpiresAt:  time.Now().Add(time.Duration(parsed.ExpiresIn) * time.Second),
		Interval:   interval,
	}

	return &DeviceFlowInfo{
		UserCode:               parsed.UserCode,
		VerificationURIComplete: parsed.VerificationURIComplete,
		ExpiresIn:              parsed.ExpiresIn,
	}, nil
}

// Status returns the current OAuth status for the given (agent, account) pair.
func (s *OAuthService) Status(ctx context.Context, agentID, accountID string) (status string, scopes string, expiresAt *time.Time, err error) {
	s.mu.Lock()
	key := pendingKey(agentID, accountID)
	pf, pending := s.pending[key]
	s.mu.Unlock()

	if pending && time.Now().Before(pf.ExpiresAt) {
		return "pending", "", nil, nil
	}

	var session database.AgentOAuthSession
	result := s.db.Where("agent_id = ? AND account_id = ?", agentID, accountID).First(&session)
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return "none", "", nil, nil
		}
		return "", "", nil, fmt.Errorf("load oauth session: %w", result.Error)
	}

	if session.RefreshExpiresAt != nil && time.Now().Before(*session.RefreshExpiresAt) {
		return "connected", session.Scopes, session.AccessExpiresAt, nil
	}

	return "none", "", nil, nil
}

// Revoke removes the OAuth session and any pending device flow.
func (s *OAuthService) Revoke(ctx context.Context, agentID, accountID string) error {
	s.mu.Lock()
	delete(s.pending, pendingKey(agentID, accountID))
	s.mu.Unlock()

	result := s.db.Where("agent_id = ? AND account_id = ?", agentID, accountID).Delete(&database.AgentOAuthSession{})
	if result.Error != nil {
		return fmt.Errorf("delete oauth session: %w", result.Error)
	}
	return nil
}

// UserAccessToken returns a valid access token for the user, refreshing if
// necessary. Returns ErrUserAuthRequired if no session exists or refresh fails.
func (s *OAuthService) UserAccessToken(ctx context.Context, agentID, accountID string) (string, error) {
	var session database.AgentOAuthSession
	result := s.db.Where("agent_id = ? AND account_id = ?", agentID, accountID).First(&session)
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return "", ErrUserAuthRequired
		}
		return "", fmt.Errorf("load oauth session: %w", result.Error)
	}

	// Check if access token is near expiry (within 5-minute margin).
	if session.AccessExpiresAt != nil && time.Until(*session.AccessExpiresAt) > 5*time.Minute {
		return session.AccessToken, nil
	}

	// Access token is expired or near expiry — attempt refresh.
	if session.RefreshToken == "" {
		_ = s.deleteSession(agentID, accountID)
		return "", ErrUserAuthRequired
	}

	newAccess, newRefresh, newExpiresIn, err := s.refreshToken(ctx, session.RefreshToken)
	if err != nil {
		// Stargate refresh rotation invalidates old tokens; delete the dead session.
		_ = s.deleteSession(agentID, accountID)
		return "", ErrUserAuthRequired
	}

	now := time.Now()
	accessExpiresAt := now.Add(time.Duration(newExpiresIn) * time.Second)
	refreshExpiresAt := now.Add(30 * 24 * time.Hour) // 30 days

	update := map[string]any{
		"access_token":       newAccess,
		"refresh_token":      newRefresh,
		"access_expires_at":  accessExpiresAt,
		"refresh_expires_at": refreshExpiresAt,
	}
	if err := s.db.Model(&database.AgentOAuthSession{}).
		Where("agent_id = ? AND account_id = ?", agentID, accountID).
		Updates(update).Error; err != nil {
		return "", fmt.Errorf("persist refreshed tokens: %w", err)
	}

	return newAccess, nil
}

// --- internal helpers ---

func (s *OAuthService) deleteSession(agentID, accountID string) error {
	result := s.db.Where("agent_id = ? AND account_id = ?", agentID, accountID).Delete(&database.AgentOAuthSession{})
	return result.Error
}

func (s *OAuthService) postForm(ctx context.Context, u string, form url.Values) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return s.httpClient.Do(req)
}

// refreshToken exchanges a refresh token for a new access+refresh pair.
func (s *OAuthService) refreshToken(ctx context.Context, refreshToken string) (newAccess, newRefresh string, expiresIn int, err error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	form.Set("client_id", s.cfg.OAuth.ClientID)
	if s.cfg.OAuth.ClientSecret != "" {
		form.Set("client_secret", s.cfg.OAuth.ClientSecret)
	}

	tokenURL := s.cfg.SolarNetwork.BaseURL + "/stargate/auth/open/token"
	resp, err := s.postForm(ctx, tokenURL, form)
	if err != nil {
		return "", "", 0, fmt.Errorf("refresh token request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", 0, fmt.Errorf("read refresh response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", 0, fmt.Errorf("refresh token endpoint returned %d: %s", resp.StatusCode, string(body))
	}

	var parsed struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
		Error        string `json:"error"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", "", 0, fmt.Errorf("decode refresh response: %w", err)
	}

	if parsed.Error != "" {
		return "", "", 0, fmt.Errorf("refresh token error: %s", parsed.Error)
	}

	if parsed.AccessToken == "" {
		return "", "", 0, fmt.Errorf("refresh token response missing access_token")
	}

	// If the server didn't rotate the refresh token, reuse the original.
	if parsed.RefreshToken == "" {
		parsed.RefreshToken = refreshToken
	}

	return parsed.AccessToken, parsed.RefreshToken, parsed.ExpiresIn, nil
}

// pollPending checks all pending device flows and polls the token endpoint.
func (s *OAuthService) pollPending(ctx context.Context, now time.Time) {
	s.mu.Lock()
	// Snapshot the keys to avoid holding the lock during HTTP calls.
	keys := make([]string, 0, len(s.pending))
	flows := make(map[string]*pendingDeviceFlow, len(s.pending))
	for k, v := range s.pending {
		keys = append(keys, k)
		flows[k] = v
	}
	s.mu.Unlock()

	for _, key := range keys {
		pf := flows[key]
		if now.Before(pf.ExpiresAt) && (pf.CurrentPoll.IsZero() || now.Sub(pf.CurrentPoll) >= pf.Interval) {
			s.pollOneDeviceFlow(ctx, key, pf, now)
		} else if !now.Before(pf.ExpiresAt) {
			// Flow expired — remove it.
			s.mu.Lock()
			delete(s.pending, key)
			s.mu.Unlock()
		}
	}
}

func (s *OAuthService) pollOneDeviceFlow(ctx context.Context, key string, pf *pendingDeviceFlow, now time.Time) {
	form := url.Values{}
	form.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")
	form.Set("device_code", pf.DeviceCode)
	form.Set("client_id", s.cfg.OAuth.ClientID)
	if s.cfg.OAuth.ClientSecret != "" {
		form.Set("client_secret", s.cfg.OAuth.ClientSecret)
	}

	tokenURL := s.cfg.SolarNetwork.BaseURL + "/stargate/auth/open/token"
	resp, err := s.postForm(ctx, tokenURL, form)
	if err != nil {
		logging.Log.Warn().Err(err).Str("agent_id", key).Msg("device flow poll failed")
		s.mu.Lock()
		pf.CurrentPoll = now
		s.mu.Unlock()
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		logging.Log.Warn().Err(err).Str("agent_id", key).Msg("read device flow poll response failed")
		s.mu.Lock()
		pf.CurrentPoll = now
		s.mu.Unlock()
		return
	}

	// Handle non-200 status codes as errors.
	if resp.StatusCode != http.StatusOK {
		logging.Log.Warn().Int("status", resp.StatusCode).Str("agent_id", key).Msg("device flow poll returned non-200")
		s.mu.Lock()
		pf.CurrentPoll = now
		s.mu.Unlock()
		return
	}

	var parsed struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
		Scope        string `json:"scope"`
		Error        string `json:"error"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		logging.Log.Warn().Err(err).Str("agent_id", key).Msg("decode device flow poll response failed")
		s.mu.Lock()
		pf.CurrentPoll = now
		s.mu.Unlock()
		return
	}

	switch parsed.Error {
	case "":
		// Success — persist the session.
		s.handleDeviceFlowSuccess(key, pf, &parsed)
	case "authorization_pending":
		// Still waiting for user — retry next tick.
		s.mu.Lock()
		pf.CurrentPoll = now
		s.mu.Unlock()
	case "slow_down":
		// Double the interval as per spec.
		s.mu.Lock()
		pf.Interval *= 2
		pf.CurrentPoll = now
		s.mu.Unlock()
	case "expired_token", "access_denied":
		// Terminal — remove from pending.
		s.mu.Lock()
		delete(s.pending, key)
		s.mu.Unlock()
	default:
		// Unknown error — treat as non-fatal, retry later.
		s.mu.Lock()
		pf.CurrentPoll = now
		s.mu.Unlock()
	}
}

func (s *OAuthService) handleDeviceFlowSuccess(key string, pf *pendingDeviceFlow, parsed *struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	Scope        string `json:"scope"`
	Error        string `json:"error"`
}) {
	// Parse agent_id from the key (split on \x00).
	parts := strings.SplitN(key, "\x00", 2)
	if len(parts) != 2 {
		return
	}
	agentID, accountID := parts[0], parts[1]

	now := time.Now()
	accessExpiresAt := now.Add(time.Duration(parsed.ExpiresIn) * time.Second)
	refreshExpiresAt := now.Add(30 * 24 * time.Hour) // 30 days

	session := database.AgentOAuthSession{
		ID:               ulid.Make().String(),
		AgentID:          agentID,
		AccountID:        accountID,
		Scopes:           parsed.Scope,
		AccessToken:      parsed.AccessToken,
		RefreshToken:     parsed.RefreshToken,
		AccessExpiresAt:  &accessExpiresAt,
		RefreshExpiresAt: &refreshExpiresAt,
	}

	// Upsert: if a session already exists for (agent, account), replace it.
	if err := s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "agent_id"}, {Name: "account_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"scopes", "access_token", "refresh_token", "access_expires_at", "refresh_expires_at"}),
	}).Create(&session).Error; err != nil {
		logging.Log.Error().Err(err).Str("agent_id", agentID).Msg("persist oauth session failed")
		return
	}

	// Remove from pending on success.
	s.mu.Lock()
	delete(s.pending, key)
	s.mu.Unlock()

	logging.Log.Info().Str("agent_id", agentID).Str("account_id", accountID).Msg("oauth device flow completed")
}
