package commands_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
		// This subtest needs its own isolated Mattermost instance because the
		// bot owner validation test must run on a clean server state.
		subTH := testhelper.SetupHelper(t)
		defer subTH.TearDown()

		ctx := context.Background()
		tempDir := t.TempDir()
		slackExportPath := filepath.Join(tempDir, "slack_export.zip")
		mmExportPath := filepath.Join(tempDir, "mattermost_import.jsonl")
		teamName := uniqueTeamName("badowner")

		// Create Slack export with bots
		err := testhelper.ExportWithBots().Build(slackExportPath)
		require.NoError(t, err)

		// Create team
		team := subTH.CreateTeam(ctx, teamName, "Bad Owner E2E Team")
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
		err = subTH.ImportBulkData(ctx, mmExportPath)
		require.NoError(t, err, "import should succeed even with non-existent bot owner")

		// Bots should still be created with correct properties
		deployBot := subTH.AssertBotExists(ctx, "deploybot")
		assert.Equal(t, "Deploy Bot", deployBot.DisplayName)

		alertBot := subTH.AssertBotExists(ctx, "alertbot")
		assert.Equal(t, "Alert Bot", alertBot.DisplayName)

		// OwnerId is the raw username string (plugin-owner fallback),
		// not a resolved user ID
		assert.Equal(t, fakeOwner, deployBot.OwnerId,
			"bot owner should be the raw username string when the user doesn't exist")
		assert.Equal(t, fakeOwner, alertBot.OwnerId,
			"bot owner should be the raw username string when the user doesn't exist")
	})

	t.Run("archived channels are imported as archived in Mattermost", func(t *testing.T) {
		// Reuses the outer th; isolation is provided by the unique team name.
		ctx := context.Background()
		tempDir := t.TempDir()
		slackExportPath := filepath.Join(tempDir, "slack_export.zip")
		mmExportPath := filepath.Join(tempDir, "mattermost_import.jsonl")
		teamName := uniqueTeamName("archived")

		// 1. Create Slack export with an archived channel
		err := testhelper.ExportWithArchivedChannels().Build(slackExportPath)
		require.NoError(t, err, "failed to create Slack export fixture with archived channels")

		// 2. Create team
		team := th.CreateTeam(ctx, teamName, "Archived Channels E2E Team")
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
		require.NoError(t, err, "transform command should succeed")
		defer os.Remove(transformLogFile)

		// 4. Verify the JSONL contains deleted_at for the archived channel
		t.Log("Checking JSONL output for archived channel...")
		outputBytes, err := os.ReadFile(mmExportPath)
		require.NoError(t, err)

		var foundArchivedChannel bool
		for _, line := range strings.Split(string(outputBytes), "\n") {
			if line == "" {
				continue
			}
			var importLine map[string]json.RawMessage
			require.NoError(t, json.Unmarshal([]byte(line), &importLine))
			if string(importLine["type"]) != `"channel"` {
				continue
			}
			var channel struct {
				Name      string `json:"name"`
				DeletedAt *int64 `json:"deleted_at"`
			}
			require.NoError(t, json.Unmarshal(importLine["channel"], &channel))
			if channel.Name == "old-project" {
				require.NotNil(t, channel.DeletedAt, "archived channel should have deleted_at set")
				assert.Greater(t, *channel.DeletedAt, int64(0), "deleted_at should be positive")
				foundArchivedChannel = true
			}
		}
		require.True(t, foundArchivedChannel, "should find archived channel 'old-project' in JSONL output")

		// 5. Validate and import
		t.Log("Validating import file...")
		th.ValidateImportFileOrFail(ctx, mmExportPath)

		t.Log("Importing data into Mattermost...")
		err = th.ImportBulkData(ctx, mmExportPath)
		require.NoError(t, err, "import should succeed")

		// 6. Verify active channel exists normally
		t.Log("Verifying active channel is not archived...")
		generalChannel := th.AssertChannelExists(ctx, teamName, "general")
		assert.Equal(t, int64(0), generalChannel.DeleteAt, "general channel should not be archived")

		// 7. Verify the archived channel is archived in Mattermost
		t.Log("Verifying archived channel is archived in Mattermost...")
		archivedChannel := th.AssertChannelIsArchived(ctx, teamName, "old-project")
		assert.Greater(t, archivedChannel.DeleteAt, int64(0), "old-project channel should be archived (DeleteAt > 0)")
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

// TestTransformSlackE2EMpimDedup verifies the mmetl-side fix for MM-68736:
// when a Slack export contains two MPIMs that share the same member set, mmetl
// should emit exactly one `direct_channel` JSONL line, preserve posts from
// both Slack channels, and the resulting import should succeed against
// Mattermost without crashing on `ChannelMember not found`.
func TestTransformSlackE2EMpimDedup(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	th := testhelper.SetupHelper(t)
	defer th.TearDown()
	t.Cleanup(func() { os.Remove(transformLogFile) })

	ctx := context.Background()
	tempDir := t.TempDir()
	slackExportPath := filepath.Join(tempDir, "slack_export.zip")
	mmExportPath := filepath.Join(tempDir, "mattermost_import.jsonl")
	teamName := uniqueTeamName("mpim")

	require.NoError(t, testhelper.ExportWithDuplicateMpims().Build(slackExportPath),
		"failed to build duplicate-MPIM Slack export fixture")

	team := th.CreateTeam(ctx, teamName, "MPIM Dedup E2E Team")
	require.NotNil(t, team)

	c := commands.RootCmd
	resetCobraFlags(c)
	c.SetArgs([]string{
		"transform", "slack",
		"--team", teamName,
		"--file", slackExportPath,
		"--output", mmExportPath,
		"--skip-attachments",
	})
	require.NoError(t, c.Execute(), "transform command should succeed")

	content, err := os.ReadFile(mmExportPath)
	require.NoError(t, err)

	var directChannelLines []map[string]json.RawMessage
	var directPostLines []map[string]json.RawMessage
	for _, line := range strings.Split(strings.TrimSpace(string(content)), "\n") {
		if line == "" {
			continue
		}
		var entry map[string]json.RawMessage
		require.NoError(t, json.Unmarshal([]byte(line), &entry))
		switch string(entry["type"]) {
		case `"direct_channel"`:
			directChannelLines = append(directChannelLines, entry)
		case `"direct_post"`:
			directPostLines = append(directPostLines, entry)
		}
	}

	require.Len(t, directChannelLines, 1,
		"two MPIMs with identical member sets should be deduplicated to one direct_channel line")

	var dc struct {
		Members []string `json:"members"`
	}
	require.NoError(t, json.Unmarshal(directChannelLines[0]["direct_channel"], &dc))
	sortedMembers := append([]string{}, dc.Members...)
	sort.Strings(sortedMembers)
	assert.Equal(t, []string{"alice", "bob", "charlie"}, sortedMembers,
		"deduplicated channel should contain all three unique members")

	require.Len(t, directPostLines, 4,
		"posts from both Slack MPIMs should survive dedup (2 + 2 = 4)")
	for _, post := range directPostLines {
		var dp struct {
			ChannelMembers []string `json:"channel_members"`
		}
		require.NoError(t, json.Unmarshal(post["direct_post"], &dp))
		require.Len(t, dp.ChannelMembers, 3,
			"every direct_post must carry the full 3-member set so the server routes it to the canonical channel")
	}

	validationResult := th.ValidateImportFileOrFail(ctx, mmExportPath)
	assert.Equal(t, uint64(3), validationResult.UserCount, "all three users should validate")

	// Import must succeed. Without dedup the server crashes with
	// `ChannelMember not found` when the second direct_channel line hits a
	// channel-hash collision with incomplete membership state.
	require.NoError(t, th.ImportBulkData(ctx, mmExportPath),
		"import of deduplicated MPIM should succeed without ChannelMember errors")

	alice := th.AssertUserExists(ctx, "alice")
	bobUser := th.AssertUserExists(ctx, "bob")
	charlie := th.AssertUserExists(ctx, "charlie")

	// Locate the imported group channel by enumerating alice's channels and
	// picking the type-G one whose member set is exactly {alice, bob, charlie}.
	// We avoid CreateGroupChannel here because that endpoint adds the calling
	// user (the test admin) to the member set and would return a different
	// 4-member channel rather than the imported 3-member one.
	aliceChannels, _, err := th.Client.GetChannelsForUserWithLastDeleteAt(ctx, alice.Id, 0)
	require.NoError(t, err)

	expectedMembers := map[string]bool{alice.Id: true, bobUser.Id: true, charlie.Id: true}
	var gmChannel *model.Channel
	for _, ch := range aliceChannels {
		if ch.Type != model.ChannelTypeGroup {
			continue
		}
		members, mErr := th.GetChannelMembers(ctx, ch.Id)
		require.NoError(t, mErr)
		if len(members) != len(expectedMembers) {
			continue
		}
		match := true
		for _, m := range members {
			if !expectedMembers[m.UserId] {
				match = false
				break
			}
		}
		if match {
			gmChannel = ch
			break
		}
	}
	require.NotNil(t, gmChannel,
		"a group channel with exactly {alice, bob, charlie} should exist after import")

	gmPosts, err := th.GetChannelPosts(ctx, gmChannel.Id, 0, 100)
	require.NoError(t, err)

	expectedMessages := []string{
		"Message from the first MPIM",
		"Reply in the first MPIM",
		"Message from the second MPIM",
		"Another message from the second MPIM",
	}
	found := map[string]bool{}
	for _, postID := range gmPosts.Order {
		for _, expected := range expectedMessages {
			if strings.Contains(gmPosts.Posts[postID].Message, expected) {
				found[expected] = true
			}
		}
	}
	for _, expected := range expectedMessages {
		assert.True(t, found[expected],
			"posts from both Slack MPIMs should be imported; missing: %q", expected)
	}
}

// TestTransformSlackE2EMpimsNotMergedWhenMembersDiffer is the non-happy-path
// counterpart to TestTransformSlackE2EMpimDedup: when two Slack MPIMs share
// some — but not all — members, the dedup pass must NOT merge them. Mattermost
// keys group channels by full member-set hash, so {alice,bob,charlie} and
// {alice,bob,dave} are independent channels and both must survive into the
// JSONL and into the imported Mattermost state.
func TestTransformSlackE2EMpimsNotMergedWhenMembersDiffer(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	th := testhelper.SetupHelper(t)
	defer th.TearDown()
	t.Cleanup(func() { os.Remove(transformLogFile) })

	ctx := context.Background()
	tempDir := t.TempDir()
	slackExportPath := filepath.Join(tempDir, "slack_export.zip")
	mmExportPath := filepath.Join(tempDir, "mattermost_import.jsonl")
	teamName := uniqueTeamName("mpimno")

	require.NoError(t, testhelper.ExportWithOverlappingMpims().Build(slackExportPath),
		"failed to build overlapping-MPIM Slack export fixture")

	team := th.CreateTeam(ctx, teamName, "MPIM No-Merge E2E Team")
	require.NotNil(t, team)

	c := commands.RootCmd
	resetCobraFlags(c)
	c.SetArgs([]string{
		"transform", "slack",
		"--team", teamName,
		"--file", slackExportPath,
		"--output", mmExportPath,
		"--skip-attachments",
	})
	require.NoError(t, c.Execute(), "transform command should succeed")

	content, err := os.ReadFile(mmExportPath)
	require.NoError(t, err)

	var directChannelMemberSets [][]string
	var directPostCount int
	for _, line := range strings.Split(strings.TrimSpace(string(content)), "\n") {
		if line == "" {
			continue
		}
		var entry map[string]json.RawMessage
		require.NoError(t, json.Unmarshal([]byte(line), &entry))
		switch string(entry["type"]) {
		case `"direct_channel"`:
			var dc struct {
				Members []string `json:"members"`
			}
			require.NoError(t, json.Unmarshal(entry["direct_channel"], &dc))
			sorted := append([]string{}, dc.Members...)
			sort.Strings(sorted)
			directChannelMemberSets = append(directChannelMemberSets, sorted)
		case `"direct_post"`:
			directPostCount++
		}
	}

	require.Len(t, directChannelMemberSets, 2,
		"two distinct member sets must produce two direct_channel lines — dedup should not collapse non-identical sets")

	// Order the two emitted sets deterministically before asserting content.
	sort.Slice(directChannelMemberSets, func(i, j int) bool {
		return strings.Join(directChannelMemberSets[i], ",") < strings.Join(directChannelMemberSets[j], ",")
	})
	assert.Equal(t, []string{"alice", "bob", "charlie"}, directChannelMemberSets[0])
	assert.Equal(t, []string{"alice", "bob", "dave"}, directChannelMemberSets[1])

	assert.Equal(t, 2, directPostCount, "one post per MPIM should survive (no merging)")

	require.NoError(t, th.ImportBulkData(ctx, mmExportPath), "import should succeed")

	alice := th.AssertUserExists(ctx, "alice")
	bobUser := th.AssertUserExists(ctx, "bob")
	charlie := th.AssertUserExists(ctx, "charlie")
	dave := th.AssertUserExists(ctx, "dave")

	aliceChannels, _, err := th.Client.GetChannelsForUserWithLastDeleteAt(ctx, alice.Id, 0)
	require.NoError(t, err)

	// Build a {sorted-userIDs → channel} index over alice's group channels.
	gmByMembers := map[string]*model.Channel{}
	for _, ch := range aliceChannels {
		if ch.Type != model.ChannelTypeGroup {
			continue
		}
		members, mErr := th.GetChannelMembers(ctx, ch.Id)
		require.NoError(t, mErr)
		ids := make([]string, 0, len(members))
		for _, m := range members {
			ids = append(ids, m.UserId)
		}
		sort.Strings(ids)
		gmByMembers[strings.Join(ids, ",")] = ch
	}

	charlieKey := joinSorted(alice.Id, bobUser.Id, charlie.Id)
	daveKey := joinSorted(alice.Id, bobUser.Id, dave.Id)
	require.NotNil(t, gmByMembers[charlieKey],
		"GM for {alice,bob,charlie} should exist in Mattermost as a distinct channel")
	require.NotNil(t, gmByMembers[daveKey],
		"GM for {alice,bob,dave} should exist in Mattermost as a distinct channel")
	assert.NotEqual(t, gmByMembers[charlieKey].Id, gmByMembers[daveKey].Id,
		"the two GMs must be distinct Mattermost channels")

	charlieGMPosts, err := th.GetChannelPosts(ctx, gmByMembers[charlieKey].Id, 0, 100)
	require.NoError(t, err)
	daveGMPosts, err := th.GetChannelPosts(ctx, gmByMembers[daveKey].Id, 0, 100)
	require.NoError(t, err)

	assert.True(t, anyPostContains(charlieGMPosts, "Hi charlie group"),
		"the {alice,bob,charlie} GM should contain its own post")
	assert.False(t, anyPostContains(charlieGMPosts, "Hi dave group"),
		"the {alice,bob,charlie} GM must NOT contain posts from the {alice,bob,dave} GM")
	assert.True(t, anyPostContains(daveGMPosts, "Hi dave group"),
		"the {alice,bob,dave} GM should contain its own post")
	assert.False(t, anyPostContains(daveGMPosts, "Hi charlie group"),
		"the {alice,bob,dave} GM must NOT contain posts from the {alice,bob,charlie} GM")
}

// TestTransformSlackE2EGuestImport verifies that Slack guests (is_restricted
// and is_ultra_restricted) are imported as Mattermost guests, with the guest
// role applied consistently at the system, team, and channel level, while
// regular Slack users keep full member roles.
func TestTransformSlackE2EGuestImport(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	th := testhelper.SetupHelper(t)
	defer th.TearDown()
	t.Cleanup(func() { os.Remove(transformLogFile) })

	ctx := context.Background()
	tempDir := t.TempDir()
	slackExportPath := filepath.Join(tempDir, "slack_export.zip")
	mmExportPath := filepath.Join(tempDir, "mattermost_import.jsonl")
	teamName := uniqueTeamName("guests")

	err := testhelper.ExportWithGuests().Build(slackExportPath)
	require.NoError(t, err, "failed to create Slack export fixture")

	team := th.CreateTeam(ctx, teamName, "Guests E2E Team")
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
	require.NoError(t, err, "transform command should succeed")

	err = th.ImportBulkData(ctx, mmExportPath)
	require.NoError(t, err, "import should succeed")

	regularUser := th.AssertUserExists(ctx, "regular.user")
	multiGuest := th.AssertUserExists(ctx, "multi.guest")
	singleGuest := th.AssertUserExists(ctx, "single.guest")

	assert.False(t, regularUser.IsGuest(), "regular.user should not be a guest")
	assert.True(t, multiGuest.IsGuest(), "multi.guest (is_restricted) should be imported as a guest")
	assert.True(t, singleGuest.IsGuest(), "single.guest (is_ultra_restricted) should be imported as a guest")

	// Team-level roles must match the guest status of each user.
	teamMembers, err := th.GetTeamMembers(ctx, team.Id)
	require.NoError(t, err)
	teamMembersByUserID := map[string]*model.TeamMember{}
	for _, tm := range teamMembers {
		teamMembersByUserID[tm.UserId] = tm
	}
	require.NotNil(t, teamMembersByUserID[regularUser.Id])
	require.NotNil(t, teamMembersByUserID[multiGuest.Id])
	require.NotNil(t, teamMembersByUserID[singleGuest.Id])
	assert.True(t, teamMembersByUserID[regularUser.Id].SchemeUser)
	assert.False(t, teamMembersByUserID[regularUser.Id].SchemeGuest)
	assert.True(t, teamMembersByUserID[multiGuest.Id].SchemeGuest)
	assert.False(t, teamMembersByUserID[multiGuest.Id].SchemeUser)
	assert.True(t, teamMembersByUserID[singleGuest.Id].SchemeGuest)
	assert.False(t, teamMembersByUserID[singleGuest.Id].SchemeUser)

	// Channel-level roles: the multi-channel guest should be a guest in both
	// "general" and "random"; the single-channel guest only in "general".
	generalChannel := th.AssertChannelExists(ctx, teamName, "general")
	randomChannel := th.AssertChannelExists(ctx, teamName, "random")

	generalMembers, err := th.GetChannelMembers(ctx, generalChannel.Id)
	require.NoError(t, err)
	generalByUserID := map[string]*model.ChannelMember{}
	for i := range generalMembers {
		generalByUserID[generalMembers[i].UserId] = &generalMembers[i]
	}
	require.NotNil(t, generalByUserID[regularUser.Id])
	require.NotNil(t, generalByUserID[multiGuest.Id])
	require.NotNil(t, generalByUserID[singleGuest.Id])
	assert.False(t, generalByUserID[regularUser.Id].SchemeGuest)
	assert.True(t, generalByUserID[multiGuest.Id].SchemeGuest)
	assert.True(t, generalByUserID[singleGuest.Id].SchemeGuest)

	randomMembers, err := th.GetChannelMembers(ctx, randomChannel.Id)
	require.NoError(t, err)
	randomByUserID := map[string]*model.ChannelMember{}
	for i := range randomMembers {
		randomByUserID[randomMembers[i].UserId] = &randomMembers[i]
	}
	require.NotNil(t, randomByUserID[regularUser.Id])
	require.NotNil(t, randomByUserID[multiGuest.Id])
	_, singleGuestInRandom := randomByUserID[singleGuest.Id]
	assert.False(t, singleGuestInRandom, "single.guest should not be a member of random, matching its Slack access scope")
	assert.False(t, randomByUserID[regularUser.Id].SchemeGuest)
	assert.True(t, randomByUserID[multiGuest.Id].SchemeGuest)

	// DM channels are a separate import path (direct_channel/DirectChannelMemberImportData)
	// from regular channels, so a guest's scheme flags there must be checked
	// independently: a guest appearing only in a DM/MPIM should still be
	// scheme_guest in that channel, not scheme_user.
	regularUserChannels, _, err := th.Client.GetChannelsForUserWithLastDeleteAt(ctx, regularUser.Id, 0)
	require.NoError(t, err)

	expectedDMMembers := map[string]bool{regularUser.Id: true, multiGuest.Id: true}
	var dmChannel *model.Channel
	for _, ch := range regularUserChannels {
		if ch.Type != model.ChannelTypeDirect {
			continue
		}
		members, mErr := th.GetChannelMembers(ctx, ch.Id)
		require.NoError(t, mErr)
		if len(members) != len(expectedDMMembers) {
			continue
		}
		match := true
		for _, m := range members {
			if !expectedDMMembers[m.UserId] {
				match = false
				break
			}
		}
		if match {
			dmChannel = ch
			break
		}
	}
	require.NotNil(t, dmChannel, "DM between regular.user and multi.guest should exist in Mattermost")

	dmMembers, err := th.GetChannelMembers(ctx, dmChannel.Id)
	require.NoError(t, err)
	dmByUserID := map[string]*model.ChannelMember{}
	for i := range dmMembers {
		dmByUserID[dmMembers[i].UserId] = &dmMembers[i]
	}
	require.NotNil(t, dmByUserID[regularUser.Id])
	require.NotNil(t, dmByUserID[multiGuest.Id])
	assert.True(t, dmByUserID[regularUser.Id].SchemeUser)
	assert.False(t, dmByUserID[regularUser.Id].SchemeGuest)
	assert.False(t, dmByUserID[multiGuest.Id].SchemeUser, "guest DM participant must not be scheme_user")
	assert.True(t, dmByUserID[multiGuest.Id].SchemeGuest, "guest DM participant must be scheme_guest")
}

// joinSorted returns a deterministic comma-joined key from the given strings.
func joinSorted(s ...string) string {
	cp := append([]string{}, s...)
	sort.Strings(cp)
	return strings.Join(cp, ",")
}

// anyPostContains reports whether any post in list contains substring.
func anyPostContains(list *model.PostList, substring string) bool {
	for _, id := range list.Order {
		if strings.Contains(list.Posts[id].Message, substring) {
			return true
		}
	}
	return false
}
