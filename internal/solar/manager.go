package solar

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/websocket"

	"src.solsynth.dev/sosys/personality/internal/agent"
	"src.solsynth.dev/sosys/personality/internal/config"
	"src.solsynth.dev/sosys/personality/internal/logging"
)

const (
	heartbeatInterval           = 60 * time.Second
	subscriptionRefreshInterval = 4 * time.Minute
	maxReconnectDelay           = 30 * time.Second
)

type Manager struct {
	baseURL          string
	registry         *agent.Registry
	loadTrackedRooms func(context.Context, string) ([]TrackedRoomState, error)
	onMessage        func(context.Context, string, InboundMessage) error

	mu      sync.RWMutex
	agents  map[string]*agentConnection
	ctx     context.Context
	cancel  context.CancelFunc
	started bool
	wg      sync.WaitGroup
}

func NewManager(
	cfg *config.Config,
	registry *agent.Registry,
	loadTrackedRooms func(context.Context, string) ([]TrackedRoomState, error),
	onMessage func(context.Context, string, InboundMessage) error,
) *Manager {
	return &Manager{
		baseURL:          strings.TrimRight(strings.TrimSpace(cfg.SolarNetwork.BaseURL), "/"),
		registry:         registry,
		loadTrackedRooms: loadTrackedRooms,
		onMessage:        onMessage,
		agents:           make(map[string]*agentConnection),
	}
}

func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.started {
		return nil
	}

	m.ctx, m.cancel = context.WithCancel(ctx)
	for _, def := range m.registry.List() {
		if !agent.HasAbility(def, "chat") {
			continue
		}
		conn := newAgentConnection(m.baseURL, def, m.loadTrackedRooms, m.onMessage)
		m.agents[def.ID] = conn
		m.wg.Add(1)
		go func(c *agentConnection) {
			defer m.wg.Done()
			c.run(m.ctx)
		}(conn)
	}
	m.started = true
	return nil
}

func (m *Manager) Stop(context.Context) error {
	m.mu.Lock()
	if !m.started {
		m.mu.Unlock()
		return nil
	}
	m.cancel()
	m.mu.Unlock()

	m.wg.Wait()

	m.mu.Lock()
	m.started = false
	m.mu.Unlock()
	return nil
}

func (m *Manager) TrackRoom(agentID, roomID string) {
	if strings.TrimSpace(roomID) == "" {
		return
	}
	conn := m.getAgent(agentID)
	if conn == nil {
		return
	}
	conn.trackRoom(roomID)
}

func (m *Manager) SendBotMessage(ctx context.Context, agentID, roomID, targetAccountName, content string) (string, string, error) {
	conn := m.getAgent(agentID)
	if conn == nil {
		return "", "", fmt.Errorf("solar chat integration unavailable for agent %q", agentID)
	}
	return conn.sendBotMessage(ctx, roomID, targetAccountName, content)
}

func (m *Manager) getAgent(agentID string) *agentConnection {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.agents[strings.TrimSpace(agentID)]
}

type agentConnection struct {
	baseURL string
	def     agent.Definition
	client  *Client

	loadTrackedRooms func(context.Context, string) ([]TrackedRoomState, error)
	onMessage        func(context.Context, string, InboundMessage) error

	roomMu       sync.RWMutex
	trackedRooms map[string]TrackedRoomState

	connMu sync.Mutex
	conn   *websocket.Conn

	botMu        sync.Mutex
	botAccountID string
}

type TrackedRoomState struct {
	RoomID        string
	LastMessageAt *time.Time
}

func newAgentConnection(
	baseURL string,
	def agent.Definition,
	loadTrackedRooms func(context.Context, string) ([]TrackedRoomState, error),
	onMessage func(context.Context, string, InboundMessage) error,
) *agentConnection {
	return &agentConnection{
		baseURL:          baseURL,
		def:              def,
		client:           NewClient(baseURL, def.SolarIntegration.AccessToken),
		loadTrackedRooms: loadTrackedRooms,
		onMessage:        onMessage,
		trackedRooms:     make(map[string]TrackedRoomState),
	}
}

