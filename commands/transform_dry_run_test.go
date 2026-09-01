package commands_test

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"

	"github.com/mattermost/mmetl/commands"
	"github.com/mattermost/mmetl/services/slack"
	"github.com/mattermost/mmetl/services/slack/fixtures"
	"github.com/mattermost/mmetl/testhelper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func runTransformSlack(t *testing.T, args ...string) error {
	t.Helper()
	c := commands.RootCmd
	resetCobraFlags(c)
	c.SetArgs(append([]string{"transform", "slack"}, args...))
	return c.Execute()
}

func runTransformRocketChat(t *testing.T, args ...string) error {
	t.Helper()
	c := commands.RootCmd
	resetCobraFlags(c)
	c.SetArgs(append([]string{"transform", "rocketchat"}, args...))
	return c.Execute()
}

func writeZip(t *testing.T, path string, files map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	require.NoError(t, err)
	defer f.Close()

	w := zip.NewWriter(f)
	for name, contents := range files {
		fw, err := w.Create(name)
		require.NoError(t, err)
		_, err = fw.Write([]byte(contents))
		require.NoError(t, err)
	}
	require.NoError(t, w.Close())
}

func dryRunOut(t *testing.T) (output, attachmentsDir string) {
	t.Helper()
	dir := testhelper.WorkDir(t)
	return filepath.Join(dir, "out.jsonl"), filepath.Join(dir, "data")
}

func assertNoDryRunOutput(t *testing.T, output, attachmentsDir string) {
	t.Helper()
	_, err := os.Stat(output)
	assert.True(t, os.IsNotExist(err), "dry-run must not write JSONL")
	_, err = os.Stat(filepath.Join(attachmentsDir, "bulk-export-attachments"))
	assert.True(t, os.IsNotExist(err), "dry-run must not create attachments directory")
}

func missingAttachmentExport(t *testing.T) string {
	t.Helper()
	path := filepath.Join(testhelper.WorkDir(t), "missing-file.zip")
	require.NoError(t, fixtures.NewSlackExportBuilder().
		AddUser(slack.SlackUser{
			Id: "U001", Username: "jane",
			Profile: slack.SlackProfile{Email: "jane@example.com", RealName: "Jane"},
		}).
		AddChannel(slack.SlackChannel{Id: "C001", Name: "general", Creator: "U001", Members: []string{"U001"}}).
		AddPost("general", slack.SlackPost{
			User:      "U001",
			Text:      "see attached",
			TimeStamp: "1704067200.000100",
			Type:      "message",
			File:      &slack.SlackFile{Id: "F001", Name: "notes.txt"},
		}).
		Build(path))
	return path
}

