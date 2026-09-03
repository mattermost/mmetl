package confluence

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// zipEpoch is the modification time stamped on every bundle entry.
//
// A ZIP stores per-entry timestamps, so writing the real clock would make two
// bundles built from the same export differ byte for byte and defeat any
// checksum comparison an operator makes between them.
var zipEpoch = time.Unix(0, 0).UTC()

// BundleWriter assembles the output bundle.
//
// It writes to a temporary file beside the destination, validates the finished
// bytes, and only then renames. A bundle that fails its own validation never
// appears at the path the operator will hand to the importer.
type BundleWriter struct {
	outputPath string
	tempPath   string
	file       *os.File
	zip        *zip.Writer

	attachments []AttachmentBlob
	blobs       map[string]*bufferedBlob
	written     map[string]bool

	// spillDir holds attachments too large to keep in memory. It is created
	// lazily, so a bundle of small attachments touches no extra disk.
	spillDir string
}

// blobSpillThreshold matches the body threshold in section 8: anything larger
// goes to disk.
//
// Buffering is unavoidable because the contract fixes the manifest as the first
// ZIP entry and the manifest carries a checksum over every attachment, so no
// attachment can be written until all of them are known. Buffering in memory is
// not: a single attachment may be a gigabyte.
const blobSpillThreshold = 1 << 20

// bufferedBlob is one attachment held either in memory or in a temporary file.
type bufferedBlob struct {
	size   int64
	memory []byte
	path   string
}

func (b *bufferedBlob) open() (io.ReadCloser, error) {
	if b.path == "" {
		return io.NopCloser(bytes.NewReader(b.memory)), nil
	}
	file, err := os.Open(b.path)
	if err != nil {
		return nil, fmt.Errorf("reopening spilled attachment: %w", err)
	}
	return file, nil
}

// NewBundleWriter creates the temporary output file.
func NewBundleWriter(outputPath string) (*BundleWriter, error) {
	directory := filepath.Dir(outputPath)
	file, err := os.CreateTemp(directory, filepath.Base(outputPath)+".*.tmp")
	if err != nil {
		return nil, fmt.Errorf("creating temporary bundle in %s: %w", directory, err)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		_ = os.Remove(file.Name())
		return nil, fmt.Errorf("securing temporary bundle: %w", err)
	}

	return &BundleWriter{
		outputPath: outputPath,
		tempPath:   file.Name(),
		file:       file,
		zip:        zip.NewWriter(file),
		blobs:      map[string]*bufferedBlob{},
		written:    map[string]bool{},
	}, nil
}

// spill writes an oversized attachment to a command-scoped temporary directory,
// mode 0700 with 0600 files, and returns its path.
func (w *BundleWriter) spill(size int64, body io.Reader) (string, error) {
	if w.spillDir == "" {
		dir, err := os.MkdirTemp(filepath.Dir(w.outputPath), "mmetl-confluence-blobs-*")
		if err != nil {
			return "", fmt.Errorf("creating attachment spill directory: %w", err)
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			return "", fmt.Errorf("securing attachment spill directory: %w", err)
		}
		w.spillDir = dir
	}

	file, err := os.CreateTemp(w.spillDir, "blob-*")
	if err != nil {
		return "", fmt.Errorf("creating spilled attachment: %w", err)
	}
	defer func() { _ = file.Close() }()

	if chmodErr := file.Chmod(0o600); chmodErr != nil {
		return "", fmt.Errorf("securing spilled attachment: %w", chmodErr)
	}
	copied, err := io.Copy(file, io.LimitReader(body, size))
	if err != nil {
		return "", fmt.Errorf("spilling attachment to disk: %w", err)
	}
	if copied != size {
		return "", fmt.Errorf("spilled attachment: expected %d bytes, got %d", size, copied)
	}
	return file.Name(), nil
}

// Abort discards the partial bundle and any spilled attachments. It is safe to
// call more than once, and safe to call after Finish.
func (w *BundleWriter) Abort() {
	if w.file != nil {
		_ = w.file.Close()
		_ = os.Remove(w.tempPath)
		w.file = nil
	}
	if w.spillDir != "" {
		_ = os.RemoveAll(w.spillDir)
		w.spillDir = ""
	}
}

// WriteAttachment implements AttachmentSink.
//
// Bytes are buffered rather than written straight through, because the manifest
// carries checksums over the attachment set and the ZIP entry order is fixed by
// the contract: the manifest must be the first entry, and it cannot be built
// until every attachment is known.
func (w *BundleWriter) WriteAttachment(bundlePath string, size int64, body io.Reader) error {
	if w.written[bundlePath] {
		return fmt.Errorf("attachment %s written twice", bundlePath)
	}

	blob := &bufferedBlob{size: size}
	if size > blobSpillThreshold {
		path, err := w.spill(size, body)
		if err != nil {
			return err
		}
		blob.path = path
	} else {
		buffer := &bytes.Buffer{}
		buffer.Grow(int(size))

		copied, err := io.Copy(buffer, io.LimitReader(body, size))
		if err != nil {
			return fmt.Errorf("buffering attachment %s: %w", bundlePath, err)
		}
		if copied != size {
			return fmt.Errorf("attachment %s: expected %d bytes, got %d", bundlePath, size, copied)
		}
		blob.memory = buffer.Bytes()
	}

	w.written[bundlePath] = true
	w.blobs[bundlePath] = blob
	w.attachments = append(w.attachments, AttachmentBlob{Path: bundlePath, Size: size, Open: blob.open})
	return nil
}

