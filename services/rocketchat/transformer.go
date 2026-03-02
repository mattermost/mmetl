package rocketchat

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/mattermost/mattermost/server/public/model"
	log "github.com/sirupsen/logrus"

	"github.com/mattermost/mmetl/services/intermediate"
)

// Transformer holds all state for a Rocket.Chat → Mattermost transformation.
type Transformer struct {
	TeamName     string
	Intermediate *intermediate.Intermediate
	Logger       log.FieldLogger

	// SkipTeamExport controls whether the team line is written to the JSONL output.
	// Set to true when importing into an existing Mattermost team to avoid the
	// "duplicate team" validation error.
	SkipTeamExport bool

	// skippedRoomIDs records room IDs that were skipped (encrypted/discussion) so
	// that messages in those rooms are also skipped.
	skippedRoomIDs map[string]bool

	// roomIDToChannelName maps RC room _id → Mattermost channel name (or "" for direct rooms).
	roomIDToChannelName map[string]string

	// roomIDToType maps RC room _id → room type string ("c", "p", "d").
	roomIDToType map[string]string
}

// NewTransformer creates a new Transformer for the given team.
func NewTransformer(teamName string, logger log.FieldLogger) *Transformer {
	return &Transformer{
		TeamName:            teamName,
		Intermediate:        &intermediate.Intermediate{},
		Logger:              logger,
		skippedRoomIDs:      make(map[string]bool),
		roomIDToChannelName: make(map[string]string),
		roomIDToType:        make(map[string]string),
	}
}

// ---------------------------------------------------------------------------
// Phase 2.1 — User transformation
// ---------------------------------------------------------------------------

