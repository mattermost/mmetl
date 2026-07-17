package rocketchat

import (
	"bytes"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/v8/channels/app/imports"
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

		tr.transformUsers(users, false, "", GuestHandlingUser)

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
		tr.transformUsers(users, false, "", GuestHandlingUser)
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
		tr.transformUsers(users, false, "", GuestHandlingUser)
		// User should be transformed (role mapping is informational — not stored in IntermediateUser itself)
		require.NotNil(t, tr.Intermediate.UsersById["u1"])
	})

	t.Run("inactive user sets DeleteAt", func(t *testing.T) {
		tr := NewTransformer("test", newLogger())
		users := []RocketChatUser{
			{ID: "u1", Username: "inactive", Name: "Inactive User", Emails: []RCEmail{{Address: "i@i.com"}}, Active: false, Type: "user"},
		}
		tr.transformUsers(users, false, "", GuestHandlingUser)
		u := tr.Intermediate.UsersById["u1"]
		require.NotNil(t, u)
		assert.NotZero(t, u.DeleteAt)
	})

	t.Run("missing email with defaultEmailDomain", func(t *testing.T) {
		tr := NewTransformer("test", newLogger())
		users := []RocketChatUser{
			{ID: "u1", Username: "noemail", Name: "No Email", Emails: nil, Active: true, Type: "user"},
		}
		tr.transformUsers(users, false, "example.org", GuestHandlingUser)
		u := tr.Intermediate.UsersById["u1"]
		require.NotNil(t, u)
		assert.Equal(t, "noemail@example.org", u.Email)
	})

	t.Run("missing email with skipEmptyEmails", func(t *testing.T) {
		tr := NewTransformer("test", newLogger())
		users := []RocketChatUser{
			{ID: "u1", Username: "noemail", Name: "No Email", Emails: nil, Active: true, Type: "user"},
		}
		tr.transformUsers(users, true, "", GuestHandlingUser)
		u := tr.Intermediate.UsersById["u1"]
		require.NotNil(t, u)
		assert.Equal(t, "", u.Email)
	})

	t.Run("bot user is imported as bot", func(t *testing.T) {
		tr := NewTransformer("test", newLogger())
		users := []RocketChatUser{
			{ID: "b1", Username: "bot", Name: "My Bot", Type: "bot", Active: true},
			{ID: "u1", Username: "human", Name: "Human User", Emails: []RCEmail{{Address: "h@h.com"}}, Active: true, Type: "user"},
		}
		tr.transformUsers(users, false, "", GuestHandlingUser)
		assert.Len(t, tr.Intermediate.UsersById, 2)

		bot := tr.Intermediate.UsersById["b1"]
		require.NotNil(t, bot)
		assert.True(t, bot.IsBot)
		assert.Equal(t, "bot", bot.Username)
		assert.Equal(t, "My Bot", bot.DisplayName)
		assert.Equal(t, "", bot.Email)
		assert.Equal(t, "", bot.Password)
		assert.Equal(t, int64(0), bot.DeleteAt)

		human := tr.Intermediate.UsersById["u1"]
		require.NotNil(t, human)
		assert.False(t, human.IsBot)
	})

	t.Run("inactive bot user sets DeleteAt", func(t *testing.T) {
		tr := NewTransformer("test", newLogger())
		users := []RocketChatUser{
			{ID: "b1", Username: "bot", Name: "My Bot", Type: "bot", Active: false},
		}
		tr.transformUsers(users, false, "", GuestHandlingUser)
		bot := tr.Intermediate.UsersById["b1"]
		require.NotNil(t, bot)
		assert.True(t, bot.IsBot)
		assert.Greater(t, bot.DeleteAt, int64(0))
	})

	t.Run("app-type user is skipped", func(t *testing.T) {
		tr := NewTransformer("test", newLogger())
		users := []RocketChatUser{
			{ID: "app1", Username: "rocket.cat", Name: "Rocket Cat", Type: "app", Active: true},
			{ID: "u1", Username: "alice", Name: "Alice", Emails: []RCEmail{{Address: "a@a.com"}}, Active: true, Type: "user"},
		}
		tr.transformUsers(users, false, "", GuestHandlingUser)
		require.Len(t, tr.Intermediate.UsersById, 1)
		assert.Nil(t, tr.Intermediate.UsersById["app1"])
		assert.NotNil(t, tr.Intermediate.UsersById["u1"])
		assert.True(t, tr.skippedUserIDs["app1"])
		assert.True(t, tr.skippedUsernames["rocket.cat"])
	})

	t.Run("unknown-type and empty-type users are skipped", func(t *testing.T) {
		tr := NewTransformer("test", newLogger())
		users := []RocketChatUser{
			{ID: "x1", Username: "mystery", Name: "Mystery", Type: "unknown", Active: true},
			{ID: "x2", Username: "blank", Name: "Blank", Type: "", Active: true},
			{ID: "u1", Username: "alice", Name: "Alice", Emails: []RCEmail{{Address: "a@a.com"}}, Active: true, Type: "user"},
		}
		tr.transformUsers(users, false, "", GuestHandlingUser)
		require.Len(t, tr.Intermediate.UsersById, 1)
		assert.NotNil(t, tr.Intermediate.UsersById["u1"])
		assert.True(t, tr.skippedUserIDs["x1"])
		assert.True(t, tr.skippedUserIDs["x2"])
	})

	t.Run("guest role sets IsGuest", func(t *testing.T) {
		tr := NewTransformer("test", newLogger())
		users := []RocketChatUser{
			{ID: "g1", Username: "guesty", Name: "Guest User", Emails: []RCEmail{{Address: "g@g.com"}}, Active: true, Roles: []string{"user", "guest"}, Type: "user"},
		}
		tr.transformUsers(users, false, "", GuestHandlingGuest)
		u := tr.Intermediate.UsersById["g1"]
		require.NotNil(t, u)
		assert.True(t, u.IsGuest)
		assert.False(t, u.IsBot)
	})

	t.Run("guest detection is case-insensitive", func(t *testing.T) {
		tr := NewTransformer("test", newLogger())
		users := []RocketChatUser{
			{ID: "g1", Username: "guesty", Name: "Guest User", Emails: []RCEmail{{Address: "g@g.com"}}, Active: true, Roles: []string{"Guest"}, Type: "user"},
		}
		tr.transformUsers(users, false, "", GuestHandlingGuest)
		u := tr.Intermediate.UsersById["g1"]
		require.NotNil(t, u)
		assert.True(t, u.IsGuest)
	})

	t.Run("regular user roles do not set IsGuest", func(t *testing.T) {
		tr := NewTransformer("test", newLogger())
		users := []RocketChatUser{
			{ID: "u1", Username: "alice", Name: "Alice", Emails: []RCEmail{{Address: "a@a.com"}}, Active: true, Roles: []string{"user", "admin"}, Type: "user"},
		}
		tr.transformUsers(users, false, "", GuestHandlingGuest)
		u := tr.Intermediate.UsersById["u1"]
		require.NotNil(t, u)
		assert.False(t, u.IsGuest)
	})

	t.Run("bot with guest role is never a guest", func(t *testing.T) {
		tr := NewTransformer("test", newLogger())
		users := []RocketChatUser{
			{ID: "b1", Username: "bot", Name: "My Bot", Type: "bot", Active: true, Roles: []string{"guest"}},
		}
		tr.transformUsers(users, false, "", GuestHandlingGuest)
		bot := tr.Intermediate.UsersById["b1"]
		require.NotNil(t, bot)
		assert.True(t, bot.IsBot)
		assert.False(t, bot.IsGuest)
	})

	t.Run("guest-handling skip drops the guest user", func(t *testing.T) {
		tr := NewTransformer("test", newLogger())
		users := []RocketChatUser{
			{ID: "g1", Username: "guesty", Name: "Guest User", Emails: []RCEmail{{Address: "g@g.com"}}, Active: true, Roles: []string{"guest"}, Type: "user"},
			{ID: "u1", Username: "alice", Name: "Alice", Emails: []RCEmail{{Address: "a@a.com"}}, Active: true, Type: "user"},
		}
		tr.transformUsers(users, false, "", GuestHandlingSkip)
		require.Len(t, tr.Intermediate.UsersById, 1)
		assert.Nil(t, tr.Intermediate.UsersById["g1"])
		assert.NotNil(t, tr.Intermediate.UsersById["u1"])
		assert.True(t, tr.skippedUserIDs["g1"])
		assert.True(t, tr.skippedUsernames["guesty"])
	})

	t.Run("guest-handling user keeps guest as flagged user", func(t *testing.T) {
		tr := NewTransformer("test", newLogger())
		users := []RocketChatUser{
			{ID: "g1", Username: "guesty", Name: "Guest User", Emails: []RCEmail{{Address: "g@g.com"}}, Active: true, Roles: []string{"guest"}, Type: "user"},
		}
		tr.transformUsers(users, false, "", GuestHandlingUser)
		u := tr.Intermediate.UsersById["g1"]
		require.NotNil(t, u)
		// IsGuest reflects detection regardless of mode; the export mode decides
		// whether guest roles are actually emitted.
		assert.True(t, u.IsGuest)
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
		tr.transformChannels(rooms)
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
		tr.transformChannels(rooms)
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
		tr.transformChannels(rooms)
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
		tr.transformChannels(rooms)
		require.Len(t, tr.Intermediate.GroupChannels, 1)
		assert.Equal(t, []string{"alice", "bob", "carol"}, tr.Intermediate.GroupChannels[0].MembersUsernames)
	})

	t.Run("direct channel with unknown member is included without a placeholder user", func(t *testing.T) {
		tr := NewTransformer("test", newLogger())
		tr.Intermediate.UsersById = map[string]*intermediate.IntermediateUser{
			"u1": {Id: "u1", Username: "alice"},
			// "bot1" / "rocket.cat" is intentionally absent.
		}
		rooms := []RocketChatRoom{
			{ID: "r1", Type: "d", UIDs: []string{"u1", "bot1"}, Usernames: []string{"alice", "rocket.cat"}},
		}
		tr.transformChannels(rooms)

		// Room must be imported, not dropped.
		require.Len(t, tr.Intermediate.DirectChannels, 1)
		assert.False(t, tr.skippedRoomIDs["r1"])

		// transformChannels does not create placeholder users for unknown
		// members; placeholders are created lazily during message transformation
		// (buildBasePost). Both usernames still appear in the channel.
		assert.Equal(t, []string{"alice", "rocket.cat"}, tr.Intermediate.DirectChannels[0].MembersUsernames)
	})

	t.Run("single-member DM is expanded to a self-DM", func(t *testing.T) {
		tr := NewTransformer("test", newLogger())
		tr.Intermediate.UsersById = map[string]*intermediate.IntermediateUser{
			"u1": {Id: "u1", Username: "alice"},
		}
		rooms := []RocketChatRoom{
			{ID: "r1", Type: "d", UIDs: []string{"u1"}, Usernames: []string{"alice"}},
		}
		tr.transformChannels(rooms)

		// RC allows self-DMs (1 participant); Mattermost models these as a direct
		// channel where the same user appears twice, so the room is kept.
		require.Len(t, tr.Intermediate.DirectChannels, 1)
		assert.False(t, tr.skippedRoomIDs["r1"])
		assert.Equal(t, []string{"alice", "alice"}, tr.Intermediate.DirectChannels[0].MembersUsernames)
	})

	t.Run("empty room name falls back to ID for OriginalName", func(t *testing.T) {
		tr := NewTransformer("test", newLogger())
		rooms := []RocketChatRoom{
			{ID: "room-id-123", Type: "c", Name: ""},
		}
		tr.transformChannels(rooms)
		require.Len(t, tr.Intermediate.PublicChannels, 1)
		assert.Equal(t, "room-id-123", tr.Intermediate.PublicChannels[0].OriginalName)
	})

	t.Run("room name with spaces is slugified", func(t *testing.T) {
		tr := NewTransformer("test", newLogger())
		rooms := []RocketChatRoom{
			{ID: "r1", Type: "c", Name: "General Discussion"},
		}
		tr.transformChannels(rooms)
		require.Len(t, tr.Intermediate.PublicChannels, 1)
		assert.Equal(t, "general-discussion", tr.Intermediate.PublicChannels[0].Name)
	})

	t.Run("group DM at exactly ChannelGroupMaxUsers members stays as group", func(t *testing.T) {
		tr := NewTransformer("test", newLogger())
		uids := make([]string, model.ChannelGroupMaxUsers)
		usernames := make([]string, model.ChannelGroupMaxUsers)
		users := make(map[string]*intermediate.IntermediateUser, model.ChannelGroupMaxUsers)
		for i := 0; i < model.ChannelGroupMaxUsers; i++ {
			uid := fmt.Sprintf("u%d", i+1)
			username := fmt.Sprintf("user%d", i+1)
			users[uid] = &intermediate.IntermediateUser{Id: uid, Username: username}
			uids[i] = uid
			usernames[i] = username
		}
		tr.Intermediate.UsersById = users
		rooms := []RocketChatRoom{
			{ID: "r1", Type: "d", UIDs: uids, Usernames: usernames},
		}
		tr.transformChannels(rooms)
		require.Len(t, tr.Intermediate.GroupChannels, 1)
		assert.Empty(t, tr.Intermediate.PrivateChannels)
	})

	t.Run("group DM exceeding ChannelGroupMaxUsers converts to private channel", func(t *testing.T) {
		tr := NewTransformer("test", newLogger())
		count := model.ChannelGroupMaxUsers + 1
		uids := make([]string, count)
		usernames := make([]string, count)
		users := make(map[string]*intermediate.IntermediateUser, count)
		for i := 0; i < count; i++ {
			uid := fmt.Sprintf("u%d", i+1)
			username := fmt.Sprintf("user%d", i+1)
			users[uid] = &intermediate.IntermediateUser{Id: uid, Username: username}
			uids[i] = uid
			usernames[i] = username
		}
		tr.Intermediate.UsersById = users
		rooms := []RocketChatRoom{
			{ID: "r1", Type: "d", Name: "big-group", UIDs: uids, Usernames: usernames},
		}
		tr.transformChannels(rooms)
		assert.Empty(t, tr.Intermediate.GroupChannels)
		require.Len(t, tr.Intermediate.PrivateChannels, 1)
	})

	t.Run("encrypted room is skipped", func(t *testing.T) {
		tr := NewTransformer("test", newLogger())
		rooms := []RocketChatRoom{
			{ID: "r1", Type: "c", Name: "encrypted-room", Encrypted: true},
		}
		tr.transformChannels(rooms)
		assert.Empty(t, tr.Intermediate.PublicChannels)
		assert.True(t, tr.skippedRoomIDs["r1"])
	})

	t.Run("null description handled", func(t *testing.T) {
		tr := NewTransformer("test", newLogger())
		rooms := []RocketChatRoom{
			{ID: "r1", Type: "c", Name: "nodesc", Description: nil},
		}
		tr.transformChannels(rooms)
		require.Len(t, tr.Intermediate.PublicChannels, 1)
		assert.Equal(t, "", tr.Intermediate.PublicChannels[0].Purpose)
	})

	t.Run("name sanitization", func(t *testing.T) {
		tr := NewTransformer("test", newLogger())
		rooms := []RocketChatRoom{
			{ID: "r1", Type: "c", Name: "My-Channel-Name"},
		}
		tr.transformChannels(rooms)
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
		tr.transformSubscriptions(subs)
		assert.Contains(t, tr.Intermediate.PublicChannels[0].Members, "u1")
		require.Len(t, tr.Intermediate.UsersById["u1"].Memberships, 1)
		assert.Equal(t, "general", tr.Intermediate.UsersById["u1"].Memberships[0].Name)
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
		tr.transformSubscriptions(subs)
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
		tr.transformSubscriptions(subs) // should not panic
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
		tr.transformSubscriptions(subs)
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
		tr.transformMessages(messages, nil)
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
		tr.transformMessages([]RocketChatMessage{root, reply}, nil)
		require.Len(t, tr.Intermediate.Posts, 1)
		assert.Equal(t, "Root", tr.Intermediate.Posts[0].Message)
		require.Len(t, tr.Intermediate.Posts[0].Replies, 1)
		assert.Equal(t, "Reply", tr.Intermediate.Posts[0].Replies[0].Message)
	})

	t.Run("thread assembly - skipped root drops replies and counts them", func(t *testing.T) {
		tr := NewTransformer("test", newLogger())
		tr.Intermediate.UsersById = map[string]*intermediate.IntermediateUser{
			"u2": {Id: "u2", Username: "bob"},
		}
		tr.roomIDToType["r1"] = "c"
		tr.roomIDToChannelName["r1"] = "general"
		// The root's author is a skipped user (e.g. a guest under
		// GuestHandlingSkip), so convertMessage returns nil for the root.
		tr.skippedUsernames["guesty"] = true

		root := RocketChatMessage{
			ID: "root", RoomID: "r1",
			User: RCMessageUser{ID: "u1", Username: "guesty"}, Message: "Root",
			Timestamp: now, ThreadCount: 2,
		}
		reply1 := RocketChatMessage{
			ID: "reply1", RoomID: "r1",
			User: RCMessageUser{ID: "u2", Username: "bob"}, Message: "Reply 1",
			Timestamp: now.Add(time.Second), ThreadID: "root",
		}
		reply2 := RocketChatMessage{
			ID: "reply2", RoomID: "r1",
			User: RCMessageUser{ID: "u2", Username: "bob"}, Message: "Reply 2",
			Timestamp: now.Add(2 * time.Second), ThreadID: "root",
		}
		tr.transformMessages([]RocketChatMessage{root, reply1, reply2}, nil)

		// The whole thread is dropped, but the lost replies must be counted so
		// the loss is visible in the end-of-transform summary: 1 root (skipped
		// user) + 2 orphaned replies.
		assert.Empty(t, tr.Intermediate.Posts)
		assert.Equal(t, 3, tr.droppedPostRefs)
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
		tr.transformMessages(messages, nil)
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
		tr.transformMessages(messages, nil)
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
		tr.transformMessages(messages, nil)
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
		tr.transformMessages(messages, nil)
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
		tr.transformMessages(messages, nil)
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
		tr.transformMessages(messages, nil)
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
		tr.transformMessages(messages, uploads)
		require.Len(t, tr.Intermediate.Posts, 1)
		require.Len(t, tr.Intermediate.Posts[0].Attachments, 1)
		assert.Equal(t, "bulk-export-attachments/file1_photo.jpg", tr.Intermediate.Posts[0].Attachments[0])
	})

	t.Run("thumbnail file ref is skipped to avoid duplicate images", func(t *testing.T) {
		tr := NewTransformer("test", newLogger())
		tr.Intermediate.UsersById = map[string]*intermediate.IntermediateUser{
			"u1": {Id: "u1", Username: "alice"},
		}
		tr.roomIDToType["r1"] = "c"
		tr.roomIDToChannelName["r1"] = "general"

		uploads := map[string]*RocketChatUpload{
			"img1":   {ID: "img1", Name: "white-wolf.jpg", Complete: true},
			"thumb1": {ID: "thumb1", Name: "thumb-white-wolf.jpg", Complete: true},
		}
		messages := []RocketChatMessage{
			{
				ID: "m1", RoomID: "r1",
				User: RCMessageUser{ID: "u1", Username: "alice"}, Message: "chart",
				Timestamp: now,
				Files: []RCFileRef{
					{ID: "img1", Name: "white-wolf.jpg", TypeGroup: "image"},
					{ID: "thumb1", Name: "thumb-white-wolf.jpg", TypeGroup: "thumb"},
				},
			},
		}
		tr.transformMessages(messages, uploads)
		require.Len(t, tr.Intermediate.Posts, 1)
		require.Len(t, tr.Intermediate.Posts[0].Attachments, 1)
		assert.Equal(t, "bulk-export-attachments/img1_white-wolf.jpg", tr.Intermediate.Posts[0].Attachments[0])
	})

	t.Run("file attachment with caption uses upload description as message", func(t *testing.T) {
		tr := NewTransformer("test", newLogger())
		tr.Intermediate.UsersById = map[string]*intermediate.IntermediateUser{
			"u1": {Id: "u1", Username: "alice"},
		}
		tr.roomIDToType["r1"] = "c"
		tr.roomIDToChannelName["r1"] = "general"

		uploads := map[string]*RocketChatUpload{
			"file1": {ID: "file1", Name: "test-simple.txt", Complete: true, Description: "texter"},
		}
		messages := []RocketChatMessage{
			{
				ID: "m1", RoomID: "r1",
				User:      RCMessageUser{ID: "u1", Username: "alice"},
				Message:   "", // RC sets msg="" when file is uploaded with a caption
				Timestamp: now,
				Files:     []RCFileRef{{ID: "file1", Name: "test-simple.txt"}},
			},
		}
		tr.transformMessages(messages, uploads)
		require.Len(t, tr.Intermediate.Posts, 1)
		p := tr.Intermediate.Posts[0]
		assert.Equal(t, "texter", p.Message)
		require.Len(t, p.Attachments, 1)
		assert.Equal(t, "bulk-export-attachments/file1_test-simple.txt", p.Attachments[0])
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
		tr.transformMessages(messages, nil)
		require.Len(t, tr.Intermediate.Posts, 1)
		assert.Equal(t, "check ~general and unknown-tag", tr.Intermediate.Posts[0].Message)
	})
}

