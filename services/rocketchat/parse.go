package rocketchat

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"go.mongodb.org/mongo-driver/bson"

	log "github.com/sirupsen/logrus"
)

// ParsedData holds all data read from a Rocket.Chat mongodump directory.
type ParsedData struct {
	Users         []RocketChatUser
	Rooms         []RocketChatRoom
	Messages      []RocketChatMessage
	Subscriptions []RocketChatSubscription
	UploadsByID   map[string]*RocketChatUpload
}

// readBSONFile reads a concatenated BSON file (as produced by mongodump) and
// deserializes each document into type T.
func readBSONFile[T any](filePath string) ([]T, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("opening BSON file %s: %w", filePath, err)
	}
	defer f.Close()

	// A mongodump .bson file is a raw stream of back-to-back BSON documents
	// with no delimiter between them. Each document encodes its own total byte
	// length in the first 4 bytes, so we use that to frame each read.
	var results []T
	for {
		// Every BSON document begins with a 4-byte little-endian int32 that
		// gives the total length of the document in bytes, length field included.
		// io.ReadFull is used instead of Read to guarantee all 4 bytes are
		// returned even if the underlying Read returns a short count.
		var sizeBuf [4]byte
		_, err := io.ReadFull(f, sizeBuf[:])
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading BSON document size from %s: %w", filePath, err)
		}

		// Decode the 4 bytes as a little-endian int32 (LSB first, as the BSON
		// spec requires). The result is the total document size in bytes.
		docSize := int32(sizeBuf[0]) | int32(sizeBuf[1])<<8 | int32(sizeBuf[2])<<16 | int32(sizeBuf[3])<<24
		if docSize < 5 {
			// A valid BSON document is at minimum 5 bytes: 4-byte length + 1-byte
			// null terminator. Anything smaller indicates a corrupt file.
			return nil, fmt.Errorf("invalid BSON document size %d in %s", docSize, filePath)
		}

		// Allocate a buffer for the complete document and copy the 4 size bytes
		// we already consumed back into the front of it. bson.Unmarshal expects
		// the full raw document including the leading length prefix, so we must
		// reconstruct it before passing the buffer to the decoder.
		remaining := int(docSize) - 4
		doc := make([]byte, int(docSize))
		copy(doc[:4], sizeBuf[:])

		_, err = io.ReadFull(f, doc[4:4+remaining])
		if err != nil {
			return nil, fmt.Errorf("reading BSON document body from %s: %w", filePath, err)
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
