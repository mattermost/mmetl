package telegram

import (
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/enescakir/emoji"
	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mmetl/services/slack"
	log "github.com/sirupsen/logrus"
)

var isValidChannelNameCharacters = regexp.MustCompile(`^[a-zA-Z0-9\-_]+$`).MatchString

// slugifyChannelName creates a URL-safe slug from channel name
func slugifyChannelName(name string) string {
	// Convert to lowercase
	name = strings.ToLower(name)

	// Replace spaces and common punctuation with hyphens
	name = regexp.MustCompile(`[\s\.,;:!?@#$%^&*()+={}[\]|\\/"'<>]+`).ReplaceAllString(name, "-")

	// Replace common accented characters
	replacements := map[string]string{
		"ñ": "n", "ç": "c", "ß": "ss",
		"à": "a", "á": "a", "â": "a", "ã": "a", "ä": "a", "å": "a", "æ": "ae",
		"è": "e", "é": "e", "ê": "e", "ë": "e",
		"ì": "i", "í": "i", "î": "i", "ï": "i",
		"ò": "o", "ó": "o", "ô": "o", "õ": "o", "ö": "o", "ø": "o", "œ": "oe",
		"ù": "u", "ú": "u", "û": "u", "ü": "u",
		"ý": "y", "ÿ": "y",
	}

	for old, new := range replacements {
		name = strings.ReplaceAll(name, old, new)
	}

	// Remove any remaining non-alphanumeric characters except hyphens and underscores
	name = regexp.MustCompile(`[^a-z0-9\-_]`).ReplaceAllString(name, "")

	// Replace multiple consecutive hyphens with single hyphen
	name = regexp.MustCompile(`-+`).ReplaceAllString(name, "-")

	// Trim hyphens and underscores from start and end
	name = strings.Trim(name, "-_")

	// Ensure minimum length and valid characters
	if len(name) < 2 || !isValidChannelNameCharacters(name) {
		return "telegram-chat"
	}

	return name
}

type Transformer struct {
	TeamName     string
	Intermediate *slack.Intermediate
	Logger       log.FieldLogger
	ExportDir    string
}

func NewTransformer(teamName string, logger log.FieldLogger, exportDir string) *Transformer {
	return &Transformer{
		TeamName:     teamName,
		Intermediate: &slack.Intermediate{},
		Logger:       logger,
		ExportDir:    exportDir,
	}
}

// Transform converts a Telegram export to the intermediate format
func (t *Transformer) Transform(telegramExport *TelegramExport, attachmentsDir string, skipAttachments bool) error {
	t.Logger.Info("Starting Telegram transformation")

	// Transform users first
	if err := t.TransformUsers(telegramExport); err != nil {
		return err
	}

	// Create a single channel for this Telegram chat
	if err := t.TransformChannel(telegramExport); err != nil {
		return err
	}

	// Populate user memberships (essential for channel membership!)
	t.PopulateUserMemberships()

	// Transform messages
	if err := t.TransformMessages(telegramExport, attachmentsDir, skipAttachments); err != nil {
		return err
	}

	t.Logger.Info("Telegram transformation completed successfully")
	return nil
}

// TransformUsers extracts all users from the Telegram export
func (t *Transformer) TransformUsers(telegramExport *TelegramExport) error {
	t.Logger.Info("Transforming users")

	users := GetUniqueUsers(telegramExport)
	resultUsers := make(map[string]*slack.IntermediateUser)

	for userID, userInfo := range users {
		// Split display name into first and last name
		nameParts := strings.Fields(userInfo.DisplayName)
		firstName := ""
		lastName := ""

		if len(nameParts) > 0 {
			firstName = nameParts[0]
			if len(nameParts) > 1 {
				lastName = strings.Join(nameParts[1:], " ")
			}
		}

		// Use actual Telegram username if available, otherwise use sanitized ID
		var username string
		if userInfo.Username != "" {
			username = userInfo.Username
			t.Logger.Debugf("Using Telegram username: %s for user %s", username, userID)
		} else {
			// Fallback to sanitized user ID
			username = strings.TrimPrefix(userID, "user")
			t.Logger.Debugf("No username found, using ID as username: %s for user %s", username, userID)
		}

		// Generate email
		email := fmt.Sprintf("%s@telegram.local", username)

		intermediateUser := &slack.IntermediateUser{
			Id:        userID,
			Username:  username,
			FirstName: firstName,
			LastName:  lastName,
			Email:     email,
			Password:  model.NewId(),
		}

		resultUsers[userID] = intermediateUser
		t.Logger.Debugf("Transformed user: %s (%s) -> @%s", userInfo.DisplayName, userID, username)
	}

	t.Intermediate.UsersById = resultUsers
	t.Logger.Infof("Transformed %d users", len(resultUsers))
	return nil
}

