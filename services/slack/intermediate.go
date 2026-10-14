package slack

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	"golang.org/x/text/unicode/norm"

	"github.com/mattermost/mmetl/services/intermediate"
)

const attachmentsInternal = "bulk-export-attachments"

// minValidCreatedTimestamp is the minimum Unix timestamp (seconds) Slack uses
// for a real channel creation time. Slack launched in 2013 and encodes missing
// creation times with placeholder values (e.g. "created": 1 for DMs), so any
// value before Jan 1, 2013 is treated as absent. This heuristic is specific to
// Slack exports and must not leak into the shared intermediate package.
//
// Values lower than this are normalized to 0 (see normalizeSlackCreated), so the
// constant itself is never used in downstream comparisons — those test Created > 0.
const minValidCreatedTimestamp = 1356998400

// normalizeSlackCreated collapses Slack's placeholder creation timestamps to 0,
// the source-agnostic "absent" sentinel understood by the intermediate package.
// Downstream code can then test validity with a simple Created > 0 check.
func normalizeSlackCreated(created int64) int64 {
	if created < minValidCreatedTimestamp {
		return 0
	}
	return created
}

// The intermediate representation now lives in the shared services/intermediate
// package so it can be reused across import sources (Slack, RocketChat, ...).
// These aliases keep the Slack transform code below unchanged. Methods such as
// CreatedMillis and Sanitise are defined on the canonical types in that package.
type (
	IntermediateChannel    = intermediate.IntermediateChannel
	IntermediateMembership = intermediate.IntermediateMembership
	IntermediateUser       = intermediate.IntermediateUser
	IntermediateReaction   = intermediate.IntermediateReaction
	IntermediatePost       = intermediate.IntermediatePost
	Intermediate           = intermediate.Intermediate
)

// directChannelKey returns a deterministic lookup key for a direct/group
// channel by sorting and joining the member usernames.
func directChannelKey(members []string) string {
	sorted := make([]string, len(members))
	copy(sorted, members)
	sort.Strings(sorted)
	return strings.Join(sorted, ",")
}

// ComputeChannelPostStats iterates all transformed posts and accumulates
// MsgCount, MsgCountRoot, and LastPostAt on each IntermediateChannel.
// Must be called after TransformPosts completes.
func (t *Transformer) ComputeChannelPostStats() {
	channelsByName := make(map[string]*IntermediateChannel)
	for _, ch := range t.Intermediate.PublicChannels {
		channelsByName[ch.Name] = ch
	}
	for _, ch := range t.Intermediate.PrivateChannels {
		channelsByName[ch.Name] = ch
	}

	channelsByMembers := make(map[string]*IntermediateChannel)
	for _, ch := range t.Intermediate.GroupChannels {
		channelsByMembers[directChannelKey(ch.MembersUsernames)] = ch
	}
	for _, ch := range t.Intermediate.DirectChannels {
		channelsByMembers[directChannelKey(ch.MembersUsernames)] = ch
	}

	for _, post := range t.Intermediate.Posts {
		var ch *IntermediateChannel
		if post.IsDirect {
			ch = channelsByMembers[directChannelKey(post.ChannelMembers)]
		} else {
			ch = channelsByName[post.Channel]
		}
		if ch == nil {
			continue
		}

		// Each entry in Intermediate.Posts is a root post; replies are
		// nested in post.Replies (see AddPostToThreads).
		ch.MsgCount++
		ch.MsgCountRoot++
		if post.CreateAt > ch.LastPostAt {
			ch.LastPostAt = post.CreateAt
		}

		for _, reply := range post.Replies {
			ch.MsgCount++
			if reply.CreateAt > ch.LastPostAt {
				ch.LastPostAt = reply.CreateAt
			}
		}
	}
}

// TransformUsers converts SlackUser records into IntermediateUser records. A
// Slack guest (is_restricted or is_ultra_restricted) is always flagged via
// IsGuest, regardless of guestHandling; guestHandling only decides whether the
// guest is dropped entirely here ("skip") or kept for later export ("guest"/
// "user" — the actual role emitted for a kept guest is decided by
// Exporter.EmitGuestRoles, set from guestHandling by the caller).
func (t *Transformer) TransformUsers(users []SlackUser, skipEmptyEmails bool, defaultEmailDomain string, guestHandling string) error {
	t.Logger.Info("Transforming users")

	t.Logger.Debugf("TransformUsers: Input SlackUser structs: %+v", users)

	// Count the source users once, split by the report kind each will land
	// under, so Transformed can be derived from what the export actually held.
	sourceUsers, sourceBots := 0, 0
	for _, user := range users {
		if user.IsBot {
			sourceBots++
		} else {
			sourceUsers++
		}
	}
	t.Report.Users().Seen(sourceUsers)
	t.Report.Bots().Seen(sourceBots)

	resultUsers := map[string]*IntermediateUser{}
	guestCount := 0
	guestsSkipped := 0
	for _, user := range users {
		isGuest := !user.IsBot && user.IsGuest()
		if isGuest {
			guestCount++
			if guestHandling == GuestHandlingSkip {
				guestsSkipped++
				t.markUserSkipped(user.Id, user.Username)
				t.Report.Users().Skip(user.Id, user.Username, intermediate.ReasonGuestSkipMode)
				continue
			}
		}

		var deleteAt int64 = 0
		if user.Deleted {
			deleteAt = model.GetMillis()
		}

		firstName := ""
		lastName := ""
		if user.Profile.RealName != "" {
			names := strings.Split(user.Profile.RealName, " ")
			firstName = names[0]
			lastName = strings.Join(names[1:], " ")
		}

		t.Logger.Debugf("TransformUsers: SlackUser struct: %+v", user)
		t.Logger.Debugf("TransformUsers: SlackUser.Profile struct: %+v", user.Profile)

		newUser := &IntermediateUser{
			Id:          user.Id,
			Username:    user.Username,
			FirstName:   firstName,
			LastName:    lastName,
			DisplayName: user.Profile.RealName,
			Position:    user.Profile.Title,
			Email:       user.Profile.Email,
			Password:    model.NewId(),
			DeleteAt:    deleteAt,
			IsGuest:     isGuest,
		}

		t.Logger.Debugf("TransformUsers: newUser IntermediateUser struct: %+v", newUser)

		if user.IsBot {
			if user.Profile.BotID != "" {
				newUser.Id = user.Profile.BotID
			} else {
				t.Report.Bots().Note(user.Id, user.Username, ReasonBotNoBotID, user.Id)
			}
			newUser.IsBot = true
		}

		if !newUser.IsBot {
			if err := newUser.Sanitise(t.Report.Users(), defaultEmailDomain, skipEmptyEmails); err != nil {
				return err
			}
		}
		resultUsers[newUser.Id] = newUser
		t.Logger.Debugf("Slack user with username %s has been imported.", newUser.Username)
	}

	t.Intermediate.UsersById = resultUsers

	switch guestHandling {
	case GuestHandlingGuest:
		t.Logger.Infof("Detected %d guest users; mode=guest (migrating as MM guests)", guestCount)
	case GuestHandlingUser:
		t.Logger.Infof("Detected %d guest users; mode=user (migrating as regular users)", guestCount)
	case GuestHandlingSkip:
		t.Logger.Infof("Detected %d guest users; mode=skip (skipping %d guest users)", guestCount, guestsSkipped)
	}

	return nil
}

