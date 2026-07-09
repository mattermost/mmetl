// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package confluence

import (
	"encoding/json"
	"io"
	"os"
	"sort"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/pkg/errors"
)

// JSONL output types for wiki/page import
// These match the types expected by the Mattermost wiki import functionality

// LineImportData represents a single line in the JSONL import file.
type LineImportData struct {
	Type                    string                             `json:"type"`
	Version                 *int                               `json:"version,omitempty"`
	Channel                 *ChannelImportData                 `json:"channel,omitempty"`
	Wiki                    *WikiImportData                    `json:"wiki,omitempty"`
	Page                    *PageImportData                    `json:"page,omitempty"`
	PageComment             *PageCommentImportData             `json:"page_comment,omitempty"`
	ResolveWikiPlaceholders *ResolveWikiPlaceholdersImportData `json:"resolve_wiki_placeholders,omitempty"`
}

// ResolveWikiPlaceholdersImportData triggers post-import placeholder resolution.
type ResolveWikiPlaceholdersImportData struct {
	Team               *string `json:"team"`
	WikiImportSourceId *string `json:"wiki_import_source_id"`
}

// ChannelImportData represents a channel to be created.
type ChannelImportData struct {
	Team        *string `json:"team"`
	Name        *string `json:"name"`
	DisplayName *string `json:"display_name"`
	Type        *string `json:"type"` // "O" for public, "P" for private
	Purpose     *string `json:"purpose,omitempty"`
	Header      *string `json:"header,omitempty"`
}

// WikiImportData represents a wiki to be imported.
type WikiImportData struct {
	Team        *string                `json:"team"`
	Channel     *string                `json:"channel"`
	Title       *string                `json:"title,omitempty"`
	Description *string                `json:"description,omitempty"`
	Props       *model.StringInterface `json:"props,omitempty"`
}

