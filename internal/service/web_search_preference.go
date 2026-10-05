package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"

	"src.solsynth.dev/sosys/persona/internal/database"
	"src.solsynth.dev/sosys/persona/internal/websearch"
)

// WebSearchEngines is the choice a caller has over where its searches run, with
// what each engine costs. The currency is the server's billing currency, the
// one every listed price is stated in.
type WebSearchEngines struct {
	Currency string                 `json:"currency"`
	Engines  []websearch.EngineInfo `json:"engines"`
}

// WebSearchPreference is the account's chosen search engine. An empty Engine
// means the server's own order decides, which may reach a metered provider.
type WebSearchPreference struct {
	Engine   string `json:"engine"`
	Currency string `json:"currency"`
}

// WebSearchEngines returns the engines a caller can choose between, priced in
// the billing currency. It returns websearch.ErrNotConfigured when the server
// has no web search, which the HTTP layer reports as an unavailable feature.
func (s *ConversationService) WebSearchEngines() (*WebSearchEngines, error) {
	if s.webSearch == nil {
		return nil, websearch.ErrNotConfigured
	}
	return &WebSearchEngines{Currency: s.billing.defaultCurrency(), Engines: s.webSearch.Catalog()}, nil
}

// WebSearchPreference reads the account's preferred search engine. An account
// that never chose one reads back empty, which means the server decides.
func (s *ConversationService) WebSearchPreference(ctx context.Context, accountID string) (*WebSearchPreference, error) {
	engine, err := s.preferredWebSearchEngine(ctx, accountID)
	if err != nil {
		return nil, err
	}
	return &WebSearchPreference{Engine: engine, Currency: s.billing.defaultCurrency()}, nil
}

// SetWebSearchPreference records the account's preferred search engine. An
// empty engine clears the preference back to the server's own order; any other
// value must name a configured engine, so a typo cannot silently leave searches
// running on the engine the caller was trying to avoid.
func (s *ConversationService) SetWebSearchPreference(ctx context.Context, accountID, engine string) (*WebSearchPreference, error) {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return nil, fmt.Errorf("account_id is required")
	}
	engine = strings.TrimSpace(engine)
	if s.webSearch == nil && engine != "" {
		return nil, websearch.ErrNotConfigured
	}
	if engine != "" && !s.hasSearchEngine(engine) {
		return nil, fmt.Errorf("%w: unknown search engine %q", websearch.ErrInvalidQuery, engine)
	}
	row := &database.WebSearchPreference{AccountID: accountID, Engine: engine}
	if err := s.db.WithContext(ctx).Save(row).Error; err != nil {
		return nil, err
	}
	return &WebSearchPreference{Engine: engine, Currency: s.billing.defaultCurrency()}, nil
}

func (s *ConversationService) hasSearchEngine(engine string) bool {
	for _, info := range s.webSearch.Catalog() {
		if info.ID == engine {
			return true
		}
	}
	return false
}

// preferredWebSearchEngine is what the search path reads: the account's stored
// engine, or empty when it has none. A missing row and a missing database both
// mean "let the server decide" rather than an error, because every search must
// still run when the preference store is unavailable.
func (s *ConversationService) preferredWebSearchEngine(ctx context.Context, accountID string) (string, error) {
	if s == nil || s.db == nil || strings.TrimSpace(accountID) == "" {
		return "", nil
	}
	var row database.WebSearchPreference
	err := s.db.WithContext(ctx).First(&row, "account_id = ?", strings.TrimSpace(accountID)).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(row.Engine), nil
}
