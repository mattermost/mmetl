package intermediate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/v8/channels/app/imports"
	log "github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makeAttachments builds n attachments with deterministic, ordered paths so a
// test can assert exactly which attachment landed in which reply.
func makeAttachments(n int) []imports.AttachmentImportData {
	paths := make([]string, n)
	for i := range n {
		paths[i] = fmt.Sprintf("att-%02d", i)
	}
	return GetAttachmentImportDataFromPaths(paths)
}

func TestCreateRepliesForAttachments(t *testing.T) {
	const user = "u1"
	const createAt = int64(1000)

	// The first POST_MAX_ATTACHMENTS attachments stay on the main post (handled
	// by the caller), so the replies must cover exactly attachments[POST_MAX_ATTACHMENTS:]
	// in order, split into chunks of at most POST_MAX_ATTACHMENTS, with no empty reply.
	t.Run("invariants across attachment counts", func(t *testing.T) {
		// Includes exact multiples of POST_MAX_ATTACHMENTS (10, 15, 20) — the
		// off-by-one case that previously emitted a spurious empty trailing reply.
		for _, count := range []int{6, 9, 10, 11, 14, 15, 16, 20} {
			t.Run(fmt.Sprintf("%d attachments", count), func(t *testing.T) {
				attachments := makeAttachments(count)
				replies := createRepliesForAttachments(attachments, user, createAt)

				// Reconstruct the attachments carried by the replies, in order.
				var got []string
				for i, reply := range replies {
					require.NotNil(t, reply.Attachments)
					replyAtt := *reply.Attachments
					assert.NotEmpty(t, replyAtt, "reply %d must not be empty", i)
					assert.LessOrEqual(t, len(replyAtt), POST_MAX_ATTACHMENTS,
						"reply %d exceeds POST_MAX_ATTACHMENTS", i)
					for _, a := range replyAtt {
						got = append(got, *a.Path)
					}
				}

				// The replies must cover exactly the attachments beyond the first
				// POST_MAX_ATTACHMENTS, preserving order, with none lost or duplicated.
				var want []string
				for _, a := range attachments[POST_MAX_ATTACHMENTS:] {
					want = append(want, *a.Path)
				}
				assert.Equal(t, want, got)
			})
		}
	})

	t.Run("exact multiple does not emit an empty trailing reply (regression)", func(t *testing.T) {
		// 10 attachments: 5 stay on the main post, 5 belong in a single reply.
		// numberSplitPosts == 2 used to produce a second, empty reply.
		replies := createRepliesForAttachments(makeAttachments(10), user, createAt)

		require.Len(t, replies, 1)
		require.NotNil(t, replies[0].Attachments)
		assert.Len(t, *replies[0].Attachments, 5)
		assert.Equal(t, createAt+1, *replies[0].CreateAt)
	})

	t.Run("at or below the max yields no replies", func(t *testing.T) {
		assert.Empty(t, createRepliesForAttachments(makeAttachments(POST_MAX_ATTACHMENTS), user, createAt))
		assert.Empty(t, createRepliesForAttachments(makeAttachments(1), user, createAt))
	})
}

// TestGuestWithNoRegularMembershipStaysConsistentInDirectChannels covers the
// interaction between two guest-handling rules that could otherwise
// contradict each other:
//   - GetImportLineFromUser downgrades a guest with zero public/private
//     channel memberships to a regular member, because Mattermost's importer
//     rejects a user that is a guest in only some of system/team/channel scope.
//   - GetImportLineFromDirectChannel (via isEffectiveGuest) must apply the
//     exact same downgrade to that user's DM/group participant entries, or
//     the export would produce a user who is "regular" at the system/team
//     level but "guest" inside a specific DM/MPIM.
func TestGuestWithNoRegularMembershipStaysConsistentInDirectChannels(t *testing.T) {
	guest := &IntermediateUser{
		Id:       "U1",
		Username: "dm-only-guest",
		Email:    "guest@example.com",
		IsGuest:  true,
		// No Memberships: this guest's only Slack access is a DM, never a
		// public/private channel.
	}
	regular := &IntermediateUser{
		Id:       "U2",
		Username: "regular",
		Email:    "regular@example.com",
	}

	exporter := &Exporter{
		TeamName: "myteam",
		Logger:   log.New(),
		Intermediate: &Intermediate{
			UsersById: map[string]*IntermediateUser{
				guest.Id:   guest,
				regular.Id: regular,
			},
		},
		EmitGuestRoles: true,
	}

	// The user line falls back to a regular member: no channel memberships to
	// be a consistent guest in.
	userLine := GetImportLineFromUser(guest, exporter.TeamName, exporter.EmitGuestRoles)
	require.NotNil(t, userLine.User.Roles)
	assert.Equal(t, model.SystemUserRoleId, *userLine.User.Roles)
	team := (*userLine.User.Teams)[0]
	require.NotNil(t, team.Roles)
	assert.Equal(t, model.TeamUserRoleId, *team.Roles)

	// The same user, participating in a DM with the regular user, must also
	// be scheme_user there — not scheme_guest — to match the non-guest
	// system/team roles above.
	dmChannel := &IntermediateChannel{
		Name:             "dm-channel",
		MembersUsernames: []string{guest.Username, regular.Username},
		Created:          1704067200,
	}

	var buf bytes.Buffer
	require.NoError(t, exporter.ExportDirectChannels([]*IntermediateChannel{dmChannel}, &buf))

	var line imports.LineImportData
	require.NoError(t, json.Unmarshal(buf.Bytes(), &line))
	require.NotNil(t, line.DirectChannel)

	participantsByUsername := map[string]*imports.DirectChannelMemberImportData{}
	for _, p := range line.DirectChannel.Participants {
		participantsByUsername[*p.Username] = p
	}

	guestParticipant := participantsByUsername[guest.Username]
	require.NotNil(t, guestParticipant)
	require.NotNil(t, guestParticipant.SchemeUser)
	require.NotNil(t, guestParticipant.SchemeGuest)
	assert.True(t, *guestParticipant.SchemeUser, "downgraded guest must be scheme_user in DMs too")
	assert.False(t, *guestParticipant.SchemeGuest, "downgraded guest must not be scheme_guest in DMs")

	regularParticipant := participantsByUsername[regular.Username]
	require.NotNil(t, regularParticipant)
	assert.True(t, *regularParticipant.SchemeUser)
	assert.False(t, *regularParticipant.SchemeGuest)
}
