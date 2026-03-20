package rocketchat

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/mattermost/mattermost/server/public/model"
	log "github.com/sirupsen/logrus"
	"golang.org/x/text/unicode/norm"

	"github.com/mattermost/mmetl/services/intermediate"
)

// Transformer holds all state for a Rocket.Chat → Mattermost transformation.
type Transformer struct {
	intermediate.Exporter // provides TeamName, Intermediate, Logger, and all export methods

	// skippedRoomIDs records room IDs that were skipped (encrypted/discussion) so
	// that messages in those rooms are also skipped.
	skippedRoomIDs map[string]bool

	// roomIDToChannelName maps RC room _id → Mattermost channel name (or "" for direct rooms).
	roomIDToChannelName map[string]string

	// roomIDToType maps RC room _id → room type string ("c", "p", "d").
	roomIDToType map[string]string

	// directRoomIDToChannel maps RC direct/group room _id → IntermediateChannel.
	// Used to look up MembersUsernames in O(1) per message instead of a linear scan.
	directRoomIDToChannel map[string]*intermediate.IntermediateChannel

	// knownChannels maps lowercase channel name → canonical name, precomputed
	// after transformChannels so convertChannelMentions avoids rebuilding it
	// per message.
	knownChannels map[string]string
}

// NewTransformer creates a new Transformer for the given team.
func NewTransformer(teamName string, logger log.FieldLogger) *Transformer {
	return &Transformer{
		Exporter: intermediate.Exporter{
			TeamName:     teamName,
			Intermediate: &intermediate.Intermediate{},
			Logger:       logger,
		},
		skippedRoomIDs:        make(map[string]bool),
		roomIDToChannelName:   make(map[string]string),
		roomIDToType:          make(map[string]string),
		directRoomIDToChannel: make(map[string]*intermediate.IntermediateChannel),
		knownChannels:         make(map[string]string),
	}
}

// Transform runs all transformation phases against a parsed dump in order:
// users, channels, subscriptions, then messages.
// When skipAttachments is true, no attachment paths are written into posts.
func (t *Transformer) Transform(parsed *ParsedData, skipAttachments bool, skipEmptyEmails bool, defaultEmailDomain string) {
	t.transformUsers(parsed.Users, skipEmptyEmails, defaultEmailDomain)
	t.transformChannels(parsed.Rooms)
	t.transformSubscriptions(parsed.Subscriptions)
	var uploadsForTransform map[string]*RocketChatUpload
	if !skipAttachments {
		uploadsForTransform = parsed.UploadsByID
	}
	t.transformMessages(parsed.Messages, uploadsForTransform)
}

// transformUsers converts RocketChatUser records into IntermediateUser records
// and stores them in Intermediate.UsersById keyed by RC _id.
func (t *Transformer) transformUsers(users []RocketChatUser, skipEmptyEmails bool, defaultEmailDomain string) {
	t.Logger.Info("Transforming users")

	result := make(map[string]*intermediate.IntermediateUser, len(users))
	for _, u := range users {
		var deleteAt int64
		if !u.Active {
			deleteAt = model.GetMillis()
		}

		firstName, lastName := splitName(u.Name)

		email := ""
		if len(u.Emails) > 0 {
			email = u.Emails[0].Address
		}

		newUser := &intermediate.IntermediateUser{
			Id:          u.ID,
			IsBot:       u.Type == "bot",
			Username:    strings.ToLower(u.Username),
			FirstName:   firstName,
			LastName:    lastName,
			DisplayName: u.Name,
			Email:       email,
			DeleteAt:    deleteAt,
		}

		if !newUser.IsBot {
			newUser.Sanitise(t.Logger, defaultEmailDomain, skipEmptyEmails)
		}
		result[newUser.Id] = newUser
		t.Logger.Debugf("transformed user: %s isBot: %t", newUser.Username, newUser.IsBot)
	}

	t.Intermediate.UsersById = result
	t.Logger.Infof("Transformed %d users", len(result))
}