// PageImportData represents a page to be imported.
type PageImportData struct {
	Team                 *string                 `json:"team"`
	WikiImportSourceId   *string                 `json:"wiki_import_source_id"`
	User                 *string                 `json:"user"`
	Title                *string                 `json:"title"`
	Content              *string                 `json:"content"`
	ParentImportSourceId *string                 `json:"parent_import_source_id,omitempty"`
	CreateAt             *int64                  `json:"create_at,omitempty"`
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

	// Write version line
	t.Logger.Info("Exporting version")
	if err := t.ExportVersion(outputFile); err != nil {
		return err
	}

	// Export channel (if auto-creating)
	if t.Intermediate.Channel != nil {
		t.Logger.Info("Exporting channel")
		if err := t.ExportChannel(outputFile); err != nil {
			return err
		}
	}

	// Export wikis (one per space)
	t.Logger.Infof("Exporting %d wikis", len(t.Intermediate.Wikis))
	if err := t.ExportWikis(outputFile); err != nil {
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

	// Export resolve_wiki_placeholders directive to resolve cross-page links
	t.Logger.Info("Exporting placeholder resolution directive")
	if err := t.ExportResolvePlaceholders(outputFile); err != nil {
		return err
	}

	return nil
}

// ExportVersion writes the version line.
func (t *Transformer) ExportVersion(writer io.Writer) error {
	version := 1
	versionLine := &LineImportData{
		Type:    "version",
		Version: &version,
	}
	return ExportWriteLine(writer, versionLine)
}

// ExportChannel writes the channel line (if auto-creating).
func (t *Transformer) ExportChannel(writer io.Writer) error {
	if t.Intermediate.Channel == nil {
		return nil
	}

	ch := t.Intermediate.Channel
	channelLine := &LineImportData{
		Type: "channel",
		Channel: &ChannelImportData{
			Team:        model.NewPointer(ch.Team),
			Name:        model.NewPointer(ch.Name),
			DisplayName: model.NewPointer(ch.DisplayName),
			Type:        model.NewPointer(ch.Type),
			Purpose:     optionalString(ch.Purpose),
			Header:      optionalString(ch.Header),
		},
	}
	return ExportWriteLine(writer, channelLine)
}

// ExportWikis writes wiki lines for all spaces.
func (t *Transformer) ExportWikis(writer io.Writer) error {
	for _, wiki := range t.Intermediate.Wikis {
		props := model.StringInterface{
			"import_source_id": wiki.SpaceKey,
		}
		wikiLine := &LineImportData{
			Type: "wiki",
			Wiki: &WikiImportData{
				Team:        model.NewPointer(wiki.Team),
				Channel:     model.NewPointer(wiki.Channel),
				Title:       model.NewPointer(wiki.Title),
				Description: model.NewPointer(wiki.Description),
				Props:       &props,
			},
		}
		if err := ExportWriteLine(writer, wikiLine); err != nil {
			return err
		}
	}
	return nil
}

// ExportWiki writes the wiki line (legacy single-wiki support).
func (t *Transformer) ExportWiki(writer io.Writer) error {
	if t.Intermediate.Wiki == nil {
		return nil
	}

	props := model.StringInterface{
		"import_source_id": t.Intermediate.Wiki.SpaceKey,
	}
	wikiLine := &LineImportData{
		Type: "wiki",
		Wiki: &WikiImportData{
			Team:        model.NewPointer(t.Intermediate.Wiki.Team),
			Channel:     model.NewPointer(t.Intermediate.Wiki.Channel),
			Title:       model.NewPointer(t.Intermediate.Wiki.Title),
			Description: model.NewPointer(t.Intermediate.Wiki.Description),
			Props:       &props,
		},
	}
	return ExportWriteLine(writer, wikiLine)
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

	// Wiki source id = SpaceKey (matches WikiImportData.Props["import_source_id"]).
	// Fall back to the transformer-level SpaceKey for legacy single-space exports
	// where IntermediatePage.SpaceKey may be unset.
	wikiSourceId := page.SpaceKey
	if wikiSourceId == "" && t.Intermediate != nil && t.Intermediate.Wiki != nil {
		wikiSourceId = t.Intermediate.Wiki.SpaceKey
	}

	return &LineImportData{
		Type: "page",
		Page: &PageImportData{
			Team:                 model.NewPointer(t.TeamName),
			WikiImportSourceId:   model.NewPointer(wikiSourceId),
			User:                 model.NewPointer(page.User),
			Title:                model.NewPointer(page.Title),
			Content:              model.NewPointer(page.Content),
			ParentImportSourceId: optionalString(page.ParentImportSourceID),
			CreateAt:             model.NewPointer(page.CreateAt),
			Props:                &props,
			Attachments:          attachments,
		},
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

// ExportResolvePlaceholders writes one directive per wiki to resolve cross-page link
// placeholders after all pages are imported. Resolution is wiki-scoped (uses the
// wiki's backing channel for page lookup), so each wiki gets its own line.
func (t *Transformer) ExportResolvePlaceholders(writer io.Writer) error {
	emit := func(spaceKey string) error {
		sourceId := spaceKey
		line := &LineImportData{
			Type: "resolve_wiki_placeholders",
			ResolveWikiPlaceholders: &ResolveWikiPlaceholdersImportData{
				Team:               &t.TeamName,
				WikiImportSourceId: &sourceId,
			},
		}
		return ExportWriteLine(writer, line)
	}

	if t.Intermediate != nil {
		for _, w := range t.Intermediate.Wikis {
			if err := emit(w.SpaceKey); err != nil {
				return err
			}
		}
		if t.Intermediate.Wiki != nil && len(t.Intermediate.Wikis) == 0 {
			return emit(t.Intermediate.Wiki.SpaceKey)
		}
	}
	return nil
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
	manifest := NewManifest(export, t.TeamName, t.ChannelName, t.ExportFile)
	manifest.SetCounts(len(t.Intermediate.Pages), len(t.Intermediate.Comments), len(t.Intermediate.Wikis), t.Stats)

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
