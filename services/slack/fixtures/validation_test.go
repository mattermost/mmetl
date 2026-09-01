package fixtures

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mattermost/mmetl/services/slack"
	"github.com/mattermost/mmetl/testhelper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSlackExportBuilderValidation(t *testing.T) {
	t.Run("fails when post references non-existent channel", func(t *testing.T) {
		tempDir := testhelper.WorkDir(t)
		outputPath := filepath.Join(tempDir, "export.zip")

		err := NewSlackExportBuilder().
			AddUser(slack.SlackUser{Id: "U001", Username: "user1"}).
			AddChannel(slack.SlackChannel{Id: "C001", Name: "general", Members: []string{"U001"}}).
			AddPost("non-existent-channel", slack.SlackPost{
				User: "U001",
				Text: "Hello",
				Type: "message",
			}).
			Build(outputPath)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "non-existent channel")
		assert.Contains(t, err.Error(), "non-existent-channel")
	})

	t.Run("fails when post references non-existent user", func(t *testing.T) {
		tempDir := testhelper.WorkDir(t)
		outputPath := filepath.Join(tempDir, "export.zip")

		err := NewSlackExportBuilder().
			AddUser(slack.SlackUser{Id: "U001", Username: "user1"}).
			AddChannel(slack.SlackChannel{Id: "C001", Name: "general", Members: []string{"U001"}}).
			AddPost("general", slack.SlackPost{
				User: "U999", // Non-existent user
				Text: "Hello",
				Type: "message",
			}).
			Build(outputPath)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "non-existent user")
		assert.Contains(t, err.Error(), "U999")
	})

	t.Run("fails when channel member references non-existent user", func(t *testing.T) {
		tempDir := testhelper.WorkDir(t)
		outputPath := filepath.Join(tempDir, "export.zip")

		err := NewSlackExportBuilder().
			AddUser(slack.SlackUser{Id: "U001", Username: "user1"}).
			AddChannel(slack.SlackChannel{
				Id:      "C001",
				Name:    "general",
				Members: []string{"U001", "U999"}, // U999 doesn't exist
			}).
			Build(outputPath)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "non-existent member user")
		assert.Contains(t, err.Error(), "U999")
	})

	t.Run("fails when channel creator references non-existent user", func(t *testing.T) {
		tempDir := testhelper.WorkDir(t)
		outputPath := filepath.Join(tempDir, "export.zip")

		err := NewSlackExportBuilder().
			AddUser(slack.SlackUser{Id: "U001", Username: "user1"}).
			AddChannel(slack.SlackChannel{
				Id:      "C001",
				Name:    "general",
				Creator: "U999", // Non-existent creator
				Members: []string{"U001"},
			}).
			Build(outputPath)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "non-existent creator user")
		assert.Contains(t, err.Error(), "U999")
	})

	t.Run("validates private channels", func(t *testing.T) {
		tempDir := testhelper.WorkDir(t)
		outputPath := filepath.Join(tempDir, "export.zip")

		err := NewSlackExportBuilder().
			AddUser(slack.SlackUser{Id: "U001", Username: "user1"}).
			AddPrivateChannel(slack.SlackChannel{
				Id:      "G001",
				Name:    "private",
				Members: []string{"U001", "U999"}, // U999 doesn't exist
			}).
			Build(outputPath)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "non-existent member user")
	})

	t.Run("validates group channels", func(t *testing.T) {
		tempDir := testhelper.WorkDir(t)
		outputPath := filepath.Join(tempDir, "export.zip")

		err := NewSlackExportBuilder().
			AddUser(slack.SlackUser{Id: "U001", Username: "user1"}).
			AddGroupChannel(slack.SlackChannel{
				Id:      "G002",
				Name:    "group-dm",
				Members: []string{"U001", "U999"},
			}).
			Build(outputPath)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "non-existent member user")
	})

	t.Run("validates direct channels", func(t *testing.T) {
		tempDir := testhelper.WorkDir(t)
		outputPath := filepath.Join(tempDir, "export.zip")

		err := NewSlackExportBuilder().
			AddUser(slack.SlackUser{Id: "U001", Username: "user1"}).
			AddDirectChannel(slack.SlackChannel{
				Id:      "D001",
				Name:    "dm",
				Members: []string{"U001", "U999"},
			}).
			Build(outputPath)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "non-existent member user")
	})

	t.Run("allows posts to private channels", func(t *testing.T) {
		tempDir := testhelper.WorkDir(t)
		outputPath := filepath.Join(tempDir, "export.zip")

		err := NewSlackExportBuilder().
			AddUser(slack.SlackUser{Id: "U001", Username: "user1"}).
			AddPrivateChannel(slack.SlackChannel{
				Id:      "G001",
				Name:    "private-team",
				Members: []string{"U001"},
			}).
			AddPost("private-team", slack.SlackPost{
				User: "U001",
				Text: "Secret message",
				Type: "message",
			}).
			Build(outputPath)

		require.NoError(t, err)
	})

	t.Run("allows bot messages without User field", func(t *testing.T) {
		tempDir := testhelper.WorkDir(t)
		outputPath := filepath.Join(tempDir, "export.zip")

		err := NewSlackExportBuilder().
			AddUser(slack.SlackUser{Id: "U001", Username: "user1"}).
			AddChannel(slack.SlackChannel{Id: "C001", Name: "general", Members: []string{"U001"}}).
			AddPost("general", slack.SlackPost{
				BotId:       "B001",
				BotUsername: "webhook-bot",
				Text:        "Automated message",
				Type:        "message",
				SubType:     "bot_message",
			}).
			Build(outputPath)

		require.NoError(t, err)
	})

	t.Run("allows empty creator field", func(t *testing.T) {
		tempDir := testhelper.WorkDir(t)
		outputPath := filepath.Join(tempDir, "export.zip")

		err := NewSlackExportBuilder().
			AddUser(slack.SlackUser{Id: "U001", Username: "user1"}).
			AddChannel(slack.SlackChannel{
				Id:      "C001",
				Name:    "general",
				Creator: "", // Empty creator is allowed
				Members: []string{"U001"},
			}).
			Build(outputPath)

		require.NoError(t, err)
	})

	t.Run("valid export passes validation", func(t *testing.T) {
		tempDir := testhelper.WorkDir(t)
		outputPath := filepath.Join(tempDir, "export.zip")

		err := NewSlackExportBuilder().
			AddUser(slack.SlackUser{Id: "U001", Username: "user1"}).
			AddUser(slack.SlackUser{Id: "U002", Username: "user2"}).
			AddChannel(slack.SlackChannel{
				Id:      "C001",
				Name:    "general",
				Creator: "U001",
				Members: []string{"U001", "U002"},
			}).
			AddPost("general", slack.SlackPost{
				User: "U001",
				Text: "Hello",
				Type: "message",
			}).
			AddPost("general", slack.SlackPost{
				User: "U002",
				Text: "Hi back!",
				Type: "message",
			}).
			Build(outputPath)

		require.NoError(t, err)

		// Verify file was created
		_, err = os.Stat(outputPath)
		require.NoError(t, err)
	})
}

func TestSlackExportBuilderSkipValidation(t *testing.T) {
	t.Run("SkipValidation allows building inconsistent exports", func(t *testing.T) {
		tempDir := testhelper.WorkDir(t)
		outputPath := filepath.Join(tempDir, "export.zip")

		// This would normally fail validation - post references non-existent user
		err := NewSlackExportBuilder().
			AddChannel(slack.SlackChannel{Id: "C001", Name: "general"}).
			AddPost("general", slack.SlackPost{
				User: "U999", // Non-existent user
				Text: "Hello from unknown user",
				Type: "message",
			}).
			SkipValidation().
			Build(outputPath)

		require.NoError(t, err, "should build successfully with SkipValidation")

		// Verify file was created
		_, err = os.Stat(outputPath)
		require.NoError(t, err)
	})
}
