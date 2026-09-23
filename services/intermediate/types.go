// Package intermediate holds the source-agnostic representation of a workspace
// migration (channels, users, posts) together with the exporter that writes the
// Mattermost bulk-import JSONL. Source-specific services (Slack, RocketChat,
// ...) transform their exports into these types and embed the Exporter to
// produce the final import file.
package intermediate

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mattermost/mattermost/server/public/model"
)

// NowFunc is used by CreatedMillis for the fallback timestamp and by the
// transform report for its run timestamps. Tests can override this for
// deterministic output.
var NowFunc = time.Now

var isValidChannelNameCharacters = regexp.MustCompile(`^[a-zA-Z0-9\-_]+$`).MatchString

func truncateRunes(s string, i int) string {
	runes := []rune(s)
	if len(runes) > i {
		return string(runes[:i])
	}
	return s
}

// SplitTextIntoChunks splits text into multiple chunks, each within the rune
// limit. It tries to split on word boundaries (spaces) or line breaks when
// possible. Returns a slice of strings, each within the maxRunes limit.
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

		// If we're still at endPos, it means we couldn't find a good break point
		// In this case, just split at the limit
		if breakPos == endPos && breakPos < len(runes) {
			// Check if we're in the middle of a word - if so, just split there
			breakPos = endPos
		}

		chunks = append(chunks, string(runes[currentPos:breakPos]))
		currentPos = breakPos
	}

	return chunks
}

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
	DeleteAt         int64             `json:"delete_at"`
	Created          int64             `json:"created"` // Unix timestamp in seconds from the source
	MsgCount         int64             `json:"msg_count"`
	MsgCountRoot     int64             `json:"msg_count_root"`
	LastPostAt       int64             `json:"last_post_at"` // milliseconds, computed from posts

	// ReportKind is the section of the transform report this channel belongs
	// to. It is decided once, where the channel's source collection is counted,
	// and then carried — because a channel can change type on the way in (an
	// oversized group DM is imported as a private channel) and the report counts
	// it as what it was in the source. Re-deriving the kind from Type at each
	// note site would scatter one channel's notes across two sections and leave
	// the second section with notes it has no Seen count for.
	ReportKind EntityKind `json:"-"`
}

// CreatedMillis returns the channel creation time in milliseconds. Created holds
// a Unix timestamp in seconds from the source, or 0 when absent. Sources are
// responsible for normalizing their own placeholder values to 0 before populating
// this field; when Created is not positive, CreatedMillis falls back to the
// current time.
func (c *IntermediateChannel) CreatedMillis() int64 {
	if c.Created > 0 {
		return c.Created * 1000
	}
	return NowFunc().UnixMilli()
}

// ReportID identifies the channel in the source export, falling back to its
// original name for sources (such as Slack) that do not always carry an ID.
func (c *IntermediateChannel) ReportID() string {
	if c.Id != "" {
		return c.Id
	}
	return c.OriginalName
}

// ReportName is the channel's human-readable name for the transform report.
func (c *IntermediateChannel) ReportName() string {
	if c.DisplayName != "" {
		return c.DisplayName
	}
	// A direct or group channel usually carries no name of its own, and the
	// slug it gets instead is just its ID again — which the report already
	// shows. Its members are what identifies it to a person reading the report.
	if c.Type == model.ChannelTypeDirect || c.Type == model.ChannelTypeGroup {
		if len(c.MembersUsernames) > 0 {
			return strings.Join(c.MembersUsernames, ", ")
		}
	}
	if c.Name != "" {
		return c.Name
	}
	return c.OriginalName
}

// ReportEntity returns the report section this channel's notes belong in.
// Channels built without a ReportKind — as several tests do — fall back to
// their current Mattermost type.
func (c *IntermediateChannel) ReportEntity(report *Report) *EntityReport {
	if c.ReportKind != "" {
		return report.For(c.ReportKind)
	}
	return report.For(EntityKindForChannelType(c.Type))
}

// EntityKindForChannelType maps a Mattermost channel type to the report entity
// kind it is counted under. Prefer IntermediateChannel.ReportEntity, which
// honours the kind the channel was counted as in the source.
func EntityKindForChannelType(channelType model.ChannelType) EntityKind {
	switch channelType {
	case model.ChannelTypePrivate:
		return EntityPrivateChannel
	case model.ChannelTypeGroup:
		return EntityGroupChannel
	case model.ChannelTypeDirect:
		return EntityDirectChannel
	default:
		return EntityPublicChannel
	}
}

