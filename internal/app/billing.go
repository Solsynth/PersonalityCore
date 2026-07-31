package app

import (
	"context"
	"time"

	"src.solsynth.dev/sosys/personality/internal/logging"
)

// runBillingSettlement catches up any completed UTC days at startup, then
// settles at each following UTC midnight.
func (a *App) runBillingSettlement(ctx context.Context) {
	settle := func() {
		if err := a.conversations.Billing().SettleCompletedDays(ctx); err != nil {
			logging.Log.Error().Err(err).Msg("daily billing settlement failed")
		}
	}
	settle()
	for {
		now := time.Now().UTC()
		next := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC)
		timer := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			settle()
		}
	}
}