// ---------------------------------------------------------------------------
// Guest handling tests
// ---------------------------------------------------------------------------

func TestValidateGuestHandling(t *testing.T) {
	for _, mode := range []string{GuestHandlingGuest, GuestHandlingUser, GuestHandlingSkip} {
		assert.NoError(t, ValidateGuestHandling(mode))
	}
	for _, mode := range []string{"", "guests", "USER", "drop", "invalid"} {
		assert.Error(t, ValidateGuestHandling(mode), "expected error for %q", mode)
	}
}

// exportUserRoles runs a guest user through the full pipeline in the given mode
// and returns the exported user line's system role, team role, and channel role
// (empty strings when the user was not exported at all).
func exportUserRoles(t *testing.T, mode string) (systemRole, teamRole, channelRole string, exported bool) {
	t.Helper()
	tr := NewTransformer("myteam", newLogger())
	parsed := &ParsedData{
		Users: []RocketChatUser{
			{ID: "g1", Username: "guesty", Name: "Guest User", Emails: []RCEmail{{Address: "g@g.com"}}, Active: true, Roles: []string{"guest"}, Type: "user"},
		},
		Rooms: []RocketChatRoom{
			{ID: "r1", Type: "c", Name: "general"},
		},
		Subscriptions: []RocketChatSubscription{
			{RoomID: "r1", User: RCMessageUser{ID: "g1", Username: "guesty"}},
		},
	}
	tr.Transform(parsed, true, false, "", mode)

	var buf bytes.Buffer
	require.NoError(t, tr.ExportUsers(&buf, ""))
	lines := readLines(t, &buf)

	for _, line := range lines {
		if line["type"] != "user" {
			continue
		}
		user := line["user"].(map[string]any)
		if user["username"] != "guesty" {
			continue
		}
		exported = true
		systemRole, _ = user["roles"].(string)
		teams := user["teams"].([]any)
		team := teams[0].(map[string]any)
		teamRole, _ = team["roles"].(string)
		channels := team["channels"].([]any)
		require.NotEmpty(t, channels)
		ch := channels[0].(map[string]any)
		channelRole, _ = ch["roles"].(string)
	}
	return systemRole, teamRole, channelRole, exported
}

