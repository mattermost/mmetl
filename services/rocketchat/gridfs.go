package rocketchat

import (
	"fmt"
	"os"
	"sort"
)

// GridFSChunk represents one chunk of a GridFS file as stored in
// rocketchat_uploads.chunks.bson.
type GridFSChunk struct {
	ID      any    `bson:"_id"`
	FilesID string `bson:"files_id"` // References the upload _id
	N       int    `bson:"n"`        // Chunk number (0-indexed)
	Data    []byte `bson:"data"`     // Binary chunk data (typically 255 KB)
}

// LoadGridFSChunks reads all chunks from chunksFilePath (which must be a BSON file
// in mongodump format, i.e. rocketchat_uploads.chunks.bson) and returns a map of
// fileID → chunks sorted by chunk number (n).
func LoadGridFSChunks(chunksFilePath string) (map[string][]GridFSChunk, error) {
	chunks, err := readBSONFile[GridFSChunk](chunksFilePath)
	if err != nil {
		return nil, fmt.Errorf("loading GridFS chunks from %s: %w", chunksFilePath, err)
	}

	byFile := make(map[string][]GridFSChunk)
	for _, c := range chunks {
		byFile[c.FilesID] = append(byFile[c.FilesID], c)
	}

	// Sort each group by chunk number.
	for fid := range byFile {
		group := byFile[fid]
		sort.Slice(group, func(i, j int) bool {
			return group[i].N < group[j].N
		})
		byFile[fid] = group
	}

	return byFile, nil
}

// ReassembleGridFSFile writes the binary data from sorted chunks to outputPath,
// creating (or truncating) the file.
func ReassembleGridFSFile(chunks []GridFSChunk, outputPath string) error {
	f, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("creating output file %s: %w", outputPath, err)
	}
	defer f.Close()

	for _, chunk := range chunks {
		if _, err := f.Write(chunk.Data); err != nil {
			return fmt.Errorf("writing chunk %d to %s: %w", chunk.N, outputPath, err)
		}
	}
	return nil
}
