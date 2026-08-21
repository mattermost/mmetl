package commands_test

// Docker-backed integration tests for the transform summary feature
// (transform-slack-summary.md / transform-rocketchat-summary.md).
//
// commands/transform_summary_test.go already checks the summary file's
// *content* (string matching) without ever importing into a live Mattermost
// server, so it can't tell us whether the numbers it reports are actually
// true. The tests here close that gap: they run the full
// fixture -> transform -> real Mattermost import pipeline (via testcontainers,
// same as the other *_e2e_test.go files in this package) and cross-check the
// summary's counts against two independent sources of truth that don't share
// any code with the summary itself:
//
//  1. mmctl's own import-file validator (testhelper.ValidateImportFileOrFail),
//     which parses the generated JSONL from scratch.
//  2. The live Mattermost server state after a real import
//     (th.ImportBulkData + API lookups).
//
// Agreement between mmetl's internal counters and these two independent
// checks is a real correctness signal, not a tautology.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/mattermost/mmetl/commands"
	"github.com/mattermost/mmetl/testhelper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTransformSlackSummaryE2E proves that transform-slack-summary.md's
// reported Users counts match reality, not just mmetl's own bookkeeping.
//
// It reuses the same channel-less-guest MPIM fixture as
// TestTransformSlackCmd_SummaryFileReportsSkippedGuest (a guest with no
// public/private channel membership, dropped in the default "guest" handling
// mode), but goes all the way through a real Mattermost import instead of
// stopping at the generated files. It checks three independent things agree:
//   - the summary table's "| Users | 3 | 1 | 0 |" row,
//   - mmctl's import-file validator's UserCount for the generated JSONL, and
//   - which usernames actually exist in Mattermost after a real import.
func TestTransformSlackSummaryE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	th := testhelper.SetupHelper(t)
	defer th.TearDown()
	t.Cleanup(func() { os.Remove(transformLogFile) })
	t.Cleanup(func() { os.Remove(transformSlackSummaryFile) })

	ctx := context.Background()
	tempDir := t.TempDir()
	slackExportPath := filepath.Join(tempDir, "slack_export.zip")
	mmExportPath := filepath.Join(tempDir, "mattermost_import.jsonl")
	teamName := uniqueTeamName("summarye2e")

	// A guest present only in an MPIM has no public/private channel to scope
	// their guest access to, so dropChannellessGuests drops them (tagged
	// EntityUser), along with their thread-root post and channel membership —
	// see services/slack/intermediate.go's dropChannellessGuests.
	require.NoError(t, testhelper.ExportWithChannellessGuestMpim().Build(slackExportPath),
		"failed to build channel-less-guest MPIM Slack export fixture")

	team := th.CreateTeam(ctx, teamName, "Summary E2E Team")
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

	// Summary file was written to the working directory (not --output).
	summaryBytes, err := os.ReadFile(transformSlackSummaryFile)
	require.NoError(t, err, "summary file should have been written")
	summary := string(summaryBytes)

	assert.Contains(t, summary, "# Slack Transform Summary")
	// 3 regular users transformed, 1 channel-less guest skipped.
	assert.Contains(t, summary, "| Users | 3 | 1 | 0 |")
	assert.Contains(t, summary, "has no public or private channel membership",
		"Notes section should explain why the guest was skipped")

	// Cross-check #1: mmctl's own import-file validator, which parses the
	// generated JSONL independently of mmetl's Intermediate struct, agrees
	// with the summary's Transformed count for Users.
	validation := th.ValidateImportFileOrFail(ctx, mmExportPath)
	assert.Equal(t, uint64(3), validation.UserCount,
		"validator's UserCount should match the summary's Transformed Users count")
	// The guest's thread-root post was skipped along with them, leaving only
	// the one standalone post in the MPIM (now a 3-member direct_post).
	assert.Equal(t, uint64(1), validation.DirectPostCount,
		"validator's DirectPostCount should match the summary's Transformed Direct posts count")

	// Cross-check #2: a real import. The "Skipped: 1" in the table should
	// correspond to a real, verifiable absence in Mattermost, not just an
	// internal counter.
	require.NoError(t, th.ImportBulkData(ctx, mmExportPath), "import should succeed")

	th.AssertUserExists(ctx, "regular1")
	th.AssertUserExists(ctx, "regular2")
	th.AssertUserExists(ctx, "regular3")

	_, err = th.GetUserByUsername(ctx, "channelless.guest")
	assert.Error(t, err, "channel-less guest should not have been imported into Mattermost")
}

