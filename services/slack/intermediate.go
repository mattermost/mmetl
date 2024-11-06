package slack

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	"golang.org/x/text/unicode/norm"
)

const attachmentsInternal = "bulk-export-attachments"

var exitFunc func(code int) = os.Exit

type IntermediateChannel struct {
	Id               string            `json:"id"`
	OriginalName     string            `json:"original_name"`
	Name             string            `json:"name"`
	DisplayName      string            `json:"display_name"`
	Members          []string          `json:"members"`
	MembersUsernames []string          `json:"members_usernames"`
	Purpose          string            `json:"purpose"`
	Header           string            `json:"header"`
	Topic            string            `json:"topic"`
	Type             model.ChannelType `json:"type"`
}

func (c *IntermediateChannel) Sanitise(logger log.FieldLogger) {
	if c.Type == model.ChannelTypeDirect {
		return
	}

	c.Name = strings.Trim(c.Name, "_-")
	if len(c.Name) > model.ChannelNameMaxLength {
		logger.Warnf("Channel %s handle exceeds the maximum length. It will be truncated when imported.", c.DisplayName)
		c.Name = c.Name[0:model.ChannelNameMaxLength]
	}
	if len(c.Name) == 1 {
		c.Name = "slack-channel-" + c.Name
	}
	if !isValidChannelNameCharacters(c.Name) {
		c.Name = strings.ToLower(c.Id)
	}

	c.DisplayName = strings.Trim(c.DisplayName, "_-")
	if utf8.RuneCountInString(c.DisplayName) > model.ChannelDisplayNameMaxRunes {
		logger.Warnf("Channel %s display name exceeds the maximum length. It will be truncated when imported.", c.DisplayName)
		c.DisplayName = truncateRunes(c.DisplayName, model.ChannelDisplayNameMaxRunes)
	}
	if len(c.DisplayName) == 1 {
		c.DisplayName = "slack-channel-" + c.DisplayName
	}
	if !isValidChannelNameCharacters(c.DisplayName) {
		c.DisplayName = strings.ToLower(c.Id)
	}

	if utf8.RuneCountInString(c.Purpose) > model.ChannelPurposeMaxRunes {
		logger.Warnf("Channel %s purpose exceeds the maximum length. It will be truncated when imported.", c.DisplayName)
		c.Purpose = truncateRunes(c.Purpose, model.ChannelPurposeMaxRunes)
	}

	if utf8.RuneCountInString(c.Header) > model.ChannelHeaderMaxRunes {
		logger.Warnf("Channel %s header exceeds the maximum length. It will be truncated when imported.", c.DisplayName)
		c.Header = truncateRunes(c.Header, model.ChannelHeaderMaxRunes)
	}
}

type IntermediateUser struct {
	Id          string   `json:"id"`
	Username    string   `json:"username"`
	FirstName   string   `json:"first_name"`
	LastName    string   `json:"last_name"`
	Position    string   `json:"position"`
	Email       string   `json:"email"`
	Password    string   `json:"password"`
	Memberships []string `json:"memberships"`
	DeleteAt    int64    `json:"delete_at"`
}

