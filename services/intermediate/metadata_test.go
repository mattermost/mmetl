package intermediate

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mattermost/mattermost/server/v8/channels/app/imports"
	log "github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// metadataFixture builds an Exporter with a stamped report over a small
// Intermediate, so one run can be asserted on from all three sides.
func metadataFixture(t *testing.T) *Exporter {
	t.Helper()

	started := time.Date(2026, 8, 26, 10, 11, 10, 0, time.UTC)
	NowFunc = func() time.Time { return time.Date(2026, 8, 26, 10, 11, 12, 123456789, time.UTC) }
	t.Cleanup(func() { NowFunc = time.Now })

	logger := log.New()
	logger.SetOutput(&bytes.Buffer{})

	report := NewReport(logger)
	report.Metadata.Additional = &Additional{
		Source: SourceInfo{
			Platform:  "slack",
			File:      "my_export.zip",
			SizeBytes: 12345678,
		},
		Target: TargetInfo{Team: "myteam", Output: "bulk-export.jsonl"},
		Run: RunInfo{
			Version:   "v1.2.3",
			BuildHash: "9f2c1ab",
			Command:   "mmetl transform slack",
			Flags:     "--file=my_export.zip --guest-handling=skip",
			Started:   started,
		},
	}
	report.Users().Seen(2)

	return &Exporter{
		TeamName:     "myteam",
		Logger:       logger,
		Report:       report,
		Intermediate: countsFixture(),
	}
}

// countsFixture is an Intermediate with one of everything the counts cover,
// including reactions and attachments nested under a reply.
func countsFixture() *Intermediate {
	return &Intermediate{
		PublicChannels:  []*IntermediateChannel{{Name: "town-square"}, {Name: "random"}},
		PrivateChannels: []*IntermediateChannel{{Name: "leads"}},
		GroupChannels:   []*IntermediateChannel{{Name: "mpim"}},
		DirectChannels:  []*IntermediateChannel{{Name: "dm1"}, {Name: "dm2"}, {Name: "dm3"}},
		UsersById: map[string]*IntermediateUser{
			"U1": {Id: "U1", Username: "one"},
			"U2": {Id: "U2", Username: "two"},
			"B1": {Id: "B1", Username: "bot", IsBot: true},
		},
		Posts: []*IntermediatePost{
			{
				Message:     "root",
				Attachments: []string{"a.png", "b.png"},
				Reactions:   []*IntermediateReaction{{EmojiName: "+1"}},
				Replies: []*IntermediatePost{
					{
						Message:     "reply one",
						Attachments: []string{"c.png"},
						Reactions:   []*IntermediateReaction{{EmojiName: "tada"}, {EmojiName: "eyes"}},
					},
					{Message: "reply two"},
				},
			},
			{Message: "standalone"},
		},
	}
}

