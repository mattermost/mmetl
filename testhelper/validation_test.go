package testhelper

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateImportFile(t *testing.T) {
	t.Run("validates valid import file", func(t *testing.T) {
		tempDir := t.TempDir()
		filePath := filepath.Join(tempDir, "import.jsonl")

		content := `{"type":"version","version":1}
{"type":"user","user":{"username":"john.doe","email":"john@test.com","teams":[{"name":"myteam","channels":[{"name":"general"}]}]}}
{"type":"user","user":{"username":"jane.smith","email":"jane@test.com","teams":[{"name":"myteam","channels":[{"name":"general"}]}]}}
{"type":"channel","channel":{"team":"myteam","name":"general","display_name":"General","type":"O"}}
{"type":"post","post":{"team":"myteam","channel":"general","user":"john.doe","message":"Hello world!","create_at":1234567890}}
`
		err := os.WriteFile(filePath, []byte(content), 0644)
		require.NoError(t, err)

		result, err := ValidateImportFile(filePath)
		require.NoError(t, err)

		assert.True(t, result.Valid, "file should be valid")
		assert.Empty(t, result.Errors, "should have no errors")
		assert.Equal(t, uint64(5), result.LineCount)
		assert.Equal(t, uint64(2), result.UserCount)
		assert.Equal(t, uint64(1), result.ChannelCount)
		assert.Equal(t, uint64(1), result.PostCount)
	})

	t.Run("detects invalid JSON", func(t *testing.T) {
		tempDir := t.TempDir()
		filePath := filepath.Join(tempDir, "import.jsonl")

		content := `{"type":"version","version":1}
{invalid json here}
{"type":"user","user":{"username":"john","email":"john@test.com"}}
`
		err := os.WriteFile(filePath, []byte(content), 0644)
		require.NoError(t, err)

		result, err := ValidateImportFile(filePath)
		require.NoError(t, err)

		assert.False(t, result.Valid, "file should be invalid")
		assert.GreaterOrEqual(t, len(result.Errors), 1, "should have at least 1 error")
	})

	t.Run("detects missing user fields", func(t *testing.T) {
		tempDir := t.TempDir()
		filePath := filepath.Join(tempDir, "import.jsonl")

		content := `{"type":"version","version":1}
{"type":"user","user":{"username":"","email":""}}
{"type":"user","user":{}}
`
		err := os.WriteFile(filePath, []byte(content), 0644)
		require.NoError(t, err)

		result, err := ValidateImportFile(filePath)
		require.NoError(t, err)

		assert.False(t, result.Valid, "file should be invalid")
		assert.GreaterOrEqual(t, len(result.Errors), 2, "should have errors for missing fields")
	})

	t.Run("detects missing channel fields", func(t *testing.T) {
		tempDir := t.TempDir()
		filePath := filepath.Join(tempDir, "import.jsonl")

		content := `{"type":"version","version":1}
{"type":"channel","channel":{"name":"","team":"","type":""}}
`
		err := os.WriteFile(filePath, []byte(content), 0644)
		require.NoError(t, err)

		result, err := ValidateImportFile(filePath)
		require.NoError(t, err)

		assert.False(t, result.Valid, "file should be invalid")
		assert.GreaterOrEqual(t, len(result.Errors), 1, "should have errors for invalid channel")
	})

	t.Run("detects invalid channel type", func(t *testing.T) {
		tempDir := t.TempDir()
		filePath := filepath.Join(tempDir, "import.jsonl")

		content := `{"type":"version","version":1}
{"type":"channel","channel":{"name":"general","team":"myteam","type":"X"}}
`
		err := os.WriteFile(filePath, []byte(content), 0644)
		require.NoError(t, err)

		result, err := ValidateImportFile(filePath)
		require.NoError(t, err)

		assert.False(t, result.Valid, "file should be invalid")
		// Check that there's an error related to channel type
		var foundTypeError bool
		for _, e := range result.Errors {
			if e.FieldName == "type" || strings.Contains(e.Error(), "type") {
				foundTypeError = true
				break
			}
		}
		assert.True(t, foundTypeError, "should have channel type error")
	})

	t.Run("detects post referencing unknown user", func(t *testing.T) {
		tempDir := t.TempDir()
		filePath := filepath.Join(tempDir, "import.jsonl")

		content := `{"type":"version","version":1}
{"type":"user","user":{"username":"john.doe","email":"john@test.com","teams":[{"name":"myteam"}]}}
{"type":"channel","channel":{"team":"myteam","name":"general","type":"O"}}
{"type":"post","post":{"team":"myteam","channel":"general","user":"unknown_user","message":"Hello","create_at":1234567890}}
`
		err := os.WriteFile(filePath, []byte(content), 0644)
		require.NoError(t, err)

		result, err := ValidateImportFile(filePath)
		require.NoError(t, err)

		assert.False(t, result.Valid, "file should be invalid")
		var foundUserError bool
		for _, e := range result.Errors {
			if e.FieldName == "user" || strings.Contains(e.Error(), "user") {
				foundUserError = true
				break
			}
		}
		assert.True(t, foundUserError, "should have error for unknown user")
	})

	t.Run("detects post referencing unknown channel", func(t *testing.T) {
		tempDir := t.TempDir()
		filePath := filepath.Join(tempDir, "import.jsonl")

		content := `{"type":"version","version":1}
{"type":"user","user":{"username":"john.doe","email":"john@test.com","teams":[{"name":"myteam"}]}}
{"type":"channel","channel":{"team":"myteam","name":"general","type":"O"}}
{"type":"post","post":{"team":"myteam","channel":"unknown_channel","user":"john.doe","message":"Hello","create_at":1234567890}}
`
		err := os.WriteFile(filePath, []byte(content), 0644)
		require.NoError(t, err)

		result, err := ValidateImportFile(filePath)
		require.NoError(t, err)

		assert.False(t, result.Valid, "file should be invalid")
		var foundChannelError bool
		for _, e := range result.Errors {
			if e.FieldName == "channel" || strings.Contains(e.Error(), "channel") {
				foundChannelError = true
				break
			}
		}
		assert.True(t, foundChannelError, "should have error for unknown channel")
	})

	t.Run("file not found returns error", func(t *testing.T) {
		result, err := ValidateImportFile("/nonexistent/file.jsonl")
		assert.Error(t, err)
		assert.Nil(t, result)
	})

	t.Run("reports counts correctly", func(t *testing.T) {
		tempDir := t.TempDir()
		filePath := filepath.Join(tempDir, "import.jsonl")

		content := `{"type":"version","version":1}
{"type":"user","user":{"username":"user1","email":"user1@test.com"}}
{"type":"user","user":{"username":"user2","email":"user2@test.com"}}
{"type":"user","user":{"username":"user3","email":"user3@test.com"}}
{"type":"channel","channel":{"team":"team1","name":"chan1","type":"O"}}
{"type":"channel","channel":{"team":"team1","name":"chan2","type":"P"}}
{"type":"post","post":{"team":"team1","channel":"chan1","user":"user1","message":"msg1","create_at":1234567890}}
{"type":"post","post":{"team":"team1","channel":"chan1","user":"user2","message":"msg2","create_at":1234567891}}
{"type":"post","post":{"team":"team1","channel":"chan2","user":"user3","message":"msg3","create_at":1234567892}}
`
		err := os.WriteFile(filePath, []byte(content), 0644)
		require.NoError(t, err)

		result, err := ValidateImportFile(filePath)
		require.NoError(t, err)

		assert.True(t, result.Valid)
		assert.Equal(t, uint64(9), result.LineCount)
		assert.Equal(t, uint64(3), result.UserCount)
		assert.Equal(t, uint64(2), result.ChannelCount)
		assert.Equal(t, uint64(3), result.PostCount)
	})
}
