package commands_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mattermost/mmetl/commands"
	"github.com/mattermost/mmetl/services/slack"
	"github.com/mattermost/mmetl/testhelper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

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
		tempDir := t.TempDir()
		slackExportPath := filepath.Join(tempDir, "slack_export.zip")
		mmExportPath := filepath.Join(tempDir, "mattermost_import.jsonl")
		teamName := uniqueTeamName("e2e")

		// 1. Create Slack export fixture
		err := slack.BasicExport().Build(slackExportPath)
		require.NoError(t, err, "failed to create Slack export fixture")

		// 2. Create the team in Mattermost first (required for import)
		team := th.CreateTeam(teamName, "E2E Test Team")
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
		c.SetArgs(args)
		err = c.Execute()
		require.NoError(t, err, "transform command should succeed")
		defer os.Remove("transform-slack.log")

		// Verify output file was created
		_, err = os.Stat(mmExportPath)
		require.NoError(t, err, "output file should exist")

		// 4. Validate the JSONL file (similar to mmctl import validate)
		t.Log("Validating import file...")
		validationResult := th.ValidateImportFileOrFail(mmExportPath)
		assert.Equal(t, uint64(2), validationResult.UserCount, "should have 2 users")
		assert.Equal(t, uint64(2), validationResult.ChannelCount, "should have 2 channels")

		// 5. Import the JSONL into Mattermost
		t.Log("Importing data into Mattermost...")
		err = th.ImportBulkData(mmExportPath)
		require.NoError(t, err, "import should succeed")

		// 5. Verify users were created in Mattermost
		t.Log("Verifying users in Mattermost...")
		johnUser := th.AssertUserExists("john.doe")
		assert.Equal(t, "john.doe@example.com", johnUser.Email, "john.doe should have correct email")
		assert.Equal(t, "John", johnUser.FirstName, "john.doe should have correct first name")
		assert.Equal(t, "Doe", johnUser.LastName, "john.doe should have correct last name")
		assert.Equal(t, "Software Engineer", johnUser.Position, "john.doe should have correct position")

		janeUser := th.AssertUserExists("jane.smith")
		assert.Equal(t, "jane.smith@example.com", janeUser.Email, "jane.smith should have correct email")
		assert.Equal(t, "Jane", janeUser.FirstName, "jane.smith should have correct first name")
		assert.Equal(t, "Smith", janeUser.LastName, "jane.smith should have correct last name")
		assert.Equal(t, "Product Manager", janeUser.Position, "jane.smith should have correct position")

		// 6. Verify channels were created in Mattermost
		t.Log("Verifying channels in Mattermost...")
		generalChannel := th.AssertChannelExists(teamName, "general")
		assert.Equal(t, "Company-wide announcements", generalChannel.Purpose)
		assert.Equal(t, "Welcome to the team!", generalChannel.Header)

		randomChannel := th.AssertChannelExists(teamName, "random")
		assert.Equal(t, "Non-work banter", randomChannel.Purpose)
		assert.Equal(t, "Water cooler chat", randomChannel.Header)

		// 7. Verify users are members of the team
		t.Log("Verifying team memberships...")
		th.AssertUserInTeam(team.Id, johnUser.Id)
		th.AssertUserInTeam(team.Id, janeUser.Id)

		// 8. Verify users are members of channels
		t.Log("Verifying channel memberships...")
		generalMembers, err := th.GetChannelMembers(generalChannel.Id)
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
		tempDir := t.TempDir()
		slackExportPath := filepath.Join(tempDir, "slack_export.zip")
		mmExportPath := filepath.Join(tempDir, "mattermost_import.jsonl")
		teamName := uniqueTeamName("posts")

		// 1. Create Slack export with posts
		err := slack.ExportWithPosts().Build(slackExportPath)
		require.NoError(t, err)

		// 2. Create team
		team := th.CreateTeam(teamName, "Posts E2E Team")
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
		c.SetArgs(args)
		err = c.Execute()
		require.NoError(t, err)
		defer os.Remove("transform-slack.log")

		// 4. Import into Mattermost
		t.Log("Importing data with posts into Mattermost...")
		err = th.ImportBulkData(mmExportPath)
		require.NoError(t, err, "import should succeed")

		// 5. Verify posts were created in Mattermost
		t.Log("Verifying posts in Mattermost...")
		generalChannel := th.AssertChannelExists(teamName, "general")

		posts, err := th.GetChannelPosts(generalChannel.Id, 0, 100)
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
		randomChannel := th.AssertChannelExists(teamName, "random")
		randomPosts, err := th.GetChannelPosts(randomChannel.Id, 0, 100)
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
		tempDir := t.TempDir()
		slackExportPath := filepath.Join(tempDir, "slack_export.zip")
		mmExportPath := filepath.Join(tempDir, "mattermost_import.jsonl")
		teamName := uniqueTeamName("mentions")

		// 1. Create Slack export with mentions
		err := slack.ExportWithMentions().Build(slackExportPath)
		require.NoError(t, err)

		// 2. Create team
		team := th.CreateTeam(teamName, "Mentions E2E Team")
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
		c.SetArgs(args)
		err = c.Execute()
		require.NoError(t, err)
		defer os.Remove("transform-slack.log")

		// 4. Import into Mattermost
		t.Log("Importing data with mentions into Mattermost...")
		err = th.ImportBulkData(mmExportPath)
		require.NoError(t, err, "import should succeed")

		// 5. Verify mentions were converted correctly
		t.Log("Verifying mentions in Mattermost...")
		generalChannel := th.AssertChannelExists(teamName, "general")

		posts, err := th.GetChannelPosts(generalChannel.Id, 0, 100)
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
		tempDir := t.TempDir()
		slackExportPath := filepath.Join(tempDir, "slack_export.zip")
		mmExportPath := filepath.Join(tempDir, "mattermost_import.jsonl")
		teamName := uniqueTeamName("deleted")

		// 1. Create Slack export with deleted user
		err := slack.ExportWithDeletedUser().Build(slackExportPath)
		require.NoError(t, err)

		// 2. Create team
		team := th.CreateTeam(teamName, "Deleted User E2E Team")
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
		c.SetArgs(args)
		err = c.Execute()
		require.NoError(t, err)
		defer os.Remove("transform-slack.log")

		// 4. Import into Mattermost
		t.Log("Importing data with deleted user into Mattermost...")
		err = th.ImportBulkData(mmExportPath)
		require.NoError(t, err, "import should succeed")

		// 5. Verify active user exists and is active
		t.Log("Verifying users in Mattermost...")
		activeUser := th.AssertUserExists("john.doe")
		assert.Equal(t, int64(0), activeUser.DeleteAt, "active user should not be deleted")

		// 6. Verify deleted user exists and is deactivated
		deletedUser := th.AssertUserExists("deleted.user")
		assert.NotEqual(t, int64(0), deletedUser.DeleteAt, "deleted user should have DeleteAt set")
	})
}

