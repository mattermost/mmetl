// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package confluence

import (
	"archive/zip"
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/mattermost/mattermost/server/public/model"
)

// Docs storage caps the emitted bundle must respect. Body and SearchText are
// each capped at 2 MiB; page Props at 64 KiB.
const (
	PageBodyMaxBytes       = 2 * 1024 * 1024
	PageSearchTextMaxBytes = 2 * 1024 * 1024
	PagePropsMaxBytes      = 64 * 1024
)

// ValidationResult holds the results of pre-flight validation.
type ValidationResult struct {
	Valid    bool
	Errors   []string
	Warnings []string
}

// Validator handles pre-flight validation for Confluence migrations.
type Validator struct {
	// MattermostURL is the Mattermost server URL (optional for server validation)
	MattermostURL string
	// MattermostToken is the authentication token (optional for server validation)
	MattermostToken string
	// TeamName is the target team name
	TeamName string
	// ChannelName is the target channel name (deprecated in v2; the backing
	// channel is resolved at import time). Only used for optional server checks.
	ChannelName string
	// RequireUserMapping fails validation when any author is unmapped.
	RequireUserMapping bool
	// FailOnRestricted fails validation when any page carries a View restriction.
	FailOnRestricted bool
}

// NewValidator creates a new validator.
func NewValidator(teamName, channelName string) *Validator {
	return &Validator{
		TeamName:    teamName,
		ChannelName: channelName,
	}
}

// SetServerConfig sets the Mattermost server configuration for server-side validation.
func (v *Validator) SetServerConfig(url, token string) {
	v.MattermostURL = url
	v.MattermostToken = token
}

// ValidateExportFormat validates that the Confluence export file has the expected
// Confluence Cloud CSV format.
func (v *Validator) ValidateExportFormat(zipReader *zip.Reader) *ValidationResult {
	result := &ValidationResult{Valid: true}

	hasAttachments := false

	fileIndex := make(map[string]*zip.File, len(zipReader.File))
	for _, file := range zipReader.File {
		fileIndex[file.Name] = file
		if strings.HasPrefix(file.Name, "attachments/") {
			hasAttachments = true
		}
	}

	if !isCSVExport(fileIndex) {
		result.Valid = false
		result.Errors = append(result.Errors, "invalid export: expected a Confluence Cloud CSV export (content.csv or exportDescriptor.properties with exportFormat=csv)")
	}

	if !hasAttachments {
		result.Warnings = append(result.Warnings, "export contains no attachments directory")
	}

	return result
}

// ValidateExportContent validates the content of the parsed export.
func (v *Validator) ValidateExportContent(export *ConfluenceExport) *ValidationResult {
	result := &ValidationResult{Valid: true}

	if len(export.Pages) == 0 {
		result.Valid = false
		result.Errors = append(result.Errors, "export contains no pages")
	}

	if export.SpaceKey == "" {
		result.Warnings = append(result.Warnings, "export has no space key")
	}

	// Check for pages with empty content
	emptyContentCount := 0
	for _, page := range export.Pages {
		if page.Content == "" {
			emptyContentCount++
		}
	}
	if emptyContentCount > 0 {
		result.Warnings = append(result.Warnings, fmt.Sprintf("%d pages have empty content", emptyContentCount))
	}

	// Check content sizes against the Docs Body cap (2 MiB). This is a pre-flight
	// heads-up on the raw storage-format body; the authoritative check runs on the
	// emitted TipTap body at export time.
	oversizedPages := []string{}
	for _, page := range export.Pages {
		if len(page.Content) > PageBodyMaxBytes {
			oversizedPages = append(oversizedPages, page.Title)
		}
	}
	if len(oversizedPages) > 0 {
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("%d pages exceed the Docs body limit (2 MiB): %s",
				len(oversizedPages), strings.Join(oversizedPages[:min(3, len(oversizedPages))], ", ")))
	}

	// Surface view-restricted pages. Restrictions are detected, not resolved into
	// an ACL, so importing them widens access unless the operator intervenes.
	if len(export.RestrictedPageIDs) > 0 {
		titles := restrictedPageTitles(export)
		msg := fmt.Sprintf("%d pages have a View restriction that will NOT be preserved on import: %s",
			len(export.RestrictedPageIDs), strings.Join(titles[:min(5, len(titles))], ", "))
		if v.FailOnRestricted {
			result.Valid = false
			result.Errors = append(result.Errors, msg)
		} else {
			result.Warnings = append(result.Warnings, msg)
		}
	}

	return result
}

// restrictedPageTitles resolves restricted content IDs to page titles for
// human-readable reporting, falling back to the bare ID when unknown.
func restrictedPageTitles(export *ConfluenceExport) []string {
	titleByID := make(map[string]string, len(export.Pages))
	for _, p := range export.Pages {
		titleByID[p.ID] = p.Title
	}
	titles := make([]string, 0, len(export.RestrictedPageIDs))
	for id := range export.RestrictedPageIDs {
		if title := titleByID[id]; title != "" {
			titles = append(titles, title)
		} else {
			titles = append(titles, id)
		}
	}
	sort.Strings(titles)
	return titles
}

