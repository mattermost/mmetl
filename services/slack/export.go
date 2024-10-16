package slack

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/v8/channels/app/imports"
	"github.com/pkg/errors"
)

const (
	POST_MAX_ATTACHMENTS = 5
)

type ChunkInfo struct {
	Id          uint     `json:"id"`
	File        string   `json:"file"`
	Attachments []string `json:"attachments"`
	Zip         string   `json:"zip,omitempty"`
}

var isValidChannelNameCharacters = regexp.MustCompile(`^[a-zA-Z0-9\-_]+$`).MatchString

func truncateRunes(s string, i int) string {
	runes := []rune(s)
	if len(runes) > i {
		return string(runes[:i])
	}
	return s
}

func SlackConvertTimeStamp(ts string) int64 {
	timeStrings := strings.Split(ts, ".")

	tail := "0000"
	if len(timeStrings) > 1 {
		tail = timeStrings[1][:4]
	}
	timeString := timeStrings[0] + tail

	timeStamp, err := strconv.ParseInt(timeString, 10, 64)
	if err != nil {
		log.Println("Slack Import: Bad timestamp detected.")
		return 1
	}

	return int64(math.Round(float64(timeStamp) / 10)) // round for precision
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

func GetImportLineFromChannel(team string, channel *IntermediateChannel) *imports.LineImportData {
	newChannel := &imports.ChannelImportData{
		Team:        model.NewString(team),
		Name:        model.NewString(channel.Name),
		DisplayName: model.NewString(channel.DisplayName),
		Type:        &channel.Type,
		Header:      &channel.Header,
		Purpose:     &channel.Purpose,
	}

	return &imports.LineImportData{
		Type:    "channel",
		Channel: newChannel,
	}
}

func GetImportLineFromDirectChannel(team string, channel *IntermediateChannel) *imports.LineImportData {
	return &imports.LineImportData{
		Type: "direct_channel",
		DirectChannel: &imports.DirectChannelImportData{
			Header:  &channel.Topic,
			Members: &channel.MembersUsernames,
		},
	}
}

func GetImportLineFromUser(user *IntermediateUser, team string, channelsOwner []string) *imports.LineImportData {
	channelMemberships := []imports.UserChannelImportData{}
	for _, channelName := range user.Memberships {
		channelRole := model.ChannelUserRoleId
		if slices.Contains(channelsOwner, channelName) {
			channelRole = model.ChannelAdminRoleId
		}
		channelMemberships = append(channelMemberships, imports.UserChannelImportData{
			Name:  model.NewString(channelName),
			Roles: model.NewString(channelRole),
		})
	}

	importUser := &imports.UserImportData{
		Username:    model.NewString(user.Username),
		Email:       model.NewString(user.Email),
		Nickname:    model.NewString(""),
		FirstName:   model.NewString(user.FirstName),
		LastName:    model.NewString(user.LastName),
		Position:    model.NewString(user.Position),
		Roles:       model.NewString(model.SystemUserRoleId),
		AuthService: model.NewString(user.AuthService),
		Teams: &[]imports.UserTeamImportData{
			{
				Name:     model.NewString(team),
				Channels: &channelMemberships,
				Roles:    model.NewString(model.TeamUserRoleId),
			},
		},
	}
	if user.AuthService != "" {
		importUser.AuthData = model.NewString(user.AuthData)
	} else {
		importUser.Password = model.NewString(user.Password)
	}

	if user.IsAdmin {
		importUser.Roles = model.NewString(model.SystemUserRoleId + " " + model.SystemAdminRoleId)
	}

	return &imports.LineImportData{
		Type: "user",
		User: importUser,
	}
}

func GetAttachmentImportDataFromPaths(paths []string) []imports.AttachmentImportData {
	attachments := []imports.AttachmentImportData{}
	for _, path := range paths {
		attachmentImportData := imports.AttachmentImportData{
			Path: model.NewString(path),
		}
		attachments = append(attachments, attachmentImportData)
	}
	return attachments
}

// This function returns a slice of replies containing all the
// attachments above the maximum number of attachments per post.
// The attachments that would fit in a post need to be processed
// outside this function
func createRepliesForAttachments(attachments []imports.AttachmentImportData, user string, createAt int64) []imports.ReplyImportData {
	replies := []imports.ReplyImportData{}

	if len(attachments) > POST_MAX_ATTACHMENTS {
		numberSplitPosts := len(attachments) / POST_MAX_ATTACHMENTS

		for i := 1; i <= numberSplitPosts; i++ {
			replyAttachments := attachments[POST_MAX_ATTACHMENTS*i:]

			if len(replyAttachments) > POST_MAX_ATTACHMENTS {
				replyAttachments = replyAttachments[0:POST_MAX_ATTACHMENTS]
			}

			newReply := imports.ReplyImportData{
				User:        model.NewString(user),
				Message:     model.NewString(""),
				CreateAt:    model.NewInt64(createAt + int64(i)),
				Attachments: &replyAttachments,
			}
			replies = append(replies, newReply)
		}
	}

	return replies
}

func GetImportLineFromPost(post *IntermediatePost, team string) (*imports.LineImportData, []string) {
	requiredAttachemnts := post.Attachments
	replies := []imports.ReplyImportData{}
	postAttachments := GetAttachmentImportDataFromPaths(post.Attachments)

	// If the post has more attachments than the maximum, create the
	// replies to contain the extra attachments
	if len(postAttachments) > POST_MAX_ATTACHMENTS {
		replies = append(replies, createRepliesForAttachments(postAttachments, post.User, post.CreateAt)...)
		postAttachments = postAttachments[0:POST_MAX_ATTACHMENTS]
	}

	for _, reply := range post.Replies {
		replyAttachments := GetAttachmentImportDataFromPaths(reply.Attachments)
		requiredAttachemnts = append(requiredAttachemnts, reply.Attachments...)

		// If a reply has more attachments than the maximum, create
		// more replies to contain the extra attachments
		if len(replyAttachments) > POST_MAX_ATTACHMENTS {
			replies = append(replies, createRepliesForAttachments(replyAttachments, reply.User, reply.CreateAt)...)
			replyAttachments = replyAttachments[0:POST_MAX_ATTACHMENTS]
		}

		newReply := imports.ReplyImportData{
			User:        &reply.User,
			Message:     &reply.Message,
			CreateAt:    &reply.CreateAt,
			Attachments: &replyAttachments,
		}
		replies = append(replies, newReply)
	}

	var newPost *imports.LineImportData
	if post.IsDirect {
		newPost = &imports.LineImportData{
			Type: "direct_post",
			DirectPost: &imports.DirectPostImportData{
				ChannelMembers: &post.ChannelMembers,
				User:           &post.User,
				Message:        &post.Message,
				Props:          &post.Props,
				CreateAt:       &post.CreateAt,
				Replies:        &replies,
				Attachments:    &postAttachments,
				Type:           &post.Type,
			},
		}
	} else {
		newPost = &imports.LineImportData{
			Type: "post",
			Post: &imports.PostImportData{
				Team:        model.NewString(team),
				Channel:     &post.Channel,
				User:        &post.User,
				Message:     &post.Message,
				Props:       &post.Props,
				CreateAt:    &post.CreateAt,
				Replies:     &replies,
				Attachments: &postAttachments,
				Type:        &post.Type,
			},
		}
	}

	return newPost, requiredAttachemnts
}

func ExportWriteLine(writer io.Writer, line *imports.LineImportData) error {
	b, err := json.Marshal(line)
	if err != nil {
		return errors.Wrap(err, "An error occurred marshalling the JSON data for export.")
	}

	if _, err := writer.Write(append(b, '\n')); err != nil {
		return errors.Wrap(err, "An error occurred writing the export data.")
	}

	return nil
}

func (t *Transformer) ExportVersion(writer io.Writer) error {
	version := 1
	versionLine := &imports.LineImportData{
		Type:    "version",
		Version: &version,
	}

	return ExportWriteLine(writer, versionLine)
}

// valid for open or private, as they export with no members
func (t *Transformer) ExportChannels(channels []*IntermediateChannel, writer io.Writer) error {
	for _, channel := range channels {
		line := GetImportLineFromChannel(t.TeamName, channel)
		if err := ExportWriteLine(writer, line); err != nil {
			return err
		}
	}

	return nil
}

// valid for group or direct, as they export with members
func (t *Transformer) ExportDirectChannels(channels []*IntermediateChannel, writer io.Writer) error {
	for _, channel := range channels {
		line := GetImportLineFromDirectChannel(t.TeamName, channel)
		if err := ExportWriteLine(writer, line); err != nil {
			return err
		}
	}

	return nil
}

func (t *Transformer) ExportUsers(writer io.Writer) error {
	for _, user := range t.Intermediate.UsersById {
		line := GetImportLineFromUser(user, t.TeamName, t.Intermediate.ChannelOwners[user.Id])
		if err := ExportWriteLine(writer, line); err != nil {
			return err
		}
	}

	return nil
}

func (t *Transformer) ExportPosts(writer io.Writer, from uint64, to uint64) (error, []string) {
	var attachments []string
	for _, post := range t.Intermediate.Posts[from:to] {
		line, postAttachments := GetImportLineFromPost(post, t.TeamName)
		if err := ExportWriteLine(writer, line); err != nil {
			return err, attachments
		}
		attachments = append(attachments, postAttachments...)
	}
	return nil, attachments
}

func (t *Transformer) Export(outputFilePath string, maxChunkSize uint) (error, []ChunkInfo, []string) {
	chunks := uint(1)
	posts := uint(len(t.Intermediate.Posts))

	if maxChunkSize == 0 {
		maxChunkSize = posts
	} else if posts > maxChunkSize {
		chunks = ((posts - 1) / maxChunkSize) + 1
	}

	var chunksInfo []ChunkInfo
	var exportedChannels []string

	for chunkN := uint(0); chunkN < chunks; chunkN++ {
		filePath := outputFilePath
		if chunks > 1 {
			if strings.HasSuffix(outputFilePath, ".jsonl") {
				filePathPrefix := outputFilePath[:(len(outputFilePath) - 6)]
				filePath = fmt.Sprintf("%s.%d.jsonl", filePathPrefix, chunkN)
			} else {
				filePath = fmt.Sprintf("%s.%d.jsonl", outputFilePath, chunkN)
			}
		}
		outputFile, err := os.Create(filePath)
		if err != nil {
			return err, chunksInfo, exportedChannels
		}
		defer outputFile.Close()

		chunkInfo := &ChunkInfo{
			Id:   chunkN,
			File: filePath,
		}

		t.Logger.Info("Exporting version")
		if err := t.ExportVersion(outputFile); err != nil {
			return err, chunksInfo, exportedChannels
		}

		if chunkN == 0 {
			t.Logger.Info("Exporting public channels")
			if err := t.ExportChannels(t.Intermediate.PublicChannels, outputFile); err != nil {
				return err, chunksInfo, exportedChannels
			}

			t.Logger.Info("Exporting private channels")
			if err := t.ExportChannels(t.Intermediate.PrivateChannels, outputFile); err != nil {
				return err, chunksInfo, exportedChannels
			}

			t.Logger.Info("Exporting users")
			if err := t.ExportUsers(outputFile); err != nil {
				return err, chunksInfo, exportedChannels
			}

			t.Logger.Info("Exporting group channels")
			if err := t.ExportDirectChannels(t.Intermediate.GroupChannels, outputFile); err != nil {
				return err, chunksInfo, exportedChannels
			}

			t.Logger.Info("Exporting direct channels")
			if err := t.ExportDirectChannels(t.Intermediate.DirectChannels, outputFile); err != nil {
				return err, chunksInfo, exportedChannels
			}
		}

		t.Logger.Info("Exporting posts")
		if posts > 0 {
			chunkStart := uint64(chunkN * maxChunkSize)
			chunkEnd := uint64((chunkN + 1) * maxChunkSize)
			if uint64(posts) < chunkEnd {
				chunkEnd = uint64(posts)
			}

			t.Logger.Infof("Export chunk %d - %d", (chunkStart + 1), chunkEnd)
			if err, chunkInfo.Attachments = t.ExportPosts(outputFile, chunkStart, chunkEnd); err != nil {
				return err, chunksInfo, exportedChannels
			}
		}
		chunksInfo = append(chunksInfo, *chunkInfo)
	}

	for _, channels := range [][]*IntermediateChannel{
		t.Intermediate.PublicChannels,
		t.Intermediate.PrivateChannels,
		t.Intermediate.GroupChannels,
		t.Intermediate.DirectChannels,
	} {
		for _, channel := range channels {
			exportedChannels = append(exportedChannels, channel.Id)
		}
	}

	return nil, chunksInfo, exportedChannels
}
