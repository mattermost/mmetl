package telegram

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/mattermost/mattermost/server/public/model"
	log "github.com/sirupsen/logrus"
)

// ValidateExportStructure performs comprehensive validation on the Telegram export
func ValidateExportStructure(export *TelegramExport) []ValidationError {
	var errors []ValidationError

	// Validate root fields
	if export.Name == "" {
		errors = append(errors, ValidationError{
			Type:    "missing_field",
			Field:   "name",
			Message: "Export missing required 'name' field",
		})
	}

	if export.Type == "" {
		errors = append(errors, ValidationError{
			Type:    "missing_field",
			Field:   "type",
			Message: "Export missing required 'type' field",
		})
	}

	if export.ID == 0 {
		errors = append(errors, ValidationError{
			Type:    "missing_field",
			Field:   "id",
			Message: "Export missing required 'id' field",
		})
	}

	if len(export.Messages) == 0 {
		errors = append(errors, ValidationError{
			Type:    "empty_data",
			Field:   "messages",
			Message: "Export contains no messages",
		})
	}

	// Validate messages
	for i, msg := range export.Messages {
		msgErrors := validateMessage(msg, i)
		errors = append(errors, msgErrors...)
	}

	return errors
}

// ValidationError represents a validation issue
type ValidationError struct {
	Type    string `json:"type"`
	Field   string `json:"field"`
	Message string `json:"message"`
	Index   *int   `json:"index,omitempty"`
}

func (e ValidationError) Error() string {
	if e.Index != nil {
		return fmt.Sprintf("%s at index %d: %s", e.Field, *e.Index, e.Message)
	}
	return fmt.Sprintf("%s: %s", e.Field, e.Message)
}

// validateMessage validates a single message
func validateMessage(msg TelegramMessage, index int) []ValidationError {
	var errors []ValidationError

	if msg.ID == 0 {
		errors = append(errors, ValidationError{
			Type:    "missing_field",
			Field:   "id",
			Message: "Message missing required 'id' field",
			Index:   &index,
		})
	}

	if msg.Type == "" {
		errors = append(errors, ValidationError{
			Type:    "missing_field",
			Field:   "type",
			Message: "Message missing required 'type' field",
			Index:   &index,
		})
	}

	if msg.Date == "" {
		errors = append(errors, ValidationError{
			Type:    "missing_field",
			Field:   "date",
			Message: "Message missing required 'date' field",
			Index:   &index,
		})
	}

	// Validate author information
	name, id := msg.GetAuthorInfo()
	if name == "" || id == "" {
		errors = append(errors, ValidationError{
			Type:    "missing_author",
			Field:   "author",
			Message: "Message missing author information (from/from_id or actor/actor_id)",
			Index:   &index,
		})
	}

	// Validate message-specific fields
	if msg.Type == "message" {
		// Regular messages should have some content
		text, _ := msg.GetTextAsString()
		if text == "" && !msg.HasMedia() {
			errors = append(errors, ValidationError{
				Type:    "empty_content",
				Field:   "text",
				Message: "Message has no text content and no media attachments",
				Index:   &index,
			})
		}
	}

	if msg.Type == "service" && msg.Action == "" {
		errors = append(errors, ValidationError{
			Type:    "missing_field",
			Field:   "action",
			Message: "Service message missing required 'action' field",
			Index:   &index,
		})
	}

	return errors
}

// ValidateForMattermost checks if the transformed data meets Mattermost requirements
func ValidateForMattermost(export *TelegramExport, logger log.FieldLogger) []ValidationError {
	var errors []ValidationError

	// Check channel name length
	channelName := strings.ToLower(strings.ReplaceAll(export.Name, " ", "-"))
	channelName = strings.Trim(channelName, "_-")

	if len(channelName) > model.ChannelNameMaxLength {
		errors = append(errors, ValidationError{
			Type:    "length_exceeded",
			Field:   "channel_name",
			Message: fmt.Sprintf("Channel name exceeds maximum length of %d characters", model.ChannelNameMaxLength),
		})
	}

	// Check display name length
	if utf8.RuneCountInString(export.Name) > model.ChannelDisplayNameMaxRunes {
		errors = append(errors, ValidationError{
			Type:    "length_exceeded",
			Field:   "channel_display_name",
			Message: fmt.Sprintf("Channel display name exceeds maximum length of %d runes", model.ChannelDisplayNameMaxRunes),
		})
	}

	// Validate user information
	users := GetUniqueUsers(export)
	for userID, userInfo := range users {
		userErrors := validateUserForMattermost(userID, userInfo.DisplayName)
		errors = append(errors, userErrors...)
	}

	// Validate message content lengths
	for i, msg := range export.Messages {
		msgErrors := validateMessageForMattermost(msg, i)
		errors = append(errors, msgErrors...)
	}

	return errors
}

