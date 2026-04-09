package slack

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/v8/channels/app/imports"
	log "github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestSplitTextIntoChunks(t *testing.T) {
	t.Run("Text within limit should return single chunk", func(t *testing.T) {
		text := "Short text"
		chunks := splitTextIntoChunks(text, 100)

		if len(chunks) != 1 {
			t.Errorf("Expected 1 chunk, got %d", len(chunks))
		}
		if chunks[0] != text {
			t.Errorf("Expected chunk to equal original text")
		}
	})

	t.Run("Long text should be split into multiple chunks", func(t *testing.T) {
		text := model.NewRandomString(model.PostMessageMaxRunesV2 * 2)
		chunks := splitTextIntoChunks(text, model.PostMessageMaxRunesV2)

		if len(chunks) < 2 {
			t.Errorf("Expected at least 2 chunks, got %d", len(chunks))
		}

		// Verify each chunk is within the limit
		for i, chunk := range chunks {
			runeCount := len([]rune(chunk))
			if runeCount > model.PostMessageMaxRunesV2 {
				t.Errorf("Chunk %d exceeds limit: %d > %d", i, runeCount, model.PostMessageMaxRunesV2)
			}
		}
	})

	t.Run("Should split on word boundaries when possible", func(t *testing.T) {
		// Create text with clear word boundaries
		word := "word "
		repeatCount := (model.PostMessageMaxRunesV2 / len(word)) + 100
		text := strings.Repeat(word, repeatCount)

		chunks := splitTextIntoChunks(text, model.PostMessageMaxRunesV2)

		// First chunk should end with a space (word boundary)
		if len(chunks) > 1 && chunks[0][len(chunks[0])-1] != ' ' {
			t.Errorf("Expected first chunk to end with word boundary (space)")
		}
	})

	t.Run("Empty string", func(t *testing.T) {
		chunks := splitTextIntoChunks("", 100)
		if len(chunks) != 1 || chunks[0] != "" {
			t.Errorf("Expected single empty chunk, got %v", chunks)
		}
	})

	t.Run("Text exactly at limit", func(t *testing.T) {
		text := "12345"
		chunks := splitTextIntoChunks(text, 5)
		if len(chunks) != 1 || chunks[0] != text {
			t.Errorf("Expected single chunk with exact text, got %v", chunks)
		}
	})

	t.Run("Simple split at word boundary", func(t *testing.T) {
		text := "Hello world this is a test"
		chunks := splitTextIntoChunks(text, 15)
		expected := []string{"Hello world ", "this is a test"}
		if len(chunks) != len(expected) {
			t.Errorf("Expected %d chunks, got %d", len(expected), len(chunks))
		}
		for i, chunk := range chunks {
			if i < len(expected) && chunk != expected[i] {
				t.Errorf("Chunk %d: expected %q, got %q", i, expected[i], chunk)
			}
		}
	})

	t.Run("Split on newline", func(t *testing.T) {
		text := "Line one\nLine two\nLine three"
		chunks := splitTextIntoChunks(text, 15)
		expected := []string{"Line one\n", "Line two\n", "Line three"}
		if len(chunks) != len(expected) {
			t.Errorf("Expected %d chunks, got %d", len(expected), len(chunks))
		}
		for i, chunk := range chunks {
			if i < len(expected) && chunk != expected[i] {
				t.Errorf("Chunk %d: expected %q, got %q", i, expected[i], chunk)
			}
		}
	})

	t.Run("Split prefers newline over space", func(t *testing.T) {
		text := "This is line one\nThis is line two and it's longer"
		chunks := splitTextIntoChunks(text, 25)
		expected := []string{"This is line one\n", "This is line two and ", "it's longer"}
		if len(chunks) != len(expected) {
			t.Errorf("Expected %d chunks, got %d", len(expected), len(chunks))
		}
		for i, chunk := range chunks {
			if i < len(expected) && chunk != expected[i] {
				t.Errorf("Chunk %d: expected %q, got %q", i, expected[i], chunk)
			}
		}
	})

	t.Run("No good break point - split in middle of word", func(t *testing.T) {
		text := "thisisaverylongwordwithnobreaks"
		chunks := splitTextIntoChunks(text, 10)
		expected := []string{"thisisaver", "ylongwordw", "ithnobreak", "s"}
		if len(chunks) != len(expected) {
			t.Errorf("Expected %d chunks, got %d", len(expected), len(chunks))
		}
		for i, chunk := range chunks {
			if i < len(expected) && chunk != expected[i] {
				t.Errorf("Chunk %d: expected %q, got %q", i, expected[i], chunk)
			}
		}
	})

	t.Run("Multiple spaces", func(t *testing.T) {
		text := "Word1    Word2    Word3"
		chunks := splitTextIntoChunks(text, 15)
		// Verify each chunk is within limit
		for i, chunk := range chunks {
			runeCount := len([]rune(chunk))
			if runeCount > 15 {
				t.Errorf("Chunk %d exceeds limit: %d > 15", i, runeCount)
			}
		}
		// Verify joining gives back original
		joined := strings.Join(chunks, "")
		if joined != text {
			t.Errorf("Joined chunks don't match original: %q != %q", joined, text)
		}
	})

	t.Run("Unicode characters (emoji and multi-byte)", func(t *testing.T) {
		text := "Hello 👋 world 🌍 test"
		chunks := splitTextIntoChunks(text, 15)
		// Verify each chunk is within limit
		for i, chunk := range chunks {
			runeCount := len([]rune(chunk))
			if runeCount > 15 {
				t.Errorf("Chunk %d exceeds limit: %d > 15", i, runeCount)
			}
		}
		// Verify joining gives back original
		joined := strings.Join(chunks, "")
		if joined != text {
			t.Errorf("Joined chunks don't match original: %q != %q", joined, text)
		}
	})

	t.Run("Long text with newlines at various positions", func(t *testing.T) {
		text := "First line\nSecond line is longer\nThird line\nFourth line is also long"
		chunks := splitTextIntoChunks(text, 20)
		// Verify each chunk is within limit
		for i, chunk := range chunks {
			runeCount := len([]rune(chunk))
			if runeCount > 20 {
				t.Errorf("Chunk %d exceeds limit: %d > 20", i, runeCount)
			}
		}
		// Verify joining gives back original
		joined := strings.Join(chunks, "")
		if joined != text {
			t.Errorf("Joined chunks don't match original")
		}
	})

	t.Run("Text with newline beyond search range", func(t *testing.T) {
		text := "This is a very long line without breaks for over 100 characters and then\nthere is a newline but it's too far away to be found in the search range which is limited to 100 characters"
		chunks := splitTextIntoChunks(text, 80)
		// Verify each chunk is within limit
		for i, chunk := range chunks {
			runeCount := len([]rune(chunk))
			if runeCount > 80 {
				t.Errorf("Chunk %d exceeds limit: %d > 80, chunk: %q", i, runeCount, chunk)
			}
		}
		// Verify joining gives back original
		joined := strings.Join(chunks, "")
		if joined != text {
			t.Errorf("Joined chunks don't match original")
		}
	})

	t.Run("Very small limit", func(t *testing.T) {
		text := "Hello"
		chunks := splitTextIntoChunks(text, 2)
		expected := []string{"He", "ll", "o"}
		if len(chunks) != len(expected) {
			t.Errorf("Expected %d chunks, got %d", len(expected), len(chunks))
		}
		for i, chunk := range chunks {
			if i < len(expected) && chunk != expected[i] {
				t.Errorf("Chunk %d: expected %q, got %q", i, expected[i], chunk)
			}
		}
	})

	t.Run("Single character chunks", func(t *testing.T) {
		text := "ABCDE"
		chunks := splitTextIntoChunks(text, 1)
		expected := []string{"A", "B", "C", "D", "E"}
		if len(chunks) != len(expected) {
			t.Errorf("Expected %d chunks, got %d", len(expected), len(chunks))
		}
		for i, chunk := range chunks {
			if i < len(expected) && chunk != expected[i] {
				t.Errorf("Chunk %d: expected %q, got %q", i, expected[i], chunk)
			}
		}
	})

	t.Run("Newline at exact boundary", func(t *testing.T) {
		text := "1234567890\n1234567890"
		chunks := splitTextIntoChunks(text, 11)
		expected := []string{"1234567890\n", "1234567890"}
		if len(chunks) != len(expected) {
			t.Errorf("Expected %d chunks, got %d", len(expected), len(chunks))
		}
		for i, chunk := range chunks {
			if i < len(expected) && chunk != expected[i] {
				t.Errorf("Chunk %d: expected %q, got %q", i, expected[i], chunk)
			}
		}
	})

	t.Run("Space at exact boundary", func(t *testing.T) {
		text := "1234567890 1234567890"
		chunks := splitTextIntoChunks(text, 11)
		expected := []string{"1234567890 ", "1234567890"}
		if len(chunks) != len(expected) {
			t.Errorf("Expected %d chunks, got %d", len(expected), len(chunks))
		}
		for i, chunk := range chunks {
			if i < len(expected) && chunk != expected[i] {
				t.Errorf("Chunk %d: expected %q, got %q", i, expected[i], chunk)
			}
		}
	})

	t.Run("Mixed content with spaces and newlines", func(t *testing.T) {
		text := "First paragraph with some text.\n\nSecond paragraph with more content that needs to be split up properly."
		chunks := splitTextIntoChunks(text, 30)
		// Verify each chunk is within limit
		for i, chunk := range chunks {
			runeCount := len([]rune(chunk))
			if runeCount > 30 {
				t.Errorf("Chunk %d exceeds limit: %d > 30", i, runeCount)
			}
		}
		// Verify joining gives back original
		joined := strings.Join(chunks, "")
		if joined != text {
			t.Errorf("Joined chunks don't match original")
		}
	})

	t.Run("Japanese characters (multi-byte unicode)", func(t *testing.T) {
		text := "これは日本語のテストです。長いテキストを分割します。"
		chunks := splitTextIntoChunks(text, 15)
		// Verify each chunk is within limit
		for i, chunk := range chunks {
			runeCount := len([]rune(chunk))
			if runeCount > 15 {
				t.Errorf("Chunk %d exceeds limit: %d > 15", i, runeCount)
			}
		}
		// Verify joining gives back original
		joined := strings.Join(chunks, "")
		if joined != text {
			t.Errorf("Joined chunks don't match original")
		}
	})

	// Comprehensive verification for all test cases
	t.Run("All chunks preserve text integrity", func(t *testing.T) {
		testTexts := []struct {
			text     string
			maxRunes int
		}{
			{"Short text", 100},
			{"", 100},
			{"Hello world this is a test", 15},
			{model.NewRandomString(model.PostMessageMaxRunesV2 * 2), model.PostMessageMaxRunesV2},
		}

		for _, tt := range testTexts {
			chunks := splitTextIntoChunks(tt.text, tt.maxRunes)

			// Verify each chunk is within limit
			for i, chunk := range chunks {
				runeCount := len([]rune(chunk))
				if runeCount > tt.maxRunes {
					t.Errorf("Chunk %d exceeds limit: %d > %d", i, runeCount, tt.maxRunes)
				}
			}

			// Verify joining gives back original
			joined := strings.Join(chunks, "")
			if joined != tt.text {
				t.Errorf("Joined chunks don't match original text")
			}
		}
	})
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

		line := GetImportLineFromBot(user, "admin")

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

		line := GetImportLineFromBot(user, "admin")

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

