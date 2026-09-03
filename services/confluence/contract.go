// Package confluence implements the Confluence Cloud XML backup reader and the
// Mattermost Docs import-bundle producer.
//
// This file is the single source of truth in this repository for the version 2
// bundle contract. The Docs importer mirrors it in
// mattermost-plugin-docs/server/importer/contract.go. Neither side may change a
// field, a code, or a validation rule without updating
// docs/confluence-jsonl-contract.md, the implementation plan, and the golden
// fixtures in both repositories.
package confluence

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"sort"
	"time"
	"unicode/utf8"
)

// Bundle-wide constants.
const (
	// BundleVersion is the only bundle version this repository produces or accepts.
	BundleVersion = 2

	// ManifestVersion is the string form of BundleVersion used in the manifest.
	ManifestVersion = "2"

	// Generator identifies the producer in the manifest.
	Generator = "mmetl-confluence-xml"

	// SourceType is the manifest and props value identifying the source system.
	SourceType = "confluence"

	// JSONLFilename is the bundle-relative path of the entity stream.
	JSONLFilename = "import.jsonl"

	// ManifestFilename is the bundle-relative path of the manifest.
	ManifestFilename = "import-manifest.json"

	// AttachmentDirName is the bundle-relative root of attachment bytes.
	AttachmentDirName = "data"
)

// Line types, in their required emission order.
const (
	LineTypeVersion                  = "version"
	LineTypeSpace                    = "space"
	LineTypePage                     = "page"
	LineTypePageComment              = "page_comment"
	LineTypeResolveSpacePlaceholders = "resolve_space_placeholders"
)

// Confluence content types carried in page props.
const (
	ContentTypePage     = "page"
	ContentTypeBlogPost = "blogpost"
)

// Prop keys. Every required key is spelled exactly once here.
const (
	PropImportSourceID = "import_source_id"
	PropImportSource   = "import_source"

	PropConfluenceSpaceKey          = "confluence_space_key"
	PropConfluenceContentType       = "confluence_content_type"
	PropConfluenceAuthorAccountID   = "confluence_author_account_id"
	PropImportLabels                = "import_labels"
	PropConfluenceLabels            = "confluence_labels"
	PropConfluenceRestrictions      = "confluence_restrictions"
	PropConfluenceContainerSourceID = "confluence_container_source_id"

	PropFilename  = "filename"
	PropMediaType = "media_type"
	PropSize      = "size"
	PropSHA256    = "sha256"
)

// Username proposal provenance values for ManifestUser.UsernameProposalSource.
const (
	UsernameProposalExplicitMapping = "explicit_mapping"
	UsernameProposalSourceUsername  = "source_username"
	UsernameProposalSourceEmail     = "source_email"
	UsernameProposalDisplayName     = "source_display_name"
	UsernameProposalFallback        = "fallback"
)

// Fidelity values. The bundle states what was actually done, never what a
// reader might hope was done.
const (
	FidelityPagesImported             = "imported"
	FidelityBlogPostsImportedAsPages  = "imported_as_pages"
	FidelityCommentsImportedAsPosts   = "imported_as_posts"
	FidelityAttachmentsImported       = "imported"
	FidelityLabelsPreservedNotApplied = "preserved_not_applied"
	FidelityRestrictionsUnverified    = "restriction_extraction_unverified"
	FidelitySpacePermissionsNotImport = "not_imported"
	FidelityExternalAuthPreserved     = "identifier_preserved_not_applied"
)

