package rocketchat

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/mattermost/mattermost/server/public/model"
	log "github.com/sirupsen/logrus"
	"golang.org/x/text/unicode/norm"

	"github.com/mattermost/mmetl/services/intermediate"
)

// Transformer holds all state for a RocketChat → Mattermost transformation.
type Transformer struct {
	intermediate.Exporter // provides TeamName, Intermediate, Logger, and all export methods

	// skippedRoomIDs records room IDs that were skipped (e.g. encrypted or
	// unknown-type rooms) so that messages in those rooms are also skipped.
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

	// skippedUserIDs / skippedUsernames record users that were dropped during
	// transformUsers (unsupported RC types, or guests under --guest-handling=skip)
	// so that every later stage can drop channel/DM memberships, posts, and
	// reactions referencing them, leaving no dangling references in the export.
	skippedUserIDs   map[string]bool
	skippedUsernames map[string]bool

	// skippedUsernameByID maps a skipped user ID to the username it had, so the
	// report can still name the user behind a dropped membership or post after
	// the IntermediateUser is gone.
	skippedUsernameByID map[string]string
}

// Guest handling modes for the --guest-handling flag.
const (
	// GuestHandlingGuest migrates RC guests as Mattermost guest accounts.
	GuestHandlingGuest = "guest"
	// GuestHandlingUser migrates RC guests as regular Mattermost users.
	GuestHandlingUser = "user"
	// GuestHandlingSkip drops RC guests entirely.
	GuestHandlingSkip = "skip"
)

// ValidateGuestHandling returns an error if the given guest-handling mode is not
// one of the supported values.
func ValidateGuestHandling(mode string) error {
	switch mode {
	case GuestHandlingGuest, GuestHandlingUser, GuestHandlingSkip:
		return nil
	default:
		return fmt.Errorf("invalid --guest-handling value %q: must be one of %q, %q, or %q",
			mode, GuestHandlingGuest, GuestHandlingUser, GuestHandlingSkip)
	}
}

// rolesContainGuest reports whether the RC roles slice contains the "guest"
// role (case-insensitive).
func rolesContainGuest(roles []string) bool {
	for _, r := range roles {
		if strings.EqualFold(r, "guest") {
			return true
		}
	}
	return false
}

// isSkippedUser reports whether the given RC user ID was dropped in
// transformUsers.
func (t *Transformer) isSkippedUser(id string) bool {
	return id != "" && t.skippedUserIDs[id]
}

// NewTransformer creates a new Transformer for the given team.
func NewTransformer(teamName string, logger log.FieldLogger) *Transformer {
	return &Transformer{
		Exporter: intermediate.Exporter{
			TeamName:     teamName,
			Intermediate: &intermediate.Intermediate{},
			Logger:       logger,
			Report:       intermediate.NewReport(logger),
		},
		skippedRoomIDs:        make(map[string]bool),
		roomIDToChannelName:   make(map[string]string),
		roomIDToType:          make(map[string]string),
		directRoomIDToChannel: make(map[string]*intermediate.IntermediateChannel),
		knownChannels:         make(map[string]string),
		skippedUserIDs:        make(map[string]bool),
		skippedUsernames:      make(map[string]bool),
		skippedUsernameByID:   make(map[string]string),
	}
}

// Transform runs all transformation phases against a parsed dump in order:
// users, channels, subscriptions, then messages.
// When skipAttachments is true, no attachment paths are written into posts.
func (t *Transformer) Transform(parsed *ParsedData, skipAttachments bool, skipEmptyEmails bool, defaultEmailDomain string, guestHandling string) error {
	// Guests are exported with Mattermost guest roles only in "guest" mode.
	t.EmitGuestRoles = guestHandling == GuestHandlingGuest

	if err := t.transformUsers(parsed.Users, skipEmptyEmails, defaultEmailDomain, guestHandling); err != nil {
		return err
	}
	t.transformChannels(parsed.Rooms)
	t.transformSubscriptions(parsed.Subscriptions)
	// Must run after transformSubscriptions, which is what populates
	// user.Memberships — the signal that decides whether a guest is channel-less.
	t.skipChannellessGuests()
	var uploadsForTransform map[string]*RocketChatUpload
	if !skipAttachments {
		uploadsForTransform = parsed.UploadsByID
	}
	t.transformMessages(parsed.Messages, uploadsForTransform)

	return nil
}

