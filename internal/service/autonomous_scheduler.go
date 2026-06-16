package service

import (
	"context"
	"time"

	"src.solsynth.dev/sosys/personality/internal/agent"
	"src.solsynth.dev/sosys/personality/internal/logging"
)

type AutonomousWakeScheduler struct {
	service  *ConversationService
	registry *agent.Registry
	lastWake map[string]time.Time
}

func NewAutonomousWakeScheduler(service *ConversationService, registry *agent.Registry) *AutonomousWakeScheduler {
	return &AutonomousWakeScheduler{
		service:  service,
		registry: registry,
		lastWake: make(map[string]time.Time),
	}
}

func (s *AutonomousWakeScheduler) Run(ctx context.Context) {
	if s == nil || s.service == nil || s.registry == nil {
		return
	}

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		s.tick(ctx, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *AutonomousWakeScheduler) tick(ctx context.Context, now time.Time) {
	for _, def := range s.registry.List() {
		if !agent.HasAbility(def, autonomousAbility) {
			continue
		}
		interval := def.Autonomous.WakeInterval
		if interval <= 0 {
			continue
		}
		if last, ok := s.lastWake[def.ID]; ok && now.Sub(last) < interval {
			continue
		}
		s.lastWake[def.ID] = now
		logging.Log.Info().
			Str("agent_id", def.ID).
			Dur("wake_interval", interval).
			Msg("running periodic autonomous wake")
		if err := s.service.TriggerPeriodicAutonomousWake(ctx, def.ID); err != nil {
			logging.Log.Error().
				Err(err).
				Str("agent_id", def.ID).
				Msg("periodic autonomous wake cycle failed")
		}
	}
}
