package rocketchat

import (
	"os"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mattermost/mmetl/services/intermediate"
)

func newLogger() log.FieldLogger {
	l := log.New()
	l.SetOutput(os.Stderr)
	return l
}

// ---------------------------------------------------------------------------
// User transformation tests
// ---------------------------------------------------------------------------

func TestTransformUsers(t *testing.T) {
	t.Run("basic user mapping", func(t *testing.T) {
		tr := NewTransformer("test", newLogger())
		users := []RocketChatUser{
			{
				ID:       "u1",
				Username: "Alice",
				Name:     "Alice Wonderland",
				Emails:   []RCEmail{{Address: "alice@example.com", Verified: true}},
				Active:   true,
				Roles:    []string{"user"},
				Type:     "user",
			},
		}

		tr.TransformUsers(users, false, "")

		require.Len(t, tr.Intermediate.UsersById, 1)
		u := tr.Intermediate.UsersById["u1"]
		require.NotNil(t, u)
		assert.Equal(t, "u1", u.Id)
		assert.Equal(t, "alice", u.Username) // lowercased
		assert.Equal(t, "Alice", u.FirstName)
		assert.Equal(t, "Wonderland", u.LastName)
		assert.Equal(t, "alice@example.com", u.Email)
		assert.Zero(t, u.DeleteAt)
	})

	t.Run("name splitting on first space", func(t *testing.T) {
		tr := NewTransformer("test", newLogger())
		users := []RocketChatUser{
			{ID: "u1", Username: "bob", Name: "Bob James Smith", Emails: []RCEmail{{Address: "b@b.com"}}, Active: true, Type: "user"},
		}
		tr.TransformUsers(users, false, "")
		u := tr.Intermediate.UsersById["u1"]
		require.NotNil(t, u)
		assert.Equal(t, "Bob", u.FirstName)
		assert.Equal(t, "James Smith", u.LastName)
	})

	t.Run("admin role mapping", func(t *testing.T) {
		tr := NewTransformer("test", newLogger())
		users := []RocketChatUser{
			{ID: "u1", Username: "admin", Name: "Admin User", Emails: []RCEmail{{Address: "admin@example.com"}}, Active: true, Roles: []string{"admin"}, Type: "user"},
		}
		tr.TransformUsers(users, false, "")
		// User should be transformed (role mapping is informational — not stored in IntermediateUser itself)
		require.NotNil(t, tr.Intermediate.UsersById["u1"])
	})

	t.Run("inactive user sets DeleteAt", func(t *testing.T) {
		tr := NewTransformer("test", newLogger())
		users := []RocketChatUser{
			{ID: "u1", Username: "inactive", Name: "Inactive User", Emails: []RCEmail{{Address: "i@i.com"}}, Active: false, Type: "user"},
		}
		tr.TransformUsers(users, false, "")
		u := tr.Intermediate.UsersById["u1"]
		require.NotNil(t, u)
		assert.NotZero(t, u.DeleteAt)
	})

	t.Run("missing email with defaultEmailDomain", func(t *testing.T) {
		tr := NewTransformer("test", newLogger())
		users := []RocketChatUser{
			{ID: "u1", Username: "noemail", Name: "No Email", Emails: nil, Active: true, Type: "user"},
		}
		tr.TransformUsers(users, false, "example.org")
		u := tr.Intermediate.UsersById["u1"]
		require.NotNil(t, u)
		assert.Equal(t, "noemail@example.org", u.Email)
	})

	t.Run("missing email with skipEmptyEmails", func(t *testing.T) {
		tr := NewTransformer("test", newLogger())
		users := []RocketChatUser{
			{ID: "u1", Username: "noemail", Name: "No Email", Emails: nil, Active: true, Type: "user"},
		}
		tr.TransformUsers(users, true, "")
		u := tr.Intermediate.UsersById["u1"]
		require.NotNil(t, u)
		assert.Equal(t, "", u.Email)
	})

	t.Run("bot user is skipped", func(t *testing.T) {
		tr := NewTransformer("test", newLogger())
		users := []RocketChatUser{
			{ID: "b1", Username: "bot", Name: "My Bot", Type: "bot"},
			{ID: "u1", Username: "human", Name: "Human User", Emails: []RCEmail{{Address: "h@h.com"}}, Active: true, Type: "user"},
		}
		tr.TransformUsers(users, false, "")
		assert.Len(t, tr.Intermediate.UsersById, 1)
		assert.Nil(t, tr.Intermediate.UsersById["b1"])
		assert.NotNil(t, tr.Intermediate.UsersById["u1"])
	})
}

