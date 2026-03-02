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
	UsersByID             map[string]*RocketChatUser
	RoomsByID             map[string]*RocketChatRoom
	MessagesByRoomID      map[string][]*RocketChatMessage
	SubscriptionsByRoomID map[string][]*RocketChatSubscription
	UploadsByID           map[string]*RocketChatUpload
}

// readBSONFile reads a concatenated BSON file (as produced by mongodump) and
// deserialises each document into type T.
func readBSONFile[T any](filePath string) ([]T, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("opening BSON file %s: %w", filePath, err)
	}
	defer f.Close()

	var results []T
	for {
		// Read the 4-byte little-endian document length prefix.
		var sizeBuf [4]byte
		_, err := io.ReadFull(f, sizeBuf[:])
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading BSON document size from %s: %w", filePath, err)
		}

		docSize := int32(sizeBuf[0]) | int32(sizeBuf[1])<<8 | int32(sizeBuf[2])<<16 | int32(sizeBuf[3])<<24
		if docSize < 5 {
			return nil, fmt.Errorf("invalid BSON document size %d in %s", docSize, filePath)
		}

		// Read the rest of the document (we already read 4 bytes of the header).
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
	uploadsPath := filepath.Join(dumpDir, "rocketchat_uploads.bson")
	if _, err := os.Stat(uploadsPath); err == nil {
		uploads, err = readBSONFile[RocketChatUpload](uploadsPath)
		if err != nil {
			return nil, fmt.Errorf("parsing uploads: %w", err)
		}
	}

	// Build lookup maps.
	usersByID := make(map[string]*RocketChatUser, len(users))
	for i := range users {
		usersByID[users[i].ID] = &users[i]
	}

	roomsByID := make(map[string]*RocketChatRoom, len(rooms))
	for i := range rooms {
		roomsByID[rooms[i].ID] = &rooms[i]
	}

	messagesByRoomID := make(map[string][]*RocketChatMessage)
	for i := range messages {
		rid := messages[i].RoomID
		messagesByRoomID[rid] = append(messagesByRoomID[rid], &messages[i])
	}

	subscriptionsByRoomID := make(map[string][]*RocketChatSubscription)
	for i := range subscriptions {
		rid := subscriptions[i].RoomID
		subscriptionsByRoomID[rid] = append(subscriptionsByRoomID[rid], &subscriptions[i])
	}

	uploadsByID := make(map[string]*RocketChatUpload, len(uploads))
	for i := range uploads {
		uploadsByID[uploads[i].ID] = &uploads[i]
	}

	logger.Infof("Parsed %d users, %d rooms, %d messages, %d subscriptions, %d uploads",
		len(users), len(rooms), len(messages), len(subscriptions), len(uploads))

	return &ParsedData{
		UsersByID:             usersByID,
		RoomsByID:             roomsByID,
		MessagesByRoomID:      messagesByRoomID,
		SubscriptionsByRoomID: subscriptionsByRoomID,
		UploadsByID:           uploadsByID,
	}, nil
}
