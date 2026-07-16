package commands_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mattermost/mattermost/server/public/model"
	"go.mongodb.org/mongo-driver/bson"

	"github.com/mattermost/mmetl/commands"
	"github.com/mattermost/mmetl/testhelper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// resetRCFlags resets all Cobra flags to their defaults before each subtest so
// that flag state does not leak between tests when reusing the global RootCmd.
// (resetCobraFlags is defined in transform_slack_e2e_test.go and available here
// since both files share the commands_test package.)
func resetRCFlags() {
	resetCobraFlags(commands.RootCmd)
}

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
	ID          string   `bson:"_id"`
	Type        string   `bson:"t"`
	Name        string   `bson:"name"`
	FName       string   `bson:"fname"`
	Description *string  `bson:"description"`
	Topic       string   `bson:"topic"`
	Usernames   []string `bson:"usernames"`
	UIDs        []string `bson:"uids"`
}

type rcMessage struct {
	ID        string                    `bson:"_id"`
	RoomID    string                    `bson:"rid"`
	User      rcMsgUser                 `bson:"u"`
	Message   string                    `bson:"msg"`
	Timestamp time.Time                 `bson:"ts"`
	ThreadID  string                    `bson:"tmid"`
	Reactions map[string]rcReactionInfo `bson:"reactions"`
}

