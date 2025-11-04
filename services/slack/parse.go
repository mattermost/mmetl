package slack

import (
	"archive/zip"
	"encoding/json"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/pkg/errors"
)

type SlackChannel struct {
	Id      string          `json:"id"`
	Name    string          `json:"name"`
	Creator string          `json:"creator"`
	Members []string        `json:"members"`
	Purpose SlackChannelSub `json:"purpose"`
	Topic   SlackChannelSub `json:"topic"`
	Type    model.ChannelType
}

type SlackChannelSub struct {
	Value string `json:"value"`
}

type SlackProfile struct {
	BotID    string `json:"bot_id"`
	RealName string `json:"real_name"`
	Email    string `json:"email"`
	Title    string `json:"title"`
}

type SlackUser struct {
	Id       string       `json:"id"`
	Username string       `json:"name"`
	IsBot    bool         `json:"is_bot"`
	Profile  SlackProfile `json:"profile"`
	Deleted  bool         `json:"deleted"`
}

type SlackFile struct {
	Id          string `json:"id"`
	Name        string `json:"name"`
	Size        int64  `json:"size"`
	DownloadURL string `json:"url_private_download"`
}

type SlackReaction struct {
	Name  string   `json:"name"`
	Count int64    `json:"count"`
	Users []string `json:"users"`
}

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

func (p *SlackPost) IsPlainMessage() bool {
	return p.Type == "message" && (p.SubType == "" || p.SubType == "file_share" || p.SubType == "thread_broadcast")
}

func (p *SlackPost) IsFileComment() bool {
	return p.Type == "message" && p.SubType == "file_comment"
}

func (p *SlackPost) IsBotMessage() bool {
	return p.Type == "message" && (p.SubType == "bot_message" || p.SubType == "tombstone")
}

func (p *SlackPost) IsJoinLeaveMessage() bool {
	return p.Type == "message" && (p.SubType == "channel_join" || p.SubType == "channel_leave")
}

func (p *SlackPost) IsMeMessage() bool {
	return p.Type == "message" && p.SubType == "me_message"
}

func (p *SlackPost) IsChannelTopicMessage() bool {
	return p.Type == "message" && p.SubType == "channel_topic"
}

func (p *SlackPost) IsChannelPurposeMessage() bool {
	return p.Type == "message" && p.SubType == "channel_purpose"
}

func (p *SlackPost) IsChannelNameMessage() bool {
	return p.Type == "message" && p.SubType == "channel_name"
}

func (p *SlackPost) isHuddleThread() bool {
	return p.Type == "message" && p.SubType == "huddle_thread"
}

type SlackComment struct {
	User    string `json:"user"`
	Comment string `json:"comment"`
}

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

func (t *Transformer) SlackParseUsers(data io.Reader) ([]SlackUser, error) {
	var users []SlackUser

	b, err := io.ReadAll(data)
	if err != nil {
		return users, err
	}

	t.Logger.Debugf("SlackParseUsers: Raw json input data: %s", string(b))

	err = json.Unmarshal(b, &users)
	if err != nil {
		t.Logger.Warnf("Slack Import: Error occurred when parsing some Slack users. Import may work anyway. err=%v", err)

		// This returns errors that are ignored
		return users, err
	}

	usersAsMaps := []map[string]interface{}{}
	_ = json.Unmarshal(b, &usersAsMaps)

	for i, u := range users {
		t.Logger.Debugf("SlackParseUsers: Parsed user struct data %+v", u)
		t.Logger.Debugf("SlackParseUsers: Parsed user data as map %+v", usersAsMaps[i])
	}

	b, err = json.Marshal(users)
	if err != nil {
		return users, err
	}

	t.Logger.Debugf("SlackParseUsers: Marshalled users struct data: %s", string(b))

	return users, nil
}

func (t *Transformer) SlackParseChannels(data io.Reader, channelType model.ChannelType) ([]SlackChannel, error) {
	decoder := json.NewDecoder(data)

	var channels []SlackChannel
	if err := decoder.Decode(&channels); err != nil {
		t.Logger.Warnf("Slack Import: Error occurred when parsing some Slack channels. Import may work anyway. err=%v", err)
		return channels, err
	}

	for i := range channels {
		channels[i].Type = channelType
	}

	return channels, nil
}

func (t *Transformer) SlackParsePosts(data io.Reader) ([]SlackPost, error) {
	decoder := json.NewDecoder(data)

	var posts []SlackPost
	if err := decoder.Decode(&posts); err != nil {
		t.Logger.Warnf("Slack Import: Error occurred when parsing some Slack posts. Import may work anyway. err=%v", err)
		return posts, err
	}
	return posts, nil
}