func (u *IntermediateUser) Sanitise(logger log.FieldLogger, defaultEmailDomain string, skipEmptyEmails bool) {
	logger.Debugf("TransformUsers: Sanitise: IntermediateUser receiver: %+v", u)

	if u.Email == "" {
		if skipEmptyEmails {
			logger.Warnf("User %s does not have an email address in the Slack export. Using blank email address due to --skip-empty-emails flag.", u.Username)
			return
		}

		if defaultEmailDomain != "" {
			u.Email = u.Username + "@" + defaultEmailDomain
			logger.Warnf("User %s does not have an email address in the Slack export. Used %s as a placeholder. The user should update their email address once logged in to the system.", u.Username, u.Email)
		} else {
			msg := fmt.Sprintf("User %s does not have an email address in the Slack export. Please provide an email domain through the --default-email-domain flag, to assign this user's email address. Alternatively, use the --skip-empty-emails flag to set the user's email to an empty string.", u.Username)
			logger.Error(msg)
			fmt.Println(msg)
			exitFunc(1)
		}
	}

	if utf8.RuneCountInString(u.FirstName) > model.UserFirstNameMaxRunes {
		logger.Warnf("User %s first name exceeds the maximum length. It will be truncated when imported.", u.Username)
		u.FirstName = truncateRunes(u.FirstName, model.UserFirstNameMaxRunes)
	}

	if utf8.RuneCountInString(u.LastName) > model.UserLastNameMaxRunes {
		logger.Warnf("User %s last name exceeds the maximum length. It will be truncated when imported.", u.Username)
		u.LastName = truncateRunes(u.LastName, model.UserLastNameMaxRunes)
	}

	if utf8.RuneCountInString(u.Position) > model.UserPositionMaxRunes {
		logger.Warnf("User %s position exceeds the maximum length. It will be truncated when imported.", u.Username)
		u.Position = truncateRunes(u.Position, model.UserPositionMaxRunes)
	}
}

type IntermediatePost struct {
	User           string                `json:"user"`
	Channel        string                `json:"channel"`
	Message        string                `json:"message"`
	Props          model.StringInterface `json:"props"`
	CreateAt       int64                 `json:"create_at"`
	Type           string                `json:"type"`
	Attachments    []string              `json:"attachments"`
	Replies        []*IntermediatePost   `json:"replies"`
	IsDirect       bool                  `json:"is_direct"`
	ChannelMembers []string              `json:"channel_members"`
}

type Intermediate struct {
	PublicChannels  []*IntermediateChannel       `json:"public_channels"`
	PrivateChannels []*IntermediateChannel       `json:"private_channels"`
	GroupChannels   []*IntermediateChannel       `json:"group_channels"`
	DirectChannels  []*IntermediateChannel       `json:"direct_channels"`
	UsersById       map[string]*IntermediateUser `json:"users"`
	Posts           []*IntermediatePost          `json:"posts"`
}

func (t *Transformer) TransformUsers(users []SlackUser, skipEmptyEmails bool, defaultEmailDomain string) {
	t.Logger.Info("Transforming users")

	t.Logger.Debugf("TransformUsers: Input SlackUser structs: %+v", users)

	resultUsers := map[string]*IntermediateUser{}
	for _, user := range users {
		var deleteAt int64 = 0
		if user.Deleted {
			deleteAt = model.GetMillis()
		}

		firstName := ""
		lastName := ""
		if user.Profile.RealName != "" {
			names := strings.Split(user.Profile.RealName, " ")
			firstName = names[0]
			lastName = strings.Join(names[1:], " ")
		}

		t.Logger.Debugf("TransformUsers: SlackUser struct: %+v", user)
		t.Logger.Debugf("TransformUsers: SlackUser.Profile struct: %+v", user.Profile)

		newUser := &IntermediateUser{
			Id:        user.Id,
			Username:  user.Username,
			FirstName: firstName,
			LastName:  lastName,
			Position:  user.Profile.Title,
			Email:     user.Profile.Email,
			Password:  model.NewId(),
			DeleteAt:  deleteAt,
		}

		t.Logger.Debugf("TransformUsers: newUser IntermediateUser struct: %+v", newUser)

		if user.IsBot {
			newUser.Id = user.Profile.BotID
		}

		newUser.Sanitise(t.Logger, defaultEmailDomain, skipEmptyEmails)
		resultUsers[newUser.Id] = newUser
		t.Logger.Debugf("Slack user with email %s and password %s has been imported.", newUser.Email, newUser.Password)
	}

	t.Intermediate.UsersById = resultUsers
}

func filterValidMembers(members []string, users map[string]*IntermediateUser) []string {
	validMembers := []string{}
	for _, member := range members {
		if _, ok := users[member]; ok {
			validMembers = append(validMembers, member)
		}
	}

	return validMembers
}