// ---------------------------------------------------------------------------
// Channel transformation tests
// ---------------------------------------------------------------------------

func TestTransformChannels(t *testing.T) {
	desc := "a channel"

	t.Run("public channel type c", func(t *testing.T) {
		tr := NewTransformer("test", newLogger())
		rooms := []RocketChatRoom{
			{ID: "r1", Type: "c", Name: "general", FName: "General", Description: &desc},
		}
		tr.TransformChannels(rooms)
		require.Len(t, tr.Intermediate.PublicChannels, 1)
		ch := tr.Intermediate.PublicChannels[0]
		assert.Equal(t, "general", ch.Name)
		assert.Equal(t, "General", ch.DisplayName)
		assert.Equal(t, desc, ch.Purpose)
	})

	t.Run("private channel type p", func(t *testing.T) {
		tr := NewTransformer("test", newLogger())
		rooms := []RocketChatRoom{
			{ID: "r1", Type: "p", Name: "secret"},
		}
		tr.TransformChannels(rooms)
		require.Len(t, tr.Intermediate.PrivateChannels, 1)
	})

	t.Run("direct channel type d with 2 users", func(t *testing.T) {
		tr := NewTransformer("test", newLogger())
		// Users must be pre-populated (TransformChannels runs after TransformUsers).
		tr.Intermediate.UsersById = map[string]*intermediate.IntermediateUser{
			"u1": {Id: "u1", Username: "alice"},
			"u2": {Id: "u2", Username: "bob"},
		}
		rooms := []RocketChatRoom{
			{ID: "r1", Type: "d", UIDs: []string{"u1", "u2"}, Usernames: []string{"alice", "bob"}},
		}
		tr.TransformChannels(rooms)
		require.Len(t, tr.Intermediate.DirectChannels, 1)
		assert.Equal(t, []string{"alice", "bob"}, tr.Intermediate.DirectChannels[0].MembersUsernames)
	})

	t.Run("group channel type d with 3+ users", func(t *testing.T) {
		tr := NewTransformer("test", newLogger())
		tr.Intermediate.UsersById = map[string]*intermediate.IntermediateUser{
			"u1": {Id: "u1", Username: "alice"},
			"u2": {Id: "u2", Username: "bob"},
			"u3": {Id: "u3", Username: "carol"},
		}
		rooms := []RocketChatRoom{
			{ID: "r1", Type: "d", UIDs: []string{"u1", "u2", "u3"}, Usernames: []string{"alice", "bob", "carol"}},
		}
		tr.TransformChannels(rooms)
		require.Len(t, tr.Intermediate.GroupChannels, 1)
		assert.Equal(t, []string{"alice", "bob", "carol"}, tr.Intermediate.GroupChannels[0].MembersUsernames)
	})

	t.Run("direct channel with unknown member (e.g. rocket.cat) is skipped", func(t *testing.T) {
		tr := NewTransformer("test", newLogger())
		tr.Intermediate.UsersById = map[string]*intermediate.IntermediateUser{
			"u1": {Id: "u1", Username: "alice"},
			// "bot1" / "rocket.cat" is intentionally absent
		}
		rooms := []RocketChatRoom{
			{ID: "r1", Type: "d", UIDs: []string{"u1", "bot1"}, Usernames: []string{"alice", "rocket.cat"}},
		}
		tr.TransformChannels(rooms)
		assert.Empty(t, tr.Intermediate.DirectChannels)
		assert.True(t, tr.skippedRoomIDs["r1"])
	})

	t.Run("encrypted room is skipped", func(t *testing.T) {
		tr := NewTransformer("test", newLogger())
		rooms := []RocketChatRoom{
			{ID: "r1", Type: "c", Name: "encrypted-room", Encrypted: true},
		}
		tr.TransformChannels(rooms)
		assert.Empty(t, tr.Intermediate.PublicChannels)
		assert.True(t, tr.skippedRoomIDs["r1"])
	})

	t.Run("discussion room (prid set) is skipped", func(t *testing.T) {
		tr := NewTransformer("test", newLogger())
		rooms := []RocketChatRoom{
			{ID: "r1", Type: "p", Name: "discussion", ParentRID: "parent-room-id"},
		}
		tr.TransformChannels(rooms)
		assert.Empty(t, tr.Intermediate.PrivateChannels)
		assert.True(t, tr.skippedRoomIDs["r1"])
	})

	t.Run("null description handled", func(t *testing.T) {
		tr := NewTransformer("test", newLogger())
		rooms := []RocketChatRoom{
			{ID: "r1", Type: "c", Name: "nodesc", Description: nil},
		}
		tr.TransformChannels(rooms)
		require.Len(t, tr.Intermediate.PublicChannels, 1)
		assert.Equal(t, "", tr.Intermediate.PublicChannels[0].Purpose)
	})

	t.Run("name sanitization", func(t *testing.T) {
		tr := NewTransformer("test", newLogger())
		rooms := []RocketChatRoom{
			{ID: "r1", Type: "c", Name: "My-Channel-Name"},
		}
		tr.TransformChannels(rooms)
		require.Len(t, tr.Intermediate.PublicChannels, 1)
		// Name should be lowercased
		assert.Equal(t, "my-channel-name", tr.Intermediate.PublicChannels[0].Name)
	})
}