func TestGuestExportRoleMapping(t *testing.T) {
	t.Run("mode guest emits guest roles", func(t *testing.T) {
		systemRole, teamRole, channelRole, exported := exportUserRoles(t, GuestHandlingGuest)
		require.True(t, exported)
		assert.Equal(t, model.SystemGuestRoleId, systemRole)
		assert.Equal(t, model.TeamGuestRoleId, teamRole)
		assert.Equal(t, model.ChannelGuestRoleId, channelRole)
	})

	t.Run("mode user emits regular user roles", func(t *testing.T) {
		systemRole, teamRole, channelRole, exported := exportUserRoles(t, GuestHandlingUser)
		require.True(t, exported)
		assert.Equal(t, model.SystemUserRoleId, systemRole)
		assert.Equal(t, model.TeamUserRoleId, teamRole)
		assert.Equal(t, model.ChannelUserRoleId, channelRole)
	})

	t.Run("mode skip omits the guest user entirely", func(t *testing.T) {
		_, _, _, exported := exportUserRoles(t, GuestHandlingSkip)
		assert.False(t, exported)
	})
}

// ---------------------------------------------------------------------------
// Channel-less guest handling tests
// ---------------------------------------------------------------------------

func TestSkipChannellessGuests(t *testing.T) {
	t.Run("guest mode drops guests with no memberships, keeps the rest", func(t *testing.T) {
		tr := NewTransformer("test", newLogger())
		tr.EmitGuestRoles = true
		tr.Intermediate.UsersById = map[string]*intermediate.IntermediateUser{
			"u1": {Id: "u1", Username: "alice", Memberships: []intermediate.IntermediateMembership{{Name: "general"}}},
			"g1": {Id: "g1", Username: "guesty", IsGuest: true},
			"g2": {Id: "g2", Username: "keeper", IsGuest: true, Memberships: []intermediate.IntermediateMembership{{Name: "general"}}},
		}

		tr.skipChannellessGuests()

		assert.Nil(t, tr.Intermediate.UsersById["g1"], "channel-less guest should be dropped")
		assert.True(t, tr.skippedUserIDs["g1"])
		assert.True(t, tr.skippedUsernames["guesty"])
		assert.NotNil(t, tr.Intermediate.UsersById["g2"], "guest with a membership stays a guest")
		assert.NotNil(t, tr.Intermediate.UsersById["u1"])
	})

	t.Run("user mode (EmitGuestRoles false) is a no-op", func(t *testing.T) {
		tr := NewTransformer("test", newLogger())
		tr.EmitGuestRoles = false
		tr.Intermediate.UsersById = map[string]*intermediate.IntermediateUser{
			"g1": {Id: "g1", Username: "guesty", IsGuest: true},
		}

		tr.skipChannellessGuests()

		assert.NotNil(t, tr.Intermediate.UsersById["g1"], "channel-less guests are only dropped in guest mode")
		assert.False(t, tr.skippedUserIDs["g1"])
	})

	t.Run("channel-less guest in a DM collapses it to a self-DM", func(t *testing.T) {
		tr := NewTransformer("test", newLogger())
		tr.EmitGuestRoles = true
		tr.Intermediate.UsersById = map[string]*intermediate.IntermediateUser{
			"u1": {Id: "u1", Username: "alice", Memberships: []intermediate.IntermediateMembership{{Name: "general"}}},
			"g1": {Id: "g1", Username: "guesty", IsGuest: true},
		}
		dm := &intermediate.IntermediateChannel{
			Id: "dm1", Type: model.ChannelTypeDirect,
			Members: []string{"u1", "g1"}, MembersUsernames: []string{"alice", "guesty"},
		}
		tr.Intermediate.DirectChannels = []*intermediate.IntermediateChannel{dm}
		tr.directRoomIDToChannel["dm1"] = dm

		tr.skipChannellessGuests()

		require.Len(t, tr.Intermediate.DirectChannels, 1)
		assert.Equal(t, []string{"alice", "alice"}, tr.Intermediate.DirectChannels[0].MembersUsernames)
		assert.NotContains(t, tr.Intermediate.DirectChannels[0].MembersUsernames, "guesty")
	})

	t.Run("group DM losing a channel-less guest is reclassified to a direct channel", func(t *testing.T) {
		tr := NewTransformer("test", newLogger())
		tr.EmitGuestRoles = true
		tr.Intermediate.UsersById = map[string]*intermediate.IntermediateUser{
			"u1": {Id: "u1", Username: "alice", Memberships: []intermediate.IntermediateMembership{{Name: "general"}}},
			"u2": {Id: "u2", Username: "bob", Memberships: []intermediate.IntermediateMembership{{Name: "general"}}},
			"g1": {Id: "g1", Username: "guesty", IsGuest: true},
		}
		gm := &intermediate.IntermediateChannel{
			Id: "gm1", Type: model.ChannelTypeGroup,
			Members: []string{"u1", "u2", "g1"}, MembersUsernames: []string{"alice", "bob", "guesty"},
		}
		tr.Intermediate.GroupChannels = []*intermediate.IntermediateChannel{gm}
		tr.directRoomIDToChannel["gm1"] = gm

		tr.skipChannellessGuests()

		assert.Empty(t, tr.Intermediate.GroupChannels)
		require.Len(t, tr.Intermediate.DirectChannels, 1)
		assert.Equal(t, []string{"alice", "bob"}, tr.Intermediate.DirectChannels[0].MembersUsernames)
		assert.Equal(t, model.ChannelTypeDirect, tr.Intermediate.DirectChannels[0].Type)
	})

	t.Run("DM between only channel-less guests is dropped entirely", func(t *testing.T) {
		tr := NewTransformer("test", newLogger())
		tr.EmitGuestRoles = true
		tr.Intermediate.UsersById = map[string]*intermediate.IntermediateUser{
			"g1": {Id: "g1", Username: "guesty1", IsGuest: true},
			"g2": {Id: "g2", Username: "guesty2", IsGuest: true},
		}
		dm := &intermediate.IntermediateChannel{
			Id: "dm1", Type: model.ChannelTypeDirect,
			Members: []string{"g1", "g2"}, MembersUsernames: []string{"guesty1", "guesty2"},
		}
		tr.Intermediate.DirectChannels = []*intermediate.IntermediateChannel{dm}
		tr.directRoomIDToChannel["dm1"] = dm

		tr.skipChannellessGuests()

		assert.Empty(t, tr.Intermediate.DirectChannels)
		assert.True(t, tr.skippedRoomIDs["dm1"], "room must be recorded so its messages are skipped")
		_, stillMapped := tr.directRoomIDToChannel["dm1"]
		assert.False(t, stillMapped)
	})
}

