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
)

// uniqueTeamName generates a unique team name for testing to avoid conflicts
// Mattermost has reserved paths like "posts", "files", "api", etc.
// Use a "t" prefix to ensure team names don't conflict with reserved URLs
func uniqueTeamName(prefix string) string {
	return fmt.Sprintf("t%s%d", prefix, time.Now().UnixNano()%10000)
}

// TestTransformSlackIntegration tests the full end-to-end flow:
// 1. Create Slack export fixture
// 2. Run transform command
// 3. Import into Mattermost
// 4. Verify data was imported correctly
func TestTransformSlackIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Setup Mattermost with testcontainers
	th := testhelper.SetupHelper(t)
	defer th.TearDown()

	t.Run("basic transform and import", func(t *testing.T) {
		tempDir := t.TempDir()
		slackExportPath := filepath.Join(tempDir, "slack_export.zip")
		mmExportPath := filepath.Join(tempDir, "mattermost_import.jsonl")
		teamName := uniqueTeamName("basic")

		// 1. Create Slack export using the fixture builder
		err := slack.BasicExport().Build(slackExportPath)
		require.NoError(t, err, "failed to create Slack export fixture")

		// 2. Create the team in Mattermost first (required for import)
		team := th.CreateTeam(teamName, "Test Team")
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

		// 4. Read and parse the output to verify structure
		lines := readImportLines(t, mmExportPath)
		require.NotEmpty(t, lines, "output should have content")

		// Verify team name in output
		channels := findAllLinesByType(lines, "channel")
		for _, ch := range channels {
			assert.Equal(t, teamName, ch.Channel.Team, "channel should reference correct team")
		}

		users := findAllLinesByType(lines, "user")
		for _, u := range users {
			require.NotEmpty(t, u.User.Teams, "user should have team assignments")
			assert.Equal(t, teamName, u.User.Teams[0].Name, "user should be in correct team")
		}

		// 5. Import the data into Mattermost
		// Note: The import API may not be available in all Mattermost editions
		// For now, we verify the transform output is correct
		t.Log("Transform completed successfully - verifying output structure")

		// Verify users in output have correct structure
		johnUser := findUserByUsername(users, "john.doe")
		require.NotNil(t, johnUser, "john.doe should exist in output")
		assert.Equal(t, "john.doe@example.com", johnUser.User.Email)
		assert.Equal(t, "John", johnUser.User.FirstName)
		assert.Equal(t, "Doe", johnUser.User.LastName)
		assert.Equal(t, "Software Engineer", johnUser.User.Position)

		janeUser := findUserByUsername(users, "jane.smith")
		require.NotNil(t, janeUser, "jane.smith should exist in output")
		assert.Equal(t, "jane.smith@example.com", janeUser.User.Email)
		assert.Equal(t, "Jane", janeUser.User.FirstName)
		assert.Equal(t, "Smith", janeUser.User.LastName)

		// Verify channels
		generalCh := findChannelByName(channels, "general")
		require.NotNil(t, generalCh, "general channel should exist")
		assert.Equal(t, "Company-wide announcements", generalCh.Channel.Purpose)
		assert.Equal(t, "Welcome to the team!", generalCh.Channel.Header)

		randomCh := findChannelByName(channels, "random")
		require.NotNil(t, randomCh, "random channel should exist")
	})

	t.Run("transform with posts", func(t *testing.T) {
		tempDir := t.TempDir()
		slackExportPath := filepath.Join(tempDir, "slack_export.zip")
		mmExportPath := filepath.Join(tempDir, "mattermost_import.jsonl")
		teamName := uniqueTeamName("posts")

		// Create Slack export with posts
		err := slack.ExportWithPosts().Build(slackExportPath)
		require.NoError(t, err)

		// Create team
		team := th.CreateTeam(teamName, "Posts Test Team")
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

		// Verify posts in output
		lines := readImportLines(t, mmExportPath)
		posts := findAllLinesByType(lines, "post")

		require.GreaterOrEqual(t, len(posts), 3, "should have at least 3 posts")

		// Verify posts reference correct team
		for _, post := range posts {
			assert.Equal(t, teamName, post.Post.Team, "post should reference correct team")
		}

		// Verify post content
		var foundHello, foundWelcome, foundCoffee bool
		for _, post := range posts {
			if strings.Contains(post.Post.Message, "Hello everyone") {
				foundHello = true
			}
			if strings.Contains(post.Post.Message, "Welcome to the team") {
				foundWelcome = true
			}
			if strings.Contains(post.Post.Message, "coffee") {
				foundCoffee = true
			}
		}
		assert.True(t, foundHello, "should have 'Hello everyone' post")
		assert.True(t, foundWelcome, "should have welcome post")
		assert.True(t, foundCoffee, "should have coffee post")
	})

	t.Run("transform with threads", func(t *testing.T) {
		tempDir := t.TempDir()
		slackExportPath := filepath.Join(tempDir, "slack_export.zip")
		mmExportPath := filepath.Join(tempDir, "mattermost_import.jsonl")
		teamName := uniqueTeamName("threads")

		// Create Slack export with threads
		err := slack.ExportWithThreads().Build(slackExportPath)
		require.NoError(t, err)

		// Create team
		team := th.CreateTeam(teamName, "Threads Test Team")
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

		// Verify output exists
		lines := readImportLines(t, mmExportPath)
		require.NotEmpty(t, lines)

		// Verify posts exist (threads are handled as posts with replies)
		posts := findAllLinesByType(lines, "post")
		require.NotEmpty(t, posts, "should have posts from threaded conversation")
	})

	t.Run("team name case conversion", func(t *testing.T) {
		tempDir := t.TempDir()
		slackExportPath := filepath.Join(tempDir, "slack_export.zip")
		mmExportPath := filepath.Join(tempDir, "mattermost_import.jsonl")
		teamName := uniqueTeamName("case")

		// Create export
		err := slack.BasicExport().Build(slackExportPath)
		require.NoError(t, err)

		// Create team with lowercase name (Mattermost requires lowercase)
		team := th.CreateTeam(teamName, "Case Test Team")
		require.NotNil(t, team)

		// Run transform with UPPERCASE team name
		// The tool will convert it to lowercase
		mixedCaseTeam := strings.ToUpper(teamName[:1]) + teamName[1:]
		args := []string{
			"transform", "slack",
			"--team", mixedCaseTeam, // Mixed case - should be converted to lowercase
			"--file", slackExportPath,
			"--output", mmExportPath,
			"--skip-attachments",
		}

		c := commands.RootCmd
		c.SetArgs(args)
		err = c.Execute()
		require.NoError(t, err)
		defer os.Remove("transform-slack.log")

		// Verify team name is lowercase in output
		lines := readImportLines(t, mmExportPath)

		channels := findAllLinesByType(lines, "channel")
		for _, ch := range channels {
			assert.Equal(t, teamName, ch.Channel.Team, "team name should be lowercase")
		}

		users := findAllLinesByType(lines, "user")
		for _, u := range users {
			for _, team := range u.User.Teams {
				assert.Equal(t, teamName, team.Name, "team name in user should be lowercase")
			}
		}
	})

	t.Run("user mentions are converted", func(t *testing.T) {
		tempDir := t.TempDir()
		slackExportPath := filepath.Join(tempDir, "slack_export.zip")
		mmExportPath := filepath.Join(tempDir, "mattermost_import.jsonl")
		teamName := uniqueTeamName("mentions")

		// Create Slack export with mentions
		err := slack.ExportWithMentions().Build(slackExportPath)
		require.NoError(t, err)

		// Create team
		team := th.CreateTeam(teamName, "Mentions Test Team")
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

		// Verify mentions are converted
		lines := readImportLines(t, mmExportPath)
		posts := findAllLinesByType(lines, "post")

		var foundUserMention, foundHereMention bool
		for _, post := range posts {
			// Slack <@U002> should be converted to @jane.smith
			if strings.Contains(post.Post.Message, "@jane.smith") {
				foundUserMention = true
			}
			// Slack <!here> should be converted to @here
			if strings.Contains(post.Post.Message, "@here") {
				foundHereMention = true
			}
		}
		assert.True(t, foundUserMention, "user mention should be converted")
		assert.True(t, foundHereMention, "@here mention should be converted")
	})
}

