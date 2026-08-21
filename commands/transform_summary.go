package commands

import (
	"os"

	"github.com/mattermost/mmetl/services/intermediate"
)

// writeTransformSummary computes the entity counts for inter, merges in the
// Skipped tallies collector observed during the run, renders the result as
// markdown, and writes it to outputPath. Shared by every transform_<provider>
// command so the summary format stays identical across sources.
func writeTransformSummary(title, outputPath string, inter *intermediate.Intermediate, collector *intermediate.WarningCollector) error {
	counts := intermediate.BuildCounts(inter)
	counts = intermediate.ApplySkipped(counts, collector.Skipped())
	markdown := intermediate.RenderMarkdown(title, counts, collector.Notes())

	return os.WriteFile(outputPath, []byte(markdown), 0644)
}
