package commands_test

import (
	"archive/zip"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mattermost/mmetl/commands"
	"github.com/mattermost/mmetl/services/intermediate"
	"github.com/mattermost/mmetl/services/slack"
	"github.com/mattermost/mmetl/services/slack/fixtures"
)

// runTransformSlack runs `transform slack` against the given fixture, writing
// the bulk import file to outputPath.
func runTransformSlackExport(t *testing.T, export *fixtures.SlackExportBuilder, outputPath string, extraArgs ...string) error {
	t.Helper()

	slackExportPath := filepath.Join(t.TempDir(), "slack_export.zip")
	require.NoError(t, export.Build(slackExportPath))

	args := append([]string{
		"transform", "slack",
		"--team", "myteam",
		"--file", slackExportPath,
		"--output", outputPath,
		"--skip-attachments",
	}, extraArgs...)

	c := commands.RootCmd
	resetCobraFlags(c)
	c.SetArgs(args)
	return c.Execute()
}

// TestTransformWritesArtifactsNextToOutput covers the artifact placement rule:
// the report and the log follow the bulk import file rather than landing in
// whatever directory the operator happened to run mmetl from.
func TestTransformWritesArtifactsNextToOutput(t *testing.T) {
	// A subdirectory that does not exist yet, so this also covers --output
	// pointing somewhere that has to be created.
	dir := filepath.Join(t.TempDir(), "migration")
	outputPath := filepath.Join(dir, "mm_export.jsonl")

	require.NoError(t, runTransformSlackExport(t, fixtures.ExportWithGuestPosts(), outputPath, "--guest-handling", "skip"))

	for _, name := range []string{
		"mm_export.jsonl",
		intermediate.ReportMarkdownFilename,
		intermediate.ReportJSONFilename,
		"transform-slack.log",
	} {
		assert.FileExists(t, filepath.Join(dir, name))
	}
}

func TestTransformReportContents(t *testing.T) {
	dir := t.TempDir()
	outputPath := filepath.Join(dir, "mm_export.jsonl")

	require.NoError(t, runTransformSlackExport(t, fixtures.ExportWithGuestPosts(), outputPath, "--guest-handling", "skip"))

	t.Run("markdown names every dropped entity", func(t *testing.T) {
		raw, err := os.ReadFile(filepath.Join(dir, intermediate.ReportMarkdownFilename))
		require.NoError(t, err)
		markdown := string(raw)

		assert.Contains(t, markdown, "# Slack Transform Report")
		assert.Contains(t, markdown, "| Provider | slack")
		assert.Contains(t, markdown, "| Team     | myteam")
		assert.NotContains(t, markdown, "## Stopped because of an error")

		// The flags the operator set are recorded, so a report explains itself.
		assert.Contains(t, markdown, "--guest-handling=skip")

		assert.Contains(t, markdown, "### Users")
		assert.Contains(t, markdown, "[^guest-skip-mode]")
		assert.Contains(t, markdown, "- **U002** (`multi.guest`)")
		assert.Contains(t, markdown, "- **U003** (`single.guest`)")
	})

	t.Run("json carries the same outcomes", func(t *testing.T) {
		raw, err := os.ReadFile(filepath.Join(dir, intermediate.ReportJSONFilename))
		require.NoError(t, err)

		var report transformReport
		require.NoError(t, json.Unmarshal(raw, &report))

		assert.Equal(t, "mmetl", report.Metadata.Generator)
		require.NotNil(t, report.Metadata.Additional)
		assert.Equal(t, "slack", report.Metadata.Additional.Source.Platform)
		assert.Equal(t, "myteam", report.Metadata.Additional.Target.Team)
		assert.Equal(t, filepath.Base(outputPath), report.Metadata.Additional.Target.Output)
		assert.Empty(t, report.Error)

		assert.ElementsMatch(t, []string{"multi.guest", "single.guest"},
			report.skippedNames("user", "guest_skip_mode"))
		assert.Equal(t, 1, report.Entities["user"].Transformed)
		assert.Equal(t, 2, report.Entities["user"].Skipped)
	})

	t.Run("line 1 of the import file carries the same metadata", func(t *testing.T) {
		raw, err := os.ReadFile(outputPath)
		require.NoError(t, err)

		firstLine, _, found := strings.Cut(string(raw), "\n")
		require.True(t, found, "the bulk import file must have more than the version line")

		var line struct {
			Type    string `json:"type"`
			Version *int   `json:"version"`
			Info    *struct {
				Generator  string                  `json:"generator"`
				Version    string                  `json:"version"`
				Created    string                  `json:"created"`
				Additional intermediate.Additional `json:"additional"`
			} `json:"info"`
		}
		require.NoError(t, json.Unmarshal([]byte(firstLine), &line))

		assert.Equal(t, "version", line.Type)
		require.NotNil(t, line.Version)
		assert.Equal(t, 1, *line.Version)
		require.NotNil(t, line.Info)
		assert.Equal(t, "mmetl", line.Info.Generator)

		additional := line.Info.Additional
		assert.Equal(t, "slack", additional.Source.Platform)
		assert.Positive(t, additional.Source.SizeBytes, "the export's size is recorded")
		assert.Equal(t, "myteam", additional.Target.Team)
		assert.Equal(t, "mmetl transform slack", additional.Run.Command)
		assert.Positive(t, additional.Counts.Users, "a run that produced users must say so")
		assert.Positive(t, additional.Counts.PublicChannels)

		// The import file gets shipped to a server, so it must not carry the
		// operator's filesystem layout. --file is an absolute temp path here.
		assert.NotContains(t, firstLine, string(os.PathSeparator))
	})

	t.Run("two runs of the same export produce identical reports", func(t *testing.T) {
		otherDir := t.TempDir()
		require.NoError(t, runTransformSlackExport(t, fixtures.ExportWithGuestPosts(), filepath.Join(otherDir, "mm_export.jsonl"), "--guest-handling", "skip"))

		first := readReportSection(t, filepath.Join(dir, intermediate.ReportMarkdownFilename))
		second := readReportSection(t, filepath.Join(otherDir, intermediate.ReportMarkdownFilename))
		assert.Equal(t, first, second, "reports of the same export must be diffable")
	})
}