// ---------------------------------------------------------------------------
// Subscription → Membership tests
// ---------------------------------------------------------------------------

func TestTransformSubscriptions(t *testing.T) {
	t.Run("user added to channel members", func(t *testing.T) {
		tr := NewTransformer("test", newLogger())
		tr.Intermediate.UsersById = map[string]*intermediate.IntermediateUser{
			"u1": {Id: "u1", Username: "alice"},
		}
		tr.Intermediate.PublicChannels = []*intermediate.IntermediateChannel{
			{Id: "r1", Name: "general"},
		}
		subs := []RocketChatSubscription{
			{RoomID: "r1", User: RCMessageUser{ID: "u1", Username: "alice"}},
		}
		tr.TransformSubscriptions(subs)
		assert.Contains(t, tr.Intermediate.PublicChannels[0].Members, "u1")
		assert.Contains(t, tr.Intermediate.UsersById["u1"].Memberships, "general")
	})

	t.Run("skip on missing user", func(t *testing.T) {
		tr := NewTransformer("test", newLogger())
		tr.Intermediate.UsersById = map[string]*intermediate.IntermediateUser{}
		tr.Intermediate.PublicChannels = []*intermediate.IntermediateChannel{
			{Id: "r1", Name: "general"},
		}
		subs := []RocketChatSubscription{
			{RoomID: "r1", User: RCMessageUser{ID: "unknown"}},
		}
		tr.TransformSubscriptions(subs)
		assert.Empty(t, tr.Intermediate.PublicChannels[0].Members)
	})

	t.Run("skip on missing channel (DM room)", func(t *testing.T) {
		tr := NewTransformer("test", newLogger())
		tr.Intermediate.UsersById = map[string]*intermediate.IntermediateUser{
			"u1": {Id: "u1", Username: "alice"},
		}
		// No public/private channels — subscription is for a DM
		subs := []RocketChatSubscription{
			{RoomID: "dm-room", User: RCMessageUser{ID: "u1"}},
		}
		tr.TransformSubscriptions(subs) // should not panic
		assert.Empty(t, tr.Intermediate.UsersById["u1"].Memberships)
	})

	t.Run("no duplicate members", func(t *testing.T) {
		tr := NewTransformer("test", newLogger())
		tr.Intermediate.UsersById = map[string]*intermediate.IntermediateUser{
			"u1": {Id: "u1", Username: "alice"},
		}
		tr.Intermediate.PublicChannels = []*intermediate.IntermediateChannel{
			{Id: "r1", Name: "general"},
		}
		subs := []RocketChatSubscription{
			{RoomID: "r1", User: RCMessageUser{ID: "u1", Username: "alice"}},
			{RoomID: "r1", User: RCMessageUser{ID: "u1", Username: "alice"}},
		}
		tr.TransformSubscriptions(subs)
		assert.Len(t, tr.Intermediate.PublicChannels[0].Members, 1)
		assert.Len(t, tr.Intermediate.UsersById["u1"].Memberships, 1)
	})
}