// TestChannellessGuestEndToEnd runs a full transform where a guest exists only
// in a DM (no channel subscription). In guest mode they must be dropped with no
// dangling references; in user mode they are kept as a regular member.
func TestChannellessGuestEndToEnd(t *testing.T) {
	newDump := func() *ParsedData {
		now := time.Now().UTC()
		return &ParsedData{
			Users: []RocketChatUser{
				{ID: "u1", Username: "alice", Name: "Alice", Emails: []RCEmail{{Address: "a@a.com"}}, Active: true, Type: "user"},
				{ID: "k1", Username: "keeper", Name: "Keeper Guest", Emails: []RCEmail{{Address: "k@k.com"}}, Active: true, Roles: []string{"guest"}, Type: "user"},
				{ID: "g1", Username: "guesty", Name: "Guest User", Emails: []RCEmail{{Address: "g@g.com"}}, Active: true, Roles: []string{"guest"}, Type: "user"},
			},
			Rooms: []RocketChatRoom{
				{ID: "r1", Type: "c", Name: "general"},
				{ID: "dm1", Type: "d", UIDs: []string{"u1", "g1"}, Usernames: []string{"alice", "guesty"}},
			},
			// guesty has no subscription to r1, so ends up channel-less; keeper does.
			Subscriptions: []RocketChatSubscription{
				{RoomID: "r1", User: RCMessageUser{ID: "u1", Username: "alice"}},
				{RoomID: "r1", User: RCMessageUser{ID: "k1", Username: "keeper"}},
			},
			Messages: []RocketChatMessage{
				{ID: "m1", RoomID: "r1", User: RCMessageUser{ID: "u1", Username: "alice"}, Message: "hi", Timestamp: now},
				{ID: "m2", RoomID: "dm1", User: RCMessageUser{ID: "g1", Username: "guesty"}, Message: "secret", Timestamp: now.Add(time.Second)},
			},
		}
	}

	t.Run("guest mode drops the channel-less guest and their DM post", func(t *testing.T) {
		tr := NewTransformer("myteam", newLogger())
		tr.Transform(newDump(), true, false, "", GuestHandlingGuest)

		assert.Nil(t, tr.Intermediate.UsersById["g1"], "channel-less guest dropped")
		require.NotNil(t, tr.Intermediate.UsersById["k1"], "guest with a channel membership kept")
		assert.True(t, tr.Intermediate.UsersById["k1"].IsGuest)
		require.NotNil(t, tr.Intermediate.UsersById["u1"])

		// The DM collapses to alice's self-DM; nothing references guesty.
		require.Len(t, tr.Intermediate.DirectChannels, 1)
		for _, name := range tr.Intermediate.DirectChannels[0].MembersUsernames {
			assert.NotEqual(t, "guesty", name)
		}

		// guesty's DM post is dropped; only alice's channel post remains.
		for _, p := range tr.Intermediate.Posts {
			assert.NotEqual(t, "guesty", p.User)
		}

		// The exported guest line for keeper must satisfy the server's validator.
		line := intermediate.GetImportLineFromUser(tr.Intermediate.UsersById["k1"], "myteam", tr.EmitGuestRoles)
		assert.Equal(t, model.SystemGuestRoleId, *line.User.Roles)
		require.Nil(t, imports.ValidateUserImportData(line.User))
	})

	t.Run("user mode keeps the channel-less guest as a regular member", func(t *testing.T) {
		tr := NewTransformer("myteam", newLogger())
		tr.Transform(newDump(), true, false, "", GuestHandlingUser)

		require.NotNil(t, tr.Intermediate.UsersById["g1"], "in user mode channel-less guests are kept")
	})
}

