// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package confluence

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/pkg/errors"
)

// JSONL output types for the Mattermost Docs (Spaces/Pages) import contract.
//
// This is the v2 contract, named in Docs terms (space/page/page_comment). There
// is no shipped consumer of an earlier version, so no compatibility shim exists.
// Entity source IDs (page IDs, space keys) are bare and interpreted within the
// bundle's source namespace (see SourceImportData); importers must scope all
// source-ID lookups to the job, never globally.

// LineImportData represents a single line in the JSONL import file.
type LineImportData struct {
	Type                     string                              `json:"type"`
	Version                  *int                                `json:"version,omitempty"`
	Source                   *SourceImportData                   `json:"source,omitempty"`
	Space                    *SpaceImportData                    `json:"space,omitempty"`
	Page                     *PageImportData                     `json:"page,omitempty"`
	PageComment              *PageCommentImportData              `json:"page_comment,omitempty"`
	ResolveSpacePlaceholders *ResolveSpacePlaceholdersImportData `json:"resolve_space_placeholders,omitempty"`
}

// SourceImportData carries the bundle-level source namespace so numeric page IDs
// and space keys cannot collide across Confluence instances. It rides on the
// version line and is carried once per bundle (not per entity line).
type SourceImportData struct {
	OrganizationId *string `json:"organization_id,omitempty"`
	SpaceKey       *string `json:"space_key,omitempty"`
}

// ResolveSpacePlaceholdersImportData triggers post-import placeholder resolution,
// scoped to the space's pages.
type ResolveSpacePlaceholdersImportData struct {
	Team                *string `json:"team"`
	SpaceImportSourceId *string `json:"space_import_source_id"`
}

// SpaceImportData represents a Space to be imported. The backing channel is
// resolved at import time from the import request, so it is not part of this
// contract.
type SpaceImportData struct {
	Team        *string                `json:"team"`
	Title       *string                `json:"title,omitempty"`
	Description *string                `json:"description,omitempty"`
	Props       *model.StringInterface `json:"props,omitempty"`
}

// PageImportData represents a page to be imported.
type PageImportData struct {
	Team                 *string                 `json:"team"`
	SpaceImportSourceId  *string                 `json:"space_import_source_id"`
	User                 *string                 `json:"user"`
	Title                *string                 `json:"title"`
	Content              *string                 `json:"content"`
	ParentImportSourceId *string                 `json:"parent_import_source_id,omitempty"`
	CreateAt             *int64                  `json:"create_at,omitempty"`
	UpdateAt             *int64                  `json:"update_at,omitempty"`
	Props                *model.StringInterface  `json:"props,omitempty"`
	Attachments          *[]AttachmentImportData `json:"attachments,omitempty"`
}

// PageCommentImportData represents a comment on a page to be imported.
type PageCommentImportData struct {
	PageImportSourceId          *string                `json:"page_import_source_id"`
	ParentCommentImportSourceId *string                `json:"parent_comment_import_source_id,omitempty"`
	User                        *string                `json:"user"`
	Content                     *string                `json:"content"`
	CreateAt                    *int64                 `json:"create_at,omitempty"`
	UpdateAt                    *int64                 `json:"update_at,omitempty"`
	IsResolved                  *bool                  `json:"is_resolved,omitempty"`
	Props                       *model.StringInterface `json:"props,omitempty"`
}

// AttachmentImportData represents an attachment to be imported.
type AttachmentImportData struct {
	Path  *string                `json:"path"`
	Props *model.StringInterface `json:"props,omitempty"`
}

// ExportWriteLine writes a single JSONL line to the writer.
func ExportWriteLine(writer io.Writer, line *LineImportData) error {
	b, err := json.Marshal(line)
	if err != nil {
		return errors.Wrap(err, "failed to marshal JSON for export")
	}

	if _, err := writer.Write(append(b, '\n')); err != nil {
		return errors.Wrap(err, "failed to write export data")
	}

	return nil
}

