// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package confluence

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// attachmentPathRegex matches Confluence attachment paths: attachments/{pageID}/{attachmentID}/{version}
var attachmentPathRegex = regexp.MustCompile(`^attachments/(\d+)/(\d+)/(\d+)$`)

// sanitizeFilename removes path traversal characters from filenames to prevent directory escape attacks.
// A malicious Confluence export could contain filenames like "../../../../etc/passwd".
func sanitizeFilename(filename string) string {
	// Get just the base name, stripping any directory components
	filename = filepath.Base(filename)
	// Remove any remaining path separators (paranoid check)
	filename = strings.ReplaceAll(filename, "/", "_")
	filename = strings.ReplaceAll(filename, "\\", "_")
	// Remove leading dots to prevent hidden files and relative paths
	filename = strings.TrimLeft(filename, ".")
	// If filename is now empty, use a safe default
	if filename == "" {
		filename = "unnamed_attachment"
	}
	return filename
}

// ExtractAttachments extracts attachment files from the Confluence export ZIP
// to the configured attachments directory.
func (t *Transformer) ExtractAttachments(zipReader *zip.Reader, export *ConfluenceExport) error {
	if t.Config.SkipAttachments {
		t.Logger.Info("Skipping attachment extraction (--skip-attachments)")
		return nil
	}

	if t.Config.AttachmentsDir == "" {
		t.Logger.Warn("No attachments directory configured, skipping extraction")
		return nil
	}

	t.Logger.Info("Extracting attachments from Confluence export")

	// Build a map of actual attachment files in the ZIP organized by page ID
	// Key: pageID, Value: map[attachmentID]*zip.File
	zipAttachmentsByPage := make(map[string]map[string]*zip.File)

	for _, f := range zipReader.File {
		if f.FileInfo().IsDir() {
			continue
		}
		matches := attachmentPathRegex.FindStringSubmatch(f.Name)
		if matches == nil {
			continue
		}
		pageID := matches[1]
		attachmentID := matches[2]

		if zipAttachmentsByPage[pageID] == nil {
			zipAttachmentsByPage[pageID] = make(map[string]*zip.File)
		}
		// Store by attachmentID - if multiple versions, later one wins (typically higher version)
		zipAttachmentsByPage[pageID][attachmentID] = f
	}

	t.Logger.Infof("Found %d pages with attachments in ZIP", len(zipAttachmentsByPage))

	// TODO(csv-attachments): the on-disk attachment layout for Confluence Cloud CSV
	// exports is unverified (the sample export contained no attachment files). The
	// attachmentPathRegex above encodes the legacy XML-export layout
	// (attachments/{pageID}/{attachmentID}/{version}); confirm and adjust it against
	// a real CSV export that contains attachments. Warn loudly when the export
	// claims attachments but none matched, so the mismatch is not silent.
	if len(zipAttachmentsByPage) == 0 && len(export.Attachments) > 0 {
		t.Logger.Warnf("Export references %d pages with attachment metadata but no files matched the expected attachments/{pageID}/{attachmentID}/{version} layout; the CSV export attachment layout may differ — attachments will be skipped", len(export.Attachments))
	}

	extractedCount := 0
	skippedCount := 0

	// Iterate through all attachments in the parsed export metadata
	for pageID, attachments := range export.Attachments {
		pageFiles := zipAttachmentsByPage[pageID]
		if pageFiles == nil {
			// Try using attachment's own PageID if different
			if len(attachments) > 0 && attachments[0].PageID != pageID {
				pageFiles = zipAttachmentsByPage[attachments[0].PageID]
			}
		}

		for _, att := range attachments {
			var zipFile *zip.File
			var zipPath string
			var srcMap map[string]*zip.File

			// Strategy 1: exact match by attachment ID under the metadata page.
			if pageFiles != nil {
				if f, ok := pageFiles[att.ID]; ok {
					zipFile, zipPath, srcMap = f, f.Name, pageFiles
				}
			}

			// Strategy 2: exact match by attachment ID under the attachment's own page.
			if zipFile == nil && att.PageID != "" && att.PageID != pageID {
				if altPageFiles := zipAttachmentsByPage[att.PageID]; altPageFiles != nil {
					if f, ok := altPageFiles[att.ID]; ok {
						zipFile, zipPath, srcMap = f, f.Name, altPageFiles
					}
				}
			}

			// Only deterministic, id-verified matches are accepted. We never fall
			// back to an arbitrary file under the page: that could silently swap
			// two attachments. An unmatched entry is skipped (and not emitted).
			if zipFile == nil {
				t.Logger.Warnf("No id-verified file for attachment in ZIP: %s (page: %s, attachment ID: %s) — skipping",
					att.FileName, pageID, att.ID)
				skippedCount++
				continue
			}

			// Mark consumed so it cannot match another entry or be re-emitted.
			delete(srcMap, att.ID)

			// Determine output path: {pageID}/{filename} (relative to data/ directory)
			// The server adds "data/" prefix during import, so JSONL paths should NOT include it
			// But files are placed in data/ subdirectory in ZIP for correct lookup
			// Sanitize filename to prevent path traversal attacks (e.g. "../../../../etc/passwd")
			safeFilename := sanitizeFilename(att.FileName)
			outputRelPath := filepath.Join(pageID, safeFilename)
			outputFullPath := filepath.Join(t.Config.AttachmentsDir, pageID, safeFilename)

			// Create directory structure
			outputDir := filepath.Dir(outputFullPath)
			if err := os.MkdirAll(outputDir, 0755); err != nil {
				return fmt.Errorf("failed to create directory %s: %w", outputDir, err)
			}

			// Extract the file
			if err := extractZipFile(zipFile, outputFullPath); err != nil {
				t.Logger.Warnf("Failed to extract attachment %s: %v", att.FileName, err)
				skippedCount++
				continue
			}

			// Update the attachment's FilePath to the output location
			att.FilePath = outputRelPath

			extractedCount++
			t.Logger.Debugf("Extracted: %s -> %s", zipPath, outputFullPath)
		}
	}

	// Files in the ZIP that were not referenced by any attachment metadata are
	// intentionally NOT extracted: pages reference attachments by source ID, so
	// an unreferenced file could never be resolved and would only inflate the
	// bundle, its checksum, and the extraction counts.
	unreferenced := 0
	for _, pageFiles := range zipAttachmentsByPage {
		unreferenced += len(pageFiles)
	}
	if unreferenced > 0 {
		t.Logger.Infof("Ignoring %d ZIP attachment file(s) not referenced by any page metadata", unreferenced)
	}

	t.Logger.Infof("Attachment extraction complete: %d extracted, %d skipped", extractedCount, skippedCount)
	t.Stats.AttachmentsExtracted = extractedCount
	t.Stats.AttachmentsSkipped = skippedCount

	return nil
}

// extractZipFile extracts a single file from a zip archive to the destination path.
func extractZipFile(zipFile *zip.File, destPath string) error {
	rc, err := zipFile.Open()
	if err != nil {
		return fmt.Errorf("failed to open zip entry: %w", err)
	}
	defer rc.Close()

	destFile, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("failed to create destination file: %w", err)
	}
	defer destFile.Close()

	if _, err := io.Copy(destFile, rc); err != nil {
		return fmt.Errorf("failed to copy file contents: %w", err)
	}

	return nil
}