func (c *agentConnection) run(ctx context.Context) {
	c.loadPersistedRooms(ctx)

	delay := 500 * time.Millisecond
	for {
		if ctx.Err() != nil {
			c.closeConn()
			return
		}

		if err := c.connectAndServe(ctx); err != nil && ctx.Err() == nil {
			logging.Log.Error().
				Err(err).
				Str("agent_id", c.def.ID).
				Msg("solar websocket connection ended")
		}

		select {
		case <-ctx.Done():
			c.closeConn()
			return
		case <-time.After(withJitter(delay)):
		}

		logging.Log.Debug().
			Str("agent_id", c.def.ID).
			Dur("reconnect_delay", delay).
			Msg("retrying solar websocket connection")

		delay *= 2
		if delay > maxReconnectDelay {
			delay = maxReconnectDelay
		}
	}
}

func (c *agentConnection) sendBotMessage(ctx context.Context, roomID, targetAccountName, content string) (string, string, error) {
	logging.Log.Info().
		Str("agent_id", c.def.ID).
		Str("room_id", strings.TrimSpace(roomID)).
		Str("target_account_name", strings.TrimSpace(targetAccountName)).
		Int("message_chars", len(strings.TrimSpace(content))).
		Msg("preparing solar bot message send")

	resolvedRoomID := strings.TrimSpace(roomID)
	if resolvedRoomID == "" {
		target, err := c.client.ResolveAccountByName(ctx, targetAccountName)
		if err != nil {
			return "", "", err
		}
		room, err := c.client.CreateDirectMessage(ctx, target.ID)
		if err != nil {
			return "", "", err
		}
		resolvedRoomID = room.ID
		logging.Log.Info().
			Str("agent_id", c.def.ID).
			Str("target_account_name", targetAccountName).
			Str("room_id", resolvedRoomID).
			Msg("resolved solar direct message room")
	}

	c.trackRoom(resolvedRoomID)
	c.sendTypingStatus(resolvedRoomID)
	msg, err := c.client.SendMessage(ctx, resolvedRoomID, content)
	if err != nil {
		return "", "", err
	}
	c.markRoomSeen(resolvedRoomID, msg.CreatedAt)
	logging.Log.Info().
		Str("agent_id", c.def.ID).
		Str("room_id", resolvedRoomID).
		Str("message_id", msg.ID).
		Msg("solar bot message sent")
	return resolvedRoomID, msg.ID, nil
}

func (c *agentConnection) loadPersistedRooms(ctx context.Context) {
	if c.loadTrackedRooms == nil {
		return
	}
	rooms, err := c.loadTrackedRooms(ctx, c.def.ID)
	if err != nil {
		logging.Log.Error().
			Err(err).
			Str("agent_id", c.def.ID).
			Msg("failed to load persisted solar rooms")
		return
	}
	for _, room := range rooms {
		c.trackRoomState(room)
	}
	logging.Log.Debug().
		Str("agent_id", c.def.ID).
		Int("tracked_room_count", len(rooms)).
		Msg("loaded persisted solar rooms")
}

func (c *agentConnection) trackRoom(roomID string) {
	c.trackRoomState(TrackedRoomState{RoomID: roomID})
}

func (c *agentConnection) trackRoomState(state TrackedRoomState) {
	roomID := strings.TrimSpace(state.RoomID)
	if roomID == "" {
		return
	}
	c.roomMu.Lock()
	existing, existed := c.trackedRooms[roomID]
	if state.LastMessageAt == nil {
		state.LastMessageAt = existing.LastMessageAt
	}
	state.RoomID = roomID
	c.trackedRooms[roomID] = state
	c.roomMu.Unlock()
	if !existed {
		logging.Log.Info().
			Str("agent_id", c.def.ID).
			Str("room_id", roomID).
			Msg("tracking solar room")
		c.subscribeRoom(roomID)
	}
}

func (c *agentConnection) trackedRoomIDs() []string {
	c.roomMu.RLock()
	defer c.roomMu.RUnlock()

	out := make([]string, 0, len(c.trackedRooms))
	for roomID := range c.trackedRooms {
		out = append(out, roomID)
	}
	return out
}

