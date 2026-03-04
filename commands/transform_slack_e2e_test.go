package commands_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mattermost/mmetl/commands"
	"github.com/mattermost/mmetl/testhelper"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// resetCobraFlags recursively resets all flags in a command tree to their
// default values. This prevents flag state from leaking between subtests
// when reusing a global cobra.Command.
func resetCobraFlags(cmd *cobra.Command) {
	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		_ = f.Value.Set(f.DefValue)
		f.Changed = false
	})
	for _, sub := range cmd.Commands() {
		resetCobraFlags(sub)
	}
}

// uniqueTeamName generates a unique team name for testing to avoid conflicts
// Mattermost has reserved paths like "posts", "files", "api", etc.
// Use a "t" prefix to ensure team names don't conflict with reserved URLs
func uniqueTeamName(prefix string) string {
	return fmt.Sprintf("t%s%d", prefix, time.Now().UnixNano()%10000)
}

// TestTransformSlackE2E tests the full end-to-end flow:
// 1. Create Slack export fixture
// 2. Run transform command to generate JSONL
// 3. Import the JSONL into Mattermost
// 4. Query Mattermost to verify data was imported correctly
func TestTransformSlackE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Setup Mattermost with testcontainers
	th := testhelper.SetupHelper(t)
	defer th.TearDown()

	t.Run("basic import creates users and channels in Mattermost", func(t *testing.T) {
		ctx := context.Background()
		tempDir := t.TempDir()
		slackExportPath := filepath.Join(tempDir, "slack_export.zip")
		mmExportPath := filepath.Join(tempDir, "mattermost_import.jsonl")
		teamName := uniqueTeamName("e2e")

		// 1. Create Slack export fixture
		err := testhelper.SlackBasicExport().Build(slackExportPath)
		require.NoError(t, err, "failed to create Slack export fixture")

		// 2. Create the team in Mattermost first (required for import)
		team := th.CreateTeam(ctx, teamName, "E2E Test Team")
		require.NotNil(t, team)
		t.Logf("Created team: %s (ID: %s)", team.Name, team.Id)

		// 3. Run the transform command
		args := []string{
			"transform", "slack",
			"--team", teamName,
			"--file", slackExportPath,
			"--output", mmExportPath,
			"--skip-attachments",
		}

		c := commands.RootCmd
		resetCobraFlags(c)
		c.SetArgs(args)
		err = c.Execute()
		require.NoError(t, err, "transform command should succeed")
		defer os.Remove("transform-slack.log")

		// Verify output file was created
		_, err = os.Stat(mmExportPath)
		require.NoError(t, err, "output file should exist")

		// 4. Validate the JSONL file (similar to mmctl import validate)
		t.Log("Validating import file...")
		validationResult := th.ValidateImportFileOrFail(ctx, mmExportPath)
		assert.Equal(t, uint64(2), validationResult.UserCount, "should have 2 users")
		assert.Equal(t, uint64(2), validationResult.ChannelCount, "should have 2 channels")

		// 5. Import the JSONL into Mattermost
		t.Log("Importing data into Mattermost...")
		err = th.ImportBulkData(ctx, mmExportPath)
		require.NoError(t, err, "import should succeed")

		// 6. Verify users were created in Mattermost
		t.Log("Verifying users in Mattermost...")
		johnUser := th.AssertUserExists(ctx, "john.doe")
		assert.Equal(t, "john.doe@example.com", johnUser.Email, "john.doe should have correct email")
		assert.Equal(t, "John", johnUser.FirstName, "john.doe should have correct first name")
		assert.Equal(t, "Doe", johnUser.LastName, "john.doe should have correct last name")
		assert.Equal(t, "Software Engineer", johnUser.Position, "john.doe should have correct position")

		janeUser := th.AssertUserExists(ctx, "jane.smith")
		assert.Equal(t, "jane.smith@example.com", janeUser.Email, "jane.smith should have correct email")
		assert.Equal(t, "Jane", janeUser.FirstName, "jane.smith should have correct first name")
		assert.Equal(t, "Smith", janeUser.LastName, "jane.smith should have correct last name")
		assert.Equal(t, "Product Manager", janeUser.Position, "jane.smith should have correct position")

		// 7. Verify channels were created in Mattermost
		t.Log("Verifying channels in Mattermost...")
		generalChannel := th.AssertChannelExists(ctx, teamName, "general")
		assert.Equal(t, "Company-wide announcements", generalChannel.Purpose)
		assert.Equal(t, "Welcome to the team!", generalChannel.Header)

		randomChannel := th.AssertChannelExists(ctx, teamName, "random")
		assert.Equal(t, "Non-work banter", randomChannel.Purpose)
		assert.Equal(t, "Water cooler chat", randomChannel.Header)

		// 8. Verify users are members of the team
		t.Log("Verifying team memberships...")
		th.AssertUserInTeam(ctx, team.Id, johnUser.Id)
		th.AssertUserInTeam(ctx, team.Id, janeUser.Id)

		// 9. Verify users are members of channels
		t.Log("Verifying channel memberships...")
		generalMembers, err := th.GetChannelMembers(ctx, generalChannel.Id)
		require.NoError(t, err)

		var johnInGeneral, janeInGeneral bool
		for _, member := range generalMembers {
			if member.UserId == johnUser.Id {
				johnInGeneral = true
			}
			if member.UserId == janeUser.Id {
				janeInGeneral = true
			}
		}
		assert.True(t, johnInGeneral, "john.doe should be member of general channel")
		assert.True(t, janeInGeneral, "jane.smith should be member of general channel")
	})

	t.Run("import with posts creates messages in Mattermost", func(t *testing.T) {
		ctx := context.Background()
		tempDir := t.TempDir()
		slackExportPath := filepath.Join(tempDir, "slack_export.zip")
		mmExportPath := filepath.Join(tempDir, "mattermost_import.jsonl")
		teamName := uniqueTeamName("posts")

		// 1. Create Slack export with posts
		err := testhelper.ExportWithPosts().Build(slackExportPath)
		require.NoError(t, err)

		// 2. Create team
		team := th.CreateTeam(ctx, teamName, "Posts E2E Team")
		require.NotNil(t, team)

		// 3. Run transform
		args := []string{
			"transform", "slack",
			"--team", teamName,
			"--file", slackExportPath,
			"--output", mmExportPath,
			"--skip-attachments",
		}

		c := commands.RootCmd
		resetCobraFlags(c)
		c.SetArgs(args)
		err = c.Execute()
		require.NoError(t, err)
		defer os.Remove("transform-slack.log")

		// 4. Import into Mattermost
		t.Log("Importing data with posts into Mattermost...")
		err = th.ImportBulkData(ctx, mmExportPath)
		require.NoError(t, err, "import should succeed")

		// 5. Verify posts were created in Mattermost
		t.Log("Verifying posts in Mattermost...")
		generalChannel := th.AssertChannelExists(ctx, teamName, "general")

		posts, err := th.GetChannelPosts(ctx, generalChannel.Id, 0, 100)
		require.NoError(t, err)
		require.NotNil(t, posts)

		// Verify we have posts
		require.NotEmpty(t, posts.Order, "should have posts in general channel")

		// Verify post content
		var foundHello, foundWelcome bool
		for _, postID := range posts.Order {
			post := posts.Posts[postID]
			if strings.Contains(post.Message, "Hello everyone") {
				foundHello = true
			}
			if strings.Contains(post.Message, "Welcome to the team") {
				foundWelcome = true
			}
		}
		assert.True(t, foundHello, "should find 'Hello everyone' post in Mattermost")
		assert.True(t, foundWelcome, "should find welcome post in Mattermost")

		// Verify random channel also has posts
		randomChannel := th.AssertChannelExists(ctx, teamName, "random")
		randomPosts, err := th.GetChannelPosts(ctx, randomChannel.Id, 0, 100)
		require.NoError(t, err)
		require.NotEmpty(t, randomPosts.Order, "should have posts in random channel")

		var foundCoffee bool
		for _, postID := range randomPosts.Order {
			post := randomPosts.Posts[postID]
			if strings.Contains(post.Message, "coffee") {
				foundCoffee = true
			}
		}
		assert.True(t, foundCoffee, "should find 'coffee' post in random channel")
	})

	t.Run("user mentions are correctly converted", func(t *testing.T) {
		ctx := context.Background()
		tempDir := t.TempDir()
		slackExportPath := filepath.Join(tempDir, "slack_export.zip")
		mmExportPath := filepath.Join(tempDir, "mattermost_import.jsonl")
		teamName := uniqueTeamName("mentions")

		// 1. Create Slack export with mentions
		err := testhelper.ExportWithMentions().Build(slackExportPath)
		require.NoError(t, err)

		// 2. Create team
		team := th.CreateTeam(ctx, teamName, "Mentions E2E Team")
		require.NotNil(t, team)

		// 3. Run transform
		args := []string{
			"transform", "slack",
			"--team", teamName,
			"--file", slackExportPath,
			"--output", mmExportPath,
			"--skip-attachments",
		}

		c := commands.RootCmd
		resetCobraFlags(c)
		c.SetArgs(args)
		err = c.Execute()
		require.NoError(t, err)
		defer os.Remove("transform-slack.log")

		// 4. Import into Mattermost
		t.Log("Importing data with mentions into Mattermost...")
		err = th.ImportBulkData(ctx, mmExportPath)
		require.NoError(t, err, "import should succeed")

		// 5. Verify mentions were converted correctly
		t.Log("Verifying mentions in Mattermost...")
		generalChannel := th.AssertChannelExists(ctx, teamName, "general")

		posts, err := th.GetChannelPosts(ctx, generalChannel.Id, 0, 100)
		require.NoError(t, err)

		var foundUserMention, foundHereMention bool
		for _, postID := range posts.Order {
			post := posts.Posts[postID]
			// Slack <@U002> should be converted to @jane.smith
			if strings.Contains(post.Message, "@jane.smith") {
				foundUserMention = true
			}
			// Slack <!here> should be converted to @here
			if strings.Contains(post.Message, "@here") {
				foundHereMention = true
			}
		}
		assert.True(t, foundUserMention, "user mention should be converted to @jane.smith")
		assert.True(t, foundHereMention, "@here mention should be present")
	})

	t.Run("deleted user is imported with deactivated status", func(t *testing.T) {
		ctx := context.Background()
		tempDir := t.TempDir()
		slackExportPath := filepath.Join(tempDir, "slack_export.zip")
		mmExportPath := filepath.Join(tempDir, "mattermost_import.jsonl")
		teamName := uniqueTeamName("deleted")

		// 1. Create Slack export with deleted user
		err := testhelper.ExportWithDeletedUser().Build(slackExportPath)
		require.NoError(t, err)

		// 2. Create team
		team := th.CreateTeam(ctx, teamName, "Deleted User E2E Team")
		require.NotNil(t, team)

		// 3. Run transform
		args := []string{
			"transform", "slack",
			"--team", teamName,
			"--file", slackExportPath,
			"--output", mmExportPath,
			"--skip-attachments",
		}

		c := commands.RootCmd
		resetCobraFlags(c)
		c.SetArgs(args)
		err = c.Execute()
		require.NoError(t, err)
		defer os.Remove("transform-slack.log")

		// 4. Import into Mattermost
		t.Log("Importing data with deleted user into Mattermost...")
		err = th.ImportBulkData(ctx, mmExportPath)
		require.NoError(t, err, "import should succeed")

		// 5. Verify active user exists and is active
		t.Log("Verifying users in Mattermost...")
		activeUser := th.AssertUserExists(ctx, "john.doe")
		assert.Equal(t, int64(0), activeUser.DeleteAt, "active user should not be deleted")

		// 6. Verify deleted user exists and is deactivated
		deletedUser := th.AssertUserExists(ctx, "deleted.user")
		assert.NotEqual(t, int64(0), deletedUser.DeleteAt, "deleted user should have DeleteAt set")
	})
}

