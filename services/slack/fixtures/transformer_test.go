package fixtures

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"

	"github.com/mattermost/mmetl/services/slack"
	"github.com/mattermost/mmetl/testhelper"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTransformerHandlesInconsistentExports(t *testing.T) {
	logger := logrus.New()
	logger.SetLevel(logrus.WarnLevel)

	t.Run("creates placeholder user for posts from missing users", func(t *testing.T) {
		tempDir := testhelper.WorkDir(t)
		outputPath := filepath.Join(tempDir, "export.zip")

		// Build an export with a post from a non-existent user
		err := NewSlackExportBuilder().
			AddUser(slack.SlackUser{Id: "U001", Username: "existing.user", Profile: slack.SlackProfile{Email: "existing@test.com"}}).
			AddChannel(slack.SlackChannel{Id: "C001", Name: "general", Members: []string{"U001"}}).
			AddPost("general", slack.SlackPost{
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

		transformer := slack.NewTransformer("testteam", logger)

		export, err := transformer.ParseSlackExportFile(reader, true)
		require.NoError(t, err)

		// Transform the export
		err = transformer.Transform(export, "", true, false, false, false, "", slack.GuestHandlingGuest)
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
		tempDir := testhelper.WorkDir(t)
		outputPath := filepath.Join(tempDir, "export.zip")

		// Build an export with a channel member that doesn't exist
		err := NewSlackExportBuilder().
			AddUser(slack.SlackUser{Id: "U001", Username: "existing.user", Profile: slack.SlackProfile{Email: "existing@test.com"}}).
			AddChannel(slack.SlackChannel{
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

		transformer := slack.NewTransformer("testteam", logger)

		export, err := transformer.ParseSlackExportFile(reader, true)
		require.NoError(t, err)

		// Transform the export
		err = transformer.Transform(export, "", true, false, false, false, "", slack.GuestHandlingGuest)
		require.NoError(t, err)

		// Verify the missing member was created as a placeholder
		missingMember := transformer.Intermediate.UsersById["U_DELETED_MEMBER"]
		require.NotNil(t, missingMember, "should create placeholder for missing member")
		assert.Equal(t, "u_deleted_member", missingMember.Username)
		assert.Equal(t, "Deleted", missingMember.FirstName)
	})

	t.Run("handles posts from multiple missing users", func(t *testing.T) {
		tempDir := testhelper.WorkDir(t)
		outputPath := filepath.Join(tempDir, "export.zip")

		// Build an export with multiple missing users
		err := NewSlackExportBuilder().
			AddUser(slack.SlackUser{Id: "U001", Username: "active.user", Profile: slack.SlackProfile{Email: "active@test.com"}}).
			AddChannel(slack.SlackChannel{Id: "C001", Name: "general", Members: []string{"U001"}}).
			AddPost("general", slack.SlackPost{
				User:      "U001",
				Text:      "Hello",
				TimeStamp: "1704067200.000100",
				Type:      "message",
			}).
			AddPost("general", slack.SlackPost{
				User:      "U_MISSING_1",
				Text:      "Post from first missing user",
				TimeStamp: "1704067260.000200",
				Type:      "message",
			}).
			AddPost("general", slack.SlackPost{
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

		transformer := slack.NewTransformer("testteam", logger)

		export, err := transformer.ParseSlackExportFile(reader, true)
		require.NoError(t, err)

		err = transformer.Transform(export, "", true, false, false, false, "", slack.GuestHandlingGuest)
		require.NoError(t, err)

		// Verify all missing users were created
		assert.NotNil(t, transformer.Intermediate.UsersById["U_MISSING_1"])
		assert.NotNil(t, transformer.Intermediate.UsersById["U_MISSING_2"])

		// Verify posts were still created
		assert.Len(t, transformer.Intermediate.Posts, 3)
	})
}