// SanitiseWithPrefix validates and truncates channel fields to Mattermost model
// limits, recording each change as a note against entity. The fallbackPrefix is
// prepended to single-character channel/display names; callers supply a
// source-specific value (e.g. "slack-channel-").
func (c *IntermediateChannel) SanitiseWithPrefix(entity *EntityReport, fallbackPrefix string) {
	if c.Type == model.ChannelTypeDirect {
		return
	}

	// Capture the identity up front: the fields the report names the channel by
	// are the same ones being truncated below.
	id, name := c.ReportID(), c.ReportName()

	c.Name = strings.Trim(c.Name, "_-")
	if len(c.Name) > model.ChannelNameMaxLength {
		entity.Note(id, name, ReasonChannelNameTruncated)
		c.Name = c.Name[0:model.ChannelNameMaxLength]
	}
	if len(c.Name) == 1 {
		c.Name = fallbackPrefix + c.Name
	}
	if !isValidChannelNameCharacters(c.Name) {
		c.Name = strings.ToLower(c.Id)
		entity.Note(id, name, ReasonChannelNameInvalidChars, c.Name)
	}

	c.DisplayName = strings.Trim(c.DisplayName, "_-")
	if utf8.RuneCountInString(c.DisplayName) > model.ChannelDisplayNameMaxRunes {
		entity.Note(id, name, ReasonChannelDisplayTruncated)
		c.DisplayName = truncateRunes(c.DisplayName, model.ChannelDisplayNameMaxRunes)
	}
	if len(c.DisplayName) == 1 {
		c.DisplayName = fallbackPrefix + c.DisplayName
	}

	if utf8.RuneCountInString(c.Purpose) > model.ChannelPurposeMaxRunes {
		entity.Note(id, name, ReasonChannelPurposeTruncated)
		c.Purpose = truncateRunes(c.Purpose, model.ChannelPurposeMaxRunes)
	}

	if utf8.RuneCountInString(c.Header) > model.ChannelHeaderMaxRunes {
		entity.Note(id, name, ReasonChannelHeaderTruncated)
		c.Header = truncateRunes(c.Header, model.ChannelHeaderMaxRunes)
	}
}

// IntermediateMembership represents a user's membership in a channel, carrying
// the channel name and pre-computed read-state fields for the Mattermost bulk
// import.
type IntermediateMembership struct {
	Name         string `json:"name"`
	LastViewedAt int64  `json:"last_viewed_at"` // milliseconds
	MsgCount     int64  `json:"msg_count"`
	MsgCountRoot int64  `json:"msg_count_root"`
}

type IntermediateUser struct {
	Id          string                   `json:"id"`
	Username    string                   `json:"username"`
	FirstName   string                   `json:"first_name"`
	LastName    string                   `json:"last_name"`
	Position    string                   `json:"position"`
	Email       string                   `json:"email"`
	Password    string                   `json:"password"`
	Memberships []IntermediateMembership `json:"memberships"`
	DeleteAt    int64                    `json:"delete_at"`
	IsBot       bool                     `json:"is_bot"`
	IsGuest     bool                     `json:"is_guest"`
	DisplayName string                   `json:"display_name"`
}

