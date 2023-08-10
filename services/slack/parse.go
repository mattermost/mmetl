package slack

import (
	"archive/zip"
	"encoding/json"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/mattermost/mattermost-server/v6/model"
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

	t.Logger.Debugf("SlackParseUsers: Parsed users struct data: %+v", users)

	for _, u := range users {
		t.Logger.Debugf("SlackParseUsers: Parsed user struct data %+v", u)
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
	regexes["@here"], _ = regexp.Compile(`<!here\|@here>`)
	regexes["@channel"], _ = regexp.Compile("<!channel>")
	regexes["@all"], _ = regexp.Compile("<!everyone>")

	for channelName, channelPosts := range posts {
		for postIdx, post := range channelPosts {
			for mention, r := range regexes {
				post.Text = r.ReplaceAllString(post.Text, mention)
				posts[channelName][postIdx] = post
			}
		}
	}

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

	for channelName, channelPosts := range posts {
		for postIdx, post := range channelPosts {
			for channelReplace, r := range regexes {
				post.Text = r.ReplaceAllString(post.Text, channelReplace)
				posts[channelName][postIdx] = post
			}
		}
	}

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

	for channelName, channelPosts := range posts {
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

	return posts
}

func (t *Transformer) ParseSlackExportFile(zipReader *zip.Reader, skipConvertPosts bool) (*SlackExport, error) {
	slackExport := SlackExport{TeamName: t.TeamName}
	slackExport.Posts = make(map[string][]SlackPost)
	slackExport.Uploads = make(map[string]*zip.File)
	numFiles := len(zipReader.File)

	for i, file := range zipReader.File {
		err := func(i int, file *zip.File) error {
			t.Logger.Infof("Processing file %d of %d: %s", i+1, numFiles, file.Name)

			reader, err := file.Open()
			if err != nil {
				return err
			}
			defer reader.Close()

			if file.Name == "channels.json" {
				slackExport.PublicChannels, _ = t.SlackParseChannels(reader, model.ChannelTypeOpen)
				slackExport.Channels = append(slackExport.Channels, slackExport.PublicChannels...)
			} else if file.Name == "dms.json" {
				slackExport.DirectChannels, _ = t.SlackParseChannels(reader, model.ChannelTypeDirect)
				slackExport.Channels = append(slackExport.Channels, slackExport.DirectChannels...)
			} else if file.Name == "groups.json" {
				slackExport.PrivateChannels, _ = t.SlackParseChannels(reader, model.ChannelTypePrivate)
				slackExport.Channels = append(slackExport.Channels, slackExport.PrivateChannels...)
			} else if file.Name == "mpims.json" {
				slackExport.GroupChannels, _ = t.SlackParseChannels(reader, model.ChannelTypeGroup)
				slackExport.Channels = append(slackExport.Channels, slackExport.GroupChannels...)
			} else if file.Name == "users.json" {
				users, _ := t.SlackParseUsers(reader)
				slackExport.Users = users
			} else {
				spl := strings.Split(file.Name, "/")
				if len(spl) == 2 && strings.HasSuffix(spl[1], ".json") {
					newposts, _ := t.SlackParsePosts(reader)
					channel := spl[0]
					if _, ok := slackExport.Posts[channel]; !ok {
						slackExport.Posts[channel] = newposts
					} else {
						slackExport.Posts[channel] = append(slackExport.Posts[channel], newposts...)
					}
				} else if len(spl) == 3 && spl[0] == "__uploads" {
					slackExport.Uploads[spl[1]] = file
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
