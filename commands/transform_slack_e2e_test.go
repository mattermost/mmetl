package commands_test

import (
	"archive/zip"
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mattermost/mmetl/commands"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ImportLine represents a line in the Mattermost import JSONL file
type ImportLine struct {
	Type    string         `json:"type"`
	Version *int           `json:"version,omitempty"`
	Channel *ChannelImport `json:"channel,omitempty"`
	User    *UserImport    `json:"user,omitempty"`
	Post    *PostImport    `json:"post,omitempty"`
}

type ChannelImport struct {
	Team        string `json:"team"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Type        string `json:"type"`
	Header      string `json:"header"`
	Purpose     string `json:"purpose"`
}

type UserImport struct {
	Username  string       `json:"username"`
	Email     string       `json:"email"`
	FirstName string       `json:"first_name"`
	LastName  string       `json:"last_name"`
	Position  string       `json:"position"`
	Roles     string       `json:"roles"`
	Teams     []TeamImport `json:"teams"`
}

type TeamImport struct {
	Name     string          `json:"name"`
	Roles    string          `json:"roles"`
	Channels []ChannelMember `json:"channels"`
}

type ChannelMember struct {
	Name  string `json:"name"`
	Roles string `json:"roles"`
}

type PostImport struct {
	Team    string `json:"team"`
	Channel string `json:"channel"`
	User    string `json:"user"`
	Message string `json:"message"`
}

func TestTransformSlackCommand(t *testing.T) {
	defaultChannelsData := `[
		{
			"id": "C001",
			"name": "general",
			"creator": "U001",
			"members": ["U001", "U002"],
			"purpose": {"value": "Company wide announcements"},
			"topic": {"value": "Work matters"}
		},
		{
			"id": "C002",
			"name": "random",
			"creator": "U002",
			"members": ["U001", "U002"],
			"purpose": {"value": "Non-work chit-chat"},
			"topic": {"value": "Anything goes!"}
		}
	]`

	defaultUsersData := `[
		{
			"id": "U001",
			"name": "johndoe",
			"is_bot": false,
			"profile": {
				"real_name": "John Doe",
				"email": "john.doe@example.com",
				"title": "Software Engineer"
			},
			"deleted": false
		},
		{
			"id": "U002",
			"name": "janesmith",
			"is_bot": false,
			"profile": {
				"real_name": "Jane Smith",
				"email": "jane.smith@example.com",
				"title": "Product Manager"
			},
			"deleted": false
		}
	]`

	defaultGeneralPostsData := `[
		{
			"user": "U001",
			"text": "Hello, World!",
			"ts": "1577836800.000100",
			"type": "message"
		},
		{
			"user": "U002",
			"text": "Hello, everyone!",
			"ts": "1577836801.000200",
			"type": "message"
		}
	]`

	defaultRandomPostsData := `[
		{
			"user": "U001",
			"text": "Random thought here",
			"ts": "1577840400.000300",
			"type": "message"
		}
	]`

	t.Run("valid export with posts", func(t *testing.T) {
		tempDir := t.TempDir()
		inputFilePath := filepath.Join(tempDir, "test_input.zip")
		outputFilePath := filepath.Join(tempDir, "test_output.jsonl")
		defer os.Remove("transform-slack.log")

		err := createSlackExportZip(inputFilePath, defaultChannelsData, defaultUsersData, map[string]string{
			"general": defaultGeneralPostsData,
			"random":  defaultRandomPostsData,
		})
		require.NoError(t, err)

		args := []string{
			"transform", "slack",
			"--team", "myteam",
			"--file", inputFilePath,
			"--output", outputFilePath,
			"--skip-attachments",
		}

		c := commands.RootCmd
		c.SetArgs(args)
		err = c.Execute()
		require.NoError(t, err)

		lines := readImportLines(t, outputFilePath)
		require.NotEmpty(t, lines)

		// Verify version line
		versionLine := findLineByType(lines, "version")
		require.NotNil(t, versionLine, "version line should exist")
		assert.Equal(t, 1, *versionLine.Version)

		// Verify channels are created with correct team
		channels := findAllLinesByType(lines, "channel")
		require.Len(t, channels, 2, "should have 2 channels")

		generalChannel := findChannelByName(channels, "general")
		require.NotNil(t, generalChannel, "general channel should exist")
		assert.Equal(t, "myteam", generalChannel.Channel.Team, "channel should be in correct team")
		assert.Equal(t, "Work matters", generalChannel.Channel.Header)
		assert.Equal(t, "Company wide announcements", generalChannel.Channel.Purpose)

		randomChannel := findChannelByName(channels, "random")
		require.NotNil(t, randomChannel, "random channel should exist")
		assert.Equal(t, "myteam", randomChannel.Channel.Team, "channel should be in correct team")

		// Verify users are created
		users := findAllLinesByType(lines, "user")
		require.Len(t, users, 2, "should have 2 users")

		johnUser := findUserByUsername(users, "johndoe")
		require.NotNil(t, johnUser, "johndoe user should exist")
		assert.Equal(t, "john.doe@example.com", johnUser.User.Email)
		assert.Equal(t, "John", johnUser.User.FirstName)
		assert.Equal(t, "Doe", johnUser.User.LastName)
		assert.Equal(t, "Software Engineer", johnUser.User.Position)

		// Verify user is assigned to correct team
		require.Len(t, johnUser.User.Teams, 1, "user should be in 1 team")
		assert.Equal(t, "myteam", johnUser.User.Teams[0].Name, "user should be in correct team")

		// Verify user has channel memberships
		channelNames := getChannelNamesFromTeam(johnUser.User.Teams[0])
		assert.Contains(t, channelNames, "general", "user should be member of general")
		assert.Contains(t, channelNames, "random", "user should be member of random")

		// Verify posts are created
		posts := findAllLinesByType(lines, "post")
		require.Len(t, posts, 3, "should have 3 posts")

		// Verify posts reference correct team and channels
		for _, post := range posts {
			assert.Equal(t, "myteam", post.Post.Team, "post should reference correct team")
			assert.Contains(t, []string{"general", "random"}, post.Post.Channel, "post should be in valid channel")
		}
	})

	t.Run("team name with uppercase is converted to lowercase", func(t *testing.T) {
		tempDir := t.TempDir()
		inputFilePath := filepath.Join(tempDir, "test_input.zip")
		outputFilePath := filepath.Join(tempDir, "test_output.jsonl")
		defer os.Remove("transform-slack.log")

		err := createSlackExportZip(inputFilePath, defaultChannelsData, defaultUsersData, map[string]string{})
		require.NoError(t, err)

		args := []string{
			"transform", "slack",
			"--team", "MyTeam", // Uppercase
			"--file", inputFilePath,
			"--output", outputFilePath,
			"--skip-attachments",
		}

		c := commands.RootCmd
		c.SetArgs(args)
		err = c.Execute()
		require.NoError(t, err)

		lines := readImportLines(t, outputFilePath)

		// Verify team name is lowercased in channels
		channels := findAllLinesByType(lines, "channel")
		for _, channel := range channels {
			assert.Equal(t, "myteam", channel.Channel.Team, "team name should be lowercase")
		}

		// Verify team name is lowercased in user team assignments
		users := findAllLinesByType(lines, "user")
		for _, user := range users {
			for _, team := range user.User.Teams {
				assert.Equal(t, "myteam", team.Name, "team name should be lowercase")
			}
		}
	})

	t.Run("export without posts", func(t *testing.T) {
		tempDir := t.TempDir()
		inputFilePath := filepath.Join(tempDir, "test_input.zip")
		outputFilePath := filepath.Join(tempDir, "test_output.jsonl")
		defer os.Remove("transform-slack.log")

		err := createSlackExportZip(inputFilePath, defaultChannelsData, defaultUsersData, map[string]string{})
		require.NoError(t, err)

		args := []string{
			"transform", "slack",
			"--team", "testteam",
			"--file", inputFilePath,
			"--output", outputFilePath,
			"--skip-attachments",
		}

		c := commands.RootCmd
		c.SetArgs(args)
		err = c.Execute()
		require.NoError(t, err)

		lines := readImportLines(t, outputFilePath)

		// Should have version, channels, users - no posts
		versionLines := findAllLinesByType(lines, "version")
		assert.Len(t, versionLines, 1, "should have 1 version line")

		channels := findAllLinesByType(lines, "channel")
		assert.Len(t, channels, 2, "should have 2 channels")

		users := findAllLinesByType(lines, "user")
		assert.Len(t, users, 2, "should have 2 users")

		posts := findAllLinesByType(lines, "post")
		assert.Len(t, posts, 0, "should have 0 posts")
	})

	t.Run("users have correct channel memberships", func(t *testing.T) {
		tempDir := t.TempDir()
		inputFilePath := filepath.Join(tempDir, "test_input.zip")
		outputFilePath := filepath.Join(tempDir, "test_output.jsonl")
		defer os.Remove("transform-slack.log")

		// Create channels with different member sets
		channelsData := `[
			{
				"id": "C001",
				"name": "general",
				"creator": "U001",
				"members": ["U001", "U002"],
				"purpose": {"value": "General"},
				"topic": {"value": ""}
			},
			{
				"id": "C002",
				"name": "engineering",
				"creator": "U001",
				"members": ["U001"],
				"purpose": {"value": "Engineering only"},
				"topic": {"value": ""}
			}
		]`

		err := createSlackExportZip(inputFilePath, channelsData, defaultUsersData, map[string]string{})
		require.NoError(t, err)

		args := []string{
			"transform", "slack",
			"--team", "testteam",
			"--file", inputFilePath,
			"--output", outputFilePath,
			"--skip-attachments",
		}

		c := commands.RootCmd
		c.SetArgs(args)
		err = c.Execute()
		require.NoError(t, err)

		lines := readImportLines(t, outputFilePath)
		users := findAllLinesByType(lines, "user")

		// John is in both channels
		johnUser := findUserByUsername(users, "johndoe")
		require.NotNil(t, johnUser)
		johnChannels := getChannelNamesFromTeam(johnUser.User.Teams[0])
		assert.Contains(t, johnChannels, "general")
		assert.Contains(t, johnChannels, "engineering")

		// Jane is only in general
		janeUser := findUserByUsername(users, "janesmith")
		require.NotNil(t, janeUser)
		janeChannels := getChannelNamesFromTeam(janeUser.User.Teams[0])
		assert.Contains(t, janeChannels, "general")
		assert.NotContains(t, janeChannels, "engineering")
	})
}

// Helper functions

func readImportLines(t *testing.T, filePath string) []ImportLine {
	t.Helper()
	file, err := os.Open(filePath)
	require.NoError(t, err)
	defer file.Close()

	var lines []ImportLine
	scanner := bufio.NewScanner(file)
	// Increase buffer size for large lines
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	for scanner.Scan() {
		var line ImportLine
		err := json.Unmarshal(scanner.Bytes(), &line)
		require.NoError(t, err, "failed to parse line: %s", scanner.Text())
		lines = append(lines, line)
	}
	require.NoError(t, scanner.Err())
	return lines
}

func findLineByType(lines []ImportLine, lineType string) *ImportLine {
	for i := range lines {
		if lines[i].Type == lineType {
			return &lines[i]
		}
	}
	return nil
}

func findAllLinesByType(lines []ImportLine, lineType string) []ImportLine {
	var result []ImportLine
	for _, line := range lines {
		if line.Type == lineType {
			result = append(result, line)
		}
	}
	return result
}

func findChannelByName(lines []ImportLine, name string) *ImportLine {
	for i := range lines {
		if lines[i].Type == "channel" && lines[i].Channel != nil && lines[i].Channel.Name == name {
			return &lines[i]
		}
	}
	return nil
}

func findUserByUsername(lines []ImportLine, username string) *ImportLine {
	for i := range lines {
		if lines[i].Type == "user" && lines[i].User != nil && lines[i].User.Username == username {
			return &lines[i]
		}
	}
	return nil
}

func getChannelNamesFromTeam(team TeamImport) []string {
	var names []string
	for _, ch := range team.Channels {
		names = append(names, ch.Name)
	}
	return names
}

// createSlackExportZip creates a properly structured Slack export ZIP file
func createSlackExportZip(outputPath, channelsData, usersData string, channelPosts map[string]string) error {
	tempDir, err := os.MkdirTemp("", "slack-export-test-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tempDir)

	// Write channels.json
	if err := writeTestFile(filepath.Join(tempDir, "channels.json"), channelsData); err != nil {
		return err
	}

	// Write users.json
	if err := writeTestFile(filepath.Join(tempDir, "users.json"), usersData); err != nil {
		return err
	}

	// Write posts for each channel in channel-name/date.json format
	for channelName, postsData := range channelPosts {
		channelDir := filepath.Join(tempDir, channelName)
		if err := os.MkdirAll(channelDir, 0755); err != nil {
			return fmt.Errorf("failed to create channel dir %s: %w", channelName, err)
		}
		// Use a fixed date for test consistency
		if err := writeTestFile(filepath.Join(channelDir, "2020-01-01.json"), postsData); err != nil {
			return err
		}
	}

	// Create the ZIP file
	return createZipFromDir(outputPath, tempDir)
}

// writeTestFile writes a string to a file
func writeTestFile(filePath, data string) error {
	file, err := os.Create(filePath)
	if err != nil {
		return err
	}
	defer file.Close()

	_, err = file.WriteString(data)
	return err
}

// createZipFromDir creates a ZIP file from a directory
func createZipFromDir(outputPath, sourceDir string) error {
	zipFile, err := os.Create(outputPath)
	if err != nil {
		return err
	}
	defer zipFile.Close()

	archive := zip.NewWriter(zipFile)
	defer archive.Close()

	return filepath.Walk(sourceDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Skip the root directory
		if path == sourceDir {
			return nil
		}

		// Get relative path for the archive
		relPath, err := filepath.Rel(sourceDir, path)
		if err != nil {
			return err
		}

		if info.IsDir() {
			// For directories, add trailing slash
			_, createErr := archive.Create(relPath + "/")
			return createErr
		}

		// Create file header
		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		header.Name = relPath
		header.Method = zip.Deflate

		writer, err := archive.CreateHeader(header)
		if err != nil {
			return err
		}

		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()

		_, err = io.Copy(writer, file)
		return err
	})
}

// Unused but kept for potential future use
var _ = strings.TrimSpace