func TestSkippedUsersReferentialIntegrity(t *testing.T) {
	// Build a dump where an "app" user (rocket.cat) participates in a public
	// channel, a DM with a real user, authors a post, and reacts to a message.
	// After the transform, nothing may reference rocket.cat.
	newDump := func() *ParsedData {
		now := time.Now().UTC()
		return &ParsedData{
			Users: []RocketChatUser{
				{ID: "app1", Username: "rocket.cat", Name: "Rocket Cat", Type: "app", Active: true},
				{ID: "u1", Username: "alice", Name: "Alice", Emails: []RCEmail{{Address: "a@a.com"}}, Active: true, Type: "user"},
			},
			Rooms: []RocketChatRoom{
				{ID: "r1", Type: "c", Name: "general"},
				{ID: "dm1", Type: "d", UIDs: []string{"u1", "app1"}, Usernames: []string{"alice", "rocket.cat"}},
			},
			Subscriptions: []RocketChatSubscription{
				{RoomID: "r1", User: RCMessageUser{ID: "u1", Username: "alice"}},
				{RoomID: "r1", User: RCMessageUser{ID: "app1", Username: "rocket.cat"}},
			},
			Messages: []RocketChatMessage{
				{ID: "m1", RoomID: "r1", User: RCMessageUser{ID: "u1", Username: "alice"}, Message: "hi", Timestamp: now,
					Reactions: map[string]RCReactionInfo{":wave:": {Usernames: []string{"rocket.cat"}}}},
				{ID: "m2", RoomID: "r1", User: RCMessageUser{ID: "app1", Username: "rocket.cat"}, Message: "beep boop", Timestamp: now.Add(time.Second)},
			},
		}
	}

	tr := NewTransformer("myteam", newLogger())
	tr.Transform(newDump(), true, false, "", GuestHandlingUser)

	// rocket.cat must not be exported, not even as a placeholder.
	assert.Nil(t, tr.Intermediate.UsersById["app1"])
	assert.Len(t, tr.Intermediate.UsersById, 1)
	assert.NotNil(t, tr.Intermediate.UsersById["u1"])

	// No public-channel membership references rocket.cat.
	require.Len(t, tr.Intermediate.PublicChannels, 1)
	assert.NotContains(t, tr.Intermediate.PublicChannels[0].Members, "app1")

	// The DM must not reference rocket.cat; with only alice left it becomes a
	// self-DM rather than a dangling reference.
	require.Len(t, tr.Intermediate.DirectChannels, 1)
	for _, name := range tr.Intermediate.DirectChannels[0].MembersUsernames {
		assert.NotEqual(t, "rocket.cat", name)
	}

	// Only alice's post survives, and no reaction references rocket.cat.
	require.Len(t, tr.Intermediate.Posts, 1)
	assert.Equal(t, "alice", tr.Intermediate.Posts[0].User)
	for _, r := range tr.Intermediate.Posts[0].Reactions {
		assert.NotEqual(t, "rocket.cat", r.User)
	}
	assert.Positive(t, tr.droppedPostRefs)
}

