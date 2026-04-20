package commands_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mattermost/mattermost/server/public/model"
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

const transformLogFile = "transform-slack.log"

// uniqueTeamName generates a unique team name for testing to avoid conflicts.
// Uses crypto/rand for sufficient entropy to prevent collisions in parallel CI,
// falling back to time-based naming if crypto/rand fails.
// The "t" prefix ensures team names don't conflict with Mattermost reserved URLs.
func uniqueTeamName(prefix string) string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("t%s%d", prefix, time.Now().UnixNano())
	}
	return fmt.Sprintf("t%s%s", prefix, hex.EncodeToString(b))
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
	t.Cleanup(func() { os.Remove(transformLogFile) })

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

	t.Run("mentions are correctly converted in export and import", func(t *testing.T) {
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

		// 4. Verify the JSONL export contains correctly converted mentions
		t.Log("Verifying mention conversion in JSONL export...")
		exportData, err := os.ReadFile(mmExportPath)
		require.NoError(t, err)

		var postMessages []string
		for _, line := range strings.Split(string(exportData), "\n") {
			if line == "" {
				continue
			}
			var importLine map[string]json.RawMessage
			err = json.Unmarshal([]byte(line), &importLine)
			require.NoError(t, err)

			if string(importLine["type"]) != `"post"` {
				continue
			}

			var postData struct {
				Message string `json:"message"`
			}
			err = json.Unmarshal(importLine["post"], &postData)
			require.NoError(t, err)
			postMessages = append(postMessages, postData.Message)
		}

		require.NotEmpty(t, postMessages, "should have posts in export")

		// Verify user mention: Slack <@U002> → Mattermost @jane.smith
		var foundUserMention bool
		for _, msg := range postMessages {
			if strings.Contains(msg, "@jane.smith") {
				foundUserMention = true
				assert.NotContains(t, msg, "<@U002>", "raw Slack user mention should not remain in export")
			}
		}
		assert.True(t, foundUserMention, "user mention <@U002> should be converted to @jane.smith")

		// Verify channel mention: Slack <#C002|random> → Mattermost ~random
		var foundChannelMention bool
		for _, msg := range postMessages {
			if strings.Contains(msg, "~random") {
				foundChannelMention = true
				assert.NotContains(t, msg, "<#C002", "raw Slack channel mention should not remain in export")
			}
		}
		assert.True(t, foundChannelMention, "channel mention <#C002|random> should be converted to ~random")

		// Verify special mention: Slack <!here> → Mattermost @here
		var foundHereMention bool
		for _, msg := range postMessages {
			if strings.Contains(msg, "@here") {
				foundHereMention = true
				assert.NotContains(t, msg, "<!here>", "raw Slack <!here> should not remain in export")
				assert.NotContains(t, msg, "<!here|", "raw Slack <!here|...> should not remain in export")
			}
		}
		assert.True(t, foundHereMention, "<!here> should be converted to @here")

		// Verify pipe-aliased special mentions: <!here|here> → @here, <!channel|@channel> → @channel
		var foundPipeAliasedHere, foundPipeAliasedChannel bool
		for _, msg := range postMessages {
			if strings.Contains(msg, "pipe-aliased here") {
				foundPipeAliasedHere = true
				assert.Contains(t, msg, "@here", "pipe-aliased <!here|here> should become @here")
				assert.NotContains(t, msg, "<!here|here>", "raw pipe-aliased <!here|here> should not remain")
			}
			if strings.Contains(msg, "pipe-aliased channel") {
				foundPipeAliasedChannel = true
				assert.Contains(t, msg, "@channel", "pipe-aliased <!channel|@channel> should become @channel")
				assert.NotContains(t, msg, "<!channel|", "raw pipe-aliased <!channel|...> should not remain")
			}
		}
		assert.True(t, foundPipeAliasedHere, "pipe-aliased <!here|here> should be converted to @here")
		assert.True(t, foundPipeAliasedChannel, "pipe-aliased <!channel|@channel> should be converted to @channel")

		// Verify W-prefix enterprise Grid user mention: <@W003> → @grid.user
		var foundWPrefixMention bool
		for _, msg := range postMessages {
			if strings.Contains(msg, "@grid.user") {
				foundWPrefixMention = true
				assert.NotContains(t, msg, "<@W003>", "raw W-prefix mention should not remain in export")
				assert.NotContains(t, msg, "<@W003|", "raw W-prefix pipe mention should not remain in export")
			}
		}
		assert.True(t, foundWPrefixMention, "W-prefix user mention <@W003> should be converted to @grid.user")

		// 5. Import into Mattermost and verify posts are correct
		t.Log("Importing data with mentions into Mattermost...")
		err = th.ImportBulkData(ctx, mmExportPath)
		require.NoError(t, err, "import should succeed")

		t.Log("Verifying mentions in Mattermost...")
		generalChannel := th.AssertChannelExists(ctx, teamName, "general")

		posts, err := th.GetChannelPosts(ctx, generalChannel.Id, 0, 100)
		require.NoError(t, err)

		var foundUserMentionInMM, foundChannelMentionInMM, foundHereMentionInMM bool
		var foundWPrefixInMM, foundPipeAliasedInMM bool
		for _, postID := range posts.Order {
			post := posts.Posts[postID]
			if strings.Contains(post.Message, "@jane.smith") {
				foundUserMentionInMM = true
			}
			if strings.Contains(post.Message, "~random") {
				foundChannelMentionInMM = true
			}
			if strings.Contains(post.Message, "@here") {
				foundHereMentionInMM = true
			}
			if strings.Contains(post.Message, "@grid.user") {
				foundWPrefixInMM = true
			}
			if strings.Contains(post.Message, "@channel") {
				foundPipeAliasedInMM = true
			}
		}
		assert.True(t, foundUserMentionInMM, "user mention @jane.smith should be present in Mattermost")
		assert.True(t, foundChannelMentionInMM, "channel mention ~random should be present in Mattermost")
		assert.True(t, foundHereMentionInMM, "@here mention should be present in Mattermost")
		assert.True(t, foundWPrefixInMM, "W-prefix user @grid.user should be present in Mattermost")
		assert.True(t, foundPipeAliasedInMM, "pipe-aliased @channel should be present in Mattermost")
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
	t.Cleanup(func() { os.Remove(transformLogFile) })

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
		defer os.Remove(transformLogFile)

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
		defer os.Remove(transformLogFile)

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
		defer os.Remove(transformLogFile)

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
		defer os.Remove(transformLogFile)

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
		defer os.Remove(transformLogFile)

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

	// Mattermost's importBot resolves --bot-owner by username. When the owner
	// username doesn't exist, the import still succeeds silently — the server
	// assumes the owner is a plugin and stores the raw username string as the
	// bot's OwnerId (instead of a resolved user ID). This means a typo in
	// --bot-owner won't cause an import failure, but the bots will have an
	// unresolvable owner. This test documents that behaviour so we notice if
	// the server-side semantics ever change.
	t.Run("import succeeds with non-existent bot owner username", func(t *testing.T) {
		th := testhelper.SetupHelper(t)
		defer th.TearDown()

		ctx := context.Background()
		tempDir := t.TempDir()
		slackExportPath := filepath.Join(tempDir, "slack_export.zip")
		mmExportPath := filepath.Join(tempDir, "mattermost_import.jsonl")
		teamName := uniqueTeamName("badowner")

		// Create Slack export with bots
		err := testhelper.ExportWithBots().Build(slackExportPath)
		require.NoError(t, err)

		// Create team
		team := th.CreateTeam(ctx, teamName, "Bad Owner E2E Team")
		require.NotNil(t, team)

		// Run transform with a --bot-owner that does not exist in Mattermost
		fakeOwner := "fake_user_non_existent"
		args := []string{
			"transform", "slack",
			"--team", teamName,
			"--file", slackExportPath,
			"--output", mmExportPath,
			"--skip-attachments",
			"--bot-owner", fakeOwner,
		}

		c := commands.RootCmd
		resetCobraFlags(c)
		c.SetArgs(args)
		err = c.Execute()
		require.NoError(t, err, "transform should succeed regardless of owner existence")
		defer os.Remove(transformLogFile)

		// Import succeeds even though the owner username doesn't exist
		err = th.ImportBulkData(ctx, mmExportPath)
		require.NoError(t, err, "import should succeed even with non-existent bot owner")

		// Bots should still be created with correct properties
		deployBot := th.AssertBotExists(ctx, "deploybot")
		assert.Equal(t, "Deploy Bot", deployBot.DisplayName)

		alertBot := th.AssertBotExists(ctx, "alertbot")
		assert.Equal(t, "Alert Bot", alertBot.DisplayName)

		// OwnerId is the raw username string (plugin-owner fallback),
		// not a resolved user ID
		assert.Equal(t, fakeOwner, deployBot.OwnerId,
			"bot owner should be the raw username string when the user doesn't exist")
		assert.Equal(t, fakeOwner, alertBot.OwnerId,
			"bot owner should be the raw username string when the user doesn't exist")
	})
}

// TestTransformSlackE2ELastViewedAt verifies that after a Slack-to-Mattermost import
// channels do not appear as unread: last_viewed_at is set on channel memberships and
// DM participants so users see all pre-import messages as already read.
func TestTransformSlackE2ELastViewedAt(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	th := testhelper.SetupHelper(t)
	defer th.TearDown()
	t.Cleanup(func() { os.Remove(transformLogFile) })

	t.Run("channels and DMs are not marked as unread after import", func(t *testing.T) {
		ctx := context.Background()
		tempDir := t.TempDir()
		slackExportPath := filepath.Join(tempDir, "slack_export.zip")
		mmExportPath := filepath.Join(tempDir, "mattermost_import.jsonl")
		teamName := uniqueTeamName("unread")

		err := testhelper.ExportWithDirectMessages().Build(slackExportPath)
		require.NoError(t, err)

		team := th.CreateTeam(ctx, teamName, "Unread E2E Team")
		require.NotNil(t, team)

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

		err = th.ImportBulkData(ctx, mmExportPath)
		require.NoError(t, err)

		johnUser := th.AssertUserExists(ctx, "john.doe")
		janeUser := th.AssertUserExists(ctx, "jane.smith")

		// Verify neither user has unread messages in the general channel.
		// GetChannelUnread returns the unread count from the server's perspective;
		// MsgCount == 0 means the channel does not appear as unread.
		generalChannel := th.AssertChannelExists(ctx, teamName, "general")
		var unread *model.ChannelUnread
		for _, user := range []*model.User{johnUser, janeUser} {
			unread, _, err = th.Client.GetChannelUnread(ctx, generalChannel.Id, user.Id)
			require.NoError(t, err)
			assert.Equal(t, int64(0), unread.MsgCount,
				"%s should have no unread messages in general channel after import", user.Username)
		}

		// Verify neither participant has unread messages in the DM channel.
		var dmChannel *model.Channel
		dmChannel, _, err = th.Client.CreateDirectChannel(ctx, johnUser.Id, janeUser.Id)
		require.NoError(t, err)
		require.NotNil(t, dmChannel)

		for _, user := range []*model.User{johnUser, janeUser} {
			unread, _, err = th.Client.GetChannelUnread(ctx, dmChannel.Id, user.Id)
			require.NoError(t, err)
			assert.Equal(t, int64(0), unread.MsgCount,
				"%s should have no unread messages in DM after import", user.Username)
		}
	})

	t.Run("member MsgCount matches imported post count", func(t *testing.T) {
		ctx := context.Background()
		tempDir := t.TempDir()
		slackExportPath := filepath.Join(tempDir, "slack_export.zip")
		mmExportPath := filepath.Join(tempDir, "mattermost_import.jsonl")
		teamName := uniqueTeamName("msgcnt")

		// ExportWithDirectMessages adds:
		//   general: 2 root posts (no replies)
		//   random:  0 posts
		//   D001:    2 root posts (no replies)
		err := testhelper.ExportWithDirectMessages().Build(slackExportPath)
		require.NoError(t, err)

		team := th.CreateTeam(ctx, teamName, "MsgCount E2E Team")
		require.NotNil(t, team)

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

		err = th.ImportBulkData(ctx, mmExportPath)
		require.NoError(t, err)

		johnUser := th.AssertUserExists(ctx, "john.doe")
		janeUser := th.AssertUserExists(ctx, "jane.smith")

		// Verify MsgCount on general channel members matches the 2 imported posts.
		generalChannel := th.AssertChannelExists(ctx, teamName, "general")
		generalMembers, err := th.GetChannelMembers(ctx, generalChannel.Id)
		require.NoError(t, err)

		for _, m := range generalMembers {
			if m.UserId == johnUser.Id || m.UserId == janeUser.Id {
				assert.Equal(t, int64(2), m.MsgCount,
					"general channel member should have MsgCount=2 (2 root posts)")
				assert.Equal(t, int64(2), m.MsgCountRoot,
					"general channel member should have MsgCountRoot=2 (2 root posts)")
			}
		}

		// Verify MsgCount on DM participants matches the 2 imported DM posts.
		var dmChannel *model.Channel
		dmChannel, _, err = th.Client.CreateDirectChannel(ctx, johnUser.Id, janeUser.Id)
		require.NoError(t, err)
		require.NotNil(t, dmChannel)

		dmMembers, err := th.GetChannelMembers(ctx, dmChannel.Id)
		require.NoError(t, err)

		for _, m := range dmMembers {
			if m.UserId == johnUser.Id || m.UserId == janeUser.Id {
				assert.Equal(t, int64(2), m.MsgCount,
					"DM participant should have MsgCount=2 (2 root posts)")
				assert.Equal(t, int64(2), m.MsgCountRoot,
					"DM participant should have MsgCountRoot=2 (2 root posts)")
			}
		}
	})
}