// TransformChannel creates a single private channel for the Telegram chat
func (t *Transformer) TransformChannel(telegramExport *TelegramExport) error {
	t.Logger.Info("Transforming channel")

	// Get all user IDs as members
	var memberIDs []string
	for userID := range t.Intermediate.UsersById {
		memberIDs = append(memberIDs, userID)
	}

	// Create a slugified channel name from the original name
	channelName := slugifyChannelName(telegramExport.Name)

	// Ensure it's within length limits
	if len(channelName) > model.ChannelNameMaxLength {
		channelName = channelName[:model.ChannelNameMaxLength]
	}

	// Final safety check - if slugification failed, use ID-based name
	if len(channelName) < 2 || !isValidChannelNameCharacters(channelName) {
		channelName = fmt.Sprintf("telegram-%d", telegramExport.ID)
	}

	channel := &slack.IntermediateChannel{
		Id:           fmt.Sprintf("telegram-%d", telegramExport.ID),
		OriginalName: telegramExport.Name,
		Name:         channelName,
		DisplayName:  telegramExport.Name,
		Members:      memberIDs,
		Purpose:      fmt.Sprintf("Imported from Telegram chat: %s", telegramExport.Name),
		Header:       "",
		Topic:        "",
		Type:         model.ChannelTypePrivate, // Telegram chats are typically private
	}

	t.Intermediate.PrivateChannels = []*slack.IntermediateChannel{channel}
	t.Logger.Infof("Created channel: %s (%s)", channel.DisplayName, channel.Name)
	return nil
}

// TransformMessages converts Telegram messages to intermediate posts
func (t *Transformer) TransformMessages(telegramExport *TelegramExport, attachmentsDir string, skipAttachments bool) error {
	t.Logger.Info("Transforming messages")

	if len(t.Intermediate.PrivateChannels) == 0 {
		return fmt.Errorf("no channel available for messages")
	}

	channel := t.Intermediate.PrivateChannels[0]
	var posts []*slack.IntermediatePost
	threads := make(map[int64]*slack.IntermediatePost) // messageID -> root post
	timestamps := make(map[int64]bool)                 // avoid timestamp collisions

	for _, msg := range telegramExport.Messages {
		post, err := t.transformSingleMessage(msg, channel, attachmentsDir, skipAttachments)
		if err != nil {
			t.Logger.WithError(err).Warnf("Failed to transform message ID %d", msg.ID)
			continue
		}

		if post == nil {
			continue // Skip messages that don't need to be imported
		}

		// Handle thread relationships
		if msg.ReplyToMessageID != nil {
			if rootPost, exists := threads[*msg.ReplyToMessageID]; exists {
				rootPost.Replies = append(rootPost.Replies, post)
				t.Logger.Debugf("Added reply to thread: message %d -> %d", msg.ID, *msg.ReplyToMessageID)
				continue
			} else {
				t.Logger.Warnf("Reply target message %d not found for message %d", *msg.ReplyToMessageID, msg.ID)
			}
		}

		// Avoid timestamp collisions
		for timestamps[post.CreateAt] {
			post.CreateAt++
		}
		timestamps[post.CreateAt] = true

		// Track this message for potential replies
		threads[msg.ID] = post
		posts = append(posts, post)
	}

	t.Intermediate.Posts = posts
	t.Logger.Infof("Transformed %d messages", len(posts))
	return nil
}

