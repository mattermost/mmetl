package commands_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mattermost/mmetl/commands"
	"github.com/mattermost/mmetl/testhelper"
	"github.com/stretchr/testify/require"
)

// TestCheckSlackGuestHandling verifies that "check slack" exposes and validates
// the same --guest-handling flag as "transform slack", so operators can preview
// an export under the exact mode they intend to run. This is a local check (no
// Mattermost server), so it runs in short mode too.
func TestCheckSlackGuestHandling(t *testing.T) {
	tempDir := t.TempDir()
	slackExportPath := filepath.Join(tempDir, "slack_export.zip")
	require.NoError(t, testhelper.ExportWithGuestPosts().Build(slackExportPath))
	t.Cleanup(func() { os.Remove("check-slack.log") })

	t.Run("accepts a valid guest-handling mode", func(t *testing.T) {
		c := commands.RootCmd
		resetCobraFlags(c)
		c.SetArgs([]string{
			"check", "slack",
			"--file", slackExportPath,
			"--guest-handling", "skip",
		})
		require.NoError(t, c.Execute(), "check should succeed with a valid guest-handling mode")
	})

	t.Run("rejects an invalid guest-handling mode", func(t *testing.T) {
		c := commands.RootCmd
		resetCobraFlags(c)
		c.SetArgs([]string{
			"check", "slack",
			"--file", slackExportPath,
			"--guest-handling", "bogus",
		})
		require.Error(t, c.Execute(), "check should fail with an invalid guest-handling mode")
	})
}