// ---------------------------------------------------------------------------
// Message transformation tests
// ---------------------------------------------------------------------------

func TestTransformMessages(t *testing.T) {
	now := time.Now().UTC()

	t.Run("regular message conversion", func(t *testing.T) {
		tr := NewTransformer("test", newLogger())
		tr.Intermediate.UsersById = map[string]*intermediate.IntermediateUser{
			"u1": {Id: "u1", Username: "alice"},
		}
		tr.roomIDToType["r1"] = "c"
		tr.roomIDToChannelName["r1"] = "general"

		messages := []RocketChatMessage{
			{ID: "m1", RoomID: "r1", User: RCMessageUser{ID: "u1", Username: "alice"}, Message: "Hello!", Timestamp: now},
		}
		tr.TransformMessages(messages, nil)
		require.Len(t, tr.Intermediate.Posts, 1)
		p := tr.Intermediate.Posts[0]
		assert.Equal(t, "alice", p.User)
		assert.Equal(t, "general", p.Channel)
		assert.Equal(t, "Hello!", p.Message)
		assert.Equal(t, now.UnixMilli(), p.CreateAt)
		assert.False(t, p.IsDirect)
	})

	t.Run("thread assembly - root gets replies", func(t *testing.T) {
		tr := NewTransformer("test", newLogger())
		tr.Intermediate.UsersById = map[string]*intermediate.IntermediateUser{
			"u1": {Id: "u1", Username: "alice"},
			"u2": {Id: "u2", Username: "bob"},
		}
		tr.roomIDToType["r1"] = "c"
		tr.roomIDToChannelName["r1"] = "general"

		root := RocketChatMessage{
			ID: "root", RoomID: "r1",
			User: RCMessageUser{ID: "u1", Username: "alice"}, Message: "Root",
			Timestamp: now, ThreadCount: 1,
		}
		reply := RocketChatMessage{
			ID: "reply", RoomID: "r1",
			User: RCMessageUser{ID: "u2", Username: "bob"}, Message: "Reply",
			Timestamp: now.Add(time.Second), ThreadID: "root",
		}
		tr.TransformMessages([]RocketChatMessage{root, reply}, nil)
		require.Len(t, tr.Intermediate.Posts, 1)
		assert.Equal(t, "Root", tr.Intermediate.Posts[0].Message)
		require.Len(t, tr.Intermediate.Posts[0].Replies, 1)
		assert.Equal(t, "Reply", tr.Intermediate.Posts[0].Replies[0].Message)
	})

	t.Run("reaction conversion - colon stripping", func(t *testing.T) {
		tr := NewTransformer("test", newLogger())
		tr.Intermediate.UsersById = map[string]*intermediate.IntermediateUser{
			"u1": {Id: "u1", Username: "alice"},
		}
		tr.roomIDToType["r1"] = "c"
		tr.roomIDToChannelName["r1"] = "general"

		messages := []RocketChatMessage{
			{
				ID: "m1", RoomID: "r1",
				User: RCMessageUser{ID: "u1", Username: "alice"}, Message: "hi",
				Timestamp: now,
				Reactions: map[string]RCReactionInfo{
					":smile:":    {Usernames: []string{"alice", "bob"}},
					":thumbsup:": {Usernames: []string{"carol"}},
				},
			},
		}
		tr.TransformMessages(messages, nil)
		require.Len(t, tr.Intermediate.Posts, 1)
		p := tr.Intermediate.Posts[0]
		require.Len(t, p.Reactions, 3)
		emojiNames := make(map[string]bool)
		for _, r := range p.Reactions {
			emojiNames[r.EmojiName] = true
		}
		assert.True(t, emojiNames["smile"])
		assert.True(t, emojiNames["thumbsup"])
	})

	t.Run("system message mapping - uj → system_join_channel", func(t *testing.T) {
		tr := NewTransformer("test", newLogger())
		tr.Intermediate.UsersById = map[string]*intermediate.IntermediateUser{
			"u1": {Id: "u1", Username: "alice"},
		}
		tr.roomIDToType["r1"] = "c"
		tr.roomIDToChannelName["r1"] = "general"

		messages := []RocketChatMessage{
			{ID: "m1", RoomID: "r1", User: RCMessageUser{ID: "u1", Username: "alice"}, Type: "uj", Timestamp: now},
		}
		tr.TransformMessages(messages, nil)
		require.Len(t, tr.Intermediate.Posts, 1)
		assert.Equal(t, "system_join_channel", tr.Intermediate.Posts[0].Type)
	})

	t.Run("system message skip - message_pinned", func(t *testing.T) {
		tr := NewTransformer("test", newLogger())
		tr.Intermediate.UsersById = map[string]*intermediate.IntermediateUser{
			"u1": {Id: "u1", Username: "alice"},
		}
		tr.roomIDToType["r1"] = "c"
		tr.roomIDToChannelName["r1"] = "general"

		messages := []RocketChatMessage{
			{ID: "m1", RoomID: "r1", User: RCMessageUser{ID: "u1", Username: "alice"}, Type: "message_pinned", Timestamp: now},
		}
		tr.TransformMessages(messages, nil)
		assert.Empty(t, tr.Intermediate.Posts)
	})

	t.Run("system message skip - discussion-created", func(t *testing.T) {
		tr := NewTransformer("test", newLogger())
		tr.roomIDToType["r1"] = "c"
		tr.roomIDToChannelName["r1"] = "general"
		tr.Intermediate.UsersById = map[string]*intermediate.IntermediateUser{}

		messages := []RocketChatMessage{
			{ID: "m1", RoomID: "r1", User: RCMessageUser{ID: "u1", Username: "alice"}, Type: "discussion-created", Timestamp: now},
		}
		tr.TransformMessages(messages, nil)
		assert.Empty(t, tr.Intermediate.Posts)
	})

	t.Run("message in skipped room is skipped", func(t *testing.T) {
		tr := NewTransformer("test", newLogger())
		tr.skippedRoomIDs["r1"] = true
		tr.Intermediate.UsersById = map[string]*intermediate.IntermediateUser{
			"u1": {Id: "u1", Username: "alice"},
		}

		messages := []RocketChatMessage{
			{ID: "m1", RoomID: "r1", User: RCMessageUser{ID: "u1", Username: "alice"}, Message: "hello", Timestamp: now},
		}
		tr.TransformMessages(messages, nil)
		assert.Empty(t, tr.Intermediate.Posts)
	})

	t.Run("DM message - IsDirect and ChannelMembers set", func(t *testing.T) {
		tr := NewTransformer("test", newLogger())
		tr.Intermediate.UsersById = map[string]*intermediate.IntermediateUser{
			"u1": {Id: "u1", Username: "alice"},
		}
		tr.Intermediate.DirectChannels = []*intermediate.IntermediateChannel{
			{Id: "dm1", MembersUsernames: []string{"alice", "bob"}},
		}
		tr.roomIDToType["dm1"] = "d"
		tr.roomIDToChannelName["dm1"] = ""

		messages := []RocketChatMessage{
			{ID: "m1", RoomID: "dm1", User: RCMessageUser{ID: "u1", Username: "alice"}, Message: "hey", Timestamp: now},
		}
		tr.TransformMessages(messages, nil)
		require.Len(t, tr.Intermediate.Posts, 1)
		p := tr.Intermediate.Posts[0]
		assert.True(t, p.IsDirect)
		assert.Equal(t, []string{"alice", "bob"}, p.ChannelMembers)
	})

	t.Run("file attachment mapping", func(t *testing.T) {
		tr := NewTransformer("test", newLogger())
		tr.Intermediate.UsersById = map[string]*intermediate.IntermediateUser{
			"u1": {Id: "u1", Username: "alice"},
		}
		tr.roomIDToType["r1"] = "c"
		tr.roomIDToChannelName["r1"] = "general"

		uploads := map[string]*RocketChatUpload{
			"file1": {ID: "file1", Name: "photo.jpg", Complete: true},
		}
		messages := []RocketChatMessage{
			{
				ID: "m1", RoomID: "r1",
				User: RCMessageUser{ID: "u1", Username: "alice"}, Message: "see this",
				Timestamp: now,
				Files:     []RCFileRef{{ID: "file1", Name: "photo.jpg"}},
			},
		}
		tr.TransformMessages(messages, uploads)
		require.Len(t, tr.Intermediate.Posts, 1)
		require.Len(t, tr.Intermediate.Posts[0].Attachments, 1)
		assert.Equal(t, "bulk-export-attachments/file1_photo.jpg", tr.Intermediate.Posts[0].Attachments[0])
	})
}

