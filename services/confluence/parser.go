// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package confluence

import (
	"archive/zip"
	"html"
	"path"
	"strconv"
	"strings"
)

// ParseConfluenceExport parses a Confluence Cloud CSV space export ZIP file into
// a ConfluenceExport. Only the CSV export format is supported; legacy
// entities.xml / HTML exports are intentionally not handled.
func (t *Transformer) ParseConfluenceExport(zipReader *zip.Reader) (*ConfluenceExport, error) {
	t.Logger.Info("Parsing Confluence export")

	export := &ConfluenceExport{
		Spaces:               make(map[string]*SpaceInfo),
		Pages:                []*ConfluencePage{},
		Comments:             []*ConfluenceComment{},
		Attachments:          make(map[string][]*ConfluenceAttachment),
		Users:                make(map[string]*ConfluenceUser),
		AttachmentFiles:      make(map[string]string),
		BodyContents:         make(map[string]string),
		HistoricalCommentIDs: make(map[string]bool),
		HistoricalPageIDs:    make(map[string]bool),
		InlineCommentAnchors: make(map[string]string),
		ContentProperties:    make(map[string]*ContentProperty),
	}

	// Index files in the ZIP by name.
	fileIndex := make(map[string]*zip.File)
	for _, f := range zipReader.File {
		fileIndex[f.Name] = f
	}

	if !isCSVExport(fileIndex) {
		return nil, ErrUnsupportedExportFormat
	}

	if err := t.parseCSVExport(fileIndex, export); err != nil {
		return nil, err
	}

	// Index attachment files by base name for later extraction/lookup.
	for _, f := range zipReader.File {
		if strings.HasPrefix(f.Name, "attachments/") && !f.FileInfo().IsDir() {
			export.AttachmentFiles[path.Base(f.Name)] = f.Name
		}
	}

	t.Logger.Infof("Parsed %d pages, %d comments, %d users",
		len(export.Pages), len(export.Comments), len(export.Users))

	return export, nil
}

// extractInlineCommentAnchors extracts inline comment markers from page content.
// Confluence inline comments are marked with: <ac:inline-comment-marker ac:ref="COMMENT_ID">anchor text</ac:inline-comment-marker>
// This function finds all such markers and returns a map of comment ID → anchor text.
// It operates on the storage-format body string and is independent of the export
// container format (XML or CSV).
func extractInlineCommentAnchors(content string) map[string]string {
	anchors := make(map[string]string)

	// Pattern: <ac:inline-comment-marker ac:ref="COMMENT_ID">anchor text</ac:inline-comment-marker>
	const markerStart = "<ac:inline-comment-marker"
	const markerEnd = "</ac:inline-comment-marker>"

	pos := 0
	for {
		startIdx := strings.Index(content[pos:], markerStart)
		if startIdx == -1 {
			break
		}
		startIdx += pos

		endIdx := strings.Index(content[startIdx:], markerEnd)
		if endIdx == -1 {
			break
		}
		endIdx += startIdx + len(markerEnd)

		marker := content[startIdx:endIdx]

		// Extract ac:ref attribute value
		refIdx := strings.Index(marker, "ac:ref=\"")
		if refIdx != -1 {
			refStart := refIdx + len("ac:ref=\"")
			refEnd := strings.Index(marker[refStart:], "\"")
			if refEnd != -1 {
				commentID := marker[refStart : refStart+refEnd]

				// Extract anchor text (content between > and </ac:inline-comment-marker>)
				contentStart := strings.Index(marker, ">")
				if contentStart != -1 {
					contentStart++
					contentEnd := strings.Index(marker[contentStart:], "</ac:inline-comment-marker>")
					if contentEnd != -1 {
						anchorText := marker[contentStart : contentStart+contentEnd]
						// Strip any nested HTML tags from the anchor text
						anchorText = stripHTMLTags(anchorText)
						anchorText = strings.TrimSpace(anchorText)
						if anchorText != "" {
							anchors[commentID] = anchorText
						}
					}
				}
			}
		}

		pos = endIdx
	}

	return anchors
}

// stripHTMLTags removes HTML tags from a string and decodes HTML entities, keeping only text content.
func stripHTMLTags(s string) string {
	var result strings.Builder
	inTag := false
	for _, c := range s {
		if c == '<' {
			inTag = true
			continue
		}
		if c == '>' {
			inTag = false
			continue
		}
		if !inTag {
			result.WriteRune(c)
		}
	}
	// Decode HTML entities like &apos; &quot; &amp; etc.
	return html.UnescapeString(result.String())
}

// parsePositionValue parses a Confluence position/ordering value into an int,
// returning 0 for empty or malformed input.
func parsePositionValue(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	pos, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return pos
}
