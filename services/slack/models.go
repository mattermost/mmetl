package slack

import (
	"archive/zip"

	"github.com/mattermost/mattermost/server/public/model"
)

// SlackChannel represents a Slack channel in the export
type SlackChannel struct {
	Id      string          `json:"id"`
	Name    string          `json:"name"`
	Creator string          `json:"creator"`
	Members []string        `json:"members"`
	Purpose SlackChannelSub `json:"purpose"`
	Topic   SlackChannelSub `json:"topic"`
	Type    model.ChannelType
}

// SlackChannelSub represents a sub-field in Slack channel data (purpose/topic)
type SlackChannelSub struct {
	Value string `json:"value"`
}

// SlackProfile represents a Slack user profile
type SlackProfile struct {
	BotID    string `json:"bot_id"`
	RealName string `json:"real_name"`
	Email    string `json:"email"`
	Title    string `json:"title"`
}

// SlackUser represents a Slack user in the export
type SlackUser struct {
	Id       string       `json:"id"`
	Username string       `json:"name"`
	IsBot    bool         `json:"is_bot"`
	Profile  SlackProfile `json:"profile"`
	Deleted  bool         `json:"deleted"`
}

// SlackFile represents an uploaded file in Slack
type SlackFile struct {
	Id          string `json:"id"`
	Name        string `json:"name"`
	Size        int64  `json:"size"`
	DownloadURL string `json:"url_private_download"`
}

// SlackReaction represents a reaction on a Slack post
type SlackReaction struct {
	Name  string   `json:"name"`
	Count int64    `json:"count"`
	Users []string `json:"users"`
}

// SlackRoom represents a Slack huddle/call room
type SlackRoom struct {
	Id                 string   `json:"id"`
	Name               string   `json:"name"`
	CreatedBy          string   `json:"created_by"`
	DateStart          int64    `json:"date_start"`
	DateEnd            int64    `json:"date_end"`
	Participants       []string `json:"participants"`
	ParticipantHistory []string `json:"participant_history"`
	ThreadTS           string   `json:"thread_root_ts"`
	Channels           []string `json:"channels"`
	IsDMCall           bool     `json:"is_dm_call"`
	WasRejected        bool     `json:"was_rejected"`
	WasMissed          bool     `json:"was_missed"`
	WasAccepted        bool     `json:"was_accepted"`
	HasEnded           bool     `json:"has_ended"`
}

// SlackPost represents a Slack message/post in the export
type SlackPost struct {
	User        string                   `json:"user"`
	BotId       string                   `json:"bot_id"`
	BotUsername string                   `json:"username"`
	Text        string                   `json:"text"`
	TimeStamp   string                   `json:"ts"`
	ThreadTS    string                   `json:"thread_ts"`
	Type        string                   `json:"type"`
	SubType     string                   `json:"subtype"`
	Comment     *SlackComment            `json:"comment"`
	Upload      bool                     `json:"upload"`
	File        *SlackFile               `json:"file"`
	Files       []*SlackFile             `json:"files"`
	Attachments []*model.SlackAttachment `json:"attachments"`
	Reactions   []*SlackReaction         `json:"reactions"`
	Room        *SlackRoom               `json:"room"`
}

// IsPlainMessage returns true if the post is a plain message
func (p *SlackPost) IsPlainMessage() bool {
	return p.Type == "message" && (p.SubType == "" || p.SubType == "file_share" || p.SubType == "thread_broadcast")
}

// IsFileComment returns true if the post is a file comment
func (p *SlackPost) IsFileComment() bool {
	return p.Type == "message" && p.SubType == "file_comment"
}

// IsBotMessage returns true if the post is from a bot
func (p *SlackPost) IsBotMessage() bool {
	return p.Type == "message" && (p.SubType == "bot_message" || p.SubType == "tombstone")
}

// IsJoinLeaveMessage returns true if the post is a join/leave message
func (p *SlackPost) IsJoinLeaveMessage() bool {
	return p.Type == "message" && (p.SubType == "channel_join" || p.SubType == "channel_leave")
}

// IsMeMessage returns true if the post is a /me message
func (p *SlackPost) IsMeMessage() bool {
	return p.Type == "message" && p.SubType == "me_message"
}

// IsChannelTopicMessage returns true if the post is a channel topic change
func (p *SlackPost) IsChannelTopicMessage() bool {
	return p.Type == "message" && p.SubType == "channel_topic"
}

// IsChannelPurposeMessage returns true if the post is a channel purpose change
func (p *SlackPost) IsChannelPurposeMessage() bool {
	return p.Type == "message" && p.SubType == "channel_purpose"
}

// IsChannelNameMessage returns true if the post is a channel name change
func (p *SlackPost) IsChannelNameMessage() bool {
	return p.Type == "message" && p.SubType == "channel_name"
}

// isHuddleThread returns true if the post is a huddle thread
func (p *SlackPost) isHuddleThread() bool {
	return p.Type == "message" && p.SubType == "huddle_thread"
}

// SlackComment represents a comment on a Slack file
type SlackComment struct {
	User    string `json:"user"`
	Comment string `json:"comment"`
}

// SlackExport represents the complete Slack export data
type SlackExport struct {
	TeamName        string
	Channels        []SlackChannel
	PublicChannels  []SlackChannel
	PrivateChannels []SlackChannel
	GroupChannels   []SlackChannel
	DirectChannels  []SlackChannel
	Users           []SlackUser
	Posts           map[string][]SlackPost
	Uploads         map[string]*zip.File
}