func TestTransformSlackDryRun(t *testing.T) {
	t.Run("requires --team", func(t *testing.T) {
		path := filepath.Join(testhelper.WorkDir(t), "export.zip")
		require.NoError(t, fixtures.SlackBasicExport().Build(path))

		err := runTransformSlack(t, "--dry-run", "--file", path)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "team")
	})

	t.Run("succeeds for a valid export", func(t *testing.T) {
		path := filepath.Join(testhelper.WorkDir(t), "export.zip")
		output, attachmentsDir := dryRunOut(t)
		require.NoError(t, fixtures.ExportWithGuestPosts().Build(path))

		require.NoError(t, runTransformSlack(t,
			"--dry-run",
			"--team", "testteam",
			"--file", path,
			"--output", output,
			"--attachments-dir", attachmentsDir,
			"--guest-handling", "skip",
		))
		assertNoDryRunOutput(t, output, attachmentsDir)
	})

	t.Run("rejects an invalid guest-handling mode", func(t *testing.T) {
		path := filepath.Join(testhelper.WorkDir(t), "export.zip")
		require.NoError(t, fixtures.ExportWithGuestPosts().Build(path))

		err := runTransformSlack(t,
			"--dry-run",
			"--team", "testteam",
			"--file", path,
			"--guest-handling", "bogus",
		)
		require.Error(t, err)
	})

	t.Run("fails when required files are missing", func(t *testing.T) {
		path := filepath.Join(testhelper.WorkDir(t), "incomplete.zip")
		writeZip(t, path, map[string]string{"users.json": "[]"})

		err := runTransformSlack(t, "--dry-run", "--team", "testteam", "--file", path)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "dry-run failed")
	})

	t.Run("fails when bots exist without --bot-owner", func(t *testing.T) {
		path := filepath.Join(testhelper.WorkDir(t), "bots.zip")
		require.NoError(t, fixtures.ExportWithBots().Build(path))

		err := runTransformSlack(t, "--dry-run", "--team", "testteam", "--file", path)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "dry-run failed")
	})

	t.Run("succeeds when bots exist with --bot-owner", func(t *testing.T) {
		path := filepath.Join(testhelper.WorkDir(t), "bots.zip")
		output, attachmentsDir := dryRunOut(t)
		require.NoError(t, fixtures.ExportWithBots().Build(path))

		require.NoError(t, runTransformSlack(t,
			"--dry-run",
			"--team", "testteam",
			"--file", path,
			"--output", output,
			"--attachments-dir", attachmentsDir,
			"--bot-owner", "admin",
		))
		assertNoDryRunOutput(t, output, attachmentsDir)
	})

	t.Run("fails on empty emails without a fallback flag", func(t *testing.T) {
		dir := testhelper.WorkDir(t)
		path := filepath.Join(dir, "noemail.zip")
		output := filepath.Join(dir, "out.jsonl")
		require.NoError(t, fixtures.NewSlackExportBuilder().
			AddUser(slack.SlackUser{Id: "U001", Username: "noemail", Profile: slack.SlackProfile{RealName: "No Email"}}).
			AddChannel(slack.SlackChannel{Id: "C001", Name: "general", Creator: "U001", Members: []string{"U001"}}).
			Build(path))

		err := runTransformSlack(t, "--dry-run", "--team", "testteam", "--file", path)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "dry-run failed")

		err = runTransformSlack(t, "--team", "testteam", "--file", path, "--output", output, "--skip-attachments")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "does not have an email address")
	})

	t.Run("fails when an attachment is missing from the zip", func(t *testing.T) {
		path := missingAttachmentExport(t)
		err := runTransformSlack(t, "--dry-run", "--team", "testteam", "--file", path)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "dry-run failed")
	})

	t.Run("succeeds when a missing attachment is skipped", func(t *testing.T) {
		path := missingAttachmentExport(t)
		output, attachmentsDir := dryRunOut(t)

		require.NoError(t, runTransformSlack(t,
			"--dry-run",
			"--team", "testteam",
			"--file", path,
			"--output", output,
			"--attachments-dir", attachmentsDir,
			"--skip-attachments",
		))
		assertNoDryRunOutput(t, output, attachmentsDir)
	})

	t.Run("succeeds when CheckIntermediate only reports warnings", func(t *testing.T) {
		path := filepath.Join(testhelper.WorkDir(t), "dup-channels.zip")
		output, attachmentsDir := dryRunOut(t)
		require.NoError(t, fixtures.NewSlackExportBuilder().
			AddUser(slack.SlackUser{
				Id: "U001", Username: "jane",
				Profile: slack.SlackProfile{Email: "jane@example.com", RealName: "Jane"},
			}).
			AddChannel(slack.SlackChannel{Id: "C001", Name: "general", Creator: "U001", Members: []string{"U001"}}).
			AddChannel(slack.SlackChannel{Id: "C002", Name: "general", Creator: "U001", Members: []string{"U001"}}).
			Build(path))

		require.NoError(t, runTransformSlack(t,
			"--dry-run",
			"--team", "testteam",
			"--file", path,
			"--output", output,
			"--attachments-dir", attachmentsDir,
		))
		assertNoDryRunOutput(t, output, attachmentsDir)
	})
}

