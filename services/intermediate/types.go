package intermediate

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"unicode/utf8"

	"github.com/mattermost/mattermost/server/public/model"
	log "github.com/sirupsen/logrus"
)

// ExitFunc is the function called for fatal errors. Override in tests.
var ExitFunc func(code int) = os.Exit

var isValidChannelNameCharacters = regexp.MustCompile(`^[a-zA-Z0-9\-_]+$`).MatchString

// IsValidChannelNameCharacters checks if the given string contains only valid
// channel name characters (alphanumeric, hyphens, underscores).
func IsValidChannelNameCharacters(s string) bool {
	return isValidChannelNameCharacters(s)
}

// TruncateRunes truncates string s to at most i runes.
func TruncateRunes(s string, i int) string {
	runes := []rune(s)
	if len(runes) > i {
		return string(runes[:i])
	}
	return s
}

// IntermediateChannel holds a channel in the intermediate representation.
type IntermediateChannel struct {
	Id               string            `json:"id"`
	OriginalName     string            `json:"original_name"`
	Name             string            `json:"name"`
	DisplayName      string            `json:"display_name"`
	Members          []string          `json:"members"`
	MembersUsernames []string          `json:"members_usernames"`
	Purpose          string            `json:"purpose"`
	Header           string            `json:"header"`
	Topic            string            `json:"topic"`
	Type             model.ChannelType `json:"type"`
}

// Sanitise validates and truncates channel fields to Mattermost model limits.
// It uses "imported-channel-" as the fallback prefix for single-character names.
// Use SanitiseWithPrefix for a custom fallback prefix.
func (c *IntermediateChannel) Sanitise(logger log.FieldLogger) {
	c.SanitiseWithPrefix(logger, "imported-channel-")
}

// SanitiseWithPrefix validates and truncates channel fields to Mattermost model limits.
// The fallbackPrefix is prepended to single-character channel/display names.
func (c *IntermediateChannel) SanitiseWithPrefix(logger log.FieldLogger, fallbackPrefix string) {
	if c.Type == model.ChannelTypeDirect {
		return
	}

	c.Name = trimLeadingTrailingDashUnderscore(c.Name)
	if len(c.Name) > model.ChannelNameMaxLength {
		logger.Warnf("Channel %s handle exceeds the maximum length. It will be truncated when imported.", c.DisplayName)
		c.Name = c.Name[0:model.ChannelNameMaxLength]
	}
	if len(c.Name) == 1 {
		c.Name = fallbackPrefix + c.Name
	}
	if !isValidChannelNameCharacters(c.Name) {
		c.Name = toLowerString(c.Id)
	}

	c.DisplayName = trimLeadingTrailingDashUnderscore(c.DisplayName)
	if utf8.RuneCountInString(c.DisplayName) > model.ChannelDisplayNameMaxRunes {
		logger.Warnf("Channel %s display name exceeds the maximum length. It will be truncated when imported.", c.DisplayName)
		c.DisplayName = TruncateRunes(c.DisplayName, model.ChannelDisplayNameMaxRunes)
	}
	if len(c.DisplayName) == 1 {
		c.DisplayName = fallbackPrefix + c.DisplayName
	}

	if utf8.RuneCountInString(c.Purpose) > model.ChannelPurposeMaxRunes {
		logger.Warnf("Channel %s purpose exceeds the maximum length. It will be truncated when imported.", c.DisplayName)
		c.Purpose = TruncateRunes(c.Purpose, model.ChannelPurposeMaxRunes)
	}

	if utf8.RuneCountInString(c.Header) > model.ChannelHeaderMaxRunes {
		logger.Warnf("Channel %s header exceeds the maximum length. It will be truncated when imported.", c.DisplayName)
		c.Header = TruncateRunes(c.Header, model.ChannelHeaderMaxRunes)
	}
}

func trimLeadingTrailingDashUnderscore(s string) string {
	start := 0
	end := len(s)
	for start < end && (s[start] == '_' || s[start] == '-') {
		start++
	}
	for end > start && (s[end-1] == '_' || s[end-1] == '-') {
		end--
	}
	return s[start:end]
}

