package slack

import (
	"encoding/json"
	"io"
	"log"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/mattermost/mattermost-server/app"
	"github.com/mattermost/mattermost-server/model"
	"github.com/pkg/errors"
)

var isValidChannelNameCharacters = regexp.MustCompile(`^[a-zA-Z0-9\-_]+$`).MatchString

func truncateRunes(s string, i int) string {
	runes := []rune(s)
	if len(runes) > i {
		return string(runes[:i])
	}
	return s
}

func SlackConvertTimeStamp(ts string) int64 {
	timeString := strings.SplitN(ts, ".", 2)[0]

	timeStamp, err := strconv.ParseInt(timeString, 10, 64)
	if err != nil {
		log.Println("Slack Import: Bad timestamp detected.")
		return 1
	}
	return timeStamp * 1000 // Convert to milliseconds
}

func SlackConvertChannelName(channelName string, channelId string) string {
	newName := strings.Trim(channelName, "_-")
	if len(newName) == 1 {
		return "slack-channel-" + newName
	}

	if isValidChannelNameCharacters(newName) {
		return newName
	}
	return strings.ToLower(channelId)
}

func SplitChannelsByMemberSize(channels []SlackChannel, limit int) (regularChannels, bigChannels []SlackChannel) {
	for _, channel := range channels {
		if len(channel.Members) == 1 {
			log.Println("Bulk export for direct channels containing a single member is not supported. Not importing channel " + channel.Name)
		} else if len(channel.Members) > limit {
			bigChannels = append(bigChannels, channel)
		} else {
			regularChannels = append(regularChannels, channel)
		}
	}
	return
}

func GetImportLineFromChannel(team string, channel *IntermediateChannel) *app.LineImportData {
	newChannel := &app.ChannelImportData{
		Team:        model.NewString(team),
		Name:        model.NewString(channel.Name),
		DisplayName: model.NewString(channel.DisplayName),
		Type:        &channel.Type,
		Header:      &channel.Header,
		Purpose:     &channel.Purpose,
		//TODO: needs support in mattermost channel model before this can be uncommented
		//IsArchived: &channel.IsArchived,
	}

	return &app.LineImportData{
		Type:    "channel",
		Channel: newChannel,
	}
}

func GetImportLineFromDirectChannel(team string, channel *IntermediateChannel) *app.LineImportData {
	return &app.LineImportData{
		Type: "direct_channel",
		DirectChannel: &app.DirectChannelImportData{
			Header:  &channel.Topic,
			Members: &channel.MembersUsernames,
		},
	}
}

func GetImportLineFromUser(user *IntermediateUser, team string) *app.LineImportData {
	channelMemberships := []app.UserChannelImportData{}
	for _, channelName := range user.Memberships {
		channelMemberships = append(channelMemberships, app.UserChannelImportData{
			Name:  model.NewString(channelName),
			Roles: model.NewString(model.CHANNEL_USER_ROLE_ID),
		})
	}

	return &app.LineImportData{
		Type: "user",
		User: &app.UserImportData{
			Username:  model.NewString(user.Username),
			Email:     model.NewString(user.Email),
			Nickname:  model.NewString(""),
			FirstName: model.NewString(user.FirstName),
			LastName:  model.NewString(user.LastName),
			Position:  model.NewString(""),
			Roles:     model.NewString(model.SYSTEM_USER_ROLE_ID),
			Teams: &[]app.UserTeamImportData{
				{
					Name:     model.NewString(team),
					Channels: &channelMemberships,
					Roles:    model.NewString(model.TEAM_USER_ROLE_ID),
				},
			},
		},
	}
}

func GetAttachmentImportDataFromPaths(paths []string) []app.AttachmentImportData {
	attachments := []app.AttachmentImportData{}
	for _, path := range paths {
		attachmentImportData := app.AttachmentImportData{
			Path: &path,
		}
		attachments = append(attachments, attachmentImportData)
	}
	return attachments
}

