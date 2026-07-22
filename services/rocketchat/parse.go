package rocketchat

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"go.mongodb.org/mongo-driver/bson"

	log "github.com/sirupsen/logrus"
)

// ParsedData holds all data read from a RocketChat mongodump directory.
type ParsedData struct {
	Users         []RocketChatUser
	Rooms         []RocketChatRoom
	Messages      []RocketChatMessage
	Subscriptions []RocketChatSubscription
	UploadsByID   map[string]*RocketChatUpload
}

// maxBSONDocSize is the maximum allowed BSON document size (16 MiB), matching
// MongoDB's enforced limit. Any document claiming to be larger is treated as
// corrupt to prevent unbounded memory allocation.
const maxBSONDocSize = 16 * 1024 * 1024

// bsonReadBufSize is the size of the buffer used to read BSON files. A mongodump
// stream is millions of small back-to-back documents, so reading directly from
// the *os.File would issue two syscalls per document (size prefix + body).
// Wrapping it in a bufio.Reader this size amortises those into a handful of
// large reads.
const bsonReadBufSize = 1 << 20 // 1 MiB

// bsonDocReader frames a mongodump .bson stream — a raw sequence of back-to-back
// BSON documents with no delimiter between them — into individual documents. It
// buffers the underlying reader and tracks the byte offset of each document so
// callers that need random access later (e.g. the GridFS index) can record where
// a document begins.
type bsonDocReader struct {
	r      *bufio.Reader
	offset int64 // byte offset of the next document to be read
}

func newBSONDocReader(r io.Reader) *bsonDocReader {
	return &bsonDocReader{r: bufio.NewReaderSize(r, bsonReadBufSize)}
}

// next returns the next raw BSON document (including its leading length prefix,
// as bson.Unmarshal expects) and the byte offset at which it began in the
// stream. It returns io.EOF once the stream is cleanly exhausted.
func (br *bsonDocReader) next() (doc []byte, offset int64, err error) {
	startOffset := br.offset

	// Every BSON document begins with a 4-byte little-endian int32 that gives
	// the total length of the document in bytes, length field included.
	// io.ReadFull is used instead of Read to guarantee all 4 bytes are returned
	// even if the underlying Read returns a short count. A clean io.EOF here
	// (zero bytes read) marks the end of the stream; a partial read surfaces as
	// io.ErrUnexpectedEOF and is treated as corruption by the caller.
	var sizeBuf [4]byte
	if _, err := io.ReadFull(br.r, sizeBuf[:]); err != nil {
		return nil, 0, err
	}

	// Decode the 4 bytes as a little-endian int32 (LSB first, as the BSON spec
	// requires). The result is the total document size in bytes.
	docSize := int32(sizeBuf[0]) | int32(sizeBuf[1])<<8 | int32(sizeBuf[2])<<16 | int32(sizeBuf[3])<<24
	if docSize < 5 {
		// A valid BSON document is at minimum 5 bytes: 4-byte length + 1-byte
		// null terminator. Anything smaller indicates a corrupt file.
		return nil, 0, fmt.Errorf("invalid BSON document size %d", docSize)
	}
	if docSize > maxBSONDocSize {
		// Guard against corrupt or malicious files that declare an enormous
		// document size, which would cause a huge allocation.
		return nil, 0, fmt.Errorf("BSON document size %d exceeds maximum allowed %d", docSize, maxBSONDocSize)
	}

	// Allocate a buffer for the complete document and copy the 4 size bytes we
	// already consumed back into the front of it. bson.Unmarshal expects the
	// full raw document including the leading length prefix, so we must
	// reconstruct it before passing the buffer to the decoder.
	buf := make([]byte, int(docSize))
	copy(buf[:4], sizeBuf[:])
	if _, err := io.ReadFull(br.r, buf[4:]); err != nil {
		return nil, 0, fmt.Errorf("reading BSON document body: %w", err)
	}

	br.offset += int64(docSize)
	return buf, startOffset, nil
}

// readBSONFile reads a concatenated BSON file (as produced by mongodump) and
// deserializes each document into type T.
func readBSONFile[T any](filePath string) ([]T, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("opening BSON file %s: %w", filePath, err)
	}
	defer f.Close()

	br := newBSONDocReader(f)
	var results []T
	for {
		doc, _, err := br.next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading BSON document from %s: %w", filePath, err)
		}

		var item T
		if err := bson.Unmarshal(doc, &item); err != nil {
			return nil, fmt.Errorf("unmarshalling BSON document from %s: %w", filePath, err)
		}
		results = append(results, item)
	}

	return results, nil
}

// ParseDump reads all required BSON files from dumpDir and returns a ParsedData
// struct with lookup maps ready for transformation.
func ParseDump(dumpDir string, logger log.FieldLogger) (*ParsedData, error) {
	required := []string{
		"users.bson",
		"rocketchat_room.bson",
		"rocketchat_message.bson",
		"rocketchat_subscription.bson",
	}
	for _, name := range required {
		p := filepath.Join(dumpDir, name)
		if _, err := os.Stat(p); os.IsNotExist(err) {
			return nil, fmt.Errorf("required BSON file not found: %s", p)
		}
	}

	// --- users ---
	users, err := readBSONFile[RocketChatUser](filepath.Join(dumpDir, "users.bson"))
	if err != nil {
		return nil, fmt.Errorf("parsing users: %w", err)
	}

	// --- rooms ---
	rooms, err := readBSONFile[RocketChatRoom](filepath.Join(dumpDir, "rocketchat_room.bson"))
	if err != nil {
		return nil, fmt.Errorf("parsing rooms: %w", err)
	}

	// --- messages ---
	messages, err := readBSONFile[RocketChatMessage](filepath.Join(dumpDir, "rocketchat_message.bson"))
	if err != nil {
		return nil, fmt.Errorf("parsing messages: %w", err)
	}

	// --- subscriptions ---
	subscriptions, err := readBSONFile[RocketChatSubscription](filepath.Join(dumpDir, "rocketchat_subscription.bson"))
	if err != nil {
		return nil, fmt.Errorf("parsing subscriptions: %w", err)
	}

	// --- uploads (optional) ---
	var uploads []RocketChatUpload
	uploadsBSON := filepath.Join(dumpDir, "rocketchat_uploads.bson")
	if _, err := os.Stat(uploadsBSON); err == nil {
		uploads, err = readBSONFile[RocketChatUpload](uploadsBSON)
		if err != nil {
			return nil, fmt.Errorf("parsing uploads: %w", err)
		}
	}

	// Build upload lookup map.
	uploadsByID := make(map[string]*RocketChatUpload, len(uploads))
	for i := range uploads {
		uploadsByID[uploads[i].ID] = &uploads[i]
	}

	logger.Infof("Parsed %d users, %d rooms, %d messages, %d subscriptions, %d uploads",
		len(users), len(rooms), len(messages), len(subscriptions), len(uploads))

	return &ParsedData{
		Users:         users,
		Rooms:         rooms,
		Messages:      messages,
		Subscriptions: subscriptions,
		UploadsByID:   uploadsByID,
	}, nil
}