// Stable producer warning codes. Consumers key logic off these, never off
// human-readable messages.
const (
	WarnSourceArchiveUnknownEntry  = "source_archive_unknown_entry"
	WarnXMLUnknownReferenceClass   = "xml_unknown_reference_class"
	WarnPageMissingBody            = "page_missing_body"
	WarnPageTitleDefaulted         = "page_title_defaulted"
	WarnPageTitleTruncated         = "page_title_truncated"
	WarnPageParentMissingPromoted  = "page_parent_missing_promoted"
	WarnPageDepthFlattened         = "page_depth_flattened"
	WarnPageContentTooLarge        = "page_content_too_large"
	WarnPagePropsTooLarge          = "page_props_too_large"
	WarnUnsupportedMacro           = "unsupported_macro"
	WarnDangerousURLRemoved        = "dangerous_url_removed"
	WarnCommentParentMissingSkip   = "comment_parent_missing_skipped"
	WarnCommentAncestorSkipped     = "comment_ancestor_skipped"
	WarnCommentTooLargeSkipped     = "comment_too_large_skipped"
	WarnAttachmentHomePageMissing  = "attachment_home_page_missing"
	WarnAttachmentBlobMissing      = "attachment_blob_missing"
	WarnAttachmentBlobCorrupt      = "attachment_blob_corrupt"
	WarnAttachmentSizeMismatch     = "attachment_size_mismatch"
	WarnAttachmentFilenameSanitize = "attachment_filename_sanitized"
	WarnAttachmentSkippedByFlag    = "attachment_skipped_by_flag"
	WarnUserPlaceholderEmail       = "user_placeholder_email"
	WarnRestrictionUnverified      = "restriction_extraction_unverified"
)

// Producer hard-error codes.
const (
	ErrCodeContentDuplicateCanonical = "content_duplicate_canonical"
)

// Limits enforced by the producer so the Docs importer never receives a bundle
// it must reject. They mirror the Docs model limits, except PropsTargetBytes,
// which is deliberately stricter than the destination limit to leave the
// importer room for its own metadata namespace.
const (
	TitleMaxRunes    = 255
	BodyMaxBytes     = 2 << 20  // 2 MiB
	PropsMaxBytes    = 64 << 10 // 64 KiB, destination hard limit
	PropsTargetBytes = 48 << 10 // 48 KiB, producer target
	MaxPageDepth     = 10
	MessageMaxBytes  = 2048 // bounded warning and error messages
)

// EmptyDocumentJSON is the canonical empty TipTap document. A page with no
// usable source body is emitted with exactly these bytes.
const EmptyDocumentJSON = `{"type":"doc","content":[{"type":"paragraph"}]}`

// Line is one JSONL record. Exactly one payload pointer is set, and it is the
// one named by Type.
type Line struct {
	Type                     string                   `json:"type"`
	Version                  *int                     `json:"version,omitempty"`
	Source                   *Source                  `json:"source,omitempty"`
	Space                    *SpaceData               `json:"space,omitempty"`
	Page                     *PageData                `json:"page,omitempty"`
	PageComment              *PageCommentData         `json:"page_comment,omitempty"`
	ResolveSpacePlaceholders *ResolvePlaceholdersData `json:"resolve_space_placeholders,omitempty"`
}

// Source identifies the Confluence namespace every source ID in the bundle is
// scoped to.
//
// SpaceKey is the identity the destination scopes by, and it is what
// space.props.import_source_id and every page's space_import_source_id repeat.
// SpaceID is the immutable numeric Space object ID, carried alongside it as
// metadata: a Confluence space key can be renamed and the ID cannot, so an
// importer that later wants rename-safe reimport has the stable identity
// available without a new bundle version.
type Source struct {
	OrganizationID string `json:"organization_id"`
	SpaceID        string `json:"space_id"`
	SpaceKey       string `json:"space_key"`
}

// SpaceData describes the destination Docs Space.
type SpaceData struct {
	Team        string         `json:"team"`
	Title       string         `json:"title"`
	Description string         `json:"description,omitempty"`
	Props       map[string]any `json:"props"`
}

// PageData describes one destination Docs page. Pages appear parent-first.
type PageData struct {
	Team string `json:"team"`
	// SpaceImportSourceID repeats Source.SpaceKey.
	SpaceImportSourceID  string           `json:"space_import_source_id"`
	User                 string           `json:"user"`
	Title                string           `json:"title"`
	Content              string           `json:"content"`
	ParentImportSourceID string           `json:"parent_import_source_id,omitempty"`
	CreateAt             int64            `json:"create_at,omitempty"`
	UpdateAt             int64            `json:"update_at,omitempty"`
	Props                map[string]any   `json:"props"`
	Attachments          []AttachmentData `json:"attachments,omitempty"`
}

