package intermediate

import (
	"fmt"
	"strings"
)

// RenderMarkdown renders the entity count table and the accompanying Notes
// section as a self-contained markdown document. counts is expected to be in
// display order (as returned by BuildCounts/ApplySkipped); notes is expected
// to already be sorted (as returned by WarningCollector.Notes).
func RenderMarkdown(title string, counts []EntityCount, notes []NoteEntry) string {
	var b strings.Builder

	fmt.Fprintf(&b, "# %s\n\n", title)
	b.WriteString("| Entity | Transformed | Skipped | Failed |\n")
	b.WriteString("|---|---|---|---|\n")
	for _, c := range counts {
		fmt.Fprintf(&b, "| %s | %d | %d | %d |\n", c.Name, c.Transformed, c.Skipped, c.Failed)
	}

	b.WriteString("\n## Notes\n\n")
	if len(notes) == 0 {
		b.WriteString("No issues encountered.\n")
		return b.String()
	}

	for _, n := range notes {
		switch {
		case len(n.Examples) == 1:
			// Single distinct message (the common case: an unparameterized
			// Warn, or every occurrence happened to format identically) — one
			// line, with the total count folded in rather than repeated. The
			// source location (n.Location) is used to group/dedup notes but
			// isn't shown — it's meaningless to someone reading the summary.
			fmt.Fprintf(&b, "- %s", n.Examples[0])
			if n.Count > 1 {
				fmt.Fprintf(&b, " (×%d)", n.Count)
			}
			b.WriteString("\n")
		default:
			// Multiple distinct messages grouped from the same call site (e.g.
			// a Warnf with different interpolated identifiers) — show each
			// example once, without individually misattributing the total
			// count to any single one of them.
			fmt.Fprintf(&b, "- %d occurrence(s):\n", n.Count)
			for _, example := range n.Examples {
				fmt.Fprintf(&b, "  - %s\n", example)
			}
		}
		if len(n.Examples) == maxNoteExamples && n.Count > len(n.Examples) {
			b.WriteString("  - ... additional occurrences not shown above\n")
		}
	}

	return b.String()
}