type rcReactionInfo struct {
	Usernames []string `bson:"usernames"`
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

// TestTransformRocketChatImportE2E exercises the documented migration path:
// mongodump-shaped BSON -> mmetl transform -> Mattermost bulk import -> API checks.
func TestTransformRocketChatImportE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	th := testhelper.SetupHelper(t)
	ctx := context.Background()
	dir := t.TempDir()
	outputPath := filepath.Join(dir, "mattermost_import.jsonl")
	teamName := uniqueTeamName("rce2e")
	t.Cleanup(func() { os.Remove("transform-rocketchat.log") })

	description := "Coordination for the engineering team"
	users := []any{
		rcBSONUser{ID: "alice-id", Username: "alice", Name: "Alice Anderson", Emails: []rcMail{{Address: "alice@example.com", Verified: true}}, Active: true, Roles: []string{"user"}, Type: "user"},
		rcBSONUser{ID: "bob-id", Username: "bob", Name: "Bob Brown", Emails: []rcMail{{Address: "bob@example.com", Verified: true}}, Active: true, Roles: []string{"user"}, Type: "user"},
		rcBSONUser{ID: "carol-id", Username: "carol", Name: "Carol Clark", Emails: []rcMail{{Address: "carol@example.com", Verified: true}}, Active: true, Roles: []string{"user"}, Type: "user"},
	}
	rooms := []any{
		rcRoom{ID: "engineering-id", Type: "c", Name: "engineering", FName: "Engineering", Description: &description, Topic: "Sprint planning and code review"},
		rcRoom{ID: "secret-id", Type: "p", Name: "secret-project", FName: "Secret Project"},
		rcRoom{ID: "alice-bob-dm", Type: "d", Usernames: []string{"alice", "bob"}, UIDs: []string{"alice-id", "bob-id"}},
	}
	baseTime := time.Date(2024, 1, 2, 9, 30, 0, 0, time.UTC)
	messages := []any{
		rcMessage{
			ID: "engineering-root", RoomID: "engineering-id", User: rcMsgUser{ID: "alice-id", Username: "alice"},
			Message: "Morning all! Kicking off the sprint", Timestamp: baseTime,
			Reactions: map[string]rcReactionInfo{":thumbsup:": {Usernames: []string{"bob"}}},
		},
		rcMessage{ID: "engineering-reply", RoomID: "engineering-id", User: rcMsgUser{ID: "bob-id", Username: "bob"}, Message: "I can review the migration PR", Timestamp: baseTime.Add(time.Minute), ThreadID: "engineering-root"},
		rcMessage{ID: "secret-root", RoomID: "secret-id", User: rcMsgUser{ID: "carol-id", Username: "carol"}, Message: "Confidential prototype update", Timestamp: baseTime.Add(2 * time.Minute)},
		rcMessage{ID: "dm-root", RoomID: "alice-bob-dm", User: rcMsgUser{ID: "alice-id", Username: "alice"}, Message: "Can we sync on the migration?", Timestamp: baseTime.Add(3 * time.Minute)},
	}
	subscriptions := []any{
		rcSubscription{RoomID: "engineering-id", User: rcMsgUser{ID: "alice-id", Username: "alice"}},
		rcSubscription{RoomID: "engineering-id", User: rcMsgUser{ID: "bob-id", Username: "bob"}},
		rcSubscription{RoomID: "secret-id", User: rcMsgUser{ID: "alice-id", Username: "alice"}},
		rcSubscription{RoomID: "secret-id", User: rcMsgUser{ID: "carol-id", Username: "carol"}},
	}
	writeDumpDir(t, dir, users, rooms, messages, subscriptions)

	team := th.CreateTeam(ctx, teamName, "RocketChat E2E Team")
	resetRCFlags()
	commands.RootCmd.SetArgs([]string{
		"transform", "rocketchat",
		"--team", teamName,
		"--dump-dir", dir,
		"--output", outputPath,
		"--skip-attachments",
	})
	require.NoError(t, commands.RootCmd.Execute())

	validation := th.ValidateImportFileOrFail(ctx, outputPath)
	assert.Equal(t, uint64(3), validation.UserCount)
	assert.Equal(t, uint64(2), validation.ChannelCount)
	assert.Equal(t, uint64(2), validation.PostCount)
	assert.Equal(t, uint64(1), validation.DirectPostCount)
	require.NoError(t, th.ImportBulkData(ctx, outputPath))

	alice := th.AssertUserExists(ctx, "alice")
	bob := th.AssertUserExists(ctx, "bob")
	carol := th.AssertUserExists(ctx, "carol")
	assert.Equal(t, "alice@example.com", alice.Email)
	assert.Equal(t, "Alice", alice.FirstName)
	assert.Equal(t, "Anderson", alice.LastName)
	th.AssertUserInTeam(ctx, team.Id, alice.Id)
	th.AssertUserInTeam(ctx, team.Id, bob.Id)
	th.AssertUserInTeam(ctx, team.Id, carol.Id)

	engineering := th.AssertChannelExists(ctx, teamName, "engineering")
	assert.Equal(t, model.ChannelTypeOpen, engineering.Type)
	assert.Equal(t, description, engineering.Purpose)
	assert.Equal(t, "Sprint planning and code review", engineering.Header)
	assertChannelMembers(t, th, ctx, engineering.Id, []string{alice.Id, bob.Id}, []string{carol.Id})

	secret := th.AssertChannelExists(ctx, teamName, "secret-project")
	assert.Equal(t, model.ChannelTypePrivate, secret.Type)
	assertChannelMembers(t, th, ctx, secret.Id, []string{alice.Id, carol.Id}, []string{bob.Id})
	secretPosts, err := th.GetChannelPosts(ctx, secret.Id, 0, 100)
	require.NoError(t, err)
	require.NotNil(t, findPostByMessage(secretPosts, "Confidential prototype update"))

	engineeringPosts, err := th.GetChannelPosts(ctx, engineering.Id, 0, 100)
	require.NoError(t, err)
	root := findPostByMessage(engineeringPosts, "Morning all! Kicking off the sprint")
	require.NotNil(t, root)
	reply := findPostByMessage(engineeringPosts, "I can review the migration PR")
	require.NotNil(t, reply)
	assert.Equal(t, root.Id, reply.RootId)
	reactions, _, err := th.Client.GetReactions(ctx, root.Id)
	require.NoError(t, err)
	require.Len(t, reactions, 1)
	assert.Equal(t, bob.Id, reactions[0].UserId)
	assert.Equal(t, "thumbsup", reactions[0].EmojiName)

	dm, _, err := th.Client.CreateDirectChannel(ctx, alice.Id, bob.Id)
	require.NoError(t, err)
	dmPosts, err := th.GetChannelPosts(ctx, dm.Id, 0, 100)
	require.NoError(t, err)
	require.NotNil(t, findPostByMessage(dmPosts, "Can we sync on the migration?"))
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
		resetRCFlags()
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
		resetRCFlags()
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
		resetRCFlags()
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
		resetRCFlags()
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
		resetRCFlags()
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
		resetRCFlags()
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

	t.Run("user with no email and default-email-domain generates email", func(t *testing.T) {
		dir := t.TempDir()
		outputPath := filepath.Join(dir, "output.jsonl")

		users := []any{
			rcBSONUser{ID: "u1", Username: "noemail", Name: "No Email", Active: true, Type: "user"},
		}
		writeDumpDir(t, dir, users, []any{}, []any{}, []any{})
		defer os.Remove("transform-rocketchat.log")

		c := commands.RootCmd
		resetRCFlags()
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

func findPostByMessage(posts *model.PostList, message string) *model.Post {
	if posts == nil {
		return nil
	}
	for _, post := range posts.Posts {
		if post.Message == message {
			return post
		}
	}
	return nil
}

func assertChannelMembers(t *testing.T, th *testhelper.TestHelper, ctx context.Context, channelID string, expected, unexpected []string) {
	t.Helper()
	members, err := th.GetChannelMembers(ctx, channelID)
	require.NoError(t, err)

	memberIDs := make(map[string]struct{}, len(members))
	for _, member := range members {
		memberIDs[member.UserId] = struct{}{}
	}
	for _, userID := range expected {
		assert.Contains(t, memberIDs, userID)
	}
	for _, userID := range unexpected {
		assert.NotContains(t, memberIDs, userID)
	}
}