func TestTransformRocketChatDryRun(t *testing.T) {
	t.Run("fails when bots exist without --bot-owner", func(t *testing.T) {
		dir := testhelper.WorkDir(t)
		writeDumpDir(t, dir,
			[]any{
				rcBSONUser{ID: "alice-id", Username: "alice", Name: "Alice", Emails: []rcMail{{Address: "alice@example.com"}}, Active: true, Roles: []string{"user"}, Type: "user"},
				rcBSONUser{ID: "bot-id", Username: "buildbot", Name: "Build Bot", Active: true, Roles: []string{"bot"}, Type: "bot"},
			},
			[]any{rcRoom{ID: "r1", Type: "c", Name: "general", FName: "General"}},
			[]any{},
			[]any{},
		)

		err := runTransformRocketChat(t, "--dry-run", "--team", "testteam", "--dump-dir", dir)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "dry-run failed")
	})

	t.Run("succeeds when bots exist with --bot-owner", func(t *testing.T) {
		dir := testhelper.WorkDir(t)
		output, attachmentsDir := dryRunOut(t)
		writeDumpDir(t, dir,
			[]any{
				rcBSONUser{ID: "alice-id", Username: "alice", Name: "Alice", Emails: []rcMail{{Address: "alice@example.com"}}, Active: true, Roles: []string{"user"}, Type: "user"},
				rcBSONUser{ID: "bot-id", Username: "buildbot", Name: "Build Bot", Active: true, Roles: []string{"bot"}, Type: "bot"},
			},
			[]any{rcRoom{ID: "r1", Type: "c", Name: "general", FName: "General"}},
			[]any{},
			[]any{},
		)

		require.NoError(t, runTransformRocketChat(t,
			"--dry-run",
			"--team", "testteam",
			"--dump-dir", dir,
			"--output", output,
			"--attachments-dir", attachmentsDir,
			"--bot-owner", "admin",
		))
		assertNoDryRunOutput(t, output, attachmentsDir)
	})

	t.Run("succeeds without bots", func(t *testing.T) {
		dir := testhelper.WorkDir(t)
		output, attachmentsDir := dryRunOut(t)
		writeDumpDir(t, dir,
			[]any{rcBSONUser{ID: "alice-id", Username: "alice", Name: "Alice", Emails: []rcMail{{Address: "alice@example.com"}}, Active: true, Roles: []string{"user"}, Type: "user"}},
			[]any{rcRoom{ID: "r1", Type: "c", Name: "general", FName: "General"}},
			[]any{},
			[]any{},
		)

		require.NoError(t, runTransformRocketChat(t,
			"--dry-run",
			"--team", "testteam",
			"--dump-dir", dir,
			"--output", output,
			"--attachments-dir", attachmentsDir,
			"--skip-attachments",
		))
		assertNoDryRunOutput(t, output, attachmentsDir)
	})

	t.Run("fails when required dump files are missing", func(t *testing.T) {
		dir := testhelper.WorkDir(t)
		marshalBSONFileCmds(t, filepath.Join(dir, "users.bson"), []any{})

		err := runTransformRocketChat(t, "--dry-run", "--team", "testteam", "--dump-dir", dir)
		require.Error(t, err)
	})

	t.Run("fails on empty emails without a fallback flag", func(t *testing.T) {
		dir := testhelper.WorkDir(t)
		writeDumpDir(t, dir,
			[]any{rcBSONUser{ID: "alice-id", Username: "alice", Name: "Alice", Active: true, Roles: []string{"user"}, Type: "user"}},
			[]any{rcRoom{ID: "r1", Type: "c", Name: "general", FName: "General"}},
			[]any{},
			[]any{},
		)

		err := runTransformRocketChat(t, "--dry-run", "--team", "testteam", "--dump-dir", dir, "--skip-attachments")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "dry-run failed")
	})

	t.Run("fails when a GridFS upload is missing chunks", func(t *testing.T) {
		dir := testhelper.WorkDir(t)
		writeDumpDir(t, dir,
			[]any{rcBSONUser{ID: "alice-id", Username: "alice", Name: "Alice", Emails: []rcMail{{Address: "alice@example.com"}}, Active: true, Roles: []string{"user"}, Type: "user"}},
			[]any{rcRoom{ID: "r1", Type: "c", Name: "general", FName: "General"}},
			[]any{},
			[]any{},
		)
		marshalBSONFileCmds(t, filepath.Join(dir, "rocketchat_uploads.bson"), []any{
			rcBSONUpload{ID: "up1", Name: "photo.jpg", Size: 1, Store: "GridFS:Uploads", Complete: true},
		})

		err := runTransformRocketChat(t, "--dry-run", "--team", "testteam", "--dump-dir", dir)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "dry-run failed")
	})

	t.Run("succeeds when a missing GridFS upload is skipped", func(t *testing.T) {
		dir := testhelper.WorkDir(t)
		output, attachmentsDir := dryRunOut(t)
		writeDumpDir(t, dir,
			[]any{rcBSONUser{ID: "alice-id", Username: "alice", Name: "Alice", Emails: []rcMail{{Address: "alice@example.com"}}, Active: true, Roles: []string{"user"}, Type: "user"}},
			[]any{rcRoom{ID: "r1", Type: "c", Name: "general", FName: "General"}},
			[]any{},
			[]any{},
		)
		marshalBSONFileCmds(t, filepath.Join(dir, "rocketchat_uploads.bson"), []any{
			rcBSONUpload{ID: "up1", Name: "photo.jpg", Size: 1, Store: "GridFS:Uploads", Complete: true},
		})

		require.NoError(t, runTransformRocketChat(t,
			"--dry-run",
			"--team", "testteam",
			"--dump-dir", dir,
			"--output", output,
			"--attachments-dir", attachmentsDir,
			"--skip-attachments",
		))
		assertNoDryRunOutput(t, output, attachmentsDir)
	})
}

type rcBSONUpload struct {
	ID       string `bson:"_id"`
	Name     string `bson:"name"`
	Size     int64  `bson:"size"`
	Store    string `bson:"store"`
	Complete bool   `bson:"complete"`
}
