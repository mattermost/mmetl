package data_integrity

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/mattermost/mattermost/server/v8/channels/app/imports"
	log "github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mattermost/mmetl/testhelper"
)

func TestSyncImportUsersIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	th := testhelper.SetupHelper(t)
	defer th.TearDown()

	t.Run("user sync with no changes - user exists with same username and email", func(t *testing.T) {
		// Create user in Mattermost
		mmUser := th.CreateUser("testuser1", "testuser1@test.local")

		// Create import file with the same user
		inputFile := createTempImportFile(t, []imports.LineImportData{
			{
				Type: "user",
				User: &imports.UserImportData{
					Username: strPtr("testuser1"),
					Email:    strPtr("testuser1@test.local"),
				},
			},
		})
		defer os.Remove(inputFile)

		// Create output file
		outputFile := t.TempDir() + "/output.jsonl"

		// Run sync
		err := runSyncImportUsers(t, th, inputFile, outputFile)
		require.NoError(t, err)

		// Verify output - user should be unchanged
		outputLines := readOutputFile(t, outputFile)
		require.Len(t, outputLines, 1)

		var userData imports.LineImportData
		err = json.Unmarshal([]byte(outputLines[0]), &userData)
		require.NoError(t, err)

		assert.Equal(t, "user", userData.Type)
		assert.Equal(t, mmUser.Username, *userData.User.Username)
		assert.Equal(t, strings.ToLower(mmUser.Email), strings.ToLower(*userData.User.Email))
	})

	t.Run("email updated from database - username matches but email differs", func(t *testing.T) {
		// Create user in Mattermost with a specific email
		mmUser := th.CreateUser("testuser2", "dbuser2@test.local")

		// Create import file with the same username but different email
		inputFile := createTempImportFile(t, []imports.LineImportData{
			{
				Type: "user",
				User: &imports.UserImportData{
					Username: strPtr("testuser2"),
					Email:    strPtr("importuser2@test.local"), // Different email
				},
			},
		})
		defer os.Remove(inputFile)

		// Create output file
		outputFile := t.TempDir() + "/output.jsonl"

		// Run sync
		err := runSyncImportUsers(t, th, inputFile, outputFile)
		require.NoError(t, err)

		// Verify output - email should be updated to match database
		outputLines := readOutputFile(t, outputFile)
		require.Len(t, outputLines, 1)

		var userData imports.LineImportData
		err = json.Unmarshal([]byte(outputLines[0]), &userData)
		require.NoError(t, err)

		assert.Equal(t, "user", userData.Type)
		assert.Equal(t, mmUser.Username, *userData.User.Username)
		assert.Equal(t, strings.ToLower(mmUser.Email), strings.ToLower(*userData.User.Email))
	})

	t.Run("username updated from database - email matches but username differs", func(t *testing.T) {
		// Create user in Mattermost with a specific username
		mmUser := th.CreateUser("dbuser3", "testuser3@test.local")

		// Create import file with the same email but different username
		inputFile := createTempImportFile(t, []imports.LineImportData{
			{
				Type: "user",
				User: &imports.UserImportData{
					Username: strPtr("importuser3"), // Different username
					Email:    strPtr("testuser3@test.local"),
				},
			},
		})
		defer os.Remove(inputFile)

		// Create output file
		outputFile := t.TempDir() + "/output.jsonl"

		// Run sync
		err := runSyncImportUsers(t, th, inputFile, outputFile)
		require.NoError(t, err)

		// Verify output - username should be updated to match database
		outputLines := readOutputFile(t, outputFile)
		require.Len(t, outputLines, 1)

		var userData imports.LineImportData
		err = json.Unmarshal([]byte(outputLines[0]), &userData)
		require.NoError(t, err)

		assert.Equal(t, "user", userData.Type)
		assert.Equal(t, mmUser.Username, *userData.User.Username)
		assert.Equal(t, strings.ToLower(mmUser.Email), strings.ToLower(*userData.User.Email))
	})

	t.Run("username propagates to posts - when username changes, posts are updated", func(t *testing.T) {
		// Create user in Mattermost
		mmUser := th.CreateUser("dbuser4", "testuser4@test.local")

		// Create import file with a different username and a post by that username
		inputFile := createTempImportFile(t, []imports.LineImportData{
			{
				Type: "user",
				User: &imports.UserImportData{
					Username: strPtr("importuser4"), // Different username
					Email:    strPtr("testuser4@test.local"),
				},
			},
			{
				Type: "post",
				Post: &imports.PostImportData{
					User:    strPtr("importuser4"), // Uses import username
					Message: strPtr("Test message"),
				},
			},
		})
		defer os.Remove(inputFile)

		// Create output file
		outputFile := t.TempDir() + "/output.jsonl"

		// Run sync
		err := runSyncImportUsers(t, th, inputFile, outputFile)
		require.NoError(t, err)

		// Verify output
		outputLines := readOutputFile(t, outputFile)
		require.Len(t, outputLines, 2)

		// Check user line
		var userData imports.LineImportData
		err = json.Unmarshal([]byte(outputLines[0]), &userData)
		require.NoError(t, err)
		assert.Equal(t, "user", userData.Type)
		assert.Equal(t, mmUser.Username, *userData.User.Username)

		// Check post line - username should be updated
		var postData imports.LineImportData
		err = json.Unmarshal([]byte(outputLines[1]), &postData)
		require.NoError(t, err)
		assert.Equal(t, "post", postData.Type)
		assert.Equal(t, mmUser.Username, *postData.Post.User)
	})

	t.Run("direct channel members updated - member lists reflect username changes", func(t *testing.T) {
		// Create user in Mattermost
		mmUser := th.CreateUser("dbuser5", "testuser5@test.local")

		// Create import file with a different username and a direct channel with that username
		inputFile := createTempImportFile(t, []imports.LineImportData{
			{
				Type: "user",
				User: &imports.UserImportData{
					Username: strPtr("importuser5"), // Different username
					Email:    strPtr("testuser5@test.local"),
				},
			},
			{
				Type: "direct_channel",
				DirectChannel: &imports.DirectChannelImportData{
					Members: &[]string{"importuser5", "otheruser"}, // Uses import username
				},
			},
		})
		defer os.Remove(inputFile)

		// Create output file
		outputFile := t.TempDir() + "/output.jsonl"

		// Run sync
		err := runSyncImportUsers(t, th, inputFile, outputFile)
		require.NoError(t, err)

		// Verify output
		outputLines := readOutputFile(t, outputFile)
		require.Len(t, outputLines, 2)

		// Check user line
		var userData imports.LineImportData
		err = json.Unmarshal([]byte(outputLines[0]), &userData)
		require.NoError(t, err)
		assert.Equal(t, mmUser.Username, *userData.User.Username)

		// Check direct channel line - member username should be updated
		var dcData imports.LineImportData
		err = json.Unmarshal([]byte(outputLines[1]), &dcData)
		require.NoError(t, err)
		assert.Equal(t, "direct_channel", dcData.Type)
		require.NotNil(t, dcData.DirectChannel.Members)
		members := *dcData.DirectChannel.Members
		assert.Contains(t, members, mmUser.Username)
		assert.Contains(t, members, "otheruser")
		assert.NotContains(t, members, "importuser5") // Original username should be replaced
	})

	t.Run("direct post usernames and channel members are updated", func(t *testing.T) {
		// Create user in Mattermost
		mmUser := th.CreateUser("dbuser6", "testuser6@test.local")

		// Create import file with a different username and a direct post
		inputFile := createTempImportFile(t, []imports.LineImportData{
			{
				Type: "user",
				User: &imports.UserImportData{
					Username: strPtr("importuser6"), // Different username
					Email:    strPtr("testuser6@test.local"),
				},
			},
			{
				Type: "direct_post",
				DirectPost: &imports.DirectPostImportData{
					User:           strPtr("importuser6"), // Uses import username
					ChannelMembers: &[]string{"importuser6", "otheruser"},
					Message:        strPtr("Test direct message"),
				},
			},
		})
		defer os.Remove(inputFile)

		// Create output file
		outputFile := t.TempDir() + "/output.jsonl"

		// Run sync
		err := runSyncImportUsers(t, th, inputFile, outputFile)
		require.NoError(t, err)

		// Verify output
		outputLines := readOutputFile(t, outputFile)
		require.Len(t, outputLines, 2)

		// Check direct post line
		var dpData imports.LineImportData
		err = json.Unmarshal([]byte(outputLines[1]), &dpData)
		require.NoError(t, err)
		assert.Equal(t, "direct_post", dpData.Type)
		assert.Equal(t, mmUser.Username, *dpData.DirectPost.User)

		// Check channel members
		require.NotNil(t, dpData.DirectPost.ChannelMembers)
		members := *dpData.DirectPost.ChannelMembers
		assert.Contains(t, members, mmUser.Username)
		assert.Contains(t, members, "otheruser")
	})

	t.Run("duplicate user resolution - active user preferred over inactive", func(t *testing.T) {
		// Create two users: one with matching username, one with matching email
		// Then deactivate one
		usernameUser := th.CreateUser("conflictuser7", "different7@test.local")
		emailUser := th.CreateUser("differentuser7", "conflict7@test.local")

		// Deactivate the email match user
		th.DeactivateUser(emailUser.Id)

		// Create import file with conflicting username and email
		inputFile := createTempImportFile(t, []imports.LineImportData{
			{
				Type: "user",
				User: &imports.UserImportData{
					Username: strPtr("conflictuser7"),        // Matches active usernameUser
					Email:    strPtr("conflict7@test.local"), // Matches inactive emailUser
				},
			},
		})
		defer os.Remove(inputFile)

		// Create output file
		outputFile := t.TempDir() + "/output.jsonl"

		// Run sync
		err := runSyncImportUsers(t, th, inputFile, outputFile)
		require.NoError(t, err)

		// Verify output - should prefer the active username user
		outputLines := readOutputFile(t, outputFile)
		require.Len(t, outputLines, 1)

		var userData imports.LineImportData
		err = json.Unmarshal([]byte(outputLines[0]), &userData)
		require.NoError(t, err)

		assert.Equal(t, usernameUser.Username, *userData.User.Username)
		// Email should be updated to match the active user
		assert.Equal(t, strings.ToLower(usernameUser.Email), strings.ToLower(*userData.User.Email))
	})

	t.Run("user with no match in database - user is unchanged", func(t *testing.T) {
		// Create import file with a user that doesn't exist in the database
		inputFile := createTempImportFile(t, []imports.LineImportData{
			{
				Type: "user",
				User: &imports.UserImportData{
					Username: strPtr("newuser8"),
					Email:    strPtr("newuser8@test.local"),
				},
			},
		})
		defer os.Remove(inputFile)

		// Create output file
		outputFile := t.TempDir() + "/output.jsonl"

		// Run sync
		err := runSyncImportUsers(t, th, inputFile, outputFile)
		require.NoError(t, err)

		// Verify output - user should be unchanged
		outputLines := readOutputFile(t, outputFile)
		require.Len(t, outputLines, 1)

		var userData imports.LineImportData
		err = json.Unmarshal([]byte(outputLines[0]), &userData)
		require.NoError(t, err)

		assert.Equal(t, "newuser8", *userData.User.Username)
		assert.Equal(t, "newuser8@test.local", *userData.User.Email)
	})

	t.Run("reactions usernames are updated", func(t *testing.T) {
		// Create user in Mattermost
		mmUser := th.CreateUser("dbuser9", "testuser9@test.local")

		reactionUser := "importuser9"
		reactions := []imports.ReactionImportData{
			{User: &reactionUser},
		}

		// Create import file with a post with reactions by a different username
		inputFile := createTempImportFile(t, []imports.LineImportData{
			{
				Type: "user",
				User: &imports.UserImportData{
					Username: strPtr("importuser9"),
					Email:    strPtr("testuser9@test.local"),
				},
			},
			{
				Type: "post",
				Post: &imports.PostImportData{
					User:      strPtr("someotheruser"),
					Message:   strPtr("Test message with reactions"),
					Reactions: &reactions,
				},
			},
		})
		defer os.Remove(inputFile)

		// Create output file
		outputFile := t.TempDir() + "/output.jsonl"

		// Run sync
		err := runSyncImportUsers(t, th, inputFile, outputFile)
		require.NoError(t, err)

		// Verify output
		outputLines := readOutputFile(t, outputFile)
		require.Len(t, outputLines, 2)

		// Check post line - reaction username should be updated
		var postData imports.LineImportData
		err = json.Unmarshal([]byte(outputLines[1]), &postData)
		require.NoError(t, err)

		require.NotNil(t, postData.Post.Reactions)
		reactionsList := *postData.Post.Reactions
		require.Len(t, reactionsList, 1)
		assert.Equal(t, mmUser.Username, *reactionsList[0].User)
	})
}