// filterValidMembers drops members that were skipped and invents a placeholder
// for members the export never described. It is the single point at which a
// source channel's member list is read, so it is also where channel memberships
// are counted for the report.
func (t *Transformer) filterValidMembers(channel SlackChannel, members []string, users map[string]*IntermediateUser) []string {
	memberships := t.Report.ChannelMemberships()
	memberships.Seen(len(members))

	validMembers := []string{}
	for _, member := range members {
		if t.skippedUserIDs[member] {
			memberships.Skip(
				intermediate.MembershipID(channel.Id, member),
				intermediate.MembershipID(getOriginalName(channel), t.usernameFor(member)),
				intermediate.ReasonMembershipSkippedUser,
			)
			continue
		}
		if _, ok := users[member]; ok {
			validMembers = append(validMembers, member)
		} else {
			// Create a new deleted user for this lost reference so we can handle channel memberships appropriately
			t.CreateIntermediateUser(member)
			validMembers = append(validMembers, member)
		}
	}
	return validMembers
}

// skipRemainingMembers records every member still on a channel that is about to
// be dropped. Their memberships were counted when the channel's member list was
// first read, and Transformed is derived as seen-minus-skipped, so without this
// a dropped channel's members would be reported as having reached the import
// file.
func (t *Transformer) skipRemainingMembers(channelID, channelName string, members []string, reason *intermediate.Reason) {
	memberships := t.Report.ChannelMemberships()
	for _, member := range members {
		memberships.Skip(
			intermediate.MembershipID(channelID, member),
			intermediate.MembershipID(channelName, t.usernameFor(member)),
			reason,
		)
	}
}

func getOriginalName(channel SlackChannel) string {
	if channel.Name == "" {
		return channel.Id
	} else {
		return channel.Name
	}
}

// TransformChannels converts SlackChannel records of a single source kind into
// IntermediateChannel records. kind is what the channels were in the Slack
// export, which is what the report counts them as even when a channel changes
// type on the way in (an oversized MPIM becomes a private channel but is still
// reported as a group channel).
func (t *Transformer) TransformChannels(channels []SlackChannel, kind intermediate.EntityKind) []*IntermediateChannel {
	entity := t.Report.For(kind)

	resultChannels := []*IntermediateChannel{}
	for _, channel := range channels {
		// Capture the source name before the oversized-MPIM branch below
		// overwrites channel.Name with the channel purpose. OriginalName is what
		// routes this channel's posts to it (see buildChannelsByOriginalNameMap),
		// so it has to stay the name the export used, or every post in an
		// oversized MPIM is dropped as channel_not_found.
		originalName := getOriginalName(channel)

		validMembers := t.filterValidMembers(channel, channel.Members, t.Intermediate.UsersById)
		if (channel.Type == model.ChannelTypeDirect || channel.Type == model.ChannelTypeGroup) && len(validMembers) <= 1 {
			entity.Skip(channel.Id, originalName, ReasonDMSingleMember)
			t.skipRemainingMembers(channel.Id, originalName, validMembers, intermediate.ReasonMembershipChannelSkipped)
			continue
		}

		if channel.Type == model.ChannelTypeGroup && len(validMembers) > model.ChannelGroupMaxUsers {
			entity.Note(channel.Id, originalName, intermediate.ReasonMPIMConvertedToPrivate,
				strconv.Itoa(len(validMembers)))
			channel.Name = channel.Purpose.Value
			channel.Type = model.ChannelTypePrivate
		}

		name := SlackConvertChannelName(channel.Name, channel.Id)
		newChannel := &IntermediateChannel{
			Id:           channel.Id,
			OriginalName: originalName,
			Name:         name,
			DisplayName:  channel.Name,
			Members:      validMembers,
			Purpose:      channel.Purpose.Value,
			Header:       channel.Topic.Value,
			Type:         channel.Type,
			Created:      normalizeSlackCreated(channel.Created),
			ReportKind:   kind,
		}

		// Public and private channels support DeletedAt in the Mattermost import
		// format. Direct and group channels use a separate import type (direct_channel)
		// that has no DeletedAt field, so archiving those is not supported. Oversized
		// MPIMs are rewritten to ChannelTypePrivate above, so they're eligible here.
		if channel.IsArchived && channel.Type != model.ChannelTypeDirect && channel.Type != model.ChannelTypeGroup {
			if channel.Updated > 0 {
				// Use the Slack "updated" timestamp (already in milliseconds) as a
				// best-effort approximation of the archive time. Slack exports do not
				// include a dedicated archive timestamp.
				newChannel.DeleteAt = channel.Updated
			} else {
				entity.Note(channel.Id, originalName, ReasonArchivedNoTimestamp)
				newChannel.DeleteAt = model.GetMillis()
			}
		}

		newChannel.SanitiseWithPrefix(entity, "slack-channel-")
		resultChannels = append(resultChannels, newChannel)
	}

	return resultChannels
}

func (t *Transformer) PopulateUserMemberships() {
	t.Logger.Info("Populating user memberships")

	for userId, user := range t.Intermediate.UsersById {
		if user.IsBot {
			continue
		}
		var memberships []IntermediateMembership
		for _, channel := range t.Intermediate.PublicChannels {
			for _, memberId := range channel.Members {
				if userId == memberId {
					memberships = append(memberships, IntermediateMembership{
						Name: channel.Name,
					})
					break
				}
			}
		}
		for _, channel := range t.Intermediate.PrivateChannels {
			for _, memberId := range channel.Members {
				if userId == memberId {
					memberships = append(memberships, IntermediateMembership{
						Name: channel.Name,
					})
					break
				}
			}
		}
		user.Memberships = memberships
	}
}

