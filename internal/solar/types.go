package solar

import "time"

type Account struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Nick string `json:"nick"`
}

type AccountProfile map[string]any

type Post map[string]any

type PaginatedPosts struct {
	Items []Post
	Total int
}

type ChatRoom struct {
	ID   string `json:"id"`
	Type int    `json:"type"`
}

type ChatMember struct {
	ID        string    `json:"id"`
	AccountID string    `json:"account_id"`
	Account   Account   `json:"account"`
	Nick      string    `json:"nick"`
	ChatRoom  *ChatRoom `json:"chat_room,omitempty"`
}

type ChatAttachment struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	MIMEType string `json:"mime_type"`
}

type ChatMessage struct {
	ID               string                 `json:"id"`
	Type             string                 `json:"type"`
	Content          string                 `json:"content"`
	Meta             map[string]any         `json:"meta"`
	MembersMentioned []string               `json:"members_mentioned"`
	Attachments      []ChatAttachment       `json:"attachments"`
	RepliedMessageID string                 `json:"replied_message_id"`
	SenderID         string                 `json:"sender_id"`
	Sender           ChatMember             `json:"sender"`
	ChatRoomID       string                 `json:"chat_room_id"`
	ChatRoom         *ChatRoom              `json:"chat_room,omitempty"`
	CreatedAt        time.Time              `json:"created_at"`
	UpdatedAt        time.Time              `json:"updated_at"`
	DeletedAt        *time.Time             `json:"deleted_at"`
	Extra            map[string]interface{} `json:"-"`
}

type Packet struct {
	Type         string `json:"type"`
	Data         any    `json:"data"`
	Endpoint     string `json:"endpoint,omitempty"`
	ErrorMessage string `json:"errorMessage,omitempty"`
}

type InboundMessage struct {
	RoomID           string
	RoomType         int
	MessageID        string
	MessageType      string
	Content          string
	Attachments      []ChatAttachment
	SenderAccountID  string
	SenderName       string
	SenderNick       string
	MentionedBot     bool
	RepliedMessageID string
	CreatedAt        time.Time
}
