package rocketchat

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	log "github.com/sirupsen/logrus"
	"golang.org/x/text/unicode/norm"

	"github.com/mattermost/mmetl/services/intermediate"
)

// ExtractAttachments extracts all complete uploads into outputDir.
// For GridFS uploads (store starts with "GridFS:"), binary data is streamed one
// chunk at a time from gridfsIndex (which may be nil if the export has no GridFS
// chunks file). For FileSystem uploads, files are copied from uploadsDir (the
// path provided by the user via --uploads-dir).
//
// Skips uploads that are incomplete or whose source cannot be found. Returns
// the set of attachment paths (in the same "bulk-export-attachments/<id>_<name>"
// form embedded in IntermediatePost.Attachments by convertMessage) that failed
// to extract, so the caller can prune them from the already-built Intermediate
// before exporting — otherwise the JSONL would reference a file that doesn't
// exist on disk, and the summary would double-count the attachment as both
// Transformed (still present in post.Attachments) and Skipped (this Warn).
func ExtractAttachments(
	uploads map[string]*RocketChatUpload,
	gridfsIndex *GridFSIndex,
	outputDir string,
	uploadsDir string,
	logger log.FieldLogger,
) (map[string]bool, error) {
	failedPaths := map[string]bool{}

	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return nil, fmt.Errorf("creating attachments directory %s: %w", outputDir, err)
	}

	// Open the GridFS chunks file once and share it across all GridFS uploads for
	// random-access reads, rather than re-opening it for every attachment.
	var chunksFile *os.File
	if gridfsIndex != nil {
		var err error
		chunksFile, err = os.Open(gridfsIndex.path)
		if err != nil {
			return nil, fmt.Errorf("opening GridFS chunks file %s: %w", gridfsIndex.path, err)
		}
		defer chunksFile.Close()
	}

	// All Warn-level messages in this function report a real, uploaded
	// attachment we're failing to migrate — tagged so they count as Skipped.
	// The two Debug-level cases below (incomplete uploads, auto-generated
	// thumbnails) are RC-internal artifacts with no actual file content to
	// migrate, not migratable attachments we're losing, so they're excluded
	// from both Transformed and Skipped rather than reported as either. The
	// partial-file cleanup-failure Warn further down is incidental to an
	// already-counted drop and must not be tagged (it would double-count).
	entityLogger := logger.WithField(intermediate.EntityKeyField, intermediate.EntityAttachment)

	done := 0
	skipped := 0
	for _, upload := range uploads {
		if !upload.Complete {
			logger.Debugf("Upload %s (%s) is incomplete, skipping", upload.ID, upload.Name)
			skipped++
			continue
		}

		if upload.TypeGroup == "thumb" {
			logger.Debugf("Skipping auto-generated thumbnail %s (%s)", upload.ID, upload.Name)
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
		// Matches the path convertMessage embeds in IntermediatePost.Attachments.
		relPath := "bulk-export-attachments/" + destFilename

		var extractErr error
		switch {
		case strings.HasPrefix(upload.Store, "GridFS:"):
			switch {
			case gridfsIndex != nil && gridfsIndex.Has(upload.ID):
				extractErr = gridfsIndex.reassembleFrom(chunksFile, upload.ID, destPath)
			case upload.Size == 0:
				// GridFS deliberately stores zero-byte files without any chunk
				// documents. The complete upload metadata is sufficient to recreate
				// an empty attachment even when the chunks file is absent.
				extractErr = createEmptyFile(destPath)
			default:
				entityLogger.Warnf("GridFS chunks not found for upload %s (%s), skipping", upload.ID, upload.Name)
				failedPaths[relPath] = true
				skipped++
				continue
			}

		case upload.Store == "FileSystem":
			if uploadsDir == "" {
				entityLogger.Warnf("FileSystem upload %s (%s) skipped: --uploads-dir not provided", upload.ID, upload.Name)
				failedPaths[relPath] = true
				skipped++
				continue
			}
			// The path field is a URL path like "/file-upload/{id}/{name}".
			// Extract the filename from the last segment. Sanitise it and reject
			// traversal sentinels so a malicious dump path (e.g. "/file-upload/..")
			// cannot escape uploadsDir. filepath.Base collapses to a single path
			// element, so "." and ".." are the only escape vectors that remain.
			srcFilename := sanitizeFilename(filepath.Base(upload.Path))
			if srcFilename == "" || srcFilename == "." || srcFilename == ".." {
				entityLogger.Warnf("FileSystem upload %s (%s) skipped: unsafe source path %q", upload.ID, upload.Name, upload.Path)
				failedPaths[relPath] = true
				skipped++
				continue
			}
			srcPath := filepath.Join(uploadsDir, srcFilename)
			extractErr = copyFile(srcPath, destPath)

		default:
			entityLogger.Warnf("Unknown upload store %q for %s (%s), skipping", upload.Store, upload.ID, upload.Name)
			failedPaths[relPath] = true
			skipped++
			continue
		}

		if extractErr != nil {
			// Remove any partial file left by a failed write so it cannot be
			// imported later as a corrupt attachment.
			if removeErr := os.Remove(destPath); removeErr != nil && !os.IsNotExist(removeErr) {
				logger.Warnf("Failed to clean up partial attachment %s: %v", destPath, removeErr)
			}
			entityLogger.Warnf("Failed to extract upload %s (%s): %v", upload.ID, upload.Name, extractErr)
			failedPaths[relPath] = true
			skipped++
			continue
		}

		done++
		if done%100 == 0 {
			logger.Infof("Extracted %d attachments so far...", done)
		}
	}

	logger.Infof("Extracted %d attachments, skipped %d", done, skipped)
	return failedPaths, nil
}

func createEmptyFile(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("creating empty attachment %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", path, err)
	}
	return nil
}

// copyFile copies src to dst, creating dst if necessary.
func copyFile(src, dst string) (err error) {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("opening source file %s: %w", src, err)
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("creating destination file %s: %w", dst, err)
	}
	// Surface a delayed write error from Close (e.g. a failed flush) so a
	// truncated copy is not reported as success, without masking an earlier
	// copy error.
	defer func() {
		if cerr := out.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("closing %s: %w", dst, cerr)
		}
	}()

	if _, cerr := io.Copy(out, in); cerr != nil {
		return fmt.Errorf("copying %s to %s: %w", src, dst, cerr)
	}
	return nil
}