// applyChannelStatsToMemberships updates each user's channel memberships
// with read-state values computed from post stats. Uses channel.LastPostAt
// for LastViewedAt (falling back to CreatedMillis for channels with no posts).
func (t *Transformer) applyChannelStatsToMemberships() {
	channelsByName := make(map[string]*IntermediateChannel)
	for _, ch := range t.Intermediate.PublicChannels {
		channelsByName[ch.Name] = ch
	}
	for _, ch := range t.Intermediate.PrivateChannels {
		channelsByName[ch.Name] = ch
	}

	// Cache fallback values and warn once per channel, not once per member.
	fallbackByChannel := make(map[string]int64)
	for _, user := range t.Intermediate.UsersById {
		for i, m := range user.Memberships {
			ch := channelsByName[m.Name]
			if ch == nil {
				continue
			}
			if ch.LastPostAt > 0 {
				user.Memberships[i].LastViewedAt = ch.LastPostAt
				user.Memberships[i].MsgCount = ch.MsgCount
				user.Memberships[i].MsgCountRoot = ch.MsgCountRoot
			} else {
				fb, ok := fallbackByChannel[ch.Name]
				if !ok {
					// Slack placeholder Created values are normalized to 0 (absent) at
					// construction
					if ch.Created <= 0 {
						ch.ReportEntity(t.Report).
							Note(ch.ReportID(), ch.ReportName(), intermediate.ReasonChannelNoCreatedTimestamp)
					}
					fb = ch.CreatedMillis()
					fallbackByChannel[ch.Name] = fb
				}
				user.Memberships[i].LastViewedAt = fb
			}
		}
	}
}

func (t *Transformer) PopulateChannelMemberships() {
	t.Logger.Info("Populating channel memberships")

	for _, channel := range t.Intermediate.GroupChannels {
		members := []string{}
		for _, memberId := range channel.Members {
			if user, ok := t.Intermediate.UsersById[memberId]; ok {
				members = append(members, user.Username)
			}
		}

		channel.MembersUsernames = members
	}
	for _, channel := range t.Intermediate.DirectChannels {
		members := []string{}
		for _, memberId := range channel.Members {
			if user, ok := t.Intermediate.UsersById[memberId]; ok {
				members = append(members, user.Username)
			}
		}

		channel.MembersUsernames = members
	}
}

// DeduplicateDirectAndGroupChannelsByMembers collapses group/direct channels that share
// the same member set (per directChannelKey) into a single canonical
// IntermediateChannel. Slack allows multiple MPIMs with identical members but
// Mattermost cannot represent that — emitting two `direct_channel` lines with
// the same hash crashes the bulk importer (see MM-68736).
//
// The canonical channel is the one with the lexicographically smallest Slack
// Id, so the choice is deterministic across runs. Topic/Header/Purpose fall
// back to the first non-empty value in canonical order; Created is the minimum.
// Post-derived stats are left zero and recomputed by ComputeChannelPostStats
// after dedup.
//
// Each dropped duplicate's OriginalName is added to GroupChannelAliases so
// buildChannelsByOriginalNameMap routes posts from every colliding Slack
// channel name to the canonical IntermediateChannel.
//
// Must run after PopulateChannelMemberships (the key uses MembersUsernames)
// and before TransformPosts (so post lookup sees the deduplicated slices).
func (t *Transformer) DeduplicateDirectAndGroupChannelsByMembers() {
	t.Logger.Info("Deduplicating group and direct channels by member set")

	// Reset on every run so a reused Transformer doesn't carry aliases from
	// a previous Transform() over to the current one — buildChannelsByOriginalNameMap
	// would otherwise overlay stale Slack channel names onto the new export.
	t.Intermediate.GroupChannelAliases = map[string]string{}

	t.Intermediate.GroupChannels = t.dedupByMembers(t.Intermediate.GroupChannels)
	t.Intermediate.DirectChannels = t.dedupByMembers(t.Intermediate.DirectChannels)
}

func (t *Transformer) dedupByMembers(channels []*IntermediateChannel) []*IntermediateChannel {
	if len(channels) <= 1 {
		return channels
	}

	groups := map[string][]*IntermediateChannel{}
	keyOrder := []string{}
	for _, ch := range channels {
		if len(ch.MembersUsernames) == 0 {
			// Defensive: a channel with no resolvable members can't be keyed and
			// shouldn't be merged with anything. Keep it as its own bucket using
			// the Slack Id so it survives untouched.
			key := "__noMembers__" + ch.Id
			groups[key] = append(groups[key], ch)
			keyOrder = append(keyOrder, key)
			continue
		}
		key := directChannelKey(ch.MembersUsernames)
		if _, seen := groups[key]; !seen {
			keyOrder = append(keyOrder, key)
		}
		groups[key] = append(groups[key], ch)
	}

	result := make([]*IntermediateChannel, 0, len(channels))
	for _, key := range keyOrder {
		bucket := groups[key]
		if len(bucket) == 1 {
			result = append(result, bucket[0])
			continue
		}

		sort.SliceStable(bucket, func(i, j int) bool {
			return bucket[i].Id < bucket[j].Id
		})

		canonical := bucket[0]
		dupSummaries := make([]string, 0, len(bucket)-1)
		for _, dup := range bucket[1:] {
			dupSummaries = append(dupSummaries, fmt.Sprintf("%s (%s)", dup.Id, dup.OriginalName))
			dup.ReportEntity(t.Report).
				Note(dup.ReportID(), dup.ReportName(), ReasonMPIMMerged, canonical.OriginalName)
			if canonical.Topic == "" && dup.Topic != "" {
				canonical.Topic = dup.Topic
			}
			if canonical.Header == "" && dup.Header != "" {
				canonical.Header = dup.Header
			}
			if canonical.Purpose == "" && dup.Purpose != "" {
				canonical.Purpose = dup.Purpose
			}
			// Slack placeholder Created values are normalized to 0 (absent) at
			// construction, so a real timestamp on the canonical isn't replaced
			// by a placeholder from a duplicate.
			canonicalCreatedValid := canonical.Created > 0
			dupCreatedValid := dup.Created > 0
			if dupCreatedValid && (!canonicalCreatedValid || dup.Created < canonical.Created) {
				canonical.Created = dup.Created
			}
			t.Intermediate.GroupChannelAliases[dup.OriginalName] = canonical.OriginalName
		}
		t.Logger.Infof("Merged %d duplicate channel(s) into canonical %s (%s) keyed by members=%s: [%s]",
			len(dupSummaries), canonical.Id, canonical.OriginalName, key, strings.Join(dupSummaries, ", "))
		result = append(result, canonical)
	}

	return result
}

