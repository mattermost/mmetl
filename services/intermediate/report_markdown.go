package intermediate

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// providerTitles maps the machine provider name to how it is spelled in prose.
var providerTitles = map[string]string{
	"slack":      "Slack",
	"rocketchat": "RocketChat",
}

func providerTitle(provider string) string {
	if title, ok := providerTitles[provider]; ok {
		return title
	}
	if provider == "" {
		return "Transform Report"
	}
	return strings.ToUpper(provider[:1]) + provider[1:]
}

// reasonGroup is one `####` block: every note in an entity section that shares
// a reason, rendered under a single heading and a single footnote reference.
type reasonGroup struct {
	reason *Reason
	notes  []ReportEntityNote
}

// Markdown renders the human-facing report.
func (r *Report) Markdown() string {
	if r == nil {
		return ""
	}

	var b strings.Builder

	title := providerTitle(r.Metadata.Provider)
	if r.Metadata.Provider != "" {
		title += " Transform Report"
	}
	fmt.Fprintf(&b, "# %s\n", title)

	if r.Error != "" {
		b.WriteString("\n## Stopped because of an error\n\n")
		b.WriteString("The transform did not finish. The report below covers everything processed up to the\npoint of failure.\n\n")
		for _, line := range strings.Split(strings.TrimRight(r.Error, "\n"), "\n") {
			fmt.Fprintf(&b, "    %s\n", line)
		}
	}

	b.WriteString("\n## Run\n\n")
	b.WriteString(renderTable([]string{"Field", "Value"}, r.runRows()))

	// footnoteOrder collects reasons in the order they first appear in the
	// rendered document, which is deterministic; the order they were first
	// recorded in is not, because most source collections are Go maps.
	footnoteOrder := []*Reason{}
	seenReason := map[string]bool{}

	kinds := r.orderedKinds()
	summaryRows := [][]string{}
	detailSections := []string{}

	for _, kind := range kinds {
		entity := r.Entities[kind]
		if !entity.hasContent() {
			continue
		}

		title := EntityTitle(kind)
		groups := entity.reasonGroups()

		// Only a kind that has a detail section gets a link, so the summary
		// never points at an anchor that is not in the document.
		label := title
		if len(groups) > 0 {
			label = fmt.Sprintf("[%s](#%s)", title, anchor(title))
		}
		summaryRows = append(summaryRows, []string{
			label,
			fmt.Sprintf("%d", entity.Transformed),
			fmt.Sprintf("%d", entity.Skipped),
		})

		if len(groups) == 0 {
			continue
		}

		var section strings.Builder
		fmt.Fprintf(&section, "\n### %s\n", title)
		for _, group := range groups {
			if !seenReason[group.reason.Code] {
				seenReason[group.reason.Code] = true
				footnoteOrder = append(footnoteOrder, group.reason)
			}
			fmt.Fprintf(&section, "\n#### %s\n\n", groupHeading(group))
			for _, note := range group.notes {
				section.WriteString(renderNoteBullet(note, group.reason))
			}
		}
		detailSections = append(detailSections, section.String())
	}

	b.WriteString("\n## Summary\n\n")
	if len(summaryRows) == 0 {
		b.WriteString("Nothing was found to transform.\n")
	} else {
		b.WriteString(renderTable([]string{"Entity", "Transformed", "Skipped"}, summaryRows))
	}

	if len(detailSections) > 0 {
		b.WriteString("\n## Details\n")
		for _, section := range detailSections {
			b.WriteString(section)
		}
	}

	if len(footnoteOrder) > 0 {
		// The blank line this leaves is load-bearing: `---` directly under a
		// text line is a setext heading, not a thematic break.
		b.WriteString("\n---\n")
		for _, reason := range footnoteOrder {
			fmt.Fprintf(&b, "\n[^%s]: %s\n", footnoteLabel(reason.Code), singleLine(reason.Detail))
		}
	}

	return b.String()
}

