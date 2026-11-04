package commands_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mattermost/mmetl/services/intermediate"
)

// transformReport is the JSON transform report, decoded far enough for a test
// to assert on the outcome of every source entity without parsing Markdown.
type transformReport struct {
	Metadata struct {
		Provider string `json:"provider"`
		Team     string `json:"team"`
		Output   string `json:"output"`
	} `json:"metadata"`
	Error    string `json:"error,omitempty"`
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
	Reasons map[string]struct {
		Code   string `json:"code"`
		Short  string `json:"short"`
		Detail string `json:"detail"`
		Skip   bool   `json:"skip"`
	} `json:"reasons"`
}

// skippedNames returns the names of every entity of a kind skipped for a given
// reason, so a test can assert on exactly who was dropped and why.
func (r transformReport) skippedNames(kind, reasonCode string) []string {
	names := []string{}
	for _, note := range r.Entities[kind].Notes {
		if note.ReasonCode == reasonCode {
			names = append(names, note.EntityName)
		}
	}
	return names
}

// readTransformReport loads the JSON report written next to the bulk import
// file, and checks the Markdown report was written beside it under the given
// title.
func readTransformReport(t *testing.T, outputPath, title string) transformReport {
	t.Helper()

	dir := filepath.Dir(outputPath)
	raw, err := os.ReadFile(filepath.Join(dir, intermediate.ReportJSONFilename))
	require.NoError(t, err, "the transform must write a JSON report next to the bulk import file")

	var report transformReport
	require.NoError(t, json.Unmarshal(raw, &report))

	markdown, err := os.ReadFile(filepath.Join(dir, intermediate.ReportMarkdownFilename))
	require.NoError(t, err, "the transform must write a Markdown report next to the bulk import file")
	require.Contains(t, string(markdown), title)

	return report
}