func toLowerString(s string) string {
	result := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		b := s[i]
		if b >= 'A' && b <= 'Z' {
			b += 32
		}
		result[i] = b
	}
	return string(result)
}

// IntermediateUser holds a user in the intermediate representation.
type IntermediateUser struct {
	Id          string   `json:"id"`
	Username    string   `json:"username"`
	FirstName   string   `json:"first_name"`
	LastName    string   `json:"last_name"`
	Position    string   `json:"position"`
	Email       string   `json:"email"`
	Password    string   `json:"password"`
	Memberships []string `json:"memberships"`
	DeleteAt    int64    `json:"delete_at"`
	IsBot       bool     `json:"is_bot"`
	DisplayName string   `json:"display_name"`
}

// Sanitise validates and truncates user fields to Mattermost model limits.
func (u *IntermediateUser) Sanitise(logger log.FieldLogger, defaultEmailDomain string, skipEmptyEmails bool) {
	logger.Debugf("TransformUsers: Sanitise: IntermediateUser receiver: %+v", u)

	if u.Email == "" {
		if skipEmptyEmails {
			logger.Warnf("User %s does not have an email address in the export. Using blank email address due to --skip-empty-emails flag.", u.Username)
			return
		}

		if defaultEmailDomain != "" {
			u.Email = u.Username + "@" + defaultEmailDomain
			logger.Warnf("User %s does not have an email address in the export. Used %s as a placeholder. The user should update their email address once logged in to the system.", u.Username, u.Email)
		} else {
			msg := fmt.Sprintf("User %s does not have an email address in the export. Please provide an email domain through the --default-email-domain flag, to assign this user's email address. Alternatively, use the --skip-empty-emails flag to set the user's email to an empty string.", u.Username)
			logger.Error(msg)
			fmt.Println(msg)
			ExitFunc(1)
		}
	}

	if utf8.RuneCountInString(u.FirstName) > model.UserFirstNameMaxRunes {
		logger.Warnf("User %s first name exceeds the maximum length. It will be truncated when imported.", u.Username)
		u.FirstName = TruncateRunes(u.FirstName, model.UserFirstNameMaxRunes)
	}

	if utf8.RuneCountInString(u.LastName) > model.UserLastNameMaxRunes {
		logger.Warnf("User %s last name exceeds the maximum length. It will be truncated when imported.", u.Username)
		u.LastName = TruncateRunes(u.LastName, model.UserLastNameMaxRunes)
	}

	if utf8.RuneCountInString(u.Position) > model.UserPositionMaxRunes {
		logger.Warnf("User %s position exceeds the maximum length. It will be truncated when imported.", u.Username)
		u.Position = TruncateRunes(u.Position, model.UserPositionMaxRunes)
	}
}

// IntermediateReaction holds a reaction in the intermediate representation.
type IntermediateReaction struct {
	User      string `json:"user"`
	EmojiName string `json:"emoji_name"`
	CreateAt  int64  `json:"create_at"`
}

// IntermediatePost holds a post in the intermediate representation.
type IntermediatePost struct {
	User           string                  `json:"user"`
	Channel        string                  `json:"channel"`
	Message        string                  `json:"message"`
	Props          model.StringInterface   `json:"props"`
	CreateAt       int64                   `json:"create_at"`
	Type           string                  `json:"type"`
	Attachments    []string                `json:"attachments"`
	Replies        []*IntermediatePost     `json:"replies"`
	Reactions      []*IntermediateReaction `json:"reactions"`
	IsDirect       bool                    `json:"is_direct"`
	ChannelMembers []string                `json:"channel_members"`
}

// Intermediate holds all intermediate data for the migration.
type Intermediate struct {
	PublicChannels  []*IntermediateChannel       `json:"public_channels"`
	PrivateChannels []*IntermediateChannel       `json:"private_channels"`
	GroupChannels   []*IntermediateChannel       `json:"group_channels"`
	DirectChannels  []*IntermediateChannel       `json:"direct_channels"`
	UsersById       map[string]*IntermediateUser `json:"users"`
	Posts           []*IntermediatePost          `json:"posts"`
}

