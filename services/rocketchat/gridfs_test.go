package rocketchat

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	log "github.com/sirupsen/logrus"
)

// buildBSONChunksFile creates a BSON file containing the given chunks.
func buildBSONChunksFile(t *testing.T, dir string, chunks []GridFSChunk) string {
	t.Helper()
	docs := make([]any, len(chunks))
	for i, c := range chunks {
		docs[i] = c
	}
	p := filepath.Join(dir, "chunks.bson")
	marshalBSONFile(t, p, docs)
	return p
}

func TestLoadGridFSChunks(t *testing.T) {
	t.Run("single chunk file", func(t *testing.T) {
		dir := t.TempDir()
		chunks := []GridFSChunk{
			{FilesID: "file1", N: 0, Data: []byte("hello world")},
		}
		p := buildBSONChunksFile(t, dir, chunks)

		result, err := LoadGridFSChunks(p)
		require.NoError(t, err)
		require.Len(t, result["file1"], 1)
		assert.Equal(t, []byte("hello world"), result["file1"][0].Data)
	})

	t.Run("multiple chunks sorted by n", func(t *testing.T) {
		dir := t.TempDir()
		// Write in reverse order — Load should sort by N.
		chunks := []GridFSChunk{
			{FilesID: "file1", N: 2, Data: []byte("world")},
			{FilesID: "file1", N: 0, Data: []byte("hello ")},
			{FilesID: "file1", N: 1, Data: []byte("brave ")},
		}
		p := buildBSONChunksFile(t, dir, chunks)

		result, err := LoadGridFSChunks(p)
		require.NoError(t, err)
		require.Len(t, result["file1"], 3)
		assert.Equal(t, 0, result["file1"][0].N)
		assert.Equal(t, 1, result["file1"][1].N)
		assert.Equal(t, 2, result["file1"][2].N)
	})

	t.Run("multiple files grouped correctly", func(t *testing.T) {
		dir := t.TempDir()
		chunks := []GridFSChunk{
			{FilesID: "file1", N: 0, Data: []byte("fileone")},
			{FilesID: "file2", N: 0, Data: []byte("filetwo")},
		}
		p := buildBSONChunksFile(t, dir, chunks)

		result, err := LoadGridFSChunks(p)
		require.NoError(t, err)
		assert.Len(t, result["file1"], 1)
		assert.Len(t, result["file2"], 1)
	})

	t.Run("empty chunks file", func(t *testing.T) {
		dir := t.TempDir()
		p := filepath.Join(dir, "chunks.bson")
		require.NoError(t, os.WriteFile(p, []byte{}, 0600))

		result, err := LoadGridFSChunks(p)
		require.NoError(t, err)
		assert.Empty(t, result)
	})
}

func TestReassembleGridFSFile(t *testing.T) {
	t.Run("single chunk", func(t *testing.T) {
		dir := t.TempDir()
		outPath := filepath.Join(dir, "out.bin")
		chunks := []GridFSChunk{
			{N: 0, Data: []byte("hello world")},
		}
		require.NoError(t, ReassembleGridFSFile(chunks, outPath))
		data, err := os.ReadFile(outPath)
		require.NoError(t, err)
		assert.Equal(t, []byte("hello world"), data)
	})

	t.Run("multiple chunks concatenated in order", func(t *testing.T) {
		dir := t.TempDir()
		outPath := filepath.Join(dir, "out.bin")
		chunks := []GridFSChunk{
			{N: 0, Data: []byte("Hello, ")},
			{N: 1, Data: []byte("brave ")},
			{N: 2, Data: []byte("new world!")},
		}
		require.NoError(t, ReassembleGridFSFile(chunks, outPath))
		data, err := os.ReadFile(outPath)
		require.NoError(t, err)
		assert.Equal(t, []byte("Hello, brave new world!"), data)
	})

	t.Run("empty chunks produces empty file", func(t *testing.T) {
		dir := t.TempDir()
		outPath := filepath.Join(dir, "out.bin")
		require.NoError(t, ReassembleGridFSFile(nil, outPath))
		data, err := os.ReadFile(outPath)
		require.NoError(t, err)
		assert.Empty(t, data)
	})
}