// Sanitise validates and truncates user fields to Mattermost model limits,
// recording each change as a note against entity. It returns an error when the
// user has no email address and neither --skip-empty-emails nor
// --default-email-domain was given, so the caller can abort the run through the
// normal error path — and still write the transform report.
func (u *IntermediateUser) Sanitise(entity *EntityReport, defaultEmailDomain string, skipEmptyEmails bool) error {
	if logger := entity.Logger(); logger != nil {
		// Log only non-sensitive identifiers; the full struct includes Email and
		// Password, which must not be written to logs even at debug level.
		logger.Debugf("TransformUsers: Sanitise: IntermediateUser Username=%s Id=%s", u.Username, u.Id)
	}

	if u.Email == "" {
		switch {
		case skipEmptyEmails:
			entity.Note(u.Id, u.Username, ReasonEmailBlank)
			return nil
		case defaultEmailDomain != "":
			u.Email = u.Username + "@" + defaultEmailDomain
			entity.Note(u.Id, u.Username, ReasonEmailPlaceholder, u.Email)
		default:
			return fmt.Errorf("user %s does not have an email address in the export. Please provide an email domain through the --default-email-domain flag, to assign this user's email address. Alternatively, use the --skip-empty-emails flag to set the user's email to an empty string", u.Username)
		}
	}

	if utf8.RuneCountInString(u.FirstName) > model.UserFirstNameMaxRunes {
		entity.Note(u.Id, u.Username, ReasonFirstNameTruncated)
		u.FirstName = truncateRunes(u.FirstName, model.UserFirstNameMaxRunes)
	}

	if utf8.RuneCountInString(u.LastName) > model.UserLastNameMaxRunes {
		entity.Note(u.Id, u.Username, ReasonLastNameTruncated)
		u.LastName = truncateRunes(u.LastName, model.UserLastNameMaxRunes)
	}

	if utf8.RuneCountInString(u.Position) > model.UserPositionMaxRunes {
		entity.Note(u.Id, u.Username, ReasonPositionTruncated)
		u.Position = truncateRunes(u.Position, model.UserPositionMaxRunes)
	}

	return nil
}

type IntermediateReaction struct {
	User      string `json:"user"`
	EmojiName string `json:"emoji_name"`
	CreateAt  int64  `json:"create_at"`
}

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

type Intermediate struct {
	PublicChannels  []*IntermediateChannel       `json:"public_channels"`
	PrivateChannels []*IntermediateChannel       `json:"private_channels"`
	GroupChannels   []*IntermediateChannel       `json:"group_channels"`
	DirectChannels  []*IntermediateChannel       `json:"direct_channels"`
	UsersById       map[string]*IntermediateUser `json:"users"`
	Posts           []*IntermediatePost          `json:"posts"`

	// GroupChannelAliases maps a duplicate group/direct channel's OriginalName
	// to the OriginalName of the canonical channel it was merged into. Mattermost
	// keys group channels by member-set hash, so any source that emits multiple
	// group channels with identical member sets would collide server-side. We
	// deduplicate to avoid emitting two `direct_channel` lines for the same hash
	// (which crashes the bulk importer when one channel's members aren't fully
	// written yet — see MM-68736), and route posts from every aliased source
	// channel name to the surviving IntermediateChannel. Slack MPIMs are the
	// motivating example, but the mechanism is source-agnostic.
	GroupChannelAliases map[string]string `json:"-"`
}

// SplitPostIntoThread splits a post's message if it exceeds the maximum rune
// limit. The first chunk becomes/remains the main post, and additional chunks
// are added as replies. Reactions and attachments are kept only on the first
// chunk. It returns how many posts the message ended up as, so the caller can
// record the split against the post in the transform report; 1 means the post
// was left alone.
func SplitPostIntoThread(post *IntermediatePost) int {
	if utf8.RuneCountInString(post.Message) <= model.PostMessageMaxRunesV2 {
		// No splitting needed
		return 1
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
			CreateAt:       post.CreateAt + int64(i+1), // Increment timestamp to maintain order
			IsDirect:       post.IsDirect,
			ChannelMembers: post.ChannelMembers,
			// No reactions, attachments, or props for continuation chunks
		}
		post.Replies = append(post.Replies, reply)
	}

	return len(chunks)
}

// SplitOversizedReplies splits any replies that exceed the maximum rune limit
// into sibling replies, deduplicates timestamps, and sorts all replies by
// CreateAt. It returns how many replies had to be split, so the caller can
// record the splits against the post in the transform report; 0 means every
// reply was left alone.
func SplitOversizedReplies(post *IntermediatePost) int {
	originalReplies := post.Replies
	post.Replies = []*IntermediatePost{}
	split := 0

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
		split++

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
				// No reactions, attachments, or props for continuation chunks
			}
			post.Replies = append(post.Replies, continuationReply)
		}
	}

	// Sort replies by CreateAt to ensure proper ordering. This is important
	// because split chunks may have timestamps that need to be interleaved with
	// other replies.
	sort.Slice(post.Replies, func(i, j int) bool {
		return post.Replies[i].CreateAt < post.Replies[j].CreateAt
	})

	return split
}