// validateUserForMattermost validates user data against Mattermost limits
func validateUserForMattermost(userID, displayName string) []ValidationError {
	var errors []ValidationError

	if userID == "" {
		errors = append(errors, ValidationError{
			Type:    "invalid_user",
			Field:   "user_id",
			Message: "User ID cannot be empty",
		})
	}

	if displayName == "" {
		errors = append(errors, ValidationError{
			Type:    "invalid_user",
			Field:   "display_name",
			Message: "User display name cannot be empty",
		})
	}

	// Split name and check lengths
	nameParts := strings.Fields(displayName)
	if len(nameParts) > 0 {
		firstName := nameParts[0]
		if utf8.RuneCountInString(firstName) > model.UserFirstNameMaxRunes {
			errors = append(errors, ValidationError{
				Type:    "length_exceeded",
				Field:   "user_first_name",
				Message: fmt.Sprintf("User first name exceeds maximum length of %d runes", model.UserFirstNameMaxRunes),
			})
		}

		if len(nameParts) > 1 {
			lastName := strings.Join(nameParts[1:], " ")
			if utf8.RuneCountInString(lastName) > model.UserLastNameMaxRunes {
				errors = append(errors, ValidationError{
					Type:    "length_exceeded",
					Field:   "user_last_name",
					Message: fmt.Sprintf("User last name exceeds maximum length of %d runes", model.UserLastNameMaxRunes),
				})
			}
		}
	}

	return errors
}

// validateMessageForMattermost validates message data against Mattermost limits
func validateMessageForMattermost(msg TelegramMessage, index int) []ValidationError {
	var errors []ValidationError

	// Check message text length
	text, err := msg.GetTextAsString()
	if err != nil {
		errors = append(errors, ValidationError{
			Type:    "invalid_text",
			Field:   "text",
			Message: fmt.Sprintf("Failed to extract message text: %v", err),
			Index:   &index,
		})
	}

	if utf8.RuneCountInString(text) > model.PostMessageMaxRunesV2 {
		errors = append(errors, ValidationError{
			Type:    "length_exceeded",
			Field:   "message_text",
			Message: fmt.Sprintf("Message text exceeds maximum length of %d runes", model.PostMessageMaxRunesV2),
			Index:   &index,
		})
	}

	// Validate file paths
	if msg.Photo != "" {
		if err := validateFilePath(msg.Photo); err != nil {
			errors = append(errors, ValidationError{
				Type:    "invalid_file_path",
				Field:   "photo",
				Message: fmt.Sprintf("Invalid photo path: %v", err),
				Index:   &index,
			})
		}
	}

	if msg.File != "" {
		if err := validateFilePath(msg.File); err != nil {
			errors = append(errors, ValidationError{
				Type:    "invalid_file_path",
				Field:   "file",
				Message: fmt.Sprintf("Invalid file path: %v", err),
				Index:   &index,
			})
		}
	}

	return errors
}

// validateFilePath checks if a file path is valid and safe
func validateFilePath(path string) error {
	if path == "" {
		return fmt.Errorf("file path cannot be empty")
	}

	// Check for path traversal attempts
	if strings.Contains(path, "..") {
		return fmt.Errorf("file path contains invalid '..' sequence")
	}

	// Ensure path is relative and doesn't start with /
	if filepath.IsAbs(path) {
		return fmt.Errorf("file path must be relative, not absolute")
	}

	// Check for invalid characters (basic check)
	if strings.ContainsAny(path, "<>:\"|?*") {
		return fmt.Errorf("file path contains invalid characters")
	}

	return nil
}

// SanitizeForMattermost applies sanitization to fix common issues
func SanitizeForMattermost(export *TelegramExport) {
	// Sanitize channel name
	channelName := strings.ToLower(strings.ReplaceAll(export.Name, " ", "-"))
	channelName = strings.Trim(channelName, "_-")

	// Ensure we have a valid channel name
	if channelName == "" {
		channelName = "telegram-chat"
	}

	if len(channelName) > model.ChannelNameMaxLength {
		channelName = channelName[:model.ChannelNameMaxLength]
	}

	// Sanitize display name
	if utf8.RuneCountInString(export.Name) > model.ChannelDisplayNameMaxRunes {
		runes := []rune(export.Name)
		export.Name = string(runes[:model.ChannelDisplayNameMaxRunes])
	}

	// Sanitize messages
	for i := range export.Messages {
		sanitizeMessage(&export.Messages[i])
	}
}

// sanitizeMessage applies sanitization to a single message
func sanitizeMessage(msg *TelegramMessage) {
	// Truncate very long text content
	text, err := msg.GetTextAsString()
	if err == nil && utf8.RuneCountInString(text) > model.PostMessageMaxRunesV2 {
		// For messages with array text, this is more complex
		// We'll handle this in the transformer if needed
	}

	// Clean file paths
	if msg.Photo != "" {
		msg.Photo = filepath.Clean(msg.Photo)
	}

	if msg.File != "" {
		msg.File = filepath.Clean(msg.File)
	}
}