// Helper functions

func strPtr(s string) *string {
	return &s
}

func createTempImportFile(t *testing.T, lines []imports.LineImportData) string {
	t.Helper()

	tmpFile, err := os.CreateTemp("", "import-*.jsonl")
	require.NoError(t, err)
	defer tmpFile.Close()

	for _, line := range lines {
		jsonBytes, err := json.Marshal(line)
		require.NoError(t, err)
		_, err = tmpFile.WriteString(string(jsonBytes) + "\n")
		require.NoError(t, err)
	}

	return tmpFile.Name()
}

func readOutputFile(t *testing.T, path string) []string {
	t.Helper()

	file, err := os.Open(path)
	require.NoError(t, err)
	defer file.Close()

	var lines []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if line != "" {
			lines = append(lines, line)
		}
	}
	require.NoError(t, scanner.Err())

	return lines
}

func runSyncImportUsers(t *testing.T, th *testhelper.TestHelper, inputFile, outputFile string) error {
	t.Helper()

	// Open input file
	reader, err := os.Open(inputFile)
	require.NoError(t, err)
	defer reader.Close()

	// Set up logger
	logger := log.New()
	logger.SetOutput(os.Stdout)
	logger.SetLevel(log.DebugLevel)

	// Get API client
	client := th.GetAPIClient()

	// Run sync
	flags := SyncImportUsersFlags{
		DryRun:     false,
		OutputFile: outputFile,
	}

	return SyncImportUsers(reader, flags, client, logger)
}
