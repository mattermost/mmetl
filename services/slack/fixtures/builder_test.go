package fixtures

import (
	"archive/zip"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/mattermost/mmetl/services/slack"
	"github.com/mattermost/mmetl/testhelper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSlackExportBuilder(t *testing.T) {
	t.Run("creates valid zip file", func(t *testing.T) {
		tempDir := testhelper.WorkDir(t)
		outputPath := filepath.Join(tempDir, "export.zip")

		err := NewSlackExportBuilder().
			AddUser(slack.SlackUser{
				Id:       "U001",
				Username: "testuser",
				Profile:  slack.SlackProfile{Email: "test@example.com"},
			}).
			AddChannel(slack.SlackChannel{
				Id:      "C001",
				Name:    "general",
				Members: []string{"U001"},
			}).
			Build(outputPath)
		require.NoError(t, err)

		// Verify file exists
		_, err = os.Stat(outputPath)
		require.NoError(t, err, "zip file should exist")

		// Verify it's a valid zip
		reader, err := zip.OpenReader(outputPath)
		require.NoError(t, err, "should be a valid zip file")
		defer reader.Close()

		// Check expected files exist
		fileNames := make(map[string]bool)
		for _, file := range reader.File {
			fileNames[file.Name] = true
		}

		assert.True(t, fileNames["channels.json"], "should have channels.json")
		assert.True(t, fileNames["users.json"], "should have users.json")
	})

	t.Run("creates valid channels.json", func(t *testing.T) {
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
				Purpose: slack.SlackChannelSub{Value: "General discussion"},
				Topic:   slack.SlackChannelSub{Value: "Welcome!"},
			}).
			AddChannel(slack.SlackChannel{
				Id:      "C002",
				Name:    "random",
				Creator: "U002",
				Members: []string{"U001"},
			}).
			Build(outputPath)
		require.NoError(t, err)

		// Read and parse channels.json
		reader, err := zip.OpenReader(outputPath)
		require.NoError(t, err)
		defer reader.Close()

		var channels []slack.SlackChannel
		for _, file := range reader.File {
			if file.Name == "channels.json" {
				func() {
					rc, err := file.Open()
					require.NoError(t, err)
					defer rc.Close()

					err = json.NewDecoder(rc).Decode(&channels)
					require.NoError(t, err)
				}()
				break
			}
		}

		require.Len(t, channels, 2)
		assert.Equal(t, "C001", channels[0].Id)
		assert.Equal(t, "general", channels[0].Name)
		assert.Equal(t, "U001", channels[0].Creator)
		assert.Equal(t, []string{"U001", "U002"}, channels[0].Members)
		assert.Equal(t, "General discussion", channels[0].Purpose.Value)
		assert.Equal(t, "Welcome!", channels[0].Topic.Value)

		assert.Equal(t, "C002", channels[1].Id)
		assert.Equal(t, "random", channels[1].Name)
	})

	t.Run("creates valid users.json", func(t *testing.T) {
		tempDir := testhelper.WorkDir(t)
		outputPath := filepath.Join(tempDir, "export.zip")

		err := NewSlackExportBuilder().
			AddUser(slack.SlackUser{
				Id:       "U001",
				Username: "john.doe",
				IsBot:    false,
				Profile: slack.SlackProfile{
					RealName: "John Doe",
					Email:    "john@example.com",
					Title:    "Engineer",
				},
				Deleted: false,
			}).
			AddUser(slack.SlackUser{
				Id:       "U002",
				Username: "bot.user",
				IsBot:    true,
				Profile: slack.SlackProfile{
					RealName: "Bot User",
					BotID:    "B001",
				},
				Deleted: false,
			}).
			Build(outputPath)
		require.NoError(t, err)

		// Read and parse users.json
		reader, err := zip.OpenReader(outputPath)
		require.NoError(t, err)
		defer reader.Close()

		var users []slack.SlackUser
		for _, file := range reader.File {
			if file.Name == "users.json" {
				func() {
					rc, err := file.Open()
					require.NoError(t, err)
					defer rc.Close()

					err = json.NewDecoder(rc).Decode(&users)
					require.NoError(t, err)
				}()
				break
			}
		}

		require.Len(t, users, 2)
		assert.Equal(t, "U001", users[0].Id)
		assert.Equal(t, "john.doe", users[0].Username)
		assert.False(t, users[0].IsBot)
		assert.Equal(t, "john@example.com", users[0].Profile.Email)
		assert.Equal(t, "John Doe", users[0].Profile.RealName)
		assert.Equal(t, "Engineer", users[0].Profile.Title)

		assert.Equal(t, "U002", users[1].Id)
		assert.True(t, users[1].IsBot)
		assert.Equal(t, "B001", users[1].Profile.BotID)
	})

	t.Run("creates valid posts in channel folders", func(t *testing.T) {
		tempDir := testhelper.WorkDir(t)
		outputPath := filepath.Join(tempDir, "export.zip")

		err := NewSlackExportBuilder().
			AddUser(slack.SlackUser{Id: "U001", Username: "user1"}).
			AddUser(slack.SlackUser{Id: "U002", Username: "user2"}).
			AddChannel(slack.SlackChannel{Id: "C001", Name: "general", Members: []string{"U001", "U002"}}).
			AddPost("general", slack.SlackPost{
				User:      "U001",
				Text:      "Hello World!",
				TimeStamp: "1704067200.000100",
				Type:      "message",
			}).
			AddPost("general", slack.SlackPost{
				User:      "U002",
				Text:      "Hi there!",
				TimeStamp: "1704067260.000200",
				Type:      "message",
			}).
			Build(outputPath)
		require.NoError(t, err)

		// Read and parse posts from channel folder
		reader, err := zip.OpenReader(outputPath)
		require.NoError(t, err)
		defer reader.Close()

		var posts []slack.SlackPost
		for _, file := range reader.File {
			if file.Name == "general/2025-01-01.json" {
				func() {
					rc, err := file.Open()
					require.NoError(t, err)
					defer rc.Close()

					err = json.NewDecoder(rc).Decode(&posts)
					require.NoError(t, err)
				}()
				break
			}
		}

		require.Len(t, posts, 2)
		assert.Equal(t, "U001", posts[0].User)
		assert.Equal(t, "Hello World!", posts[0].Text)
		assert.Equal(t, "1704067200.000100", posts[0].TimeStamp)
		assert.Equal(t, "message", posts[0].Type)

		assert.Equal(t, "U002", posts[1].User)
		assert.Equal(t, "Hi there!", posts[1].Text)
	})

	t.Run("creates private channels in groups.json", func(t *testing.T) {
		tempDir := testhelper.WorkDir(t)
		outputPath := filepath.Join(tempDir, "export.zip")

		err := NewSlackExportBuilder().
			AddUser(slack.SlackUser{Id: "U001", Username: "user1"}).
			AddPrivateChannel(slack.SlackChannel{
				Id:      "G001",
				Name:    "private-team",
				Members: []string{"U001"},
			}).
			Build(outputPath)
		require.NoError(t, err)

		reader, err := zip.OpenReader(outputPath)
		require.NoError(t, err)
		defer reader.Close()

		var found bool
		for _, file := range reader.File {
			if file.Name == "groups.json" {
				found = true
				func() {
					rc, err := file.Open()
					require.NoError(t, err)
					defer rc.Close()

					var channels []slack.SlackChannel
					err = json.NewDecoder(rc).Decode(&channels)
					require.NoError(t, err)
					require.Len(t, channels, 1)
					assert.Equal(t, "private-team", channels[0].Name)
				}()
				break
			}
		}
		assert.True(t, found, "groups.json should exist")
	})

	t.Run("creates group DMs in mpims.json", func(t *testing.T) {
		tempDir := testhelper.WorkDir(t)
		outputPath := filepath.Join(tempDir, "export.zip")

		err := NewSlackExportBuilder().
			AddUser(slack.SlackUser{Id: "U001", Username: "user1"}).
			AddUser(slack.SlackUser{Id: "U002", Username: "user2"}).
			AddUser(slack.SlackUser{Id: "U003", Username: "user3"}).
			AddGroupChannel(slack.SlackChannel{
				Id:      "G002",
				Name:    "mpdm-user1--user2--user3-1",
				Members: []string{"U001", "U002", "U003"},
			}).
			Build(outputPath)
		require.NoError(t, err)

		reader, err := zip.OpenReader(outputPath)
		require.NoError(t, err)
		defer reader.Close()

		var found bool
		for _, file := range reader.File {
			if file.Name == "mpims.json" {
				found = true
				break
			}
		}
		assert.True(t, found, "mpims.json should exist")
	})

	t.Run("creates direct messages in dms.json", func(t *testing.T) {
		tempDir := testhelper.WorkDir(t)
		outputPath := filepath.Join(tempDir, "export.zip")

		err := NewSlackExportBuilder().
			AddUser(slack.SlackUser{Id: "U001", Username: "user1"}).
			AddUser(slack.SlackUser{Id: "U002", Username: "user2"}).
			AddDirectChannel(slack.SlackChannel{
				Id:      "D001",
				Name:    "dm-u001-u002",
				Members: []string{"U001", "U002"},
			}).
			Build(outputPath)
		require.NoError(t, err)

		reader, err := zip.OpenReader(outputPath)
		require.NoError(t, err)
		defer reader.Close()

		var found bool
		for _, file := range reader.File {
			if file.Name == "dms.json" {
				found = true
				break
			}
		}
		assert.True(t, found, "dms.json should exist")
	})

	t.Run("creates posts with thread timestamps", func(t *testing.T) {
		tempDir := testhelper.WorkDir(t)
		outputPath := filepath.Join(tempDir, "export.zip")

		err := NewSlackExportBuilder().
			AddUser(slack.SlackUser{Id: "U001", Username: "user1"}).
			AddUser(slack.SlackUser{Id: "U002", Username: "user2"}).
			AddChannel(slack.SlackChannel{Id: "C001", Name: "general", Members: []string{"U001", "U002"}}).
			AddPost("general", slack.SlackPost{
				User:      "U001",
				Text:      "Thread root",
				TimeStamp: "1704067200.000100",
				ThreadTS:  "1704067200.000100",
				Type:      "message",
			}).
			AddPost("general", slack.SlackPost{
				User:      "U002",
				Text:      "Thread reply",
				TimeStamp: "1704067260.000200",
				ThreadTS:  "1704067200.000100",
				Type:      "message",
			}).
			Build(outputPath)
		require.NoError(t, err)

		reader, err := zip.OpenReader(outputPath)
		require.NoError(t, err)
		defer reader.Close()

		var posts []slack.SlackPost
		for _, file := range reader.File {
			if file.Name == "general/2025-01-01.json" {
				func() {
					rc, err := file.Open()
					require.NoError(t, err)
					defer rc.Close()

					err = json.NewDecoder(rc).Decode(&posts)
					require.NoError(t, err)
				}()
				break
			}
		}

		require.Len(t, posts, 2)
		assert.Equal(t, "1704067200.000100", posts[0].ThreadTS)
		assert.Equal(t, "1704067200.000100", posts[1].ThreadTS)
	})
}