func getOriginalName(channel SlackChannel) string {
	if channel.Name == "" {
		return channel.Id
	} else {
		return channel.Name
	}
}

func (t *Transformer) TransformChannels(channels []SlackChannel) []*IntermediateChannel {
	resultChannels := []*IntermediateChannel{}
	for _, channel := range channels {
		if t.ChannelOnly != "" && channel.Name != t.ChannelOnly {
			t.Logger.Infof("--channel-only %s active - skipping channel %s", t.ChannelOnly, channel.Name)
			continue
		}

		validMembers := filterValidMembers(channel.Members, t.Intermediate.UsersById)
		if (channel.Type == model.ChannelTypeDirect || channel.Type == model.ChannelTypeGroup) && len(validMembers) <= 1 {
			t.Logger.Warnf("Bulk export for direct channels containing a single member is not supported. Not importing channel %s", channel.Name)
			continue
		}

		if channel.Type == model.ChannelTypeGroup && len(validMembers) > model.ChannelGroupMaxUsers {
			channel.Name = channel.Purpose.Value
			channel.Type = model.ChannelTypePrivate
		}

		name := SlackConvertChannelName(channel.Name, channel.Id)
		newChannel := &IntermediateChannel{
			OriginalName: getOriginalName(channel),
			Name:         name,
			DisplayName:  name,
			Members:      validMembers,
			Purpose:      channel.Purpose.Value,
			Header:       channel.Topic.Value,
			Type:         channel.Type,
		}

		newChannel.Sanitise(t.Logger)
		resultChannels = append(resultChannels, newChannel)
	}

	return resultChannels
}

func (t *Transformer) PopulateUserMemberships() {
	t.Logger.Info("Populating user memberships")

	for userId, user := range t.Intermediate.UsersById {
		memberships := []string{}
		for _, channel := range t.Intermediate.PublicChannels {
			if t.ChannelOnly != "" && channel.Name != t.ChannelOnly {
				t.Logger.Infof("--channel-only %s active - skipping channel %s", t.ChannelOnly, channel.Name)
				continue
			}
			for _, memberId := range channel.Members {
				if userId == memberId {
					memberships = append(memberships, channel.Name)
					break
				}
			}
		}
		for _, channel := range t.Intermediate.PrivateChannels {
			if t.ChannelOnly != "" && channel.Name != t.ChannelOnly {
				t.Logger.Infof("--channel-only %s active - skipping channel %s", t.ChannelOnly, channel.Name)
				continue
			}
			for _, memberId := range channel.Members {
				if userId == memberId {
					memberships = append(memberships, channel.Name)
					break
				}
			}
		}
		user.Memberships = memberships
	}
}

func (t *Transformer) PopulateChannelMemberships() {
	t.Logger.Info("Populating channel memberships")

	for _, channel := range t.Intermediate.GroupChannels {
		if t.ChannelOnly != "" && channel.Name != t.ChannelOnly {
			t.Logger.Infof("--channel-only %s active - skipping channel %s", t.ChannelOnly, channel.Name)
			continue
		}
		members := []string{}
		for _, memberId := range channel.Members {
			if user, ok := t.Intermediate.UsersById[memberId]; ok {
				members = append(members, user.Username)
			}
		}

		channel.MembersUsernames = members
	}
	for _, channel := range t.Intermediate.DirectChannels {
		if t.ChannelOnly != "" && channel.Name != t.ChannelOnly {
			t.Logger.Infof("--channel-only %s active - skipping channel %s", t.ChannelOnly, channel.Name)
			continue
		}
		members := []string{}
		for _, memberId := range channel.Members {
			if user, ok := t.Intermediate.UsersById[memberId]; ok {
				members = append(members, user.Username)
			}
		}

		channel.MembersUsernames = members
	}
}

