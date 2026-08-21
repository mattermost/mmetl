package fixtures

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mmetl/services/intermediate"
	"github.com/mattermost/mmetl/services/slack"
	"github.com/mattermost/mmetl/testhelper"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSlackExportBuilderCanBeParsedByTransformer(t *testing.T) {
	logger := logrus.New()
	logger.SetLevel(logrus.DebugLevel)

	t.Run("BasicExport can be parsed", func(t *testing.T) {
		tempDir := testhelper.WorkDir(t)
		outputPath := filepath.Join(tempDir, "export.zip")

		err := SlackBasicExport().Build(outputPath)
		require.NoError(t, err)

		// Open the zip file with the transformer
		file, err := os.Open(outputPath)
		require.NoError(t, err)
		defer file.Close()

		info, err := file.Stat()
		require.NoError(t, err)

		reader, err := zip.NewReader(file, info.Size())
		require.NoError(t, err)

		transformer := &slack.Transformer{
			Exporter: intermediate.Exporter{
				TeamName: "testteam",
				Logger:   logger,
			},
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
		tempDir := testhelper.WorkDir(t)
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

		transformer := &slack.Transformer{
			Exporter: intermediate.Exporter{
				TeamName: "testteam",
				Logger:   logger,
			},
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
		tempDir := testhelper.WorkDir(t)
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

		transformer := &slack.Transformer{
			Exporter: intermediate.Exporter{
				TeamName: "testteam",
				Logger:   logger,
			},
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
		tempDir := testhelper.WorkDir(t)
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

		transformer := &slack.Transformer{
			Exporter: intermediate.Exporter{
				TeamName: "testteam",
				Logger:   logger,
			},
		}

		export, err := transformer.ParseSlackExportFile(reader, true)
		require.NoError(t, err)

		posts := export.Posts["general"]
		require.Len(t, posts, 5)

		// Verify mention formats are present
		assert.Contains(t, posts[0].Text, "<@U002>")
		assert.Contains(t, posts[1].Text, "<#C002|random>")
		assert.Contains(t, posts[2].Text, "<!here>")
		assert.Contains(t, posts[3].Text, "<!here|here>")
		assert.Contains(t, posts[3].Text, "<!channel|@channel>")
		assert.Contains(t, posts[4].Text, "<@W003>")
		assert.Contains(t, posts[4].Text, "<@W003|grid.user>")
	})

	t.Run("ExportWithDeletedUser can be parsed", func(t *testing.T) {
		tempDir := testhelper.WorkDir(t)
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

		transformer := &slack.Transformer{
			Exporter: intermediate.Exporter{
				TeamName: "testteam",
				Logger:   logger,
			},
		}

		export, err := transformer.ParseSlackExportFile(reader, true)
		require.NoError(t, err)

		require.Len(t, export.Users, 2)

		// Find the deleted user
		var deletedUser *slack.SlackUser
		var activeUser *slack.SlackUser
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

func TestSlackExportBuilderBotFixtures(t *testing.T) {
	logger := logrus.New()
	logger.SetLevel(logrus.DebugLevel)

	t.Run("ExportWithBots can be parsed and contains bot users", func(t *testing.T) {
		tempDir := testhelper.WorkDir(t)
		outputPath := filepath.Join(tempDir, "export.zip")

		err := ExportWithBots().Build(outputPath)
		require.NoError(t, err)

		file, err := os.Open(outputPath)
		require.NoError(t, err)
		defer file.Close()

		info, err := file.Stat()
		require.NoError(t, err)

		reader, err := zip.NewReader(file, info.Size())
		require.NoError(t, err)

		transformer := &slack.Transformer{
			Exporter: intermediate.Exporter{
				TeamName: "testteam",
				Logger:   logger,
			},
		}

		export, err := transformer.ParseSlackExportFile(reader, true)
		require.NoError(t, err)

		require.Len(t, export.Users, 3)

		var regularUsers, botUsers []slack.SlackUser
		for _, user := range export.Users {
			if user.IsBot {
				botUsers = append(botUsers, user)
			} else {
				regularUsers = append(regularUsers, user)
			}
		}

		assert.Len(t, regularUsers, 1, "should have 1 regular user")
		assert.Len(t, botUsers, 2, "should have 2 bot users")

		assert.Equal(t, "john.doe", regularUsers[0].Username)

		// Verify bot properties
		botIDs := make(map[string]slack.SlackUser)
		for _, bot := range botUsers {
			botIDs[bot.Profile.BotID] = bot
		}

		deployBot, ok := botIDs["B001"]
		require.True(t, ok, "should have Deploy Bot (B001)")
		assert.Equal(t, "deploybot", deployBot.Username)
		assert.Equal(t, "Deploy Bot", deployBot.Profile.RealName)
		assert.Equal(t, "Handles deployments", deployBot.Profile.Title)

		alertBot, ok := botIDs["B002"]
		require.True(t, ok, "should have Alert Bot (B002)")
		assert.Equal(t, "alertbot", alertBot.Username)
		assert.Equal(t, "Alert Bot", alertBot.Profile.RealName)
	})

	t.Run("ExportWithBotPosts can be parsed and contains bot posts", func(t *testing.T) {
		tempDir := testhelper.WorkDir(t)
		outputPath := filepath.Join(tempDir, "export.zip")

		err := ExportWithBotPosts().Build(outputPath)
		require.NoError(t, err)

		file, err := os.Open(outputPath)
		require.NoError(t, err)
		defer file.Close()

		info, err := file.Stat()
		require.NoError(t, err)

		reader, err := zip.NewReader(file, info.Size())
		require.NoError(t, err)

		transformer := &slack.Transformer{
			Exporter: intermediate.Exporter{
				TeamName: "testteam",
				Logger:   logger,
			},
		}

		export, err := transformer.ParseSlackExportFile(reader, true)
		require.NoError(t, err)

		require.Contains(t, export.Posts, "general")
		posts := export.Posts["general"]
		require.Len(t, posts, 3)

		// First post is from a regular user
		assert.Equal(t, "U001", posts[0].User)
		assert.Equal(t, "Starting the deploy", posts[0].Text)

		// Second post is a bot message
		assert.Equal(t, "B001", posts[1].BotId)
		assert.Equal(t, "bot_message", posts[1].SubType)
		assert.Equal(t, "Deployment started for v2.0.0", posts[1].Text)

		// Third post is also a bot message
		assert.Equal(t, "B002", posts[2].BotId)
		assert.Equal(t, "bot_message", posts[2].SubType)
		assert.Equal(t, "Alert: CPU usage above 90%", posts[2].Text)
	})

	t.Run("ExportWithDeletedBot can be parsed and contains deleted bot", func(t *testing.T) {
		tempDir := testhelper.WorkDir(t)
		outputPath := filepath.Join(tempDir, "export.zip")

		err := ExportWithDeletedBot().Build(outputPath)
		require.NoError(t, err)

		file, err := os.Open(outputPath)
		require.NoError(t, err)
		defer file.Close()

		info, err := file.Stat()
		require.NoError(t, err)

		reader, err := zip.NewReader(file, info.Size())
		require.NoError(t, err)

		transformer := &slack.Transformer{
			Exporter: intermediate.Exporter{
				TeamName: "testteam",
				Logger:   logger,
			},
		}

		export, err := transformer.ParseSlackExportFile(reader, true)
		require.NoError(t, err)

		require.Len(t, export.Users, 2)

		var deletedBot *slack.SlackUser
		for i := range export.Users {
			if export.Users[i].IsBot && export.Users[i].Deleted {
				deletedBot = &export.Users[i]
				break
			}
		}

		require.NotNil(t, deletedBot, "should have a deleted bot user")
		assert.Equal(t, "oldbot", deletedBot.Username)
		assert.Equal(t, "B003", deletedBot.Profile.BotID)
		assert.Equal(t, "Old Bot", deletedBot.Profile.RealName)
		assert.True(t, deletedBot.Deleted)
	})
}