// transformUsers converts RocketChatUser records into IntermediateUser records
// and stores them in Intermediate.UsersById keyed by RC _id.
func (t *Transformer) transformUsers(users []RocketChatUser, skipEmptyEmails bool, defaultEmailDomain string, guestHandling string) error {
	t.Logger.Info("Transforming users")

	// Count the source accounts once, split by the report kind each will land
	// under. Accounts of an unsupported type are counted as users, which is what
	// they are skipped as below.
	sourceBots := 0
	for _, u := range users {
		if u.Type == "bot" {
			sourceBots++
		}
	}
	t.Report.Bots().Seen(sourceBots)
	t.Report.Users().Seen(len(users) - sourceBots)

	result := make(map[string]*intermediate.IntermediateUser, len(users))
	// guestCount counts every guest the dump held, migrated ones included. The
	// report only records the guests it skipped, so this is the one place the
	// total lives.
	guestCount := 0
	for _, u := range users {
		// Only real people (type "user") and bots (type "bot") are migrated.
		// Everything else — "app" (marketplace/app-owned accounts like
		// rocket.cat), "unknown", empty, etc. — is skipped and recorded so that
		// downstream stages drop any memberships, posts, and reactions that
		// reference it.
		isBot := u.Type == "bot"
		if u.Type != "user" && !isBot {
			typeLabel := u.Type
			if typeLabel == "" {
				typeLabel = "empty"
			}
			t.markUserSkipped(u.ID, u.Username)
			t.Report.Users().Skip(u.ID, u.Username, ReasonUserUnsupportedType, typeLabel)
			continue
		}

		// A guest is a type "user" whose roles include "guest"; bots are never
		// guests.
		isGuest := !isBot && rolesContainGuest(u.Roles)
		if isGuest {
			guestCount++
			if guestHandling == GuestHandlingSkip {
				t.markUserSkipped(u.ID, u.Username)
				t.Report.Users().Skip(u.ID, u.Username, intermediate.ReasonGuestSkipMode)
				continue
			}
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

		newUser := &intermediate.IntermediateUser{
			Id:          u.ID,
			IsBot:       isBot,
			IsGuest:     isGuest,
			Username:    strings.ToLower(u.Username),
			FirstName:   firstName,
			LastName:    lastName,
			DisplayName: u.Name,
			Email:       email,
			DeleteAt:    deleteAt,
		}

		if !newUser.IsBot {
			if err := newUser.Sanitise(t.Report.Users(), defaultEmailDomain, skipEmptyEmails); err != nil {
				return err
			}
		}
		result[newUser.Id] = newUser
		t.Logger.Debugf("transformed user: %s isBot: %t isGuest: %t", newUser.Username, newUser.IsBot, newUser.IsGuest)
	}

	t.Intermediate.UsersById = result

	// Which accounts were skipped, of what type, and which guests went with
	// them are all in the transform report, named individually.
	switch guestHandling {
	case GuestHandlingGuest:
		t.Logger.Infof("Detected %d guest users; mode=guest (migrating as MM guests)", guestCount)
	case GuestHandlingUser:
		t.Logger.Infof("Detected %d guest users; mode=user (migrating as regular users)", guestCount)
	case GuestHandlingSkip:
		t.Logger.Infof("Detected %d guest users; mode=skip", guestCount)
	}

	return nil
}

// countAndDropSkippedMembers is dropSkippedMembers for the first pass over a
// room, where its members are also counted as the source memberships they are.
func (t *Transformer) countAndDropSkippedMembers(roomID string, uids, usernames []string) (outUIDs, outUsernames []string) {
	t.Report.ChannelMemberships().Seen(max(len(uids), len(usernames)))
	return t.dropSkippedMembers(roomID, uids, usernames)
}

// dropSkippedMembers returns the given parallel uid/username slices with any
// skipped users removed, recording each removal against roomID in the report.
// The two slices are only filtered together when they are the same length (RC
// stores them as parallel arrays); otherwise they are filtered independently by
// ID and username respectively. It does not count the members it keeps — use
// countAndDropSkippedMembers on the pass that first reads a room's member list.
func (t *Transformer) dropSkippedMembers(roomID string, uids, usernames []string) (outUIDs, outUsernames []string) {
	memberships := t.Report.ChannelMemberships()
	drop := func(uid, username string) {
		if username == "" {
			username = t.skippedUsernameByID[uid]
		}
		memberships.Skip(intermediate.MembershipID(roomID, uid), intermediate.MembershipID(roomID, username), intermediate.ReasonMembershipSkippedUser)
	}

	if len(uids) == len(usernames) {
		outUIDs = make([]string, 0, len(uids))
		outUsernames = make([]string, 0, len(usernames))
		for i, uid := range uids {
			if t.skippedUserIDs[uid] || t.skippedUsernames[usernames[i]] {
				drop(uid, usernames[i])
				continue
			}
			outUIDs = append(outUIDs, uid)
			outUsernames = append(outUsernames, usernames[i])
		}
		return outUIDs, outUsernames
	}

	for _, uid := range uids {
		if t.skippedUserIDs[uid] {
			drop(uid, "")
			continue
		}
		outUIDs = append(outUIDs, uid)
	}
	for _, username := range usernames {
		if t.skippedUsernames[username] {
			drop("", username)
			continue
		}
		outUsernames = append(outUsernames, username)
	}
	return outUIDs, outUsernames
}

// skipRemainingMembers records every member still on a room that is about to be
// dropped. Their memberships were counted when the room's member list was first
// read, and Transformed is derived as seen-minus-skipped, so without this a
// dropped room's members would be reported as having reached the import file.
// It walks whichever of the two parallel arrays is longer, because a malformed
// room can leave them out of step — and Seen counted the longer of the two.
func (t *Transformer) skipRemainingMembers(roomID string, uids, usernames []string) {
	memberships := t.Report.ChannelMemberships()
	at := func(values []string, i int) string {
		if i < len(values) {
			return values[i]
		}
		return ""
	}
	for i := range max(len(uids), len(usernames)) {
		memberships.Skip(
			intermediate.MembershipID(roomID, at(uids, i)),
			intermediate.MembershipID(roomID, at(usernames, i)),
			intermediate.ReasonMembershipChannelSkipped,
		)
	}
}

// markUserSkipped records a user (by ID and username) as skipped so downstream
// stages drop everything referencing them.
func (t *Transformer) markUserSkipped(id, username string) {
	if id != "" {
		t.skippedUserIDs[id] = true
	}
	if username != "" {
		t.skippedUsernames[strings.ToLower(username)] = true
	}
	if id != "" && username != "" {
		if t.skippedUsernameByID == nil {
			t.skippedUsernameByID = map[string]string{}
		}
		t.skippedUsernameByID[id] = strings.ToLower(username)
	}
}

// skipChannellessGuests drops guest users (in "guest" mode only) that ended up
// with zero public/private channel memberships. Such a user cannot be
// represented as a Mattermost guest — every no-channel guest role shape fails
// the server's isValidGuestRoles and would abort the whole bulk import — and
// silently promoting them to a full member would grant public-channel access a
// guest never had. We drop them instead; operators who want channel-less guests
// kept as regular members can pass --guest-handling=user.
//
// This must run after transformSubscriptions, which populates user.Memberships.
// Because the direct/group channels were already built (in transformChannels)
// before these users were marked skipped, their DM member lists are re-filtered
// here so no exported line references a user with no user line.
func (t *Transformer) skipChannellessGuests() {
	if !t.EmitGuestRoles {
		return
	}

	dropped := false
	for id, user := range t.Intermediate.UsersById {
		if !user.IsGuest || user.IsBot || len(user.Memberships) > 0 {
			continue
		}
		t.Report.Users().Skip(id, user.Username, intermediate.ReasonGuestNoChannel)
		t.markUserSkipped(id, user.Username)
		delete(t.Intermediate.UsersById, id)
		dropped = true
	}

	if !dropped {
		return
	}
	t.rebuildDMsWithoutSkippedMembers()
}

// rebuildDMsWithoutSkippedMembers re-filters the already-built direct and group
// channels to remove members that were skipped after the channels were created
// (i.e. channel-less guests). It mirrors the classification logic in
// transformChannels' direct-room handling: a channel with no remaining members
// is dropped (and its room recorded so messages are skipped), a single
// remaining member becomes a self-DM, three or more members stay a group DM,
// and two members are a direct channel — so a group that shrinks below three is
// reclassified accordingly.
func (t *Transformer) rebuildDMsWithoutSkippedMembers() {
	var directs, groups []*intermediate.IntermediateChannel

	refilter := func(channels []*intermediate.IntermediateChannel) {
		for _, ch := range channels {
			// Not counted again: these members were already counted when the
			// room's member list was first read in transformChannels.
			uids, usernames := t.dropSkippedMembers(ch.Id, ch.Members, ch.MembersUsernames)
			// The channel's kind in the dump: this pass can reclassify a group
			// room as a direct one, but the report still counts it as what the
			// dump held.
			entity := ch.ReportEntity(t.Report)

			if len(uids) == 0 {
				entity.Skip(ch.ReportID(), ch.ReportName(), ReasonDMAllMembersSkipped)
				t.skipRemainingMembers(ch.Id, uids, usernames)
				t.skippedRoomIDs[ch.Id] = true
				delete(t.directRoomIDToChannel, ch.Id)
				continue
			}

			// dropSkippedMembers filters uid and username arrays independently
			// when they are not parallel, so a malformed member list can leave
			// unequal counts here. We can't reliably pair members in that case
			// (and duplicating a self-DM below would panic), so drop the room.
			if len(uids) != len(usernames) {
				entity.Skip(ch.ReportID(), ch.ReportName(), ReasonDMMemberCountMismatch,
					strconv.Itoa(len(uids)), strconv.Itoa(len(usernames)))
				t.skipRemainingMembers(ch.Id, uids, usernames)
				t.skippedRoomIDs[ch.Id] = true
				delete(t.directRoomIDToChannel, ch.Id)
				continue
			}

			// RC self-DMs are modelled as a direct channel where the same user
			// appears twice.
			if len(uids) == 1 {
				uids = []string{uids[0], uids[0]}
				usernames = []string{usernames[0], usernames[0]}
			}

			ch.Members = uids
			ch.MembersUsernames = usernames
			if len(uids) >= 3 {
				ch.Type = model.ChannelTypeGroup
				groups = append(groups, ch)
			} else {
				ch.Type = model.ChannelTypeDirect
				directs = append(directs, ch)
			}
			t.directRoomIDToChannel[ch.Id] = ch
		}
	}

	refilter(t.Intermediate.GroupChannels)
	refilter(t.Intermediate.DirectChannels)

	t.Intermediate.GroupChannels = groups
	t.Intermediate.DirectChannels = directs
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
	// The placeholder is not a source entity, but it does reach the import
	// file, so it is counted as one to keep Transformed consistent.
	t.Report.Users().Seen(1)
	t.Report.Users().Note(rcUserID, username, intermediate.ReasonUserPlaceholderCreated)
	return u
}

// transformChannels converts RocketChatRoom records into IntermediateChannel records.
func (t *Transformer) transformChannels(rooms []RocketChatRoom) {
	t.Logger.Info("Transforming channels")

	// Count each source room under the kind it would import as, before anything
	// is dropped, so Transformed can be derived from what the dump held.
	for i := range rooms {
		t.Report.For(roomEntityKind(&rooms[i])).Seen(1)
	}

	for i := range rooms {
		room := &rooms[i]
		entity := t.Report.For(roomEntityKind(room))

		if room.Encrypted {
			entity.Skip(room.ID, roomOriginalName(room), ReasonRoomEncrypted)
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

			// Drop skipped users (unsupported types / skipped guests) from the
			// DM member list so we never export a direct/group channel that
			// references a user with no corresponding user line.
			uids, usernames = t.countAndDropSkippedMembers(room.ID, uids, usernames)

			// If every member was skipped, there is nothing to migrate.
			if len(uids) == 0 {
				entity.Skip(room.ID, roomOriginalName(room), ReasonDMAllMembersSkipped)
				t.skipRemainingMembers(room.ID, uids, usernames)
				t.skippedRoomIDs[room.ID] = true
				continue
			}

			// A member list with unequal uid/username counts is malformed (RC
			// stores them as parallel arrays, and dropSkippedMembers filters them
			// independently when they are not). We can't reliably pair members,
			// so drop the room rather than risk dangling references or an
			// out-of-range panic in the self-DM duplication below.
			if len(uids) != len(usernames) {
				entity.Skip(room.ID, roomOriginalName(room), ReasonDMMemberCountMismatch,
					strconv.Itoa(len(uids)), strconv.Itoa(len(usernames)))
				t.skipRemainingMembers(room.ID, uids, usernames)
				t.skippedRoomIDs[room.ID] = true
				continue
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
				entity.Note(room.ID, roomOriginalName(room), intermediate.ReasonMPIMConvertedToPrivate, strconv.Itoa(len(uids)))
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
			roomType := room.Type
			if roomType == "" {
				roomType = "empty"
			}
			entity.Skip(room.ID, roomOriginalName(room), ReasonRoomUnknownType, roomType)
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
	// Capture OriginalName before mutating room.Name below, so an empty name
	// falls back to the bare room ID rather than the "channel-<id>" slug.
	originalName := roomOriginalName(room)

	// Handle case of group rooms which are converted to private channels due to exceeding the group DM member limit.
	if room.Name == "" {
		t.Report.For(roomEntityKind(room)).Note(room.ID, originalName, ReasonRoomNoName)
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
		OriginalName: originalName,
		Name:         rcConvertChannelName(room.Name, room.ID),
		DisplayName:  displayName,
		Purpose:      description,
		Header:       room.Topic,
		Type:         chType,
		// The room's kind in the dump, not the type it is being imported as: an
		// oversized group DM arrives here as a private channel but is still
		// reported as the group channel it was.
		ReportKind: roomEntityKind(room),
	}
	ch.SanitiseWithPrefix(ch.ReportEntity(t.Report), "rocketchat-channel-")
	return ch
}

func (t *Transformer) roomToDirectChannel(room *RocketChatRoom, chType model.ChannelType, uids, usernames []string) *intermediate.IntermediateChannel {
	ch := &intermediate.IntermediateChannel{
		Id:               room.ID,
		OriginalName:     roomOriginalName(room),
		Members:          uids,
		MembersUsernames: usernames,
		Type:             chType,
		ReportKind:       roomEntityKind(room),
	}
	return ch
}

// roomEntityKind maps a RocketChat room to the report entity kind it is counted
// under. A direct room becomes a group channel once it has more than two
// members, which is the same split transformChannels makes. Rooms of an unknown
// type have no Mattermost counterpart at all; they are counted as public
// channels so their skip is still visible somewhere in the report.
func roomEntityKind(room *RocketChatRoom) intermediate.EntityKind {
	switch room.Type {
	case "p":
		return intermediate.EntityPrivateChannel
	case "d":
		if len(room.UIDs) > 2 {
			return intermediate.EntityGroupChannel
		}
		return intermediate.EntityDirectChannel
	default:
		return intermediate.EntityPublicChannel
	}
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

// rcConvertChannelName converts a RocketChat room name to a
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

	channelMemberSets := make(map[string]map[string]struct{}, len(channelByRoomID))
	userMembershipSets := make(map[string]map[string]struct{}, len(t.Intermediate.UsersById))

	entity := t.Report.Subscriptions()

	for i := range subscriptions {
		sub := &subscriptions[i]

		ch, ok := channelByRoomID[sub.RoomID]
		if !ok {
			// Subscription to a DM/group/skipped room — not relevant here, and
			// not counted, because this pass is only responsible for the
			// subscriptions that become public/private channel memberships.
			continue
		}
		entity.Seen(1)

		// Drop memberships of users we deliberately skipped (they are expected
		// to be absent).
		if t.isSkippedUser(sub.User.ID) {
			entity.Skip(
				intermediate.MembershipID(sub.RoomID, sub.User.ID),
				intermediate.MembershipID(ch.Name, t.skippedUsernameByID[sub.User.ID]),
				intermediate.ReasonMembershipSkippedUser,
			)
			continue
		}

		user, ok := t.Intermediate.UsersById[sub.User.ID]
		if !ok {
			entity.Skip(
				intermediate.MembershipID(sub.RoomID, sub.User.ID),
				intermediate.MembershipID(ch.Name, sub.User.Username),
				ReasonSubscriptionUnknownUser,
			)
			continue
		}

		// Bots don't need channel or team memberships.
		if user.IsBot {
			continue
		}

		// Add to channel Members (by user ID) if not already present.
		memberSet, ok := channelMemberSets[sub.RoomID]
		if !ok {
			memberSet = make(map[string]struct{}, len(ch.Members))
			for _, id := range ch.Members {
				memberSet[id] = struct{}{}
			}
			channelMemberSets[sub.RoomID] = memberSet
		}
		if _, exists := memberSet[sub.User.ID]; !exists {
			memberSet[sub.User.ID] = struct{}{}
			ch.Members = append(ch.Members, sub.User.ID)
		}

		// Add channel to the user's memberships if not already present.
		membershipSet, ok := userMembershipSets[sub.User.ID]
		if !ok {
			membershipSet = make(map[string]struct{}, len(user.Memberships))
			for _, m := range user.Memberships {
				membershipSet[m.Name] = struct{}{}
			}
			userMembershipSets[sub.User.ID] = membershipSet
		}
		if _, exists := membershipSet[ch.Name]; !exists {
			membershipSet[ch.Name] = struct{}{}
			user.Memberships = append(user.Memberships, intermediate.IntermediateMembership{Name: ch.Name})
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
	reportPosts := t.Report.Posts()
	reportThreads := t.Report.Threads()
	reportPosts.Seen(len(messages))
	reportThreads.Seen(len(threadReplies))

	var posts []*intermediate.IntermediatePost
	for i := range messages {
		m := &messages[i]

		// Skip thread replies — they will be attached to their root post.
		if m.ThreadID != "" {
			continue
		}

		// dropThreadReplies names every reply lost with a root that was not
		// imported. Replies carry a ThreadID, so they are skipped by the loop
		// above and never reach convertMessage; without this they would go
		// missing from both the import file and the report.
		dropThreadReplies := func() {
			replies := threadReplies[m.ID]
			if len(replies) == 0 {
				return
			}
			reportThreads.Skip(intermediate.PostID(m.RoomID, m.ID), m.User.Username, intermediate.ReasonThreadRootMissing)
			for _, reply := range replies {
				reportPosts.Skip(intermediate.PostID(reply.RoomID, reply.ID), reply.User.Username, intermediate.ReasonThreadRootMissing)
			}
		}

		// Skip messages in skipped rooms.
		if t.skippedRoomIDs[m.RoomID] {
			reportPosts.Skip(intermediate.PostID(m.RoomID, m.ID), m.User.Username, ReasonMessageUnknownRoom, m.RoomID)
			dropThreadReplies()
			continue
		}

		post := t.convertMessage(m, uploadsById)
		if post == nil {
			// The root was dropped (e.g. authored by a skipped user).
			dropThreadReplies()
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
		if chunks := intermediate.SplitPostIntoThread(post); chunks > 1 {
			reportPosts.Note(intermediate.PostID(m.RoomID, m.ID), post.User, intermediate.ReasonPostSplit, strconv.Itoa(chunks))
		}

		// Split any oversized replies, deduplicate timestamps, and sort replies.
		if split := intermediate.SplitOversizedReplies(post); split > 0 {
			reportPosts.Note(intermediate.PostID(m.RoomID, m.ID), post.User, intermediate.ReasonPostRepliesSplit, strconv.Itoa(split))
		}

		posts = append(posts, post)
	}

	t.Intermediate.Posts = posts
	t.Logger.Infof("Transformed %d posts", len(posts))
}

// convertMessage converts a single RocketChatMessage to an IntermediatePost.
// Returns nil if the message should be skipped.
func (t *Transformer) convertMessage(m *RocketChatMessage, uploadsById map[string]*RocketChatUpload) *intermediate.IntermediatePost {
	// Drop posts authored by a skipped user (unsupported type / skipped guest)
	// before any placeholder user is created for them, so the export never
	// references a user with no user line.
	if t.isSkippedUser(m.User.ID) || t.skippedUsernames[strings.ToLower(m.User.Username)] {
		t.Report.Posts().Skip(intermediate.PostID(m.RoomID, m.ID), strings.ToLower(m.User.Username), intermediate.ReasonPostSkippedAuthor)
		return nil
	}

	// Handle system messages.
	if m.Type != "" {
		if skippedSystemMessageTypes[m.Type] {
			t.Report.Posts().Skip(intermediate.PostID(m.RoomID, m.ID), strings.ToLower(m.User.Username), ReasonMessageUnsupportedType, m.Type)
			return nil
		}
		mmType, ok := systemMessageTypeMap[m.Type]
		if !ok {
			t.Report.Posts().Skip(intermediate.PostID(m.RoomID, m.ID), strings.ToLower(m.User.Username), ReasonMessageUnsupportedType, m.Type)
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
			if fileRef.TypeGroup == "thumb" {
				continue
			}
			upload, ok := uploadsById[fileRef.ID]
			if !ok || !upload.Complete {
				continue
			}
			// If the message has no text but the upload has a description
			// (caption typed by the user when uploading), use it as the post message.
			if post.Message == "" && upload.Description != "" {
				post.Message = upload.Description
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
		if user, ok := t.Intermediate.UsersById[m.User.ID]; ok {
			// Prefer the canonical username from the users collection over the
			// per-message username, which can be stale if the user was renamed
			// in RocketChat. This keeps the post's author matching the exported
			// user line.
			username = user.Username
		} else {
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
			t.Report.Posts().Skip(intermediate.PostID(m.RoomID, m.ID), strings.ToLower(m.User.Username), ReasonMessageUnknownRoom, m.RoomID)
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

	entity := t.Report.Reactions()

	var reactions []*intermediate.IntermediateReaction
	baseTs := m.Timestamp.UnixMilli()
	counter := int64(0)

	// Sort keys so SanitizeEmojiName collision ownership is deterministic
	// across runs (Go map iteration order is randomized).
	codes := make([]string, 0, len(m.Reactions))
	for code := range m.Reactions {
		codes = append(codes, code)
	}
	sort.Strings(codes)

	for _, emojiCode := range codes {
		info := m.Reactions[emojiCode]
		// Strip surrounding colons: ":smile:" → "smile"
		emojiName := strings.Trim(emojiCode, ":")

		// Strip skin-tone suffixes: "thumbsup::skin-tone-3" → "thumbsup"
		if idx := strings.Index(emojiName, "::"); idx >= 0 {
			emojiName = emojiName[:idx]
		}

		emojiName = t.SanitizeEmojiName(emojiName)

		entity.Seen(len(info.Usernames))
		for _, username := range info.Usernames {
			lower := strings.ToLower(username)
			// Skip reactions by skipped users so they don't reference a
			// non-existent user line.
			if t.skippedUsernames[lower] {
				entity.Skip(intermediate.ReactionID(m.RoomID, m.ID, lower, emojiName), lower, intermediate.ReasonReactionSkippedUser)
				continue
			}
			counter++
			reactions = append(reactions, &intermediate.IntermediateReaction{
				User:      lower,
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