func (t *Transformer) TransformAllChannels(slackExport *SlackExport) error {
	t.Logger.Info("Transforming channels")

	if t.ChannelOnly != "" {
		t.Logger.Infof("Only transforming channel: %s", t.ChannelOnly)
	}

	// transform public
	t.Intermediate.PublicChannels = t.TransformChannels(slackExport.PublicChannels)

	// transform private
	t.Intermediate.PrivateChannels = t.TransformChannels(slackExport.PrivateChannels)

	// transform group
	regularGroupChannels, bigGroupChannels := SplitChannelsByMemberSize(slackExport.GroupChannels, model.ChannelGroupMaxUsers)

	t.Intermediate.PrivateChannels = append(t.Intermediate.PrivateChannels, t.TransformChannels(bigGroupChannels)...)

	t.Intermediate.GroupChannels = t.TransformChannels(regularGroupChannels)

	// transform direct
	t.Intermediate.DirectChannels = t.TransformChannels(slackExport.DirectChannels)

	return nil
}

func AddPostToThreads(original SlackPost, post *IntermediatePost, threads map[string]*IntermediatePost, channel *IntermediateChannel, timestamps map[int64]bool) {
	// direct and group posts need the channel members in the import line
	if channel.Type == model.ChannelTypeDirect || channel.Type == model.ChannelTypeGroup {
		post.IsDirect = true
		post.ChannelMembers = channel.MembersUsernames
	} else {
		post.IsDirect = false
	}

	// avoid timestamp duplications
	for {
		// if the timestamp hasn't been used already, break and use
		if _, ok := timestamps[post.CreateAt]; !ok {
			break
		}
		post.CreateAt++
	}
	timestamps[post.CreateAt] = true

	// if post is part of a thread
	if original.ThreadTS != "" && original.ThreadTS != original.TimeStamp {
		rootPost, ok := threads[original.ThreadTS]
		if !ok {
			log.Printf("ERROR processing post in thread, couldn't find rootPost: %+v\n", original)
			return
		}
		rootPost.Replies = append(rootPost.Replies, post)
		return
	}

	// if post is the root of a thread
	if original.TimeStamp == original.ThreadTS {
		if threads[original.ThreadTS] != nil {
			log.Println("WARNING: overwriting root post for thread " + original.ThreadTS)
		}
		threads[original.ThreadTS] = post
		return
	}

	if threads[original.TimeStamp] != nil {
		log.Println("WARNING: overwriting root post for thread " + original.TimeStamp)
	}

	threads[original.TimeStamp] = post
}

func buildChannelsByOriginalNameMap(intermediate *Intermediate) map[string]*IntermediateChannel {
	channelsByName := map[string]*IntermediateChannel{}
	for _, channel := range intermediate.PublicChannels {
		channelsByName[channel.OriginalName] = channel
	}
	for _, channel := range intermediate.PrivateChannels {
		channelsByName[channel.OriginalName] = channel
	}
	for _, channel := range intermediate.GroupChannels {
		channelsByName[channel.OriginalName] = channel
	}
	for _, channel := range intermediate.DirectChannels {
		channelsByName[channel.OriginalName] = channel
	}
	return channelsByName
}

func getNormalisedFilePath(file *SlackFile, attachmentsDir string) string {
	n := makeAlphaNum(file.Name, '.', '-', '_')
	p := path.Join(attachmentsDir, fmt.Sprintf("%s_%s", file.Id, n))
	return norm.NFC.String(p)
}

func addFileToPost(file *SlackFile, uploads map[string]*zip.File, post *IntermediatePost, attachmentsDir string, allowDownload bool) error {
	if _, ok := uploads[file.Id]; ok || !allowDownload {
		return addZipFileToPost(file, uploads, post, attachmentsDir)
	}

	return addDownloadToPost(file, post, attachmentsDir)
}

func addDownloadToPost(file *SlackFile, post *IntermediatePost, attachmentsDir string) error {
	destFilePath := getNormalisedFilePath(file, attachmentsInternal)
	fullFilePath := path.Join(attachmentsDir, destFilePath)

	log.Printf("Downloading %q into %q...\n", file.DownloadURL, destFilePath)

	err := downloadInto(fullFilePath, file.DownloadURL, file.Size)
	if err != nil {
		return err
	}

	log.Println("Download successful!")

	post.Attachments = append(post.Attachments, destFilePath)
	return nil
}