func (t *Transformer) TransformAllChannels(slackExport *SlackExport) error {
	t.Logger.Info("Transforming channels")

	// Count each source collection once, before anything is dropped or
	// reclassified, so Transformed can be derived from what the export held.
	t.Report.PublicChannels().Seen(len(slackExport.PublicChannels))
	t.Report.PrivateChannels().Seen(len(slackExport.PrivateChannels))
	t.Report.GroupChannels().Seen(len(slackExport.GroupChannels))
	t.Report.DirectChannels().Seen(len(slackExport.DirectChannels))

	// transform public
	t.Intermediate.PublicChannels = t.TransformChannels(slackExport.PublicChannels, intermediate.EntityPublicChannel)

	// transform private
	t.Intermediate.PrivateChannels = t.TransformChannels(slackExport.PrivateChannels, intermediate.EntityPrivateChannel)

	// transform group
	regularGroupChannels, bigGroupChannels := SplitChannelsByMemberSize(slackExport.GroupChannels, model.ChannelGroupMaxUsers, t.Report)

	// Oversized MPIMs are imported as private channels but stay group channels
	// as far as the report is concerned, since that is what they were in Slack.
	t.Intermediate.PrivateChannels = append(t.Intermediate.PrivateChannels, t.TransformChannels(bigGroupChannels, intermediate.EntityGroupChannel)...)

	t.Intermediate.GroupChannels = t.TransformChannels(regularGroupChannels, intermediate.EntityGroupChannel)

	// transform direct
	t.Intermediate.DirectChannels = t.TransformChannels(slackExport.DirectChannels, intermediate.EntityDirectChannel)

	return nil
}

// dropChannellessGuests removes guests that ended up with no public/private
// channel membership. Mattermost's bulk-import format can only express a
// guest's channel access through public/private channel membership
// (UserTeamImportData.Channels) — a guest present only in a DM/MPIM has no
// such scope and cannot be validly imported as a guest. Silently promoting
// them to a full member instead would defeat the point of this feature, so
// they — and any memberships/posts referencing them — are dropped instead,
// same as --guest-handling=skip but for this user only. Only runs in "guest"
// mode: "user" mode always imports guests as full members regardless of
// channel access, and "skip" mode has already dropped them in TransformUsers.
//
// Must run after TransformAllChannels (so PublicChannels/PrivateChannels
// membership is known) and before PopulateChannelMemberships/TransformPosts
// (so the drop cascades to DM/group membership and authored content).
func (t *Transformer) dropChannellessGuests(guestHandling string) {
	if guestHandling != GuestHandlingGuest {
		return
	}

	hasChannelAccess := map[string]bool{}
	for _, channel := range t.Intermediate.PublicChannels {
		for _, memberId := range channel.Members {
			hasChannelAccess[memberId] = true
		}
	}
	for _, channel := range t.Intermediate.PrivateChannels {
		for _, memberId := range channel.Members {
			hasChannelAccess[memberId] = true
		}
	}

	skipped := 0
	for id, user := range t.Intermediate.UsersById {
		if !user.IsGuest || hasChannelAccess[id] {
			continue
		}
		t.Report.Users().Skip(id, user.Username, intermediate.ReasonGuestNoChannel)
		t.markUserSkipped(id, user.Username)
		delete(t.Intermediate.UsersById, id)
		skipped++
	}

	if skipped == 0 {
		return
	}
	t.Logger.Infof("Skipped %d guest user(s) with no channel to scope their guest access to", skipped)

	t.Intermediate.GroupChannels = t.dropSkippedFromDirectOrGroupChannels(t.Intermediate.GroupChannels, intermediate.EntityGroupChannel)
	t.Intermediate.DirectChannels = t.dropSkippedFromDirectOrGroupChannels(t.Intermediate.DirectChannels, intermediate.EntityDirectChannel)
}

// dropSkippedFromDirectOrGroupChannels removes now-skipped members from each
// channel's Members list, dropping the whole channel if fewer than 2 members
// remain, mirroring the single-member check in TransformChannels.
func (t *Transformer) dropSkippedFromDirectOrGroupChannels(channels []*IntermediateChannel, kind intermediate.EntityKind) []*IntermediateChannel {
	entity := t.Report.For(kind)
	memberships := t.Report.ChannelMemberships()

	result := make([]*IntermediateChannel, 0, len(channels))
	for _, channel := range channels {
		remaining := []string{}
		for _, memberId := range channel.Members {
			if t.skippedUserIDs[memberId] {
				// Not counted as Seen again: these members were already counted
				// when the source channel's member list was first read in
				// filterValidMembers.
				memberships.Skip(
					intermediate.MembershipID(channel.ReportID(), memberId),
					intermediate.MembershipID(channel.OriginalName, t.usernameFor(memberId)),
					intermediate.ReasonMembershipSkippedUser,
				)
				continue
			}
			remaining = append(remaining, memberId)
		}
		channel.Members = remaining

		if len(remaining) <= 1 {
			entity.Skip(channel.ReportID(), channel.OriginalName, ReasonDMSingleMember)
			t.skipRemainingMembers(channel.ReportID(), channel.OriginalName, remaining, intermediate.ReasonMembershipChannelSkipped)
			continue
		}
		result = append(result, channel)
	}
	return result
}

// AddPostToThreads places post into its thread within threads. It returns false
// (placing nothing) when the post is a reply whose thread root was never
// imported — e.g. a skipped guest authored the root, so the root was dropped.
// In that case the whole thread is skipped consistently and the caller counts
// the dropped reply. It returns true when the post is placed (as a thread root
// or as a reply under an existing root).
func AddPostToThreads(original SlackPost, post *IntermediatePost, threads map[string]*IntermediatePost, channel *IntermediateChannel, timestamps map[int64]bool) bool {
	// direct and group posts need the channel members in the import line
	if channel.Type == model.ChannelTypeDirect || channel.Type == model.ChannelTypeGroup {
		post.IsDirect = true
		post.ChannelMembers = channel.MembersUsernames
	} else {
		post.IsDirect = false
	}

	// avoid timestamp duplications
	for {
		// if the timestamp hasn't been used already, break and use
		if _, ok := timestamps[post.CreateAt]; !ok {
			break
		}
		post.CreateAt++
	}
	timestamps[post.CreateAt] = true

	// if post is part of a thread
	if original.ThreadTS != "" && original.ThreadTS != original.TimeStamp {
		rootPost, ok := threads[original.ThreadTS]
		if !ok {
			// The thread root was never imported — e.g. its author was a
			// skipped guest, so the root post was dropped. Drop this reply too
			// so the whole thread is skipped consistently. The caller (via
			// addPostToThreads) records and surfaces the drop.
			return false
		}
		rootPost.Replies = append(rootPost.Replies, post)
		return true
	}

	// if post is the root of a thread
	if original.TimeStamp == original.ThreadTS {
		threads[original.ThreadTS] = post
		return true
	}

	threads[original.TimeStamp] = post
	return true
}

