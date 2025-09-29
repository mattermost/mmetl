package telegram

import (
	"encoding/json"
	"time"
)

// TelegramExport represents the root structure of a Telegram JSON export
type TelegramExport struct {
	Name     string            `json:"name"`
	Type     string            `json:"type"`
	ID       int64             `json:"id"`
	Messages []TelegramMessage `json:"messages"`
}

// TelegramMessage represents a message in the Telegram export
type TelegramMessage struct {
	ID           int64                  `json:"id"`
	Type         string                 `json:"type"` // "message" or "service"
	Date         string                 `json:"date"`
	DateUnixtime string                 `json:"date_unixtime"`
	From         string                 `json:"from,omitempty"`
	FromID       string                 `json:"from_id,omitempty"`
	Actor        string                 `json:"actor,omitempty"`
	ActorID      string                 `json:"actor_id,omitempty"`
	Action       string                 `json:"action,omitempty"`
	Inviter      string                 `json:"inviter,omitempty"`
	Members      []string               `json:"members,omitempty"`
	Text         json.RawMessage        `json:"text"`
	TextEntities []TelegramTextEntity   `json:"text_entities"`

	// Message metadata
	ReplyToMessageID *int64 `json:"reply_to_message_id,omitempty"`
	ForwardedFrom    string `json:"forwarded_from,omitempty"`
	Edited           string `json:"edited,omitempty"`
	EditedUnixtime   string `json:"edited_unixtime,omitempty"`

	// Media fields
	Photo         string `json:"photo,omitempty"`
	PhotoFileSize *int64 `json:"photo_file_size,omitempty"`
	File          string `json:"file,omitempty"`
	FileName      string `json:"file_name,omitempty"`
	FileSize      *int64 `json:"file_size,omitempty"`
	Thumbnail     string `json:"thumbnail,omitempty"`
	MediaType     string `json:"media_type,omitempty"`
	MimeType      string `json:"mime_type,omitempty"`
	Width         *int   `json:"width,omitempty"`
	Height        *int   `json:"height,omitempty"`
	DurationSeconds *int `json:"duration_seconds,omitempty"`
	StickerEmoji  string `json:"sticker_emoji,omitempty"`

	// Reactions
	Reactions []TelegramReaction `json:"reactions,omitempty"`
}

// TelegramTextEntity represents a formatted text entity
type TelegramTextEntity struct {
	Type       string `json:"type"`
	Text       string `json:"text"`
	DocumentID string `json:"document_id,omitempty"` // For custom emojis
	Href       string `json:"href,omitempty"`        // For links
	UserID     *int64 `json:"user_id,omitempty"`     // For mention_name entities
}

// TelegramReaction represents a reaction to a message
type TelegramReaction struct {
	Type       string                    `json:"type"` // "emoji" or "custom_emoji"
	Count      int                       `json:"count"`
	Emoji      string                    `json:"emoji,omitempty"`      // For regular emojis
	DocumentID string                    `json:"document_id,omitempty"` // For custom emojis
	Recent     []TelegramReactionAuthor  `json:"recent"`
}

// TelegramReactionAuthor represents who reacted to a message
type TelegramReactionAuthor struct {
	From   string `json:"from"`
	FromID string `json:"from_id"`
	Date   string `json:"date"`
}

// GetTextAsString extracts text content from the polymorphic Text field
func (m *TelegramMessage) GetTextAsString() (string, error) {
	if len(m.Text) == 0 {
		return "", nil
	}

	// Try to unmarshal as string first
	var textStr string
	if err := json.Unmarshal(m.Text, &textStr); err == nil {
		return textStr, nil
	}

	// Try to unmarshal as array of mixed types
	var textArray []interface{}
	if err := json.Unmarshal(m.Text, &textArray); err != nil {
		return "", err
	}

	var result string
	for _, item := range textArray {
		switch v := item.(type) {
		case string:
			result += v
		case map[string]interface{}:
			if text, ok := v["text"].(string); ok {
				result += text
			}
		}
	}

	return result, nil
}

// GetAuthorInfo returns the author information for the message
func (m *TelegramMessage) GetAuthorInfo() (name, id string) {
	if m.From != "" {
		return m.From, m.FromID
	}
	return m.Actor, m.ActorID
}

// IsServiceMessage returns true if this is a service message
func (m *TelegramMessage) IsServiceMessage() bool {
	return m.Type == "service"
}

// IsRegularMessage returns true if this is a regular message
func (m *TelegramMessage) IsRegularMessage() bool {
	return m.Type == "message"
}

// HasMedia returns true if the message contains media attachments
func (m *TelegramMessage) HasMedia() bool {
	return m.Photo != "" || m.File != ""
}

// HasReactions returns true if the message has reactions
func (m *TelegramMessage) HasReactions() bool {
	return len(m.Reactions) > 0
}

// GetTimestamp returns the message timestamp as a time.Time
func (m *TelegramMessage) GetTimestamp() (time.Time, error) {
	return time.Parse("2006-01-02T15:04:05", m.Date)
}