// TestMetadataOneModelThreeRenderings is the assertion that pins the shared
// model: a single run's version line, JSON report and Markdown report all have
// to be readings of the same Info.
func TestMetadataOneModelThreeRenderings(t *testing.T) {
	exporter := metadataFixture(t)

	var buf bytes.Buffer
	require.NoError(t, exporter.ExportVersion(&buf))

	var line imports.LineImportData
	require.NoError(t, json.Unmarshal(buf.Bytes(), &line))

	t.Run("version line", func(t *testing.T) {
		assert.Equal(t, "version", line.Type)
		require.NotNil(t, line.Version)
		assert.Equal(t, 1, *line.Version, "a version other than 1 fails the whole import")
		require.NotNil(t, line.Info)

		assert.Equal(t, "mmetl", line.Info.Generator)
		assert.Equal(t, "v1.2.3 (9f2c1ab)", line.Info.Version)
		created, err := time.Parse(time.RFC3339Nano, line.Info.Created)
		require.NoError(t, err)
		assert.Equal(t, time.Date(2026, 8, 26, 10, 11, 12, 123456789, time.UTC), created.UTC())
	})

	var fromLine Additional
	require.NotNil(t, line.Info)
	require.NoError(t, json.Unmarshal(line.Info.Additional, &fromLine))

	t.Run("additional", func(t *testing.T) {
		assert.Equal(t, "slack", fromLine.Source.Platform)
		assert.Equal(t, "my_export.zip", fromLine.Source.File)
		assert.Equal(t, int64(12345678), fromLine.Source.SizeBytes)
		assert.Equal(t, "myteam", fromLine.Target.Team)
		assert.Equal(t, "mmetl transform slack", fromLine.Run.Command)
		// The version line is written before the run ends, so this is the one
		// field the two renderings differ on until RewriteVersion.
		assert.True(t, fromLine.Run.Finished.IsZero(), "the run has not finished when line 1 is written")
		assert.Equal(t, Counts{}, fromLine.Counts, "counts wait until every line is written")
	})

	// ExportVersion only; RewriteVersion is not called, so Finish is what
	// stamps the report's finish time.
	exporter.Report.Finish(nil)

	t.Run("json report carries the same model", func(t *testing.T) {
		encoded, err := exporter.Report.JSON()
		require.NoError(t, err)

		var decoded struct {
			Metadata Info `json:"metadata"`
		}
		require.NoError(t, json.Unmarshal(encoded, &decoded))

		assert.Equal(t, "mmetl", decoded.Metadata.Generator)
		assert.Equal(t, "v1.2.3 (9f2c1ab)", decoded.Metadata.Version)
		require.NotNil(t, decoded.Metadata.Additional)

		fromReport := *decoded.Metadata.Additional
		assert.False(t, fromReport.Run.Finished.IsZero(), "Finish stamps the report's finish time")

		// Zero Finished so the rest of Additional matches the version-line snapshot.
		fromReport.Run.Finished = time.Time{}
		assert.Equal(t, fromLine, fromReport)
	})

	t.Run("markdown report reads the same model", func(t *testing.T) {
		markdown := exporter.Report.Markdown()

		assert.Contains(t, markdown, "# Slack Transform Report")
		assert.Contains(t, markdown, "| Provider | slack")
		assert.Contains(t, markdown, "| mmetl    | v1.2.3 (9f2c1ab)")
		assert.Contains(t, markdown, "| Input    | my_export.zip")
		assert.Contains(t, markdown, "| Team     | myteam")
		assert.Contains(t, markdown, "| Output   | bulk-export.jsonl")
		assert.Contains(t, markdown, "--guest-handling=skip")

		assert.NotContains(t, markdown, "## Produced",
			"counts are not a prediction; they wait for RewriteVersion")
	})
}

// TestProducedRowsSkipZeroCounts covers the reason the Produced table is built
// from non-zero counts: a Slack export with no group channels should not render
// a row of zeroes.
func TestProducedRowsSkipZeroCounts(t *testing.T) {
	report := NewReport(nil)
	report.Metadata.Additional.Counts = Counts{Users: 3, Posts: 7}

	assert.Equal(t, [][]string{{"Users", "3"}, {"Posts", "7"}}, report.producedRows())

	t.Run("no counts at all renders no section", func(t *testing.T) {
		empty := NewReport(nil)
		empty.Finish(nil)
		assert.Empty(t, empty.producedRows())
		assert.NotContains(t, empty.Markdown(), "## Produced")
	})
}

func TestIntermediateCounts(t *testing.T) {
	counts := countsFixture().Counts()

	assert.Equal(t, Counts{
		Users:           2,
		Bots:            1,
		PublicChannels:  2,
		PrivateChannels: 1,
		GroupChannels:   1,
		DirectChannels:  3,
		Posts:           2,
		Replies:         2,
		Reactions:       3, // one on the root, two on a reply
		Attachments:     3, // two on the root, one on a reply
	}, counts)

	t.Run("a nil Intermediate counts as empty", func(t *testing.T) {
		var nilIntermediate *Intermediate
		assert.Equal(t, Counts{}, nilIntermediate.Counts())
	})
}

// TestExportVersionWithoutReport pins Exporter.Report's documented contract: a
// nil report is valid and inert. The metadata is the report's, so without one
// line 1 is the bare version line the format requires — not a panic, and not a
// half-populated `info`.
func TestExportVersionWithoutReport(t *testing.T) {
	exporter := &Exporter{TeamName: "myteam", Intermediate: countsFixture()}

	var buf bytes.Buffer
	require.NoError(t, exporter.ExportVersion(&buf))

	var line imports.LineImportData
	require.NoError(t, json.Unmarshal(buf.Bytes(), &line))

	assert.Equal(t, "version", line.Type)
	require.NotNil(t, line.Version)
	assert.Equal(t, 1, *line.Version)
	assert.Nil(t, line.Info, "the metadata comes from the report; there is none")
}