// ValidateUserMapping validates user mappings and returns warnings for unmapped users.
func (v *Validator) ValidateUserMapping(export *ConfluenceExport, userMapper *UserMapper) *ValidationResult {
	result := &ValidationResult{Valid: true}

	if userMapper == nil {
		if v.RequireUserMapping {
			result.Valid = false
			result.Errors = append(result.Errors, "no user mapping provided but --require-user-mapping is set")
		} else {
			result.Warnings = append(result.Warnings, "no user mapping provided - users will be auto-generated")
		}
		return result
	}

	// Collect all unique user account IDs from pages and comments
	userIDs := make(map[string]bool)
	for _, page := range export.Pages {
		if page.CreatedBy != "" {
			userIDs[page.CreatedBy] = true
		}
	}
	for _, comment := range export.Comments {
		if comment.CreatedBy != "" {
			userIDs[comment.CreatedBy] = true
		}
	}

	// Check which users are unmapped
	unmappedUsers := []string{}
	for userID := range userIDs {
		if _, err := userMapper.GetUsername(userID); err != nil {
			unmappedUsers = append(unmappedUsers, userID)
		}
	}

	if len(unmappedUsers) > 0 {
		msg := fmt.Sprintf("%d Confluence users not in mapping file (will use fallback)", len(unmappedUsers))
		if v.RequireUserMapping {
			result.Valid = false
			result.Errors = append(result.Errors, strings.Replace(msg, "will use fallback", "--require-user-mapping is set", 1))
		} else {
			result.Warnings = append(result.Warnings, msg)
		}
	}

	return result
}

// ValidateServer validates the target Mattermost server configuration.
// This requires MattermostURL and MattermostToken to be set.
func (v *Validator) ValidateServer(ctx context.Context) *ValidationResult {
	result := &ValidationResult{Valid: true}

	if v.MattermostURL == "" || v.MattermostToken == "" {
		result.Warnings = append(result.Warnings, "server validation skipped - no URL/token provided")
		return result
	}

	// Create Mattermost client
	client := model.NewAPIv4Client(v.MattermostURL)
	client.SetToken(v.MattermostToken)

	// Validate team exists
	team, resp, err := client.GetTeamByName(ctx, v.TeamName, "")
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusNotFound {
			result.Valid = false
			result.Errors = append(result.Errors, fmt.Sprintf("team %q not found", v.TeamName))
		} else {
			result.Valid = false
			result.Errors = append(result.Errors, fmt.Sprintf("failed to check team: %v", err))
		}
		return result
	}

	// The backing channel is resolved at import time in v2, so a channel is only
	// checked when one was explicitly supplied.
	if v.ChannelName == "" {
		return result
	}

	// Validate channel exists
	channel, resp, err := client.GetChannelByName(ctx, v.ChannelName, team.Id, "")
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusNotFound {
			result.Valid = false
			result.Errors = append(result.Errors, fmt.Sprintf("channel %q not found in team %q", v.ChannelName, v.TeamName))
		} else {
			result.Valid = false
			result.Errors = append(result.Errors, fmt.Sprintf("failed to check channel: %v", err))
		}
		return result
	}

	// Validate permissions (check if we can get channel membership)
	_, resp, err = client.GetChannelMember(ctx, channel.Id, "me", "")
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusForbidden {
			result.Warnings = append(result.Warnings, fmt.Sprintf("may not have access to channel %q", v.ChannelName))
		}
	}

	return result
}

// ValidateAll runs all validation checks and combines results.
func (v *Validator) ValidateAll(ctx context.Context, zipReader *zip.Reader, export *ConfluenceExport, userMapper *UserMapper) *ValidationResult {
	combined := &ValidationResult{Valid: true}

	// Export format validation
	formatResult := v.ValidateExportFormat(zipReader)
	combined.Errors = append(combined.Errors, formatResult.Errors...)
	combined.Warnings = append(combined.Warnings, formatResult.Warnings...)
	if !formatResult.Valid {
		combined.Valid = false
	}

	// Export content validation
	if export != nil {
		contentResult := v.ValidateExportContent(export)
		combined.Errors = append(combined.Errors, contentResult.Errors...)
		combined.Warnings = append(combined.Warnings, contentResult.Warnings...)
		if !contentResult.Valid {
			combined.Valid = false
		}

		// User mapping validation
		userResult := v.ValidateUserMapping(export, userMapper)
		combined.Errors = append(combined.Errors, userResult.Errors...)
		combined.Warnings = append(combined.Warnings, userResult.Warnings...)
		if !userResult.Valid {
			combined.Valid = false
		}
	}

	// Server validation (optional)
	if v.MattermostURL != "" && v.MattermostToken != "" {
		serverResult := v.ValidateServer(ctx)
		combined.Errors = append(combined.Errors, serverResult.Errors...)
		combined.Warnings = append(combined.Warnings, serverResult.Warnings...)
		if !serverResult.Valid {
			combined.Valid = false
		}
	}

	return combined
}

// min returns the minimum of two integers.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