// transformSingleMessage converts a single Telegram message to an intermediate post
func (t *Transformer) transformSingleMessage(msg TelegramMessage, channel *slack.IntermediateChannel, attachmentsDir string, skipAttachments bool) (*slack.IntermediatePost, error) {
	// Get author information
	_, authorID := msg.GetAuthorInfo()
	if authorID == "" {
		return nil, fmt.Errorf("message %d missing author information", msg.ID)
	}

	author, exists := t.Intermediate.UsersById[authorID]
	if !exists {
		return nil, fmt.Errorf("user %s not found for message %d", authorID, msg.ID)
	}

	// Parse timestamp
	timestamp, err := msg.GetTimestamp()
	if err != nil {
		return nil, fmt.Errorf("invalid timestamp for message %d: %v", msg.ID, err)
	}

	// Convert to milliseconds
	createAt := timestamp.UnixNano() / int64(time.Millisecond)

	post := &slack.IntermediatePost{
		User:     author.Username,
		Channel:  channel.Name,
		CreateAt: createAt,
	}

	// Handle different message types
	switch msg.Type {
	case "message":
		return t.transformRegularMessage(msg, post, attachmentsDir, skipAttachments)
	case "service":
		return t.transformServiceMessage(msg, post)
	default:
		t.Logger.Warnf("Unknown message type: %s for message %d", msg.Type, msg.ID)
		return nil, nil
	}
}

// transformRegularMessage handles regular text/media messages
func (t *Transformer) transformRegularMessage(msg TelegramMessage, post *slack.IntermediatePost, attachmentsDir string, skipAttachments bool) (*slack.IntermediatePost, error) {
	// Get text content
	textContent, err := msg.GetTextAsString()
	if err != nil {
		return nil, fmt.Errorf("failed to extract text from message %d: %v", msg.ID, err)
	}

	// Convert text entities to Mattermost format
	post.Message = t.convertTextEntities(msg.TextEntities)
	if post.Message == "" {
		post.Message = textContent // Fallback to raw text
	}

	// Handle media attachments
	if !skipAttachments && msg.HasMedia() {
		if err := t.addMediaToPost(msg, post, attachmentsDir); err != nil {
			t.Logger.WithError(err).Warnf("Failed to add media to message %d", msg.ID)
		}
	}

	// Handle reactions
	if msg.HasReactions() {
		post.Reactions = t.transformReactions(msg.Reactions, post.CreateAt)
	}

	// Add forwarded message info
	if msg.ForwardedFrom != "" {
		post.Message = fmt.Sprintf("*Forwarded from %s:*\n%s", msg.ForwardedFrom, post.Message)
	}

	return post, nil
}

// transformServiceMessage handles service messages (joins, leaves, etc.)
func (t *Transformer) transformServiceMessage(msg TelegramMessage, post *slack.IntermediatePost) (*slack.IntermediatePost, error) {
	switch msg.Action {
	case "join_group_by_link":
		post.Message = fmt.Sprintf("%s joined the group", msg.Actor)
		post.Type = model.PostTypeJoinChannel
	case "invite_members":
		if len(msg.Members) > 0 {
			post.Message = fmt.Sprintf("%s invited %s to the group", msg.Actor, strings.Join(msg.Members, ", "))
		} else {
			post.Message = fmt.Sprintf("%s invited members to the group", msg.Actor)
		}
		post.Type = model.PostTypeAddToChannel
	default:
		// Generic service message
		post.Message = fmt.Sprintf("Service: %s", msg.Action)
		post.Type = model.PostTypeSystemGeneric
	}

	return post, nil
}