// threadRootKey returns the key a non-reply post occupies in the threads map,
// which is its own timestamp whether or not it declares a thread.
func threadRootKey(original SlackPost) string {
	if original.ThreadTS != "" && original.TimeStamp == original.ThreadTS {
		return original.ThreadTS
	}
	return original.TimeStamp
}

// isThreadReply reports whether a Slack post is a reply in someone else's thread.
func isThreadReply(original SlackPost) bool {
	return original.ThreadTS != "" && original.ThreadTS != original.TimeStamp
}

// addPostToThreads wraps AddPostToThreads, recording the outcome in the report.
// AddPostToThreads only returns false when a reply's thread root was never
// imported (e.g. the root's author was a skipped guest), so a false return here
// always means "reply dropped along with its thread": the reply is named under
// posts and the thread itself is skipped once, however many replies it had.
func (t *Transformer) addPostToThreads(original SlackPost, post *IntermediatePost, threads map[string]*IntermediatePost, channel *IntermediateChannel, timestamps map[int64]bool) {
	// A non-reply overwrites whatever occupies its key, so the post that was
	// there is lost. Name the displaced post before AddPostToThreads replaces it.
	if rootKey := threadRootKey(original); !isThreadReply(original) && threads[rootKey] != nil {
		t.Report.Posts().Skip(intermediate.PostID(channel.OriginalName, rootKey), threads[rootKey].User, ReasonPostDuplicateTimestamp)
	}

	placed := AddPostToThreads(original, post, threads, channel, timestamps)

	if isThreadReply(original) {
		t.accountForThread(channel, original.ThreadTS, !placed)
	}
	if placed {
		return
	}
	t.skipPost(channel, original, post.User, intermediate.ReasonThreadRootMissing)
}

// accountForThread counts a thread the first time one of its replies is seen,
// and skips it once when its root was never imported. Doing it here rather than
// per reply keeps a thread with hundreds of replies to a single report entry.
func (t *Transformer) accountForThread(channel *IntermediateChannel, threadTS string, dropped bool) {
	key := threadKey(channel.OriginalName, threadTS)
	if t.reportedThreads == nil {
		t.reportedThreads = map[string]bool{}
	}
	if t.reportedThreads[key] {
		return
	}
	t.reportedThreads[key] = true

	t.Report.Threads().Seen(1)
	if dropped {
		// Name the author of the root that never made it, not one of the
		// replies: they are the reason the whole thread is missing.
		t.Report.Threads().Skip(intermediate.PostID(channel.OriginalName, threadTS), t.droppedRootAuthors[key], intermediate.ReasonThreadRootMissing)
	}
}

// threadKey is the per-channel identity of a thread, used for the maps that
// keep a thread to one report entry however many replies it has.
func threadKey(channelName, threadTS string) string {
	return channelName + "\x00" + threadTS
}

// skipPost records a dropped source message. When the message would have been a
// thread root it also remembers its author, so a thread later dropped for want
// of that root can name who it belonged to.
func (t *Transformer) skipPost(channel *IntermediateChannel, post SlackPost, author string, reason *intermediate.Reason, args ...string) {
	t.Report.Posts().Skip(intermediate.PostID(channel.OriginalName, post.TimeStamp), author, reason, args...)

	if isThreadReply(post) {
		return
	}
	if t.droppedRootAuthors == nil {
		t.droppedRootAuthors = map[string]string{}
	}
	t.droppedRootAuthors[threadKey(channel.OriginalName, post.TimeStamp)] = author
}

func buildChannelsByOriginalNameMap(intermediate *Intermediate) map[string]*IntermediateChannel {
	channelsByName := map[string]*IntermediateChannel{}
	for _, channel := range intermediate.PublicChannels {
		channelsByName[channel.OriginalName] = channel
	}
	for _, channel := range intermediate.PrivateChannels {
		channelsByName[channel.OriginalName] = channel
	}
	for _, channel := range intermediate.GroupChannels {
		channelsByName[channel.OriginalName] = channel
	}
	for _, channel := range intermediate.DirectChannels {
		channelsByName[channel.OriginalName] = channel
	}
	for alias, canonical := range intermediate.GroupChannelAliases {
		if ch, ok := channelsByName[canonical]; ok {
			channelsByName[alias] = ch
		}
	}
	return channelsByName
}

func getNormalisedFilePath(file *SlackFile, attachmentsDir string) string {
	n := makeAlphaNum(file.Name, '.', '-', '_')
	p := path.Join(attachmentsDir, fmt.Sprintf("%s_%s", file.Id, n))
	return norm.NFC.String(p)
}

func addFileToPost(file *SlackFile, uploads map[string]*zip.File, post *IntermediatePost, attachmentsDir string, allowDownload bool) error {
	if _, ok := uploads[file.Id]; ok || !allowDownload {
		return addZipFileToPost(file, uploads, post, attachmentsDir)
	}

	return addDownloadToPost(file, post, attachmentsDir)
}

func addDownloadToPost(file *SlackFile, post *IntermediatePost, attachmentsDir string) error {
	destFilePath := getNormalisedFilePath(file, attachmentsInternal)
	fullFilePath := path.Join(attachmentsDir, destFilePath)

	log.Printf("Downloading %q into %q...\n", file.DownloadURL, destFilePath)

	err := downloadInto(fullFilePath, file.DownloadURL, file.Size)
	if err != nil {
		return err
	}

	log.Println("Download successful!")

	post.Attachments = append(post.Attachments, destFilePath)
	return nil
}

var sizes = []string{"KiB", "MiB", "GiB", "TiB", "PiB"}

func humanSize(size int64) string {
	if size < 0 {
		return "unknown"
	}
	if size < 1024 {
		return fmt.Sprintf("%d B", size)
	}

	limit := int64(1024 * 1024)
	for _, name := range sizes {
		if size < limit {
			return fmt.Sprintf("%.2f %s", float64(size)/float64(limit/1024), name)
		}

		limit *= 1024
	}

	return fmt.Sprintf("%.2f %s", float64(size)/float64(limit/1024), sizes[len(sizes)-1])
}

