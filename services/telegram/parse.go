package telegram

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/pkg/errors"
)

// ParseTelegramExportFile parses a Telegram JSON export file
func ParseTelegramExportFile(filePath string) (*TelegramExport, error) {
	// Open the JSON file
	file, err := os.Open(filePath)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to open Telegram export file: %s", filePath)
	}
	defer file.Close()

	// Read the file content
	data, err := io.ReadAll(file)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to read Telegram export file: %s", filePath)
	}

	// Parse the JSON
	var export TelegramExport
	if err := json.Unmarshal(data, &export); err != nil {
		return nil, errors.Wrapf(err, "failed to parse Telegram export JSON: %s", filePath)
	}

	// Validate the export structure
	if err := validateTelegramExport(&export); err != nil {
		return nil, errors.Wrapf(err, "invalid Telegram export format: %s", filePath)
	}

	return &export, nil
}

// validateTelegramExport performs basic validation on the parsed export
func validateTelegramExport(export *TelegramExport) error {
	if export.Name == "" {
		return fmt.Errorf("export missing required 'name' field")
	}

	if export.Type == "" {
		return fmt.Errorf("export missing required 'type' field")
	}

	if export.ID == 0 {
		return fmt.Errorf("export missing required 'id' field")
	}

	if len(export.Messages) == 0 {
		return fmt.Errorf("export contains no messages")
	}

	// Validate a sample of messages to ensure they have required fields
	for i, msg := range export.Messages {
		if i >= 10 { // Only validate first 10 messages for performance
			break
		}

		if msg.ID == 0 {
			return fmt.Errorf("message at index %d missing required 'id' field", i)
		}

		if msg.Type == "" {
			return fmt.Errorf("message at index %d missing required 'type' field", i)
		}

		if msg.Date == "" {
			return fmt.Errorf("message at index %d missing required 'date' field", i)
		}

		// Validate that messages have either from/from_id or actor/actor_id
		name, id := msg.GetAuthorInfo()
		if name == "" || id == "" {
			return fmt.Errorf("message at index %d missing author information", i)
		}
	}

	return nil
}

// GetAttachmentPaths returns all file paths referenced in the export
func GetAttachmentPaths(export *TelegramExport) []string {
	var paths []string
	pathSet := make(map[string]bool) // Use map to avoid duplicates

	for _, msg := range export.Messages {
		// Photo attachments
		if msg.Photo != "" {
			pathSet[msg.Photo] = true
		}

		// File attachments
		if msg.File != "" {
			pathSet[msg.File] = true
		}

		// Thumbnails
		if msg.Thumbnail != "" {
			pathSet[msg.Thumbnail] = true
		}

		// Custom emoji files from text entities
		for _, entity := range msg.TextEntities {
			if entity.DocumentID != "" {
				pathSet[entity.DocumentID] = true
			}
		}

		// Custom emoji files from reactions
		for _, reaction := range msg.Reactions {
			if reaction.DocumentID != "" {
				pathSet[reaction.DocumentID] = true
			}
		}
	}

	// Convert map keys to slice
	for path := range pathSet {
		paths = append(paths, path)
	}

	return paths
}

// ValidateAttachmentPaths checks if all referenced files exist in the export directory
func ValidateAttachmentPaths(export *TelegramExport, exportDir string) []string {
	var missingFiles []string
	paths := GetAttachmentPaths(export)

	for _, path := range paths {
		fullPath := filepath.Join(exportDir, path)
		if _, err := os.Stat(fullPath); os.IsNotExist(err) {
			missingFiles = append(missingFiles, path)
		}
	}

	return missingFiles
}

// UserInfo holds both display name and username information
type UserInfo struct {
	DisplayName string
	Username    string // extracted from mentions
}

// GetUniqueUsers extracts all unique users from the export with usernames when available
func GetUniqueUsers(export *TelegramExport) map[string]*UserInfo {
	users := make(map[string]*UserInfo) // userID -> UserInfo

	// First pass: collect basic user info from messages
	for _, msg := range export.Messages {
		name, id := msg.GetAuthorInfo()
		if name != "" && id != "" {
			if _, exists := users[id]; !exists {
				users[id] = &UserInfo{DisplayName: name}
			}
		}

		// Also collect users from reactions
		for _, reaction := range msg.Reactions {
			for _, author := range reaction.Recent {
				if author.From != "" && author.FromID != "" {
					if _, exists := users[author.FromID]; !exists {
						users[author.FromID] = &UserInfo{DisplayName: author.From}
					}
				}
			}
		}
	}

	// Second pass: extract usernames from mention entities
	for _, msg := range export.Messages {
		for _, entity := range msg.TextEntities {
			if entity.Type == "mention" && entity.Text != "" {
				// Extract username from @username format
				username := strings.TrimPrefix(entity.Text, "@")
				if username != "" && username != entity.Text {
					// Try to find the user this mention refers to by matching display names
					// This is a best-effort approach since we can't definitively map mentions to user IDs
					for userID, userInfo := range users {
						// Simple heuristic: if the username matches part of the display name or ID
						if strings.Contains(strings.ToLower(userInfo.DisplayName), strings.ToLower(username)) ||
							strings.Contains(strings.ToLower(userID), strings.ToLower(username)) {
							userInfo.Username = username
							break
						}
					}
				}
			}

			if entity.Type == "mention_name" && entity.UserID != nil {
				// Convert user_id to our format
				userID := fmt.Sprintf("user%d", *entity.UserID)
				if userInfo, exists := users[userID]; exists {
					// The mention_name provides display name, but we already have that
					// If we find a good username from other mentions, keep it
					if userInfo.Username == "" {
						// As fallback, we could generate a username from display name
						// but let's keep it empty for now and use ID as fallback
					}
				}
			}
		}
	}

	return users
}

// GetMessagesByType returns messages grouped by type
func GetMessagesByType(export *TelegramExport) map[string][]TelegramMessage {
	messagesByType := make(map[string][]TelegramMessage)

	for _, msg := range export.Messages {
		messagesByType[msg.Type] = append(messagesByType[msg.Type], msg)
	}

	return messagesByType
}

// GetMediaStatistics returns statistics about media files in the export
type MediaStatistics struct {
	Photos     int
	Videos     int
	Stickers   int
	Animations int
	Documents  int
	TotalSize  int64
}

func GetMediaStatistics(export *TelegramExport) MediaStatistics {
	var stats MediaStatistics

	for _, msg := range export.Messages {
		if msg.Photo != "" {
			stats.Photos++
			if msg.PhotoFileSize != nil {
				stats.TotalSize += *msg.PhotoFileSize
			}
		}

		if msg.File != "" {
			switch msg.MediaType {
			case "video_file":
				stats.Videos++
			case "sticker":
				stats.Stickers++
			case "animation":
				stats.Animations++
			default:
				stats.Documents++
			}

			if msg.FileSize != nil {
				stats.TotalSize += *msg.FileSize
			}
		}
	}

	return stats
}