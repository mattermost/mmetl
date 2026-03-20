package slack

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mattermost/mattermost/server/v8/channels/app/imports"
	log "github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mattermost/mmetl/services/intermediate"
)

func TestSlackConvertChannelName(t *testing.T) {
	testCases := []struct {
		Name           string
		ChannelName    string
		ChannelId      string
		ExpectedResult string
	}{
		{
			Name:           "Name with leading dash is trimmed",
			ChannelName:    "-boolean",
			ChannelId:      "C001",
			ExpectedResult: "boolean",
		},
		{
			Name:           "Name with leading and trailing dashes is trimmed",
			ChannelName:    "--test--",
			ChannelId:      "C002",
			ExpectedResult: "test",
		},
		{
			Name:           "Name with leading underscores is trimmed",
			ChannelName:    "__hidden",
			ChannelId:      "C003",
			ExpectedResult: "hidden",
		},
		{
			Name:           "Single character after trim gets prefixed",
			ChannelName:    "-a-",
			ChannelId:      "C004",
			ExpectedResult: "slack-channel-a",
		},
		{
			Name:           "Name that is only dashes falls back to channel ID",
			ChannelName:    "---",
			ChannelId:      "C005",
			ExpectedResult: "c005",
		},
		{
			Name:           "Valid name is returned as-is",
			ChannelName:    "general",
			ChannelId:      "C006",
			ExpectedResult: "general",
		},
		{
			Name:           "Name with invalid characters falls back to channel ID",
			ChannelName:    "my channel!",
			ChannelId:      "C007",
			ExpectedResult: "c007",
		},
		{
			Name:           "Uppercase name is lowercased",
			ChannelName:    "MyChannel",
			ChannelId:      "C008",
			ExpectedResult: "mychannel",
		},
		{
			Name:           "Mixed case name is lowercased",
			ChannelName:    "General-Discussion",
			ChannelId:      "C009",
			ExpectedResult: "general-discussion",
		},
		{
			Name:           "Single uppercase character after trim is lowercased with prefix",
			ChannelName:    "-A-",
			ChannelId:      "C010",
			ExpectedResult: "slack-channel-a",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			result := SlackConvertChannelName(tc.ChannelName, tc.ChannelId)
			require.Equal(t, tc.ExpectedResult, result)
		})
	}
}

func TestSlackConvertTimeStamp(t *testing.T) {
	testCases := []struct {
		Name           string
		SlackTimeStamp string
		ExpectedResult int64
	}{
		{
			Name:           "Converting an invalid timestamp",
			SlackTimeStamp: "asd",
			ExpectedResult: 1,
		},
		{
			Name:           "Converting a valid timestamp, rounding down",
			SlackTimeStamp: "1549307811.074100",
			ExpectedResult: 1549307811074,
		},
		{
			Name:           "Converting a valid timestamp, rounding up",
			SlackTimeStamp: "1549307811.074500",
			ExpectedResult: 1549307811075,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			res := SlackConvertTimeStamp(tc.SlackTimeStamp)
			require.Equal(t, tc.ExpectedResult, res)
		})
	}
}

func TestGetImportLineFromBot(t *testing.T) {
	t.Run("Basic bot export", func(t *testing.T) {
		user := &IntermediateUser{
			Id:          "B001",
			Username:    "mybot",
			DisplayName: "My Bot",
			Position:    "Bot Description",
			IsBot:       true,
		}

		line := intermediate.BotImportLine(user, "admin")

		assert.Equal(t, "bot", line.Type)
		require.NotNil(t, line.Bot)
		assert.Equal(t, "mybot", *line.Bot.Username)
		assert.Equal(t, "My Bot", *line.Bot.DisplayName)
		assert.Equal(t, "admin", *line.Bot.Owner)
		assert.Nil(t, line.Bot.Description)
		assert.Nil(t, line.Bot.DeleteAt)
		assert.Nil(t, line.User)
	})

	t.Run("Deleted bot export", func(t *testing.T) {
		user := &IntermediateUser{
			Id:          "B002",
			Username:    "deletedbot",
			DisplayName: "Deleted Bot",
			IsBot:       true,
			DeleteAt:    1234567890,
		}

		line := intermediate.BotImportLine(user, "admin")

		assert.Equal(t, "bot", line.Type)
		require.NotNil(t, line.Bot)
		require.NotNil(t, line.Bot.DeleteAt)
		assert.Equal(t, int64(1234567890), *line.Bot.DeleteAt)
	})
}

func TestExportUsersWithBots(t *testing.T) {
	t.Run("Users are exported before bots", func(t *testing.T) {
		slackTransformer := NewTransformer("test", log.New())
		slackTransformer.Intermediate.UsersById = map[string]*IntermediateUser{
			"U001": {
				Id:       "U001",
				Username: "alice",
				Email:    "alice@example.com",
			},
			"B001": {
				Id:          "B001",
				Username:    "mybot",
				DisplayName: "My Bot",
				IsBot:       true,
			},
			"U002": {
				Id:       "U002",
				Username: "bob",
				Email:    "bob@example.com",
			},
		}

		var buf bytes.Buffer
		err := slackTransformer.ExportUsers(&buf, "admin")
		require.NoError(t, err)

		lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
		require.Len(t, lines, 3) // 2 users + 1 bot

		// Decode each line
		var line0, line1, line2 imports.LineImportData
		require.NoError(t, json.Unmarshal([]byte(lines[0]), &line0))
		require.NoError(t, json.Unmarshal([]byte(lines[1]), &line1))
		require.NoError(t, json.Unmarshal([]byte(lines[2]), &line2))

		// First two should be regular users (sorted by username: alice, bob)
		assert.Equal(t, "user", line0.Type)
		assert.Equal(t, "alice", *line0.User.Username)
		assert.Equal(t, "user", line1.Type)
		assert.Equal(t, "bob", *line1.User.Username)

		// Last should be the bot
		assert.Equal(t, "bot", line2.Type)
		assert.Equal(t, "mybot", *line2.Bot.Username)
		assert.Equal(t, "admin", *line2.Bot.Owner)
	})

	t.Run("No bots means no bot lines", func(t *testing.T) {
		slackTransformer := NewTransformer("test", log.New())
		slackTransformer.Intermediate.UsersById = map[string]*IntermediateUser{
			"U001": {
				Id:       "U001",
				Username: "alice",
				Email:    "alice@example.com",
			},
		}

		var buf bytes.Buffer
		err := slackTransformer.ExportUsers(&buf, "")
		require.NoError(t, err)

		lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
		require.Len(t, lines, 1)

		var line0 imports.LineImportData
		require.NoError(t, json.Unmarshal([]byte(lines[0]), &line0))
		assert.Equal(t, "user", line0.Type)
	})
}