// TestTransformRocketChatSummaryE2E is the RocketChat counterpart of
// TestTransformSlackSummaryE2E. It mirrors the channel-less-guest fixture
// from TestTransformRocketChatCmd_SummaryFile (commands/transform_summary_test.go):
// alice has a channel membership, carol is a guest who only appears in a DM
// with no channel subscription, so she — and her DM post — are dropped by
// RocketChat's channel-less-guest handling (mirroring Slack's
// dropChannellessGuests). It cross-checks the summary's Users and Direct
// posts rows against mmctl's independent validator and a real import.
func TestTransformRocketChatSummaryE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	th := testhelper.SetupHelper(t)
	defer th.TearDown()
	t.Cleanup(func() { os.Remove("transform-rocketchat.log") })
	t.Cleanup(func() { os.Remove(transformRocketChatSummaryFile) })

	ctx := context.Background()
	dir := t.TempDir()
	outputPath := filepath.Join(dir, "mattermost_import.jsonl")
	teamName := uniqueTeamName("rcsummarye2e")

	users := []any{
		rcBSONUser{ID: "alice-id", Username: "alice", Name: "Alice Anderson", Emails: []rcMail{{Address: "alice@example.com", Verified: true}}, Active: true, Roles: []string{"user"}, Type: "user"},
		rcBSONUser{ID: "carol-id", Username: "carol", Name: "Carol Guest", Emails: []rcMail{{Address: "carol@example.com", Verified: true}}, Active: true, Roles: []string{"guest"}, Type: "user"},
	}
	rooms := []any{
		rcRoom{ID: "engineering-id", Type: "c", Name: "engineering", FName: "Engineering"},
		// carol only appears in a DM (no channel subscription) -> channel-less
		// guest, dropped along with her DM post.
		rcRoom{ID: "alice-carol-dm", Type: "d", Usernames: []string{"alice", "carol"}, UIDs: []string{"alice-id", "carol-id"}},
	}
	messages := []any{
		rcMessage{ID: "eng-root", RoomID: "engineering-id", User: rcMsgUser{ID: "alice-id", Username: "alice"}, Message: "Welcome!"},
		rcMessage{ID: "carol-dm", RoomID: "alice-carol-dm", User: rcMsgUser{ID: "carol-id", Username: "carol"}, Message: "should be dropped"},
	}
	subscriptions := []any{
		rcSubscription{RoomID: "engineering-id", User: rcMsgUser{ID: "alice-id", Username: "alice"}},
	}
	writeDumpDir(t, dir, users, rooms, messages, subscriptions)

	team := th.CreateTeam(ctx, teamName, "RocketChat Summary E2E Team")
	require.NotNil(t, team)

	resetRCFlags()
	commands.RootCmd.SetArgs([]string{
		"transform", "rocketchat",
		"--team", teamName,
		"--dump-dir", dir,
		"--output", outputPath,
		"--skip-attachments",
		"--guest-handling", "guest",
	})
	require.NoError(t, commands.RootCmd.Execute(), "transform command should succeed")

	summaryBytes, err := os.ReadFile(transformRocketChatSummaryFile)
	require.NoError(t, err, "summary file should have been written")
	summary := string(summaryBytes)

	assert.Contains(t, summary, "# RocketChat Transform Summary")
	// alice transformed; channel-less guest carol skipped.
	assert.Contains(t, summary, "| Users | 1 | 1 | 0 |")
	// carol's DM post is dropped along with her.
	assert.Contains(t, summary, "| Direct posts | 0 | 1 | 0 |")
	assert.Contains(t, summary, "can't be imported as a guest",
		"Notes section should explain why carol was skipped")

	// Cross-check #1: mmctl's own import-file validator agrees with the
	// summary's Transformed counts, computed via a completely independent
	// code path (parsing the JSONL rather than mmetl's Intermediate struct).
	validation := th.ValidateImportFileOrFail(ctx, outputPath)
	assert.Equal(t, uint64(1), validation.UserCount,
		"validator's UserCount should match the summary's Transformed Users count")
	assert.Equal(t, uint64(0), validation.DirectPostCount,
		"validator's DirectPostCount should match the summary's Transformed Direct posts count")

	// Cross-check #2: a real import. "Skipped: 1" should correspond to a
	// real, verifiable absence in Mattermost.
	require.NoError(t, th.ImportBulkData(ctx, outputPath), "import should succeed")

	alice := th.AssertUserExists(ctx, "alice")
	assert.False(t, alice.IsGuest(), "alice should be a regular user, not a guest")

	_, err = th.GetUserByUsername(ctx, "carol")
	assert.Error(t, err, "channel-less guest carol should not have been imported into Mattermost")

	// The engineering channel should contain only alice's welcome post; carol
	// never had access to it and her DM post was dropped separately.
	engineering := th.AssertChannelExists(ctx, teamName, "engineering")
	posts, err := th.GetChannelPosts(ctx, engineering.Id, 0, 100)
	require.NoError(t, err)
	assert.Len(t, posts.Order, 1, "engineering channel should have exactly alice's post")
}
