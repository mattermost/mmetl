// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package confluence

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/mattermost/mattermost/server/public/model"
)

// Intermediate holds the transformed data ready for export.
type Intermediate struct {
	// Single Space — one bundle covers exactly one Confluence space.
	Space *IntermediateSpace

	// SourceOrganizationID namespaces source IDs across Confluence instances.
	SourceOrganizationID string

	Pages    []*IntermediatePage
	Comments []*IntermediateComment
}

// IntermediateSpace represents the Space to be created. The backing channel is
// resolved at import time from the import request, so it is not modeled here.
type IntermediateSpace struct {
	Team        string
	Title       string // Space title (= Confluence space name)
	Description string
	SpaceKey    string // Original Confluence space key for tracking
}

// IntermediatePage represents a page ready for JSONL export.
type IntermediatePage struct {
	// Identifiers
	ImportSourceID string // Confluence page ID for idempotency
	Title          string

	// Space tracking
	SpaceKey string // Original Confluence space key

	// Hierarchy
	ParentImportSourceID string // Parent's Confluence page ID

	// Content
	Content string // TipTap JSON content

	// Metadata
	User     string // Mattermost username
	CreateAt int64  // Unix timestamp in milliseconds
	UpdateAt int64

	// Props for import tracking
	Props model.StringInterface

	// Attachments (path + source ID for placeholder resolution)
	Attachments []IntermediateAttachment
}

// IntermediateAttachment holds attachment path and source ID.
type IntermediateAttachment struct {
	Path     string // File path relative to attachments dir
	SourceID string // Confluence attachment ID for placeholder resolution
}

// IntermediateComment represents a page comment ready for JSONL export.
type IntermediateComment struct {
	// Identifiers
	ImportSourceID     string // Confluence comment ID
	PageImportSourceID string // Parent page's Confluence ID

	// Threading
	ParentCommentImportSourceID string // For threaded comments

	// Content
	Content string // Markdown text

	// Inline anchor for inline comments
	InlineAnchorID   string // The UUID that links this comment to the TipTap mark
	InlineAnchorText string // The text that was highlighted when creating the inline comment

	// Status
	IsResolved bool // True if comment was resolved in Confluence

	// Metadata
	User     string
	CreateAt int64
	UpdateAt int64

	// Props
	Props model.StringInterface
}

// TransformPages transforms Confluence pages to intermediate format.
func (t *Transformer) TransformPages(export *ConfluenceExport) error {
	t.Logger.Info("Transforming pages")

	// Filter out historical page versions (old edits), deleted, draft, and archived pages
	var currentPages []*ConfluencePage
	historicalSkipped := 0
	deletedSkipped := 0
	draftSkipped := 0
	archivedSkipped := 0
	for _, page := range export.Pages {
		if export.HistoricalPageIDs[page.ID] {
			historicalSkipped++
			continue
		}
		if page.ContentStatus == "deleted" {
			deletedSkipped++
			continue
		}
		if page.ContentStatus == "draft" {
			draftSkipped++
			continue
		}
		if page.ContentStatus == "archived" {
			archivedSkipped++
			continue
		}
		currentPages = append(currentPages, page)
	}
	if historicalSkipped > 0 || deletedSkipped > 0 || draftSkipped > 0 || archivedSkipped > 0 {
		t.Logger.Infof("Skipped %d historical, %d deleted, %d draft, %d archived pages", historicalSkipped, deletedSkipped, draftSkipped, archivedSkipped)
	}

	// Build page tree and get topologically sorted pages
	sortedPages, err := t.buildPageHierarchy(currentPages)
	if err != nil {
		return err
	}

	// Set up the single Space (one bundle = one Confluence space).
	if err := t.setupSpace(export); err != nil {
		return err
	}

	// Build children lookup map (pageID -> list of child page info)
	// This is used to generate child page links for children/pagetree macros
	childrenByParent := make(map[string][]ChildPageInfo)
	for _, page := range currentPages {
		if page.ParentID != "" {
			childrenByParent[page.ParentID] = append(childrenByParent[page.ParentID], ChildPageInfo{
				ID:    page.ID,
				Title: page.Title,
			})
		}
	}

	// Transform each page
	for _, confPage := range sortedPages {
		// Get children for this page (if any)
		children := childrenByParent[confPage.ID]
		intermediatePage, err := t.transformPage(confPage, export, children)
		if err != nil {
			t.Logger.Warnf("Failed to transform page %s (%s): %v", confPage.ID, confPage.Title, err)
			continue
		}
		t.Intermediate.Pages = append(t.Intermediate.Pages, intermediatePage)
	}

	return nil
}