var sizes = []string{"KiB", "MiB", "GiB", "TiB", "PiB"}

func humanSize(size int64) string {
	if size < 0 {
		return "unknown"
	}
	if size < 1024 {
		return fmt.Sprintf("%d B", size)
	}

	limit := int64(1024 * 1024)
	for _, name := range sizes {
		if size < limit {
			return fmt.Sprintf("%.2f %s", float64(size)/float64(limit/1024), name)
		}

		limit *= 1024
	}

	return fmt.Sprintf("%.2f %s", float64(size)/float64(limit/1024), sizes[len(sizes)-1])
}

func addZipFileToPost(file *SlackFile, uploads map[string]*zip.File, post *IntermediatePost, attachmentsDir string) error {
	zipFile, ok := uploads[file.Id]
	if !ok {
		return errors.Errorf("failed to retrieve file with id %s", file.Id)
	}

	zipFileReader, err := zipFile.Open()
	if err != nil {
		return errors.Wrapf(err, "failed to open attachment from zipfile for id %s", file.Id)
	}
	defer zipFileReader.Close()

	destFilePath := getNormalisedFilePath(file, attachmentsInternal)
	destFile, err := os.Create(path.Join(attachmentsDir, destFilePath))
	if err != nil {
		return errors.Wrapf(err, "failed to create file %s in the attachments directory", file.Id)
	}
	defer destFile.Close()

	_, err = io.Copy(destFile, zipFileReader)
	if err != nil {
		return errors.Wrapf(err, "failed to create file %s in the attachments directory", file.Id)
	}

	log.Printf("SUCCESS COPYING FILE %s TO DEST %s", file.Id, destFilePath)

	post.Attachments = append(post.Attachments, destFilePath)

	return nil
}

func (t *Transformer) CreateIntermediateUser(userID string) {
	newUser := &IntermediateUser{
		Id:        userID,
		Username:  strings.ToLower(userID),
		FirstName: "Deleted",
		LastName:  "User",
		Email:     fmt.Sprintf("%s@local", userID),
		Password:  model.NewId(),
	}
	t.Intermediate.UsersById[userID] = newUser
	t.Logger.Warnf("Created a new user because the original user was missing from the import files. user=%s", userID)
}

func (t *Transformer) CreateAndAddPostToThreads(post SlackPost, threads map[string]*IntermediatePost, timestamps map[int64]bool, channel *IntermediateChannel) {
	author := t.Intermediate.UsersById[post.User]
	if author == nil {
		t.CreateIntermediateUser(post.User)
		author = t.Intermediate.UsersById[post.User]
	}

	newPost := &IntermediatePost{
		User:     author.Username,
		Channel:  channel.Name,
		Message:  post.Text,
		CreateAt: SlackConvertTimeStamp(post.TimeStamp),
	}

	AddPostToThreads(post, newPost, threads, channel, timestamps)
}

func (t *Transformer) AddFilesToPost(post *SlackPost, skipAttachments bool, slackExport *SlackExport, attachmentsDir string, newPost *IntermediatePost, allowDownload bool) {
	if skipAttachments || (post.File == nil && post.Files == nil) {
		return
	}
	if post.File != nil {
		if err := addFileToPost(post.File, slackExport.Uploads, newPost, attachmentsDir, allowDownload); err != nil {
			t.Logger.WithError(err).Error("Failed to add file to post")
		}
	} else if post.Files != nil {
		for _, file := range post.Files {
			if file.Name == "" {
				t.Logger.Warnf("Not able to access the file %s as file access is denied so skipping", file.Id)
				continue
			}
			if err := addFileToPost(file, slackExport.Uploads, newPost, attachmentsDir, allowDownload); err != nil {
				t.Logger.WithError(err).Error("Failed to add file to post")
			}
		}
	}
}