// AttachmentData names one attachment blob carried in the bundle and the props
// the importer needs to verify and map it.
type AttachmentData struct {
	Path  string         `json:"path"`
	Props map[string]any `json:"props"`
}

// PageCommentData describes one destination Mattermost post backing a
// Confluence comment. Thread roots appear before their descendants.
type PageCommentData struct {
	PageImportSourceID          string         `json:"page_import_source_id"`
	ParentCommentImportSourceID string         `json:"parent_comment_import_source_id,omitempty"`
	ThreadRootImportSourceID    string         `json:"thread_root_import_source_id"`
	User                        string         `json:"user"`
	Content                     string         `json:"content"`
	CreateAt                    int64          `json:"create_at,omitempty"`
	UpdateAt                    int64          `json:"update_at,omitempty"`
	IsResolved                  bool           `json:"is_resolved"`
	Props                       map[string]any `json:"props"`
}

// ResolvePlaceholdersData is the payload of the trailing ordering sentinel.
//
// Placeholder resolution itself happens during page execution, once destination
// mappings exist; this line only marks the end of the stream. It repeats the
// team and the source space so a consumer can cross-check them against the
// manifest, which catches a stream truncated and re-terminated as well as one
// simply cut short.
type ResolvePlaceholdersData struct {
	Team                string `json:"team"`
	SpaceImportSourceID string `json:"space_import_source_id"`
}

// Manifest is import-manifest.json.
type Manifest struct {
	Version          string            `json:"version"`
	Generator        string            `json:"generator"`
	GeneratorVersion string            `json:"generator_version"`
	CreatedAt        time.Time         `json:"created_at"`
	Source           ManifestSource    `json:"source"`
	Target           ManifestTarget    `json:"target"`
	Counts           ManifestCounts    `json:"counts"`
	Checksums        ManifestChecksums `json:"checksums"`
	Users            []ManifestUser    `json:"users"`
	Fidelity         ManifestFidelity  `json:"fidelity"`
	Warnings         []Warning         `json:"warnings"`
	Errors           []Issue           `json:"errors"`
}

// ManifestSource records where the bundle came from. It carries no page titles,
// body text, or per-user private data.
type ManifestSource struct {
	Type           string `json:"type"`
	OrganizationID string `json:"organization_id"`
	SpaceID        string `json:"space_id"`
	SpaceKey       string `json:"space_key"`
	SpaceName      string `json:"space_name"`
	ExportFile     string `json:"export_file"`
	TimezoneID     string `json:"timezone_id"`
}

// ManifestTarget records the requested destination team.
type ManifestTarget struct {
	Team string `json:"team"`
}

// ManifestCounts describes producer discovery and emission, never successful
// destination import.
type ManifestCounts struct {
	SpacesEmitted            int `json:"spaces_emitted"`
	PagesDiscovered          int `json:"pages_discovered"`
	PagesEmitted             int `json:"pages_emitted"`
	PagesSkipped             int `json:"pages_skipped"`
	BlogPostsEmitted         int `json:"blogposts_emitted"`
	CommentsDiscovered       int `json:"comments_discovered"`
	CommentsEmitted          int `json:"comments_emitted"`
	CommentsSkipped          int `json:"comments_skipped"`
	AttachmentsDiscovered    int `json:"attachments_discovered"`
	AttachmentsEmitted       int `json:"attachments_emitted"`
	AttachmentsSkipped       int `json:"attachments_skipped"`
	UsersEmitted             int `json:"users_emitted"`
	UsersInactive            int `json:"users_inactive"`
	UsersPlaceholderEmail    int `json:"users_placeholder_email"`
	LabelsPreserved          int `json:"labels_preserved"`
	RestrictedPagesPreserved int `json:"restricted_pages_preserved"`
	PagesFlattened           int `json:"pages_flattened"`
	MissingBodies            int `json:"missing_bodies"`

	// The four-count summary the Docs importer reconciles against the stream it
	// parsed. It is redundant with the breakdown above and derived from it, but
	// the importer reads these names and nothing else, so a bundle that omits
	// them is rejected for declaring zero pages.
	//
	// Pages counts page and blog-post lines together, because a blog post is
	// emitted as a page.
	Spaces      int `json:"spaces"`
	Pages       int `json:"pages"`
	Comments    int `json:"comments"`
	Attachments int `json:"attachments"`
}