// Export writes the transformed data to a JSONL file.
func (t *Transformer) Export(outputFilePath string) error {
	outputFile, err := os.Create(outputFilePath)
	if err != nil {
		return errors.Wrap(err, "failed to create output file")
	}
	defer outputFile.Close()

	// Write version line (carries the bundle-level source namespace)
	t.Logger.Info("Exporting version")
	if err := t.ExportVersion(outputFile); err != nil {
		return err
	}

	// Export the single space
	t.Logger.Info("Exporting space")
	if err := t.ExportSpace(outputFile); err != nil {
		return err
	}

	// Export pages
	t.Logger.Infof("Exporting %d pages", len(t.Intermediate.Pages))
	if err := t.ExportPages(outputFile); err != nil {
		return err
	}

	// Export comments
	t.Logger.Infof("Exporting %d comments", len(t.Intermediate.Comments))
	if err := t.ExportComments(outputFile); err != nil {
		return err
	}

	// Export resolve_space_placeholders directive to resolve cross-page links
	t.Logger.Info("Exporting placeholder resolution directive")
	if err := t.ExportResolvePlaceholders(outputFile); err != nil {
		return err
	}

	return nil
}

// sourceImportData builds the bundle-level source namespace, or nil when neither
// an organization id nor a space key is available.
func (t *Transformer) sourceImportData() *SourceImportData {
	if t.Intermediate == nil {
		return nil
	}
	src := &SourceImportData{}
	if t.Intermediate.SourceOrganizationID != "" {
		src.OrganizationId = model.NewPointer(t.Intermediate.SourceOrganizationID)
	}
	if t.Intermediate.Space != nil && t.Intermediate.Space.SpaceKey != "" {
		src.SpaceKey = model.NewPointer(t.Intermediate.Space.SpaceKey)
	}
	if src.OrganizationId == nil && src.SpaceKey == nil {
		return nil
	}
	return src
}

// ExportVersion writes the version line, including the source namespace.
func (t *Transformer) ExportVersion(writer io.Writer) error {
	version := 2
	versionLine := &LineImportData{
		Type:    "version",
		Version: &version,
		Source:  t.sourceImportData(),
	}
	return ExportWriteLine(writer, versionLine)
}

// ExportSpace writes the single space line.
func (t *Transformer) ExportSpace(writer io.Writer) error {
	if t.Intermediate == nil || t.Intermediate.Space == nil {
		return nil
	}

	space := t.Intermediate.Space
	props := model.StringInterface{
		"import_source_id": space.SpaceKey,
	}
	spaceLine := &LineImportData{
		Type: "space",
		Space: &SpaceImportData{
			Team:        model.NewPointer(space.Team),
			Title:       model.NewPointer(space.Title),
			Description: model.NewPointer(space.Description),
			Props:       &props,
		},
	}
	return ExportWriteLine(writer, spaceLine)
}

// ExportPages writes all page lines.
func (t *Transformer) ExportPages(writer io.Writer) error {
	for _, page := range t.Intermediate.Pages {
		pageLine := t.GetImportLineFromPage(page)
		if err := ExportWriteLine(writer, pageLine); err != nil {
			return err
		}
	}
	return nil
}

// GetImportLineFromPage converts an intermediate page to JSONL import format.
func (t *Transformer) GetImportLineFromPage(page *IntermediatePage) *LineImportData {
	// Build attachments with import_source_id for placeholder resolution
	var attachments *[]AttachmentImportData
	if len(page.Attachments) > 0 {
		attData := make([]AttachmentImportData, len(page.Attachments))
		for i, att := range page.Attachments {
			attProps := model.StringInterface{
				"import_source_id": att.SourceID,
			}
			attData[i] = AttachmentImportData{
				Path:  model.NewPointer(att.Path),
				Props: &attProps,
			}
		}
		attachments = &attData
	}

	// Build props with import tracking
	props := page.Props
	if props == nil {
		props = model.StringInterface{}
	}
	props["import_source_id"] = page.ImportSourceID
	props["import_source"] = "confluence"

	// Space source id = SpaceKey (matches SpaceImportData.Props["import_source_id"]).
	// Fall back to the single Space's key when IntermediatePage.SpaceKey is unset.
	spaceSourceId := page.SpaceKey
	if spaceSourceId == "" && t.Intermediate != nil && t.Intermediate.Space != nil {
		spaceSourceId = t.Intermediate.Space.SpaceKey
	}

	// Enforce Docs size limits on the emitted body and props (warn, don't drop).
	t.checkPageLimits(page, props)

	return &LineImportData{
		Type: "page",
		Page: &PageImportData{
			Team:                 model.NewPointer(t.TeamName),
			SpaceImportSourceId:  model.NewPointer(spaceSourceId),
			User:                 model.NewPointer(page.User),
			Title:                model.NewPointer(page.Title),
			Content:              model.NewPointer(page.Content),
			ParentImportSourceId: optionalString(page.ParentImportSourceID),
			CreateAt:             model.NewPointer(page.CreateAt),
			UpdateAt:             optionalInt64(page.UpdateAt),
			Props:                &props,
			Attachments:          attachments,
		},
	}
}