// setupSpace builds the single IntermediateSpace from the export. A Confluence
// CSV export always covers one space; more than one is rejected here so the
// single-space assumption is enforced rather than silently mis-handled.
func (t *Transformer) setupSpace(export *ConfluenceExport) error {
	if len(export.Spaces) > 1 {
		return fmt.Errorf("multi-space exports are not supported; import one space per bundle (found %d spaces)", len(export.Spaces))
	}

	t.Intermediate.SourceOrganizationID = export.OrganizationID

	// Prefer the parsed space record; fall back to the descriptor-level space key.
	for _, space := range export.Spaces {
		t.Intermediate.Space = &IntermediateSpace{
			Team:        t.TeamName,
			Title:       space.Name,
			Description: "Migrated from Confluence space: " + space.Key,
			SpaceKey:    space.Key,
		}
		return nil
	}
	if export.SpaceKey != "" {
		t.Intermediate.Space = &IntermediateSpace{
			Team:        t.TeamName,
			Title:       export.SpaceName,
			Description: "Migrated from Confluence space: " + export.SpaceKey,
			SpaceKey:    export.SpaceKey,
		}
	}
	return nil
}

// transformPage converts a single Confluence page to intermediate format.
// children contains info about this page's child pages, used for generating navigation links.
func (t *Transformer) transformPage(confPage *ConfluencePage, export *ConfluenceExport, children []ChildPageInfo) (*IntermediatePage, error) {
	// Convert HTML content to TipTap JSON, passing children info for navigation macros
	tiptapContent, err := ConvertHTMLToTipTapWithChildren(confPage.Content, children)
	if err != nil {
		t.Logger.Warnf("Failed to convert content for page %s, using raw HTML wrapper: %v", confPage.ID, err)
		// Fallback: wrap raw HTML in a code block
		tiptapContent = wrapRawHTMLInCodeBlock(confPage.Content)
	} else if !json.Valid([]byte(tiptapContent)) {
		// Guard against a converter that returns malformed JSON: fall back to the
		// code-block wrapper and count it, so invalid bodies are never emitted.
		msg := fmt.Sprintf("page %s produced invalid TipTap JSON; using raw HTML code-block fallback", confPage.ID)
		t.Logger.Warn(msg)
		t.Stats.Warnings = append(t.Stats.Warnings, msg)
		tiptapContent = wrapRawHTMLInCodeBlock(confPage.Content)
	}

	// Build filename → attachment ID mapping for this page
	filenameToID := make(map[string]string)
	if attachments, ok := export.Attachments[confPage.ID]; ok {
		for _, att := range attachments {
			filenameToID[att.FileName] = att.ID
		}
	}

	// Convert filename-based placeholders to ID-based placeholders
	if len(filenameToID) > 0 {
		tiptapContent = ConvertAttachmentPlaceholdersToFileIDs(tiptapContent, filenameToID)
	}

	// Resolve user mentions ({{CONF_USER:userkey}} -> @username)
	tiptapContent = ResolveUserMentions(tiptapContent, export.Users)

	// Resolve user
	username := t.resolveUsername(confPage.CreatedBy, export)

	// Determine space key for this page
	spaceKey := confPage.SpaceKey
	if spaceKey == "" {
		// Fall back to export-level space key (single-space export)
		spaceKey = export.SpaceKey
	}

	// Build props
	props := model.StringInterface{
		"import_source_id": confPage.ID,
		"import_source":    "confluence",
	}
	if len(confPage.Labels) > 0 {
		props["import_labels"] = confPage.Labels
	}
	if spaceKey != "" {
		props["confluence_space_key"] = spaceKey
	}
	// Preserve the source author's Atlassian account ID so a later Mattermost step
	// can match the user by account ID (and, via the Atlassian API, email) even
	// when this migration could not resolve them to a Mattermost username.
	if confPage.CreatedBy != "" {
		props["confluence_author_account_id"] = confPage.CreatedBy
	}

	// Use page title or generate one from ID if empty
	pageTitle := confPage.Title
	if pageTitle == "" {
		pageTitle = fmt.Sprintf("Untitled Page %s", confPage.ID)
		t.Logger.Warnf("Page %s has no title, using generated title: %s", confPage.ID, pageTitle)
	}

	page := &IntermediatePage{
		ImportSourceID:       confPage.ID,
		Title:                pageTitle,
		SpaceKey:             spaceKey,
		ParentImportSourceID: confPage.ParentID,
		Content:              tiptapContent,
		User:                 username,
		CreateAt:             confPage.CreatedAt.UnixMilli(),
		UpdateAt:             timeMillis(confPage.UpdatedAt),
		Props:                props,
	}

	// Handle attachments
	if attachments, ok := export.Attachments[confPage.ID]; ok && !t.Config.SkipAttachments {
		for _, att := range attachments {
			page.Attachments = append(page.Attachments, IntermediateAttachment{
				Path:     att.FilePath,
				SourceID: att.ID,
			})
			t.Stats.AttachmentCount++
		}
	}

	return page, nil
}

