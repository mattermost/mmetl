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

	usersAsMaps := []map[string]any{}
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
			// Don't truncate here - splitting will happen later in the transformation phase
			posts[channelName][postIdx].Text = result
		}
	}

	t.Logger.Infof("Slack Import: Converted markdown")

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

			switch file.Name {
			case "channels.json":
				slackExport.PublicChannels, _ = t.SlackParseChannels(reader, model.ChannelTypeOpen)
				slackExport.Channels = append(slackExport.Channels, slackExport.PublicChannels...)
			case "dms.json":
				slackExport.DirectChannels, _ = t.SlackParseChannels(reader, model.ChannelTypeDirect)
				slackExport.Channels = append(slackExport.Channels, slackExport.DirectChannels...)
			case "groups.json":
				slackExport.PrivateChannels, _ = t.SlackParseChannels(reader, model.ChannelTypePrivate)
				slackExport.Channels = append(slackExport.Channels, slackExport.PrivateChannels...)
			case "mpims.json":
				slackExport.GroupChannels, _ = t.SlackParseChannels(reader, model.ChannelTypeGroup)
				slackExport.Channels = append(slackExport.Channels, slackExport.GroupChannels...)
			case "users.json":
				usersJSONFileName := os.Getenv("USERS_JSON_FILE")
				if usersJSONFileName != "" {
					reader.Close()
					reader, err = os.Open(usersJSONFileName)
					if err != nil {
						return errors.Wrap(err, "failed to read users file from USERS_JSON_FILE")
					}
					defer reader.Close()
				}

				users, _ := t.SlackParseUsers(reader)
				slackExport.Users = users
			default:
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