func TestGetImportLineFromUser(t *testing.T) {
	t.Run("read state fields are set on channel memberships", func(t *testing.T) {
		user := &IntermediateUser{
			Username:  "alice",
			Email:     "alice@example.com",
			FirstName: "Alice",
			LastName:  "Smith",
			Memberships: []IntermediateMembership{
				{Name: "general", LastViewedAt: 5000, MsgCount: 10, MsgCountRoot: 5},
				{Name: "random", LastViewedAt: 9000, MsgCount: 20, MsgCountRoot: 12},
			},
		}

		line := GetImportLineFromUser(user, "myteam")

		assert.Equal(t, "user", line.Type)
		require.NotNil(t, line.User)
		require.NotNil(t, line.User.Teams)
		require.Len(t, *line.User.Teams, 1)

		team := (*line.User.Teams)[0]
		require.NotNil(t, team.Channels)
		channels := *team.Channels
		require.Len(t, channels, 2)

		assert.Equal(t, "general", *channels[0].Name)
		assert.Equal(t, int64(5000), *channels[0].LastViewedAt)
		assert.Equal(t, int64(10), *channels[0].MsgCount)
		assert.Equal(t, int64(5), *channels[0].MsgCountRoot)

		assert.Equal(t, "random", *channels[1].Name)
		assert.Equal(t, int64(9000), *channels[1].LastViewedAt)
		assert.Equal(t, int64(20), *channels[1].MsgCount)
		assert.Equal(t, int64(12), *channels[1].MsgCountRoot)
	})

	t.Run("MsgCount omitted when zero", func(t *testing.T) {
		user := &IntermediateUser{
			Username: "bob",
			Email:    "bob@example.com",
			Memberships: []IntermediateMembership{
				{Name: "empty-channel", LastViewedAt: 1000},
			},
		}

		line := GetImportLineFromUser(user, "myteam")

		channels := *(*line.User.Teams)[0].Channels
		require.Len(t, channels, 1)
		assert.Equal(t, int64(1000), *channels[0].LastViewedAt)
		assert.Nil(t, channels[0].MsgCount)
		assert.Nil(t, channels[0].MsgCountRoot)
	})

	t.Run("LastViewedAt omitted when zero", func(t *testing.T) {
		user := &IntermediateUser{
			Username: "dave",
			Email:    "dave@example.com",
			Memberships: []IntermediateMembership{
				{Name: "orphan-channel"},
			},
		}

		line := GetImportLineFromUser(user, "myteam")

		channels := *(*line.User.Teams)[0].Channels
		require.Len(t, channels, 1)
		assert.Nil(t, channels[0].LastViewedAt)
		assert.Nil(t, channels[0].MsgCount)
		assert.Nil(t, channels[0].MsgCountRoot)
	})

	t.Run("No memberships produces nil channels", func(t *testing.T) {
		user := &IntermediateUser{
			Username: "charlie",
			Email:    "charlie@example.com",
		}

		line := GetImportLineFromUser(user, "myteam")

		require.NotNil(t, line.User.Teams)
		team := (*line.User.Teams)[0]
		assert.Nil(t, team.Channels)
	})
}