func addZipFileToPost(file *SlackFile, uploads map[string]*zip.File, post *IntermediatePost, attachmentsDir string) error {
	zipFile, ok := uploads[file.Id]
	if !ok {
		return errors.Errorf("failed to retrieve file with id %s", file.Id)
	}

	zipFileReader, err := zipFile.Open()
	if err != nil {
		return errors.Wrapf(err, "failed to open attachment from zipfile for id %s", file.Id)
	}
	defer zipFileReader.Close()

	destFilePath := getNormalisedFilePath(file, attachmentsInternal)
	destFile, err := os.Create(path.Join(attachmentsDir, destFilePath))
	if err != nil {
		return errors.Wrapf(err, "failed to create file %s in the attachments directory", file.Id)
	}
	defer destFile.Close()

	_, err = io.Copy(destFile, zipFileReader)
	if err != nil {
		return errors.Wrapf(err, "failed to create file %s in the attachments directory", file.Id)
	}

	log.Printf("SUCCESS COPYING FILE %s TO DEST %s", file.Id, destFilePath)

	post.Attachments = append(post.Attachments, destFilePath)

	return nil
}

func (t *Transformer) CreateIntermediateUser(userID string) {
	newUser := &IntermediateUser{
		Id:        userID,
		Username:  strings.ToLower(userID),
		FirstName: "Deleted",
		LastName:  "User",
		Email:     fmt.Sprintf("%s@local", userID),
		Password:  model.NewId(),
	}
	t.Intermediate.UsersById[userID] = newUser
	// The placeholder is not a source entity, but it does reach the import
	// file, so it is counted as one to keep Transformed consistent.
	t.Report.Users().Seen(1)
	t.Report.Users().Note(userID, newUser.Username, intermediate.ReasonUserPlaceholderCreated)
}

func (t *Transformer) CreateIntermediateBotUser(userID string) {
	newUser := &IntermediateUser{
		Id:          userID,
		Username:    strings.ToLower(userID),
		DisplayName: "Unknown Bot",
		IsBot:       true,
	}
	t.Intermediate.UsersById[userID] = newUser
	t.Report.Bots().Seen(1)
	t.Report.Bots().Note(userID, newUser.Username, intermediate.ReasonUserPlaceholderCreated)
}

func (t *Transformer) CreateAndAddPostToThreads(post SlackPost, threads map[string]*IntermediatePost, timestamps map[int64]bool, channel *IntermediateChannel) {
	if t.isSkippedUser(post.User) {
		t.skipPost(channel, post, t.usernameFor(post.User), intermediate.ReasonPostSkippedAuthor)
		return
	}

	author := t.Intermediate.UsersById[post.User]
	if author == nil {
		t.CreateIntermediateUser(post.User)
		author = t.Intermediate.UsersById[post.User]
	}

	newPost := &IntermediatePost{
		User:      author.Username,
		Channel:   channel.Name,
		Message:   post.Text,
		Reactions: t.getReactionsFromPost(post, channel),
		CreateAt:  SlackConvertTimeStamp(post.TimeStamp),
	}

	t.addPostToThreads(post, newPost, threads, channel, timestamps)
}

func (t *Transformer) AddFilesToPost(post *SlackPost, skipAttachments bool, slackExport *SlackExport, attachmentsDir string, newPost *IntermediatePost, allowDownload bool) {
	if skipAttachments || (post.File == nil && post.Files == nil) {
		return
	}

	files := collectPostFiles(post)

	entity := t.Report.Files()
	entity.Seen(len(files))
	for _, file := range files {
		if file.Name == "" {
			entity.Skip(file.Id, "", ReasonFileAccessDenied)
			continue
		}
		if t.DryRun {
			t.verifySlackAttachment(file, slackExport.Uploads, allowDownload)
			continue
		}
		if err := addFileToPost(file, slackExport.Uploads, newPost, attachmentsDir, allowDownload); err != nil {
			entity.Skip(file.Id, file.Name, ReasonFileAddFailed, fileAddFailureReason(err))
		}
	}
}

// collectPostFiles returns distinct files from the legacy File field and the
// Files slice, deduplicated by Slack file ID. File is listed first when set.
func collectPostFiles(post *SlackPost) []*SlackFile {
	seen := make(map[string]struct{})
	var files []*SlackFile
	add := func(f *SlackFile) {
		if f == nil {
			return
		}
		if _, ok := seen[f.Id]; ok {
			return
		}
		seen[f.Id] = struct{}{}
		files = append(files, f)
	}
	add(post.File)
	for _, f := range post.Files {
		add(f)
	}
	return files
}

func fileAddFailureReason(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "failed to retrieve file"):
		return "not present in the export zip"
	case strings.Contains(msg, "HTTP request") || strings.Contains(msg, "download:"):
		return "download failed"
	default:
		return "could not write attachment"
	}
}

func (t *Transformer) verifySlackAttachment(file *SlackFile, uploads map[string]*zip.File, allowDownload bool) {
	if file.Name == "" {
		t.Report.Files().Skip(file.Id, "", ReasonFileAccessDenied)
		return
	}
	if _, ok := uploads[file.Id]; ok {
		return
	}
	if allowDownload {
		t.Logger.Warnf("Attachment %s is not in the zip; --allow-download would fetch it at transform time", file.Id)
		return
	}
	t.RecordError(fmt.Errorf("failed to retrieve file with id %s", file.Id))
}

func (t *Transformer) AddAttachmentsToPost(post *SlackPost, newPost *IntermediatePost) (model.StringInterface, []byte) {
	props := model.StringInterface{"attachments": post.Attachments}
	propsByteArray, _ := json.Marshal(props)
	return props, propsByteArray
}

func buildMessagePropsFromHuddle(post *SlackPost) model.StringInterface {
	type Attachment struct {
		ID       int    `json:"id"`
		Text     string `json:"text"`
		Fallback string `json:"fallback"`
	}

	type MessageProps struct {
		Title       string       `json:"title"`
		EndAt       int64        `json:"end_at"`
		StartAt     int64        `json:"start_at"`
		Attachments []Attachment `json:"attachments"`
		FromPlugin  bool         `json:"from_plugin"`
	}

	props := MessageProps{
		Title: "",
		Attachments: []Attachment{{
			ID:       0,
			Text:     "Call ended",
			Fallback: "Call ended",
		}},
		FromPlugin: true,
		EndAt:      0,
		StartAt:    0,
	}

	if post.Room != nil {
		props.EndAt = post.Room.DateEnd * 1000
		props.StartAt = post.Room.DateStart * 1000
	}

	propsMap := make(map[string]any)
	bytes, _ := json.Marshal(props)
	_ = json.Unmarshal(bytes, &propsMap)

	return propsMap
}