// splitName splits a full name on the first space.
func splitName(name string) (firstName, lastName string) {
	idx := strings.Index(name, " ")
	if idx < 0 {
		return name, ""
	}
	return name[:idx], name[idx+1:]
}

// createPlaceholderUser creates a deleted placeholder user for a missing RC user.
func (t *Transformer) createPlaceholderUser(rcUserID string) *intermediate.IntermediateUser {
	username := strings.ToLower(rcUserID)
	u := &intermediate.IntermediateUser{
		Id:        rcUserID,
		Username:  username,
		FirstName: "Deleted",
		LastName:  "User",
		Email:     fmt.Sprintf("%s@local", username),
		Password:  model.NewId(),
		DeleteAt:  model.GetMillis(),
	}
	t.Intermediate.UsersById[rcUserID] = u
	t.Logger.Warnf("Created placeholder user for missing RC user ID: %s", rcUserID)
	return u
}

// transformChannels converts RocketChatRoom records into IntermediateChannel records.
func (t *Transformer) transformChannels(rooms []RocketChatRoom) {
	t.Logger.Info("Transforming channels")

	for i := range rooms {
		room := &rooms[i]

		if room.Encrypted {
			t.Logger.Warnf("Skipping encrypted room: %s", room.Name)
			t.skippedRoomIDs[room.ID] = true
			continue
		}

		t.roomIDToType[room.ID] = room.Type

		switch room.Type {
		case "c":
			ch := t.roomToIntermediateChannel(room, model.ChannelTypeOpen)
			t.Intermediate.PublicChannels = append(t.Intermediate.PublicChannels, ch)
			t.roomIDToChannelName[room.ID] = ch.Name

		case "p":
			ch := t.roomToIntermediateChannel(room, model.ChannelTypePrivate)
			t.Intermediate.PrivateChannels = append(t.Intermediate.PrivateChannels, ch)
			t.roomIDToChannelName[room.ID] = ch.Name

		case "d":
			uids := room.UIDs
			usernames := make([]string, len(room.Usernames))
			for i, username := range room.Usernames {
				usernames[i] = strings.ToLower(username)
			}

			// RC allows self-DMs (1 participant). Mattermost models these as a
			// direct channel where the same user appears twice.
			if len(uids) == 1 {
				uids = []string{uids[0], uids[0]}
				usernames = []string{usernames[0], usernames[0]}
			}

			if len(uids) >= 3 && len(uids) <= model.ChannelGroupMaxUsers {
				ch := t.roomToDirectChannel(room, model.ChannelTypeGroup, uids, usernames)
				t.Intermediate.GroupChannels = append(t.Intermediate.GroupChannels, ch)
				// Direct channel names are resolved via member usernames at post time.
				t.roomIDToChannelName[room.ID] = ""
				t.directRoomIDToChannel[room.ID] = ch
			} else if len(uids) > model.ChannelGroupMaxUsers {
				// Mattermost group messages support at most model.ChannelGroupMaxUsers
				// members; convert oversized group DMs to private channels.
				t.Logger.Warnf("Room %s has %d members (>%d), converting group DM to private channel",
					room.ID, len(uids), model.ChannelGroupMaxUsers)
				ch := t.roomToIntermediateChannel(room, model.ChannelTypePrivate)
				t.Intermediate.PrivateChannels = append(t.Intermediate.PrivateChannels, ch)
				t.roomIDToChannelName[room.ID] = ch.Name
				t.roomIDToType[room.ID] = "p"
			} else {
				ch := t.roomToDirectChannel(room, model.ChannelTypeDirect, uids, usernames)
				t.Intermediate.DirectChannels = append(t.Intermediate.DirectChannels, ch)
				// Direct channel names are resolved via member usernames at post time.
				t.roomIDToChannelName[room.ID] = ""
				t.directRoomIDToChannel[room.ID] = ch
			}

		default:
			t.Logger.Warnf("Skipping room with unknown type %q: %s", room.Type, room.Name)
			t.skippedRoomIDs[room.ID] = true
		}
	}

	// Precompute the lowercase-name → canonical-name lookup used by
	// convertChannelMentions so that it isn't rebuilt for every message.
	t.knownChannels = make(map[string]string, len(t.roomIDToChannelName))
	for _, name := range t.roomIDToChannelName {
		if name != "" {
			t.knownChannels[strings.ToLower(name)] = name
		}
	}

	t.Logger.Infof("Transformed %d public, %d private, %d group, %d direct channels",
		len(t.Intermediate.PublicChannels),
		len(t.Intermediate.PrivateChannels),
		len(t.Intermediate.GroupChannels),
		len(t.Intermediate.DirectChannels),
	)
}

