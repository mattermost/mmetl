package confluence

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// recordingSink captures what the copier writes.
type recordingSink struct {
	written map[string][]byte
	err     error
}

func newRecordingSink() *recordingSink {
	return &recordingSink{written: map[string][]byte{}}
}

func (s *recordingSink) WriteAttachment(bundlePath string, size int64, body io.Reader) error {
	if s.err != nil {
		return s.err
	}
	buf, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	if int64(len(buf)) != size {
		return io.ErrUnexpectedEOF
	}
	s.written[bundlePath] = buf
	return nil
}

func sha256Of(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

// attachmentArchive builds a source archive holding the given blobs, keyed by
// their archive path.
func attachmentArchive(t *testing.T, blobs map[string]string) *SourceArchive {
	t.Helper()

	entries := []testEntry{
		{name: DescriptorEntryName, body: sampleDescriptor, method: zip.Deflate},
		{name: EntitiesEntryName, body: wrapEntities(""), method: zip.Deflate},
	}
	for path, body := range blobs {
		entries = append(entries, testEntry{name: path, body: body, method: zip.Deflate})
	}

	archive, err := OpenSourceArchive(buildArchive(t, entries...))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, archive.Close()) })

	return archive
}

func TestCopyAttachments(t *testing.T) {
	const body = "confluence fixture diagram bytes\n"

	archive := attachmentArchive(t, map[string]string{
		"attachments/26542083/26542119/1": body,
	})

	attachment := &Attachment{
		SourceID:          "26542119",
		ContainerSourceID: "26542083",
		PageSourceID:      "26542273",
		Version:           1,
		Filename:          "space logo.png",
		Size:              int64(len(body)),
	}
	deps := &DependencySelection{
		Attachments:        map[string][]*Attachment{"26542273": {attachment}},
		AttachmentsByID:    map[string]*Attachment{"26542119": attachment},
		AttachmentsEmitted: 1,
	}

	sink := newRecordingSink()
	results, warnings, err := CopyAttachments(archive, deps, sink)
	require.NoError(t, err)
	require.Empty(t, warnings)
	require.Len(t, results, 1)

	require.Equal(t, "data/26542273/26542119/space logo.png", results[0].BundlePath)
	require.Equal(t, int64(len(body)), results[0].Size)
	require.Equal(t, sha256Of(body), results[0].SHA256)
	require.Equal(t, body, string(sink.written[results[0].BundlePath]))

	// The attachment carries what was written, and keeps the original filename
	// for the props.
	require.Equal(t, "space logo.png", attachment.Filename)
	require.Equal(t, "space logo.png", attachment.SanitizedFilename)
	require.Equal(t, sha256Of(body), attachment.SHA256)
	require.Equal(t, results[0].BundlePath, attachment.BundlePath)
}

// One corrupt or missing blob must not cost the operator the whole space, so
// each of these skips the attachment and keeps going.
func TestCopyAttachments_SkipsUnusableBlobs(t *testing.T) {
	const good = "good bytes"

	tests := []struct {
		name        string
		blobs       map[string]string
		declareSize int64
		wantCode    string
		wantMessage string
	}{
		{
			name:        "blob missing from the archive",
			blobs:       map[string]string{"attachments/1/100/1": good},
			wantCode:    WarnAttachmentBlobMissing,
			wantMessage: "no blob at",
		},
		{
			name:        "size disagrees with the archive entry",
			blobs:       map[string]string{"attachments/1/200/1": good},
			declareSize: 999,
			wantCode:    WarnAttachmentSizeMismatch,
			wantMessage: "Confluence records 999 bytes",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			archive := attachmentArchive(t, test.blobs)

			attachment := &Attachment{
				SourceID: "200", ContainerSourceID: "1", PageSourceID: "50",
				Version: 1, Filename: "f.txt", Size: test.declareSize,
			}
			deps := &DependencySelection{
				Attachments:        map[string][]*Attachment{"50": {attachment}},
				AttachmentsByID:    map[string]*Attachment{"200": attachment},
				AttachmentsEmitted: 1,
			}

			results, warnings, err := CopyAttachments(archive, deps, newRecordingSink())
			require.NoError(t, err)
			require.Empty(t, results)

			require.Len(t, warnings, 1)
			require.Equal(t, test.wantCode, warnings[0].Code)
			require.Contains(t, warnings[0].Message, test.wantMessage)

			// The bundle must never list a blob it does not carry.
			require.Empty(t, deps.Attachments, "the page's empty attachment list is removed")
			require.Empty(t, deps.AttachmentsByID)
			require.Zero(t, deps.AttachmentsEmitted)
			require.Equal(t, 1, deps.AttachmentsSkipped)
		})
	}
}

