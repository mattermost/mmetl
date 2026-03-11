package rocketchat

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/v8/channels/app/imports"

	"github.com/mattermost/mmetl/services/intermediate"
)

func newExportLogger() log.FieldLogger {
	l := log.New()
	l.SetOutput(os.Stderr)
	return l
}

// readLines splits a JSONL buffer into individual JSON objects.
func readLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var result []map[string]any
	dec := json.NewDecoder(buf)
	for dec.More() {
		var obj map[string]any
		require.NoError(t, dec.Decode(&obj))
		result = append(result, obj)
	}
	return result
}

func TestExportVersion(t *testing.T) {
	tr := NewTransformer("myteam", newExportLogger())
	var buf bytes.Buffer
	require.NoError(t, tr.ExportVersion(&buf))

	lines := readLines(t, &buf)
	require.Len(t, lines, 1)
	assert.Equal(t, "version", lines[0]["type"])
	assert.Equal(t, float64(1), lines[0]["version"])
}

func TestExportChannels(t *testing.T) {
	tr := NewTransformer("myteam", newExportLogger())
	channels := []*intermediate.IntermediateChannel{
		{Name: "general", DisplayName: "General", Type: model.ChannelTypeOpen},
		{Name: "random", DisplayName: "Random", Type: model.ChannelTypeOpen},
	}
	var buf bytes.Buffer
	require.NoError(t, tr.ExportChannels(channels, &buf))

	lines := readLines(t, &buf)
	require.Len(t, lines, 2)
	for _, line := range lines {
		assert.Equal(t, "channel", line["type"])
	}
}

func TestExportUsers(t *testing.T) {
	tr := NewTransformer("myteam", newExportLogger())
	tr.Intermediate.UsersById = map[string]*intermediate.IntermediateUser{
		"u2": {Id: "u2", Username: "bob", Email: "bob@b.com", Memberships: []string{"general"}},
		"u1": {Id: "u1", Username: "alice", Email: "alice@a.com", Memberships: []string{"general", "random"}},
	}

	var buf bytes.Buffer
	require.NoError(t, tr.ExportUsers(&buf))

	lines := readLines(t, &buf)
	require.Len(t, lines, 2)
	// Should be sorted by username: alice first
	assert.Equal(t, "user", lines[0]["type"])
	user0 := lines[0]["user"].(map[string]any)
	assert.Equal(t, "alice", user0["username"])

	user1 := lines[1]["user"].(map[string]any)
	assert.Equal(t, "bob", user1["username"])

	// Alice should have 2 channel memberships
	teams0 := user0["teams"].([]any)
	require.Len(t, teams0, 1)
	team0 := teams0[0].(map[string]any)
	channels0 := team0["channels"].([]any)
	assert.Len(t, channels0, 2)
}

func TestExportPosts(t *testing.T) {
	now := time.Now().UnixMilli()
	tr := NewTransformer("myteam", newExportLogger())
	tr.Intermediate.Posts = []*intermediate.IntermediatePost{
		{
			User:     "alice",
			Channel:  "general",
			Message:  "Hello!",
			CreateAt: now,
			Reactions: []*intermediate.IntermediateReaction{
				{User: "bob", EmojiName: "thumbsup", CreateAt: now + 1},
			},
		},
	}

	var buf bytes.Buffer
	require.NoError(t, tr.ExportPosts(&buf))

	lines := readLines(t, &buf)
	require.Len(t, lines, 1)
	assert.Equal(t, "post", lines[0]["type"])
	post := lines[0]["post"].(map[string]any)
	assert.Equal(t, "alice", post["user"])
	assert.Equal(t, "general", post["channel"])
	assert.Equal(t, "Hello!", post["message"])
	reactions := post["reactions"].([]any)
	require.Len(t, reactions, 1)
	r := reactions[0].(map[string]any)
	assert.Equal(t, "thumbsup", r["emoji_name"])
}

func TestExportPostsWithReplies(t *testing.T) {
	now := time.Now().UnixMilli()
	tr := NewTransformer("myteam", newExportLogger())
	tr.Intermediate.Posts = []*intermediate.IntermediatePost{
		{
			User:     "alice",
			Channel:  "general",
			Message:  "Root post",
			CreateAt: now,
			Replies: []*intermediate.IntermediatePost{
				{User: "bob", Channel: "general", Message: "Reply", CreateAt: now + 1},
			},
		},
	}

	var buf bytes.Buffer
	require.NoError(t, tr.ExportPosts(&buf))

	lines := readLines(t, &buf)
	require.Len(t, lines, 1)
	post := lines[0]["post"].(map[string]any)
	replies := post["replies"].([]any)
	require.Len(t, replies, 1)
	reply := replies[0].(map[string]any)
	assert.Equal(t, "Reply", reply["message"])
}

