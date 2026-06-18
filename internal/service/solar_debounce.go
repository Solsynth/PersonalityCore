package service

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"src.solsynth.dev/sosys/personality/internal/logging"
)

type snInboundBatcher struct {
	delay time.Duration
	flush func(context.Context, string, []ExternalInboundMessage) error

	mu      sync.Mutex
	batches map[string]*snInboundBatch
}

type snInboundBatch struct {
	agentID string
	roomID  string
	seq     int64
	timer   *time.Timer
	items   []ExternalInboundMessage
}

func newSnInboundBatcher(delay time.Duration, flush func(context.Context, string, []ExternalInboundMessage) error) *snInboundBatcher {
	if delay < 100*time.Millisecond {
		delay = 100 * time.Millisecond
	}
	return &snInboundBatcher{
		delay:   delay,
		flush:   flush,
		batches: make(map[string]*snInboundBatch),
	}
}

func (b *snInboundBatcher) Enqueue(ctx context.Context, agentID string, input ExternalInboundMessage) error {
	if b == nil {
		return fmt.Errorf("solar inbound batcher is not configured")
	}
	key := solarInboundBatchKey(agentID, input.RoomID)

	b.mu.Lock()
	batch, ok := b.batches[key]
	if !ok {
		batch = &snInboundBatch{
			agentID: strings.TrimSpace(agentID),
			roomID:  strings.TrimSpace(input.RoomID),
		}
		b.batches[key] = batch
	}
	if batch.timer != nil {
		batch.timer.Stop()
	}
	batch.items = append(batch.items, input)
	batch.seq++
	queuedCount := len(batch.items)
	seq := batch.seq
	batch.timer = time.AfterFunc(b.delay, func() {
		if err := b.flushBatch(context.Background(), key, seq); err != nil {
			logging.Log.Error().
				Err(err).
				Str("agent_id", strings.TrimSpace(agentID)).
				Str("room_id", strings.TrimSpace(input.RoomID)).
				Msg("failed to flush solar inbound batch")
		}
	})
	b.mu.Unlock()

	logging.Log.Debug().
		Str("agent_id", strings.TrimSpace(agentID)).
		Str("room_id", strings.TrimSpace(input.RoomID)).
		Int("queued_messages", queuedCount).
		Dur("debounce_delay", b.delay).
		Msg("queued solar inbound message for debounce")

	return nil
}

func (b *snInboundBatcher) FlushAll(ctx context.Context) error {
	if b == nil {
		return nil
	}

	b.mu.Lock()
	keys := make([]string, 0, len(b.batches))
	for key := range b.batches {
		keys = append(keys, key)
	}
	b.mu.Unlock()

	for _, key := range keys {
		if err := b.flushBatch(ctx, key, 0); err != nil {
			return err
		}
	}
	return nil
}

func (b *snInboundBatcher) flushBatch(ctx context.Context, key string, seq int64) error {
	if b == nil {
		return nil
	}

	b.mu.Lock()
	batch, ok := b.batches[key]
	if !ok {
		b.mu.Unlock()
		return nil
	}
	if seq != 0 && batch.seq != seq {
		b.mu.Unlock()
		return nil
	}
	delete(b.batches, key)
	if batch.timer != nil {
		batch.timer.Stop()
	}
	items := append([]ExternalInboundMessage(nil), batch.items...)
	b.mu.Unlock()

	if len(items) == 0 {
		return nil
	}

	sort.Slice(items, func(i, j int) bool {
		if items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].MessageID < items[j].MessageID
		}
		return items[i].CreatedAt.Before(items[j].CreatedAt)
	})

	logging.Log.Info().
		Str("agent_id", strings.TrimSpace(batch.agentID)).
		Str("room_id", strings.TrimSpace(batch.roomID)).
		Int("message_count", len(items)).
		Msg("flushing debounced solar inbound batch")

	if b.flush != nil {
		return b.flush(ctx, batch.agentID, items)
	}
	return nil
}

func solarInboundBatchKey(agentID, roomID string) string {
	return strings.TrimSpace(agentID) + "|" + strings.TrimSpace(roomID)
}