func (t *Transformer) roomToIntermediateChannel(room *RocketChatRoom, chType model.ChannelType) *intermediate.IntermediateChannel {
	// Handle case of group rooms which are converted to private channels due to exceeding the group DM member limit.
	if room.Name == "" {
		t.Logger.Warnf("Room %s has empty name, using ID as fallback", room.ID)
		room.Name = "channel-" + room.ID
	}

	displayName := room.FName
	if displayName == "" {
		displayName = room.Name
	}

	description := ""
	if room.Description != nil {
		description = *room.Description
	}

	ch := &intermediate.IntermediateChannel{
		Id:           room.ID,
		OriginalName: roomOriginalName(room),
		Name:         rcConvertChannelName(room.Name, room.ID),
		DisplayName:  displayName,
		Purpose:      description,
		Header:       room.Topic,
		Type:         chType,
	}
	ch.SanitiseWithPrefix(t.Logger, "rocketchat-channel-")
	return ch
}

func (t *Transformer) roomToDirectChannel(room *RocketChatRoom, chType model.ChannelType, uids, usernames []string) *intermediate.IntermediateChannel {
	ch := &intermediate.IntermediateChannel{
		Id:               room.ID,
		OriginalName:     roomOriginalName(room),
		Members:          uids,
		MembersUsernames: usernames,
		Type:             chType,
	}
	return ch
}

// roomOriginalName returns the room's Name if non-empty, falling back to its
// ID. This mirrors the Slack getOriginalName pattern and ensures OriginalName
// is always a meaningful, non-empty identifier.
func roomOriginalName(room *RocketChatRoom) string {
	if room.Name == "" {
		return room.ID
	}
	return room.Name
}

// rcConvertChannelName converts a Rocket.Chat room name to a
// Mattermost-compatible channel name slug. Spaces and unsupported characters
// are replaced with hyphens, the result is lowercased, and the room ID is used
// as a fallback when the slug would otherwise be empty. SanitiseWithPrefix
// further trims and validates the result.
func rcConvertChannelName(name, id string) string {
	var sb strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			sb.WriteRune(r)
		default:
			// Replace spaces and any other invalid character with a hyphen.
			sb.WriteRune('-')
		}
	}
	slug := strings.Trim(sb.String(), "-_")
	if slug == "" {
		return strings.ToLower(id)
	}
	return slug
}

// transformSubscriptions uses subscription records to populate channel member lists
// for public and private channels, and user membership lists.
func (t *Transformer) transformSubscriptions(subscriptions []RocketChatSubscription) {
	t.Logger.Info("Transforming subscriptions")

	// Build a room-id → channel index for public + private channels.
	channelByRoomID := make(map[string]*intermediate.IntermediateChannel, len(t.Intermediate.PublicChannels)+len(t.Intermediate.PrivateChannels))
	for _, ch := range t.Intermediate.PublicChannels {
		channelByRoomID[ch.Id] = ch
	}
	for _, ch := range t.Intermediate.PrivateChannels {
		channelByRoomID[ch.Id] = ch
	}

	for i := range subscriptions {
		sub := &subscriptions[i]

		ch, ok := channelByRoomID[sub.RoomID]
		if !ok {
			// Subscription to a DM/group/skipped room — not relevant here.
			continue
		}

		user, ok := t.Intermediate.UsersById[sub.User.ID]
		if !ok {
			t.Logger.Warnf("Subscription references unknown user %s (room %s), skipping", sub.User.ID, sub.RoomID)
			continue
		}

		// Bots don't need channel or team memberships.
		if user.IsBot {
			continue
		}

		// Add to channel Members (by user ID) if not already present.
		if !slices.Contains(ch.Members, sub.User.ID) {
			ch.Members = append(ch.Members, sub.User.ID)
		}

		if !slices.Contains(user.Memberships, ch.Name) {
			user.Memberships = append(user.Memberships, ch.Name)
		}
	}
}