func GetImportLineFromPost(post *IntermediatePost, team string) *app.LineImportData {
	replies := []app.ReplyImportData{}
	for _, reply := range post.Replies {
		attachments := GetAttachmentImportDataFromPaths(reply.Attachments)
		newReply := app.ReplyImportData{
			User:        &reply.User,
			Message:     &reply.Message,
			CreateAt:    &reply.CreateAt,
			Attachments: &attachments,
		}
		replies = append(replies, newReply)
	}

	var newPost *app.LineImportData
	attachments := GetAttachmentImportDataFromPaths(post.Attachments)
	if post.IsDirect {
		newPost = &app.LineImportData{
			Type: "direct_post",
			DirectPost: &app.DirectPostImportData{
				ChannelMembers: &post.ChannelMembers,
				User:           &post.User,
				Message:        &post.Message,
				CreateAt:       &post.CreateAt,
				Replies:        &replies,
				Attachments:    &attachments,
			},
		}
	} else {
		newPost = &app.LineImportData{
			Type: "post",
			Post: &app.PostImportData{
				Team:        model.NewString(team),
				Channel:     &post.Channel,
				User:        &post.User,
				Message:     &post.Message,
				CreateAt:    &post.CreateAt,
				Replies:     &replies,
				Attachments: &attachments,
			},
		}
	}

	return newPost
}

func ExportWriteLine(writer io.Writer, line *app.LineImportData) error {
	b, err := json.Marshal(line)
	if err != nil {
		return errors.Wrap(err, "An error occurred marshalling the JSON data for export.")
	}

	if _, err := writer.Write(append(b, '\n')); err != nil {
		return errors.Wrap(err, "An error occurred writing the export data.")
	}

	return nil
}

func ExportVersion(writer io.Writer) error {
	version := 1
	versionLine := &app.LineImportData{
		Type:    "version",
		Version: &version,
	}

	return ExportWriteLine(writer, versionLine)
}

// valid for open or private, as they export with no members
func ExportChannels(team string, channels []*IntermediateChannel, writer io.Writer) error {
	for _, channel := range channels {
		line := GetImportLineFromChannel(team, channel)
		if err := ExportWriteLine(writer, line); err != nil {
			return err
		}
	}

	return nil
}

// valid for group or direct, as they export with members
func ExportDirectChannels(team string, channels []*IntermediateChannel, writer io.Writer) error {
	for _, channel := range channels {
		line := GetImportLineFromDirectChannel(team, channel)
		if err := ExportWriteLine(writer, line); err != nil {
			return err
		}
	}

	return nil
}

func ExportUsers(team string, usersById map[string]*IntermediateUser, writer io.Writer) error {
	for _, user := range usersById {
		line := GetImportLineFromUser(user, team)
		if err := ExportWriteLine(writer, line); err != nil {
			return err
		}
	}

	return nil
}

func ExportPosts(team string, posts []*IntermediatePost, writer io.Writer) error {
	for _, post := range posts {
		line := GetImportLineFromPost(post, team)
		if err := ExportWriteLine(writer, line); err != nil {
			return err
		}
	}
	return nil
}

func Export(team string, intermediate *Intermediate, outputFilePath string) error {
	outputFile, err := os.Create(outputFilePath)
	if err != nil {
		return err
	}
	defer outputFile.Close()

	log.Println("Exporting version")
	if err := ExportVersion(outputFile); err != nil {
		return err
	}

	log.Println("Exporting public channels")
	if err := ExportChannels(team, intermediate.PublicChannels, outputFile); err != nil {
		return err
	}

	log.Println("Exporting private channels")
	if err := ExportChannels(team, intermediate.PrivateChannels, outputFile); err != nil {
		return err
	}

	log.Println("Exporting users")
	if err := ExportUsers(team, intermediate.UsersById, outputFile); err != nil {
		return err
	}

	log.Println("Exporting group channels")
	if err := ExportDirectChannels(team, intermediate.GroupChannels, outputFile); err != nil {
		return err
	}

	log.Println("Exporting direct channels")
	if err := ExportDirectChannels(team, intermediate.DirectChannels, outputFile); err != nil {
		return err
	}

	log.Println("Exporting posts")
	if err := ExportPosts(team, intermediate.Posts, outputFile); err != nil {
		return err
	}

	return nil
}
