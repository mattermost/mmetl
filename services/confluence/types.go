// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package confluence

import "time"

// ConfluenceExport represents a parsed Confluence space export.
//
// A Confluence Cloud CSV export always covers a single space; the Spaces map is
// the parse-side shape (the natural seam should site/multi-space exports ever
// appear), but the transform/export side commits to one space per bundle.
type ConfluenceExport struct {
	// SpaceKey/SpaceName describe the single space this export covers.
	SpaceKey  string
	SpaceName string

	// OrganizationID namespaces source IDs (page IDs, space keys) across
	// Confluence instances. Derived from exportDescriptor.properties.
	OrganizationID string

	// Spaces holds the parsed space records keyed by space key. A CSV export
	// yields exactly one; more than one is rejected at transform time.
	Spaces map[string]*SpaceInfo // spaceKey -> space info

	// RestrictedPageIDs holds content IDs carrying a View restriction. These are
	// detected and surfaced (not resolved into an ACL) so a view-restricted page
	// is not silently widened on import.
	RestrictedPageIDs map[string]bool

	Pages           []*ConfluencePage
	Comments        []*ConfluenceComment
	Attachments     map[string][]*ConfluenceAttachment // pageID -> attachments
	Users           map[string]*ConfluenceUser         // accountID -> user
	AttachmentFiles map[string]string                  // attachmentID -> file path in export
	BodyContents    map[string]string                  // bodyContentID -> HTML content

	// HistoricalCommentIDs contains comment IDs that are historical versions (old edits)
	// These should be skipped during transformation as they're not current comments
	HistoricalCommentIDs map[string]bool

	// HistoricalPageIDs contains page IDs that are historical versions (old edits)
	// These should be skipped during transformation as they're not current pages
	HistoricalPageIDs map[string]bool

	// InlineCommentAnchors maps inline-marker-ref UUID to anchor text extracted from page content.
	// Populated by extracting <ac:inline-comment-marker> tags from page bodies.
	InlineCommentAnchors map[string]string

	// ContentProperties maps ContentProperty ID to property name and value.
	// Used to look up inline-marker-ref for comments.
	ContentProperties map[string]*ContentProperty
}

// ContentProperty represents a key-value property attached to content.
type ContentProperty struct {
	ID          string
	Name        string
	StringValue string
}

// SpaceInfo holds space metadata.
type SpaceInfo struct {
	Key         string
	Name        string
	Description string
	HomePageID  string // Root page ID for the space
}

// ConfluencePage represents a page from the Confluence export.
type ConfluencePage struct {
	ID                string
	Title             string
	ParentID          string // Empty for root pages
	SpaceKey          string // Space this page belongs to
	Content           string // HTML content (Confluence Storage Format)
	BodyContentID     string // Reference to BodyContent object
	CreatedBy         string // Account ID
	CreatedAt         time.Time
	UpdatedBy         string // Account ID
	UpdatedAt         time.Time
	Version           int
	Labels            []string
	Children          []*ConfluencePage // For building hierarchy
	Restrictions      *PageRestrictions
	HistoricalIDs     []string // IDs of historical versions (old edits) of this page
	OriginalVersionID string   // If non-empty, this page is a historical version pointing to the current page
	ContentStatus     string   // "current", "draft", "deleted", "archived"
	Position          int      // Ordering position within parent (from Confluence's position property)
}

// PageRestrictions represents view/edit restrictions on a page.
type PageRestrictions struct {
	ViewUsers  []string // Account IDs
	ViewGroups []string
	EditUsers  []string // Account IDs
	EditGroups []string
}

// ConfluenceComment represents a comment from the Confluence export.
type ConfluenceComment struct {
	ID                 string
	PageID             string
	ParentID           string // For threaded comments
	Content            string // HTML content
	BodyContentID      string // Reference to BodyContent object
	CreatedBy          string // Account ID
	CreatedAt          time.Time
	UpdatedBy          string // Account ID
	UpdatedAt          time.Time
	InlineAnchor       *InlineAnchor // For inline comments
	HistoricalIDs      []string      // IDs of historical versions (old edits) of this comment
	ContentPropertyIDs []string      // IDs of ContentProperty objects for this comment
	IsResolved         bool          // True if comment was resolved in Confluence
}

// InlineAnchor represents the position of an inline comment in page content.
type InlineAnchor struct {
	AnchorID          string // The UUID from ac:ref attribute (used to link TipTap mark to comment)
	OriginalSelection string // The selected text
	TextContext       string // Surrounding text for fuzzy matching
	Offset            int    // Character offset in content
}

// ConfluenceAttachment represents an attachment from the Confluence export.
type ConfluenceAttachment struct {
	ID        string
	PageID    string
	FileName  string
	MediaType string
	FileSize  int64
	CreatedBy string
	CreatedAt time.Time
	Comment   string
	FilePath  string // Path within the export ZIP
}

// ConfluenceUser represents a user from the Confluence export.
type ConfluenceUser struct {
	AccountID   string
	Username    string
	DisplayName string
	Email       string
}

// ExportFormat represents the type of Confluence export.
type ExportFormat string

const (
	// ExportFormatXML is the standard Confluence XML export format.
	ExportFormatXML ExportFormat = "xml"
	// ExportFormatHTML is the HTML export format (used by some Confluence versions).
	ExportFormatHTML ExportFormat = "html"
)