// checkPageLimits warns (via stats + logger) when a page's emitted body or props
// exceed the Docs storage caps. The page is still emitted; the operator decides.
func (t *Transformer) checkPageLimits(page *IntermediatePage, props model.StringInterface) {
	if n := len(page.Content); n > PageBodyMaxBytes {
		msg := fmt.Sprintf("page %q body is %d bytes, exceeding the %d-byte Docs limit", page.Title, n, PageBodyMaxBytes)
		t.Logger.Warn(msg)
		t.Stats.Warnings = append(t.Stats.Warnings, msg)
	}
	if b, err := json.Marshal(props); err == nil && len(b) > PagePropsMaxBytes {
		msg := fmt.Sprintf("page %q props are %d bytes, exceeding the %d-byte Docs limit", page.Title, len(b), PagePropsMaxBytes)
		t.Logger.Warn(msg)
		t.Stats.Warnings = append(t.Stats.Warnings, msg)
	}
}

// ExportComments writes all comment lines in topologically sorted order.
// Parents are exported before their children to ensure proper thread resolution during import.
func (t *Transformer) ExportComments(writer io.Writer) error {
	// Topologically sort comments so parents come before children
	sortedComments := topologicalSortComments(t.Intermediate.Comments)

	for _, comment := range sortedComments {
		commentLine := t.GetImportLineFromComment(comment)
		if err := ExportWriteLine(writer, commentLine); err != nil {
			return err
		}
	}
	return nil
}

// topologicalSortComments sorts comments so that parent comments appear before their children.
// This ensures that when importing, parent comments exist before children reference them.
func topologicalSortComments(comments []*IntermediateComment) []*IntermediateComment {
	if len(comments) == 0 {
		return comments
	}

	// Build a map of comment ID -> comment for quick lookup
	commentByID := make(map[string]*IntermediateComment, len(comments))
	for _, c := range comments {
		commentByID[c.ImportSourceID] = c
	}

	// Build adjacency list: parent -> children
	children := make(map[string][]*IntermediateComment)
	var roots []*IntermediateComment

	for _, c := range comments {
		if c.ParentCommentImportSourceID == "" {
			// No parent - this is a root comment
			roots = append(roots, c)
		} else if _, parentExists := commentByID[c.ParentCommentImportSourceID]; parentExists {
			// Parent exists in our set - add as child
			children[c.ParentCommentImportSourceID] = append(children[c.ParentCommentImportSourceID], c)
		} else {
			// Parent doesn't exist (orphaned) - treat as root
			roots = append(roots, c)
		}
	}

	// BFS traversal to build sorted list (parents before children)
	result := make([]*IntermediateComment, 0, len(comments))
	queue := roots

	for len(queue) > 0 {
		// Dequeue
		current := queue[0]
		queue = queue[1:]

		result = append(result, current)

		// Enqueue children
		if kids, ok := children[current.ImportSourceID]; ok {
			queue = append(queue, kids...)
		}
	}

	return result
}

// ExportResolvePlaceholders writes the directive that resolves cross-page link
// placeholders after all pages are imported. Resolution is space-scoped.
func (t *Transformer) ExportResolvePlaceholders(writer io.Writer) error {
	if t.Intermediate == nil || t.Intermediate.Space == nil {
		return nil
	}
	sourceId := t.Intermediate.Space.SpaceKey
	line := &LineImportData{
		Type: "resolve_space_placeholders",
		ResolveSpacePlaceholders: &ResolveSpacePlaceholdersImportData{
			Team:                &t.TeamName,
			SpaceImportSourceId: &sourceId,
		},
	}
	return ExportWriteLine(writer, line)
}