// TestTransformSlackE2ETeamConsistency verifies that the team specified
// in the command is consistently applied to all imported entities
func TestTransformSlackE2ETeamConsistency(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	ctx := context.Background()
	th := testhelper.SetupHelper(t)
	defer th.TearDown()

	teamName := uniqueTeamName("consist")
	tempDir := t.TempDir()
	slackExportPath := filepath.Join(tempDir, "slack_export.zip")
	mmExportPath := filepath.Join(tempDir, "mattermost_import.jsonl")

	// Create export with posts
	err := testhelper.ExportWithPosts().Build(slackExportPath)
	require.NoError(t, err)

	// Create team
	team := th.CreateTeam(ctx, teamName, "Consistency E2E Team")
	require.NotNil(t, team)

	// Run transform
	args := []string{
		"transform", "slack",
		"--team", teamName,
		"--file", slackExportPath,
		"--output", mmExportPath,
		"--skip-attachments",
	}

	c := commands.RootCmd
	resetCobraFlags(c)
	c.SetArgs(args)
	err = c.Execute()
	require.NoError(t, err)
	defer os.Remove("transform-slack.log")

	// Import into Mattermost
	err = th.ImportBulkData(ctx, mmExportPath)
	require.NoError(t, err)

	// Verify ALL channels are in the correct team
	generalChannel := th.AssertChannelExists(ctx, teamName, "general")
	assert.Equal(t, team.Id, generalChannel.TeamId, "general channel should be in correct team")

	randomChannel := th.AssertChannelExists(ctx, teamName, "random")
	assert.Equal(t, team.Id, randomChannel.TeamId, "random channel should be in correct team")

	// Verify ALL users are members of the team
	johnUser := th.AssertUserExists(ctx, "john.doe")
	th.AssertUserInTeam(ctx, team.Id, johnUser.Id)

	janeUser := th.AssertUserExists(ctx, "jane.smith")
	th.AssertUserInTeam(ctx, team.Id, janeUser.Id)
}

