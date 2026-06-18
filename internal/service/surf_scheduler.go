package service

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"src.solsynth.dev/sosys/personality/internal/agent"
	"src.solsynth.dev/sosys/personality/internal/config"
	"src.solsynth.dev/sosys/personality/internal/database"
)

type SurfScheduler struct {
	db            *database.DB
	conversations *ConversationService
	registry      *agent.Registry
	cfg           *config.SurfingConfig
	ctx           context.Context
	cancel        context.CancelFunc
	wg            sync.WaitGroup
}

func NewSurfScheduler(db *database.DB, conversations *ConversationService, registry *agent.Registry, cfg *config.SurfingConfig) *SurfScheduler {
	return &SurfScheduler{
		db:            db,
		conversations: conversations,
		registry:      registry,
		cfg:           cfg,
	}
}

func (ss *SurfScheduler) Start(ctx context.Context) {
	if ss.cfg == nil || !ss.cfg.Enabled {
		slog.Info("surf scheduler disabled")
		return
	}
	interval := ss.cfg.Interval
	if interval <= 0 {
		interval = 1 * time.Hour
	}
	ss.ctx, ss.cancel = context.WithCancel(ctx)
	ss.wg.Add(1)
	go ss.run(interval)
	slog.Info("surf scheduler started", "interval", interval)
}

func (ss *SurfScheduler) Stop() {
	if ss.cancel != nil {
		ss.cancel()
	}
	ss.wg.Wait()
	slog.Info("surf scheduler stopped")
}

func (ss *SurfScheduler) run(interval time.Duration) {
	defer ss.wg.Done()
	// ponytail: run once at startup after a short delay, then on interval
	time.Sleep(30 * time.Second)
	ss.tick()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ss.ctx.Done():
			return
		case <-ticker.C:
			ss.tick()
		}
	}
}

func (ss *SurfScheduler) tick() {
	ctx, cancel := context.WithTimeout(ss.ctx, 10*time.Minute)
	defer cancel()

	defs := ss.registry.List()
	for _, def := range defs {
		if !agent.HasAbility(def, "surfing") {
			continue
		}
		if def.SolarIntegration.AccountName == "" {
			continue
		}
		ss.surfAgent(ctx, def)
	}
}

func (ss *SurfScheduler) surfAgent(ctx context.Context, def agent.Definition) {
	log := slog.With("agent_id", def.ID)
	log.Info("surf wake: fetching feed")

	// Fetch latest feed posts
	feed, err := ss.conversations.sn.ListFeed(ctx, def.ID, 0, 10, false)
	if err != nil {
		log.Error("surf wake: failed to fetch feed", "err", err)
		return
	}
	if len(feed.Items) == 0 {
		log.Info("surf wake: feed is empty, skipping")
		return
	}

	// Build a summary of recent posts for context
	var lines []string
	for i, post := range feed.Items {
		content, _ := post["content"].(string)
		if len(content) > 120 {
			content = content[:120] + "..."
		}
		postID, _ := post["id"].(string)
		publisherName := ""
		if pub, ok := post["publisher"].(map[string]any); ok {
			publisherName, _ = pub["name"].(string)
		}
		lines = append(lines, fmt.Sprintf("[%d] %s (by %s, id: %s)", i+1, content, publisherName, postID))
	}

	prompt := fmt.Sprintf("[Surf Wake]\nRecent posts on your feed:\n%s", strings.Join(lines, "\n"))
	if ss.cfg != nil && strings.TrimSpace(ss.cfg.Prompt) != "" {
		prompt += "\n\n" + strings.TrimSpace(ss.cfg.Prompt)
	} else {
		prompt += "\n\nBrowse and interact with posts as you see fit. You can reply, react, repost, or create your own posts."
	}

	// Trigger autonomous run for the first account that has this agent bound
	// ponytail: use the first account we find that has a conversation with this agent
	var accountID string
	var thread database.ConversationThread
	err = ss.db.DB.WithContext(ctx).
		Where("agent_id = ?", def.ID).
		Order("updated_at DESC").
		First(&thread).Error
	if err == nil {
		accountID = thread.AccountID
	}
	if accountID == "" {
		log.Warn("surf wake: no conversation found for agent, skipping")
		return
	}

	_, err = ss.conversations.TriggerAutonomousRun(ctx, def.ID, AutonomousRunInput{
		TargetAccountID: accountID,
		Prompt:          prompt,
		Trigger:         "surf_wake",
	})
	if err != nil {
		log.Error("surf wake: failed to trigger autonomous run", "err", err)
		return
	}
	log.Info("surf wake: triggered autonomous run")
}