// GetImportLineFromComment converts an intermediate comment to JSONL import format.
func (t *Transformer) GetImportLineFromComment(comment *IntermediateComment) *LineImportData {
	// Build props with import tracking
	props := comment.Props
	if props == nil {
		props = model.StringInterface{}
	}
	props["import_source_id"] = comment.ImportSourceID
	props["import_source"] = "confluence"

	// Add inline_anchor if this is an inline comment
	if comment.InlineAnchorID != "" || comment.InlineAnchorText != "" {
		inlineAnchor := map[string]string{}
		if comment.InlineAnchorID != "" {
			inlineAnchor["anchor_id"] = comment.InlineAnchorID
		}
		if comment.InlineAnchorText != "" {
			inlineAnchor["text"] = comment.InlineAnchorText
		}
		props["inline_anchor"] = inlineAnchor
	}

	// Only include is_resolved if true (omit for unresolved comments)
	var isResolved *bool
	if comment.IsResolved {
		isResolved = model.NewPointer(true)
	}

	return &LineImportData{
		Type: "page_comment",
		PageComment: &PageCommentImportData{
			PageImportSourceId:          model.NewPointer(comment.PageImportSourceID),
			ParentCommentImportSourceId: optionalString(comment.ParentCommentImportSourceID),
			User:                        model.NewPointer(comment.User),
			Content:                     model.NewPointer(comment.Content),
			CreateAt:                    model.NewPointer(comment.CreateAt),
			UpdateAt:                    optionalInt64(comment.UpdateAt),
			IsResolved:                  isResolved,
			Props:                       &props,
		},
	}
}

// optionalString returns a pointer to the string if non-empty, otherwise nil.
func optionalString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// optionalInt64 returns a pointer to the value if non-zero, otherwise nil, so
// unset timestamps are omitted rather than emitted as epoch zero.
func optionalInt64(v int64) *int64 {
	if v == 0 {
		return nil
	}
	return &v
}

// ExportWithManifest writes the transformed data to a JSONL file and generates a manifest.
// buildManifestUsers returns one ManifestUser per distinct source user (deduped
// by Atlassian account ID), pairing it with the Mattermost username it resolves
// to. Sorted by account ID for deterministic output.
func (t *Transformer) buildManifestUsers(export *ConfluenceExport) []ManifestUser {
	seen := make(map[string]bool)
	users := make([]ManifestUser, 0, len(export.Users))
	for _, u := range export.Users {
		if u.AccountID == "" || seen[u.AccountID] {
			continue
		}
		seen[u.AccountID] = true
		users = append(users, ManifestUser{
			AccountID:          u.AccountID,
			ConfluenceUsername: u.Username,
			MattermostUsername: t.resolveUsername(u.AccountID, export),
		})
	}
	sort.Slice(users, func(i, j int) bool {
		return users[i].AccountID < users[j].AccountID
	})
	return users
}

func (t *Transformer) ExportWithManifest(outputFilePath string, export *ConfluenceExport) error {
	// First export the JSONL
	if err := t.Export(outputFilePath); err != nil {
		return err
	}

	// Generate manifest
	manifest := NewManifest(export, t.TeamName, t.ExportFile)
	spaces := 0
	if t.Intermediate.Space != nil {
		spaces = 1
	}
	manifest.SetCounts(len(t.Intermediate.Pages), len(t.Intermediate.Comments), spaces, t.Stats)
	manifest.SetRestrictedPages(export)

	// Set user mapping count if available
	if t.UserMapper != nil {
		manifest.SetUserMappingCount(t.UserMapper.GetMappingCount())
	}

	// Record the source users and the Mattermost username each resolved to, so a
	// later step can audit or re-match users after import. export.Users is keyed
	// by both aaid and user_key (aliases to the same record), so dedupe by AccountID.
	manifest.Users = t.buildManifestUsers(export)

	// Copy warnings and errors from stats
	manifest.Warnings = append(manifest.Warnings, t.Stats.Warnings...)
	manifest.Errors = append(manifest.Errors, t.Stats.Errors...)

	// Compute checksums
	if err := manifest.ComputeJSONLChecksum(outputFilePath); err != nil {
		t.Logger.Warnf("Failed to compute JSONL checksum: %v", err)
	}

	if t.Config.AttachmentsDir != "" && !t.Config.SkipAttachments {
		if err := manifest.ComputeAttachmentsChecksum(t.Config.AttachmentsDir); err != nil {
			t.Logger.Warnf("Failed to compute attachments checksum: %v", err)
		}
	}

	// Write manifest
	manifestPath := outputFilePath[:len(outputFilePath)-len(".jsonl")] + "-manifest.json"
	if err := manifest.Write(manifestPath); err != nil {
		return errors.Wrap(err, "failed to write manifest")
	}

	t.Logger.Infof("Manifest written to %s", manifestPath)
	return nil
}
