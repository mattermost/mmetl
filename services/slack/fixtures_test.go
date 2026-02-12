package slack

import (
	"archive/zip"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSlackExportBuilder(t *testing.T) {
	t.Run("creates valid zip file", func(t *testing.T) {
		tempDir := t.TempDir()
		outputPath := filepath.Join(tempDir, "export.zip")

		err := NewSlackExportBuilder().
			AddUser(SlackUser{
				Id:       "U001",
				Username: "testuser",
				Profile:  SlackProfile{Email: "test@example.com"},
			}).
			AddChannel(SlackChannel{
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
		tempDir := t.TempDir()
		outputPath := filepath.Join(tempDir, "export.zip")

		err := NewSlackExportBuilder().
			AddUser(SlackUser{Id: "U001", Username: "user1"}).
			AddUser(SlackUser{Id: "U002", Username: "user2"}).
			AddChannel(SlackChannel{
				Id:      "C001",
				Name:    "general",
				Creator: "U001",
				Members: []string{"U001", "U002"},
				Purpose: SlackChannelSub{Value: "General discussion"},
				Topic:   SlackChannelSub{Value: "Welcome!"},
			}).
			AddChannel(SlackChannel{
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

		var channels []SlackChannel
		for _, file := range reader.File {
			if file.Name == "channels.json" {
				rc, err := file.Open()
				require.NoError(t, err)
				defer rc.Close()

				err = json.NewDecoder(rc).Decode(&channels)
				require.NoError(t, err)
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
		tempDir := t.TempDir()
		outputPath := filepath.Join(tempDir, "export.zip")

		err := NewSlackExportBuilder().
			AddUser(SlackUser{
				Id:       "U001",
				Username: "john.doe",
				IsBot:    false,
				Profile: SlackProfile{
					RealName: "John Doe",
					Email:    "john@example.com",
					Title:    "Engineer",
				},
				Deleted: false,
			}).
			AddUser(SlackUser{
				Id:       "U002",
				Username: "bot.user",
				IsBot:    true,
				Profile: SlackProfile{
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

		var users []SlackUser
		for _, file := range reader.File {
			if file.Name == "users.json" {
				rc, err := file.Open()
				require.NoError(t, err)
				defer rc.Close()

				err = json.NewDecoder(rc).Decode(&users)
				require.NoError(t, err)
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
		tempDir := t.TempDir()
		outputPath := filepath.Join(tempDir, "export.zip")

		err := NewSlackExportBuilder().
			AddUser(SlackUser{Id: "U001", Username: "user1"}).
			AddUser(SlackUser{Id: "U002", Username: "user2"}).
			AddChannel(SlackChannel{Id: "C001", Name: "general", Members: []string{"U001", "U002"}}).
			AddPost("general", SlackPost{
				User:      "U001",
				Text:      "Hello World!",
				TimeStamp: "1704067200.000100",
				Type:      "message",
			}).
			AddPost("general", SlackPost{
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

		var posts []SlackPost
		for _, file := range reader.File {
			if file.Name == "general/2025-01-01.json" {
				rc, err := file.Open()
				require.NoError(t, err)
				defer rc.Close()

				err = json.NewDecoder(rc).Decode(&posts)
				require.NoError(t, err)
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
		tempDir := t.TempDir()
		outputPath := filepath.Join(tempDir, "export.zip")

		err := NewSlackExportBuilder().
			AddUser(SlackUser{Id: "U001", Username: "user1"}).
			AddPrivateChannel(SlackChannel{
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
				rc, err := file.Open()
				require.NoError(t, err)
				defer rc.Close()

				var channels []SlackChannel
				err = json.NewDecoder(rc).Decode(&channels)
				require.NoError(t, err)
				require.Len(t, channels, 1)
				assert.Equal(t, "private-team", channels[0].Name)
				break
			}
		}
		assert.True(t, found, "groups.json should exist")
	})

	t.Run("creates group DMs in mpims.json", func(t *testing.T) {
		tempDir := t.TempDir()
		outputPath := filepath.Join(tempDir, "export.zip")

		err := NewSlackExportBuilder().
			AddUser(SlackUser{Id: "U001", Username: "user1"}).
			AddUser(SlackUser{Id: "U002", Username: "user2"}).
			AddUser(SlackUser{Id: "U003", Username: "user3"}).
			AddGroupChannel(SlackChannel{
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
		tempDir := t.TempDir()
		outputPath := filepath.Join(tempDir, "export.zip")

		err := NewSlackExportBuilder().
			AddUser(SlackUser{Id: "U001", Username: "user1"}).
			AddUser(SlackUser{Id: "U002", Username: "user2"}).
			AddDirectChannel(SlackChannel{
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
		tempDir := t.TempDir()
		outputPath := filepath.Join(tempDir, "export.zip")

		err := NewSlackExportBuilder().
			AddUser(SlackUser{Id: "U001", Username: "user1"}).
			AddUser(SlackUser{Id: "U002", Username: "user2"}).
			AddChannel(SlackChannel{Id: "C001", Name: "general", Members: []string{"U001", "U002"}}).
			AddPost("general", SlackPost{
				User:      "U001",
				Text:      "Thread root",
				TimeStamp: "1704067200.000100",
				ThreadTS:  "1704067200.000100",
				Type:      "message",
			}).
			AddPost("general", SlackPost{
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

		var posts []SlackPost
		for _, file := range reader.File {
			if file.Name == "general/2025-01-01.json" {
				rc, err := file.Open()
				require.NoError(t, err)
				defer rc.Close()

				err = json.NewDecoder(rc).Decode(&posts)
				require.NoError(t, err)
				break
			}
		}

		require.Len(t, posts, 2)
		assert.Equal(t, "1704067200.000100", posts[0].ThreadTS)
		assert.Equal(t, "1704067200.000100", posts[1].ThreadTS)
	})
}

func TestSlackExportBuilderCanBeParsedByTransformer(t *testing.T) {
	logger := logrus.New()
	logger.SetLevel(logrus.DebugLevel)

	t.Run("BasicExport can be parsed", func(t *testing.T) {
		tempDir := t.TempDir()
		outputPath := filepath.Join(tempDir, "export.zip")

		err := BasicExport().Build(outputPath)
		require.NoError(t, err)

		// Open the zip file with the transformer
		file, err := os.Open(outputPath)
		require.NoError(t, err)
		defer file.Close()

		info, err := file.Stat()
		require.NoError(t, err)

		reader, err := zip.NewReader(file, info.Size())
		require.NoError(t, err)

		transformer := &Transformer{
			TeamName: "testteam",
			Logger:   logger,
		}

		export, err := transformer.ParseSlackExportFile(reader, true)
		require.NoError(t, err)
		require.NotNil(t, export)

		// Verify users were parsed
		require.Len(t, export.Users, 2)
		assert.Equal(t, "john.doe", export.Users[0].Username)
		assert.Equal(t, "john.doe@example.com", export.Users[0].Profile.Email)
		assert.Equal(t, "jane.smith", export.Users[1].Username)

		// Verify channels were parsed
		require.Len(t, export.PublicChannels, 2)
		assert.Equal(t, "general", export.PublicChannels[0].Name)
		assert.Equal(t, model.ChannelTypeOpen, export.PublicChannels[0].Type)
		assert.Equal(t, "random", export.PublicChannels[1].Name)
	})

	t.Run("ExportWithPosts can be parsed", func(t *testing.T) {
		tempDir := t.TempDir()
		outputPath := filepath.Join(tempDir, "export.zip")

		err := ExportWithPosts().Build(outputPath)
		require.NoError(t, err)

		file, err := os.Open(outputPath)
		require.NoError(t, err)
		defer file.Close()

		info, err := file.Stat()
		require.NoError(t, err)

		reader, err := zip.NewReader(file, info.Size())
		require.NoError(t, err)

		transformer := &Transformer{
			TeamName: "testteam",
			Logger:   logger,
		}

		export, err := transformer.ParseSlackExportFile(reader, true)
		require.NoError(t, err)

		// Verify posts were parsed
		require.Contains(t, export.Posts, "general")
		require.Contains(t, export.Posts, "random")
		assert.Len(t, export.Posts["general"], 2)
		assert.Len(t, export.Posts["random"], 1)

		// Verify post content
		assert.Equal(t, "Hello everyone!", export.Posts["general"][0].Text)
		assert.Equal(t, "U001", export.Posts["general"][0].User)
	})

	t.Run("ExportWithThreads can be parsed and threads are preserved", func(t *testing.T) {
		tempDir := t.TempDir()
		outputPath := filepath.Join(tempDir, "export.zip")

		err := ExportWithThreads().Build(outputPath)
		require.NoError(t, err)

		file, err := os.Open(outputPath)
		require.NoError(t, err)
		defer file.Close()

		info, err := file.Stat()
		require.NoError(t, err)

		reader, err := zip.NewReader(file, info.Size())
		require.NoError(t, err)

		transformer := &Transformer{
			TeamName: "testteam",
			Logger:   logger,
		}

		export, err := transformer.ParseSlackExportFile(reader, true)
		require.NoError(t, err)

		require.Contains(t, export.Posts, "general")
		posts := export.Posts["general"]
		require.Len(t, posts, 3)

		// All posts should have the same ThreadTS (same thread)
		rootTS := posts[0].ThreadTS
		for _, post := range posts {
			assert.Equal(t, rootTS, post.ThreadTS, "all posts should be in same thread")
		}
	})

	t.Run("ExportWithMentions can be parsed", func(t *testing.T) {
		tempDir := t.TempDir()
		outputPath := filepath.Join(tempDir, "export.zip")

		err := ExportWithMentions().Build(outputPath)
		require.NoError(t, err)

		file, err := os.Open(outputPath)
		require.NoError(t, err)
		defer file.Close()

		info, err := file.Stat()
		require.NoError(t, err)

		reader, err := zip.NewReader(file, info.Size())
		require.NoError(t, err)

		transformer := &Transformer{
			TeamName: "testteam",
			Logger:   logger,
		}

		export, err := transformer.ParseSlackExportFile(reader, true)
		require.NoError(t, err)

		posts := export.Posts["general"]
		require.Len(t, posts, 3)

		// Verify mention formats are present
		assert.Contains(t, posts[0].Text, "<@U002>")
		assert.Contains(t, posts[1].Text, "<#C002|random>")
		assert.Contains(t, posts[2].Text, "<!here>")
	})

	t.Run("ExportWithDeletedUser can be parsed", func(t *testing.T) {
		tempDir := t.TempDir()
		outputPath := filepath.Join(tempDir, "export.zip")

		err := ExportWithDeletedUser().Build(outputPath)
		require.NoError(t, err)

		file, err := os.Open(outputPath)
		require.NoError(t, err)
		defer file.Close()

		info, err := file.Stat()
		require.NoError(t, err)

		reader, err := zip.NewReader(file, info.Size())
		require.NoError(t, err)

		transformer := &Transformer{
			TeamName: "testteam",
			Logger:   logger,
		}

		export, err := transformer.ParseSlackExportFile(reader, true)
		require.NoError(t, err)

		require.Len(t, export.Users, 2)

		// Find the deleted user
		var deletedUser *SlackUser
		var activeUser *SlackUser
		for i := range export.Users {
			if export.Users[i].Deleted {
				deletedUser = &export.Users[i]
			} else {
				activeUser = &export.Users[i]
			}
		}

		require.NotNil(t, deletedUser, "should have a deleted user")
		require.NotNil(t, activeUser, "should have an active user")
		assert.Equal(t, "deleted.user", deletedUser.Username)
		assert.Equal(t, "john.doe", activeUser.Username)
	})
}

func TestSlackExportBuilderValidation(t *testing.T) {
	t.Run("fails when post references non-existent channel", func(t *testing.T) {
		tempDir := t.TempDir()
		outputPath := filepath.Join(tempDir, "export.zip")

		err := NewSlackExportBuilder().
			AddUser(SlackUser{Id: "U001", Username: "user1"}).
			AddChannel(SlackChannel{Id: "C001", Name: "general", Members: []string{"U001"}}).
			AddPost("non-existent-channel", SlackPost{
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
		tempDir := t.TempDir()
		outputPath := filepath.Join(tempDir, "export.zip")

		err := NewSlackExportBuilder().
			AddUser(SlackUser{Id: "U001", Username: "user1"}).
			AddChannel(SlackChannel{Id: "C001", Name: "general", Members: []string{"U001"}}).
			AddPost("general", SlackPost{
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
		tempDir := t.TempDir()
		outputPath := filepath.Join(tempDir, "export.zip")

		err := NewSlackExportBuilder().
			AddUser(SlackUser{Id: "U001", Username: "user1"}).
			AddChannel(SlackChannel{
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
		tempDir := t.TempDir()
		outputPath := filepath.Join(tempDir, "export.zip")

		err := NewSlackExportBuilder().
			AddUser(SlackUser{Id: "U001", Username: "user1"}).
			AddChannel(SlackChannel{
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
		tempDir := t.TempDir()
		outputPath := filepath.Join(tempDir, "export.zip")

		err := NewSlackExportBuilder().
			AddUser(SlackUser{Id: "U001", Username: "user1"}).
			AddPrivateChannel(SlackChannel{
				Id:      "G001",
				Name:    "private",
				Members: []string{"U001", "U999"}, // U999 doesn't exist
			}).
			Build(outputPath)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "non-existent member user")
	})

	t.Run("validates group channels", func(t *testing.T) {
		tempDir := t.TempDir()
		outputPath := filepath.Join(tempDir, "export.zip")

		err := NewSlackExportBuilder().
			AddUser(SlackUser{Id: "U001", Username: "user1"}).
			AddGroupChannel(SlackChannel{
				Id:      "G002",
				Name:    "group-dm",
				Members: []string{"U001", "U999"},
			}).
			Build(outputPath)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "non-existent member user")
	})

	t.Run("validates direct channels", func(t *testing.T) {
		tempDir := t.TempDir()
		outputPath := filepath.Join(tempDir, "export.zip")

		err := NewSlackExportBuilder().
			AddUser(SlackUser{Id: "U001", Username: "user1"}).
			AddDirectChannel(SlackChannel{
				Id:      "D001",
				Name:    "dm",
				Members: []string{"U001", "U999"},
			}).
			Build(outputPath)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "non-existent member user")
	})

	t.Run("allows posts to private channels", func(t *testing.T) {
		tempDir := t.TempDir()
		outputPath := filepath.Join(tempDir, "export.zip")

		err := NewSlackExportBuilder().
			AddUser(SlackUser{Id: "U001", Username: "user1"}).
			AddPrivateChannel(SlackChannel{
				Id:      "G001",
				Name:    "private-team",
				Members: []string{"U001"},
			}).
			AddPost("private-team", SlackPost{
				User: "U001",
				Text: "Secret message",
				Type: "message",
			}).
			Build(outputPath)

		require.NoError(t, err)
	})

	t.Run("allows bot messages without User field", func(t *testing.T) {
		tempDir := t.TempDir()
		outputPath := filepath.Join(tempDir, "export.zip")

		err := NewSlackExportBuilder().
			AddUser(SlackUser{Id: "U001", Username: "user1"}).
			AddChannel(SlackChannel{Id: "C001", Name: "general", Members: []string{"U001"}}).
			AddPost("general", SlackPost{
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
		tempDir := t.TempDir()
		outputPath := filepath.Join(tempDir, "export.zip")

		err := NewSlackExportBuilder().
			AddUser(SlackUser{Id: "U001", Username: "user1"}).
			AddChannel(SlackChannel{
				Id:      "C001",
				Name:    "general",
				Creator: "", // Empty creator is allowed
				Members: []string{"U001"},
			}).
			Build(outputPath)

		require.NoError(t, err)
	})

	t.Run("valid export passes validation", func(t *testing.T) {
		tempDir := t.TempDir()
		outputPath := filepath.Join(tempDir, "export.zip")

		err := NewSlackExportBuilder().
			AddUser(SlackUser{Id: "U001", Username: "user1"}).
			AddUser(SlackUser{Id: "U002", Username: "user2"}).
			AddChannel(SlackChannel{
				Id:      "C001",
				Name:    "general",
				Creator: "U001",
				Members: []string{"U001", "U002"},
			}).
			AddPost("general", SlackPost{
				User: "U001",
				Text: "Hello",
				Type: "message",
			}).
			AddPost("general", SlackPost{
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
		tempDir := t.TempDir()
		outputPath := filepath.Join(tempDir, "export.zip")

		// This would normally fail validation - post references non-existent user
		err := NewSlackExportBuilder().
			AddChannel(SlackChannel{Id: "C001", Name: "general"}).
			AddPost("general", SlackPost{
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

func TestTransformerHandlesInconsistentExports(t *testing.T) {
	logger := logrus.New()
	logger.SetLevel(logrus.WarnLevel)

	t.Run("creates placeholder user for posts from missing users", func(t *testing.T) {
		tempDir := t.TempDir()
		outputPath := filepath.Join(tempDir, "export.zip")

		// Build an export with a post from a non-existent user
		err := NewSlackExportBuilder().
			AddUser(SlackUser{Id: "U001", Username: "existing.user", Profile: SlackProfile{Email: "existing@test.com"}}).
			AddChannel(SlackChannel{Id: "C001", Name: "general", Members: []string{"U001"}}).
			AddPost("general", SlackPost{
				User:      "U_MISSING", // This user doesn't exist in users.json
				Text:      "Post from deleted user",
				TimeStamp: "1704067200.000100",
				Type:      "message",
			}).
			SkipValidation().
			Build(outputPath)
		require.NoError(t, err)

		// Parse and transform
		file, err := os.Open(outputPath)
		require.NoError(t, err)
		defer file.Close()

		info, err := file.Stat()
		require.NoError(t, err)

		reader, err := zip.NewReader(file, info.Size())
		require.NoError(t, err)

		transformer := NewTransformer("testteam", logger)

		export, err := transformer.ParseSlackExportFile(reader, true)
		require.NoError(t, err)

		// Transform the export
		err = transformer.Transform(export, "", true, false, false, false, "")
		require.NoError(t, err)

		// Verify the missing user was created as a placeholder
		missingUser := transformer.Intermediate.UsersById["U_MISSING"]
		require.NotNil(t, missingUser, "should create placeholder for missing user")
		assert.Equal(t, "u_missing", missingUser.Username, "placeholder username should be lowercase ID")
		assert.Equal(t, "Deleted", missingUser.FirstName)
		assert.Equal(t, "User", missingUser.LastName)
		assert.Equal(t, "U_MISSING@local", missingUser.Email)
	})

	t.Run("creates placeholder user for channel members that dont exist", func(t *testing.T) {
		tempDir := t.TempDir()
		outputPath := filepath.Join(tempDir, "export.zip")

		// Build an export with a channel member that doesn't exist
		err := NewSlackExportBuilder().
			AddUser(SlackUser{Id: "U001", Username: "existing.user", Profile: SlackProfile{Email: "existing@test.com"}}).
			AddChannel(SlackChannel{
				Id:      "C001",
				Name:    "general",
				Members: []string{"U001", "U_DELETED_MEMBER"}, // U_DELETED_MEMBER doesn't exist
			}).
			SkipValidation().
			Build(outputPath)
		require.NoError(t, err)

		// Parse and transform
		file, err := os.Open(outputPath)
		require.NoError(t, err)
		defer file.Close()

		info, err := file.Stat()
		require.NoError(t, err)

		reader, err := zip.NewReader(file, info.Size())
		require.NoError(t, err)

		transformer := NewTransformer("testteam", logger)

		export, err := transformer.ParseSlackExportFile(reader, true)
		require.NoError(t, err)

		// Transform the export
		err = transformer.Transform(export, "", true, false, false, false, "")
		require.NoError(t, err)

		// Verify the missing member was created as a placeholder
		missingMember := transformer.Intermediate.UsersById["U_DELETED_MEMBER"]
		require.NotNil(t, missingMember, "should create placeholder for missing member")
		assert.Equal(t, "u_deleted_member", missingMember.Username)
		assert.Equal(t, "Deleted", missingMember.FirstName)
	})

	t.Run("handles posts and reactions from multiple missing users", func(t *testing.T) {
		tempDir := t.TempDir()
		outputPath := filepath.Join(tempDir, "export.zip")

		// Build an export with multiple missing users
		err := NewSlackExportBuilder().
			AddUser(SlackUser{Id: "U001", Username: "active.user", Profile: SlackProfile{Email: "active@test.com"}}).
			AddChannel(SlackChannel{Id: "C001", Name: "general", Members: []string{"U001"}}).
			AddPost("general", SlackPost{
				User:      "U001",
				Text:      "Hello",
				TimeStamp: "1704067200.000100",
				Type:      "message",
			}).
			AddPost("general", SlackPost{
				User:      "U_MISSING_1",
				Text:      "Post from first missing user",
				TimeStamp: "1704067260.000200",
				Type:      "message",
			}).
			AddPost("general", SlackPost{
				User:      "U_MISSING_2",
				Text:      "Post from second missing user",
				TimeStamp: "1704067320.000300",
				Type:      "message",
			}).
			SkipValidation().
			Build(outputPath)
		require.NoError(t, err)

		// Parse and transform
		file, err := os.Open(outputPath)
		require.NoError(t, err)
		defer file.Close()

		info, err := file.Stat()
		require.NoError(t, err)

		reader, err := zip.NewReader(file, info.Size())
		require.NoError(t, err)

		transformer := NewTransformer("testteam", logger)

		export, err := transformer.ParseSlackExportFile(reader, true)
		require.NoError(t, err)

		err = transformer.Transform(export, "", true, false, false, false, "")
		require.NoError(t, err)

		// Verify all missing users were created
		assert.NotNil(t, transformer.Intermediate.UsersById["U_MISSING_1"])
		assert.NotNil(t, transformer.Intermediate.UsersById["U_MISSING_2"])

		// Verify posts were still created
		assert.Len(t, transformer.Intermediate.Posts, 3)
	})
}

func TestSlackExportBuilderEdgeCases(t *testing.T) {
	t.Run("empty export creates valid zip", func(t *testing.T) {
		tempDir := t.TempDir()
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
		tempDir := t.TempDir()
		outputPath := filepath.Join(tempDir, "export.zip")

		posts := []SlackPost{
			{User: "U001", Text: "Post 1", TimeStamp: "1704067200.000100", Type: "message"},
			{User: "U002", Text: "Post 2", TimeStamp: "1704067260.000200", Type: "message"},
			{User: "U001", Text: "Post 3", TimeStamp: "1704067320.000300", Type: "message"},
		}

		err := NewSlackExportBuilder().
			AddUser(SlackUser{Id: "U001", Username: "user1"}).
			AddUser(SlackUser{Id: "U002", Username: "user2"}).
			AddChannel(SlackChannel{Id: "C001", Name: "general", Members: []string{"U001", "U002"}}).
			AddPosts("general", posts).
			Build(outputPath)
		require.NoError(t, err)

		reader, err := zip.OpenReader(outputPath)
		require.NoError(t, err)
		defer reader.Close()

		var parsedPosts []SlackPost
		for _, file := range reader.File {
			if file.Name == "general/2025-01-01.json" {
				rc, err := file.Open()
				require.NoError(t, err)
				defer rc.Close()

				err = json.NewDecoder(rc).Decode(&parsedPosts)
				require.NoError(t, err)
				break
			}
		}

		assert.Len(t, parsedPosts, 3)
	})

	t.Run("builder is chainable", func(t *testing.T) {
		builder := NewSlackExportBuilder().
			AddUser(SlackUser{Id: "U001", Username: "user1"}).
			AddUser(SlackUser{Id: "U002", Username: "user2"}).
			AddChannel(SlackChannel{Id: "C001", Name: "ch1", Members: []string{"U001", "U002"}}).
			AddChannel(SlackChannel{Id: "C002", Name: "ch2", Members: []string{"U001"}}).
			AddPrivateChannel(SlackChannel{Id: "G001", Name: "private1", Members: []string{"U001"}}).
			AddPost("ch1", SlackPost{User: "U001", Text: "msg1", Type: "message"}).
			AddPost("ch1", SlackPost{User: "U002", Text: "msg2", Type: "message"})

		// Verify internal state
		assert.Len(t, builder.users, 2)
		assert.Len(t, builder.channels, 2)
		assert.Len(t, builder.privateChannels, 1)
		assert.Len(t, builder.posts["ch1"], 2)
	})
}
