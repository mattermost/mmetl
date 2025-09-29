package telegram

import (
	"encoding/json"
	"testing"
)

func TestTelegramMessageGetTextAsString(t *testing.T) {
	tests := []struct {
		name     string
		textJSON string
		expected string
		hasError bool
	}{
		{
			name:     "simple string text",
			textJSON: `"Hello world"`,
			expected: "Hello world",
			hasError: false,
		},
		{
			name:     "empty string",
			textJSON: `""`,
			expected: "",
			hasError: false,
		},
		{
			name:     "text with custom emoji",
			textJSON: `["Es lo primero que mire ", {"type":"custom_emoji","text":"😝","document_id":"stickers/AnimatedSticker (1).tgs"}, ""]`,
			expected: "Es lo primero que mire 😝",
			hasError: false,
		},
		{
			name:     "mixed content array",
			textJSON: `[{"type":"custom_emoji","text":"🧐","document_id":"stickers/AnimatedSticker.tgs"},"Ha pasado algo que requiera este leve subterfugio?"]`,
			expected: "🧐Ha pasado algo que requiera este leve subterfugio?",
			hasError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := TelegramMessage{
				Text: json.RawMessage(tt.textJSON),
			}

			result, err := msg.GetTextAsString()

			if tt.hasError && err == nil {
				t.Errorf("Expected error but got none")
			}
			if !tt.hasError && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
			if result != tt.expected {
				t.Errorf("Expected %q, got %q", tt.expected, result)
			}
		})
	}
}

func TestTelegramMessageGetAuthorInfo(t *testing.T) {
	tests := []struct {
		name         string
		msg          TelegramMessage
		expectedName string
		expectedID   string
	}{
		{
			name: "regular message",
			msg: TelegramMessage{
				From:   "John Doe",
				FromID: "user123",
			},
			expectedName: "John Doe",
			expectedID:   "user123",
		},
		{
			name: "service message",
			msg: TelegramMessage{
				Actor:   "Jane Smith",
				ActorID: "user456",
			},
			expectedName: "Jane Smith",
			expectedID:   "user456",
		},
		{
			name: "prefer from over actor",
			msg: TelegramMessage{
				From:    "John Doe",
				FromID:  "user123",
				Actor:   "Jane Smith",
				ActorID: "user456",
			},
			expectedName: "John Doe",
			expectedID:   "user123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name, id := tt.msg.GetAuthorInfo()
			if name != tt.expectedName {
				t.Errorf("Expected name %q, got %q", tt.expectedName, name)
			}
			if id != tt.expectedID {
				t.Errorf("Expected ID %q, got %q", tt.expectedID, id)
			}
		})
	}
}

func TestTelegramMessageTypeCheckers(t *testing.T) {
	tests := []struct {
		name              string
		msgType           string
		expectedService   bool
		expectedRegular   bool
	}{
		{
			name:            "service message",
			msgType:         "service",
			expectedService: true,
			expectedRegular: false,
		},
		{
			name:            "regular message",
			msgType:         "message",
			expectedService: false,
			expectedRegular: true,
		},
		{
			name:            "unknown type",
			msgType:         "unknown",
			expectedService: false,
			expectedRegular: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := TelegramMessage{Type: tt.msgType}

			if msg.IsServiceMessage() != tt.expectedService {
				t.Errorf("IsServiceMessage(): expected %t, got %t", tt.expectedService, msg.IsServiceMessage())
			}
			if msg.IsRegularMessage() != tt.expectedRegular {
				t.Errorf("IsRegularMessage(): expected %t, got %t", tt.expectedRegular, msg.IsRegularMessage())
			}
		})
	}
}

func TestTelegramMessageHasMedia(t *testing.T) {
	tests := []struct {
		name     string
		msg      TelegramMessage
		expected bool
	}{
		{
			name:     "no media",
			msg:      TelegramMessage{},
			expected: false,
		},
		{
			name: "has photo",
			msg: TelegramMessage{
				Photo: "photos/photo_1.jpg",
			},
			expected: true,
		},
		{
			name: "has file",
			msg: TelegramMessage{
				File: "video_files/video.mp4",
			},
			expected: true,
		},
		{
			name: "has both",
			msg: TelegramMessage{
				Photo: "photos/photo_1.jpg",
				File:  "video_files/video.mp4",
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.msg.HasMedia() != tt.expected {
				t.Errorf("HasMedia(): expected %t, got %t", tt.expected, tt.msg.HasMedia())
			}
		})
	}
}

func TestTelegramMessageHasReactions(t *testing.T) {
	tests := []struct {
		name     string
		msg      TelegramMessage
		expected bool
	}{
		{
			name:     "no reactions",
			msg:      TelegramMessage{},
			expected: false,
		},
		{
			name: "has reactions",
			msg: TelegramMessage{
				Reactions: []TelegramReaction{
					{
						Type:  "emoji",
						Emoji: "👍",
						Count: 1,
					},
				},
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.msg.HasReactions() != tt.expected {
				t.Errorf("HasReactions(): expected %t, got %t", tt.expected, tt.msg.HasReactions())
			}
		})
	}
}