func (t *Transformer) AddAttachmentsToPost(post *SlackPost, newPost *IntermediatePost) (model.StringInterface, []byte) {
	props := model.StringInterface{"attachments": post.Attachments}
	propsByteArray, _ := json.Marshal(props)
	return props, propsByteArray
}

func buildMessagePropsFromHuddle(post *SlackPost) model.StringInterface {
	type Attachment struct {
		ID       int    `json:"id"`
		Text     string `json:"text"`
		Fallback string `json:"fallback"`
	}

	type MessageProps struct {
		Title       string       `json:"title"`
		EndAt       int64        `json:"end_at"`
		StartAt     int64        `json:"start_at"`
		Attachments []Attachment `json:"attachments"`
		FromPlugin  bool         `json:"from_plugin"`
	}

	props := MessageProps{
		Title: "",
		Attachments: []Attachment{{
			ID:       0,
			Text:     "Call ended",
			Fallback: "Call ended",
		}},
		FromPlugin: true,
		EndAt:      0,
		StartAt:    0,
	}

	if post.Room != nil {
		props.EndAt = int64(post.Room.DateEnd) * 1000
		props.StartAt = int64(post.Room.DateStart) * 1000
	}

	propsMap := make(map[string]interface{})
	bytes, _ := json.Marshal(props)
	_ = json.Unmarshal(bytes, &propsMap)

	return propsMap
}