func (t *Transformer) getReactionsFromPost(post SlackPost, channel *IntermediateChannel) []*IntermediateReaction {
	entity := t.Report.Reactions()

	reactions := []*IntermediateReaction{}
	for _, reaction := range post.Reactions {
		entity.Seen(len(reaction.Users))
		for _, reactionUser := range reaction.Users {
			if t.isSkippedUser(reactionUser) {
				entity.Skip(
					intermediate.ReactionID(channel.OriginalName, post.TimeStamp, reactionUser, reaction.Name),
					t.usernameFor(reactionUser),
					intermediate.ReasonReactionSkippedUser,
				)
				continue
			}
			reactionAuthor := t.Intermediate.UsersById[reactionUser]
			if reactionAuthor == nil {
				t.CreateIntermediateUser(reactionUser)
				reactionAuthor = t.Intermediate.UsersById[reactionUser]
			}
			var cleanedReactionName = reaction.Name
			if strings.Contains(reaction.Name, "::") {
				cleanedReactionName = strings.Split(reaction.Name, "::")[0]
			}
			newReaction := &IntermediateReaction{
				User:      reactionAuthor.Username,
				EmojiName: t.SanitizeEmojiName(cleanedReactionName),
				CreateAt:  SlackConvertTimeStamp(post.TimeStamp) + 1,
				// we don't have the real createAt available, so we pretend that reactions were created shortly after the post,
				// to avoid validation errors at import time:
				// BulkImport: Reaction CreateAt property must be greater than the parent post CreateAt.
			}
			reactions = append(reactions, newReaction)
		}
	}
	return reactions
}

func (t *Transformer) TransformPosts(slackExport *SlackExport, attachmentsDir string, skipAttachments, discardInvalidProps, allowDownload bool) error {
	t.Logger.Info("Transforming posts")

	newGroupChannels := []*IntermediateChannel{}
	newDirectChannels := []*IntermediateChannel{}
	channelsByOriginalName := buildChannelsByOriginalNameMap(t.Intermediate)

	// Count every source message once, up front: posts are the one kind whose
	// call sites drop entries from several different places, so deriving
	// Transformed from the source total is the only count that stays honest.
	posts := t.Report.Posts()
	for _, channelPosts := range slackExport.Posts {
		posts.Seen(len(channelPosts))
	}

	resultPosts := []*IntermediatePost{}
	for originalChannelName, channelPosts := range slackExport.Posts {
		channel, ok := channelsByOriginalName[originalChannelName]
		if !ok {
			for _, post := range channelPosts {
				posts.Skip(intermediate.PostID(originalChannelName, post.TimeStamp), t.usernameFor(post.User), ReasonChannelNotFound, originalChannelName)
			}
			delete(slackExport.Posts, originalChannelName)
			continue
		}

		timestamps := make(map[int64]bool)
		sort.Slice(channelPosts, func(i, j int) bool {
			return SlackConvertTimeStamp(channelPosts[i].TimeStamp) < SlackConvertTimeStamp(channelPosts[j].TimeStamp)
		})
		threads := map[string]*IntermediatePost{}

		for _, post := range channelPosts {
			switch {
			// bot message (checked first since bots can have any subtype)
			case post.IsBotMessage():
				botId := post.BotId
				if botId == "" {
					// Some Slack exports have subtype "bot_message" but no BotId.
					// Fall back to User field, then BotUsername, then generate a placeholder.
					switch {
					case post.User != "":
						botId = post.User
					case post.BotUsername != "":
						botId = post.BotUsername
					default:
						botId = "unknown-bot-" + post.TimeStamp
					}
				}

				author := t.Intermediate.UsersById[botId]
				if author == nil {
					t.CreateIntermediateBotUser(botId)
					author = t.Intermediate.UsersById[botId]
				}

				newPost := &IntermediatePost{
					User:      author.Username,
					Channel:   channel.Name,
					Message:   post.Text,
					Reactions: t.getReactionsFromPost(post, channel),
					CreateAt:  SlackConvertTimeStamp(post.TimeStamp),
				}

				t.AddFilesToPost(&post, skipAttachments, slackExport, attachmentsDir, newPost, allowDownload)

				if len(post.Attachments) > 0 {
					props, propsB := t.AddAttachmentsToPost(&post, newPost)
					if utf8.RuneCount(propsB) <= model.PostPropsMaxRunes {
						newPost.Props = props
					} else {
						if discardInvalidProps {
							t.skipPost(channel, post, author.Username, ReasonPostPropsTooLarge)
							continue
						}
						posts.Note(intermediate.PostID(channel.OriginalName, post.TimeStamp), author.Username, ReasonPostPropsDropped)
					}
				}

				t.addPostToThreads(post, newPost, threads, channel, timestamps)

			// plain message that can have files attached
			case post.IsPlainMessage():
				if post.User == "" {
					t.skipPost(channel, post, "", ReasonPostNoUser)
					continue
				}
				if t.isSkippedUser(post.User) {
					t.skipPost(channel, post, t.usernameFor(post.User), intermediate.ReasonPostSkippedAuthor)
					continue
				}
				author := t.Intermediate.UsersById[post.User]
				if author == nil {
					t.CreateIntermediateUser(post.User)
					author = t.Intermediate.UsersById[post.User]
				}
				newPost := &IntermediatePost{
					User:      author.Username,
					Channel:   channel.Name,
					Message:   post.Text,
					Reactions: t.getReactionsFromPost(post, channel),
					CreateAt:  SlackConvertTimeStamp(post.TimeStamp),
				}
				t.AddFilesToPost(&post, skipAttachments, slackExport, attachmentsDir, newPost, allowDownload)

				if len(post.Attachments) > 0 {
					props, propsB := t.AddAttachmentsToPost(&post, newPost)
					if utf8.RuneCount(propsB) <= model.PostPropsMaxRunes {
						newPost.Props = props
					} else {
						if discardInvalidProps {
							t.skipPost(channel, post, author.Username, ReasonPostPropsTooLarge)
							continue
						}
						posts.Note(intermediate.PostID(channel.OriginalName, post.TimeStamp), author.Username, ReasonPostPropsDropped)
					}
				}

				t.addPostToThreads(post, newPost, threads, channel, timestamps)

			// file comment
			case post.IsFileComment():
				if post.Comment == nil {
					t.skipPost(channel, post, t.usernameFor(post.User), ReasonPostNoComments)
					continue
				}
				if post.Comment.User == "" {
					t.skipPost(channel, post, "", ReasonPostNoUser)
					continue
				}
				if t.isSkippedUser(post.Comment.User) {
					t.skipPost(channel, post, t.usernameFor(post.Comment.User), intermediate.ReasonPostSkippedAuthor)
					continue
				}
				author := t.Intermediate.UsersById[post.Comment.User]
				if author == nil {
					t.CreateIntermediateUser(post.User)
					author = t.Intermediate.UsersById[post.User]
				}
				newPost := &IntermediatePost{
					User:      author.Username,
					Channel:   channel.Name,
					Message:   post.Comment.Comment,
					Reactions: t.getReactionsFromPost(post, channel),
					CreateAt:  SlackConvertTimeStamp(post.TimeStamp),
				}

				t.addPostToThreads(post, newPost, threads, channel, timestamps)

			// channel join/leave messages
			case post.IsJoinLeaveMessage():
				if post.User == "" {
					t.skipPost(channel, post, "", ReasonPostNoUser)
					continue
				}

				t.CreateAndAddPostToThreads(post, threads, timestamps, channel)

			// me message
			case post.IsMeMessage():
				if post.User == "" {
					t.skipPost(channel, post, "", ReasonPostNoUser)
					continue
				}
				t.CreateAndAddPostToThreads(post, threads, timestamps, channel)

			// change topic message
			case post.IsChannelTopicMessage():
				if post.User == "" {
					t.skipPost(channel, post, "", ReasonPostNoUser)
					continue
				}
				t.CreateAndAddPostToThreads(post, threads, timestamps, channel)

			// change channel purpose message
			case post.IsChannelPurposeMessage():
				if post.User == "" {
					t.skipPost(channel, post, "", ReasonPostNoUser)
					continue
				}
				t.CreateAndAddPostToThreads(post, threads, timestamps, channel)

			// change channel name message
			case post.IsChannelNameMessage():
				if post.User == "" {
					t.skipPost(channel, post, "", ReasonPostNoUser)
					continue
				}
				t.CreateAndAddPostToThreads(post, threads, timestamps, channel)

			// Huddle thread
			case post.isHuddleThread():
				post.Text = "Call ended"
				if post.User == "" {
					t.skipPost(channel, post, "", ReasonPostNoUser)
					continue
				}

				// all huddles are owned by USLACKBOT, but the room has a CreatedBy prop.
				// this lets us get the actual user who created the huddle and fit with how Mattermost works.
				poster := post.User
				if post.Room != nil && len(post.Room.CreatedBy) > 0 {
					poster = post.Room.CreatedBy
				}

				if t.isSkippedUser(poster) {
					t.skipPost(channel, post, t.usernameFor(poster), intermediate.ReasonPostSkippedAuthor)
					continue
				}

				author := t.Intermediate.UsersById[poster]
				if author == nil {
					t.CreateIntermediateUser(poster)
					author = t.Intermediate.UsersById[poster]
				}

				huddleProps := buildMessagePropsFromHuddle(&post)

				newPost := &IntermediatePost{
					User:      author.Username,
					Channel:   channel.Name,
					Message:   post.Text,
					Reactions: t.getReactionsFromPost(post, channel),
					CreateAt:  SlackConvertTimeStamp(post.TimeStamp),
					Props:     huddleProps,
					Type:      "custom_calls",
				}

				t.addPostToThreads(post, newPost, threads, channel, timestamps)
			default:
				postType := post.SubType
				if postType == "" {
					postType = post.Type
				}
				t.skipPost(channel, post, t.usernameFor(post.User), ReasonPostUnsupportedType, postType)
			}
		}

		channelPosts := []*IntermediatePost{}
		for threadTS, post := range threads {
			// Split the post if it exceeds the maximum rune limit
			if chunks := intermediate.SplitPostIntoThread(post); chunks > 1 {
				posts.Note(intermediate.PostID(channel.OriginalName, threadTS), post.User, intermediate.ReasonPostSplit, strconv.Itoa(chunks))
			}

			// Also split any existing replies that exceed the limit, which
			// deduplicates their timestamps and re-sorts them too.
			if split := intermediate.SplitOversizedReplies(post); split > 0 {
				posts.Note(intermediate.PostID(channel.OriginalName, threadTS), post.User, intermediate.ReasonPostRepliesSplit, strconv.Itoa(split))
			}

			channelPosts = append(channelPosts, post)
		}
		resultPosts = append(resultPosts, channelPosts...)
		delete(slackExport.Posts, originalChannelName)
	}

	t.Intermediate.Posts = resultPosts
	slackExport.Posts = nil
	t.Intermediate.GroupChannels = append(t.Intermediate.GroupChannels, newGroupChannels...)
	t.Intermediate.DirectChannels = append(t.Intermediate.DirectChannels, newDirectChannels...)

	return nil
}