// TransformUsers converts RocketChatUser records into IntermediateUser records
// and stores them in Intermediate.UsersById keyed by RC _id.
func (t *Transformer) TransformUsers(users []RocketChatUser, skipEmptyEmails bool, defaultEmailDomain string) {
	t.Logger.Info("Transforming users")

	result := make(map[string]*intermediate.IntermediateUser, len(users))
	for _, u := range users {
		if u.Type == "bot" {
			t.Logger.Debugf("Skipping bot user: %s", u.Username)
			continue
		}

		var deleteAt int64
		if !u.Active {
			deleteAt = model.GetMillis()
		}

		firstName, lastName := splitName(u.Name)

		email := ""
		if len(u.Emails) > 0 {
			email = u.Emails[0].Address
		}

		roles := mapRoles(u.Roles)

		newUser := &intermediate.IntermediateUser{
			Id:        u.ID,
			Username:  strings.ToLower(u.Username),
			FirstName: firstName,
			LastName:  lastName,
			Email:     email,
			Password:  model.NewId(),
			DeleteAt:  deleteAt,
		}
		_ = roles // roles are set at team/channel level in Mattermost import

		newUser.Sanitise(t.Logger, defaultEmailDomain, skipEmptyEmails)
		result[newUser.Id] = newUser
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

// mapRoles maps RC role strings to Mattermost system role strings.
// Returns the highest-privilege role found.
func mapRoles(roles []string) string {
	for _, r := range roles {
		switch r {
		case "admin":
			return model.SystemAdminRoleId
		}
	}
	return model.SystemUserRoleId
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

// ---------------------------------------------------------------------------
// Phase 2.2 — Channel transformation
// ---------------------------------------------------------------------------

// TransformChannels converts RocketChatRoom records into IntermediateChannel records.
func (t *Transformer) TransformChannels(rooms []RocketChatRoom) {
	t.Logger.Info("Transforming channels")

	for i := range rooms {
		room := &rooms[i]

		if room.Encrypted {
			t.Logger.Warnf("Skipping encrypted room: %s", room.Name)
			t.skippedRoomIDs[room.ID] = true
			continue
		}
		if room.ParentRID != "" {
			t.Logger.Warnf("Skipping discussion room: %s (parent: %s)", room.Name, room.ParentRID)
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
			// Filter out DM/group rooms that contain users not present in UsersById
			// (e.g. rocket.cat or other bot/system users that were skipped during
			// user transformation). Mattermost validation rejects references to
			// unknown users in direct_channel members.
			if unknownUsername := t.firstUnknownMember(room.UIDs, room.Usernames); unknownUsername != "" {
				t.Logger.Warnf("Skipping direct room %s: member %q not in transformed users", room.ID, unknownUsername)
				t.skippedRoomIDs[room.ID] = true
				continue
			}
			if len(room.UIDs) >= 3 {
				ch := t.roomToDirectChannel(room, model.ChannelTypeGroup)
				t.Intermediate.GroupChannels = append(t.Intermediate.GroupChannels, ch)
			} else {
				ch := t.roomToDirectChannel(room, model.ChannelTypeDirect)
				t.Intermediate.DirectChannels = append(t.Intermediate.DirectChannels, ch)
			}
			// Direct channel names are resolved via member usernames at post time.
			t.roomIDToChannelName[room.ID] = ""

		default:
			t.Logger.Warnf("Skipping room with unknown type %q: %s", room.Type, room.Name)
			t.skippedRoomIDs[room.ID] = true
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
		OriginalName: room.Name,
		Name:         strings.ToLower(room.Name),
		DisplayName:  displayName,
		Purpose:      description,
		Header:       room.Topic,
		Type:         chType,
	}
	ch.SanitiseWithPrefix(t.Logger, "imported-channel-")
	return ch
}

func (t *Transformer) roomToDirectChannel(room *RocketChatRoom, chType model.ChannelType) *intermediate.IntermediateChannel {
	ch := &intermediate.IntermediateChannel{
		Id:               room.ID,
		OriginalName:     room.Name,
		Name:             room.Name,
		DisplayName:      room.FName,
		Members:          room.UIDs,
		MembersUsernames: room.Usernames,
		Type:             chType,
	}
	return ch
}

// firstUnknownMember returns the first username in a DM room whose user ID is
// not present in UsersById. Both uids and usernames slices are parallel; if
// uids is empty the lookup falls back to username matching. Returns "" if all
// members are known.
func (t *Transformer) firstUnknownMember(uids, usernames []string) string {
	// Build a username → id reverse map for fallback lookup.
	usernameToID := make(map[string]string, len(t.Intermediate.UsersById))
	for id, u := range t.Intermediate.UsersById {
		usernameToID[strings.ToLower(u.Username)] = id
	}

	for i, uid := range uids {
		if _, ok := t.Intermediate.UsersById[uid]; !ok {
			// Not found by ID — check username fallback.
			if i < len(usernames) {
				if _, ok := usernameToID[strings.ToLower(usernames[i])]; !ok {
					if i < len(usernames) {
						return usernames[i]
					}
					return uid
				}
			} else {
				return uid
			}
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// Phase 2.3 — Subscription → Membership
// ---------------------------------------------------------------------------

// TransformSubscriptions uses subscription records to populate channel member lists
// for public and private channels, and user membership lists.
func (t *Transformer) TransformSubscriptions(subscriptions []RocketChatSubscription) {
	t.Logger.Info("Transforming subscriptions")

	// Build a room-id → channel index for public + private channels.
	channelByRoomID := make(map[string]*intermediate.IntermediateChannel)
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

		// Add to channel Members (by user ID) if not already present.
		alreadyMember := false
		for _, m := range ch.Members {
			if m == sub.User.ID {
				alreadyMember = true
				break
			}
		}
		if !alreadyMember {
			ch.Members = append(ch.Members, sub.User.ID)
		}

		// Add to user Memberships (by channel name) if not already present.
		alreadyMembership := false
		for _, m := range user.Memberships {
			if m == ch.Name {
				alreadyMembership = true
				break
			}
		}
		if !alreadyMembership {
			user.Memberships = append(user.Memberships, ch.Name)
		}
	}
}

// ---------------------------------------------------------------------------
// Phase 2.4 — Message transformation
// ---------------------------------------------------------------------------

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

// TransformMessages converts RC messages into IntermediatePost records.
func (t *Transformer) TransformMessages(messages []RocketChatMessage, uploadsById map[string]*RocketChatUpload) {
	t.Logger.Info("Transforming messages")

	// First pass: build thread map — tmid → list of reply messages.
	threadReplies := make(map[string][]*RocketChatMessage)
	for i := range messages {
		m := &messages[i]
		if m.ThreadID != "" {
			threadReplies[m.ThreadID] = append(threadReplies[m.ThreadID], m)
		}
	}

	// Second pass: process root messages (those without a tmid).
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

		// Attach thread replies.
		for _, reply := range threadReplies[m.ID] {
			replyPost := t.convertMessage(reply, uploadsById)
			if replyPost != nil {
				post.Replies = append(post.Replies, replyPost)
			}
		}

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

	// Truncate/split long messages.
	if utf8.RuneCountInString(post.Message) > model.PostMessageMaxRunesV2 {
		runes := []rune(post.Message)
		post.Message = string(runes[:model.PostMessageMaxRunesV2])
	}

	// Reactions.
	post.Reactions = t.convertReactions(m)

	// File attachments.
	if uploadsById != nil {
		for _, fileRef := range m.Files {
			upload, ok := uploadsById[fileRef.ID]
			if !ok || !upload.Complete {
				continue
			}
			sanitizedName := sanitizeFilename(upload.Name)
			attachPath := fmt.Sprintf("bulk-export-attachments/%s_%s", upload.ID, sanitizedName)
			post.Attachments = append(post.Attachments, attachPath)
		}
	}

	return post
}

// buildBasePost constructs a base IntermediatePost from a message, resolving
// user and channel references. Returns nil if the post should be skipped.
func (t *Transformer) buildBasePost(m *RocketChatMessage) *intermediate.IntermediatePost {
	// Resolve user.
	username := strings.ToLower(m.User.Username)
	if username == "" {
		if _, ok := t.Intermediate.UsersById[m.User.ID]; !ok {
			t.createPlaceholderUser(m.User.ID)
		}
		user := t.Intermediate.UsersById[m.User.ID]
		if user != nil {
			username = user.Username
		}
	}

	// Resolve channel.
	roomType := t.roomIDToType[m.RoomID]
	isDirect := roomType == "d"

	var channelName string
	var channelMembers []string

	if isDirect {
		// For direct posts, channel name is empty; members are resolved from the room.
		channelName = ""
		// We need the channel's member usernames.
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

	// Build a lookup of all known channel names (already lowercased by
	// roomToIntermediateChannel / SanitiseWithPrefix).
	// roomIDToChannelName values are the canonical MM channel names.
	knownChannels := make(map[string]string, len(t.roomIDToChannelName))
	for _, name := range t.roomIDToChannelName {
		if name != "" {
			knownChannels[strings.ToLower(name)] = name
		}
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