func TestGuestSkipReferentialIntegrity(t *testing.T) {
	now := time.Now().UTC()
	parsed := &ParsedData{
		Users: []RocketChatUser{
			{ID: "g1", Username: "guesty", Name: "Guest User", Emails: []RCEmail{{Address: "g@g.com"}}, Active: true, Roles: []string{"guest"}, Type: "user"},
			{ID: "u1", Username: "alice", Name: "Alice", Emails: []RCEmail{{Address: "a@a.com"}}, Active: true, Type: "user"},
		},
		Rooms: []RocketChatRoom{
			{ID: "r1", Type: "c", Name: "general"},
		},
		Subscriptions: []RocketChatSubscription{
			{RoomID: "r1", User: RCMessageUser{ID: "u1", Username: "alice"}},
			{RoomID: "r1", User: RCMessageUser{ID: "g1", Username: "guesty"}},
		},
		Messages: []RocketChatMessage{
			{ID: "m1", RoomID: "r1", User: RCMessageUser{ID: "g1", Username: "guesty"}, Message: "hello from guest", Timestamp: now},
			{ID: "m2", RoomID: "r1", User: RCMessageUser{ID: "u1", Username: "alice"}, Message: "hi", Timestamp: now.Add(time.Second)},
		},
	}

	tr := NewTransformer("myteam", newLogger())
	tr.Transform(parsed, true, false, "", GuestHandlingSkip)

	// The guest is dropped along with its post and membership.
	assert.Nil(t, tr.Intermediate.UsersById["g1"])
	require.Len(t, tr.Intermediate.PublicChannels, 1)
	assert.NotContains(t, tr.Intermediate.PublicChannels[0].Members, "g1")
	require.Len(t, tr.Intermediate.Posts, 1)
	assert.Equal(t, "alice", tr.Intermediate.Posts[0].User)
	assert.Positive(t, tr.droppedPostRefs)
	assert.Positive(t, tr.droppedMembershipRefs)
}