// runRows builds the `## Run` table. Fields that were never filled in are left
// out rather than rendered blank.
func (r *Report) runRows() [][]string {
	rows := [][]string{}
	add := func(field, value string) {
		if value != "" {
			rows = append(rows, []string{field, value})
		}
	}

	add("Provider", r.Metadata.Provider)
	add("mmetl", r.Metadata.Version)
	add("Input", r.Metadata.Input)
	add("Team", r.Metadata.Team)
	add("Output", r.Metadata.Output)
	if !r.Metadata.Started.IsZero() {
		add("Started", r.Metadata.Started.UTC().Format(time.RFC3339))
	}
	if !r.Metadata.Finished.IsZero() {
		add("Finished", r.Metadata.Finished.UTC().Format(time.RFC3339))
	}
	if !r.Metadata.Started.IsZero() && !r.Metadata.Finished.IsZero() {
		add("Duration", r.Metadata.Duration().String())
	}
	if r.Metadata.Flags != "" {
		add("Flags", mdCode(r.Metadata.Flags))
	}

	return rows
}

// reasonGroups groups an entity's notes by reason, largest group first so the
// biggest problems lead each section, with the reason code breaking ties so
// equal-sized groups do not swap places between runs.
func (e *EntityReport) reasonGroups() []reasonGroup {
	byCode := map[string]*reasonGroup{}
	for _, note := range e.sortedNotes() {
		reason := note.Reason()
		if reason == nil {
			continue
		}
		group, ok := byCode[reason.Code]
		if !ok {
			group = &reasonGroup{reason: reason}
			byCode[reason.Code] = group
		}
		group.notes = append(group.notes, note)
	}

	groups := make([]reasonGroup, 0, len(byCode))
	for _, group := range byCode {
		groups = append(groups, *group)
	}
	sort.Slice(groups, func(i, j int) bool {
		if len(groups[i].notes) != len(groups[j].notes) {
			return len(groups[i].notes) > len(groups[j].notes)
		}
		return groups[i].reason.Code < groups[j].reason.Code
	})
	return groups
}

// groupHeading renders "{count} {skipped|note[s]}: {Short}[^{Code}]".
func groupHeading(group reasonGroup) string {
	count := len(group.notes)
	label := "note"
	if group.reason.Skip {
		label = "skipped"
	} else if count != 1 {
		label = "notes"
	}
	return fmt.Sprintf("%d %s: %s[^%s]", count, label, singleLine(group.reason.Short), footnoteLabel(group.reason.Code))
}

// footnoteLabel renders a reason code as a Markdown footnote label. Reason
// codes are snake_case, and a label such as `channel_no_created_ts` breaks in
// renderers that apply emphasis inside a link label: `_no_` becomes italics,
// the reference resolves to `channelnocreated_ts`, and it no longer matches its
// definition. Hyphens carry exactly the same information and cannot be read as
// emphasis, so the label stays a faithful, if not byte-identical, rendering of
// the code. The JSON report is where a consumer reads the real code.
func footnoteLabel(code string) string {
	return strings.ReplaceAll(code, "_", "-")
}

// renderNoteBullet renders one entity: its ID in bold, its name (when known) in
// a code span, and the reason's per-entity specifics after an em dash.
func renderNoteBullet(note ReportEntityNote, reason *Reason) string {
	var b strings.Builder
	b.WriteString("- **")
	b.WriteString(mdEscape(note.EntityID))
	b.WriteString("**")
	if note.EntityName != "" {
		b.WriteString(" (")
		b.WriteString(mdCode(note.EntityName))
		b.WriteString(")")
	}
	if specifics := reason.specifics(note.Args, mdCode); specifics != "" {
		b.WriteString(" — ")
		b.WriteString(specifics)
	}
	b.WriteString("\n")
	return b.String()
}