func (t *Transformer) SlackConvertUserMentions(users []SlackUser, posts map[string][]SlackPost) map[string][]SlackPost {
	var regexes = make(map[string]*regexp.Regexp, len(users))
	for _, user := range users {
		r, err := regexp.Compile("<@" + user.Id + `(\|` + user.Username + ")?>")
		if err != nil {
			t.Logger.Infof("Slack Import: Unable to compile the @mention, matching regular expression for the Slack user. username=%s user_id=%s", user.Username, user.Id)
			continue
		}
		regexes["@"+user.Username] = r
	}

	// Special cases.
	regexes["@here"], _ = regexp.Compile("<(!|@)here>")
	regexes["@channel"], _ = regexp.Compile("<!channel>")
	regexes["@all"], _ = regexp.Compile("<!everyone>")

	convertCount := 0
	for channelName, channelPosts := range posts {
		convertCount++
		t.Logger.Debugf("Slack Import: converting user mentions for channel %s. %v of %v", channelName, convertCount, len(posts))
		for postIdx, post := range channelPosts {
			for mention, r := range regexes {
				post.Text = r.ReplaceAllString(post.Text, mention)
				posts[channelName][postIdx] = post

				if post.Attachments != nil {
					for _, attachment := range post.Attachments {
						attachment.Fallback = r.ReplaceAllString(attachment.Fallback, mention)
					}
				}
			}
		}
	}

	t.Logger.Infof("Slack Import: Converted user mentions")
	return posts
}

func (t *Transformer) SlackConvertChannelMentions(channels []SlackChannel, posts map[string][]SlackPost) map[string][]SlackPost {
	var regexes = make(map[string]*regexp.Regexp, len(channels))
	for _, channel := range channels {
		r, err := regexp.Compile("<#" + channel.Id + `(\|` + channel.Name + ")?>")
		if err != nil {
			t.Logger.Infof("Slack Import: Unable to compile the !channel, matching regular expression for the Slack channel. channel_id=%s channel_name=%s", channel.Id, channel.Name)
			continue
		}
		regexes["~"+channel.Name] = r
	}

	convertCount := 0
	for channelName, channelPosts := range posts {
		convertCount++
		t.Logger.Debugf("Slack Import: converting channel mentions for channel %s. %v of %v", channelName, convertCount, len(posts))
		for postIdx, post := range channelPosts {
			for channelReplace, r := range regexes {
				post.Text = r.ReplaceAllString(post.Text, channelReplace)
				posts[channelName][postIdx] = post

				if post.Attachments != nil {
					for _, attachment := range post.Attachments {
						attachment.Fallback = r.ReplaceAllString(attachment.Fallback, channelReplace)
					}
				}
			}
		}
	}

	t.Logger.Infof("Slack Import: Converted channel mentions")

	return posts
}

func (t *Transformer) SlackConvertPostsMarkup(posts map[string][]SlackPost) map[string][]SlackPost {
	regexReplaceAllString := []struct {
		regex *regexp.Regexp
		rpl   string
	}{
		// URL
		{
			regexp.MustCompile(`<([^|<>]+)\|([^|<>]+)>`),
			"[$2]($1)",
		},
		// bold
		{
			regexp.MustCompile(`(^|[\s.;,])\*(\S[^*\n]+)\*`),
			"$1**$2**",
		},
		// strikethrough
		{
			regexp.MustCompile(`(^|[\s.;,])\~(\S[^~\n]+)\~`),
			"$1~~$2~~",
		},
		// single paragraph blockquote
		// Slack converts > character to &gt;
		{
			regexp.MustCompile(`(?sm)^&gt;`),
			">",
		},
	}

	regexReplaceAllStringFunc := []struct {
		regex *regexp.Regexp
		fn    func(string) string
	}{
		// multiple paragraphs blockquotes
		{
			regexp.MustCompile(`(?sm)^>&gt;&gt;(.+)$`),
			func(src string) string {
				// remove >>> prefix, might have leading \n
				prefixRegexp := regexp.MustCompile(`^([\n])?>&gt;&gt;(.*)`)
				src = prefixRegexp.ReplaceAllString(src, "$1$2")
				// append > to start of line
				appendRegexp := regexp.MustCompile(`(?m)^`)
				return appendRegexp.ReplaceAllString(src, ">$0")
			},
		},
	}

	convertCount := 0
	for channelName, channelPosts := range posts {
		convertCount++
		t.Logger.Debugf("Slack Import: converting markdown for channel %s. %v of %v", channelName, convertCount, len(posts))

		for postIdx, post := range channelPosts {
			result := post.Text

			for _, rule := range regexReplaceAllString {
				result = rule.regex.ReplaceAllString(result, rule.rpl)
			}

			for _, rule := range regexReplaceAllStringFunc {
				result = rule.regex.ReplaceAllStringFunc(result, rule.fn)
			}
			posts[channelName][postIdx].Text = result
		}
	}

	t.Logger.Infof("Slack Import: Converted markdown")

	return posts
}

// DetectWorkspaces scans the zip file for team folders and returns a list of workspace names
func DetectWorkspaces(zipReader *zip.Reader) []string {
	workspaceMap := make(map[string]bool)
	for _, file := range zipReader.File {
		parts := strings.Split(file.Name, "/")
		// Only include directories (not files) by checking if there's a third segment
		// This filters out system files like .DS_Store and only includes actual workspace directories
		if len(parts) >= 3 && parts[0] == "teams" && parts[1] != "" && !strings.HasPrefix(parts[1], ".") {
			workspaceMap[parts[1]] = true
		}
	}

	workspaces := make([]string, 0, len(workspaceMap))
	for workspace := range workspaceMap {
		workspaces = append(workspaces, workspace)
	}
	return workspaces
}