// deriveSummaryCounts fills the four-count summary from the breakdown, so the
// two can never disagree.
func (c *ManifestCounts) deriveSummaryCounts() {
	c.Spaces = c.SpacesEmitted
	c.Pages = c.PagesEmitted + c.BlogPostsEmitted
	c.Comments = c.CommentsEmitted
	c.Attachments = c.AttachmentsEmitted
}

// ManifestChecksums pins the exact bundle payload bytes.
type ManifestChecksums struct {
	JSONLSHA256       string `json:"jsonl_sha256"`
	AttachmentsSHA256 string `json:"attachments_sha256"`
}

// ManifestFidelity states, per content class, what the bundle actually claims.
type ManifestFidelity struct {
	Pages            string `json:"pages"`
	BlogPosts        string `json:"blogposts"`
	Comments         string `json:"comments"`
	Attachments      string `json:"attachments"`
	Labels           string `json:"labels"`
	PageRestrictions string `json:"page_restrictions"`
	SpacePermissions string `json:"space_permissions"`
	ExternalAuth     string `json:"external_auth"`
}

// ManifestUser is one source user referenced by selected content. The importer
// never matches on a placeholder email.
type ManifestUser struct {
	AccountID              string `json:"account_id"`
	ConfluenceUserKey      string `json:"confluence_user_key,omitempty"`
	ConfluenceUsername     string `json:"confluence_username,omitempty"`
	DisplayName            string `json:"display_name,omitempty"`
	Email                  string `json:"email"`
	ExternalID             string `json:"external_id,omitempty"`
	Active                 bool   `json:"active"`
	MattermostUsername     string `json:"mattermost_username"`
	EmailIsPlaceholder     bool   `json:"email_is_placeholder"`
	UsernameProposalSource string `json:"username_proposal_source"`
}

// Issue is a hard problem. A manifest with any error is not a valid bundle.
type Issue struct {
	Code       string `json:"code"`
	EntityType string `json:"entity_type,omitempty"`
	SourceID   string `json:"source_id,omitempty"`
	Message    string `json:"message"`
}

// Warning is a recoverable problem. It is always paired with a skipped count or
// a documented fallback.
type Warning struct {
	Code       string `json:"code"`
	EntityType string `json:"entity_type,omitempty"`
	SourceID   string `json:"source_id,omitempty"`
	Message    string `json:"message"`
}

// SortWarnings orders warnings deterministically by code, entity type, source
// ID, then message.
func SortWarnings(warnings []Warning) {
	sort.SliceStable(warnings, func(i, j int) bool {
		return lessIssueKey(
			[4]string{warnings[i].Code, warnings[i].EntityType, warnings[i].SourceID, warnings[i].Message},
			[4]string{warnings[j].Code, warnings[j].EntityType, warnings[j].SourceID, warnings[j].Message},
		)
	})
}

// SortIssues orders errors deterministically by code, entity type, source ID,
// then message.
func SortIssues(issues []Issue) {
	sort.SliceStable(issues, func(i, j int) bool {
		return lessIssueKey(
			[4]string{issues[i].Code, issues[i].EntityType, issues[i].SourceID, issues[i].Message},
			[4]string{issues[j].Code, issues[j].EntityType, issues[j].SourceID, issues[j].Message},
		)
	})
}

func lessIssueKey(a, b [4]string) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

// TruncateMessage bounds a warning or error message to MessageMaxBytes without
// splitting a rune.
func TruncateMessage(msg string) string {
	if len(msg) <= MessageMaxBytes {
		return msg
	}
	cut := MessageMaxBytes
	for cut > 0 && !utf8.RuneStart(msg[cut]) {
		cut--
	}
	return msg[:cut]
}

// canonicalEncoder returns a JSON encoder producing the canonical bundle
// encoding: compact, HTML-unescaped, LF-terminated. Object keys are sorted
// because every dynamic object in the contract is a Go map.
func canonicalEncoder(w io.Writer) *json.Encoder {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc
}