// TransformComments transforms Confluence comments to intermediate format.
func (t *Transformer) TransformComments(export *ConfluenceExport) error {
	t.Logger.Info("Transforming comments")

	// Build a map of comment ID → page ID for parent chain resolution
	commentToPageID := make(map[string]string)
	for _, confComment := range export.Comments {
		if confComment.PageID != "" {
			commentToPageID[confComment.ID] = confComment.PageID
		}
	}

	// Resolve PageID for comments that only have ParentID (replies)
	for _, confComment := range export.Comments {
		if confComment.PageID == "" && confComment.ParentID != "" {
			if pageID, ok := commentToPageID[confComment.ParentID]; ok {
				confComment.PageID = pageID
			}
		}
	}

	historicalSkipped := 0
	noPageSkipped := 0

	for _, confComment := range export.Comments {
		// Skip historical versions (old edits of comments)
		if export.HistoricalCommentIDs[confComment.ID] {
			historicalSkipped++
			continue
		}
		if confComment.PageID == "" {
			noPageSkipped++
			continue
		}
		intermediateComment, err := t.transformComment(confComment, export)
		if err != nil {
			t.Logger.Warnf("Failed to transform comment %s: %v", confComment.ID, err)
			continue
		}
		t.Intermediate.Comments = append(t.Intermediate.Comments, intermediateComment)
	}

	if historicalSkipped > 0 || noPageSkipped > 0 {
		t.Logger.Infof("Skipped %d historical versions, %d without page reference", historicalSkipped, noPageSkipped)
	}

	return nil
}

// transformComment converts a single Confluence comment to intermediate format.
func (t *Transformer) transformComment(confComment *ConfluenceComment, export *ConfluenceExport) (*IntermediateComment, error) {
	// Convert HTML content to Markdown (comments use plain text/markdown, not TipTap JSON)
	// This ensures comments render correctly in the Mattermost UI which expects markdown
	markdownContent := ConvertHTMLToMarkdown(confComment.Content)

	// Resolve user mentions ({{CONF_USER:userkey}} -> @username)
	markdownContent = ResolveUserMentions(markdownContent, export.Users)

	// Resolve user
	username := t.resolveUsername(confComment.CreatedBy, export)

	// Build props
	props := model.StringInterface{
		"import_source_id": confComment.ID,
		"import_source":    "confluence",
	}
	// Preserve the source author's Atlassian account ID for later user matching.
	if confComment.CreatedBy != "" {
		props["confluence_author_account_id"] = confComment.CreatedBy
	}

	// Get inline anchor info if this is an inline comment
	var inlineAnchorID, inlineAnchorText string
	if confComment.InlineAnchor != nil {
		inlineAnchorID = confComment.InlineAnchor.AnchorID
		inlineAnchorText = confComment.InlineAnchor.OriginalSelection
	}

	comment := &IntermediateComment{
		ImportSourceID:              confComment.ID,
		PageImportSourceID:          confComment.PageID,
		ParentCommentImportSourceID: confComment.ParentID,
		Content:                     markdownContent,
		InlineAnchorID:              inlineAnchorID,
		InlineAnchorText:            inlineAnchorText,
		IsResolved:                  confComment.IsResolved,
		User:                        username,
		CreateAt:                    confComment.CreatedAt.UnixMilli(),
		UpdateAt:                    timeMillis(confComment.UpdatedAt),
		Props:                       props,
	}

	return comment, nil
}