func (c *agentConnection) trackedRoomStates() []TrackedRoomState {
	c.roomMu.RLock()
	defer c.roomMu.RUnlock()

	out := make([]TrackedRoomState, 0, len(c.trackedRooms))
	for _, state := range c.trackedRooms {
		out = append(out, state)
	}
	return out
}

func (c *agentConnection) markRoomSeen(roomID string, at time.Time) {
	roomID = strings.TrimSpace(roomID)
	if roomID == "" {
		return
	}
	if at.IsZero() {
		at = time.Now()
	}
	c.roomMu.Lock()
	defer c.roomMu.Unlock()
	state := c.trackedRooms[roomID]
	state.RoomID = roomID
	state.LastMessageAt = &at
	c.trackedRooms[roomID] = state
}

func (c *agentConnection) ensureBotAccountID(ctx context.Context) (string, error) {
	c.botMu.Lock()
	defer c.botMu.Unlock()
	if c.botAccountID != "" {
		return c.botAccountID, nil
	}
	account, err := c.client.ResolveAccountByName(ctx, c.def.SolarIntegration.AccountName)
	if err != nil {
		return "", err
	}
	c.botAccountID = account.ID
	logging.Log.Debug().
		Str("agent_id", c.def.ID).
		Str("bot_account_name", c.def.SolarIntegration.AccountName).
		Str("bot_account_id", c.botAccountID).
		Msg("resolved solar bot account")
	return c.botAccountID, nil
}

func (c *agentConnection) connectAndServe(ctx context.Context) error {
	wsURL := strings.TrimPrefix(c.baseURL, "http://")
	wsURL = strings.TrimPrefix(wsURL, "https://")
	if strings.HasPrefix(c.baseURL, "https://") {
		wsURL = "wss://" + wsURL + "/ws"
	} else {
		wsURL = "ws://" + wsURL + "/ws"
	}

	cfg, err := websocket.NewConfig(wsURL, c.baseURL)
	if err != nil {
		return err
	}
	cfg.Header = http.Header{}
	cfg.Header.Set("Authorization", "Bearer "+c.def.SolarIntegration.AccessToken)

	conn, err := websocket.DialConfig(cfg)
	if err != nil {
		return err
	}
	c.setConn(conn)
	defer c.closeConn()

	logging.Log.Info().
		Str("agent_id", c.def.ID).
		Int("tracked_room_count", len(c.trackedRoomIDs())).
		Msg("solar websocket connected")

	c.subscribeAllRooms()
	c.catchUpTrackedRooms(ctx)
	return c.serveLoop(ctx, conn)
}

func (c *agentConnection) serveLoop(ctx context.Context, conn *websocket.Conn) error {
	receiveCh := make(chan Packet)
	errCh := make(chan error, 1)

	go c.readLoop(conn, receiveCh, errCh)

	heartbeatTicker := time.NewTicker(heartbeatInterval)
	defer heartbeatTicker.Stop()

	refreshTicker := time.NewTicker(subscriptionRefreshInterval)
	defer refreshTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case pkt := <-receiveCh:
			logging.Log.Debug().
				Str("agent_id", c.def.ID).
				Str("packet_type", pkt.Type).
				Str("endpoint", pkt.Endpoint).
				Msg("received solar websocket packet")
			if err := c.handlePacket(ctx, pkt); err != nil {
				return err
			}
		case err := <-errCh:
			return err
		case <-heartbeatTicker.C:
			logging.Log.Debug().
				Str("agent_id", c.def.ID).
				Msg("sending solar websocket ping")
			if err := c.sendPacket(Packet{Type: "ping"}); err != nil {
				return err
			}
		case <-refreshTicker.C:
			logging.Log.Debug().
				Str("agent_id", c.def.ID).
				Int("tracked_room_count", len(c.trackedRoomIDs())).
				Msg("refreshing solar websocket room subscriptions")
			c.subscribeAllRooms()
		}
	}
}