// MarshalCanonical encodes v with the canonical bundle encoding and no trailing
// newline.
func MarshalCanonical(v any) ([]byte, error) {
	var buf bytes.Buffer
	if err := canonicalEncoder(&buf).Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// EncodeJSONL writes lines as the canonical import.jsonl byte stream: one
// canonical JSON object per line, each terminated by a single LF.
func EncodeJSONL(w io.Writer, lines []Line) error {
	enc := canonicalEncoder(w)
	for i := range lines {
		if err := enc.Encode(&lines[i]); err != nil {
			return fmt.Errorf("encoding jsonl line %d: %w", i+1, err)
		}
	}
	return nil
}

// MarshalManifest encodes the manifest exactly as it must appear on disk:
// two-space indented, HTML-unescaped, LF-terminated.
func MarshalManifest(m *Manifest) ([]byte, error) {
	var buf bytes.Buffer
	enc := canonicalEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(m); err != nil {
		return nil, fmt.Errorf("encoding manifest: %w", err)
	}
	return buf.Bytes(), nil
}

// DecodeJSONL reads a complete import.jsonl stream. It rejects unknown fields
// so a producer that drifts from the contract fails loudly instead of silently
// dropping data.
func DecodeJSONL(r io.Reader) ([]Line, error) {
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()

	var lines []Line
	for {
		var line Line
		err := dec.Decode(&line)
		if err == io.EOF {
			return lines, nil
		}
		if err != nil {
			return nil, fmt.Errorf("decoding jsonl line %d: %w", len(lines)+1, err)
		}
		lines = append(lines, line)
	}
}

// DecodeManifest reads import-manifest.json.
func DecodeManifest(r io.Reader) (*Manifest, error) {
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()

	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("decoding manifest: %w", err)
	}
	return &m, nil
}

// SHA256Hex returns the lowercase hex SHA-256 of b.
func SHA256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// AttachmentBlob is one attachment's bundle path and bytes, used to compute the
// aggregate attachment checksum.
type AttachmentBlob struct {
	Path string
	Size int64
	Open func() (io.ReadCloser, error)
}

// AttachmentsSHA256 computes the aggregate attachment checksum. Blobs are
// framed in lexical path order as:
//
//	uint64 big-endian path byte length
//	path UTF-8 bytes
//	uint64 big-endian file byte length
//	file bytes
//
// A bundle with no attachments hashes the empty byte stream.
func AttachmentsSHA256(blobs []AttachmentBlob) (string, error) {
	ordered := make([]AttachmentBlob, len(blobs))
	copy(ordered, blobs)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Path < ordered[j].Path })

	h := sha256.New()
	var frame [8]byte
	for _, blob := range ordered {
		if blob.Size < 0 {
			return "", fmt.Errorf("attachment %q: negative size %d", blob.Path, blob.Size)
		}
		binary.BigEndian.PutUint64(frame[:], uint64(len(blob.Path)))
		if _, err := h.Write(frame[:]); err != nil {
			return "", err
		}
		if _, err := h.Write([]byte(blob.Path)); err != nil {
			return "", err
		}
		binary.BigEndian.PutUint64(frame[:], uint64(blob.Size))
		if _, err := h.Write(frame[:]); err != nil {
			return "", err
		}
		if err := copyBlob(h, blob); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func copyBlob(dst io.Writer, blob AttachmentBlob) error {
	rc, err := blob.Open()
	if err != nil {
		return fmt.Errorf("opening attachment %q: %w", blob.Path, err)
	}
	defer func() { _ = rc.Close() }()

	written, err := io.Copy(dst, io.LimitReader(rc, blob.Size+1))
	if err != nil {
		return fmt.Errorf("reading attachment %q: %w", blob.Path, err)
	}
	if written != blob.Size {
		return fmt.Errorf("attachment %q: declared %d bytes, read %d", blob.Path, blob.Size, written)
	}
	return nil
}

// AttachmentBundlePath builds the contract path for one attachment blob. The
// filename must already be sanitized.
func AttachmentBundlePath(pageSourceID, attachmentSourceID, sanitizedFilename string) string {
	return path.Join(AttachmentDirName, pageSourceID, attachmentSourceID, sanitizedFilename)
}