// TestMetadataShipsNoPaths is the safety property of the shared model: the
// version line goes to a Mattermost server, so nothing in it may carry the
// operator's filesystem layout.
func TestMetadataShipsNoPaths(t *testing.T) {
	exporter := metadataFixture(t)
	exporter.Report.Metadata.Additional.Source.File = "export.zip"
	exporter.Report.Metadata.Additional.Run.Flags = "--file=export.zip --output=bulk-export.jsonl"

	var buf bytes.Buffer
	require.NoError(t, exporter.ExportVersion(&buf))

	assert.NotContains(t, buf.String(), "/Users/")
	assert.NotContains(t, exporter.Report.Markdown(), "/Users/")

	var line imports.LineImportData
	require.NoError(t, json.Unmarshal(buf.Bytes(), &line))
	var additional Additional
	require.NoError(t, json.Unmarshal(line.Info.Additional, &additional))

	for _, value := range []string{additional.Source.File, additional.Target.Output} {
		assert.NotContains(t, value, "/")
		assert.NotContains(t, value, "\\")
	}
}

// TestVersionLineStaysBounded guards the reason Additional is fixed-cardinality:
// the version line shares the importer's 16 MB per-line scanner budget, and a
// future field holding an unbounded list should trip a test rather than a
// scanner.
func TestVersionLineStaysBounded(t *testing.T) {
	exporter := metadataFixture(t)

	var buf bytes.Buffer
	require.NoError(t, exporter.ExportVersion(&buf))

	// The line is padded to a fixed width so it can be rewritten in place, so
	// what needs bounding is the JSON inside it, not the line.
	assert.Equal(t, VersionLineSize, buf.Len(), "line 1 is a fixed-width reservation")
	assert.Equal(t, byte('\n'), buf.Bytes()[VersionLineSize-1], "the reservation ends in the newline")

	payload := bytes.TrimRight(buf.Bytes(), " \n")
	assert.Less(t, len(payload), VersionLineSize/2,
		"the metadata must stay well inside its reservation; no lists in Additional")

	assert.Equal(t, bytes.Repeat([]byte{' '}, VersionLineSize-len(payload)-1),
		buf.Bytes()[len(payload):VersionLineSize-1])
	assert.Equal(t, 1, strings.Count(strings.TrimRight(buf.String(), "\n"), "\n")+1,
		"the version line must be exactly one line")
}

// TestExportRewritesVersionLineWithFinishTime covers why line 1 is a fixed-width
// reservation: the bulk import format puts the version line first, but the run's
// finish time is only known once every other line is written.
func TestExportRewritesVersionLineWithFinishTime(t *testing.T) {
	exporter := metadataFixture(t)

	path := filepath.Join(t.TempDir(), "bulk-export.jsonl")
	// The fixture has a bot, which the export refuses to write without an owner.
	require.NoError(t, exporter.Export(path, "admin"))

	raw, err := os.ReadFile(path)
	require.NoError(t, err)

	lines := bytes.Split(bytes.TrimRight(raw, "\n"), []byte{'\n'})
	require.Greater(t, len(lines), 1, "the export must have written more than line 1")
	require.Len(t, lines[0], VersionLineSize-1, "line 1 keeps its reservation after the rewrite")

	var line imports.LineImportData
	require.NoError(t, json.Unmarshal(lines[0], &line), "a padded line still parses")
	require.NotNil(t, line.Info)

	var additional Additional
	require.NoError(t, json.Unmarshal(line.Info.Additional, &additional))

	require.False(t, additional.Run.Finished.IsZero(), "the rewritten line 1 carries a real finish time")
	assert.False(t, additional.Run.Finished.Before(additional.Run.Started),
		"the run cannot finish before it started")
	assert.Equal(t, countsFixture().Counts(), additional.Counts,
		"counts are stamped only once every other line is on disk")

	// The rewrite must not move the file's creation time to the end of the run.
	assert.Equal(t, exporter.Report.Metadata.Created, line.Info.Created)

	t.Run("the report and the rewritten line now agree in full", func(t *testing.T) {
		exporter.Report.Finish(nil)

		encoded, err := exporter.Report.JSON()
		require.NoError(t, err)
		var decoded struct {
			Metadata Info `json:"metadata"`
		}
		require.NoError(t, json.Unmarshal(encoded, &decoded))
		require.NotNil(t, decoded.Metadata.Additional)

		// Finish does not overwrite Finished once RewriteVersion has set it.
		assert.Equal(t, additional, *decoded.Metadata.Additional)

		markdown := exporter.Report.Markdown()
		assert.Contains(t, markdown, "## Produced")
		assert.Contains(t, markdown, "| Posts            | 2")
		assert.Contains(t, markdown, "| Replies          | 2")
		assert.Contains(t, markdown, "| Bots             | 1")
	})

	t.Run("the lines after it are untouched", func(t *testing.T) {
		for _, raw := range lines[1:] {
			var line imports.LineImportData
			require.NoError(t, json.Unmarshal(raw, &line))
			assert.NotEqual(t, "version", line.Type, "only line 1 is the version line")
			assert.NotEmpty(t, line.Type)
		}
	})
}

