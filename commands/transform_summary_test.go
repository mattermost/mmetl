package commands_test

// Unlike the *_e2e_test.go files in this package, these tests only exercise
// the transform step (no testcontainers, no live Mattermost) — they assert on
// the generated summary file, so they run under plain `make test`.

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mattermost/mmetl/commands"
	"github.com/mattermost/mmetl/testhelper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Written unconditionally to the working directory, alongside the
// transform-*.log file — there is no flag to relocate them, matching the log
// file's own behavior.
const (
	transformSlackSummaryFile      = "transform-slack-summary.md"
	transformRocketChatSummaryFile = "transform-rocketchat-summary.md"
)

func TestTransformSlackCmd_SummaryFile(t *testing.T) {
	tempDir := t.TempDir()
	slackExportPath := filepath.Join(tempDir, "slack_export.zip")
	outputPath := filepath.Join(tempDir, "bulk-export.jsonl")
	teamName := uniqueTeamName("summary")
	t.Cleanup(func() { os.Remove(transformLogFile) })
	t.Cleanup(func() { os.Remove(transformSlackSummaryFile) })

	require.NoError(t, testhelper.SlackBasicExport().Build(slackExportPath))

	c := commands.RootCmd
	resetCobraFlags(c)
	c.SetArgs([]string{
		"transform", "slack",
		"--team", teamName,
		"--file", slackExportPath,
		"--output", outputPath,
		"--skip-attachments",
	})
	require.NoError(t, c.Execute())

	content, err := os.ReadFile(transformSlackSummaryFile)
	require.NoError(t, err, "summary file should have been written")

	summary := string(content)
	assert.Contains(t, summary, "# Slack Transform Summary")
	assert.Contains(t, summary, "| Entity | Transformed | Skipped | Failed |")
	// SlackBasicExport has 2 users, 2 public channels, and no skips.
	assert.Contains(t, summary, "| Users | 2 | 0 | 0 |")
	assert.Contains(t, summary, "| Public channels | 2 | 0 | 0 |")
	assert.Contains(t, summary, "## Notes")
	assert.Contains(t, summary, "No issues encountered.")
}

func TestTransformSlackCmd_SummaryFileReportsSkippedGuest(t *testing.T) {
	tempDir := t.TempDir()
	slackExportPath := filepath.Join(tempDir, "slack_export.zip")
	outputPath := filepath.Join(tempDir, "bulk-export.jsonl")
	teamName := uniqueTeamName("summary")
	t.Cleanup(func() { os.Remove(transformLogFile) })
	t.Cleanup(func() { os.Remove(transformSlackSummaryFile) })

	// A guest present only in an MPIM has no public/private channel to scope
	// their guest access to, so dropChannellessGuests drops them (tagged
	// EntityUser) in the default "guest" handling mode.
	require.NoError(t, testhelper.ExportWithChannellessGuestMpim().Build(slackExportPath))

	c := commands.RootCmd
	resetCobraFlags(c)
	c.SetArgs([]string{
		"transform", "slack",
		"--team", teamName,
		"--file", slackExportPath,
		"--output", outputPath,
		"--skip-attachments",
	})
	require.NoError(t, c.Execute())

	content, err := os.ReadFile(transformSlackSummaryFile)
	require.NoError(t, err)
	summary := string(content)

	// 3 regular users transformed, 1 channel-less guest skipped.
	assert.Contains(t, summary, "| Users | 3 | 1 | 0 |")
	assert.NotContains(t, summary, "No issues encountered.")
	assert.Contains(t, summary, "has no public or private channel membership")
}