func TestExtractAttachments(t *testing.T) {
	logger := log.New()
	logger.SetOutput(os.Stderr)

	t.Run("GridFS extraction end-to-end", func(t *testing.T) {
		dir := t.TempDir()
		outDir := filepath.Join(dir, "output")

		content := []byte("binary file content")
		uploads := map[string]*RocketChatUpload{
			"up1": {ID: "up1", Name: "photo.jpg", Store: "GridFS:Uploads", Complete: true},
		}
		chunks := map[string][]GridFSChunk{
			"up1": {{N: 0, Data: content}},
		}

		err := ExtractAttachments(uploads, chunks, outDir, "", logger)
		require.NoError(t, err)

		expectedPath := filepath.Join(outDir, "up1_photo.jpg")
		data, err := os.ReadFile(expectedPath)
		require.NoError(t, err)
		assert.Equal(t, content, data)
	})

	t.Run("FileSystem copy", func(t *testing.T) {
		dir := t.TempDir()
		outDir := filepath.Join(dir, "output")
		uploadsDir := filepath.Join(dir, "uploads")
		require.NoError(t, os.MkdirAll(uploadsDir, 0755))

		srcContent := []byte("filesystem file content")
		srcPath := filepath.Join(uploadsDir, "photo.png")
		require.NoError(t, os.WriteFile(srcPath, srcContent, 0600))

		uploads := map[string]*RocketChatUpload{
			"up1": {ID: "up1", Name: "photo.png", Store: "FileSystem", Path: "/file-upload/up1/photo.png", Complete: true},
		}

		err := ExtractAttachments(uploads, nil, outDir, uploadsDir, logger)
		require.NoError(t, err)

		expectedPath := filepath.Join(outDir, "up1_photo.png")
		data, err := os.ReadFile(expectedPath)
		require.NoError(t, err)
		assert.Equal(t, srcContent, data)
	})

	t.Run("skip incomplete upload", func(t *testing.T) {
		dir := t.TempDir()
		outDir := filepath.Join(dir, "output")

		uploads := map[string]*RocketChatUpload{
			"up1": {ID: "up1", Name: "incomplete.jpg", Store: "GridFS:Uploads", Complete: false},
		}

		err := ExtractAttachments(uploads, nil, outDir, "", logger)
		require.NoError(t, err)
		// output dir might not exist since nothing was written
		_, statErr := os.Stat(filepath.Join(outDir, "up1_incomplete.jpg"))
		assert.True(t, os.IsNotExist(statErr))
	})

	t.Run("skip missing GridFS chunks with warning", func(t *testing.T) {
		dir := t.TempDir()
		outDir := filepath.Join(dir, "output")

		uploads := map[string]*RocketChatUpload{
			"up1": {ID: "up1", Name: "missing.jpg", Store: "GridFS:Uploads", Complete: true},
		}

		err := ExtractAttachments(uploads, map[string][]GridFSChunk{}, outDir, "", logger)
		require.NoError(t, err) // should not error, just warn
	})

	t.Run("NFC normalization on filename", func(t *testing.T) {
		dir := t.TempDir()
		outDir := filepath.Join(dir, "output")

		// Use NFD decomposed form: 'e' + U+0301 COMBINING ACUTE ACCENT.
		// ExtractAttachments must apply NFC *before* sanitizing so that the
		// two-codepoint sequence is composed into the single precomposed 'é'
		// (U+00E9) and then sanitized to '_'.  If NFC were applied after
		// sanitization the combining accent would have already been stripped to
		// '_' and the 'e' would remain, yielding a longer filename.
		filename := "cafe\u0301.txt" // NFD: 'e' + combining acute accent
		content := []byte("text")
		uploads := map[string]*RocketChatUpload{
			"up1": {ID: "up1", Name: filename, Store: "GridFS:Uploads", Complete: true},
		}
		chunks := map[string][]GridFSChunk{
			"up1": {{N: 0, Data: content}},
		}

		err := ExtractAttachments(uploads, chunks, outDir, "", logger)
		require.NoError(t, err)

		// NFC normalization composes "e" + combining accent → "é", then
		// sanitizeFilename replaces the non-ASCII "é" with "_", producing
		// "caf_.txt".  Without NFC-first the combining accent would be stripped
		// separately and "e" would remain, yielding the longer "cafe_.txt".
		entries, err := os.ReadDir(outDir)
		require.NoError(t, err)
		require.Len(t, entries, 1)
		assert.Equal(t, "up1_caf_.txt", entries[0].Name())
	})
}