// convertTextEntities converts Telegram text entities to Mattermost markdown
func (t *Transformer) convertTextEntities(entities []TelegramTextEntity) string {
	if len(entities) == 0 {
		return ""
	}

	var result strings.Builder

	for _, entity := range entities {
		switch entity.Type {
		case "plain":
			result.WriteString(entity.Text)
		case "bold":
			result.WriteString("**" + entity.Text + "**")
		case "italic":
			result.WriteString("*" + entity.Text + "*")
		case "strikethrough":
			result.WriteString("~~" + entity.Text + "~~")
		case "code":
			result.WriteString("`" + entity.Text + "`")
		case "pre":
			result.WriteString("```\n" + entity.Text + "\n```")
		case "text_link":
			if entity.Href != "" {
				result.WriteString(fmt.Sprintf("[%s](%s)", entity.Text, entity.Href))
			} else {
				result.WriteString(entity.Text)
			}
		case "mention":
			// For simple @username mentions, keep as-is
			result.WriteString(entity.Text)
		case "mention_name":
			// For mention_name, try to convert to proper username using user_id
			if entity.UserID != nil {
				userID := fmt.Sprintf("user%d", *entity.UserID)
				if user, exists := t.Intermediate.UsersById[userID]; exists {
					// Use the actual Mattermost username for this user
					result.WriteString("@" + user.Username)
					t.Logger.Debugf("Converted mention_name '%s' (user_id: %d) to @%s", entity.Text, *entity.UserID, user.Username)
				} else {
					// User not found, use display name as fallback
					t.Logger.Warnf("User ID %d not found for mention_name '%s', using display name", *entity.UserID, entity.Text)
					result.WriteString("@" + entity.Text)
				}
			} else {
				// No user_id provided, use display name
				result.WriteString("@" + entity.Text)
			}
		case "hashtag":
			result.WriteString("#" + strings.TrimPrefix(entity.Text, "#"))
		case "bot_command":
			result.WriteString("`" + entity.Text + "`")
		case "url", "email", "phone":
			result.WriteString(entity.Text)
		case "custom_emoji":
			// For custom emojis, we'll use the text representation
			// The actual sticker file handling is done separately
			result.WriteString(entity.Text)
		default:
			result.WriteString(entity.Text)
		}
	}

	return result.String()
}

// addMediaToPost adds media attachments to the post
func (t *Transformer) addMediaToPost(msg TelegramMessage, post *slack.IntermediatePost, attachmentsDir string) error {
	const attachmentsInternal = "bulk-export-attachments"

	if msg.Photo != "" {
		// Handle photo attachment
		destPath := path.Join(attachmentsInternal, path.Base(msg.Photo))
		post.Attachments = append(post.Attachments, destPath)

		// Copy file to attachments directory
		if err := t.copyMediaFile(msg.Photo, destPath, attachmentsDir); err != nil {
			return err
		}
	}

	if msg.File != "" {
		// Handle file attachment
		destPath := path.Join(attachmentsInternal, path.Base(msg.File))
		post.Attachments = append(post.Attachments, destPath)

		// Copy file to attachments directory
		if err := t.copyMediaFile(msg.File, destPath, attachmentsDir); err != nil {
			return err
		}
	}

	return nil
}

// copyMediaFile copies a media file from the export to the attachments directory
func (t *Transformer) copyMediaFile(srcPath, destPath, attachmentsDir string) error {
	srcFullPath := filepath.Join(t.ExportDir, srcPath)
	destFullPath := filepath.Join(attachmentsDir, destPath)

	// Check if source file exists
	if _, err := os.Stat(srcFullPath); os.IsNotExist(err) {
		t.Logger.Warnf("Source file not found: %s", srcFullPath)
		return nil // Don't fail the whole transformation for missing media
	}

	// Create destination directory if needed
	destDir := filepath.Dir(destFullPath)
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return fmt.Errorf("failed to create destination directory %s: %v", destDir, err)
	}

	// Copy the file
	srcFile, err := os.Open(srcFullPath)
	if err != nil {
		return fmt.Errorf("failed to open source file %s: %v", srcFullPath, err)
	}
	defer srcFile.Close()

	destFile, err := os.Create(destFullPath)
	if err != nil {
		return fmt.Errorf("failed to create destination file %s: %v", destFullPath, err)
	}
	defer destFile.Close()

	_, err = io.Copy(destFile, srcFile)
	if err != nil {
		return fmt.Errorf("failed to copy file from %s to %s: %v", srcFullPath, destFullPath, err)
	}

	t.Logger.Debugf("Copied media file: %s -> %s", srcPath, destPath)
	return nil
}

// buildEmojiToNameMap creates a reverse mapping from Unicode emoji to names
func buildEmojiToNameMap() map[string]string {
	emojiToName := make(map[string]string)

	// Get all emoji mappings from the library
	emojiMap := emoji.Map()

	// Reverse the mapping: emoji unicode -> name (without colons)
	for alias, emojiChar := range emojiMap {
		// Remove colons from alias to get the name
		name := strings.Trim(alias, ":")
		emojiToName[emojiChar] = name
	}

	return emojiToName
}

