package intermediate

import (
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/stretchr/testify/assert"
)

func TestBuildCounts(t *testing.T) {
	inter := &Intermediate{
		PublicChannels:  []*IntermediateChannel{{Name: "town-square"}, {Name: "general"}},
		PrivateChannels: []*IntermediateChannel{{Name: "secret"}},
		GroupChannels:   []*IntermediateChannel{{Name: "mpim1"}},
		UsersById: map[string]*IntermediateUser{
			"u1": {Username: "alice", Memberships: []IntermediateMembership{{Name: "town-square"}, {Name: "general"}}},
			"u2": {Username: "bob", Memberships: []IntermediateMembership{{Name: "town-square"}}},
			"b1": {Username: "botty", IsBot: true},
		},
		Posts: []*IntermediatePost{
			{
				Message:   "root 1",
				Reactions: []*IntermediateReaction{{EmojiName: "+1"}},
				Replies: []*IntermediatePost{
					{Message: "reply 1", Reactions: []*IntermediateReaction{{EmojiName: "eyes"}, {EmojiName: "tada"}}},
					{Message: "reply 2", Attachments: []string{"file1.png"}},
				},
			},
			{Message: "root 2", IsDirect: true, Attachments: []string{"file2.png", "file3.png"}},
		},
	}

	counts := BuildCounts(inter)

	byKey := make(map[string]EntityCount, len(counts))
	for _, c := range counts {
		byKey[c.Key] = c
	}

	// Every row must always be present, even for entities with zero instances
	// (DirectChannel here) — that's what keeps this table diffable against a
	// future import-side summary with the same fixed row list.
	assert.Len(t, counts, 12)
	assert.Contains(t, byKey, EntityDirectChannel)
	assert.Equal(t, 0, byKey[EntityDirectChannel].Transformed)

	assert.Equal(t, 2, byKey[EntityPublicChannel].Transformed)
	assert.Equal(t, 1, byKey[EntityPrivateChannel].Transformed)
	assert.Equal(t, 1, byKey[EntityGroupChannel].Transformed)
	assert.Equal(t, 2, byKey[EntityUser].Transformed)
	assert.Equal(t, 1, byKey[EntityBot].Transformed)
	assert.Equal(t, 1, byKey[EntityPost].Transformed)
	assert.Equal(t, 1, byKey[EntityDirectPost].Transformed)
	assert.Equal(t, 2, byKey[EntityReply].Transformed)
	assert.Equal(t, 3, byKey[EntityReaction].Transformed)          // 1 on the root + 2 on reply 1
	assert.Equal(t, 3, byKey[EntityAttachment].Transformed)        // 1 on reply 2 + 2 on root 2
	assert.Equal(t, 3, byKey[EntityChannelMembership].Transformed) // alice x2 + bob x1

	// Skipped/Failed default to zero until ApplySkipped is called.
	for _, c := range counts {
		assert.Zero(t, c.Skipped, c.Key)
		assert.Zero(t, c.Failed, c.Key)
	}
}

func TestBuildCountsEmpty(t *testing.T) {
	counts := BuildCounts(&Intermediate{})
	assert.Len(t, counts, 12)
	for _, c := range counts {
		assert.Zero(t, c.Transformed, c.Key)
	}
}

func TestApplySkipped(t *testing.T) {
	counts := BuildCounts(&Intermediate{
		PublicChannels: []*IntermediateChannel{{Name: "general", Type: model.ChannelTypeOpen}},
	})

	counts = ApplySkipped(counts, map[string]int{
		EntityPost:                      4,
		"unknown_key_from_a_future_row": 99, // must be ignored, not appended as a new row
	})

	byKey := make(map[string]EntityCount, len(counts))
	for _, c := range counts {
		byKey[c.Key] = c
	}

	assert.Equal(t, 4, byKey[EntityPost].Skipped)
	assert.Zero(t, byKey[EntityUser].Skipped)
	assert.Len(t, counts, 12, "unknown skipped keys must not grow the fixed row list")
}
