package slack

import (
	"archive/zip"
	"fmt"
	"io"
	"log"
	"os"
	"path"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/mattermost/mattermost-server/model"
)

type IntermediateChannel struct {
	Id               string   `json:"id"`
	OriginalName     string   `json:"original_name"`
	Name             string   `json:"name"`
	DisplayName      string   `json:"display_name"`
	Members          []string `json:"members"`
	MembersUsernames []string `json:"members_usernames"`
	Purpose          string   `json:"purpose"`
	Header           string   `json:"header"`
	Topic            string   `json:"topic"`
	Type             string   `json:"type"`
}

func (c *IntermediateChannel) Sanitise() {
	if c.Type == model.CHANNEL_DIRECT {
		return
	}

	c.Name = strings.Trim(c.Name, "_-")
	if len(c.Name) > model.CHANNEL_NAME_MAX_LENGTH {
		log.Println(fmt.Sprintf("Slack Import: Channel %v handle exceeds the maximum length. It will be truncated when imported.", c.DisplayName))
		c.Name = c.Name[0:model.CHANNEL_NAME_MAX_LENGTH]
	}
	if len(c.Name) == 1 {
		c.Name = "slack-channel-" + c.Name
	}
	if !isValidChannelNameCharacters(c.Name) {
		c.Name = strings.ToLower(c.Id)
	}

	c.DisplayName = strings.Trim(c.DisplayName, "_-")
	if utf8.RuneCountInString(c.DisplayName) > model.CHANNEL_DISPLAY_NAME_MAX_RUNES {
		log.Println(fmt.Sprintf("Slack Import: Channel %v display name exceeds the maximum length. It will be truncated when imported.", c.DisplayName))
		c.DisplayName = truncateRunes(c.DisplayName, model.CHANNEL_DISPLAY_NAME_MAX_RUNES)
	}
	if len(c.DisplayName) == 1 {
		c.DisplayName = "slack-channel-" + c.DisplayName
	}
	if !isValidChannelNameCharacters(c.DisplayName) {
		c.DisplayName = strings.ToLower(c.Id)
	}

	if utf8.RuneCountInString(c.Purpose) > model.CHANNEL_PURPOSE_MAX_RUNES {
		log.Println(fmt.Sprintf("Slack Import: Channel %v purpose exceeds the maximum length. It will be truncated when imported.", c.DisplayName))
		c.Purpose = truncateRunes(c.Purpose, model.CHANNEL_PURPOSE_MAX_RUNES)
	}

	if utf8.RuneCountInString(c.Header) > model.CHANNEL_HEADER_MAX_RUNES {
		log.Println(fmt.Sprintf("Slack Import: Channel %v header exceeds the maximum length. It will be truncated when imported.", c.DisplayName))
		c.Header = truncateRunes(c.Header, model.CHANNEL_HEADER_MAX_RUNES)
	}
}

type IntermediateUser struct {
	Id          string   `json:"id"`
	Username    string   `json:"username"`
	FirstName   string   `json:"first_name"`
	LastName    string   `json:"last_name"`
	Email       string   `json:"email"`
	Password    string   `json:"password"`
	Memberships []string `json:"memberships"`
}

func (u *IntermediateUser) Sanitise() {
	if u.Email == "" {
		u.Email = u.Username + "@example.com"
		log.Println(fmt.Sprintf("User %s does not have an email address in the Slack export. Used %s as a placeholder. The user should update their email address once logged in to the system.", u.Username, u.Email))
	}
}

type IntermediatePost struct {
	User     string `json:"user"`
	Channel  string `json:"channel"`
	Message  string `json:"message"`
	CreateAt int64  `json:"create_at"`
	// Type           string              `json:"type"`
	Attachments    []string            `json:"attachments"`
	Replies        []*IntermediatePost `json:"replies"`
	IsDirect       bool                `json:"is_direct"`
	ChannelMembers []string            `json:"channel_members"`
}

