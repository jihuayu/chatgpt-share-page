// Package conversation defines the internal snapshot model and the
// normalization from a decoded ChatGPT conversation payload.
package conversation

import "time"

// Version is the ConversationSnapshot schema version.
const Version = 1

// ConversationSnapshot is the stable internal JSON model. ChatGPT's raw
// payload is never exposed to renderers; every artifact is produced from this
// model so other sources can be added later.
type ConversationSnapshot struct {
	Version    int               `json:"version"`
	ID         string            `json:"id"`
	Title      string            `json:"title"`
	Source     SnapshotSource    `json:"source"`
	ImportedAt time.Time         `json:"imported_at"`
	UpdatedAt  time.Time         `json:"updated_at"`
	Messages   []ConversationMsg `json:"messages"`
	Metadata   SnapshotMetadata  `json:"metadata"`
}

// SnapshotSource records where the snapshot came from.
type SnapshotSource struct {
	Provider string `json:"provider"` // chatgpt
	URL      string `json:"url"`
	ShareID  string `json:"share_id"`
}

// SnapshotMetadata records import options and conversation facts.
type SnapshotMetadata struct {
	ConversationID string `json:"conversation_id,omitempty"`
	MessageCount   int    `json:"message_count"`
	Model          string `json:"model,omitempty"`
	IncludeHidden  bool   `json:"include_hidden"`
	AllNodes       bool   `json:"all_nodes"`
	Timezone       string `json:"timezone"`
}

// ConversationMsg is one normalized message.
type ConversationMsg struct {
	ID        string         `json:"id"`
	Role      string         `json:"role"`
	CreatedAt *time.Time     `json:"created_at,omitempty"`
	Blocks    []ContentBlock `json:"blocks"`
	Hidden    bool           `json:"hidden"`
}

// ContentBlock is a typed piece of message content.
type ContentBlock struct {
	Type      string `json:"type"` // markdown/code/quote/citation/attachment
	Content   string `json:"content,omitempty"`
	Language  string `json:"language,omitempty"`
	Title     string `json:"title,omitempty"`
	URL       string `json:"url,omitempty"`
	AssetName string `json:"asset_name,omitempty"`
}