// SplitTextIntoChunks splits text into multiple chunks, each within the rune limit.
// It tries to split on word boundaries (spaces) or line breaks when possible.
// Returns a slice of strings, each within the maxRunes limit.
func SplitTextIntoChunks(text string, maxRunes int) []string {
	runes := []rune(text)

	// If the text fits within the limit, return it as-is
	if len(runes) <= maxRunes {
		return []string{text}
	}

	chunks := []string{}
	currentPos := 0

	for currentPos < len(runes) {
		// Determine the end position for this chunk
		endPos := currentPos + maxRunes
		if endPos >= len(runes) {
			// Last chunk
			chunks = append(chunks, string(runes[currentPos:]))
			break
		}

		// Try to find a good break point (newline, space, etc.)
		breakPos := endPos

		// First, look for a newline within a reasonable range
		searchStart := currentPos
		if endPos-currentPos > 100 {
			searchStart = endPos - 100
		}

		foundNewline := false
		for i := endPos - 1; i >= searchStart; i-- {
			if runes[i] == '\n' {
				breakPos = i + 1 // Include the newline in the current chunk
				foundNewline = true
				break
			}
		}

		// If no newline found, look for a space
		if !foundNewline {
			for i := endPos - 1; i >= searchStart; i-- {
				if runes[i] == ' ' {
					breakPos = i + 1 // Include the space in the current chunk
					break
				}
			}
		}
		// If neither a newline nor a space was found within the search window,
		// breakPos remains at endPos and we hard-split at the rune limit.

		chunks = append(chunks, string(runes[currentPos:breakPos]))
		currentPos = breakPos
	}

	return chunks
}

// SplitPostIntoThread splits a post's message if it exceeds the maximum rune limit.
// The first chunk becomes/remains the main post, and additional chunks are added as replies.
// Reactions and attachments are kept only on the first chunk.
func SplitPostIntoThread(post *IntermediatePost) {
	if utf8.RuneCountInString(post.Message) <= model.PostMessageMaxRunesV2 {
		return
	}

	chunks := SplitTextIntoChunks(post.Message, model.PostMessageMaxRunesV2)

	// First chunk stays as the main message
	post.Message = chunks[0]

	// Create replies for the remaining chunks
	for i, chunk := range chunks[1:] {
		reply := &IntermediatePost{
			User:           post.User,
			Channel:        post.Channel,
			Message:        chunk,
			CreateAt:       post.CreateAt + int64(i+1),
			IsDirect:       post.IsDirect,
			ChannelMembers: post.ChannelMembers,
		}
		post.Replies = append(post.Replies, reply)
	}
}

// SplitOversizedReplies splits any replies that exceed the maximum rune limit
// into sibling replies, deduplicates timestamps, and sorts all replies by CreateAt.
func SplitOversizedReplies(post *IntermediatePost) {
	originalReplies := post.Replies
	post.Replies = []*IntermediatePost{}

	// Build a set of used timestamps from existing replies to avoid duplicates
	usedTimestamps := make(map[int64]bool)
	for _, reply := range originalReplies {
		usedTimestamps[reply.CreateAt] = true
	}

	for _, reply := range originalReplies {
		if utf8.RuneCountInString(reply.Message) <= model.PostMessageMaxRunesV2 {
			post.Replies = append(post.Replies, reply)
			continue
		}

		// Reply needs splitting - add all chunks as siblings
		chunks := SplitTextIntoChunks(reply.Message, model.PostMessageMaxRunesV2)

		// First chunk: update the original reply
		reply.Message = chunks[0]
		post.Replies = append(post.Replies, reply)

		// Remaining chunks: create new sibling replies
		for i, chunk := range chunks[1:] {
			timestamp := reply.CreateAt + int64(i+1)
			for usedTimestamps[timestamp] {
				timestamp++
			}
			usedTimestamps[timestamp] = true

			continuationReply := &IntermediatePost{
				User:           reply.User,
				Channel:        reply.Channel,
				Message:        chunk,
				CreateAt:       timestamp,
				IsDirect:       reply.IsDirect,
				ChannelMembers: reply.ChannelMembers,
			}
			post.Replies = append(post.Replies, continuationReply)
		}
	}

	// Sort replies by CreateAt to ensure proper ordering
	sort.Slice(post.Replies, func(i, j int) bool {
		return post.Replies[i].CreateAt < post.Replies[j].CreateAt
	})
}