func (c *agentConnection) readLoop(conn *websocket.Conn, out chan<- Packet, errCh chan<- error) {
	for {
		var raw string
		if err := websocket.Message.Receive(conn, &raw); err != nil {
			errCh <- err
			return
		}

		var pkt Packet
		if err := json.Unmarshal([]byte(raw), &pkt); err != nil {
			errCh <- fmt.Errorf("decode solar websocket packet: %w", err)
			return
		}
		out <- pkt
	}
}

func (c *agentConnection) handlePacket(ctx context.Context, pkt Packet) error {
	switch pkt.Type {
	case "pong":
		logging.Log.Debug().
			Str("agent_id", c.def.ID).
			Msg("received solar websocket pong")
		return nil
	case "error", "error.dupe":
		return fmt.Errorf("solar websocket error for agent %s: %s", c.def.ID, pkt.ErrorMessage)
	case "messages.new":
		msg, err := decodeMessage(pkt.Data)
		if err != nil {
			return err
		}
		logging.Log.Info().
			Str("agent_id", c.def.ID).
			Str("room_id", msg.ChatRoomID).
			Str("message_id", msg.ID).
			Str("message_type", firstNonEmpty(msg.Type, "text")).
			Str("sender_account_id", msg.Sender.AccountID).
			Int("content_chars", len(strings.TrimSpace(msg.Content))).
			Msg("received solar chat message")
		botAccountID, err := c.ensureBotAccountID(ctx)
		if err != nil {
			return err
		}
		if strings.TrimSpace(msg.Sender.AccountID) == strings.TrimSpace(botAccountID) {
			logging.Log.Debug().
				Str("agent_id", c.def.ID).
				Str("message_id", msg.ID).
				Msg("ignoring self-authored solar message")
			return nil
		}
		if c.onMessage == nil {
			logging.Log.Debug().
				Str("agent_id", c.def.ID).
				Str("message_id", msg.ID).
				Msg("solar message received but no inbound handler is registered")
			return nil
		}
		return c.onMessage(ctx, c.def.ID, InboundMessage{
			RoomID:          msg.ChatRoomID,
			MessageID:       msg.ID,
			MessageType:     firstNonEmpty(msg.Type, "text"),
			Content:         strings.TrimSpace(msg.Content),
			SenderAccountID: msg.Sender.AccountID,
			SenderName:      msg.Sender.Account.Name,
			SenderNick:      firstNonEmpty(msg.Sender.Nick, msg.Sender.Account.Nick),
			CreatedAt:       msg.CreatedAt,
		})
	default:
		logging.Log.Debug().
			Str("agent_id", c.def.ID).
			Str("packet_type", pkt.Type).
			Msg("ignoring unsupported solar websocket packet")
		return nil
	}
}

func (c *agentConnection) subscribeAllRooms() {
	for _, roomID := range c.trackedRoomIDs() {
		c.subscribeRoom(roomID)
	}
}

func (c *agentConnection) catchUpTrackedRooms(ctx context.Context) {
	states := c.trackedRoomStates()
	if len(states) == 0 {
		return
	}
	logging.Log.Info().
		Str("agent_id", c.def.ID).
		Int("tracked_room_count", len(states)).
		Msg("catching up tracked solar rooms after connect")

	for _, state := range states {
		if err := c.catchUpRoom(ctx, state); err != nil {
			logging.Log.Error().
				Err(err).
				Str("agent_id", c.def.ID).
				Str("room_id", state.RoomID).
				Msg("failed to catch up solar room history")
		}
	}
}