// systemMessageTypeMap maps RC system message types to Mattermost post types.
// Types not listed here are skipped.
var systemMessageTypeMap = map[string]string{
	"uj": "system_join_channel",
	"ul": "system_leave_channel",
	"au": "system_add_to_channel",
	"ru": "system_remove_from_channel",
}

// skippedSystemMessageTypes is the set of RC system message types that produce no post.
var skippedSystemMessageTypes = map[string]bool{
	"r":                         true,
	"message_pinned":            true,
	"discussion-created":        true,
	"user-muted":                true,
	"subscription-role-added":   true,
	"room_changed_privacy":      true,
	"room_changed_topic":        true,
	"room_changed_description":  true,
	"room_changed_announcement": true,
	"room_changed_avatar":       true,
	"user-unmuted":              true,
	"subscription-role-removed": true,
}

// transformMessages converts RC messages into IntermediatePost records.
func (t *Transformer) transformMessages(messages []RocketChatMessage, uploadsById map[string]*RocketChatUpload) {
	t.Logger.Info("Transforming messages")

	// Sort messages by timestamp for deterministic output and to ensure root
	// posts are always processed before their replies.
	sort.Slice(messages, func(i, j int) bool {
		return messages[i].Timestamp.Before(messages[j].Timestamp)
	})

	// First pass: build thread map — tmid → list of reply messages.
	threadReplies := make(map[string][]*RocketChatMessage)
	for i := range messages {
		m := &messages[i]
		if m.ThreadID != "" {
			threadReplies[m.ThreadID] = append(threadReplies[m.ThreadID], m)
		}
	}

	// Second pass: process root messages (those without a tmid).
	// Track timestamps globally to avoid duplicates that cause import failures.
	// This is more conservative than per-channel (Mattermost only requires
	// unique timestamps within a channel), but keeps the logic simple.
	timestamps := make(map[int64]bool)
	var posts []*intermediate.IntermediatePost
	for i := range messages {
		m := &messages[i]

		// Skip thread replies — they will be attached to their root post.
		if m.ThreadID != "" {
			continue
		}

		// Skip messages in skipped rooms.
		if t.skippedRoomIDs[m.RoomID] {
			continue
		}

		post := t.convertMessage(m, uploadsById)
		if post == nil {
			continue
		}

		// Deduplicate timestamps: increment until unique.
		for timestamps[post.CreateAt] {
			post.CreateAt++
		}
		timestamps[post.CreateAt] = true

		// Attach thread replies with timestamp deduplication.
		for _, reply := range threadReplies[m.ID] {
			replyPost := t.convertMessage(reply, uploadsById)
			if replyPost != nil {
				for timestamps[replyPost.CreateAt] {
					replyPost.CreateAt++
				}
				timestamps[replyPost.CreateAt] = true
				post.Replies = append(post.Replies, replyPost)
			}
		}

		// Split oversized root messages into continuation thread replies.
		intermediate.SplitPostIntoThread(post)

		// Split any oversized replies, deduplicate timestamps, and sort replies.
		intermediate.SplitOversizedReplies(post)

		posts = append(posts, post)
	}

	t.Intermediate.Posts = posts
	t.Logger.Infof("Transformed %d posts", len(posts))
}