func TestCopyAttachments_KeepsGoodBlobsAlongsideBad(t *testing.T) {
	const body = "kept"

	archive := attachmentArchive(t, map[string]string{"attachments/1/200/1": body})

	good := &Attachment{
		SourceID: "200", ContainerSourceID: "1", PageSourceID: "50",
		Version: 1, Filename: "kept.txt", Size: int64(len(body)),
	}
	missing := &Attachment{
		SourceID: "201", ContainerSourceID: "1", PageSourceID: "50",
		Version: 1, Filename: "gone.txt",
	}
	deps := &DependencySelection{
		Attachments:        map[string][]*Attachment{"50": {good, missing}},
		AttachmentsByID:    map[string]*Attachment{"200": good, "201": missing},
		AttachmentsEmitted: 2,
	}

	results, warnings, err := CopyAttachments(archive, deps, newRecordingSink())
	require.NoError(t, err)

	require.Len(t, results, 1)
	require.Equal(t, "200", results[0].Attachment.SourceID)
	require.Len(t, warnings, 1)

	require.Len(t, deps.Attachments["50"], 1)
	require.Equal(t, 1, deps.AttachmentsEmitted)
	require.Equal(t, 1, deps.AttachmentsSkipped)
	require.NotContains(t, deps.AttachmentsByID, "201")
}

// A sink failure is a real failure: the output is being written and cannot be
// left half-formed.
func TestCopyAttachments_SinkErrorIsFatal(t *testing.T) {
	const body = "bytes"

	archive := attachmentArchive(t, map[string]string{"attachments/1/200/1": body})

	attachment := &Attachment{
		SourceID: "200", ContainerSourceID: "1", PageSourceID: "50",
		Version: 1, Filename: "f.txt", Size: int64(len(body)),
	}
	deps := &DependencySelection{
		Attachments:     map[string][]*Attachment{"50": {attachment}},
		AttachmentsByID: map[string]*Attachment{"200": attachment},
	}

	sink := newRecordingSink()
	sink.err = io.ErrClosedPipe

	_, _, err := CopyAttachments(archive, deps, sink)
	require.ErrorIs(t, err, io.ErrClosedPipe)
}

// Two attachments whose filenames sanitize to the same string must still land
// on distinct paths.
func TestCopyAttachments_ResolvesPathCollisions(t *testing.T) {
	archive := attachmentArchive(t, map[string]string{
		"attachments/1/200/1": "a",
		"attachments/1/201/1": "b",
	})

	first := &Attachment{SourceID: "200", ContainerSourceID: "1", PageSourceID: "50", Version: 1, Filename: "a<b.png", Size: 1}
	second := &Attachment{SourceID: "201", ContainerSourceID: "1", PageSourceID: "50", Version: 1, Filename: "a>b.png", Size: 1}

	deps := &DependencySelection{
		Attachments:     map[string][]*Attachment{"50": {first, second}},
		AttachmentsByID: map[string]*Attachment{"200": first, "201": second},
	}

	results, warnings, err := CopyAttachments(archive, deps, newRecordingSink())
	require.NoError(t, err)
	require.Empty(t, warnings)
	require.Len(t, results, 2)

	// Each attachment already has its own source ID in the path, so these
	// differ; the suffix logic is what protects a genuine clash.
	require.NotEqual(t, results[0].BundlePath, results[1].BundlePath)
	require.Equal(t, "data/50/200/a_b.png", results[0].BundlePath)
	require.Equal(t, "data/50/201/a_b.png", results[1].BundlePath)
}

