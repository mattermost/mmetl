package rocketchat

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson"

	log "github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// marshalBSONFile serialises a slice of documents into a concatenated BSON file
// (the format produced by mongodump) and writes it to filePath.
func marshalBSONFile(t *testing.T, filePath string, docs []any) {
	t.Helper()
	var buf bytes.Buffer
	for _, doc := range docs {
		b, err := bson.Marshal(doc)
		require.NoError(t, err)
		_, err = buf.Write(b)
		require.NoError(t, err)
	}
	err := os.WriteFile(filePath, buf.Bytes(), 0600)
	require.NoError(t, err)
}

func TestReadBSONFile(t *testing.T) {
	t.Run("deserialises single document", func(t *testing.T) {
		dir := t.TempDir()
		filePath := filepath.Join(dir, "test.bson")

		expected := RocketChatUser{
			ID:       "user1",
			Username: "alice",
			Name:     "Alice Wonderland",
			Active:   true,
			Roles:    []string{"user"},
			Type:     "user",
		}

		marshalBSONFile(t, filePath, []any{expected})

		results, err := readBSONFile[RocketChatUser](filePath)
		require.NoError(t, err)
		require.Len(t, results, 1)
		assert.Equal(t, "user1", results[0].ID)
		assert.Equal(t, "alice", results[0].Username)
		assert.Equal(t, "Alice Wonderland", results[0].Name)
		assert.True(t, results[0].Active)
	})

	t.Run("deserialises multiple documents", func(t *testing.T) {
		dir := t.TempDir()
		filePath := filepath.Join(dir, "users.bson")

		docs := []any{
			RocketChatUser{ID: "u1", Username: "alice"},
			RocketChatUser{ID: "u2", Username: "bob"},
			RocketChatUser{ID: "u3", Username: "carol"},
		}
		marshalBSONFile(t, filePath, docs)

		results, err := readBSONFile[RocketChatUser](filePath)
		require.NoError(t, err)
		require.Len(t, results, 3)
		assert.Equal(t, "u1", results[0].ID)
		assert.Equal(t, "u2", results[1].ID)
		assert.Equal(t, "u3", results[2].ID)
	})

	t.Run("returns empty slice for empty file", func(t *testing.T) {
		dir := t.TempDir()
		filePath := filepath.Join(dir, "empty.bson")
		require.NoError(t, os.WriteFile(filePath, []byte{}, 0600))

		results, err := readBSONFile[RocketChatUser](filePath)
		require.NoError(t, err)
		assert.Empty(t, results)
	})

	t.Run("returns error for non-existent file", func(t *testing.T) {
		_, err := readBSONFile[RocketChatUser]("/does/not/exist.bson")
		require.Error(t, err)
	})
}

func TestParseDump(t *testing.T) {
	t.Run("parses all required collections", func(t *testing.T) {
		dir := t.TempDir()
		logger := log.New()
		logger.SetOutput(os.Stderr)

		desc := "test room"
		// Write users.bson
		marshalBSONFile(t, filepath.Join(dir, "users.bson"), []any{
			RocketChatUser{ID: "u1", Username: "alice", Active: true},
			RocketChatUser{ID: "u2", Username: "bob", Active: false},
		})

		// Write rocketchat_room.bson
		marshalBSONFile(t, filepath.Join(dir, "rocketchat_room.bson"), []any{
			RocketChatRoom{ID: "r1", Type: "c", Name: "general", Description: &desc},
		})

		// Write rocketchat_message.bson
		ts := time.Now()
		marshalBSONFile(t, filepath.Join(dir, "rocketchat_message.bson"), []any{
			RocketChatMessage{ID: "m1", RoomID: "r1", User: RCMessageUser{ID: "u1", Username: "alice"}, Message: "hello", Timestamp: ts},
			RocketChatMessage{ID: "m2", RoomID: "r1", User: RCMessageUser{ID: "u2", Username: "bob"}, Message: "world", Timestamp: ts},
		})

		// Write rocketchat_subscription.bson
		marshalBSONFile(t, filepath.Join(dir, "rocketchat_subscription.bson"), []any{
			RocketChatSubscription{RoomID: "r1", User: RCMessageUser{ID: "u1", Username: "alice"}},
		})

		parsed, err := ParseDump(dir, logger)
		require.NoError(t, err)

		assert.Len(t, parsed.Users, 2)
		assert.Len(t, parsed.Rooms, 1)
		assert.Len(t, parsed.Messages, 2)
		assert.Len(t, parsed.Subscriptions, 1)
		assert.Empty(t, parsed.UploadsByID)

		assert.Equal(t, "alice", parsed.Users[0].Username)
		assert.Equal(t, "general", parsed.Rooms[0].Name)
	})

	t.Run("parses optional uploads collection when present", func(t *testing.T) {
		dir := t.TempDir()
		logger := log.New()
		logger.SetOutput(os.Stderr)

		marshalBSONFile(t, filepath.Join(dir, "users.bson"), []any{
			RocketChatUser{ID: "u1", Username: "alice"},
		})
		marshalBSONFile(t, filepath.Join(dir, "rocketchat_room.bson"), []any{
			RocketChatRoom{ID: "r1", Type: "c", Name: "general"},
		})
		marshalBSONFile(t, filepath.Join(dir, "rocketchat_message.bson"), []any{})
		marshalBSONFile(t, filepath.Join(dir, "rocketchat_subscription.bson"), []any{})
		marshalBSONFile(t, filepath.Join(dir, "rocketchat_uploads.bson"), []any{
			RocketChatUpload{ID: "up1", Name: "photo.png", Type: "image/png", Complete: true},
		})

		parsed, err := ParseDump(dir, logger)
		require.NoError(t, err)
		assert.Len(t, parsed.UploadsByID, 1)
		assert.Equal(t, "photo.png", parsed.UploadsByID["up1"].Name)
	})

	t.Run("returns error when required file missing", func(t *testing.T) {
		dir := t.TempDir()
		logger := log.New()
		// Only write users.bson, omit the rest
		marshalBSONFile(t, filepath.Join(dir, "users.bson"), []any{})

		_, err := ParseDump(dir, logger)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "rocketchat_room.bson")
	})
}