func TestTransformRocketChatCmd_SummaryFile(t *testing.T) {
	dir := t.TempDir()
	outputPath := filepath.Join(dir, "bulk-export.jsonl")
	teamName := uniqueTeamName("rcsummary")
	t.Cleanup(func() { os.Remove("transform-rocketchat.log") })
	t.Cleanup(func() { os.Remove(transformRocketChatSummaryFile) })

	users := []any{
		rcBSONUser{ID: "alice-id", Username: "alice", Name: "Alice Anderson", Emails: []rcMail{{Address: "alice@example.com", Verified: true}}, Active: true, Roles: []string{"user"}, Type: "user"},
		rcBSONUser{ID: "carol-id", Username: "carol", Name: "Carol Guest", Emails: []rcMail{{Address: "carol@example.com", Verified: true}}, Active: true, Roles: []string{"guest"}, Type: "user"},
	}
	rooms := []any{
		rcRoom{ID: "engineering-id", Type: "c", Name: "engineering", FName: "Engineering"},
		// carol only appears in a DM (no channel subscription) -> channel-less
		// guest, dropped by skipChannellessGuests (tagged EntityUser).
		rcRoom{ID: "alice-carol-dm", Type: "d", Usernames: []string{"alice", "carol"}, UIDs: []string{"alice-id", "carol-id"}},
	}
	baseTime := time.Date(2024, 1, 2, 9, 30, 0, 0, time.UTC)
	messages := []any{
		rcMessage{ID: "eng-root", RoomID: "engineering-id", User: rcMsgUser{ID: "alice-id", Username: "alice"}, Message: "Welcome!", Timestamp: baseTime},
		rcMessage{ID: "carol-dm", RoomID: "alice-carol-dm", User: rcMsgUser{ID: "carol-id", Username: "carol"}, Message: "should be dropped", Timestamp: baseTime.Add(time.Minute)},
	}
	subscriptions := []any{
		rcSubscription{RoomID: "engineering-id", User: rcMsgUser{ID: "alice-id", Username: "alice"}},
	}
	writeDumpDir(t, dir, users, rooms, messages, subscriptions)

	resetRCFlags()
	commands.RootCmd.SetArgs([]string{
		"transform", "rocketchat",
		"--team", teamName,
		"--dump-dir", dir,
		"--output", outputPath,
		"--skip-attachments",
		"--guest-handling", "guest",
	})
	require.NoError(t, commands.RootCmd.Execute())

	content, err := os.ReadFile(transformRocketChatSummaryFile)
	require.NoError(t, err)
	summary := string(content)

	assert.Contains(t, summary, "# RocketChat Transform Summary")
	// alice transformed; channel-less guest carol skipped.
	assert.Contains(t, summary, "| Users | 1 | 1 | 0 |")
	// carol's DM post is dropped along with her.
	assert.Contains(t, summary, "| Direct posts | 0 | 1 | 0 |")
	assert.NotContains(t, summary, "No issues encountered.")
}

// TestTransformRocketChatCmd_SummaryFile_AttachmentExtractionFailure exercises
// the one path none of the other summary tests cover, since they all pass
// --skip-attachments: an attachment that ExtractAttachments fails to extract
// (here, a FileSystem-store upload with no --uploads-dir given) must be
// pruned from the post it was provisionally added to during Transform, so it
// counts as Skipped only — not also as Transformed. See
// intermediate.PruneAttachments and rocketchat.ExtractAttachments.
func TestTransformRocketChatCmd_SummaryFile_AttachmentExtractionFailure(t *testing.T) {
	dir := t.TempDir()
	outputPath := filepath.Join(dir, "bulk-export.jsonl")
	teamName := uniqueTeamName("rcattachfail")
	t.Cleanup(func() { os.Remove("transform-rocketchat.log") })
	t.Cleanup(func() { os.Remove(transformRocketChatSummaryFile) })

	users := []any{
		rcBSONUser{ID: "alice-id", Username: "alice", Name: "Alice Anderson", Emails: []rcMail{{Address: "alice@example.com", Verified: true}}, Active: true, Roles: []string{"user"}, Type: "user"},
	}
	rooms := []any{
		rcRoom{ID: "engineering-id", Type: "c", Name: "engineering", FName: "Engineering"},
	}
	messages := []any{
		rcMessage{
			ID: "eng-root", RoomID: "engineering-id", User: rcMsgUser{ID: "alice-id", Username: "alice"}, Message: "see attached",
			Files: []rcFileRef{{ID: "up1", Name: "photo.png", Type: "image/png", Size: 100}},
		},
	}
	subscriptions := []any{
		rcSubscription{RoomID: "engineering-id", User: rcMsgUser{ID: "alice-id", Username: "alice"}},
	}
	writeDumpDir(t, dir, users, rooms, messages, subscriptions)
	// FileSystem-store upload with no corresponding --uploads-dir passed below,
	// so ExtractAttachments fails it with "skipped: --uploads-dir not provided".
	marshalBSONFileCmds(t, filepath.Join(dir, "rocketchat_uploads.bson"), []any{
		rcUpload{ID: "up1", Name: "photo.png", Type: "image/png", Size: 100, RoomID: "engineering-id", UserID: "alice-id", Store: "FileSystem", Path: "/file-upload/up1/photo.png", Complete: true},
	})

	resetRCFlags()
	commands.RootCmd.SetArgs([]string{
		"transform", "rocketchat",
		"--team", teamName,
		"--dump-dir", dir,
		"--output", outputPath,
		"--attachments-dir", dir,
		// Deliberately no --skip-attachments and no --uploads-dir.
	})
	require.NoError(t, commands.RootCmd.Execute())

	content, err := os.ReadFile(transformRocketChatSummaryFile)
	require.NoError(t, err)
	summary := string(content)

	// The failed attachment must be Skipped, and NOT also Transformed —
	// before the fix this read "| Attachments | 1 | 1 | 0 |" (double-counted).
	assert.Contains(t, summary, "| Attachments | 0 | 1 | 0 |")
	assert.Contains(t, summary, "--uploads-dir not provided")

	// The exported JSONL's post must not reference the file that was never
	// written to disk.
	jsonl, err := os.ReadFile(outputPath)
	require.NoError(t, err)
	assert.NotContains(t, string(jsonl), "up1_photo.png")
}