// resolveUsername maps a Confluence account ID to a Mattermost username.
func (t *Transformer) resolveUsername(accountID string, export *ConfluenceExport) string {
	var email, username string

	// Get additional user info from Confluence export
	if user, ok := export.Users[accountID]; ok {
		email = user.Email
		username = user.Username
		// In some Confluence exports, email is stored in the username field
		if email == "" && strings.Contains(username, "@") {
			email = username
		}
	}

	// Check if accountID is actually an email (common in Confluence exports)
	if strings.Contains(accountID, "@") {
		email = accountID
	}

	// First check user mapper if available
	if t.UserMapper != nil {
		// Try email lookup (may be in username field from Confluence)
		if email != "" {
			if mmUser, err := t.UserMapper.GetUsernameByEmail(email); err == nil {
				return mmUser
			}
		}
		// Try account ID lookup
		if accountID != "" {
			if mmUser, err := t.UserMapper.GetUsername(accountID); err == nil {
				return mmUser
			}
		}
		// Try username lookup
		if username != "" {
			if mmUser, err := t.UserMapper.GetUsernameByConfluenceUsername(username); err == nil {
				return mmUser
			}
		}

		// If user mapper exists and has a fallback, use it
		if fallback := t.UserMapper.GetFallbackUsername(); fallback != "" {
			return fallback
		}
	}

	// Fall back to Confluence user data
	if email != "" {
		return emailToUsername(email)
	}
	// Only use username if it looks like a human-readable name (not an account ID)
	if username != "" && !looksLikeAccountID(username) {
		return emailToUsername(username)
	}

	// Last resort: use account ID - track as unmapped
	t.Stats.UsersUnmapped++
	t.Logger.Warnf("Could not resolve username for account %s, using fallback", accountID)
	if len(accountID) >= 8 {
		return "confluence_user_" + accountID[:8]
	}
	return "confluence_user_" + accountID
}

// looksLikeAccountID returns true if the string appears to be an Atlassian account ID
// rather than a human-readable username. Account IDs are typically:
// - 24-character hex strings (e.g., "5a6771383c7f1842c3d7b906")
// - UUID format with "557058:" or "712020:" prefixes (e.g., "557058:43d65f7e-87b9-4b8c-81f8-ab731104114e")
func looksLikeAccountID(s string) bool {
	// Check for UUID-style account IDs with numeric prefix
	if strings.Contains(s, ":") && strings.Contains(s, "-") {
		return true
	}
	// Check for 24-character hex strings (common Atlassian format)
	if len(s) == 24 {
		for _, c := range s {
			if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
				return false
			}
		}
		return true
	}
	return false
}

// timeMillis returns the Unix-millisecond timestamp, or 0 for a zero time so the
// value is omitted rather than emitted as a bogus far-past timestamp.
func timeMillis(ts time.Time) int64 {
	if ts.IsZero() {
		return 0
	}
	return ts.UnixMilli()
}

// wrapRawHTMLInCodeBlock wraps HTML in a TipTap code block as fallback.
func wrapRawHTMLInCodeBlock(html string) string {
	return `{"type":"doc","content":[{"type":"codeBlock","attrs":{"language":"html"},"content":[{"type":"text","text":` + jsonEscape(html) + `}]}]}`
}

// emailToUsername extracts username from email address.
func emailToUsername(email string) string {
	for i, c := range email {
		if c == '@' {
			return email[:i]
		}
	}
	return email
}

// jsonEscape escapes a string for JSON embedding.
func jsonEscape(s string) string {
	// Simple JSON string escaping
	result := `"`
	for _, c := range s {
		switch c {
		case '"':
			result += `\"`
		case '\\':
			result += `\\`
		case '\n':
			result += `\n`
		case '\r':
			result += `\r`
		case '\t':
			result += `\t`
		default:
			if c < 32 {
				result += `\u` + string('0'+c/16) + string('0'+c%16)
			} else {
				result += string(c)
			}
		}
	}
	result += `"`
	return result
}
