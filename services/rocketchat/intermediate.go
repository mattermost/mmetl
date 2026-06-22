package rocketchat

import "time"

// RocketChatUser maps to the MongoDB `users` collection.
type RocketChatUser struct {
	ID        string    `bson:"_id"`
	Username  string    `bson:"username"`
	Name      string    `bson:"name"`
	Emails    []RCEmail `bson:"emails"`
	Active    bool      `bson:"active"`
	Roles     []string  `bson:"roles"`
	Type      string    `bson:"type"` // "user", "bot"
	CreatedAt time.Time `bson:"createdAt"`
}

// RCEmail is an email address entry on a RocketChatUser.
type RCEmail struct {
	Address  string `bson:"address"`
	Verified bool   `bson:"verified"`
}

// RocketChatRoom maps to the MongoDB `rocketchat_room` collection.
type RocketChatRoom struct {
	ID          string    `bson:"_id"`
	Type        string    `bson:"t"` // c, p, d
	Name        string    `bson:"name"`
	FName       string    `bson:"fname"`       // Display name (absent on DM rooms)
	Description *string   `bson:"description"` // Can be null — use pointer
	Topic       string    `bson:"topic"`
	UIDs        []string  `bson:"uids"`      // User IDs for DM rooms
	Usernames   []string  `bson:"usernames"` // Usernames for DM rooms (parallel to UIDs)
	Archived    bool      `bson:"archived"`
	Encrypted   bool      `bson:"encrypted"` // If true, skip — E2E encrypted room
	ParentRID   string    `bson:"prid"`      // If present, this is a discussion room
	TeamID      string    `bson:"teamId"`    // RC team association
	TeamMain    bool      `bson:"teamMain"`  // Whether this is the main channel of an RC team
	ReadOnly    bool      `bson:"ro"`
	CreatedAt   time.Time `bson:"ts"`
}

// RocketChatMessage maps to the MongoDB `rocketchat_message` collection.
type RocketChatMessage struct {
	ID            string                    `bson:"_id"`
	RoomID        string                    `bson:"rid"`
	User          RCMessageUser             `bson:"u"`
	Message       string                    `bson:"msg"`
	Type          string                    `bson:"t"` // System message type (empty for regular messages)
	Timestamp     time.Time                 `bson:"ts"`
	EditedAt      *time.Time                `bson:"editedAt"`
	ThreadID      string                    `bson:"tmid"`   // Thread parent message ID
	ThreadCount   int                       `bson:"tcount"` // Number of replies (only on root)
	ThreadLastMsg *time.Time                `bson:"tlm"`    // Thread last message timestamp (only on root)
	Reactions     map[string]RCReactionInfo `bson:"reactions"`
	Mentions      []RCMention               `bson:"mentions"`
	Channels      []RCChannelRef            `bson:"channels"`
	Files         []RCFileRef               `bson:"files"`
	Replies       []string                  `bson:"replies"` // User IDs who replied (only on root)
	Pinned        bool                      `bson:"pinned"`
	Groupable     bool                      `bson:"groupable"`
}

// RCMessageUser is the nested user object on a message.
type RCMessageUser struct {
	ID       string `bson:"_id"`
	Username string `bson:"username"`
	Name     string `bson:"name"`
}

// RCReactionInfo holds the list of usernames who reacted with a given emoji.
type RCReactionInfo struct {
	Usernames []string `bson:"usernames"`
}

// RCMention is a @mention reference within a message.
type RCMention struct {
	ID       string `bson:"_id"`
	Username string `bson:"username"`
	Name     string `bson:"name"`
	Type     string `bson:"type"` // "user"
}

// RCChannelRef is a #channel reference within a message.
type RCChannelRef struct {
	ID    string `bson:"_id"`
	FName string `bson:"fname"`
	Name  string `bson:"name"`
}

// RCFileRef is a file attachment reference on a message.
type RCFileRef struct {
	ID   string `bson:"_id"`
	Name string `bson:"name"`
	Type string `bson:"type"` // MIME type
	Size int64  `bson:"size"`
}

// RocketChatSubscription maps to the MongoDB `rocketchat_subscription` collection.
type RocketChatSubscription struct {
	RoomID   string        `bson:"rid"`
	User     RCMessageUser `bson:"u"` // Nested object {_id, username, name}
	Roles    []string      `bson:"roles"`
	Favorite bool          `bson:"f"`
	LastSeen time.Time     `bson:"ls"`
	RoomType string        `bson:"t"`    // Room type: c, p, d
	Name     string        `bson:"name"` // Room name
}

// RocketChatUpload maps to the MongoDB `rocketchat_uploads` collection.
type RocketChatUpload struct {
	ID          string    `bson:"_id"`
	Name        string    `bson:"name"`
	Type        string    `bson:"type"` // MIME type e.g. "image/jpeg"
	Size        int64     `bson:"size"`
	RoomID      string    `bson:"rid"`
	UserID      string    `bson:"userId"`
	Store       string    `bson:"store"` // "GridFS:Uploads", "FileSystem", etc. Match prefix "GridFS:"
	Path        string    `bson:"path"`  // URL path like "/file-upload/{id}/{name}"
	Description string    `bson:"description"`
	Complete    bool      `bson:"complete"` // Only process if true
	UploadedAt  time.Time `bson:"uploadedAt"`
}