// TestTransformSlackE2EBotImport tests the bot import functionality end-to-end
func TestTransformSlackE2EBotImport(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	th := testhelper.SetupHelper(t)
	defer th.TearDown()

	t.Run("bot users are imported as Mattermost bots with correct properties", func(t *testing.T) {
		ctx := context.Background()
		tempDir := t.TempDir()
		slackExportPath := filepath.Join(tempDir, "slack_export.zip")
		mmExportPath := filepath.Join(tempDir, "mattermost_import.jsonl")
		teamName := uniqueTeamName("bots")

		// 1. Create Slack export with bots
		err := testhelper.ExportWithBots().Build(slackExportPath)
		require.NoError(t, err, "failed to create Slack export fixture")

		// 2. Create team and ensure the bot owner (admin) exists
		team := th.CreateTeam(ctx, teamName, "Bots E2E Team")
		require.NotNil(t, team)

		// 3. Run transform with --bot-owner pointing to the admin user
		args := []string{
			"transform", "slack",
			"--team", teamName,
			"--file", slackExportPath,
			"--output", mmExportPath,
			"--skip-attachments",
			"--bot-owner", "admin",
		}

		c := commands.RootCmd
		resetCobraFlags(c)
		c.SetArgs(args)
		err = c.Execute()
		require.NoError(t, err, "transform command should succeed")
		defer os.Remove("transform-slack.log")

		// 4. Import into Mattermost
		t.Log("Importing data with bots into Mattermost...")
		err = th.ImportBulkData(ctx, mmExportPath)
		require.NoError(t, err, "import should succeed")

		// 5. Verify the regular user was created correctly
		t.Log("Verifying regular user in Mattermost...")
		johnUser := th.AssertUserExists(ctx, "john.doe")
		assert.Equal(t, "john.doe@example.com", johnUser.Email)
		assert.Equal(t, "John", johnUser.FirstName)
		assert.Equal(t, "Doe", johnUser.LastName)

		// 6. Verify bot users were created as proper Mattermost bots
		t.Log("Verifying bots in Mattermost...")
		deployBot := th.AssertBotExists(ctx, "deploybot")
		assert.Equal(t, "Deploy Bot", deployBot.DisplayName)
		assert.Equal(t, "Handles deployments", deployBot.Description)
		assert.Equal(t, int64(0), deployBot.DeleteAt, "active bot should not be deleted")

		alertBot := th.AssertBotExists(ctx, "alertbot")
		assert.Equal(t, "Alert Bot", alertBot.DisplayName)
		assert.Equal(t, int64(0), alertBot.DeleteAt, "active bot should not be deleted")

		// 7. Verify the bot owner is the admin user
		assert.Equal(t, th.AdminUser.Id, deployBot.OwnerId, "bot owner should be the admin user")
		assert.Equal(t, th.AdminUser.Id, alertBot.OwnerId, "bot owner should be the admin user")

		// 8. Verify bot users have IsBot flag set on their user records
		deployBotUser := th.AssertUserExists(ctx, "deploybot")
		assert.True(t, deployBotUser.IsBot, "deploybot user should have IsBot=true")

		alertBotUser := th.AssertUserExists(ctx, "alertbot")
		assert.True(t, alertBotUser.IsBot, "alertbot user should have IsBot=true")

		// 9. Verify the regular user is NOT a bot
		assert.False(t, johnUser.IsBot, "john.doe should not be a bot")

		// 10. Verify the channel was created
		th.AssertChannelExists(ctx, teamName, "general")
	})

	t.Run("bot posts are correctly attributed to bot users", func(t *testing.T) {
		ctx := context.Background()
		tempDir := t.TempDir()
		slackExportPath := filepath.Join(tempDir, "slack_export.zip")
		mmExportPath := filepath.Join(tempDir, "mattermost_import.jsonl")
		teamName := uniqueTeamName("botposts")

		// 1. Create Slack export with bot posts
		err := testhelper.ExportWithBotPosts().Build(slackExportPath)
		require.NoError(t, err)

		// 2. Create team
		team := th.CreateTeam(ctx, teamName, "Bot Posts E2E Team")
		require.NotNil(t, team)

		// 3. Run transform
		args := []string{
			"transform", "slack",
			"--team", teamName,
			"--file", slackExportPath,
			"--output", mmExportPath,
			"--skip-attachments",
			"--bot-owner", "admin",
		}

		c := commands.RootCmd
		resetCobraFlags(c)
		c.SetArgs(args)
		err = c.Execute()
		require.NoError(t, err)
		defer os.Remove("transform-slack.log")

		// 4. Import into Mattermost
		err = th.ImportBulkData(ctx, mmExportPath)
		require.NoError(t, err, "import should succeed")

		// 5. Verify posts in the channel
		t.Log("Verifying bot posts in Mattermost...")
		generalChannel := th.AssertChannelExists(ctx, teamName, "general")

		posts, err := th.GetChannelPosts(ctx, generalChannel.Id, 0, 100)
		require.NoError(t, err)
		require.NotEmpty(t, posts.Order, "should have posts in general channel")

		// 6. Verify we can find all three posts
		var foundDeploy, foundHuman, foundAlert bool
		for _, postID := range posts.Order {
			post := posts.Posts[postID]
			if strings.Contains(post.Message, "Starting the deploy") {
				foundHuman = true
			}
			if strings.Contains(post.Message, "Deployment started for v2.0.0") {
				foundDeploy = true
				// Verify the post is attributed to the bot user
				deployBotUser := th.AssertUserExists(ctx, "deploybot")
				assert.Equal(t, deployBotUser.Id, post.UserId, "deploy post should be attributed to deploybot user")
			}
			if strings.Contains(post.Message, "Alert: CPU usage above 90%") {
				foundAlert = true
				// Verify the post is attributed to the alert bot user
				alertBotUser := th.AssertUserExists(ctx, "alertbot")
				assert.Equal(t, alertBotUser.Id, post.UserId, "alert post should be attributed to alertbot user")
			}
		}
		assert.True(t, foundHuman, "should find human post")
		assert.True(t, foundDeploy, "should find deploy bot post")
		assert.True(t, foundAlert, "should find alert bot post")
	})

	t.Run("transform fails without --bot-owner when bots exist", func(t *testing.T) {
		tempDir := t.TempDir()
		slackExportPath := filepath.Join(tempDir, "slack_export.zip")
		mmExportPath := filepath.Join(tempDir, "mattermost_import.jsonl")
		teamName := uniqueTeamName("noowner")

		// Create Slack export with bots
		err := testhelper.ExportWithBots().Build(slackExportPath)
		require.NoError(t, err)

		// Run transform WITHOUT --bot-owner
		args := []string{
			"transform", "slack",
			"--team", teamName,
			"--file", slackExportPath,
			"--output", mmExportPath,
			"--skip-attachments",
		}

		c := commands.RootCmd
		resetCobraFlags(c)
		c.SetArgs(args)
		err = c.Execute()
		defer os.Remove("transform-slack.log")

		// Should fail with a clear error about --bot-owner
		require.Error(t, err, "transform should fail without --bot-owner when bots exist")
		assert.Contains(t, err.Error(), "bot-owner", "error should mention --bot-owner flag")
	})

	t.Run("transform succeeds without --bot-owner when no bots exist", func(t *testing.T) {
		tempDir := t.TempDir()
		slackExportPath := filepath.Join(tempDir, "slack_export.zip")
		mmExportPath := filepath.Join(tempDir, "mattermost_import.jsonl")
		teamName := uniqueTeamName("nobots")

		// Create Slack export WITHOUT bots
		err := testhelper.SlackBasicExport().Build(slackExportPath)
		require.NoError(t, err)

		// Run transform without --bot-owner (should be fine since no bots)
		args := []string{
			"transform", "slack",
			"--team", teamName,
			"--file", slackExportPath,
			"--output", mmExportPath,
			"--skip-attachments",
		}

		c := commands.RootCmd
		resetCobraFlags(c)
		c.SetArgs(args)
		err = c.Execute()
		defer os.Remove("transform-slack.log")

		require.NoError(t, err, "transform should succeed without --bot-owner when no bots exist")
	})

	t.Run("deleted bot produces correct delete_at in export and imports successfully", func(t *testing.T) {
		ctx := context.Background()
		tempDir := t.TempDir()
		slackExportPath := filepath.Join(tempDir, "slack_export.zip")
		mmExportPath := filepath.Join(tempDir, "mattermost_import.jsonl")
		teamName := uniqueTeamName("delbot")

		// Create Slack export with a deleted bot
		err := testhelper.ExportWithDeletedBot().Build(slackExportPath)
		require.NoError(t, err)

		// Create team
		team := th.CreateTeam(ctx, teamName, "Deleted Bot E2E Team")
		require.NotNil(t, team)

		// Run transform
		args := []string{
			"transform", "slack",
			"--team", teamName,
			"--file", slackExportPath,
			"--output", mmExportPath,
			"--skip-attachments",
			"--bot-owner", "admin",
		}

		c := commands.RootCmd
		resetCobraFlags(c)
		c.SetArgs(args)
		err = c.Execute()
		require.NoError(t, err)
		defer os.Remove("transform-slack.log")

		// Verify the generated JSONL contains a bot line with delete_at set.
		// Note: Mattermost's importBot server function does not currently honor
		// delete_at, so we verify the export file content rather than server state.
		// See: https://github.com/mattermost/mattermost/blob/master/server/channels/app/import_functions.go#L835-L930
		exportData, err := os.ReadFile(mmExportPath)
		require.NoError(t, err)

		var foundBotLine bool
		for _, line := range strings.Split(string(exportData), "\n") {
			if line == "" {
				continue
			}
			var importLine map[string]json.RawMessage
			err = json.Unmarshal([]byte(line), &importLine)
			require.NoError(t, err)

			if string(importLine["type"]) != `"bot"` {
				continue
			}

			var botData struct {
				Username string `json:"username"`
				DeleteAt *int64 `json:"delete_at"`
			}
			err = json.Unmarshal(importLine["bot"], &botData)
			require.NoError(t, err)

			if botData.Username == "oldbot" {
				foundBotLine = true
				require.NotNil(t, botData.DeleteAt, "deleted bot should have delete_at in export")
				assert.NotEqual(t, int64(0), *botData.DeleteAt, "delete_at should be non-zero")
			}
		}
		assert.True(t, foundBotLine, "should find oldbot in export JSONL")

		// Also verify the import succeeds (Mattermost accepts the file without errors)
		err = th.ImportBulkData(ctx, mmExportPath)
		require.NoError(t, err, "import should succeed")

		// Bot should exist in Mattermost (even though delete_at is not applied server-side)
		th.AssertBotExists(ctx, "oldbot")
	})
}
