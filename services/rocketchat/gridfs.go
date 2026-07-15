package rocketchat

import (
	"fmt"
	"io"
	"os"
	"sort"

	"go.mongodb.org/mongo-driver/bson"
)

// GridFSChunk represents one chunk of a GridFS file as stored in
// rocketchat_uploads.chunks.bson.
type GridFSChunk struct {
	ID      any    `bson:"_id"`
	FilesID string `bson:"files_id"` // References the upload _id
	N       int    `bson:"n"`        // Chunk number (0-indexed)
	Data    []byte `bson:"data"`     // Binary chunk data (typically 255 KB)
}

// gridFSChunkMeta decodes only the lightweight metadata of a chunk. The Data
// field is deliberately omitted so the BSON decoder skips the (large) binary
// payload rather than allocating and retaining it while the index is built.
type gridFSChunkMeta struct {
	FilesID string `bson:"files_id"`
	N       int    `bson:"n"`
}

// gridFSChunkLoc records where a single chunk lives within the chunks BSON file,
// so its data can be streamed on demand instead of held in memory.
type gridFSChunkLoc struct {
	offset int64 // byte offset of the chunk document within the chunks file
	n      int   // chunk number (0-indexed)
}

// GridFSIndex is a memory-light index over rocketchat_uploads.chunks.bson.
//
// A GridFS export concatenates the binary data of every uploaded file, which can
// be many gigabytes. Rather than loading all of it into memory at once, the index
// records only the byte offset and chunk number of each chunk (grouped by upload
// ID and sorted by chunk number). Attachment data is then streamed one chunk at a
// time during extraction, bounding peak memory to roughly a single chunk.
type GridFSIndex struct {
	path   string
	byFile map[string][]gridFSChunkLoc
}

// BuildGridFSIndex scans chunksFilePath (a mongodump BSON file, i.e.
// rocketchat_uploads.chunks.bson) and builds an index of chunk locations without
// retaining any chunk data.
func BuildGridFSIndex(chunksFilePath string) (*GridFSIndex, error) {
	f, err := os.Open(chunksFilePath)
	if err != nil {
		return nil, fmt.Errorf("opening GridFS chunks file %s: %w", chunksFilePath, err)
	}
	defer f.Close()

	br := newBSONDocReader(f)
	byFile := make(map[string][]gridFSChunkLoc)
	for {
		doc, offset, err := br.next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading GridFS chunk from %s: %w", chunksFilePath, err)
		}

		// Decode metadata only; the transient doc buffer (including the binary
		// payload) is discarded each iteration, so peak memory stays around one
		// chunk rather than the whole attachment corpus.
		var meta gridFSChunkMeta
		if err := bson.Unmarshal(doc, &meta); err != nil {
			return nil, fmt.Errorf("unmarshalling GridFS chunk metadata from %s: %w", chunksFilePath, err)
		}
		byFile[meta.FilesID] = append(byFile[meta.FilesID], gridFSChunkLoc{offset: offset, n: meta.N})
	}

	// Sort each group by chunk number so reassembly streams in order and can
	// validate that the sequence is contiguous.
	for fid := range byFile {
		locs := byFile[fid]
		sort.Slice(locs, func(i, j int) bool {
			return locs[i].n < locs[j].n
		})
		byFile[fid] = locs
	}

	return &GridFSIndex{path: chunksFilePath, byFile: byFile}, nil
}

// Has reports whether the index contains at least one chunk for filesID.
func (idx *GridFSIndex) Has(filesID string) bool {
	return len(idx.byFile[filesID]) > 0
}

// WriteFile reassembles the file identified by filesID into outputPath, opening
// the chunks file for the duration of the call. Prefer reassembleFrom when
// extracting many files so the chunks file is opened only once.
func (idx *GridFSIndex) WriteFile(filesID, outputPath string) error {
	f, err := os.Open(idx.path)
	if err != nil {
		return fmt.Errorf("opening GridFS chunks file %s: %w", idx.path, err)
	}
	defer f.Close()
	return idx.reassembleFrom(f, filesID, outputPath)
}

// reassembleFrom writes the binary data of the file identified by filesID to
// outputPath, streaming one chunk at a time from r (which must read the chunks
// file this index was built from).
//
// The chunks must form a complete, contiguous sequence starting at 0. A gap or
// duplicate chunk number is treated as a corrupt file and returns an error
// (before any output file is created) rather than silently producing a truncated
// or garbled output file.
func (idx *GridFSIndex) reassembleFrom(r io.ReaderAt, filesID, outputPath string) error {
	locs := idx.byFile[filesID]

	// Validate that chunks form the contiguous sequence 0, 1, 2, …, len-1 before
	// creating any output. locs is sorted by chunk number at index-build time, so
	// a value not matching its position catches both gaps and duplicates.
	for i, loc := range locs {
		if loc.n != i {
			return fmt.Errorf("chunk sequence error for %s: expected chunk %d but got chunk %d (gap or duplicate)", outputPath, i, loc.n)
		}
	}

	out, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("creating output file %s: %w", outputPath, err)
	}
	defer out.Close()

	for _, loc := range locs {
		chunk, err := readChunkAt(r, loc.offset)
		if err != nil {
			return fmt.Errorf("reading chunk %d of %s: %w", loc.n, outputPath, err)
		}
		if _, err := out.Write(chunk.Data); err != nil {
			return fmt.Errorf("writing chunk %d to %s: %w", loc.n, outputPath, err)
		}
	}
	return nil
}

// readChunkAt reads and decodes the single GridFS chunk document that begins at
// offset in r.
func readChunkAt(r io.ReaderAt, offset int64) (*GridFSChunk, error) {
	// The document's total length is encoded in its first 4 bytes (little-endian
	// int32), so read that first to know how many bytes the chunk occupies.
	var sizeBuf [4]byte
	if _, err := r.ReadAt(sizeBuf[:], offset); err != nil {
		return nil, fmt.Errorf("reading chunk size at offset %d: %w", offset, err)
	}
	docSize := int32(sizeBuf[0]) | int32(sizeBuf[1])<<8 | int32(sizeBuf[2])<<16 | int32(sizeBuf[3])<<24
	if docSize < 5 {
		return nil, fmt.Errorf("invalid chunk document size %d at offset %d", docSize, offset)
	}
	if docSize > maxBSONDocSize {
		return nil, fmt.Errorf("chunk document size %d at offset %d exceeds maximum allowed %d", docSize, offset, maxBSONDocSize)
	}

	buf := make([]byte, int(docSize))
	if _, err := r.ReadAt(buf, offset); err != nil {
		return nil, fmt.Errorf("reading chunk body at offset %d: %w", offset, err)
	}

	var chunk GridFSChunk
	if err := bson.Unmarshal(buf, &chunk); err != nil {
		return nil, fmt.Errorf("unmarshalling chunk at offset %d: %w", offset, err)
	}
	return &chunk, nil
}