// Initialize the emoji mapping once
var emojiToNameMap = buildEmojiToNameMap()

// sanitizeEmojiName sanitizes custom emoji names for Mattermost compatibility
func sanitizeEmojiName(name string) string {
	// Replace spaces and special characters with underscores
	name = regexp.MustCompile(`[\s\(\)\[\]{}.,;:!?@#$%^&*+=|\\/"'<>-]+`).ReplaceAllString(name, "_")

	// Convert to lowercase
	name = strings.ToLower(name)

	// Remove consecutive underscores
	name = regexp.MustCompile(`_+`).ReplaceAllString(name, "_")

	// Trim underscores from start and end
	name = strings.Trim(name, "_")

	// Ensure it's not empty
	if name == "" {
		name = "custom_emoji"
	}

	return name
}

// convertEmojiToName converts Unicode emoji to Mattermost-compatible emoji name
func convertEmojiToName(emojiChar string) string {
	// First try the comprehensive library mapping
	if name, exists := emojiToNameMap[emojiChar]; exists {
		return name
	}

	// Fallback for common variations not in the library
	fallbackMappings := map[string]string{
		"❤": "heart",           // Heart without variant selector
		"❤️": "heart",          // Heart with variant selector
		"+1": "thumbsup",       // Alternative text representation
		"-1": "thumbsdown",     // Alternative text representation
	}

	if name, exists := fallbackMappings[emojiChar]; exists {
		return name
	}

	// If we can't convert it, return empty string to skip
	return ""
}

// transformReactions converts Telegram reactions to intermediate format
func (t *Transformer) transformReactions(reactions []TelegramReaction, postCreateAt int64) []*slack.IntermediateReaction {
	var result []*slack.IntermediateReaction

	for _, reaction := range reactions {
		var emojiName string

		if reaction.Type == "custom_emoji" {
			// Discard custom emojis with a warning
			t.Logger.Warnf("Discarding custom emoji reaction: %s (document: %s) - custom emojis not supported", reaction.Emoji, reaction.DocumentID)
			continue
		} else {
			// Convert Unicode emoji to Mattermost emoji name
			emojiName = convertEmojiToName(reaction.Emoji)
			if emojiName == "" {
				// Skip unknown Unicode emojis
				t.Logger.Warnf("Discarding unknown official emoji reaction: %s - not in supported emoji list", reaction.Emoji)
				continue
			}
		}

		for _, author := range reaction.Recent {
			user, exists := t.Intermediate.UsersById[author.FromID]
			if !exists {
				t.Logger.Warnf("User %s not found for reaction", author.FromID)
				continue
			}

			result = append(result, &slack.IntermediateReaction{
				User:      user.Username,
				EmojiName: emojiName,
				CreateAt:  postCreateAt + 1, // Reactions must be after the post
			})
		}
	}

	return result
}

// Export delegates to the existing Slack export functionality
func (t *Transformer) Export(outputPath string) error {
	// Create a temporary Slack transformer to use its export functionality
	slackTransformer := slack.NewTransformer(t.TeamName, t.Logger)
	slackTransformer.Intermediate = t.Intermediate

	return slackTransformer.Export(outputPath)
}

// PopulateUserMemberships populates user memberships from channel member lists
func (t *Transformer) PopulateUserMemberships() {
	t.Logger.Info("Populating user memberships")

	for userId, user := range t.Intermediate.UsersById {
		memberships := []string{}
		for _, channel := range t.Intermediate.PublicChannels {
			for _, memberId := range channel.Members {
				if userId == memberId {
					memberships = append(memberships, channel.Name)
					break
				}
			}
		}
		for _, channel := range t.Intermediate.PrivateChannels {
			for _, memberId := range channel.Members {
				if userId == memberId {
					memberships = append(memberships, channel.Name)
					break
				}
			}
		}
		user.Memberships = memberships
		t.Logger.Debugf("User %s added to channels: %v", user.Username, memberships)
	}
}