// AttachmentsChecksum is the aggregate checksum over everything written so far.
func (w *BundleWriter) AttachmentsChecksum() (string, error) {
	return AttachmentsSHA256(w.attachments)
}

// Finish writes the manifest, the JSONL, and the attachment blobs in contract
// order, validates the result, and atomically renames it into place.
func (w *BundleWriter) Finish(manifest *Manifest, lines []Line) error {
	defer w.Abort()

	jsonl, err := renderJSONL(lines)
	if err != nil {
		return err
	}
	attachmentsSum, err := w.AttachmentsChecksum()
	if err != nil {
		return err
	}

	// Derived here, at the single point every bundle is written, so no caller can
	// produce one whose summary disagrees with its breakdown.
	manifest.Counts.deriveSummaryCounts()
	manifest.Checksums = ManifestChecksums{
		JSONLSHA256:       SHA256Hex(jsonl),
		AttachmentsSHA256: attachmentsSum,
	}
	manifestBytes, err := MarshalManifest(manifest)
	if err != nil {
		return err
	}

	// Contract order: manifest, JSONL, then attachments sorted lexically.
	if err := w.writeEntry(ManifestFilename, manifestBytes); err != nil {
		return err
	}
	if err := w.writeEntry(JSONLFilename, jsonl); err != nil {
		return err
	}
	for _, path := range sortedBlobPaths(w.blobs) {
		if err := w.writeBlobEntry(path, w.blobs[path]); err != nil {
			return err
		}
	}

	if err := w.zip.Close(); err != nil {
		return fmt.Errorf("closing bundle archive: %w", err)
	}
	if err := w.file.Sync(); err != nil {
		return fmt.Errorf("flushing bundle to disk: %w", err)
	}
	if err := w.file.Close(); err != nil {
		return fmt.Errorf("closing bundle file: %w", err)
	}
	w.file = nil

	if err := validateWrittenBundle(w.tempPath); err != nil {
		_ = os.Remove(w.tempPath)
		return err
	}
	if err := os.Rename(w.tempPath, w.outputPath); err != nil {
		_ = os.Remove(w.tempPath)
		return fmt.Errorf("moving bundle into place: %w", err)
	}
	return nil
}

func (w *BundleWriter) writeEntry(name string, body []byte) error {
	entry, err := w.createEntry(name)
	if err != nil {
		return err
	}
	if _, err := entry.Write(body); err != nil {
		return fmt.Errorf("writing bundle entry %s: %w", name, err)
	}
	return nil
}

// writeBlobEntry streams a buffered attachment into the archive, so a spilled
// blob is never brought back into memory whole.
func (w *BundleWriter) writeBlobEntry(name string, blob *bufferedBlob) error {
	entry, err := w.createEntry(name)
	if err != nil {
		return err
	}

	body, err := blob.open()
	if err != nil {
		return err
	}
	defer func() { _ = body.Close() }()

	if _, err := io.Copy(entry, io.LimitReader(body, blob.size)); err != nil {
		return fmt.Errorf("writing bundle entry %s: %w", name, err)
	}
	return nil
}

func (w *BundleWriter) createEntry(name string) (io.Writer, error) {
	header := &zip.FileHeader{Name: name, Method: zip.Deflate, Modified: zipEpoch}
	header.SetMode(0o600)

	entry, err := w.zip.CreateHeader(header)
	if err != nil {
		return nil, fmt.Errorf("creating bundle entry %s: %w", name, err)
	}
	return entry, nil
}

func sortedBlobPaths(blobs map[string]*bufferedBlob) []string {
	paths := make([]string, 0, len(blobs))
	for path := range blobs {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

func renderJSONL(lines []Line) ([]byte, error) {
	var buf bytes.Buffer
	if err := EncodeJSONL(&buf, lines); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// validateWrittenBundle re-reads the finished file and runs the same validation
// the importer will.
//
// It reads the bytes on disk rather than the values in memory on purpose: the
// point is to catch a writer bug, and a check against the in-memory model would
// share any mistake the writer made.
func validateWrittenBundle(path string) error {
	reader, err := zip.OpenReader(path)
	if err != nil {
		return fmt.Errorf("reopening the bundle for validation: %w", err)
	}
	defer func() { _ = reader.Close() }()

	if err := checkBundleEntryOrder(reader); err != nil {
		return err
	}
	if _, _, err := ValidateBundleFS(reader); err != nil {
		return fmt.Errorf("the bundle failed its own validation and was not written: %w", err)
	}
	return nil
}

// checkBundleEntryOrder enforces the contract's entry order, which ValidateBundleFS
// cannot see because an fs.FS has no order.
func checkBundleEntryOrder(reader *zip.ReadCloser) error {
	if len(reader.File) < 2 {
		return errors.New("the bundle is missing its manifest or entity stream")
	}
	if reader.File[0].Name != ManifestFilename {
		return fmt.Errorf("the first bundle entry is %q, want %q", reader.File[0].Name, ManifestFilename)
	}
	if reader.File[1].Name != JSONLFilename {
		return fmt.Errorf("the second bundle entry is %q, want %q", reader.File[1].Name, JSONLFilename)
	}

	previous := ""
	for _, file := range reader.File[2:] {
		if file.Name <= previous {
			return fmt.Errorf("bundle attachment entries are not in lexical order: %q follows %q", file.Name, previous)
		}
		previous = file.Name
		if !file.Modified.Equal(zipEpoch) {
			return fmt.Errorf("bundle entry %q carries a non-epoch timestamp", file.Name)
		}
	}
	return nil
}