// SummaryText renders the plain-text end-of-run summary printed to stdout, so an
// operator who never opens the report still sees the counts.
func (r *Report) SummaryText(markdownPath, jsonPath string) string {
	if r == nil {
		return ""
	}

	var b strings.Builder
	b.WriteString("Transform summary:\n")

	rows := [][]string{{"Entity", "Transformed", "Skipped"}}
	for _, kind := range r.orderedKinds() {
		entity := r.Entities[kind]
		if !entity.hasContent() {
			continue
		}
		rows = append(rows, []string{
			EntityTitle(kind),
			fmt.Sprintf("%d", entity.Transformed),
			fmt.Sprintf("%d", entity.Skipped),
		})
	}

	if len(rows) == 1 {
		b.WriteString("  nothing was found to transform\n")
	} else {
		widths := columnWidths(rows)
		for _, row := range rows {
			b.WriteString("  ")
			b.WriteString(padRight(row[0], widths[0]))
			fmt.Fprintf(&b, "  %s  %s\n", padLeft(row[1], widths[1]), padLeft(row[2], widths[2]))
		}
	}

	if r.Error != "" {
		fmt.Fprintf(&b, "\nThe transform stopped because of an error: %s\n", singleLine(r.Error))
	}
	if markdownPath != "" {
		fmt.Fprintf(&b, "\nReport: %s\n", markdownPath)
	}
	if jsonPath != "" {
		fmt.Fprintf(&b, "        %s\n", jsonPath)
	}

	return b.String()
}

// anchor returns the GitHub-flavored Markdown anchor for a heading: lowercased,
// spaces turned into hyphens, everything else dropped.
func anchor(title string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(title) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		case r == ' ':
			b.WriteRune('-')
		}
	}
	return b.String()
}

// singleLine collapses newlines and tabs so a value can never break out of the
// table row or list item it is rendered in.
func singleLine(s string) string {
	replacer := strings.NewReplacer("\r\n", " ", "\r", " ", "\n", " ", "\t", " ")
	return strings.TrimSpace(replacer.Replace(s))
}

// mdEscape escapes the Markdown metacharacters that a channel or user name from
// a source export can plausibly contain.
func mdEscape(s string) string {
	s = singleLine(s)
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case '\\', '`', '*', '_', '[', ']', '(', ')', '#', '+', '-', '!', '|', '<', '>', '~':
			b.WriteRune('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// mdCode wraps a value in a code span, choosing a backtick fence longer than
// the longest backtick run inside it so the span cannot be terminated early.
func mdCode(s string) string {
	s = singleLine(s)
	if s == "" {
		return "``"
	}

	longest, current := 0, 0
	for _, r := range s {
		if r == '`' {
			current++
			longest = max(longest, current)
			continue
		}
		current = 0
	}

	fence := strings.Repeat("`", longest+1)
	// A code span whose content starts or ends with a backtick needs one space
	// of padding, which CommonMark strips when rendering.
	if strings.HasPrefix(s, "`") || strings.HasSuffix(s, "`") {
		return fence + " " + s + " " + fence
	}
	return fence + s + fence
}

// renderTable renders a padded GitHub-flavored Markdown table.
func renderTable(header []string, rows [][]string) string {
	escaped := make([][]string, 0, len(rows)+1)
	escaped = append(escaped, header)
	for _, row := range rows {
		cells := make([]string, len(row))
		for i, cell := range row {
			cells[i] = strings.ReplaceAll(singleLine(cell), "|", "\\|")
		}
		escaped = append(escaped, cells)
	}

	widths := columnWidths(escaped)

	var b strings.Builder
	writeRow := func(cells []string) {
		b.WriteString("|")
		for i, cell := range cells {
			fmt.Fprintf(&b, " %s |", padRight(cell, widths[i]))
		}
		b.WriteString("\n")
	}

	writeRow(escaped[0])
	separators := make([]string, len(widths))
	for i, width := range widths {
		separators[i] = strings.Repeat("-", width)
	}
	writeRow(separators)
	for _, row := range escaped[1:] {
		writeRow(row)
	}

	return b.String()
}

func columnWidths(rows [][]string) []int {
	widths := []int{}
	for _, row := range rows {
		for i, cell := range row {
			for len(widths) <= i {
				widths = append(widths, 0)
			}
			widths[i] = max(widths[i], len([]rune(cell)))
		}
	}
	return widths
}

func padRight(s string, width int) string {
	if pad := width - len([]rune(s)); pad > 0 {
		return s + strings.Repeat(" ", pad)
	}
	return s
}

func padLeft(s string, width int) string {
	if pad := width - len([]rune(s)); pad > 0 {
		return strings.Repeat(" ", pad) + s
	}
	return s
}