// TestExportDoesNotStampCountsOnPartialFailure covers why counts wait for
// RewriteVersion: a file that stopped mid-export must not claim the lines it
// never wrote, in either the version line or the report next to it.
func TestExportDoesNotStampCountsOnPartialFailure(t *testing.T) {
	exporter := metadataFixture(t)
	path := filepath.Join(t.TempDir(), "bulk-export.jsonl")

	err := exporter.Export(path, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bot owner")

	assert.Equal(t, Counts{}, exporter.Report.Metadata.details().Counts)

	raw, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	firstLine, _, found := bytes.Cut(raw, []byte{'\n'})
	require.True(t, found, "a partial export still writes line 1")

	var line imports.LineImportData
	require.NoError(t, json.Unmarshal(firstLine, &line))
	require.NotNil(t, line.Info)
	var additional Additional
	require.NoError(t, json.Unmarshal(line.Info.Additional, &additional))
	assert.Equal(t, Counts{}, additional.Counts)
	assert.True(t, additional.Run.Finished.IsZero())

	exporter.Report.Finish(err)
	assert.NotContains(t, exporter.Report.Markdown(), "## Produced")
}

// failingWriterAt rejects WriteAt so RewriteVersion can be asserted without a
// real filesystem failure.
type failingWriterAt struct{}

func (failingWriterAt) WriteAt([]byte, int64) (int, error) {
	return 0, errors.New("disk full")
}

// TestRewriteVersionDoesNotCommitOnWriteAtFailure covers why Finished and
// Counts land on the report only after WriteAt succeeds: a rewrite that fails
// must not leave the report claiming a finish the import file never recorded.
func TestRewriteVersionDoesNotCommitOnWriteAtFailure(t *testing.T) {
	exporter := metadataFixture(t)
	exporter.Report.Metadata.Generator = generatorName
	exporter.Report.Metadata.Created = NowFunc().UTC().Format(time.RFC3339Nano)
	exporter.Report.Metadata.Version = exporter.Report.Metadata.VersionString()

	err := exporter.RewriteVersion(failingWriterAt{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rewriting the version line")

	details := exporter.Report.Metadata.details()
	assert.True(t, details.Run.Finished.IsZero(), "Finished must stay zero when WriteAt fails")
	assert.Equal(t, Counts{}, details.Counts, "Counts must stay empty when WriteAt fails")
}

func TestInfoVersionString(t *testing.T) {
	for name, testCase := range map[string]struct {
		info     Info
		expected string
	}{
		"version and hash": {
			info:     Info{Additional: &Additional{Run: RunInfo{Version: "v1.2.3", BuildHash: "9f2c1ab"}}},
			expected: "v1.2.3 (9f2c1ab)",
		},
		"version only": {
			info:     Info{Additional: &Additional{Run: RunInfo{Version: "v1.2.3"}}},
			expected: "v1.2.3",
		},
		"nothing stamped falls back to whatever was set directly": {
			info:     Info{Version: "already rendered"},
			expected: "already rendered",
		},
		"empty": {info: Info{}, expected: ""},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, testCase.expected, testCase.info.VersionString())
		})
	}
}

func TestRunInfoDuration(t *testing.T) {
	started := time.Date(2026, 8, 26, 10, 11, 10, 0, time.UTC)

	for name, testCase := range map[string]struct {
		run      RunInfo
		expected time.Duration
	}{
		"not finished":   {run: RunInfo{Started: started}, expected: 0},
		"not started":    {run: RunInfo{Finished: started}, expected: 0},
		"finished first": {run: RunInfo{Started: started, Finished: started.Add(-time.Second)}, expected: 0},
		"minutes round to the second": {
			run:      RunInfo{Started: started, Finished: started.Add(2*time.Minute + 38*time.Second + 400*time.Millisecond)},
			expected: 2*time.Minute + 38*time.Second,
		},
		"sub-second rounds to the millisecond": {
			run:      RunInfo{Started: started, Finished: started.Add(12*time.Millisecond + 400*time.Microsecond)},
			expected: 12 * time.Millisecond,
		},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, testCase.expected, testCase.run.Duration())
		})
	}
}
