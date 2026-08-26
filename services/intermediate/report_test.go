package intermediate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mattermost/mattermost/server/public/model"
	log "github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Reasons used only by these tests. They are registered like any other, which
// is also what proves a provider package can add its own.
var (
	testReasonSkip = RegisterReason(&Reason{
		Code:   "test_skip",
		Short:  "was skipped for a reason",
		Detail: "The long explanation of the skip.",
		Skip:   true,
	})
	testReasonNote = RegisterReason(&Reason{
		Code:      "test_note",
		Short:     "was changed on the way in",
		Specifics: "changed to %s",
		Detail:    "The long explanation of the change.",
	})
	testReasonQuiet = RegisterReason(&Reason{
		Code:   "test_quiet",
		Short:  "was skipped in bulk",
		Detail: "The long explanation of the bulk skip.",
		Skip:   true,
		Quiet:  true,
	})
)

func TestReportNilIsInert(t *testing.T) {
	var report *Report

	// Every recording call has to survive a nil report, because an Exporter
	// built as a struct literal has no report at all.
	require.NotPanics(t, func() {
		report.Users().Seen(3)
		report.Posts().Skip("p1", "ana", testReasonSkip)
		report.For(EntityFile).Note("f1", "a.png", testReasonNote, "b.png")
		report.Finish(nil)
	})

	assert.Empty(t, report.Markdown())
	assert.Empty(t, report.SummaryText("a", "b"))

	encoded, err := report.JSON()
	require.NoError(t, err)
	assert.Nil(t, encoded)

	markdownPath, jsonPath, err := report.Write(t.TempDir())
	require.NoError(t, err)
	assert.Empty(t, markdownPath)
	assert.Empty(t, jsonPath)
}

func TestReportTransformedIsDerived(t *testing.T) {
	t.Run("transformed is source total minus skipped", func(t *testing.T) {
		report := NewReport(nil)
		report.Users().Seen(10)
		report.Users().Skip("u1", "ana", testReasonSkip)
		report.Users().Skip("u2", "bob", testReasonSkip)
		// A note on a transformed entity must not move either counter.
		report.Users().Note("u3", "cy", testReasonNote, "cyd")

		report.Finish(nil)

		assert.Equal(t, 8, report.Users().Transformed)
		assert.Equal(t, 2, report.Users().Skipped)
	})

	t.Run("more skips than source entities never goes negative", func(t *testing.T) {
		report := NewReport(nil)
		report.Posts().Skip("p1", "", testReasonSkip)

		report.Finish(nil)

		assert.Equal(t, 0, report.Posts().Transformed)
		assert.Equal(t, 1, report.Posts().Skipped)
	})

	t.Run("Seen accumulates across calls", func(t *testing.T) {
		report := NewReport(nil)
		report.Posts().Seen(4)
		report.Posts().Seen(6)

		report.Finish(nil)

		assert.Equal(t, 10, report.Posts().Transformed)
	})

	t.Run("Finish is idempotent", func(t *testing.T) {
		report := NewReport(nil)
		report.Users().Seen(3)
		report.Users().Skip("u1", "ana", testReasonSkip)

		report.Finish(nil)
		report.Finish(nil)

		assert.Equal(t, 2, report.Users().Transformed)
	})
}

func TestReportFinishRecordsError(t *testing.T) {
	report := NewReport(nil)
	report.Finish(assert.AnError)

	assert.Equal(t, assert.AnError.Error(), report.Error)
	assert.Contains(t, report.Markdown(), "## Stopped because of an error")
	assert.Contains(t, report.Markdown(), "    "+assert.AnError.Error())
}

