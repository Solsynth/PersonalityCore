package solar

import "time"

type Account struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Nick string `json:"nick"`
}

type ChatRoom struct {
	ID string `json:"id"`
}

type ChatMember struct {
	ID        string  `json:"id"`
	AccountID string  `json:"account_id"`
	Account   Account `json:"account"`
	Nick      string  `json:"nick"`
}

type ChatMessage struct {
	ID         string                 `json:"id"`
	Type       string                 `json:"type"`
	Content    string                 `json:"content"`
	Meta       map[string]any         `json:"meta"`
	SenderID   string                 `json:"sender_id"`
	Sender     ChatMember             `json:"sender"`
	ChatRoomID string                 `json:"chat_room_id"`
	CreatedAt  time.Time              `json:"created_at"`
	UpdatedAt  time.Time              `json:"updated_at"`
	DeletedAt  *time.Time             `json:"deleted_at"`
	Extra      map[string]interface{} `json:"-"`
}

type Packet struct {
	Type         string `json:"type"`
	Data         any    `json:"data"`
	Endpoint     string `json:"endpoint,omitempty"`
	ErrorMessage string `json:"errorMessage,omitempty"`
}

type InboundMessage struct {
	RoomID          string
	MessageID       string
	MessageType     string
	Content         string
	SenderAccountID string
	SenderName      string
	SenderNick      string
	CreatedAt       time.Time
}