func TestGetImportLineFromDirectChannel(t *testing.T) {
	t.Run("uses LastPostAt and post stats when posts exist", func(t *testing.T) {
		channel := &IntermediateChannel{
			Name:             "dm-channel",
			Topic:            "DM topic",
			MembersUsernames: []string{"alice", "bob"},
			Created:          1704067200,
			MsgCount:         8,
			MsgCountRoot:     5,
			LastPostAt:       1704099999000,
		}

		line := GetImportLineFromDirectChannel("myteam", channel)

		assert.Equal(t, "direct_channel", line.Type)
		require.NotNil(t, line.DirectChannel)
		require.NotNil(t, line.DirectChannel.Members)
		assert.Equal(t, []string{"alice", "bob"}, *line.DirectChannel.Members)
		require.Len(t, line.DirectChannel.Participants, 2)

		for _, p := range line.DirectChannel.Participants {
			assert.Equal(t, int64(1704099999000), *p.LastViewedAt)
			assert.Equal(t, int64(8), *p.MsgCount)
			assert.Equal(t, int64(5), *p.MsgCountRoot)
		}
	})

	t.Run("falls back to CreatedMillis when no posts", func(t *testing.T) {
		channel := &IntermediateChannel{
			Name:             "dm-channel",
			Topic:            "DM topic",
			MembersUsernames: []string{"alice", "bob"},
			Created:          1704067200,
		}

		line := GetImportLineFromDirectChannel("myteam", channel)

		require.Len(t, line.DirectChannel.Participants, 2)
		assert.Equal(t, int64(1704067200000), *line.DirectChannel.Participants[0].LastViewedAt)
		assert.Nil(t, line.DirectChannel.Participants[0].MsgCount)
	})

	t.Run("falls back to current time when Created is invalid", func(t *testing.T) {
		fixedTime := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
		originalNowFunc := nowFunc
		nowFunc = func() time.Time { return fixedTime }
		defer func() { nowFunc = originalNowFunc }()

		channel := &IntermediateChannel{
			Name:             "dm-channel",
			Topic:            "DM topic",
			MembersUsernames: []string{"alice", "bob"},
			Created:          1, // Slack DM placeholder
		}

		line := GetImportLineFromDirectChannel("myteam", channel)

		require.Len(t, line.DirectChannel.Participants, 2)
		assert.Equal(t, fixedTime.UnixMilli(), *line.DirectChannel.Participants[0].LastViewedAt)
		assert.Nil(t, line.DirectChannel.Participants[0].MsgCount)
	})
}