func TestExportDirectPosts(t *testing.T) {
	now := time.Now().UnixMilli()
	tr := NewTransformer("myteam", newExportLogger())
	tr.Intermediate.Posts = []*intermediate.IntermediatePost{
		{
			User:           "alice",
			Message:        "Hey Bob!",
			CreateAt:       now,
			IsDirect:       true,
			ChannelMembers: []string{"alice", "bob"},
		},
	}

	var buf bytes.Buffer
	require.NoError(t, tr.ExportPosts(&buf))

	lines := readLines(t, &buf)
	require.Len(t, lines, 1)
	assert.Equal(t, "direct_post", lines[0]["type"])
	dp := lines[0]["direct_post"].(map[string]any)
	assert.Equal(t, "alice", dp["user"])
	members := dp["channel_members"].([]any)
	assert.Len(t, members, 2)
}

func TestExportOrder(t *testing.T) {
	tr := NewTransformer("myteam", newExportLogger())
	tr.Intermediate = &intermediate.Intermediate{
		PublicChannels:  []*intermediate.IntermediateChannel{{Name: "general", DisplayName: "General", Type: model.ChannelTypeOpen}},
		PrivateChannels: []*intermediate.IntermediateChannel{{Name: "secret", DisplayName: "Secret", Type: model.ChannelTypePrivate}},
		UsersById: map[string]*intermediate.IntermediateUser{
			"u1": {Id: "u1", Username: "alice", Email: "a@a.com"},
		},
		DirectChannels: []*intermediate.IntermediateChannel{
			{MembersUsernames: []string{"alice", "bob"}},
		},
		Posts: []*intermediate.IntermediatePost{
			{User: "alice", Channel: "general", Message: "hi", CreateAt: 1000},
		},
	}

	tmpFile, err := os.CreateTemp(t.TempDir(), "export-*.jsonl")
	require.NoError(t, err)
	tmpFile.Close()

	require.NoError(t, tr.Export(tmpFile.Name()))

	data, err := os.ReadFile(tmpFile.Name())
	require.NoError(t, err)

	var types []string
	dec := json.NewDecoder(bytes.NewReader(data))
	for dec.More() {
		var line imports.LineImportData
		require.NoError(t, dec.Decode(&line))
		types = append(types, line.Type)
	}

	// Expected order: version, public channel, private channel, user,
	// direct channel, post
	require.Len(t, types, 6)
	assert.Equal(t, "version", types[0])
	assert.Equal(t, "channel", types[1]) // public channel
	assert.Equal(t, "channel", types[2]) // private channel
	assert.Equal(t, "user", types[3])
	assert.Equal(t, "direct_channel", types[4])
	assert.Equal(t, "post", types[5])
}

func TestExportUsersIncludeTeamMemberships(t *testing.T) {
	tr := NewTransformer("myteam", newExportLogger())
	tr.Intermediate.UsersById = map[string]*intermediate.IntermediateUser{
		"u1": {Id: "u1", Username: "alice", Email: "a@a.com", Memberships: []string{"general", "random"}},
	}

	var buf bytes.Buffer
	require.NoError(t, tr.ExportUsers(&buf))

	lines := readLines(t, &buf)
	require.Len(t, lines, 1)
	user := lines[0]["user"].(map[string]any)
	teams := user["teams"].([]any)
	require.Len(t, teams, 1)
	team := teams[0].(map[string]any)
	assert.Equal(t, "myteam", team["name"])
	channels := team["channels"].([]any)
	assert.Len(t, channels, 2)
	channelNames := []string{}
	for _, c := range channels {
		ch := c.(map[string]any)
		channelNames = append(channelNames, ch["name"].(string))
	}
	assert.Contains(t, channelNames, "general")
	assert.Contains(t, channelNames, "random")
}

func TestExportLinesAreValidJSON(t *testing.T) {
	tr := NewTransformer("myteam", newExportLogger())
	tr.Intermediate = &intermediate.Intermediate{
		PublicChannels: []*intermediate.IntermediateChannel{{Name: "gen", DisplayName: "Gen", Type: model.ChannelTypeOpen}},
		UsersById: map[string]*intermediate.IntermediateUser{
			"u1": {Id: "u1", Username: "alice", Email: "a@a.com"},
		},
		Posts: []*intermediate.IntermediatePost{
			{User: "alice", Channel: "gen", Message: "hello", CreateAt: 1000},
		},
	}

	tmpFile, err := os.CreateTemp(t.TempDir(), "export-*.jsonl")
	require.NoError(t, err)
	tmpFile.Close()

	require.NoError(t, tr.Export(tmpFile.Name()))

	data, err := os.ReadFile(tmpFile.Name())
	require.NoError(t, err)

	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var obj map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &obj), "invalid JSON line: %s", line)
	}
}