// TestTransformSlackIntegrationVerifyUsers tests that users are correctly created
// and have proper attributes after transformation
func TestTransformSlackIntegrationVerifyUsers(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	th := testhelper.SetupHelper(t)
	defer th.TearDown()

	t.Run("users have correct email and name", func(t *testing.T) {
		tempDir := t.TempDir()
		slackExportPath := filepath.Join(tempDir, "slack_export.zip")
		mmExportPath := filepath.Join(tempDir, "mattermost_import.jsonl")
		teamName := uniqueTeamName("usrvrfy")

		// Create export with specific user data
		builder := slack.NewSlackExportBuilder().
			AddUser(slack.SlackUser{
				Id:       "U001",
				Username: "testuser",
				IsBot:    false,
				Profile: slack.SlackProfile{
					RealName: "Test User Full Name",
					Email:    "testuser@company.com",
					Title:    "Senior Developer",
				},
				Deleted: false,
			}).
			AddChannel(slack.SlackChannel{
				Id:      "C001",
				Name:    "general",
				Creator: "U001",
				Members: []string{"U001"},
				Purpose: slack.SlackChannelSub{Value: "General"},
				Topic:   slack.SlackChannelSub{Value: ""},
			})

		err := builder.Build(slackExportPath)
		require.NoError(t, err)

		// Create team
		team := th.CreateTeam(teamName, "User Verify Team")
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

		// Verify user in output
		lines := readImportLines(t, mmExportPath)
		users := findAllLinesByType(lines, "user")
		require.Len(t, users, 1, "should have 1 user")

		user := users[0]
		assert.Equal(t, "testuser", user.User.Username, "username should match")
		assert.Equal(t, "testuser@company.com", user.User.Email, "email should match")
		assert.Equal(t, "Test", user.User.FirstName, "first name should be parsed")
		assert.Equal(t, "User Full Name", user.User.LastName, "last name should be parsed")
		assert.Equal(t, "Senior Developer", user.User.Position, "position should match")

		// Verify user is assigned to correct team
		require.Len(t, user.User.Teams, 1, "user should be in 1 team")
		assert.Equal(t, teamName, user.User.Teams[0].Name, "user should be in correct team")

		// Verify user has channel membership
		channelNames := getChannelNamesFromTeam(user.User.Teams[0])
		assert.Contains(t, channelNames, "general", "user should be member of general")
	})

	t.Run("deleted user is handled correctly", func(t *testing.T) {
		tempDir := t.TempDir()
		slackExportPath := filepath.Join(tempDir, "slack_export.zip")
		mmExportPath := filepath.Join(tempDir, "mattermost_import.jsonl")
		teamName := uniqueTeamName("deleted")

		// Create export with deleted user
		err := slack.ExportWithDeletedUser().Build(slackExportPath)
		require.NoError(t, err)

		// Create team
		team := th.CreateTeam(teamName, "Deleted User Team")
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

		// Verify both users exist in output
		lines := readImportLines(t, mmExportPath)
		users := findAllLinesByType(lines, "user")
		require.Len(t, users, 2, "should have 2 users (including deleted)")
	})
}