func TestExportDirectChannels(t *testing.T) {
	t.Run("writes all channels as JSONL lines", func(t *testing.T) {
		transformer := NewTransformer("myteam", log.New())
		channels := []*IntermediateChannel{
			{
				Name:             "dm1",
				MembersUsernames: []string{"alice", "bob"},
				Created:          1704067200,
				MsgCount:         5,
				MsgCountRoot:     3,
				LastPostAt:       1704099999000,
			},
			{
				Name:             "dm2",
				MembersUsernames: []string{"charlie", "dave"},
				Created:          1704067200,
			},
		}

		var buf bytes.Buffer
		err := transformer.ExportDirectChannels(channels, &buf)
		require.NoError(t, err)

		lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
		require.Len(t, lines, 2)

		for _, line := range lines {
			var parsed map[string]json.RawMessage
			require.NoError(t, json.Unmarshal([]byte(line), &parsed))
			assert.Equal(t, `"direct_channel"`, string(parsed["type"]))
		}
	})

	t.Run("empty channel list produces no output", func(t *testing.T) {
		transformer := NewTransformer("myteam", log.New())
		var buf bytes.Buffer
		err := transformer.ExportDirectChannels([]*IntermediateChannel{}, &buf)
		require.NoError(t, err)
		assert.Empty(t, buf.String())
	})
}