// TestTransformReportWrittenOnFailure covers the reason the report write is
// deferred: a run that aborts is exactly when an operator needs to know how far
// it got.
func TestTransformReportWrittenOnFailure(t *testing.T) {
	dir := t.TempDir()
	outputPath := filepath.Join(dir, "mm_export.jsonl")

	// A user with no email address aborts the run unless --skip-empty-emails or
	// --default-email-domain was given. It used to call os.Exit, which is
	// exactly the path that would have skipped the report.
	export := fixtures.ExportWithGuestPosts().AddUser(slack.SlackUser{
		Id:       "U0NOEMAIL",
		Username: "no.email",
		Profile:  slack.SlackProfile{RealName: "No Email"},
	})

	err := runTransformSlackExport(t, export, outputPath, "--guest-handling", "skip")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--default-email-domain")

	raw, readErr := os.ReadFile(filepath.Join(dir, intermediate.ReportMarkdownFilename))
	require.NoError(t, readErr, "an aborted run must still leave a report")

	markdown := string(raw)
	assert.Contains(t, markdown, "## Stopped because of an error")
	assert.Contains(t, markdown, "does not have an email address")

	var report transformReport
	rawJSON, readErr := os.ReadFile(filepath.Join(dir, intermediate.ReportJSONFilename))
	require.NoError(t, readErr)
	require.NoError(t, json.Unmarshal(rawJSON, &report))
	assert.Contains(t, report.Error, "does not have an email address")

	// The work done before the failure is still accounted for.
	assert.NotEmpty(t, report.skippedNames("user", "guest_skip_mode"))
}

// TestTransformRejectsEmptySlackExport covers the reason the zip is checked for
// files after it opens: a valid empty archive has a non-nil, zero-length File
// slice, and used to exit 0 with no import file and no explanation.
func TestTransformRejectsEmptySlackExport(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "empty.zip")
	outputPath := filepath.Join(dir, "mm_export.jsonl")

	f, err := os.Create(zipPath)
	require.NoError(t, err)
	w := zip.NewWriter(f)
	require.NoError(t, w.Close())
	require.NoError(t, f.Close())

	c := commands.RootCmd
	resetCobraFlags(c)
	c.SetArgs([]string{
		"transform", "slack",
		"--team", "myteam",
		"--file", zipPath,
		"--output", outputPath,
		"--skip-attachments",
	})
	err = c.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "contains no files")
	assert.NoFileExists(t, outputPath)

	raw, readErr := os.ReadFile(filepath.Join(dir, intermediate.ReportMarkdownFilename))
	require.NoError(t, readErr, "an empty archive must still leave a report")
	assert.Contains(t, string(raw), "## Stopped because of an error")
	assert.Contains(t, string(raw), "contains no files")
}

// TestTransformReportWrittenWhenInputUnreadable covers the failures that happen
// before the transform starts but after there is somewhere to write: they used
// to exit with no report at all.
func TestTransformReportWrittenWhenInputUnreadable(t *testing.T) {
	dir := t.TempDir()
	outputPath := filepath.Join(dir, "mm_export.jsonl")

	c := commands.RootCmd
	resetCobraFlags(c)
	c.SetArgs([]string{
		"transform", "slack",
		"--team", "myteam",
		"--file", filepath.Join(dir, "does-not-exist.zip"),
		"--output", outputPath,
		"--skip-attachments",
	})
	require.Error(t, c.Execute())

	raw, err := os.ReadFile(filepath.Join(dir, intermediate.ReportMarkdownFilename))
	require.NoError(t, err, "a run that cannot open its export must still leave a report")
	assert.Contains(t, string(raw), "## Stopped because of an error")
	assert.Contains(t, string(raw), "does-not-exist.zip")
}

// readReportSection returns the report with the `## Run` block removed, since
// that block records timings and paths that legitimately differ between runs.
func readReportSection(t *testing.T, path string) string {
	t.Helper()

	raw, err := os.ReadFile(path)
	require.NoError(t, err)

	markdown := string(raw)
	start := strings.Index(markdown, "\n## Run\n")
	end := strings.Index(markdown, "\n## Summary\n")
	require.Positive(t, start)
	require.Greater(t, end, start)

	return markdown[:start] + markdown[end:]
}