func (t *Transformer) TransformPosts(slackExport *SlackExport, attachmentsDir string, skipAttachments, discardInvalidProps, allowDownload bool, maxMessageLength int, channelOnly string) error {
	t.Logger.Info("Transforming posts")

	newGroupChannels := []*IntermediateChannel{}
	newDirectChannels := []*IntermediateChannel{}
	channelsByOriginalName := buildChannelsByOriginalNameMap(t.Intermediate)

	resultPosts := []*IntermediatePost{}
	for originalChannelName, channelPosts := range slackExport.Posts {
		channel, ok := channelsByOriginalName[originalChannelName]
		if !ok {
			t.Logger.Warnf("--- Couldn't find channel %s referenced by posts", originalChannelName)
			continue
		}

		if channelOnly != "" && channel.Name != channelOnly {
			t.Logger.Infof("--channel-only %s active - skipping channel %s", channelOnly, channel.Name)
			continue
		}

		t.Logger.Infof("Transforming channel %s", channel.Name)

		timestamps := make(map[int64]bool)
		sort.Slice(channelPosts, func(i, j int) bool {
			return SlackConvertTimeStamp(channelPosts[i].TimeStamp) < SlackConvertTimeStamp(channelPosts[j].TimeStamp)
		})
		threads := map[string]*IntermediatePost{}

		for _, post := range channelPosts {
			switch {
			// plain message that can have files attached
			case post.IsPlainMessage():
				if post.User == "" {
					t.Logger.Warn("Unable to import the message as the user field is missing.")
					continue
				}
				author := t.Intermediate.UsersById[post.User]
				if author == nil {
					t.CreateIntermediateUser(post.User)
					author = t.Intermediate.UsersById[post.User]
				}

				// Split message if needed
				messageParts := splitMessage(post.Text, t.MaxMessageLength)

				// Create the main post with first part
				mainPost := &IntermediatePost{
					User:     author.Username,
					Channel:  channel.Name,
					Message:  messageParts[0],
					CreateAt: SlackConvertTimeStamp(post.TimeStamp),
				}

				// Add files and attachments only to main post
				t.AddFilesToPost(&post, skipAttachments, slackExport, attachmentsDir, mainPost, allowDownload)

				if len(post.Attachments) > 0 {
					props, propsB := t.AddAttachmentsToPost(&post, mainPost)
					if utf8.RuneCount(propsB) <= model.PostPropsMaxRunes {
						mainPost.Props = props
					} else {
						if discardInvalidProps {
							t.Logger.Warn("Unable import post as props exceed the maximum character count. Skipping as --discard-invalid-props is enabled.")
							continue
						} else {
							t.Logger.Warn("Unable to add props to post as they exceed the maximum character count.")
						}
					}
				}

				// Add subsequent parts as replies
				for i := 1; i < len(messageParts); i++ {
					reply := &IntermediatePost{
						User:     author.Username,
						Channel:  channel.Name,
						Message:  messageParts[i],
						CreateAt: SlackConvertTimeStamp(post.TimeStamp) + int64(i), // Increment timestamp for order
					}
					mainPost.Replies = append(mainPost.Replies, reply)
				}

				AddPostToThreads(post, mainPost, threads, channel, timestamps)

			// file comment
			case post.IsFileComment():
				if post.Comment == nil {
					t.Logger.Warn("Unable to import the message as it has no comments.")
					continue
				}
				if post.Comment.User == "" {
					t.Logger.Warn("Unable to import the message as the user field is missing.")
					continue
				}
				author := t.Intermediate.UsersById[post.Comment.User]
				if author == nil {
					t.CreateIntermediateUser(post.User)
					author = t.Intermediate.UsersById[post.User]
				}
				newPost := &IntermediatePost{
					User:     author.Username,
					Channel:  channel.Name,
					Message:  post.Comment.Comment,
					CreateAt: SlackConvertTimeStamp(post.TimeStamp),
				}

				AddPostToThreads(post, newPost, threads, channel, timestamps)

			// bot message
			case post.IsBotMessage():
				if post.BotId == "" {
					if post.User == "" {
						t.Logger.Warn("Unable to import the message as the user field is missing.")
						continue
					}
					post.BotId = post.User
				}

				author := t.Intermediate.UsersById[post.BotId]
				if author == nil {
					t.CreateIntermediateUser(post.BotId)
					author = t.Intermediate.UsersById[post.BotId]
				}

				newPost := &IntermediatePost{
					User:     author.Username,
					Channel:  channel.Name,
					Message:  post.Text,
					CreateAt: SlackConvertTimeStamp(post.TimeStamp),
				}

				t.AddFilesToPost(&post, skipAttachments, slackExport, attachmentsDir, newPost, allowDownload)

				if len(post.Attachments) > 0 {
					props, propsB := t.AddAttachmentsToPost(&post, newPost)
					if utf8.RuneCount(propsB) <= model.PostPropsMaxRunes {
						newPost.Props = props
					} else {
						if discardInvalidProps {
							t.Logger.Warn("Unable to import the post as props exceed the maximum character count. Skipping as --discard-invalid-props is enabled.")
							continue
						} else {
							t.Logger.Warn("Unable to add the props to post as they exceed the maximum character count.")
						}
					}
				}

				AddPostToThreads(post, newPost, threads, channel, timestamps)

			// channel join/leave messages
			case post.IsJoinLeaveMessage():
				if post.User == "" {
					t.Logger.Warn("Unable to import the message as the user field is missing.")
					continue
				}

				t.CreateAndAddPostToThreads(post, threads, timestamps, channel)

			// me message
			case post.IsMeMessage():
				if post.User == "" {
					t.Logger.Warn("Unable to import the message as the user field is missing.")
					continue
				}
				t.CreateAndAddPostToThreads(post, threads, timestamps, channel)

			// change topic message
			case post.IsChannelTopicMessage():
				if post.User == "" {
					t.Logger.Warn("Unable to import the message as the user field is missing.")
					continue
				}
				t.CreateAndAddPostToThreads(post, threads, timestamps, channel)

			// change channel purpose message
			case post.IsChannelPurposeMessage():
				if post.User == "" {
					t.Logger.Warn("Unable to import the message as the user field is missing.")
					continue
				}
				t.CreateAndAddPostToThreads(post, threads, timestamps, channel)

			// change channel name message
			case post.IsChannelNameMessage():
				if post.User == "" {
					t.Logger.Warn("Slack Import: Unable to import the message as the user field is missing.")
					continue
				}
				t.CreateAndAddPostToThreads(post, threads, timestamps, channel)

			// Huddle thread
			case post.isHuddleThread():
				post.Text = "Call ended"
				if post.User == "" {
					t.Logger.Warn("Slack Import: Unable to import the message as the user field is missing.")
					continue
				}

				// all huddles are owned by USLACKBOT, but the room has a CreatedBy prop.
				// this lets us get the actual user who created the huddle and fit with how Mattermost works.
				poster := post.User
				if len(post.Room.CreatedBy) > 0 {
					poster = post.Room.CreatedBy
				}

				author := t.Intermediate.UsersById[poster]
				if author == nil {
					t.CreateIntermediateUser(poster)
					author = t.Intermediate.UsersById[poster]
				}

				huddleProps := buildMessagePropsFromHuddle(&post)

				newPost := &IntermediatePost{
					User:     author.Username,
					Channel:  channel.Name,
					Message:  post.Text,
					CreateAt: SlackConvertTimeStamp(post.TimeStamp),
					Props:    huddleProps,
					Type:     "custom_calls",
				}

				AddPostToThreads(post, newPost, threads, channel, timestamps)
			default:
				t.Logger.Warnf("Unable to import the message as its type is not supported. post_type=%s, post_subtype=%s", post.Type, post.SubType)
			}
		}

		channelPosts := []*IntermediatePost{}
		for _, post := range threads {
			channelPosts = append(channelPosts, post)
		}
		resultPosts = append(resultPosts, channelPosts...)
	}

	t.Intermediate.Posts = resultPosts
	t.Intermediate.GroupChannels = append(t.Intermediate.GroupChannels, newGroupChannels...)
	t.Intermediate.DirectChannels = append(t.Intermediate.DirectChannels, newDirectChannels...)

	return nil
}