type Intermediate struct {
	PublicChannels  []*IntermediateChannel       `json:"public_channels"`
	PrivateChannels []*IntermediateChannel       `json:"private_channels"`
	GroupChannels   []*IntermediateChannel       `json:"group_channels"`
	DirectChannels  []*IntermediateChannel       `json:"direct_channels"`
	UsersById       map[string]*IntermediateUser `json:"users"`
	Posts           []*IntermediatePost          `json:"posts"`
}

func TransformUsers(users []SlackUser, intermediate *Intermediate) {
	resultUsers := map[string]*IntermediateUser{}
	for _, user := range users {
		newUser := &IntermediateUser{
			Id:        user.Id,
			Username:  user.Username,
			FirstName: user.Profile.FirstName,
			LastName:  user.Profile.LastName,
			Email:     user.Profile.Email,
			Password:  model.NewId(),
		}

		newUser.Sanitise()
		resultUsers[newUser.Id] = newUser
		log.Println(fmt.Sprintf("Slack user with email %s and password %s has been imported.", newUser.Email, newUser.Password))
	}

	intermediate.UsersById = resultUsers
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

func TransformChannels(channels []SlackChannel, users map[string]*IntermediateUser) []*IntermediateChannel {
	resultChannels := []*IntermediateChannel{}
	for _, channel := range channels {
		validMembers := filterValidMembers(channel.Members, users)
		if (channel.Type == model.CHANNEL_DIRECT || channel.Type == model.CHANNEL_GROUP) && len(validMembers) <= 1 {
			log.Println("Bulk export for direct channels containing a single member is not supported. Not importing channel " + channel.Name)
			continue
		}

		if channel.Type == model.CHANNEL_GROUP && len(validMembers) > model.CHANNEL_GROUP_MAX_USERS {
			channel.Name = channel.Purpose.Value
			channel.Type = model.CHANNEL_PRIVATE
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

		newChannel.Sanitise()
		resultChannels = append(resultChannels, newChannel)
	}

	return resultChannels
}

func PopulateUserMemberships(intermediate *Intermediate) {
	for userId, user := range intermediate.UsersById {
		memberships := []string{}
		for _, channel := range intermediate.PublicChannels {
			for _, memberId := range channel.Members {
				if userId == memberId {
					memberships = append(memberships, channel.Name)
					break
				}
			}
		}
		for _, channel := range intermediate.PrivateChannels {
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

func PopulateChannelMemberships(intermediate *Intermediate) {
	for _, channel := range intermediate.GroupChannels {
		members := []string{}
		for _, memberId := range channel.Members {
			if user, ok := intermediate.UsersById[memberId]; ok {
				members = append(members, user.Username)
			}
		}

		channel.MembersUsernames = members
	}
	for _, channel := range intermediate.DirectChannels {
		members := []string{}
		for _, memberId := range channel.Members {
			if user, ok := intermediate.UsersById[memberId]; ok {
				members = append(members, user.Username)
			}
		}

		channel.MembersUsernames = members
	}
}

func TransformAllChannels(slackExport *SlackExport, intermediate *Intermediate) error {
	// transform public
	intermediate.PublicChannels = TransformChannels(slackExport.PublicChannels, intermediate.UsersById)

	// transform private
	intermediate.PrivateChannels = TransformChannels(slackExport.PrivateChannels, intermediate.UsersById)

	// transform group
	regularGroupChannels, bigGroupChannels := SplitChannelsByMemberSize(slackExport.GroupChannels, model.CHANNEL_GROUP_MAX_USERS)

	intermediate.PrivateChannels = append(intermediate.PrivateChannels, TransformChannels(bigGroupChannels, intermediate.UsersById)...)

	intermediate.GroupChannels = TransformChannels(regularGroupChannels, intermediate.UsersById)

	// transform direct
	intermediate.DirectChannels = TransformChannels(slackExport.DirectChannels, intermediate.UsersById)

	return nil
}

func AddPostToThreads(original SlackPost, post *IntermediatePost, threads map[string]*IntermediatePost, channel *IntermediateChannel) {
	// direct and group posts need the channel members in the import line
	if channel.Type == model.CHANNEL_DIRECT || channel.Type == model.CHANNEL_GROUP {
		post.IsDirect = true
		post.ChannelMembers = channel.MembersUsernames
	} else {
		post.IsDirect = false
	}

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

func addFileToPost(file *SlackFile, uploads map[string]*zip.File, post *IntermediatePost, attachmentsDir string) {
	zipFile, ok := uploads[file.Id]
	if !ok {
		log.Printf("Error retrieving file with id %s", file.Id)
		return
	}

	zipFileReader, err := zipFile.Open()
	if err != nil {
		log.Printf("Error opening attachment from zipfile for id %s. Err=%s", file.Id, err.Error())
		return
	}
	defer zipFileReader.Close()

	destFilePath := path.Join(attachmentsDir, fmt.Sprintf("%s_%s", file.Id, file.Name))
	destFile, err := os.Create(destFilePath)
	if err != nil {
		log.Printf("Error creating file %s in the attachments directory. Err=%s", file.Id, err.Error())
		return
	}
	defer destFile.Close()

	_, err = io.Copy(destFile, zipFileReader)
	if err != nil {
		log.Printf("Error creating file %s in the attachments directory. Err=%s", file.Id, err.Error())
		return
	} else {
		log.Printf("SUCCESS COPYING FILE %s TO DEST %s", file.Id, destFilePath)
	}

	post.Attachments = append(post.Attachments, destFilePath)
}

func TransformPosts(slackExport *SlackExport, intermediate *Intermediate, attachmentsDir string, skipAttachments bool) error {
	newGroupChannels := []*IntermediateChannel{}
	newDirectChannels := []*IntermediateChannel{}
	channelsByOriginalName := buildChannelsByOriginalNameMap(intermediate)

	resultPosts := []*IntermediatePost{}
	for originalChannelName, channelPosts := range slackExport.Posts {
		channel, ok := channelsByOriginalName[originalChannelName]
		if !ok {
			log.Printf("--- Couldn't find channel %s referenced by posts", originalChannelName)
			continue
		}

		sort.Slice(channelPosts, func(i, j int) bool {
			return SlackConvertTimeStamp(channelPosts[i].TimeStamp) < SlackConvertTimeStamp(channelPosts[j].TimeStamp)
		})
		threads := map[string]*IntermediatePost{}

		for _, post := range channelPosts {
			switch {
			// plain message that can have files attached
			case post.IsPlainMessage():
				if post.User == "" {
					log.Println("Slack Import: Unable to import the message as the user field is missing.")
					continue
				}
				author := intermediate.UsersById[post.User]
				if author == nil {
					log.Println("Slack Import: Unable to add the message as the Slack user does not exist in Mattermost. user=" + post.User)
					continue
				}
				newPost := &IntermediatePost{
					User:     author.Username,
					Channel:  channel.Name,
					Message:  post.Text,
					CreateAt: SlackConvertTimeStamp(post.TimeStamp),
				}
				if post.Upload && !skipAttachments {
					if post.File != nil {
						addFileToPost(post.File, slackExport.Uploads, newPost, attachmentsDir)
					} else if post.Files != nil {
						for _, file := range post.Files {
							addFileToPost(file, slackExport.Uploads, newPost, attachmentsDir)
						}
					}
				}

				AddPostToThreads(post, newPost, threads, channel)

			// file comment
			case post.IsFileComment():
				if post.Comment == nil {
					log.Println("Slack Import: Unable to import the message as it has no comments.")
					continue
				}
				if post.Comment.User == "" {
					log.Println("Slack Import: Unable to import the message as the user field is missing.")
					continue
				}
				author := intermediate.UsersById[post.Comment.User]
				if author == nil {
					log.Println("Slack Import: Unable to add the message as the Slack user does not exist in Mattermost. user=" + post.Comment.User)
					continue
				}
				newPost := &IntermediatePost{
					User:     author.Username,
					Channel:  channel.Name,
					Message:  post.Comment.Comment,
					CreateAt: SlackConvertTimeStamp(post.TimeStamp),
				}

				AddPostToThreads(post, newPost, threads, channel)

			// bot message
			case post.IsBotMessage():
				// log.Println("Slack Import: bot messages are not yet supported")
				break

			// channel join/leave messages
			case post.IsJoinLeaveMessage():
				// log.Println("Slack Import: Join/Leave messages are not yet supported")
				break

			// me message
			case post.IsMeMessage():
				// log.Println("Slack Import: me messages are not yet supported")
				break

			// change topic message
			case post.IsChannelTopicMessage():
				if post.User == "" {
					log.Println("Slack Import: Unable to import the message as the user field is missing.")
					continue
				}
				author := intermediate.UsersById[post.User]
				if author == nil {
					log.Println("Slack Import: Unable to add the message as the Slack user does not exist in Mattermost. user=" + post.User)
					continue
				}

				newPost := &IntermediatePost{
					User:     author.Username,
					Channel:  channel.Name,
					Message:  post.Text,
					CreateAt: SlackConvertTimeStamp(post.TimeStamp),
					// Type:     model.POST_HEADER_CHANGE,
				}

				AddPostToThreads(post, newPost, threads, channel)

			// change channel purpose message
			case post.IsChannelPurposeMessage():
				if post.User == "" {
					log.Println("Slack Import: Unable to import the message as the user field is missing.")
					continue
				}
				author := intermediate.UsersById[post.User]
				if author == nil {
					log.Println("Slack Import: Unable to add the message as the Slack user does not exist in Mattermost. user=" + post.User)
					continue
				}

				newPost := &IntermediatePost{
					User:     author.Username,
					Channel:  channel.Name,
					Message:  post.Text,
					CreateAt: SlackConvertTimeStamp(post.TimeStamp),
					// Type:     model.POST_HEADER_CHANGE,
				}

				AddPostToThreads(post, newPost, threads, channel)

			// change channel name message
			case post.IsChannelNameMessage():
				if post.User == "" {
					log.Println("Slack Import: Unable to import the message as the user field is missing.")
					continue
				}
				author := intermediate.UsersById[post.User]
				if author == nil {
					log.Println("Slack Import: Unable to add the message as the Slack user does not exist in Mattermost. user=" + post.User)
					continue
				}

				newPost := &IntermediatePost{
					User:     author.Username,
					Channel:  channel.Name,
					Message:  post.Text,
					CreateAt: SlackConvertTimeStamp(post.TimeStamp),
					// Type:     model.POST_DISPLAYNAME_CHANGE,
				}

				AddPostToThreads(post, newPost, threads, channel)

			default:
				log.Println("Slack Import: Unable to import the message as its type is not supported. post_type=" + post.Type + " post_subtype=" + post.SubType)
			}
		}

		channelPosts := []*IntermediatePost{}
		for _, post := range threads {
			channelPosts = append(channelPosts, post)
		}
		resultPosts = append(resultPosts, channelPosts...)
	}

	intermediate.Posts = resultPosts
	intermediate.GroupChannels = append(intermediate.GroupChannels, newGroupChannels...)
	intermediate.DirectChannels = append(intermediate.DirectChannels, newDirectChannels...)

	return nil
}

func Transform(slackExport *SlackExport, attachmentsDir string, skipAttachments bool) (*Intermediate, error) {
	intermediate := &Intermediate{}

	// ToDo: change log lines to something more meaningful
	log.Println("Transforming users")
	TransformUsers(slackExport.Users, intermediate)

	log.Println("Transforming channels")
	if err := TransformAllChannels(slackExport, intermediate); err != nil {
		return nil, err
	}

	log.Println("Populating user memberships")
	PopulateUserMemberships(intermediate)

	log.Println("Populating channel memberships")
	PopulateChannelMemberships(intermediate)

	log.Println("Transforming posts")
	if err := TransformPosts(slackExport, intermediate, attachmentsDir, skipAttachments); err != nil {
		return nil, err
	}

	return intermediate, nil
}
