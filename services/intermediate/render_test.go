package intermediate

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRenderMarkdown_NoNotes(t *testing.T) {
	counts := []EntityCount{
		{Key: EntityUser, Name: "Users", Transformed: 5, Skipped: 0, Failed: 0},
	}

	out := RenderMarkdown("Test Summary", counts, nil)

	assert.Contains(t, out, "# Test Summary")
	assert.Contains(t, out, "| Entity | Transformed | Skipped | Failed |")
	assert.Contains(t, out, "| Users | 5 | 0 | 0 |")
	assert.Contains(t, out, "## Notes")
	assert.Contains(t, out, "No issues encountered.")
}

func TestRenderMarkdown_SingleExampleFoldsCount(t *testing.T) {
	notes := []NoteEntry{
		{Location: "file.go:10", Examples: []string{"missing user field"}, Count: 14},
	}

	out := RenderMarkdown("Test", nil, notes)

	assert.Contains(t, out, "- missing user field (×14)")
	// The source location groups/dedups notes internally but isn't rendered.
	assert.NotContains(t, out, "file.go:10")
	// Must not repeat the line 14 times.
	assert.Equal(t, 1, countOccurrences(out, "missing user field"))
}

func TestRenderMarkdown_SingleOccurrenceOmitsCount(t *testing.T) {
	notes := []NoteEntry{
		{Location: "file.go:10", Examples: []string{"one-off warning"}, Count: 1},
	}

	out := RenderMarkdown("Test", nil, notes)

	assert.Contains(t, out, "- one-off warning\n")
	assert.NotContains(t, out, "×1)")
}

func TestRenderMarkdown_MultipleDistinctExamples(t *testing.T) {
	notes := []NoteEntry{
		{Location: "file.go:20", Examples: []string{"user alice missing email", "user bob missing email"}, Count: 2},
	}

	out := RenderMarkdown("Test", nil, notes)

	assert.Contains(t, out, "- 2 occurrence(s):")
	assert.Contains(t, out, "  - user alice missing email")
	assert.Contains(t, out, "  - user bob missing email")
}

func TestRenderMarkdown_TruncatedExamplesNoted(t *testing.T) {
	notes := []NoteEntry{
		{
			Location: "file.go:30",
			Examples: []string{"a", "b", "c", "d", "e"}, // == maxNoteExamples
			Count:    50,
		},
	}

	out := RenderMarkdown("Test", nil, notes)

	assert.Contains(t, out, "... additional occurrences not shown above")
}

func countOccurrences(haystack, needle string) int {
	count := 0
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			count++
		}
	}
	return count
}
