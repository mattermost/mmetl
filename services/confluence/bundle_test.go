package confluence

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// writeFixtureBundle builds a real bundle from a fixture and returns its path.
func writeFixtureBundle(t *testing.T, fixture *fixtureBundle, outputPath string) {
	t.Helper()

	writer, err := NewBundleWriter(outputPath)
	require.NoError(t, err)
	defer writer.Abort()

	for _, path := range sortedKeys(fixture.blobs) {
		body := fixture.blobs[path]
		require.NoError(t, writer.WriteAttachment(path, int64(len(body)), bytes.NewReader(body)))
	}

	manifest := fixture.manifest
	require.NoError(t, writer.Finish(&manifest, fixture.lines))
}

// bundleEntries reads a written bundle into a name-to-bytes map, preserving the
// entry order.
func bundleEntries(t *testing.T, path string) (map[string][]byte, []string) {
	t.Helper()

	reader, err := zip.OpenReader(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, reader.Close()) }()

	contents := map[string][]byte{}
	var order []string
	for _, file := range reader.File {
		rc, err := file.Open()
		require.NoError(t, err)
		body, err := io.ReadAll(rc)
		require.NoError(t, rc.Close())
		require.NoError(t, err)

		contents[file.Name] = body
		order = append(order, file.Name)
	}
	return contents, order
}

// TestBundleWriter_MatchesTheGoldenFixture is the E13 gate. The writer must
// produce exactly the bytes the frozen fixtures carry, since those are what the
// Docs importer is tested against.
func TestBundleWriter_MatchesTheGoldenFixture(t *testing.T) {
	output := filepath.Join(t.TempDir(), "confluence-eng.zip")
	writeFixtureBundle(t, fullBundle(), output)

	contents, order := bundleEntries(t, output)

	t.Run("entry order is manifest, stream, then attachments in lexical order", func(t *testing.T) {
		require.Equal(t, []string{
			ManifestFilename,
			JSONLFilename,
			"data/262145/393217/diagram.png",
			"data/262145/393218/space_logo.png",
		}, order)
	})

	t.Run("every entry matches the committed fixture byte for byte", func(t *testing.T) {
		for name, body := range contents {
			want, err := os.ReadFile(filepath.Join(fixtureDir("full"), filepath.FromSlash(name)))
			require.NoErrorf(t, err, "fixture is missing %s", name)
			require.Equalf(t, string(want), string(body), "%s differs from the fixture", name)
		}
	})
}

// A ZIP records a timestamp per entry, so a real clock would make two bundles
// built from one export differ and defeat any comparison an operator makes.
func TestBundleWriter_IsByteReproducible(t *testing.T) {
	dir := t.TempDir()

	first := filepath.Join(dir, "first.zip")
	second := filepath.Join(dir, "second.zip")
	writeFixtureBundle(t, fullBundle(), first)
	writeFixtureBundle(t, fullBundle(), second)

	firstBytes, err := os.ReadFile(first)
	require.NoError(t, err)
	secondBytes, err := os.ReadFile(second)
	require.NoError(t, err)

	require.Equal(t, firstBytes, secondBytes)
}

func TestBundleWriter_EntryTimestampsAreEpoch(t *testing.T) {
	output := filepath.Join(t.TempDir(), "bundle.zip")
	writeFixtureBundle(t, fullBundle(), output)

	reader, err := zip.OpenReader(output)
	require.NoError(t, err)
	defer func() { require.NoError(t, reader.Close()) }()

	for _, file := range reader.File {
		require.Truef(t, file.Modified.Equal(zipEpoch), "%s carries %s", file.Name, file.Modified)
	}
}

func TestBundleWriter_MinimalBundle(t *testing.T) {
	output := filepath.Join(t.TempDir(), "minimal.zip")
	writeFixtureBundle(t, minimalBundle(), output)

	_, order := bundleEntries(t, output)
	require.Equal(t, []string{ManifestFilename, JSONLFilename}, order,
		"a bundle with no attachments carries only the two required entries")
}

// A bundle that fails its own validation must never appear at the path the
// operator will hand to the importer.
func TestBundleWriter_RejectsAnInvalidBundle(t *testing.T) {
	broken := fullBundle()
	broken.lines[3].Page.ParentImportSourceID = "does-not-exist"

	output := filepath.Join(t.TempDir(), "broken.zip")

	writer, err := NewBundleWriter(output)
	require.NoError(t, err)
	defer writer.Abort()

	for _, path := range sortedKeys(broken.blobs) {
		body := broken.blobs[path]
		require.NoError(t, writer.WriteAttachment(path, int64(len(body)), bytes.NewReader(body)))
	}

	manifest := broken.manifest
	err = writer.Finish(&manifest, broken.lines)
	require.ErrorContains(t, err, "failed its own validation")

	_, statErr := os.Stat(output)
	require.True(t, os.IsNotExist(statErr), "no bundle may be left at the output path")
}