// convertMessage converts a single RocketChatMessage to an IntermediatePost.
// Returns nil if the message should be skipped.
func (t *Transformer) convertMessage(m *RocketChatMessage, uploadsById map[string]*RocketChatUpload) *intermediate.IntermediatePost {
	// Handle system messages.
	if m.Type != "" {
		if skippedSystemMessageTypes[m.Type] {
			return nil
		}
		mmType, ok := systemMessageTypeMap[m.Type]
		if !ok {
			t.Logger.Debugf("Skipping unsupported system message type %q in room %s", m.Type, m.RoomID)
			return nil
		}

		// System messages are modelled as regular posts with a type set.
		post := t.buildBasePost(m)
		if post == nil {
			return nil
		}
		post.Type = mmType
		return post
	}

	post := t.buildBasePost(m)
	if post == nil {
		return nil
	}

	// Convert #channel-name references to Mattermost ~channel-name format.
	// Uses the structured channels list on the message when available, then
	// falls back to scanning the text for any remaining #word tokens.
	post.Message = t.convertChannelMentions(post.Message, m.Channels)

	// NOTE: oversized messages are split into thread continuations by
	// transformMessages after all replies are assembled, rather than
	// truncated here, to avoid data loss.

	// Reactions.
	post.Reactions = t.convertReactions(m)

	// File attachments.
	if uploadsById != nil {
		for _, fileRef := range m.Files {
			upload, ok := uploadsById[fileRef.ID]
			if !ok || !upload.Complete {
				continue
			}
			// Apply NFC normalization before sanitizing, matching the logic in
			// ExtractAttachments, so the path embedded in the JSONL matches the
			// filename that will be created on disk.
			sanitizedName := sanitizeFilename(norm.NFC.String(upload.Name))
			attachPath := fmt.Sprintf("bulk-export-attachments/%s_%s", sanitizeFilename(upload.ID), sanitizedName)
			post.Attachments = append(post.Attachments, attachPath)
		}
	}

	return post
}

// buildBasePost constructs a base IntermediatePost from a message, resolving
// user and channel references. Returns nil if the post should be skipped.
func (t *Transformer) buildBasePost(m *RocketChatMessage) *intermediate.IntermediatePost {
	// Resolve user.
	// Always check UsersById and create a placeholder when the user ID is absent.
	// This ensures every post's author has a corresponding user line in the JSONL,
	// even when the message carries a username that is not in the user collection.
	username := strings.ToLower(m.User.Username)
	if m.User.ID != "" {
		if _, ok := t.Intermediate.UsersById[m.User.ID]; !ok {
			placeholder := t.createPlaceholderUser(m.User.ID)
			if username != "" {
				// Use the username from the message so the post's user field
				// matches the placeholder user line we'll export.
				placeholder.Username = username
				placeholder.Email = fmt.Sprintf("%s@local", username)
			} else {
				username = placeholder.Username
			}
		}
	}
	if username == "" {
		if user := t.Intermediate.UsersById[m.User.ID]; user != nil {
			username = user.Username
		}
	}

	// Resolve channel.
	roomType := t.roomIDToType[m.RoomID]
	isDirect := roomType == "d"

	var channelName string
	var channelMembers []string

	if isDirect {
		// For direct posts, channel name is empty; members are resolved from the
		// precomputed directRoomIDToChannel map (O(1) instead of linear scan).
		channelName = ""
		if ch, ok := t.directRoomIDToChannel[m.RoomID]; ok {
			channelMembers = ch.MembersUsernames
		} else {
			// Fallback: linear scan for callers (e.g. unit tests) that populate
			// Intermediate.DirectChannels / GroupChannels directly without going
			// through transformChannels, which normally builds the map.
			for _, ch := range t.Intermediate.DirectChannels {
				if ch.Id == m.RoomID {
					channelMembers = ch.MembersUsernames
					break
				}
			}
			if channelMembers == nil {
				for _, ch := range t.Intermediate.GroupChannels {
					if ch.Id == m.RoomID {
						channelMembers = ch.MembersUsernames
						break
					}
				}
			}
		}
	} else {
		name, ok := t.roomIDToChannelName[m.RoomID]
		if !ok {
			t.Logger.Debugf("Message %s references unknown room %s, skipping", m.ID, m.RoomID)
			return nil
		}
		channelName = name
	}

	createAt := m.Timestamp.UnixMilli()
	if createAt <= 0 {
		createAt = model.GetMillis()
	}

	return &intermediate.IntermediatePost{
		User:           username,
		Channel:        channelName,
		Message:        m.Message,
		CreateAt:       createAt,
		IsDirect:       isDirect,
		ChannelMembers: channelMembers,
	}
}

