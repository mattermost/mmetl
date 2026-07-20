// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package confluence

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Manifest contains metadata about the migration for audit and verification.
type Manifest struct {
	Version          string                   `json:"version"`
	Generator        string                   `json:"generator"`
	GeneratorVersion string                   `json:"generator_version"`
	CreatedAt        time.Time                `json:"created_at"`
	Source           ManifestSource           `json:"source"`
	Target           ManifestTarget           `json:"target"`
	Counts           ManifestCounts           `json:"counts"`
	Checksums        ManifestChecksums        `json:"checksums,omitempty"`
	Users            []ManifestUser           `json:"users,omitempty"`
	RestrictedPages  []ManifestRestrictedPage `json:"restricted_pages,omitempty"`
	Warnings         []string                 `json:"warnings,omitempty"`
	Errors           []string                 `json:"errors,omitempty"`
}

// ManifestRestrictedPage records a page that carried a View restriction in
// Confluence. Restrictions are detected but not imported, so this list flags
// pages whose access will change on import.
type ManifestRestrictedPage struct {
	ID    string `json:"id"`
	Title string `json:"title,omitempty"`
}

// ManifestUser records a source Confluence user and the Mattermost username it
// resolved to, so a later step can audit or re-match users (e.g. by email via the
// Atlassian API) after import. The Confluence CSV export itself carries no email
// or human-readable username, only these opaque identifiers.
type ManifestUser struct {
	AccountID          string `json:"account_id"`
	ConfluenceUsername string `json:"confluence_username,omitempty"`
	MattermostUsername string `json:"mattermost_username,omitempty"`
}

// ManifestSource describes the source of the migration. OrganizationID and
// SpaceKey together form the source namespace that scopes all bundle source IDs.
type ManifestSource struct {
	Type           string `json:"type"`
	OrganizationID string `json:"organization_id,omitempty"`
	SpaceKey       string `json:"space_key,omitempty"`
	SpaceName      string `json:"space_name,omitempty"`
	ExportFile     string `json:"export_file"`
}

// ManifestTarget describes the target of the migration. Team is advisory
// destination metadata recorded for audit; the Docs import request selects the
// actual target team. There is no backing-channel field — the Docs plugin
// creates and owns the Space's backing channel at import time.
type ManifestTarget struct {
	Team string `json:"team"`
}

// ManifestCounts contains entity counts from the migration.
type ManifestCounts struct {
	Spaces         int `json:"spaces,omitempty"`
	Pages          int `json:"pages"`
	Comments       int `json:"comments"`
	Attachments    int `json:"attachments"`
	UsersMapped    int `json:"users_mapped"`
	UsersUnmapped  int `json:"users_unmapped"`
	PagesFlattened int `json:"pages_flattened,omitempty"`
}

// ManifestChecksums contains checksums for verification.
type ManifestChecksums struct {
	JSONLSha256       string `json:"jsonl_sha256,omitempty"`
	AttachmentsSha256 string `json:"attachments_sha256,omitempty"`
}

// MigrationStats tracks statistics during migration for manifest generation.
type MigrationStats struct {
	Warnings             []string
	Errors               []string
	UsersUnmapped        int
	PagesFlattened       int
	AttachmentCount      int
	AttachmentsExtracted int
	AttachmentsSkipped   int
}

// NewManifest creates a new manifest with basic information. teamName is
// advisory destination metadata recorded for audit; the Docs import request
// selects the actual target team.
func NewManifest(export *ConfluenceExport, teamName, exportFilePath string) *Manifest {
	manifest := &Manifest{
		Version:          "2",
		Generator:        "mmetl-confluence",
		GeneratorVersion: "2.0.0",
		CreatedAt:        time.Now().UTC(),
		Source: ManifestSource{
			Type:           "confluence",
			OrganizationID: export.OrganizationID,
			ExportFile:     filepath.Base(exportFilePath),
		},
		Target: ManifestTarget{
			Team: teamName,
		},
	}

	// A CSV export covers a single space. Prefer the parsed record; fall back to
	// the descriptor-level key/name.
	for _, space := range export.Spaces {
		manifest.Source.SpaceKey = space.Key
		manifest.Source.SpaceName = space.Name
		break
	}
	if manifest.Source.SpaceKey == "" && export.SpaceKey != "" {
		manifest.Source.SpaceKey = export.SpaceKey
		manifest.Source.SpaceName = export.SpaceName
	}

	return manifest
}

// SetCounts sets the entity counts in the manifest.
func (m *Manifest) SetCounts(pages, comments, spaces int, stats *MigrationStats) {
	m.Counts = ManifestCounts{
		Spaces:         spaces,
		Pages:          pages,
		Comments:       comments,
		Attachments:    stats.AttachmentCount,
		UsersUnmapped:  stats.UsersUnmapped,
		PagesFlattened: stats.PagesFlattened,
	}
}

// SetRestrictedPages records pages that carried a View restriction, resolving
// content IDs to titles where possible.
func (m *Manifest) SetRestrictedPages(export *ConfluenceExport) {
	if len(export.RestrictedPageIDs) == 0 {
		return
	}
	titleByID := make(map[string]string, len(export.Pages))
	for _, p := range export.Pages {
		titleByID[p.ID] = p.Title
	}
	ids := make([]string, 0, len(export.RestrictedPageIDs))
	for id := range export.RestrictedPageIDs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		m.RestrictedPages = append(m.RestrictedPages, ManifestRestrictedPage{
			ID:    id,
			Title: titleByID[id],
		})
	}
}

// SetUserMappingCount sets the user mapping count.
func (m *Manifest) SetUserMappingCount(mapped int) {
	m.Counts.UsersMapped = mapped
}

// AddWarning adds a warning to the manifest.
func (m *Manifest) AddWarning(warning string) {
	m.Warnings = append(m.Warnings, warning)
}

// AddError adds an error to the manifest.
func (m *Manifest) AddError(err string) {
	m.Errors = append(m.Errors, err)
}

// ComputeJSONLChecksum computes and sets the SHA256 checksum of the JSONL file.
func (m *Manifest) ComputeJSONLChecksum(jsonlPath string) error {
	hash, err := computeFileHash(jsonlPath)
	if err != nil {
		return err
	}
	m.Checksums.JSONLSha256 = hash
	return nil
}

// ComputeAttachmentsChecksum computes a combined checksum of all attachments.
func (m *Manifest) ComputeAttachmentsChecksum(attachmentsDir string) error {
	if _, err := os.Stat(attachmentsDir); os.IsNotExist(err) {
		return nil
	}

	hasher := sha256.New()
	err := filepath.Walk(attachmentsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}

		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()

		if _, err := io.Copy(hasher, file); err != nil {
			return err
		}
		return nil
	})

	if err != nil {
		return err
	}

	m.Checksums.AttachmentsSha256 = hex.EncodeToString(hasher.Sum(nil))
	return nil
}

// Write writes the manifest to a JSON file.
func (m *Manifest) Write(outputPath string) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(outputPath, data, 0644)
}

// LoadManifest loads a manifest from a JSON file.
func LoadManifest(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, err
	}

	return &manifest, nil
}

// computeFileHash computes the SHA256 hash of a file.
func computeFileHash(filePath string) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}

	return hex.EncodeToString(hasher.Sum(nil)), nil
}
