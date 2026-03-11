package commands_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson"

	"github.com/mattermost/mmetl/commands"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// marshalBSONFileCmds serialises a slice of documents into a concatenated BSON
// file (the format produced by mongodump) and writes it to filePath.
func marshalBSONFileCmds(t *testing.T, filePath string, docs []any) {
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

// rcBSONUser is a minimal BSON-serialisable struct mirroring RocketChatUser.
// Using a local struct avoids importing the rocketchat package (which would
// create a dependency cycle through commands → rocketchat in test code).
type rcBSONUser struct {
	ID       string   `bson:"_id"`
	Username string   `bson:"username"`
	Name     string   `bson:"name"`
	Emails   []rcMail `bson:"emails"`
	Active   bool     `bson:"active"`
	Roles    []string `bson:"roles"`
	Type     string   `bson:"type"`
}

type rcMail struct {
	Address  string `bson:"address"`
	Verified bool   `bson:"verified"`
}

type rcRoom struct {
	ID        string   `bson:"_id"`
	Type      string   `bson:"t"`
	Name      string   `bson:"name"`
	FName     string   `bson:"fname"`
	Usernames []string `bson:"usernames"`
	UIDs      []string `bson:"uids"`
}

type rcMessage struct {
	ID        string    `bson:"_id"`
	RoomID    string    `bson:"rid"`
	User      rcMsgUser `bson:"u"`
	Message   string    `bson:"msg"`
	Timestamp time.Time `bson:"ts"`
	ThreadID  string    `bson:"tmid"`
}

type rcMsgUser struct {
	ID       string `bson:"_id"`
	Username string `bson:"username"`
}

type rcSubscription struct {
	RoomID string    `bson:"rid"`
	User   rcMsgUser `bson:"u"`
}

func writeDumpDir(t *testing.T, dir string, users []any, rooms []any, messages []any, subs []any) {
	t.Helper()
	marshalBSONFileCmds(t, filepath.Join(dir, "users.bson"), users)
	marshalBSONFileCmds(t, filepath.Join(dir, "rocketchat_room.bson"), rooms)
	marshalBSONFileCmds(t, filepath.Join(dir, "rocketchat_message.bson"), messages)
	marshalBSONFileCmds(t, filepath.Join(dir, "rocketchat_subscription.bson"), subs)
}

func TestTransformRocketChatE2E(t *testing.T) {
	ts1 := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	ts2 := time.Date(2024, 1, 1, 12, 1, 0, 0, time.UTC)
	ts3 := time.Date(2024, 1, 1, 12, 2, 0, 0, time.UTC)

	defaultUsers := []any{
		rcBSONUser{
			ID:       "u1",
			Username: "johndoe",
			Name:     "John Doe",
			Emails:   []rcMail{{Address: "john@example.com", Verified: true}},
			Active:   true,
			Roles:    []string{"user"},
			Type:     "user",
		},
		rcBSONUser{
			ID:       "u2",
			Username: "janesmith",
			Name:     "Jane Smith",
			Emails:   []rcMail{{Address: "jane@example.com", Verified: true}},
			Active:   true,
			Roles:    []string{"user"},
			Type:     "user",
		},
	}

	defaultRooms := []any{
		rcRoom{ID: "r1", Type: "c", Name: "general", FName: "General"},
		rcRoom{ID: "r2", Type: "p", Name: "private-stuff", FName: "Private Stuff"},
		rcRoom{
			ID:        "r3",
			Type:      "d",
			Usernames: []string{"johndoe", "janesmith"},
			UIDs:      []string{"u1", "u2"},
		},
	}

	defaultMessages := []any{
		rcMessage{ID: "m1", RoomID: "r1", User: rcMsgUser{ID: "u1", Username: "johndoe"}, Message: "Hello world", Timestamp: ts1},
		rcMessage{ID: "m2", RoomID: "r1", User: rcMsgUser{ID: "u2", Username: "janesmith"}, Message: "Hi there", Timestamp: ts2},
		rcMessage{ID: "m3", RoomID: "r3", User: rcMsgUser{ID: "u1", Username: "johndoe"}, Message: "Direct message", Timestamp: ts3},
	}

	defaultSubs := []any{
		rcSubscription{RoomID: "r1", User: rcMsgUser{ID: "u1", Username: "johndoe"}},
		rcSubscription{RoomID: "r1", User: rcMsgUser{ID: "u2", Username: "janesmith"}},
		rcSubscription{RoomID: "r2", User: rcMsgUser{ID: "u1", Username: "johndoe"}},
	}

	t.Run("valid export produces correct JSONL", func(t *testing.T) {
		dir := t.TempDir()
		outputPath := filepath.Join(dir, "output.jsonl")

		writeDumpDir(t, dir, defaultUsers, defaultRooms, defaultMessages, defaultSubs)

		defer os.Remove("transform-rocketchat.log")

		c := commands.RootCmd
		c.SetArgs([]string{
			"transform", "rocketchat",
			"--team", "testteam",
			"--dump-dir", dir,
			"--output", outputPath,
			"--skip-attachments",
		})
		err := c.Execute()
		require.NoError(t, err)

		data, err := os.ReadFile(outputPath)
		require.NoError(t, err)

		lines := splitJSONLLines(t, data)
		require.NotEmpty(t, lines)

		// First line must be version
		assertJSONField(t, lines[0], "type", "version")
		assert.Equal(t, float64(1), lines[0]["version"])

		// Public channel (general)
		channelLines := findLinesByType(lines, "channel")
		require.NotEmpty(t, channelLines)
		generalCh := findChannelByName(channelLines, "general")
		require.NotNil(t, generalCh, "expected channel 'general'")
		assert.Equal(t, "O", generalCh["type"])
		assert.Equal(t, "General", generalCh["display_name"])

		// Private channel (private-stuff)
		privateCh := findChannelByName(channelLines, "private-stuff")
		require.NotNil(t, privateCh, "expected channel 'private-stuff'")
		assert.Equal(t, "P", privateCh["type"])

		// User lines
		userLines := findLinesByType(lines, "user")
		require.Len(t, userLines, 2)
		john := findUserByUsername(userLines, "johndoe")
		require.NotNil(t, john, "expected user 'johndoe'")
		assert.Equal(t, "john@example.com", john["email"])
		assert.Equal(t, "John", john["first_name"])
		assert.Equal(t, "Doe", john["last_name"])
		// johndoe is subscribed to r1 (general) and r2 (private-stuff) but not r3 (DM)
		teams := john["teams"].([]any)
		require.Len(t, teams, 1)
		teamEntry := teams[0].(map[string]any)
		assert.Equal(t, "testteam", teamEntry["name"])
		channels := teamEntry["channels"].([]any)
		require.Len(t, channels, 2)

		// Direct channel line
		directChLines := findLinesByType(lines, "direct_channel")
		require.NotEmpty(t, directChLines, "expected at least one direct_channel line")
		// r3 has usernames johndoe and janesmith
		dmLine := findDirectChannelWithMembers(directChLines, []string{"johndoe", "janesmith"})
		require.NotNil(t, dmLine, "expected direct_channel with johndoe and janesmith")

		// Post lines (non-direct)
		postLines := findLinesByType(lines, "post")
		require.Len(t, postLines, 2)
		msgs := collectMessages(postLines)
		assert.Contains(t, msgs, "Hello world")
		assert.Contains(t, msgs, "Hi there")

		// Direct post line
		directPostLines := findLinesByType(lines, "direct_post")
		require.Len(t, directPostLines, 1)
		dp := directPostLines[0]
		assert.Equal(t, "Direct message", dp["message"])
	})

	t.Run("team name uppercase is lowercased", func(t *testing.T) {
		dir := t.TempDir()
		outputPath := filepath.Join(dir, "output.jsonl")
		writeDumpDir(t, dir, defaultUsers, defaultRooms, defaultMessages, defaultSubs)
		defer os.Remove("transform-rocketchat.log")

		c := commands.RootCmd
		c.SetArgs([]string{
			"transform", "rocketchat",
			"--team", "TestTeam",
			"--dump-dir", dir,
			"--output", outputPath,
			"--skip-attachments",
		})
		require.NoError(t, c.Execute())

		data, err := os.ReadFile(outputPath)
		require.NoError(t, err)
		lines := splitJSONLLines(t, data)

		// Team name should be lowercased in channel records.
		channelLines := findLinesByType(lines, "channel")
		require.NotEmpty(t, channelLines)
		assert.Equal(t, "testteam", channelLines[0]["team"])
	})

	t.Run("thread replies nested under parent post", func(t *testing.T) {
		dir := t.TempDir()
		outputPath := filepath.Join(dir, "output.jsonl")

		// root message m1 and a reply m2 (tmid = m1)
		replyMsg := rcMessage{
			ID:        "m2",
			RoomID:    "r1",
			User:      rcMsgUser{ID: "u2", Username: "janesmith"},
			Message:   "This is a reply",
			Timestamp: ts2,
			ThreadID:  "m1",
		}
		rootMsg := rcMessage{
			ID:        "m1",
			RoomID:    "r1",
			User:      rcMsgUser{ID: "u1", Username: "johndoe"},
			Message:   "Root message",
			Timestamp: ts1,
		}
		rooms := []any{rcRoom{ID: "r1", Type: "c", Name: "general", FName: "General"}}
		subs := []any{
			rcSubscription{RoomID: "r1", User: rcMsgUser{ID: "u1", Username: "johndoe"}},
			rcSubscription{RoomID: "r1", User: rcMsgUser{ID: "u2", Username: "janesmith"}},
		}
		writeDumpDir(t, dir, defaultUsers, rooms, []any{rootMsg, replyMsg}, subs)
		defer os.Remove("transform-rocketchat.log")

		c := commands.RootCmd
		c.SetArgs([]string{
			"transform", "rocketchat",
			"--team", "testteam",
			"--dump-dir", dir,
			"--output", outputPath,
			"--skip-attachments",
		})
		require.NoError(t, c.Execute())

		data, err := os.ReadFile(outputPath)
		require.NoError(t, err)
		lines := splitJSONLLines(t, data)

		postLines := findLinesByType(lines, "post")
		require.Len(t, postLines, 1, "only root post should be a top-level post line")

		post := postLines[0]
		assert.Equal(t, "Root message", post["message"])

		replies, ok := post["replies"].([]any)
		require.True(t, ok, "expected replies array on root post")
		require.Len(t, replies, 1)
		reply := replies[0].(map[string]any)
		assert.Equal(t, "This is a reply", reply["message"])
		assert.Equal(t, "janesmith", reply["user"])
	})
}

// ---------------------------------------------------------------------------
// Edge case tests (Phase 5.2)
// ---------------------------------------------------------------------------

func TestTransformRocketChatEdgeCases(t *testing.T) {
	t.Run("empty collections produce minimal JSONL", func(t *testing.T) {
		dir := t.TempDir()
		outputPath := filepath.Join(dir, "output.jsonl")
		writeDumpDir(t, dir, []any{}, []any{}, []any{}, []any{})
		defer os.Remove("transform-rocketchat.log")

		c := commands.RootCmd
		c.SetArgs([]string{
			"transform", "rocketchat",
			"--team", "testteam",
			"--dump-dir", dir,
			"--output", outputPath,
			"--skip-attachments",
		})
		require.NoError(t, c.Execute())

		data, err := os.ReadFile(outputPath)
		require.NoError(t, err)
		lines := splitJSONLLines(t, data)

		// Must have only the version line, nothing else
		require.Len(t, lines, 1)
		assertJSONField(t, lines[0], "type", "version")
	})

	t.Run("message with username not in UsersById uses username from message", func(t *testing.T) {
		dir := t.TempDir()
		outputPath := filepath.Join(dir, "output.jsonl")

		users := []any{
			rcBSONUser{ID: "u1", Username: "alice", Emails: []rcMail{{Address: "a@a.com"}}, Active: true, Type: "user"},
		}
		rooms := []any{rcRoom{ID: "r1", Type: "c", Name: "general", FName: "General"}}
		// "ghost" user ID and username is not in the users collection.
		// The transformer uses the username from the message directly.
		messages := []any{
			rcMessage{ID: "m1", RoomID: "r1", User: rcMsgUser{ID: "ghost", Username: "ghost"}, Message: "ghost message", Timestamp: time.Now()},
			rcMessage{ID: "m2", RoomID: "r1", User: rcMsgUser{ID: "u1", Username: "alice"}, Message: "real message", Timestamp: time.Now()},
		}
		subs := []any{rcSubscription{RoomID: "r1", User: rcMsgUser{ID: "u1", Username: "alice"}}}
		writeDumpDir(t, dir, users, rooms, messages, subs)
		defer os.Remove("transform-rocketchat.log")

		c := commands.RootCmd
		c.SetArgs([]string{
			"transform", "rocketchat",
			"--team", "testteam",
			"--dump-dir", dir,
			"--output", outputPath,
			"--skip-attachments",
		})
		require.NoError(t, c.Execute())

		data, err := os.ReadFile(outputPath)
		require.NoError(t, err)
		lines := splitJSONLLines(t, data)

		postLines := findLinesByType(lines, "post")
		msgs := collectMessages(postLines)
		// Both messages should appear — the ghost user's username is taken from the message.
		assert.Contains(t, msgs, "ghost message")
		assert.Contains(t, msgs, "real message")
	})

	t.Run("encrypted room is skipped", func(t *testing.T) {
		dir := t.TempDir()
		outputPath := filepath.Join(dir, "output.jsonl")

		rooms := []any{
			rcRoom{ID: "r1", Type: "c", Name: "general", FName: "General"},
		}
		// Encrypted rooms cannot be represented with the simple rcRoom struct (no Encrypted field).
		// Use a raw bson.D to include the encrypted: true field.
		encryptedRoom := bson.D{
			{Key: "_id", Value: "r2"},
			{Key: "t", Value: "c"},
			{Key: "name", Value: "encrypted-channel"},
			{Key: "fname", Value: "Encrypted Channel"},
			{Key: "encrypted", Value: true},
		}
		users := []any{
			rcBSONUser{ID: "u1", Username: "alice", Emails: []rcMail{{Address: "a@a.com"}}, Active: true, Type: "user"},
		}
		subs := []any{rcSubscription{RoomID: "r1", User: rcMsgUser{ID: "u1", Username: "alice"}}}
		marshalBSONFileCmds(t, filepath.Join(dir, "users.bson"), users)
		marshalBSONFileCmds(t, filepath.Join(dir, "rocketchat_room.bson"), append(rooms, encryptedRoom))
		marshalBSONFileCmds(t, filepath.Join(dir, "rocketchat_message.bson"), []any{})
		marshalBSONFileCmds(t, filepath.Join(dir, "rocketchat_subscription.bson"), subs)
		defer os.Remove("transform-rocketchat.log")

		c := commands.RootCmd
		c.SetArgs([]string{
			"transform", "rocketchat",
			"--team", "testteam",
			"--dump-dir", dir,
			"--output", outputPath,
			"--skip-attachments",
		})
		require.NoError(t, c.Execute())

		data, err := os.ReadFile(outputPath)
		require.NoError(t, err)
		lines := splitJSONLLines(t, data)

		chLines := findLinesByType(lines, "channel")
		names := make([]string, 0, len(chLines))
		for _, ch := range chLines {
			if n, ok := ch["name"].(string); ok {
				names = append(names, n)
			}
		}
		assert.Contains(t, names, "general")
		assert.NotContains(t, names, "encrypted-channel")
	})

	t.Run("discussion room is skipped", func(t *testing.T) {
		dir := t.TempDir()
		outputPath := filepath.Join(dir, "output.jsonl")

		regularRoom := bson.D{
			{Key: "_id", Value: "r1"},
			{Key: "t", Value: "c"},
			{Key: "name", Value: "general"},
			{Key: "fname", Value: "General"},
		}
		discussionRoom := bson.D{
			{Key: "_id", Value: "r2"},
			{Key: "t", Value: "p"},
			{Key: "name", Value: "discussion-room"},
			{Key: "fname", Value: "Discussion Room"},
			{Key: "prid", Value: "r1"}, // marks it as a discussion
		}
		users := []any{
			rcBSONUser{ID: "u1", Username: "alice", Emails: []rcMail{{Address: "a@a.com"}}, Active: true, Type: "user"},
		}
		subs := []any{rcSubscription{RoomID: "r1", User: rcMsgUser{ID: "u1", Username: "alice"}}}
		marshalBSONFileCmds(t, filepath.Join(dir, "users.bson"), users)
		marshalBSONFileCmds(t, filepath.Join(dir, "rocketchat_room.bson"), []any{regularRoom, discussionRoom})
		marshalBSONFileCmds(t, filepath.Join(dir, "rocketchat_message.bson"), []any{})
		marshalBSONFileCmds(t, filepath.Join(dir, "rocketchat_subscription.bson"), subs)
		defer os.Remove("transform-rocketchat.log")

		c := commands.RootCmd
		c.SetArgs([]string{
			"transform", "rocketchat",
			"--team", "testteam",
			"--dump-dir", dir,
			"--output", outputPath,
			"--skip-attachments",
		})
		require.NoError(t, c.Execute())

		data, err := os.ReadFile(outputPath)
		require.NoError(t, err)
		lines := splitJSONLLines(t, data)

		chLines := findLinesByType(lines, "channel")
		names := make([]string, 0, len(chLines))
		for _, ch := range chLines {
			if n, ok := ch["name"].(string); ok {
				names = append(names, n)
			}
		}
		assert.Contains(t, names, "general")
		assert.NotContains(t, names, "discussion-room")
	})

	t.Run("user with no email and default-email-domain generates email", func(t *testing.T) {
		dir := t.TempDir()
		outputPath := filepath.Join(dir, "output.jsonl")

		users := []any{
			rcBSONUser{ID: "u1", Username: "noemail", Name: "No Email", Active: true, Type: "user"},
		}
		writeDumpDir(t, dir, users, []any{}, []any{}, []any{})
		defer os.Remove("transform-rocketchat.log")

		c := commands.RootCmd
		c.SetArgs([]string{
			"transform", "rocketchat",
			"--team", "testteam",
			"--dump-dir", dir,
			"--output", outputPath,
			"--skip-attachments",
			"--default-email-domain", "myorg.com",
		})
		require.NoError(t, c.Execute())

		data, err := os.ReadFile(outputPath)
		require.NoError(t, err)
		lines := splitJSONLLines(t, data)

		userLines := findLinesByType(lines, "user")
		require.Len(t, userLines, 1)
		assert.Equal(t, "noemail@myorg.com", userLines[0]["email"])
	})
}

// --- helpers ---

func splitJSONLLines(t *testing.T, data []byte) []map[string]any {
	t.Helper()
	var lines []map[string]any
	for _, raw := range bytes.Split(bytes.TrimRight(data, "\n"), []byte("\n")) {
		if len(raw) == 0 {
			continue
		}
		var m map[string]any
		require.NoError(t, json.Unmarshal(raw, &m), "invalid JSON line: %s", string(raw))
		lines = append(lines, m)
	}
	return lines
}

func assertJSONField(t *testing.T, m map[string]any, key string, expected any) {
	t.Helper()
	assert.Equal(t, expected, m[key])
}

func findLinesByType(lines []map[string]any, typeName string) []map[string]any {
	var result []map[string]any
	for _, l := range lines {
		if l["type"] == typeName {
			inner, ok := l[typeName].(map[string]any)
			if ok {
				result = append(result, inner)
			}
		}
	}
	return result
}

func findChannelByName(channelLines []map[string]any, name string) map[string]any {
	for _, ch := range channelLines {
		if ch["name"] == name {
			return ch
		}
	}
	return nil
}

func findUserByUsername(userLines []map[string]any, username string) map[string]any {
	for _, u := range userLines {
		if u["username"] == username {
			return u
		}
	}
	return nil
}

func findDirectChannelWithMembers(dcLines []map[string]any, members []string) map[string]any {
	memberSet := make(map[string]bool)
	for _, m := range members {
		memberSet[m] = true
	}
	for _, dc := range dcLines {
		rawMembers, ok := dc["members"].([]any)
		if !ok {
			continue
		}
		if len(rawMembers) != len(members) {
			continue
		}
		match := true
		for _, rm := range rawMembers {
			if s, ok := rm.(string); !ok || !memberSet[s] {
				match = false
				break
			}
		}
		if match {
			return dc
		}
	}
	return nil
}

func collectMessages(postLines []map[string]any) []string {
	var msgs []string
	for _, p := range postLines {
		if m, ok := p["message"].(string); ok {
			msgs = append(msgs, m)
		}
	}
	return msgs
}