// convertReactions converts RC reaction map to IntermediateReaction slice.
func (t *Transformer) convertReactions(m *RocketChatMessage) []*intermediate.IntermediateReaction {
	if len(m.Reactions) == 0 {
		return nil
	}

	var reactions []*intermediate.IntermediateReaction
	baseTs := m.Timestamp.UnixMilli()
	counter := int64(0)

	for emojiCode, info := range m.Reactions {
		// Strip surrounding colons: ":smile:" → "smile"
		emojiName := strings.Trim(emojiCode, ":")

		// Strip skin-tone suffixes: "thumbsup::skin-tone-3" → "thumbsup"
		if idx := strings.Index(emojiName, "::"); idx >= 0 {
			emojiName = emojiName[:idx]
		}

		for _, username := range info.Usernames {
			counter++
			reactions = append(reactions, &intermediate.IntermediateReaction{
				User:      strings.ToLower(username),
				EmojiName: emojiName,
				CreateAt:  baseTs + counter,
			})
		}
	}
	return reactions
}

// channelMentionRe matches a #word token that could be a RC channel reference.
// RC channel names are lowercase alphanumeric with hyphens and underscores.
// We also allow uppercase and dots so we catch display names before lowercasing.
var channelMentionRe = regexp.MustCompile(`#([A-Za-z0-9._-]+)`)

// convertChannelMentions rewrites #channel-name tokens in text to the
// Mattermost format ~channel-name, or strips the leading '#' when the name
// does not correspond to a known channel (to avoid creating spurious hashtags).
//
// RC provides a structured `channels` array on each message listing the
// channels explicitly referenced. We use that first (O(1) lookups), then apply
// a regex pass for any remaining #word tokens in the text.
func (t *Transformer) convertChannelMentions(text string, refs []RCChannelRef) string {
	if !strings.Contains(text, "#") {
		return text
	}

	// Use the precomputed knownChannels map (built once at end of transformChannels).
	// If it is empty — which happens when unit tests set roomIDToChannelName
	// directly without going through transformChannels — rebuild lazily from
	// roomIDToChannelName and cache the result for subsequent calls.
	knownChannels := t.knownChannels
	if len(knownChannels) == 0 && len(t.roomIDToChannelName) > 0 {
		knownChannels = make(map[string]string, len(t.roomIDToChannelName))
		for _, name := range t.roomIDToChannelName {
			if name != "" {
				knownChannels[strings.ToLower(name)] = name
			}
		}
		t.knownChannels = knownChannels
	}

	// Index the structured refs by lowercase name and fname for fast lookup.
	// RC's `channels` array gives us the exact names the sender intended.
	refByName := make(map[string]string, len(refs))
	for _, ref := range refs {
		if ref.Name != "" {
			refByName[strings.ToLower(ref.Name)] = ref.Name
		}
		if ref.FName != "" {
			refByName[strings.ToLower(ref.FName)] = ref.Name // fname → canonical name
		}
	}

	return channelMentionRe.ReplaceAllStringFunc(text, func(match string) string {
		// match is the full "#word"; extract just the word part.
		word := match[1:] // strip leading '#'
		lower := strings.ToLower(word)

		// 1. Check if it matches a channel from the structured refs list.
		//    Then verify that canonical name exists in our known channels.
		if canonicalName, ok := refByName[lower]; ok {
			mmName := strings.ToLower(canonicalName)
			if _, known := knownChannels[mmName]; known {
				return "~" + mmName
			}
		}

		// 2. Check directly against known channel names.
		if _, known := knownChannels[lower]; known {
			return "~" + lower
		}

		// 3. Not a known channel — strip '#' to prevent MM hashtag indexing.
		return word
	})
}

// sanitizeFilename returns a safe filename by replacing non-alphanumeric
// characters (other than '.', '-', '_') with underscores.
func sanitizeFilename(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}