func (t *Transformer) Transform(slackExport *SlackExport, attachmentsDir string, skipAttachments, discardInvalidProps, allowDownload, skipEmptyEmails bool, defaultEmailDomain string, maxMessageLength int, channelOnly string) error {
	t.MaxMessageLength = maxMessageLength
	t.TransformUsers(slackExport.Users, skipEmptyEmails, defaultEmailDomain)

	if err := t.TransformAllChannels(slackExport); err != nil {
		return err
	}

	t.PopulateUserMemberships()
	t.PopulateChannelMemberships()

	if err := t.TransformPosts(slackExport, attachmentsDir, skipAttachments, discardInvalidProps, allowDownload, maxMessageLength, channelOnly); err != nil {
		return err
	}

	return nil
}

func makeAlphaNum(str string, allowAdditional ...rune) string {
	for match, replace := range specialReplacements {
		str = strings.ReplaceAll(str, match, replace)
	}

	str = norm.NFKD.String(str)
	str = strings.Map(func(r rune) rune {
		for _, allowed := range allowAdditional {
			if r == allowed {
				return r
			}
		}

		// filter all non-ASCII runes
		if r > 127 {
			return -1
		}

		// restrict the remaining characters
		if r >= 'a' && r <= 'z' {
			return r
		}
		if r >= 'A' && r <= 'Z' {
			return r
		}
		if r >= '0' && r <= '9' {
			return r
		}

		return '_'
	}, str)
	return str
}

var specialReplacements = map[string]string{
	"ß": "ss",
}

func splitMessage(message string, maxLength int) []string {
	if message == "" {
		return []string{""}
	}

	if maxLength <= 0 || len(message) <= maxLength {
		return []string{message}
	}

	var parts []string
	for len(message) > maxLength {
		// Find last space before max length to avoid splitting words
		splitIndex := strings.LastIndex(message[:maxLength], " ")
		if splitIndex == -1 {
			splitIndex = maxLength
		}

		parts = append(parts, strings.TrimSpace(message[:splitIndex]))
		message = strings.TrimSpace(message[splitIndex:])
	}

	if len(message) > 0 {
		parts = append(parts, message)
	}

	// Ensure we always return at least one part
	if len(parts) == 0 {
		return []string{""}
	}

	return parts
}