func TestReportLogging(t *testing.T) {
	newLogger := func() (*log.Logger, *strings.Builder) {
		buffer := &strings.Builder{}
		logger := log.New()
		logger.SetOutput(buffer)
		logger.SetFormatter(&log.TextFormatter{DisableTimestamp: true})
		return logger, buffer
	}

	t.Run("a skip logs a warning carrying the reason", func(t *testing.T) {
		logger, buffer := newLogger()
		report := NewReport(logger)

		report.Users().Skip("U004", "channelless.guest", testReasonSkip)

		output := buffer.String()
		assert.Contains(t, output, "level=warning")
		assert.Contains(t, output, "entity_kind=user")
		assert.Contains(t, output, "entity_id=U004")
		assert.Contains(t, output, "reason_code=test_skip")
		assert.Contains(t, output, testReasonSkip.Short)
		assert.Contains(t, output, testReasonSkip.Detail)
	})

	t.Run("a note logs at info and includes its specifics", func(t *testing.T) {
		logger, buffer := newLogger()
		report := NewReport(logger)

		report.Emoji().Note("café", "cafe", testReasonNote, "cafe")

		output := buffer.String()
		assert.Contains(t, output, "level=info")
		assert.Contains(t, output, "changed to cafe")
	})

	t.Run("a quiet reason logs once in aggregate, not per entity", func(t *testing.T) {
		logger, buffer := newLogger()
		report := NewReport(logger)

		for _, id := range []string{"p1", "p2", "p3"} {
			report.Posts().Skip(id, "", testReasonQuiet)
		}
		assert.Empty(t, buffer.String(), "a quiet reason must not log per entity")

		report.Finish(nil)

		output := buffer.String()
		assert.Contains(t, output, "test_quiet")
		assert.Contains(t, output, "=3")
		// The notes are still kept in full: suppressing the log lines must not
		// suppress the report entries.
		assert.Len(t, report.Posts().Notes, 3)
	})
}

// TestReportDeterminism is the point of the sorting rules: two runs over the
// same export must produce byte-identical reports, or they cannot be diffed.
func TestReportDeterminism(t *testing.T) {
	// Pin the clock so the only thing that can differ between the two reports
	// is the order the notes were recorded in.
	NowFunc = func() time.Time { return time.Date(2026, 8, 24, 10, 16, 41, 0, time.UTC) }
	t.Cleanup(func() { NowFunc = time.Now })

	build := func(order []string) *Report {
		report := NewReport(nil)
		report.Users().Seen(10)
		for _, id := range order {
			report.Users().Skip(id, id+"-name", testReasonSkip)
		}
		report.Users().Note("u9", "nine", testReasonNote, "renamed")
		report.Finish(nil)
		return report
	}

	forward := build([]string{"u1", "u2", "u3"})
	backward := build([]string{"u3", "u1", "u2"})

	assert.Equal(t, forward.Markdown(), backward.Markdown())

	forwardJSON, err := forward.JSON()
	require.NoError(t, err)
	backwardJSON, err := backward.JSON()
	require.NoError(t, err)
	assert.Equal(t, string(forwardJSON), string(backwardJSON))
}

func TestReportMarkdown(t *testing.T) {
	started := time.Date(2026, 8, 24, 10, 14, 3, 0, time.UTC)
	report := NewReport(nil)
	report.Metadata.Additional = &Additional{
		Source: SourceInfo{Platform: "slack", File: "my_export.zip"},
		Target: TargetInfo{Team: "myteam", Output: "mm_export.jsonl"},
		Run: RunInfo{
			Version:   "v0.5.1",
			BuildHash: "721d761",
			Flags:     "--guest-handling=guest",
			Started:   started,
			Finished:  started.Add(2*time.Minute + 38*time.Second),
		},
	}
	report.Users().Seen(4)
	report.Users().Skip("U004", "channelless.guest", testReasonSkip)
	report.Users().Note("U009", "long.name", testReasonNote, "shortened")
	report.PublicChannels().Seen(12)
	report.Posts().Seen(8234)
	for _, id := range []string{"general/1", "general/2", "random/3"} {
		report.Posts().Skip(id, "channelless.guest", testReasonQuiet)
	}
	report.Finish(nil)

	markdown := report.Markdown()

	t.Run("run metadata", func(t *testing.T) {
		assert.Contains(t, markdown, "# Slack Transform Report")
		assert.Contains(t, markdown, "| Provider | slack")
		assert.Contains(t, markdown, "| Started  | 2026-08-24T10:14:03Z")
		assert.Contains(t, markdown, "| Duration | 2m38s")
	})

	t.Run("summary links resolve to the detail sections", func(t *testing.T) {
		assert.Contains(t, markdown, "| [Users](#users)")
		assert.Contains(t, markdown, "\n### Users\n")

		// Public channels had nothing to report, so it is listed without a
		// link rather than pointing at a section that is not in the document.
		assert.Contains(t, markdown, "| Public channels ")
		assert.NotContains(t, markdown, "#public-channels")
		assert.NotContains(t, markdown, "\n### Public channels\n")
	})

	t.Run("notes are grouped by reason with a count and a footnote", func(t *testing.T) {
		// Footnote labels hyphenate the reason code so no renderer can read an
		// underscore pair inside the label as emphasis and break the link.
		assert.Contains(t, markdown, "#### 1 skipped: was skipped for a reason[^test-skip]")
		assert.Contains(t, markdown, "#### 1 note: was changed on the way in[^test-note]")
		assert.Contains(t, markdown, "#### 3 skipped: was skipped in bulk[^test-quiet]")
		assert.Contains(t, markdown, "- **U004** (`channelless.guest`)")
		assert.Contains(t, markdown, "- **U009** (`long.name`) — changed to `shortened`")
	})

	t.Run("a horizontal rule separates the footnotes from the details", func(t *testing.T) {
		assert.Equal(t, 1, strings.Count(markdown, "\n\n---\n"))
		assert.Less(t,
			strings.Index(markdown, "\n\n---\n"),
			strings.Index(markdown, "\n[^"),
			"the rule must sit above the first footnote")
	})

	t.Run("each reason gets exactly one footnote", func(t *testing.T) {
		assert.Equal(t, 1, strings.Count(markdown, "\n[^test-skip]: "))
		assert.Equal(t, 1, strings.Count(markdown, "\n[^test-note]: "))
		assert.Equal(t, 1, strings.Count(markdown, "\n[^test-quiet]: "))
		assert.Contains(t, markdown, "[^test-skip]: "+testReasonSkip.Detail)
	})

	t.Run("kinds with nothing to say are left out entirely", func(t *testing.T) {
		report.For(EntityUpload)
		assert.NotContains(t, report.Markdown(), "Uploads")
	})
}

