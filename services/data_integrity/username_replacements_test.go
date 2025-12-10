package data_integrity

import (
	"testing"

	"github.com/mattermost/mattermost/server/v8/channels/app/imports"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReplaceUsername(t *testing.T) {
	t.Run("nil input returns nil", func(t *testing.T) {
		result := replaceUsername(nil, map[string]string{"old": "new"})
		assert.Nil(t, result)
	})

	t.Run("username not in mapping returns original", func(t *testing.T) {
		username := "unchanged"
		result := replaceUsername(&username, map[string]string{"old": "new"})
		require.NotNil(t, result)
		assert.Equal(t, "unchanged", *result)
	})

	t.Run("username in mapping returns new username", func(t *testing.T) {
		username := "olduser"
		result := replaceUsername(&username, map[string]string{"olduser": "newuser"})
		require.NotNil(t, result)
		assert.Equal(t, "newuser", *result)
	})

	t.Run("empty string username", func(t *testing.T) {
		username := ""
		result := replaceUsername(&username, map[string]string{"": "newuser"})
		require.NotNil(t, result)
		assert.Equal(t, "newuser", *result)
	})
}

func TestReplaceUsernames(t *testing.T) {
	t.Run("nil input returns nil", func(t *testing.T) {
		result := replaceUsernames(nil, map[string]string{"old": "new"})
		assert.Nil(t, result)
	})

	t.Run("empty slice returns empty slice", func(t *testing.T) {
		input := []string{}
		result := replaceUsernames(&input, map[string]string{"old": "new"})
		require.NotNil(t, result)
		assert.Empty(t, *result)
	})

	t.Run("no usernames in mapping returns original", func(t *testing.T) {
		input := []string{"user1", "user2", "user3"}
		result := replaceUsernames(&input, map[string]string{"old": "new"})
		require.NotNil(t, result)
		assert.Equal(t, []string{"user1", "user2", "user3"}, *result)
	})

	t.Run("some usernames in mapping are replaced", func(t *testing.T) {
		input := []string{"user1", "olduser", "user3"}
		result := replaceUsernames(&input, map[string]string{"olduser": "newuser"})
		require.NotNil(t, result)
		assert.Equal(t, []string{"user1", "newuser", "user3"}, *result)
	})

	t.Run("all usernames in mapping are replaced", func(t *testing.T) {
		input := []string{"old1", "old2", "old3"}
		mappings := map[string]string{
			"old1": "new1",
			"old2": "new2",
			"old3": "new3",
		}
		result := replaceUsernames(&input, mappings)
		require.NotNil(t, result)
		assert.Equal(t, []string{"new1", "new2", "new3"}, *result)
	})

	t.Run("duplicate usernames are all replaced", func(t *testing.T) {
		input := []string{"olduser", "user2", "olduser"}
		result := replaceUsernames(&input, map[string]string{"olduser": "newuser"})
		require.NotNil(t, result)
		assert.Equal(t, []string{"newuser", "user2", "newuser"}, *result)
	})
}

func TestHandleReactions(t *testing.T) {
	t.Run("nil reactions returns nil", func(t *testing.T) {
		result := handleReactions(nil, map[string]string{"old": "new"})
		assert.Nil(t, result)
	})

	t.Run("empty reactions returns empty slice", func(t *testing.T) {
		reactions := []imports.ReactionImportData{}
		result := handleReactions(&reactions, map[string]string{"old": "new"})
		require.NotNil(t, result)
		assert.Empty(t, *result)
	})

	t.Run("reactions with usernames not in mapping remain unchanged", func(t *testing.T) {
		user1 := "user1"
		user2 := "user2"
		reactions := []imports.ReactionImportData{
			{User: &user1},
			{User: &user2},
		}
		result := handleReactions(&reactions, map[string]string{"old": "new"})
		require.NotNil(t, result)
		require.Len(t, *result, 2)
		assert.Equal(t, "user1", *(*result)[0].User)
		assert.Equal(t, "user2", *(*result)[1].User)
	})

	t.Run("reactions with usernames in mapping are replaced", func(t *testing.T) {
		oldUser := "olduser"
		user2 := "user2"
		reactions := []imports.ReactionImportData{
			{User: &oldUser},
			{User: &user2},
		}
		result := handleReactions(&reactions, map[string]string{"olduser": "newuser"})
		require.NotNil(t, result)
		require.Len(t, *result, 2)
		assert.Equal(t, "newuser", *(*result)[0].User)
		assert.Equal(t, "user2", *(*result)[1].User)
	})

	t.Run("reactions with nil user pointers", func(t *testing.T) {
		reactions := []imports.ReactionImportData{
			{User: nil},
		}
		result := handleReactions(&reactions, map[string]string{"old": "new"})
		require.NotNil(t, result)
		require.Len(t, *result, 1)
		assert.Nil(t, (*result)[0].User)
	})
}

func TestHandleReplies(t *testing.T) {
	t.Run("nil replies returns nil", func(t *testing.T) {
		result := handleReplies(nil, map[string]string{"old": "new"})
		assert.Nil(t, result)
	})

	t.Run("empty replies returns empty slice", func(t *testing.T) {
		replies := []imports.ReplyImportData{}
		result := handleReplies(&replies, map[string]string{"old": "new"})
		require.NotNil(t, result)
		assert.Empty(t, *result)
	})

	t.Run("replies with usernames not in mapping remain unchanged", func(t *testing.T) {
		user1 := "user1"
		user2 := "user2"
		replies := []imports.ReplyImportData{
			{User: &user1},
			{User: &user2},
		}
		result := handleReplies(&replies, map[string]string{"old": "new"})
		require.NotNil(t, result)
		require.Len(t, *result, 2)
		assert.Equal(t, "user1", *(*result)[0].User)
		assert.Equal(t, "user2", *(*result)[1].User)
	})

	t.Run("replies with usernames in mapping are replaced", func(t *testing.T) {
		oldUser := "olduser"
		user2 := "user2"
		replies := []imports.ReplyImportData{
			{User: &oldUser},
			{User: &user2},
		}
		result := handleReplies(&replies, map[string]string{"olduser": "newuser"})
		require.NotNil(t, result)
		require.Len(t, *result, 2)
		assert.Equal(t, "newuser", *(*result)[0].User)
		assert.Equal(t, "user2", *(*result)[1].User)
	})

	t.Run("replies with FlaggedBy usernames are replaced", func(t *testing.T) {
		user := "user1"
		flaggedBy := []string{"olduser", "user2"}
		replies := []imports.ReplyImportData{
			{
				User:      &user,
				FlaggedBy: &flaggedBy,
			},
		}
		result := handleReplies(&replies, map[string]string{"olduser": "newuser"})
		require.NotNil(t, result)
		require.Len(t, *result, 1)
		assert.Equal(t, []string{"newuser", "user2"}, *(*result)[0].FlaggedBy)
	})

	t.Run("replies with nested reactions are replaced", func(t *testing.T) {
		user := "user1"
		reactionUser := "olduser"
		reactions := []imports.ReactionImportData{
			{User: &reactionUser},
		}
		replies := []imports.ReplyImportData{
			{
				User:      &user,
				Reactions: &reactions,
			},
		}
		result := handleReplies(&replies, map[string]string{"olduser": "newuser"})
		require.NotNil(t, result)
		require.Len(t, *result, 1)
		require.NotNil(t, (*result)[0].Reactions)
		require.Len(t, *(*result)[0].Reactions, 1)
		assert.Equal(t, "newuser", *(*(*result)[0].Reactions)[0].User)
	})
}

func TestProcessPost(t *testing.T) {
	t.Run("post with no usernames to replace", func(t *testing.T) {
		user := "user1"
		post := &imports.PostImportData{
			User: &user,
		}
		result := processPost(post, map[string]string{"old": "new"})
		require.NotNil(t, result)
		assert.Equal(t, "user1", *result.User)
	})

	t.Run("post user is replaced", func(t *testing.T) {
		user := "olduser"
		post := &imports.PostImportData{
			User: &user,
		}
		result := processPost(post, map[string]string{"olduser": "newuser"})
		require.NotNil(t, result)
		assert.Equal(t, "newuser", *result.User)
	})

	t.Run("post FlaggedBy users are replaced", func(t *testing.T) {
		user := "user1"
		flaggedBy := []string{"olduser", "user2"}
		post := &imports.PostImportData{
			User:      &user,
			FlaggedBy: &flaggedBy,
		}
		result := processPost(post, map[string]string{"olduser": "newuser"})
		require.NotNil(t, result)
		assert.Equal(t, []string{"newuser", "user2"}, *result.FlaggedBy)
	})

	t.Run("post reactions are replaced", func(t *testing.T) {
		user := "user1"
		reactionUser := "olduser"
		reactions := []imports.ReactionImportData{
			{User: &reactionUser},
		}
		post := &imports.PostImportData{
			User:      &user,
			Reactions: &reactions,
		}
		result := processPost(post, map[string]string{"olduser": "newuser"})
		require.NotNil(t, result)
		require.NotNil(t, result.Reactions)
		require.Len(t, *result.Reactions, 1)
		assert.Equal(t, "newuser", *(*result.Reactions)[0].User)
	})

	t.Run("post replies are replaced", func(t *testing.T) {
		user := "user1"
		replyUser := "olduser"
		replies := []imports.ReplyImportData{
			{User: &replyUser},
		}
		post := &imports.PostImportData{
			User:    &user,
			Replies: &replies,
		}
		result := processPost(post, map[string]string{"olduser": "newuser"})
		require.NotNil(t, result)
		require.NotNil(t, result.Replies)
		require.Len(t, *result.Replies, 1)
		assert.Equal(t, "newuser", *(*result.Replies)[0].User)
	})
}

func TestProcessDirectPost(t *testing.T) {
	t.Run("direct post with no usernames to replace", func(t *testing.T) {
		user := "user1"
		members := []string{"user1", "user2"}
		post := &imports.DirectPostImportData{
			User:           &user,
			ChannelMembers: &members,
		}
		result := processDirectPost(post, map[string]string{"old": "new"})
		require.NotNil(t, result)
		assert.Equal(t, "user1", *result.User)
		assert.Equal(t, []string{"user1", "user2"}, *result.ChannelMembers)
	})

	t.Run("direct post user is replaced", func(t *testing.T) {
		user := "olduser"
		members := []string{"user1", "user2"}
		post := &imports.DirectPostImportData{
			User:           &user,
			ChannelMembers: &members,
		}
		result := processDirectPost(post, map[string]string{"olduser": "newuser"})
		require.NotNil(t, result)
		assert.Equal(t, "newuser", *result.User)
	})

	t.Run("direct post ChannelMembers are replaced", func(t *testing.T) {
		user := "user1"
		members := []string{"olduser", "user2"}
		post := &imports.DirectPostImportData{
			User:           &user,
			ChannelMembers: &members,
		}
		result := processDirectPost(post, map[string]string{"olduser": "newuser"})
		require.NotNil(t, result)
		assert.Equal(t, []string{"newuser", "user2"}, *result.ChannelMembers)
	})

	t.Run("direct post FlaggedBy users are replaced", func(t *testing.T) {
		user := "user1"
		members := []string{"user1", "user2"}
		flaggedBy := []string{"olduser", "user3"}
		post := &imports.DirectPostImportData{
			User:           &user,
			ChannelMembers: &members,
			FlaggedBy:      &flaggedBy,
		}
		result := processDirectPost(post, map[string]string{"olduser": "newuser"})
		require.NotNil(t, result)
		assert.Equal(t, []string{"newuser", "user3"}, *result.FlaggedBy)
	})

	t.Run("direct post reactions and replies are replaced", func(t *testing.T) {
		user := "user1"
		members := []string{"user1", "user2"}
		reactionUser := "olduser"
		replyUser := "olduser2"
		reactions := []imports.ReactionImportData{{User: &reactionUser}}
		replies := []imports.ReplyImportData{{User: &replyUser}}
		post := &imports.DirectPostImportData{
			User:           &user,
			ChannelMembers: &members,
			Reactions:      &reactions,
			Replies:        &replies,
		}
		mappings := map[string]string{
			"olduser":  "newuser",
			"olduser2": "newuser2",
		}
		result := processDirectPost(post, mappings)
		require.NotNil(t, result)
		require.NotNil(t, result.Reactions)
		require.Len(t, *result.Reactions, 1)
		assert.Equal(t, "newuser", *(*result.Reactions)[0].User)
		require.NotNil(t, result.Replies)
		require.Len(t, *result.Replies, 1)
		assert.Equal(t, "newuser2", *(*result.Replies)[0].User)
	})
}

func TestProcessChannel(t *testing.T) {
	t.Run("channel is returned unchanged", func(t *testing.T) {
		name := "channel1"
		channel := &imports.ChannelImportData{
			Name: &name,
		}
		result := processChannel(channel, map[string]string{"old": "new"})
		require.NotNil(t, result)
		assert.Equal(t, "channel1", *result.Name)
	})
}

func TestProcessDirectChannel(t *testing.T) {
	t.Run("direct channel with no usernames to replace", func(t *testing.T) {
		members := []string{"user1", "user2"}
		channel := &imports.DirectChannelImportData{
			Members: &members,
		}
		result := processDirectChannel(channel, map[string]string{"old": "new"})
		require.NotNil(t, result)
		assert.Equal(t, []string{"user1", "user2"}, *result.Members)
	})

	t.Run("direct channel Members are replaced", func(t *testing.T) {
		members := []string{"olduser", "user2"}
		channel := &imports.DirectChannelImportData{
			Members: &members,
		}
		result := processDirectChannel(channel, map[string]string{"olduser": "newuser"})
		require.NotNil(t, result)
		assert.Equal(t, []string{"newuser", "user2"}, *result.Members)
	})

	t.Run("direct channel FavoritedBy are replaced", func(t *testing.T) {
		members := []string{"user1", "user2"}
		favoritedBy := []string{"olduser", "user3"}
		channel := &imports.DirectChannelImportData{
			Members:     &members,
			FavoritedBy: &favoritedBy,
		}
		result := processDirectChannel(channel, map[string]string{"olduser": "newuser"})
		require.NotNil(t, result)
		assert.Equal(t, []string{"newuser", "user3"}, *result.FavoritedBy)
	})

	t.Run("direct channel with nil Members and FavoritedBy", func(t *testing.T) {
		channel := &imports.DirectChannelImportData{}
		result := processDirectChannel(channel, map[string]string{"old": "new"})
		require.NotNil(t, result)
		assert.Nil(t, result.Members)
		assert.Nil(t, result.FavoritedBy)
	})
}
