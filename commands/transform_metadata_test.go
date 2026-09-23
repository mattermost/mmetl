package commands

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mattermost/mmetl/services/intermediate"
)

// TestFormatChangedFlagsRedactsPaths covers the reason pathFlags exists: the
// flag string is written into the bulk import file, which gets shipped to a
// Mattermost server, so it must not carry the operator's directory layout.
func TestFormatChangedFlagsRedactsPaths(t *testing.T) {
	cmd := &cobra.Command{Use: "slack"}
	cmd.Flags().String("file", "", "")
	cmd.Flags().String("output", "", "")
	cmd.Flags().String("dump-dir", "", "")
	cmd.Flags().String("attachments-dir", "", "")
	cmd.Flags().String("uploads-dir", "", "")
	cmd.Flags().String("team-map-path", "", "")
	cmd.Flags().String("team", "", "")
	cmd.Flags().String("bot-owner", "", "")
	cmd.Flags().String("default-email-domain", "", "")

	require.NoError(t, cmd.Flags().Set("file", "/Users/someone/exports/my_export.zip"))
	require.NoError(t, cmd.Flags().Set("output", "/tmp/mm/bulk-export.jsonl"))
	require.NoError(t, cmd.Flags().Set("dump-dir", "/Users/someone/dump/rocketchat"))
	require.NoError(t, cmd.Flags().Set("attachments-dir", "/Users/someone/data"))
	require.NoError(t, cmd.Flags().Set("uploads-dir", "/var/rc/uploads"))
	require.NoError(t, cmd.Flags().Set("team-map-path", "/etc/mmetl/teams.json"))
	require.NoError(t, cmd.Flags().Set("team", "myteam"))

	flags := formatChangedFlags(cmd)

	assert.Equal(t,
		"--attachments-dir=data --dump-dir=rocketchat --file=my_export.zip --output=bulk-export.jsonl --team-map-path=teams.json --team=myteam --uploads-dir=uploads",
		flags)
	assert.NotContains(t, flags, "/Users/")
	assert.NotContains(t, flags, "/var/")
	assert.NotContains(t, flags, "/etc/")

	t.Run("values that change the output and are not paths are kept whole", func(t *testing.T) {
		require.NoError(t, cmd.Flags().Set("bot-owner", "admin"))
		require.NoError(t, cmd.Flags().Set("default-email-domain", "example.com"))

		flags := formatChangedFlags(cmd)
		assert.Contains(t, flags, "--bot-owner=admin")
		assert.Contains(t, flags, "--default-email-domain=example.com")
	})
}

func TestInputSizeBytes(t *testing.T) {
	dir := t.TempDir()

	file := filepath.Join(dir, "export.zip")
	require.NoError(t, os.WriteFile(file, []byte("0123456789"), 0644))

	t.Run("a file is its own size", func(t *testing.T) {
		assert.Equal(t, int64(10), inputSizeBytes(file))
	})

	t.Run("a directory is the total of the files under it", func(t *testing.T) {
		// RocketChat's --dump-dir: a directory of .bson files, one level down.
		dump := filepath.Join(dir, "dump", "rocketchat")
		require.NoError(t, os.MkdirAll(dump, 0755))
		require.NoError(t, os.WriteFile(filepath.Join(dump, "users.bson"), []byte("abc"), 0644))
		require.NoError(t, os.WriteFile(filepath.Join(dump, "rocketchat_room.bson"), []byte("defg"), 0644))

		assert.Equal(t, int64(7), inputSizeBytes(filepath.Join(dir, "dump")))
	})

	t.Run("an unreadable input is a zero, not a failed transform", func(t *testing.T) {
		assert.Zero(t, inputSizeBytes(filepath.Join(dir, "does-not-exist.zip")))
	})
}

func TestWriteTransformReportDoesNotMaskTransformError(t *testing.T) {
	report := intermediate.NewReport(nil)
	notADir := filepath.Join(t.TempDir(), "not-a-dir")
	require.NoError(t, os.WriteFile(notADir, []byte("x"), 0644))

	transformErr := errors.New("transform failed")
	runErr := transformErr
	writeTransformReport(report, notADir, &runErr)
	assert.Equal(t, transformErr, runErr)
}

func TestWriteTransformReportFailsCommandWhenWriteFails(t *testing.T) {
	report := intermediate.NewReport(nil)
	notADir := filepath.Join(t.TempDir(), "not-a-dir")
	require.NoError(t, os.WriteFile(notADir, []byte("x"), 0644))

	var runErr error
	writeTransformReport(report, notADir, &runErr)
	require.Error(t, runErr)
}