// The writer's own validation reads the bytes on disk rather than the model it
// built them from, so a writer bug cannot validate itself as correct.
func TestBundleWriter_ValidationReadsTheWrittenBytes(t *testing.T) {
	output := filepath.Join(t.TempDir(), "bundle.zip")
	writeFixtureBundle(t, fullBundle(), output)

	contents, _ := bundleEntries(t, output)
	manifest, err := DecodeManifest(bytes.NewReader(contents[ManifestFilename]))
	require.NoError(t, err)

	require.Equal(t, SHA256Hex(contents[JSONLFilename]), manifest.Checksums.JSONLSHA256,
		"the manifest checksum describes the bytes actually written")
}

func TestBundleWriter_AbortLeavesNothingBehind(t *testing.T) {
	dir := t.TempDir()
	output := filepath.Join(dir, "bundle.zip")

	writer, err := NewBundleWriter(output)
	require.NoError(t, err)
	require.NoError(t, writer.WriteAttachment("data/1/2/a.txt", 1, strings.NewReader("a")))
	writer.Abort()

	require.Empty(t, remainingFiles(t, dir), "the temporary bundle is removed")
	writer.Abort()
}

// A large attachment goes to disk rather than into memory, and the spill is
// cleaned up whichever way the writer finishes.
func TestBundleWriter_SpillsLargeAttachments(t *testing.T) {
	dir := t.TempDir()
	output := filepath.Join(dir, "bundle.zip")

	writer, err := NewBundleWriter(output)
	require.NoError(t, err)
	defer writer.Abort()

	big := bytes.Repeat([]byte("x"), blobSpillThreshold+1)
	require.NoError(t, writer.WriteAttachment("data/1/2/big.bin", int64(len(big)), bytes.NewReader(big)))

	require.NotEmpty(t, writer.spillDir, "an oversized attachment is not held in memory")
	require.Empty(t, writer.blobs["data/1/2/big.bin"].memory)

	small := []byte("small")
	require.NoError(t, writer.WriteAttachment("data/1/3/small.bin", int64(len(small)), bytes.NewReader(small)))
	require.NotEmpty(t, writer.blobs["data/1/3/small.bin"].memory, "a small attachment stays in memory")

	spillDir := writer.spillDir
	writer.Abort()

	_, statErr := os.Stat(spillDir)
	require.True(t, os.IsNotExist(statErr), "the spill directory is removed")
}

// A spilled attachment must reach the bundle with exactly its original bytes.
func TestBundleWriter_SpilledAttachmentsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	output := filepath.Join(dir, "bundle.zip")

	big := bytes.Repeat([]byte("abcdefgh"), (blobSpillThreshold/8)+7)
	fixture := minimalBundle()
	fixture.blobs = map[string][]byte{
		AttachmentBundlePath(fixtureHomePageID, "393217", "big.bin"): big,
	}
	fixture.lines[2].Page.Attachments = []AttachmentData{{
		Path: AttachmentBundlePath(fixtureHomePageID, "393217", "big.bin"),
		Props: map[string]any{
			PropImportSourceID:              "393217",
			PropConfluenceContainerSourceID: fixtureHomePageID,
			PropFilename:                    "big.bin",
			PropMediaType:                   "application/octet-stream",
			PropSize:                        len(big),
			PropSHA256:                      SHA256Hex(big),
		},
	}}
	fixture.manifest.Counts.AttachmentsEmitted = 1

	writeFixtureBundle(t, fixture, output)

	contents, _ := bundleEntries(t, output)
	require.Equal(t, big, contents[AttachmentBundlePath(fixtureHomePageID, "393217", "big.bin")])

	require.Empty(t, remainingSpillDirs(t, dir), "the spill directory is removed on success")
}

func TestBundleWriter_RejectsDuplicateAttachmentPaths(t *testing.T) {
	writer, err := NewBundleWriter(filepath.Join(t.TempDir(), "bundle.zip"))
	require.NoError(t, err)
	defer writer.Abort()

	require.NoError(t, writer.WriteAttachment("data/1/2/a.txt", 1, strings.NewReader("a")))
	require.ErrorContains(t, writer.WriteAttachment("data/1/2/a.txt", 1, strings.NewReader("b")), "written twice")
}

func TestBundleWriter_RejectsAShortAttachment(t *testing.T) {
	writer, err := NewBundleWriter(filepath.Join(t.TempDir(), "bundle.zip"))
	require.NoError(t, err)
	defer writer.Abort()

	require.ErrorContains(t,
		writer.WriteAttachment("data/1/2/a.txt", 10, strings.NewReader("short")),
		"expected 10 bytes, got 5")
}

func remainingFiles(t *testing.T, dir string) []string {
	t.Helper()

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return names
}

func remainingSpillDirs(t *testing.T, dir string) []string {
	t.Helper()

	var found []string
	for _, name := range remainingFiles(t, dir) {
		if strings.HasPrefix(name, "mmetl-confluence-blobs-") {
			found = append(found, name)
		}
	}
	return found
}