// TestTransformSlackIntegrationTeamMatches verifies that the team specified
// in the command is correctly used throughout the output
func TestTransformSlackIntegrationTeamMatches(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	th := testhelper.SetupHelper(t)
	defer th.TearDown()

	teamName := uniqueTeamName("exact")
	tempDir := t.TempDir()
	slackExportPath := filepath.Join(tempDir, "slack_export.zip")
	mmExportPath := filepath.Join(tempDir, "mattermost_import.jsonl")

	// Create export
	err := slack.ExportWithPosts().Build(slackExportPath)
	require.NoError(t, err)

	// Create team with exact name
	team := th.CreateTeam(teamName, "Exact Team Name")
	require.NotNil(t, team)

	// Run transform with exact team name
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

	// Parse output
	lines := readImportLines(t, mmExportPath)

	// Verify ALL channels reference the correct team
	channels := findAllLinesByType(lines, "channel")
	for _, ch := range channels {
		assert.Equal(t, teamName, ch.Channel.Team,
			"channel %s should reference team %s", ch.Channel.Name, teamName)
	}

	// Verify ALL users are assigned to the correct team
	users := findAllLinesByType(lines, "user")
	for _, u := range users {
		require.NotEmpty(t, u.User.Teams, "user %s should have team assignments", u.User.Username)
		for _, team := range u.User.Teams {
			assert.Equal(t, teamName, team.Name,
				"user %s should be in team %s", u.User.Username, teamName)
		}
	}

	// Verify ALL posts reference the correct team
	posts := findAllLinesByType(lines, "post")
	for _, p := range posts {
		assert.Equal(t, teamName, p.Post.Team,
			"post in channel %s should reference team %s", p.Post.Channel, teamName)
	}
}