func TestReportMarkdownGroupsOrderedByDescendingCount(t *testing.T) {
	report := NewReport(nil)
	report.Posts().Seen(10)
	report.Posts().Skip("p1", "", testReasonSkip)
	for _, id := range []string{"q1", "q2", "q3"} {
		report.Posts().Skip(id, "", testReasonQuiet)
	}
	report.Finish(nil)

	markdown := report.Markdown()
	assert.Less(t,
		strings.Index(markdown, "#### 3 skipped"),
		strings.Index(markdown, "#### 1 skipped"),
		"the largest group must lead the section")
}

func TestReportMarkdownEscaping(t *testing.T) {
	report := NewReport(nil)
	report.PublicChannels().Seen(1)
	report.PublicChannels().Note("a|b`c", "name `with` backticks | pipes [and] brackets", testReasonNote, "x")
	report.Finish(nil)

	markdown := report.Markdown()

	// The bold entity ID must not be able to break the list item.
	assert.Contains(t, markdown, `- **a\|b\`+"`"+`c**`)
	// A name containing backticks needs a longer fence than the run inside it.
	assert.Contains(t, markdown, "``name `with` backticks | pipes [and] brackets``")
}

func TestReportMarkdownEmpty(t *testing.T) {
	report := NewReport(nil)
	report.Finish(nil)

	markdown := report.Markdown()
	assert.Contains(t, markdown, "Nothing was found to transform.")
	assert.NotContains(t, markdown, "## Details")
	assert.NotContains(t, markdown, "## Stopped because of an error")
	// No footnotes means no rule to separate them from. Match the rule on its
	// own line, since a table's separator row also contains dashes.
	assert.NotContains(t, markdown, "\n\n---\n")
}

func TestReportJSON(t *testing.T) {
	report := NewReport(nil)
	report.Users().Seen(3)
	report.Users().Skip("u1", "ana", testReasonSkip)
	report.Users().Note("u2", "bob", testReasonNote, "bobby")
	report.Finish(nil)

	encoded, err := report.JSON()
	require.NoError(t, err)

	var decoded struct {
		Entities map[string]struct {
			Transformed int `json:"transformed"`
			Skipped     int `json:"skipped"`
			Notes       []struct {
				EntityID   string   `json:"entity_id"`
				EntityName string   `json:"entity_name"`
				ReasonCode string   `json:"reason_code"`
				Args       []string `json:"args"`
			} `json:"notes"`
		} `json:"entities"`
		Reasons map[string]Reason `json:"reasons"`
	}
	require.NoError(t, json.Unmarshal(encoded, &decoded))

	users := decoded.Entities["user"]
	assert.Equal(t, 2, users.Transformed)
	assert.Equal(t, 1, users.Skipped)
	require.Len(t, users.Notes, 2)

	// Sorted by (reason code, entity id), the same order the Markdown uses.
	assert.Equal(t, "test_note", users.Notes[0].ReasonCode)
	assert.Equal(t, "u2", users.Notes[0].EntityID)
	assert.Equal(t, []string{"bobby"}, users.Notes[0].Args)
	assert.Equal(t, "test_skip", users.Notes[1].ReasonCode)

	// The prose lives once in the dictionary, not once per note.
	require.Contains(t, decoded.Reasons, "test_skip")
	assert.Equal(t, testReasonSkip.Detail, decoded.Reasons["test_skip"].Detail)
	assert.Len(t, decoded.Reasons, 2, "only the reasons this run used are emitted")
}

func TestReportWrite(t *testing.T) {
	report := NewReport(nil)
	report.Metadata.Additional.Source.Platform = "slack"
	report.Users().Seen(1)
	report.Finish(nil)

	// A nested directory that does not exist yet: --output may point anywhere.
	dir := filepath.Join(t.TempDir(), "out")
	markdownPath, jsonPath, err := report.Write(dir)
	require.NoError(t, err)

	assert.Equal(t, filepath.Join(dir, ReportMarkdownFilename), markdownPath)
	assert.Equal(t, filepath.Join(dir, ReportJSONFilename), jsonPath)

	markdown, err := os.ReadFile(markdownPath)
	require.NoError(t, err)
	assert.Contains(t, string(markdown), "# Slack Transform Report")

	encoded, err := os.ReadFile(jsonPath)
	require.NoError(t, err)
	assert.True(t, json.Valid(encoded))
}

func TestRegisterReasonRejectsDuplicates(t *testing.T) {
	assert.PanicsWithValue(t,
		`intermediate: duplicate reason code "test_skip" (already registered with short text "was skipped for a reason")`,
		func() { RegisterReason(&Reason{Code: "test_skip", Short: "something else"}) })

	assert.Panics(t, func() { RegisterReason(&Reason{Short: "no code"}) })
	assert.Panics(t, func() { RegisterReason(nil) })
}

// TestRegisteredReasonsAreComplete guards the invariant the registry exists for:
// a note can only ever point at a reason that carries prose for the report.
func TestRegisteredReasonsAreComplete(t *testing.T) {
	for _, code := range RegisteredReasonCodes() {
		reason := LookupReason(code)
		require.NotNil(t, reason, code)
		assert.NotEmpty(t, reason.Short, "%s has no short text for its group heading", code)
		assert.NotEmpty(t, reason.Detail, "%s has no detail for its footnote", code)
		assert.Equal(t, code, reason.Code)
	}
}

// TestSplitReportsChunkCounts pins the return values the transform report's
// post_split / post_replies_split notes are built from. Both splitters used to
// return nothing, which is why oversized replies went unreported.
func TestSplitReportsChunkCounts(t *testing.T) {
	oversized := strings.Repeat("a", model.PostMessageMaxRunesV2*2+10)

	t.Run("a post that fits is left alone", func(t *testing.T) {
		post := &IntermediatePost{Message: "short"}
		assert.Equal(t, 1, SplitPostIntoThread(post))
		assert.Empty(t, post.Replies)
	})

	t.Run("an oversized post reports how many posts it became", func(t *testing.T) {
		post := &IntermediatePost{Message: oversized}
		chunks := SplitPostIntoThread(post)
		assert.Greater(t, chunks, 1)
		assert.Len(t, post.Replies, chunks-1, "the root plus its continuations")
	})

	t.Run("replies that fit report no splits", func(t *testing.T) {
		post := &IntermediatePost{Replies: []*IntermediatePost{
			{Message: "one", CreateAt: 1},
			{Message: "two", CreateAt: 2},
		}}
		assert.Equal(t, 0, SplitOversizedReplies(post))
		assert.Len(t, post.Replies, 2)
	})

	t.Run("oversized replies report how many were split", func(t *testing.T) {
		post := &IntermediatePost{Replies: []*IntermediatePost{
			{Message: "fits", CreateAt: 1},
			{Message: oversized, CreateAt: 2},
			{Message: oversized, CreateAt: 3},
		}}
		assert.Equal(t, 2, SplitOversizedReplies(post))
		assert.Greater(t, len(post.Replies), 3, "each split reply gains siblings")
	})
}