func (c *agentConnection) catchUpRoom(ctx context.Context, state TrackedRoomState) error {
	roomID := strings.TrimSpace(state.RoomID)
	if roomID == "" {
		return nil
	}

	botAccountID, err := c.ensureBotAccountID(ctx)
	if err != nil {
		return err
	}

	var messages []ChatMessage
	offset := 0
	for {
		batch, err := c.client.ListMessages(ctx, roomID, offset, 100)
		if err != nil {
			return err
		}
		if len(batch) == 0 {
			break
		}

		reachedSeenBoundary := false
		for _, msg := range batch {
			if state.LastMessageAt != nil && !msg.CreatedAt.After(*state.LastMessageAt) {
				reachedSeenBoundary = true
				continue
			}
			messages = append(messages, msg)
		}
		if reachedSeenBoundary || len(batch) < 100 {
			break
		}
		offset += len(batch)
	}

	if len(messages) == 0 {
		logging.Log.Debug().
			Str("agent_id", c.def.ID).
			Str("room_id", roomID).
			Msg("solar room catch-up found no unread messages")
		return nil
	}

	sort.Slice(messages, func(i, j int) bool {
		return messages[i].CreatedAt.Before(messages[j].CreatedAt)
	})

	processed := 0
	for _, msg := range messages {
		if strings.TrimSpace(msg.Sender.AccountID) == strings.TrimSpace(botAccountID) {
			c.markRoomSeen(roomID, msg.CreatedAt)
			continue
		}
		if strings.TrimSpace(msg.Content) == "" {
			c.markRoomSeen(roomID, msg.CreatedAt)
			continue
		}

		logging.Log.Info().
			Str("agent_id", c.def.ID).
			Str("room_id", roomID).
			Str("message_id", msg.ID).
			Str("message_type", firstNonEmpty(msg.Type, "text")).
			Msg("replaying historical solar message")

		if c.onMessage != nil {
			if err := c.onMessage(ctx, c.def.ID, InboundMessage{
				RoomID:          msg.ChatRoomID,
				MessageID:       msg.ID,
				MessageType:     firstNonEmpty(msg.Type, "text"),
				Content:         strings.TrimSpace(msg.Content),
				SenderAccountID: msg.Sender.AccountID,
				SenderName:      msg.Sender.Account.Name,
				SenderNick:      firstNonEmpty(msg.Sender.Nick, msg.Sender.Account.Nick),
				CreatedAt:       msg.CreatedAt,
			}); err != nil {
				return err
			}
		}
		c.markRoomSeen(roomID, msg.CreatedAt)
		processed++
	}

	logging.Log.Info().
		Str("agent_id", c.def.ID).
		Str("room_id", roomID).
		Int("replayed_count", processed).
		Msg("completed solar room catch-up")
	return nil
}

func (c *agentConnection) subscribeRoom(roomID string) {
	logging.Log.Debug().
		Str("agent_id", c.def.ID).
		Str("room_id", roomID).
		Msg("subscribing solar room over websocket")
	_ = c.sendPacket(Packet{
		Type:     "messages.subscribe",
		Endpoint: "messager",
		Data: map[string]any{
			"chat_room_id": roomID,
		},
	})
}

func (c *agentConnection) sendTypingStatus(roomID string) {
	roomID = strings.TrimSpace(roomID)
	if roomID == "" {
		return
	}
	now := time.Now().UTC()
	logging.Log.Debug().
		Str("agent_id", c.def.ID).
		Str("room_id", roomID).
		Msg("sending solar typing packet")
	_ = c.sendPacket(Packet{
		Type:     "messages.typing",
		Endpoint: "messager",
		Data: map[string]any{
			"chat_room_id": roomID,
			"ts":           now.UnixMilli(),
			"type":         "typing",
		},
	})
}

func (c *agentConnection) sendPacket(pkt Packet) error {
	raw, err := json.Marshal(pkt)
	if err != nil {
		return err
	}

	c.connMu.Lock()
	defer c.connMu.Unlock()
	if c.conn == nil {
		return nil
	}
	return websocket.Message.Send(c.conn, string(raw))
}

func (c *agentConnection) setConn(conn *websocket.Conn) {
	c.connMu.Lock()
	defer c.connMu.Unlock()
	c.conn = conn
}

func (c *agentConnection) closeConn() {
	c.connMu.Lock()
	defer c.connMu.Unlock()
	if c.conn != nil {
		_ = c.conn.Close()
		c.conn = nil
	}
}

func decodeMessage(data any) (*ChatMessage, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	var msg ChatMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		return nil, fmt.Errorf("decode solar chat message: %w", err)
	}
	return &msg, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func withJitter(delay time.Duration) time.Duration {
	jitter := time.Duration(rand.Intn(200)-100) * time.Millisecond
	next := delay + jitter
	if next < 100*time.Millisecond {
		return 100 * time.Millisecond
	}
	return next
}