func (t *Transformer) Transform(slackExport *SlackExport, attachmentsDir string, skipAttachments, discardInvalidProps, allowDownload, skipEmptyEmails bool, defaultEmailDomain string, guestHandling string) error {
	// Guests are exported with Mattermost guest roles only in "guest" mode.
	t.EmitGuestRoles = guestHandling == GuestHandlingGuest

	if err := t.TransformUsers(slackExport.Users, skipEmptyEmails, defaultEmailDomain, guestHandling); err != nil {
		return err
	}

	if err := t.TransformAllChannels(slackExport); err != nil {
		return err
	}

	t.dropChannellessGuests(guestHandling)

	t.PopulateUserMemberships()
	t.PopulateChannelMemberships()
	t.DeduplicateDirectAndGroupChannelsByMembers()

	if err := t.TransformPosts(slackExport, attachmentsDir, skipAttachments, discardInvalidProps, allowDownload); err != nil {
		return err
	}

	t.ComputeChannelPostStats()
	t.applyChannelStatsToMemberships()

	return nil
}

func makeAlphaNum(str string, allowAdditional ...rune) string {
	for match, replace := range specialReplacements {
		str = strings.ReplaceAll(str, match, replace)
	}

	str = norm.NFKD.String(str)
	str = strings.Map(func(r rune) rune {
		for _, allowed := range allowAdditional {
			if r == allowed {
				return r
			}
		}

		// filter all non-ASCII runes
		if r > 127 {
			return -1
		}

		// restrict the remaining characters
		if r >= 'a' && r <= 'z' {
			return r
		}
		if r >= 'A' && r <= 'Z' {
			return r
		}
		if r >= '0' && r <= '9' {
			return r
		}

		return '_'
	}, str)
	return str
}

var specialReplacements = map[string]string{
	"ß": "ss",
}
