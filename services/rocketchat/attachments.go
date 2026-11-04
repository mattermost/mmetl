package rocketchat

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/text/unicode/norm"

	"github.com/mattermost/mmetl/services/intermediate"
)

// ExtractAttachments extracts all complete uploads into outputDir.
// For GridFS uploads (store starts with "GridFS:"), binary data is streamed one
// chunk at a time from gridfsIndex (which may be nil if the export has no GridFS
// chunks file). For FileSystem uploads, files are copied from uploadsDir (the
// path provided by the user via --uploads-dir).
//
// Uploads that are incomplete or whose source cannot be found are skipped and
// named individually in report, which may be nil.
func ExtractAttachments(
	uploads map[string]*RocketChatUpload,
	gridfsIndex *GridFSIndex,
	outputDir string,
	uploadsDir string,
	report *intermediate.Report,
) error {
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("creating attachments directory %s: %w", outputDir, err)
	}

	entity := report.Uploads()
	entity.Seen(len(uploads))
	logger := report.Logger()

	// Open the GridFS chunks file once and share it across all GridFS uploads for
	// random-access reads, rather than re-opening it for every attachment.
	var chunksFile *os.File
	if gridfsIndex != nil {
		var err error
		chunksFile, err = os.Open(gridfsIndex.path)
		if err != nil {
			return fmt.Errorf("opening GridFS chunks file %s: %w", gridfsIndex.path, err)
		}
		defer chunksFile.Close()
	}

	done := 0
	for _, upload := range uploads {
		if !upload.Complete {
			entity.Skip(upload.ID, upload.Name, ReasonUploadIncomplete)
			continue
		}

		if upload.TypeGroup == "thumb" {
			entity.Skip(upload.ID, upload.Name, ReasonUploadThumbnail)
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
			switch {
			case gridfsIndex != nil && gridfsIndex.Has(upload.ID):
				extractErr = gridfsIndex.reassembleFrom(chunksFile, upload.ID, destPath)
			case upload.Size == 0:
				// GridFS deliberately stores zero-byte files without any chunk
				// documents. The complete upload metadata is sufficient to recreate
				// an empty attachment even when the chunks file is absent.
				extractErr = createEmptyFile(destPath)
			default:
				entity.Skip(upload.ID, upload.Name, ReasonUploadGridFSMissing)
				continue
			}

		case upload.Store == "FileSystem":
			if uploadsDir == "" {
				entity.Skip(upload.ID, upload.Name, ReasonUploadNoUploadsDir)
				continue
			}
			// The path field is a URL path like "/file-upload/{id}/{name}".
			// Extract the filename from the last segment. Sanitise it and reject
			// traversal sentinels so a malicious dump path (e.g. "/file-upload/..")
			// cannot escape uploadsDir. filepath.Base collapses to a single path
			// element, so "." and ".." are the only escape vectors that remain.
			srcFilename := sanitizeFilename(filepath.Base(upload.Path))
			if srcFilename == "" || srcFilename == "." || srcFilename == ".." {
				entity.Skip(upload.ID, upload.Name, ReasonUploadUnsafePath, upload.Path)
				continue
			}
			srcPath := filepath.Join(uploadsDir, srcFilename)
			extractErr = copyFile(srcPath, destPath)

		default:
			entity.Skip(upload.ID, upload.Name, ReasonUploadUnknownStore, upload.Store)
			continue
		}

		if extractErr != nil {
			// Remove any partial file left by a failed write so it cannot be
			// imported later as a corrupt attachment.
			removeErr := os.Remove(destPath)
			if logger != nil {
				if removeErr != nil && !os.IsNotExist(removeErr) {
					logger.Warnf("Failed to clean up partial attachment %s: %v", destPath, removeErr)
				}
				logger.WithError(extractErr).Warnf("Failed to extract upload %s", upload.ID)
			}
			entity.Skip(upload.ID, upload.Name, ReasonUploadExtractFailed)
			continue
		}

		done++
		if done%100 == 0 && logger != nil {
			logger.Infof("Extracted %d attachments so far...", done)
		}
	}

	if logger != nil {
		logger.Infof("Extracted %d attachments, skipped %d", done, entity.Skipped)
	}
	return nil
}

// VerifyAttachments checks that each complete upload's source exists without
// writing any files, and records every upload in report the way
// ExtractAttachments would. Missing sources are returned as a joined error.
func VerifyAttachments(
	uploads map[string]*RocketChatUpload,
	gridfsIndex *GridFSIndex,
	uploadsDir string,
	report *intermediate.Report,
) error {
	entity := report.Uploads()
	entity.Seen(len(uploads))
	logger := report.Logger()

	var errs []error
	fail := func(upload *RocketChatUpload, reason *intermediate.Reason, err error, args ...string) {
		entity.Skip(upload.ID, upload.Name, reason, args...)
		if logger != nil {
			logger.Error(err)
		}
		errs = append(errs, err)
	}

	for _, upload := range uploads {
		switch {
		case !upload.Complete:
			entity.Skip(upload.ID, upload.Name, ReasonUploadIncomplete)
			continue
		case upload.TypeGroup == "thumb":
			entity.Skip(upload.ID, upload.Name, ReasonUploadThumbnail)
			continue
		}

		switch {
		case strings.HasPrefix(upload.Store, "GridFS:"):
			if (gridfsIndex != nil && gridfsIndex.Has(upload.ID)) || upload.Size == 0 {
				continue
			}
			fail(upload, ReasonUploadGridFSMissing, fmt.Errorf("GridFS chunks not found for upload %s (%s)", upload.ID, upload.Name))
		case upload.Store == "FileSystem":
			if uploadsDir == "" {
				fail(upload, ReasonUploadNoUploadsDir, fmt.Errorf("FileSystem upload %s (%s): --uploads-dir not provided", upload.ID, upload.Name))
				continue
			}
			srcFilename := sanitizeFilename(filepath.Base(upload.Path))
			if srcFilename == "" || srcFilename == "." || srcFilename == ".." {
				fail(upload, ReasonUploadUnsafePath, fmt.Errorf("FileSystem upload %s (%s): unsafe source path %q", upload.ID, upload.Name, upload.Path), upload.Path)
				continue
			}
			srcPath := filepath.Join(uploadsDir, srcFilename)
			if _, statErr := os.Stat(srcPath); statErr != nil {
				fail(upload, ReasonUploadExtractFailed, fmt.Errorf("FileSystem upload %s (%s) not found at %s", upload.ID, upload.Name, srcPath))
			}
		default:
			fail(upload, ReasonUploadUnknownStore, fmt.Errorf("unknown upload store %q for %s (%s)", upload.Store, upload.ID, upload.Name), upload.Store)
		}
	}
	return errors.Join(errs...)
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