// ---------------------------------------------------------------------------
// Channel mention conversion tests
// ---------------------------------------------------------------------------

func TestConvertChannelMentions(t *testing.T) {
	makeTransformerWithChannels := func(channelNames ...string) *Transformer {
		tr := NewTransformer("test", newLogger())
		for _, name := range channelNames {
			// roomIDToChannelName maps roomID → MM channel name (lowercase).
			// Use name as room ID for simplicity.
			tr.roomIDToChannelName[name] = name
		}
		return tr
	}

	t.Run("known channel reference converted to tilde format", func(t *testing.T) {
		tr := makeTransformerWithChannels("general", "random")
		result := tr.convertChannelMentions("check out #general for updates", nil)
		assert.Equal(t, "check out ~general for updates", result)
	})

	t.Run("unknown channel reference has hash stripped", func(t *testing.T) {
		tr := makeTransformerWithChannels("general")
		result := tr.convertChannelMentions("use #somehashtag for this topic", nil)
		assert.Equal(t, "use somehashtag for this topic", result)
	})

	t.Run("multiple references in one message", func(t *testing.T) {
		tr := makeTransformerWithChannels("general", "random")
		result := tr.convertChannelMentions("see #general and #random and #notachannel", nil)
		assert.Equal(t, "see ~general and ~random and notachannel", result)
	})

	t.Run("structured refs used for lookup", func(t *testing.T) {
		tr := makeTransformerWithChannels("my-channel")
		refs := []RCChannelRef{
			{ID: "r1", Name: "my-channel", FName: "My Channel"},
		}
		// RC may store "#my-channel" or display name variant in text
		result := tr.convertChannelMentions("join #my-channel now", refs)
		assert.Equal(t, "join ~my-channel now", result)
	})

	t.Run("no hash in message returns unchanged", func(t *testing.T) {
		tr := makeTransformerWithChannels("general")
		result := tr.convertChannelMentions("hello world", nil)
		assert.Equal(t, "hello world", result)
	})

	t.Run("channel mention case-insensitive match", func(t *testing.T) {
		tr := makeTransformerWithChannels("general")
		// RC may emit "#General" with capital letter
		result := tr.convertChannelMentions("see #General for info", nil)
		assert.Equal(t, "see ~general for info", result)
	})

	t.Run("channel mention at start of message", func(t *testing.T) {
		tr := makeTransformerWithChannels("announcements")
		result := tr.convertChannelMentions("#announcements is the place", nil)
		assert.Equal(t, "~announcements is the place", result)
	})

	t.Run("channel mention at end of message", func(t *testing.T) {
		tr := makeTransformerWithChannels("general")
		result := tr.convertChannelMentions("post in #general", nil)
		assert.Equal(t, "post in ~general", result)
	})

	t.Run("integration: channel mention converted in TransformMessages", func(t *testing.T) {
		tr := NewTransformer("test", newLogger())
		tr.Intermediate.UsersById = map[string]*intermediate.IntermediateUser{
			"u1": {Id: "u1", Username: "alice"},
		}
		tr.roomIDToType["r1"] = "c"
		tr.roomIDToChannelName["r1"] = "general"
		tr.roomIDToChannelName["r2"] = "random"

		messages := []RocketChatMessage{
			{
				ID:      "m1",
				RoomID:  "r1",
				User:    RCMessageUser{ID: "u1", Username: "alice"},
				Message: "check #general and #unknown-tag",
				Channels: []RCChannelRef{
					{ID: "r1", Name: "general"},
				},
				Timestamp: time.Now(),
			},
		}
		tr.TransformMessages(messages, nil)
		require.Len(t, tr.Intermediate.Posts, 1)
		assert.Equal(t, "check ~general and unknown-tag", tr.Intermediate.Posts[0].Message)
	})
}
