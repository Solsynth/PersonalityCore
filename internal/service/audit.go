package service

import (
	"context"
	"strings"
)

// AuditAttribution is the transport-level attribution of one API call: which
// endpoint admitted it, which credential it used, and where it came from. It
// travels on the request context from the HTTP/gRPC boundary down to the
// billing calls that write ledger rows, so a ledger row can be traced back to
// the call that spent it.
//
// Attribution is best-effort by design: a background job or an internal
// scheduler has no caller and carries the zero value, which billing records as
// empty rather than guessing.
type AuditAttribution struct {
	// Surface is the API endpoint or RPC method that admitted the call, for
	// example "/api/conversations/:id/runs".
	Surface string
	// CredentialID is the AI access credential (sat_ token) the call
	// authenticated with, empty when it used the account's own session.
	CredentialID string
	// ClientIP is the address the request arrived from.
	ClientIP string
	// DeviceID is the caller-declared device identifier (X-Device-Id).
	DeviceID string
	// UserAgent is the request's user agent.
	UserAgent string
}

type auditAttributionContextKey struct{}

// WithAuditAttribution attaches attribution to ctx, replacing any earlier
// value. A boundary sets it once per request.
func WithAuditAttribution(ctx context.Context, a AuditAttribution) context.Context {
	return context.WithValue(ctx, auditAttributionContextKey{}, a)
}

// AuditAttributionFrom returns the attribution attached to ctx, if any.
func AuditAttributionFrom(ctx context.Context) (AuditAttribution, bool) {
	a, ok := ctx.Value(auditAttributionContextKey{}).(AuditAttribution)
	if !ok {
		return AuditAttribution{}, false
	}
	return a, true
}

// WithAuditCredential records the credential a call authenticated with while
// preserving the rest of the attribution. The OpenAI-compatible surface
// resolves its credential after the transport attribution is already on the
// context, so it merges the id in rather than replacing the whole value.
func WithAuditCredential(ctx context.Context, credentialID string) context.Context {
	a, _ := AuditAttributionFrom(ctx)
	a.CredentialID = strings.TrimSpace(credentialID)
	return WithAuditAttribution(ctx, a)
}

// ledgerAttribution returns the attribution on ctx, each field truncated to the
// width of its ledger column so an oversized header cannot fail the insert.
func ledgerAttribution(ctx context.Context) AuditAttribution {
	a, _ := AuditAttributionFrom(ctx)
	return AuditAttribution{
		Surface:      truncateRunes(a.Surface, 128),
		CredentialID: strings.TrimSpace(a.CredentialID),
		ClientIP:     truncateRunes(a.ClientIP, 64),
		DeviceID:     truncateRunes(a.DeviceID, 128),
		UserAgent:    truncateRunes(a.UserAgent, 256),
	}
}

// truncateRunes cuts a string to at most max characters without splitting a
// multi-byte rune, matching the character-width semantics of the ledger's
// varchar columns.
func truncateRunes(value string, max int) string {
	if max <= 0 || len(value) <= max {
		return value
	}
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max])
}