func TestSanitizeAttachmentFilename(t *testing.T) {
	tests := map[string]string{
		"diagram.png":      "diagram.png",
		"space logo.png":   "space logo.png",
		"café.png":         "café.png",
		"a<b>c:d.png":      "a_b_c_d.png",
		`a"b|c?d*e.png`:    "a_b_c_d_e.png",
		"../../etc/passwd": "passwd",
		`..\..\evil.txt`:   "evil.txt",
		"/absolute.png":    "absolute.png",
		"trailing.  ":      "trailing",
		"trailing dot.":    "trailing dot",
		"":                 defaultAttachmentFilename,
		"...":              defaultAttachmentFilename,
		"..":               defaultAttachmentFilename,
		".":                defaultAttachmentFilename,
		"   ":              defaultAttachmentFilename,
		"\x00\x01":         defaultAttachmentFilename,
		// Windows refuses these even with an extension.
		"CON.txt": "_CON.txt",
		"nul":     "_nul",
		"aux.png": "_aux.png",
		// A leading dot is a hidden file, not a traversal.
		".hidden.png": ".hidden.png",
	}

	for raw, want := range tests {
		t.Run(raw, func(t *testing.T) {
			got := SanitizeAttachmentFilename(raw)
			require.Equal(t, want, got)

			require.NotContains(t, got, "/")
			require.NotContains(t, got, `\`)
			require.NotEqual(t, ".", got)
			require.NotEqual(t, "..", got)
			require.NotEmpty(t, got)
		})
	}

	t.Run("long names are truncated", func(t *testing.T) {
		got := SanitizeAttachmentFilename(strings.Repeat("x", 500) + ".png")
		require.LessOrEqual(t, len([]rune(got)), attachmentFilenameMaxRunes)
		require.NotEmpty(t, got)
	})

	// The sanitized name must survive the contract's own path validation.
	t.Run("sanitized names build valid contract paths", func(t *testing.T) {
		for raw := range tests {
			name := SanitizeAttachmentFilename(raw)
			path := AttachmentBundlePath("100", "200", name)
			require.NoErrorf(t, checkAttachmentPath(path, "100", "200"), "%q -> %q", raw, name)
		}
	})
}

// TestVerifyAttachmentBlob_DetectsCorruption builds an archive whose declared
// CRC does not match its bytes, which is what a truncated or tampered export
// looks like.
func TestVerifyAttachmentBlob_DetectsCorruption(t *testing.T) {
	path := buildArchiveWithBadCRC(t)

	archive, err := OpenSourceArchive(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, archive.Close()) }()

	attachment := &Attachment{
		SourceID: "200", ContainerSourceID: "1", PageSourceID: "50", Version: 1, Filename: "f.txt",
	}
	deps := &DependencySelection{
		Attachments:        map[string][]*Attachment{"50": {attachment}},
		AttachmentsByID:    map[string]*Attachment{"200": attachment},
		AttachmentsEmitted: 1,
	}

	sink := newRecordingSink()
	results, warnings, err := CopyAttachments(archive, deps, sink)
	require.NoError(t, err)

	require.Empty(t, results)
	require.Len(t, warnings, 1)
	require.Equal(t, WarnAttachmentBlobCorrupt, warnings[0].Code)

	// Verification happens before the write, so nothing corrupt reaches the
	// bundle.
	require.Empty(t, sink.written)
}

// buildArchiveWithBadCRC writes a ZIP whose stored entry declares a CRC that
// does not match its bytes.
func buildArchiveWithBadCRC(t *testing.T) string {
	t.Helper()

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	for _, entry := range []testEntry{
		{name: DescriptorEntryName, body: sampleDescriptor},
		{name: EntitiesEntryName, body: wrapEntities("")},
	} {
		w, err := zw.Create(entry.name)
		require.NoError(t, err)
		_, err = io.WriteString(w, entry.body)
		require.NoError(t, err)
	}

	// A raw header lets the CRC be set independently of the bytes written.
	w, err := zw.CreateRaw(&zip.FileHeader{
		Name:               "attachments/1/200/1",
		Method:             zip.Store,
		CRC32:              0xDEADBEEF,
		CompressedSize64:   5,
		UncompressedSize64: 5,
	})
	require.NoError(t, err)
	_, err = w.Write([]byte("bytes"))
	require.NoError(t, err)

	require.NoError(t, zw.Close())

	path := t.TempDir() + "/bad-crc.zip"
	require.NoError(t, os.WriteFile(path, buf.Bytes(), 0o600))
	return path
}

// TestCopyAttachments_PrivateSample is the E12 gate against a real export.
//
//	CONFLUENCE_SAMPLE_ZIP=/path/to/Confluence-export.zip go test ./services/confluence/...
func TestCopyAttachments_PrivateSample(t *testing.T) {
	path := os.Getenv("CONFLUENCE_SAMPLE_ZIP")
	if path == "" {
		t.Skip("set CONFLUENCE_SAMPLE_ZIP to run against a real Confluence export")
	}

	archive, err := OpenSourceArchive(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, archive.Close()) }()

	spaces, descriptor, err := CatalogSpaces(archive)
	require.NoError(t, err)

	copied := 0
	for _, space := range spaces {
		content, err := SelectPageMetadata(archive, space, descriptor)
		require.NoError(t, err)
		deps, err := SelectDependencies(archive, space, descriptor, content, false)
		require.NoError(t, err)
		if deps.AttachmentsEmitted == 0 {
			continue
		}

		sink := newRecordingSink()
		results, warnings, err := CopyAttachments(archive, deps, sink)
		require.NoErrorf(t, err, "space %s", space.SpaceKey)
		require.Emptyf(t, warnings, "space %s reported %v", space.SpaceKey, warnings)

		for _, result := range results {
			copied++

			require.NoError(t, checkAttachmentPath(result.BundlePath,
				result.Attachment.PageSourceID, result.Attachment.SourceID))
			require.Equal(t, result.Size, int64(len(sink.written[result.BundlePath])))
			require.Equal(t, result.SHA256, sha256Of(string(sink.written[result.BundlePath])))
			require.Contains(t, content.ByID, result.Attachment.PageSourceID)

			t.Logf("%-42s %s (%d bytes)", space.SpaceKey, result.BundlePath, result.Size)
		}
	}
	require.Equal(t, 1, copied, "the sample carries exactly one attachment blob")
}
