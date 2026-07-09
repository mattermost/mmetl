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

			// Strategy 1: Exact match by attachment ID
			if pageFiles != nil {
				if f, ok := pageFiles[att.ID]; ok {
					zipFile = f
					zipPath = f.Name
				}
			}

			// Strategy 2: Try different page ID (use attachment's own PageID)
			if zipFile == nil && att.PageID != "" && att.PageID != pageID {
				if altPageFiles := zipAttachmentsByPage[att.PageID]; altPageFiles != nil {
					if f, ok := altPageFiles[att.ID]; ok {
						zipFile = f
						zipPath = f.Name
					}
				}
			}

			// Strategy 3: Fall back to any available file under the page (for mismatched metadata)
			if zipFile == nil && pageFiles != nil && len(pageFiles) > 0 {
				for attID, f := range pageFiles {
					zipFile = f
					zipPath = f.Name
					t.Logger.Debugf("Fallback match: expected ID %s, using %s", att.ID, attID)
					delete(pageFiles, attID) // Mark as used
					break
				}
			}

			if zipFile == nil {
				t.Logger.Warnf("Attachment file not found in ZIP: %s (page: %s, attachment ID: %s)",
					att.FilePath, pageID, att.ID)
				skippedCount++
				continue
			}

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

	// Also extract any attachments in ZIP that weren't in the metadata
	extraCount := 0
	for pageID, pageFiles := range zipAttachmentsByPage {
		for attachmentID, f := range pageFiles {
			// Generate a filename from the path
			fileName := fmt.Sprintf("attachment_%s", attachmentID)
			outputFullPath := filepath.Join(t.Config.AttachmentsDir, pageID, fileName)

			outputDir := filepath.Dir(outputFullPath)
			if err := os.MkdirAll(outputDir, 0755); err != nil {
				t.Logger.Warnf("Failed to create directory for extra attachment: %v", err)
				continue
			}

			if err := extractZipFile(f, outputFullPath); err != nil {
				t.Logger.Warnf("Failed to extract extra attachment %s: %v", f.Name, err)
				continue
			}
			extraCount++
		}
	}

	if extraCount > 0 {
		t.Logger.Infof("Extracted %d additional attachments not in XML metadata", extraCount)
	}

	t.Logger.Infof("Attachment extraction complete: %d extracted, %d skipped", extractedCount, skippedCount)
	t.Stats.AttachmentsExtracted = extractedCount + extraCount
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