// getFilePrefix returns the appropriate file prefix based on the workspace name
// Returns empty string for single workspace exports, "teams/workspacename/" for multi-workspace exports
func (t *Transformer) getFilePrefix() string {
	if t.WorkspaceName == "" {
		return ""
	}
	return "teams/" + t.WorkspaceName + "/"
}

// matchesWorkspace checks if a file path belongs to the selected workspace
func (t *Transformer) matchesWorkspace(filePath string) bool {
	prefix := t.getFilePrefix()

	// Single workspace export (no prefix)
	if prefix == "" {
		// File should NOT be in teams/ directory
		return !strings.HasPrefix(filePath, "teams/")
	}

	// Multi-workspace export (has prefix)
	return strings.HasPrefix(filePath, prefix)
}

func (t *Transformer) ParseSlackExportFile(zipReader *zip.Reader, skipConvertPosts bool) (*SlackExport, error) {
	slackExport := SlackExport{TeamName: t.TeamName}
	slackExport.Posts = make(map[string][]SlackPost)
	slackExport.Uploads = make(map[string]*zip.File)
	numFiles := len(zipReader.File)

	prefix := t.getFilePrefix()

	for i, file := range zipReader.File {
		err := func(i int, file *zip.File) error {
			t.Logger.Infof("Processing file %d of %d: %s", i+1, numFiles, file.Name)

			// Skip files that don't belong to the selected workspace
			if !t.matchesWorkspace(file.Name) {
				return nil
			}

			reader, err := file.Open()
			if err != nil {
				return err
			}
			defer reader.Close()

			switch file.Name {
			case prefix + "channels.json":
				slackExport.PublicChannels, _ = t.SlackParseChannels(reader, model.ChannelTypeOpen)
				slackExport.Channels = append(slackExport.Channels, slackExport.PublicChannels...)
			case prefix + "dms.json":
				slackExport.DirectChannels, _ = t.SlackParseChannels(reader, model.ChannelTypeDirect)
				slackExport.Channels = append(slackExport.Channels, slackExport.DirectChannels...)
			case prefix + "groups.json":
				slackExport.PrivateChannels, _ = t.SlackParseChannels(reader, model.ChannelTypePrivate)
				slackExport.Channels = append(slackExport.Channels, slackExport.PrivateChannels...)
			case prefix + "mpims.json":
				slackExport.GroupChannels, _ = t.SlackParseChannels(reader, model.ChannelTypeGroup)
				slackExport.Channels = append(slackExport.Channels, slackExport.GroupChannels...)
			case prefix + "users.json":
				usersJSONFileName := os.Getenv("USERS_JSON_FILE")
				if usersJSONFileName != "" {
					reader.Close()
					reader, err = os.Open(usersJSONFileName)
					if err != nil {
						return errors.Wrap(err, "failed to read users file from USERS_JSON_FILE")
					}
				}

				users, _ := t.SlackParseUsers(reader)
				slackExport.Users = users
			default:
				spl := strings.Split(file.Name, "/")
				// Handle channel message files
				// Single workspace: channel/date.json (2 segments)
				// Multi workspace: teams/team1/channel/date.json (4 segments)
				if (len(spl) == 2 || len(spl) == 4) && strings.HasSuffix(spl[len(spl)-1], ".json") {
					newposts, _ := t.SlackParsePosts(reader)
					// Extract channel name (last directory before the .json file)
					channel := spl[len(spl)-2]
					if _, ok := slackExport.Posts[channel]; !ok {
						slackExport.Posts[channel] = newposts
					} else {
						slackExport.Posts[channel] = append(slackExport.Posts[channel], newposts...)
					}
				} else if (len(spl) == 3 || len(spl) == 5) && strings.Contains(file.Name, "__uploads") {
					// Handle uploads
					// Single workspace: __uploads/file/name (3 segments)
					// Multi workspace: teams/team1/__uploads/file/name (5 segments)
					uploadFileId := spl[len(spl)-2]
					slackExport.Uploads[uploadFileId] = file
				}
			}

			return nil
		}(i, file)

		if err != nil {
			return nil, err
		}
	}

	if !skipConvertPosts {
		t.Logger.Info("Converting post mentions and markup")
		start := time.Now()
		slackExport.Posts = t.SlackConvertUserMentions(slackExport.Users, slackExport.Posts)
		slackExport.Posts = t.SlackConvertChannelMentions(slackExport.Channels, slackExport.Posts)
		slackExport.Posts = t.SlackConvertPostsMarkup(slackExport.Posts)
		elapsed := time.Since(start)
		t.Logger.Debugf("Converting mentions finished (%s)", elapsed)
	}

	return &slackExport, nil
}