func TestSlackExportBuilderEdgeCases(t *testing.T) {
	t.Run("empty export creates valid zip", func(t *testing.T) {
		tempDir := testhelper.WorkDir(t)
		outputPath := filepath.Join(tempDir, "export.zip")

		err := NewSlackExportBuilder().Build(outputPath)
		require.NoError(t, err)

		reader, err := zip.OpenReader(outputPath)
		require.NoError(t, err)
		defer reader.Close()

		// Should still have channels.json and users.json (empty arrays)
		fileNames := make(map[string]bool)
		for _, file := range reader.File {
			fileNames[file.Name] = true
		}

		assert.True(t, fileNames["channels.json"])
		assert.True(t, fileNames["users.json"])
	})

	t.Run("AddPosts adds multiple posts", func(t *testing.T) {
		tempDir := testhelper.WorkDir(t)
		outputPath := filepath.Join(tempDir, "export.zip")

		posts := []slack.SlackPost{
			{User: "U001", Text: "Post 1", TimeStamp: "1704067200.000100", Type: "message"},
			{User: "U002", Text: "Post 2", TimeStamp: "1704067260.000200", Type: "message"},
			{User: "U001", Text: "Post 3", TimeStamp: "1704067320.000300", Type: "message"},
		}

		err := NewSlackExportBuilder().
			AddUser(slack.SlackUser{Id: "U001", Username: "user1"}).
			AddUser(slack.SlackUser{Id: "U002", Username: "user2"}).
			AddChannel(slack.SlackChannel{Id: "C001", Name: "general", Members: []string{"U001", "U002"}}).
			AddPosts("general", posts).
			Build(outputPath)
		require.NoError(t, err)

		reader, err := zip.OpenReader(outputPath)
		require.NoError(t, err)
		defer reader.Close()

		var parsedPosts []slack.SlackPost
		for _, file := range reader.File {
			if file.Name == "general/2025-01-01.json" {
				func() {
					rc, err := file.Open()
					require.NoError(t, err)
					defer rc.Close()

					err = json.NewDecoder(rc).Decode(&parsedPosts)
					require.NoError(t, err)
				}()
				break
			}
		}

		assert.Len(t, parsedPosts, 3)
	})

	t.Run("builder is chainable", func(t *testing.T) {
		builder := NewSlackExportBuilder().
			AddUser(slack.SlackUser{Id: "U001", Username: "user1"}).
			AddUser(slack.SlackUser{Id: "U002", Username: "user2"}).
			AddChannel(slack.SlackChannel{Id: "C001", Name: "ch1", Members: []string{"U001", "U002"}}).
			AddChannel(slack.SlackChannel{Id: "C002", Name: "ch2", Members: []string{"U001"}}).
			AddPrivateChannel(slack.SlackChannel{Id: "G001", Name: "private1", Members: []string{"U001"}}).
			AddPost("ch1", slack.SlackPost{User: "U001", Text: "msg1", Type: "message"}).
			AddPost("ch1", slack.SlackPost{User: "U002", Text: "msg2", Type: "message"})

		// Verify internal state
		assert.Len(t, builder.users, 2)
		assert.Len(t, builder.channels, 2)
		assert.Len(t, builder.privateChannels, 1)
		assert.Len(t, builder.posts["ch1"], 2)
	})
}
