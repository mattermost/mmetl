package rocketchat

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	log "github.com/sirupsen/logrus"
	"golang.org/x/text/unicode/norm"
)

// ExtractAttachments extracts all complete uploads into outputDir.
// For GridFS uploads (store starts with "GridFS:"), binary data is read from
// gridfsChunks (keyed by upload._id). For FileSystem uploads, files are copied
// from uploadsDir (the path provided by the user via --uploads-dir).
//
// Skips uploads that are incomplete or whose source cannot be found.
func ExtractAttachments(
	uploads map[string]*RocketChatUpload,
	gridfsChunks map[string][]GridFSChunk,
	outputDir string,
	uploadsDir string,
	logger log.FieldLogger,
) error {
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("creating attachments directory %s: %w", outputDir, err)
	}

	done := 0
	skipped := 0
	for _, upload := range uploads {
		if !upload.Complete {
			skipped++
			continue
		}

		// Apply NFC normalization before sanitizing so that combining characters
		// are composed into their canonical form first (e.g. NFD "e" + combining
		// acute → NFC "é") before any character-stripping takes place.
		sanitizedName := sanitizeFilename(norm.NFC.String(upload.Name))
		// Sanitize the upload ID as well to prevent path traversal attacks via
		// crafted IDs containing ".." or path separators.
		sanitizedID := sanitizeFilename(upload.ID)
		destFilename := fmt.Sprintf("%s_%s", sanitizedID, sanitizedName)
		destPath := filepath.Join(outputDir, destFilename)

		var extractErr error
		switch {
		case strings.HasPrefix(upload.Store, "GridFS:"):
			chunks, ok := gridfsChunks[upload.ID]
			if !ok || len(chunks) == 0 {
				logger.Warnf("GridFS chunks not found for upload %s (%s), skipping", upload.ID, upload.Name)
				skipped++
				continue
			}
			extractErr = ReassembleGridFSFile(chunks, destPath)

		case upload.Store == "FileSystem":
			if uploadsDir == "" {
				logger.Warnf("FileSystem upload %s (%s) skipped: --uploads-dir not provided", upload.ID, upload.Name)
				skipped++
				continue
			}
			// The path field is a URL path like "/file-upload/{id}/{name}".
			// Extract the filename from the last segment.
			srcFilename := filepath.Base(upload.Path)
			srcPath := filepath.Join(uploadsDir, srcFilename)
			extractErr = copyFile(srcPath, destPath)

		default:
			logger.Warnf("Unknown upload store %q for %s (%s), skipping", upload.Store, upload.ID, upload.Name)
			skipped++
			continue
		}

		if extractErr != nil {
			// Remove any partial file left by a failed write so it cannot be
			// imported later as a corrupt attachment.
			if removeErr := os.Remove(destPath); removeErr != nil && !os.IsNotExist(removeErr) {
				logger.Warnf("Failed to clean up partial attachment %s: %v", destPath, removeErr)
			}
			logger.Warnf("Failed to extract upload %s (%s): %v", upload.ID, upload.Name, extractErr)
			skipped++
			continue
		}

		done++
		if done%100 == 0 {
			logger.Infof("Extracted %d attachments so far...", done)
		}
	}

	logger.Infof("Extracted %d attachments, skipped %d", done, skipped)
	return nil
}

// copyFile copies src to dst, creating dst if necessary.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("opening source file %s: %w", src, err)
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("creating destination file %s: %w", dst, err)
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("copying %s to %s: %w", src, dst, err)
	}
	return nil
}
