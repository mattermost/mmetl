package slack

import (
	"archive/zip"
	"bytes"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDetectWorkspaces(t *testing.T) {
	t.Run("detects single workspace", func(t *testing.T) {
		zipReader := createTestZip(t, map[string]string{
			"teams/team1/users.json":               "[]",
			"teams/team1/channels.json":            "[]",
			"teams/team1/dms.json":                 "[]",
			"teams/team1/groups.json":              "[]",
			"teams/team1/channel1/2024-01-01.json": "[]",
		})

		workspaces := DetectWorkspaces(zipReader)
		assert.Len(t, workspaces, 1)
		assert.Contains(t, workspaces, "team1")
	})

	t.Run("detects multiple workspaces", func(t *testing.T) {
		zipReader := createTestZip(t, map[string]string{
			"teams/team1/users.json":    "[]",
			"teams/team1/channels.json": "[]",
			"teams/team2/users.json":    "[]",
			"teams/team2/channels.json": "[]",
			"teams/team3/users.json":    "[]",
		})

		workspaces := DetectWorkspaces(zipReader)
		assert.Len(t, workspaces, 3)
		assert.Contains(t, workspaces, "team1")
		assert.Contains(t, workspaces, "team2")
		assert.Contains(t, workspaces, "team3")
	})

	t.Run("filters out system files like .DS_Store", func(t *testing.T) {
		zipReader := createTestZip(t, map[string]string{
			"teams/.DS_Store":           "",
			"teams/team1/users.json":    "[]",
			"teams/team1/channels.json": "[]",
		})

		workspaces := DetectWorkspaces(zipReader)
		assert.Len(t, workspaces, 1)
		assert.Contains(t, workspaces, "team1")
		assert.NotContains(t, workspaces, ".DS_Store")
	})

	t.Run("returns empty list for single workspace export", func(t *testing.T) {
		zipReader := createTestZip(t, map[string]string{
			"users.json":               "[]",
			"channels.json":            "[]",
			"dms.json":                 "[]",
			"channel1/2024-01-01.json": "[]",
		})

		workspaces := DetectWorkspaces(zipReader)
		assert.Empty(t, workspaces)
	})

	t.Run("handles empty zip", func(t *testing.T) {
		zipReader := createTestZip(t, map[string]string{})

		workspaces := DetectWorkspaces(zipReader)
		assert.Empty(t, workspaces)
	})
}

func TestGetFilePrefix(t *testing.T) {
	t.Run("returns empty prefix for single workspace export", func(t *testing.T) {
		transformer := NewTransformer("test", "", logrus.New())
		prefix := transformer.getFilePrefix()
		assert.Equal(t, "", prefix)
	})

	t.Run("returns correct prefix for multi-workspace export", func(t *testing.T) {
		transformer := NewTransformer("test", "team1", logrus.New())
		prefix := transformer.getFilePrefix()
		assert.Equal(t, "teams/team1/", prefix)
	})

	t.Run("handles different workspace names", func(t *testing.T) {
		testCases := []struct {
			workspaceName  string
			expectedPrefix string
		}{
			{"myworkspace", "teams/myworkspace/"},
			{"prod-team", "teams/prod-team/"},
			{"team_123", "teams/team_123/"},
		}

		for _, tc := range testCases {
			transformer := NewTransformer("test", tc.workspaceName, logrus.New())
			prefix := transformer.getFilePrefix()
			assert.Equal(t, tc.expectedPrefix, prefix, "workspace: %s", tc.workspaceName)
		}
	})
}

func TestMatchesWorkspace(t *testing.T) {
	t.Run("single workspace export - matches files at root", func(t *testing.T) {
		transformer := NewTransformer("test", "", logrus.New())

		assert.True(t, transformer.matchesWorkspace("users.json"))
		assert.True(t, transformer.matchesWorkspace("channels.json"))
		assert.True(t, transformer.matchesWorkspace("channel1/2024-01-01.json"))
		assert.True(t, transformer.matchesWorkspace("__uploads/file1/data"))
	})

	t.Run("single workspace export - rejects files in teams directory", func(t *testing.T) {
		transformer := NewTransformer("test", "", logrus.New())

		assert.False(t, transformer.matchesWorkspace("teams/team1/users.json"))
		assert.False(t, transformer.matchesWorkspace("teams/team1/channels.json"))
		assert.False(t, transformer.matchesWorkspace("teams/team1/channel1/2024-01-01.json"))
	})

	t.Run("multi-workspace export - matches files in correct workspace", func(t *testing.T) {
		transformer := NewTransformer("test", "team1", logrus.New())

		assert.True(t, transformer.matchesWorkspace("teams/team1/users.json"))
		assert.True(t, transformer.matchesWorkspace("teams/team1/channels.json"))
		assert.True(t, transformer.matchesWorkspace("teams/team1/channel1/2024-01-01.json"))
		assert.True(t, transformer.matchesWorkspace("teams/team1/__uploads/file1/data"))
	})

	t.Run("multi-workspace export - rejects files from other workspaces", func(t *testing.T) {
		transformer := NewTransformer("test", "team1", logrus.New())

		assert.False(t, transformer.matchesWorkspace("teams/team2/users.json"))
		assert.False(t, transformer.matchesWorkspace("teams/team2/channels.json"))
		assert.False(t, transformer.matchesWorkspace("teams/team2/channel1/2024-01-01.json"))
	})

	t.Run("multi-workspace export - rejects files at root", func(t *testing.T) {
		transformer := NewTransformer("test", "team1", logrus.New())

		assert.False(t, transformer.matchesWorkspace("users.json"))
		assert.False(t, transformer.matchesWorkspace("channels.json"))
		assert.False(t, transformer.matchesWorkspace("channel1/2024-01-01.json"))
	})
}

func TestParseSlackExportFile_MultiWorkspace(t *testing.T) {
	t.Run("parses multi-workspace export correctly", func(t *testing.T) {
		zipReader := createTestZip(t, map[string]string{
			"teams/team1/users.json":              `[{"id":"U1","username":"user1","profile":{"email":"user1@example.com"}}]`,
			"teams/team1/channels.json":           `[{"id":"C1","name":"general","is_channel":true,"members":["U1"]}]`,
			"teams/team1/dms.json":                `[]`,
			"teams/team1/groups.json":             `[]`,
			"teams/team1/mpims.json":              `[]`,
			"teams/team1/general/2024-01-01.json": `[{"type":"message","user":"U1","text":"Hello"}]`,
		})

		transformer := NewTransformer("test", "team1", logrus.New())
		slackExport, err := transformer.ParseSlackExportFile(zipReader, false)

		require.NoError(t, err)
		assert.NotNil(t, slackExport)
		assert.Len(t, slackExport.Users, 1)
		assert.Equal(t, "U1", slackExport.Users[0].Id)
		assert.Len(t, slackExport.PublicChannels, 1)
		assert.Equal(t, "C1", slackExport.PublicChannels[0].Id)
		assert.Len(t, slackExport.Posts["general"], 1)
	})

	t.Run("ignores files from other workspaces", func(t *testing.T) {
		zipReader := createTestZip(t, map[string]string{
			"teams/team1/users.json":    `[{"id":"U1","username":"user1","profile":{"email":"user1@example.com"}}]`,
			"teams/team1/channels.json": `[{"id":"C1","name":"general"}]`,
			"teams/team2/users.json":    `[{"id":"U2","username":"user2","profile":{"email":"user2@example.com"}}]`,
			"teams/team2/channels.json": `[{"id":"C2","name":"other"}]`,
		})

		transformer := NewTransformer("test", "team1", logrus.New())
		slackExport, err := transformer.ParseSlackExportFile(zipReader, false)

		require.NoError(t, err)
		assert.Len(t, slackExport.Users, 1)
		assert.Equal(t, "U1", slackExport.Users[0].Id)
		assert.Len(t, slackExport.PublicChannels, 1)
		assert.Equal(t, "C1", slackExport.PublicChannels[0].Id)
	})

	t.Run("parses single workspace export correctly", func(t *testing.T) {
		zipReader := createTestZip(t, map[string]string{
			"users.json":              `[{"id":"U1","username":"user1","profile":{"email":"user1@example.com"}}]`,
			"channels.json":           `[{"id":"C1","name":"general","is_channel":true,"members":["U1"]}]`,
			"dms.json":                `[]`,
			"groups.json":             `[]`,
			"mpims.json":              `[]`,
			"general/2024-01-01.json": `[{"type":"message","user":"U1","text":"Hello"}]`,
		})

		transformer := NewTransformer("test", "", logrus.New())
		slackExport, err := transformer.ParseSlackExportFile(zipReader, false)

		require.NoError(t, err)
		assert.NotNil(t, slackExport)
		assert.Len(t, slackExport.Users, 1)
		assert.Equal(t, "U1", slackExport.Users[0].Id)
		assert.Len(t, slackExport.PublicChannels, 1)
		assert.Equal(t, "C1", slackExport.PublicChannels[0].Id)
		assert.Len(t, slackExport.Posts["general"], 1)
	})

	t.Run("handles channel messages with correct path segments", func(t *testing.T) {
		zipReader := createTestZip(t, map[string]string{
			"teams/team1/users.json":               `[]`,
			"teams/team1/channels.json":            `[]`,
			"teams/team1/dms.json":                 `[]`,
			"teams/team1/groups.json":              `[]`,
			"teams/team1/mpims.json":               `[]`,
			"teams/team1/channel1/2024-01-01.json": `[{"type":"message","text":"msg1"}]`,
			"teams/team1/channel1/2024-01-02.json": `[{"type":"message","text":"msg2"}]`,
			"teams/team1/channel2/2024-01-01.json": `[{"type":"message","text":"msg3"}]`,
		})

		transformer := NewTransformer("test", "team1", logrus.New())
		slackExport, err := transformer.ParseSlackExportFile(zipReader, true) // skip convert posts

		require.NoError(t, err)
		assert.Len(t, slackExport.Posts["channel1"], 2)
		assert.Len(t, slackExport.Posts["channel2"], 1)
	})
}

func TestCheckForRequiredFile_WithSubdirectories(t *testing.T) {
	t.Run("accepts required file at root", func(t *testing.T) {
		zipReader := createTestZip(t, map[string]string{
			"channels.json": "[]",
			"users.json":    "[]",
		})

		transformer := NewTransformer("test", "", logrus.New())
		found := transformer.checkForRequiredFile(zipReader, "channels.json")
		assert.True(t, found)
	})

	t.Run("accepts required file in subdirectory", func(t *testing.T) {
		zipReader := createTestZip(t, map[string]string{
			"teams/team1/channels.json": "[]",
			"teams/team1/users.json":    "[]",
		})

		transformer := NewTransformer("test", "team1", logrus.New())
		found := transformer.checkForRequiredFile(zipReader, "channels.json")
		assert.True(t, found)
	})

	t.Run("rejects when file not found", func(t *testing.T) {
		zipReader := createTestZip(t, map[string]string{
			"users.json": "[]",
		})

		transformer := NewTransformer("test", "", logrus.New())
		found := transformer.checkForRequiredFile(zipReader, "channels.json")
		assert.False(t, found)
	})
}

// Helper function to create a test zip file in memory
func createTestZip(t *testing.T, files map[string]string) *zip.Reader {
	buf := new(bytes.Buffer)
	zipWriter := zip.NewWriter(buf)

	for filename, content := range files {
		writer, err := zipWriter.Create(filename)
		require.NoError(t, err)
		_, err = writer.Write([]byte(content))
		require.NoError(t, err)
	}

	err := zipWriter.Close()
	require.NoError(t, err)

	zipReader, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	require.NoError(t, err)

	return zipReader
}
