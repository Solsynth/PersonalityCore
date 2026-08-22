package service

import (
	"context"
	"strings"
)

// CallerIdentity carries the authenticated caller's display identity from the
// HTTP request into service execution. Tools use it to address the user even
// when no Solar Network lookup is possible (for example pet agents, which have
// no chat credentials).
type CallerIdentity struct {
	AccountID string
	Name      string
	Nick      string
}

type callerIdentityContextKey struct{}

// WithCallerIdentity attaches the authenticated caller's identity to ctx.
func WithCallerIdentity(ctx context.Context, id CallerIdentity) context.Context {
	return context.WithValue(ctx, callerIdentityContextKey{}, id)
}

// CallerIdentityFrom returns the authenticated caller identity attached to ctx.
func CallerIdentityFrom(ctx context.Context) (CallerIdentity, bool) {
	id, ok := ctx.Value(callerIdentityContextKey{}).(CallerIdentity)
	if !ok || strings.TrimSpace(id.AccountID) == "" {
		return CallerIdentity{}, false
	}
	return id, true
}