// TestTransformSlackE2ETeamConsistency verifies that the team specified
// in the command is consistently applied to all imported entities
func TestTransformSlackE2ETeamConsistency(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	th := testhelper.SetupHelper(t)
	defer th.TearDown()

	teamName := uniqueTeamName("consist")
	tempDir := t.TempDir()
	slackExportPath := filepath.Join(tempDir, "slack_export.zip")
	mmExportPath := filepath.Join(tempDir, "mattermost_import.jsonl")

	// Create export with posts
	err := slack.ExportWithPosts().Build(slackExportPath)
	require.NoError(t, err)

	// Create team
	team := th.CreateTeam(teamName, "Consistency E2E Team")
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
	c.SetArgs(args)
	err = c.Execute()
	require.NoError(t, err)
	defer os.Remove("transform-slack.log")

	// Import into Mattermost
	err = th.ImportBulkData(mmExportPath)
	require.NoError(t, err)

	// Verify ALL channels are in the correct team
	generalChannel := th.AssertChannelExists(teamName, "general")
	assert.Equal(t, team.Id, generalChannel.TeamId, "general channel should be in correct team")

	randomChannel := th.AssertChannelExists(teamName, "random")
	assert.Equal(t, team.Id, randomChannel.TeamId, "random channel should be in correct team")

	// Verify ALL users are members of the team
	johnUser := th.AssertUserExists("john.doe")
	th.AssertUserInTeam(team.Id, johnUser.Id)

	janeUser := th.AssertUserExists("jane.smith")
	th.AssertUserInTeam(team.Id, janeUser.Id)
}

// TestTransformSlackWithCreateTeamIntegration tests team creation via the --create-team flag
func TestTransformSlackWithCreateTeamIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Setup Mattermost with testcontainers
	th := testhelper.SetupHelper(t)
	defer th.TearDown()

	t.Run("team is created in Mattermost when --create-team flag is set", func(t *testing.T) {
		tempDir := t.TempDir()
		slackExportPath := filepath.Join(tempDir, "slack_export.zip")
		mmExportPath := filepath.Join(tempDir, "mattermost_import.jsonl")
		teamName := uniqueTeamName("createteam")

		// 1. Create Slack export fixture
		err := slack.BasicExport().Build(slackExportPath)
		require.NoError(t, err)

		// 2. Run transform with --create-team flag
		args := []string{
			"transform", "slack",
			"--team", teamName,
			"--file", slackExportPath,
			"--output", mmExportPath,
			"--skip-attachments",
			"--create-team",
		}

		c := commands.RootCmd
		c.SetArgs(args)
		err = c.Execute()
		require.NoError(t, err)
		defer os.Remove("transform-slack.log")

		// 3. Import into Mattermost (no need to pre-create team)
		t.Log("Importing data with team creation into Mattermost...")
		err = th.ImportBulkData(mmExportPath)
		require.NoError(t, err, "import should succeed")

		// 4. Verify team was created in Mattermost
		t.Log("Verifying team was created in Mattermost...")
		team := th.AssertTeamExists(teamName)
		require.NotNil(t, team)

		// Verify team properties
		assert.Equal(t, teamName, team.Name)
		// Title case: "myteam" -> "Myteam"
		caser := cases.Title(language.English)
		expectedDisplayName := caser.String(teamName)
		assert.Equal(t, expectedDisplayName, team.DisplayName)
		// Type "I" means invite-only
		assert.Equal(t, "I", team.Type)

		// 5. Verify users were imported and added to the team
		t.Log("Verifying users in team...")
		johnUser := th.AssertUserExists("john.doe")
		th.AssertUserInTeam(team.Id, johnUser.Id)

		janeUser := th.AssertUserExists("jane.smith")
		th.AssertUserInTeam(team.Id, janeUser.Id)

		// 6. Verify channels were created in the team
		t.Log("Verifying channels in team...")
		generalChannel := th.AssertChannelExists(teamName, "general")
		assert.Equal(t, team.Id, generalChannel.TeamId)

		randomChannel := th.AssertChannelExists(teamName, "random")
		assert.Equal(t, team.Id, randomChannel.TeamId)
	})

	t.Run("team is not created when --create-team flag is not set", func(t *testing.T) {
		tempDir := t.TempDir()
		slackExportPath := filepath.Join(tempDir, "slack_export.zip")
		mmExportPath := filepath.Join(tempDir, "mattermost_import.jsonl")
		teamName := uniqueTeamName("nocreateteam")

		// 1. Create Slack export fixture
		err := slack.BasicExport().Build(slackExportPath)
		require.NoError(t, err)

		// 2. Run transform WITHOUT --create-team flag
		args := []string{
			"transform", "slack",
			"--team", teamName,
			"--file", slackExportPath,
			"--output", mmExportPath,
			"--skip-attachments",
			"--create-team=false",
		}

		c := commands.RootCmd
		c.SetArgs(args)
		err = c.Execute()
		require.NoError(t, err)
		defer os.Remove("transform-slack.log")

		// 3. Verify no team line in export file
		lines := readImportLines(t, mmExportPath)
		teamLine := findLineByType(lines, "team")
		require.Nil(t, teamLine, "should not have team line in export when flag is not set")

		// 4. Pre-create team and verify import succeeds
		t.Log("Pre-creating team and importing...")
		th.CreateTeam(teamName, "Test Team")
		err = th.ImportBulkData(mmExportPath)
		require.NoError(t, err, "import should succeed with pre-created team")

		// Verify team properties were NOT changed by import (should match what we created)
		updatedTeam := th.AssertTeamExists(teamName)
		assert.Equal(t, "Test Team", updatedTeam.DisplayName, "display name should not be changed by import")

		// Verify users were added to the team
		johnUser := th.AssertUserExists("john.doe")
		th.AssertUserInTeam(updatedTeam.Id, johnUser.Id)
	})
}

// Note: Helper types (ImportLine, etc.) and functions (readImportLines, etc.)
// are defined in transform_slack_e2e_test.go
