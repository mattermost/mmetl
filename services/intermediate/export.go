package intermediate

import (
	"encoding/json"
	"io"
	"os"
	"sort"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/v8/channels/app/imports"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
)

const POST_MAX_ATTACHMENTS = 5

// Exporter writes an Intermediate representation to the Mattermost bulk-import
// JSONL format. Source-specific transformers embed it to gain the Export* methods.
type Exporter struct {
	TeamName     string
	Intermediate *Intermediate
	Logger       log.FieldLogger
}

func GetImportLineFromChannel(team string, channel *IntermediateChannel) *imports.LineImportData {
	newChannel := &imports.ChannelImportData{
		Team:        model.NewPointer(team),
		Name:        model.NewPointer(channel.Name),
		DisplayName: model.NewPointer(channel.DisplayName),
		Type:        &channel.Type,
		Header:      &channel.Header,
		Purpose:     &channel.Purpose,
	}

	if channel.DeleteAt > 0 {
		newChannel.DeletedAt = model.NewPointer(channel.DeleteAt)
	}

	return &imports.LineImportData{
		Type:    "channel",
		Channel: newChannel,
	}
}

func GetImportLineFromDirectChannel(team string, channel *IntermediateChannel) *imports.LineImportData {
	var participants []*imports.DirectChannelMemberImportData
	lastViewedAt := channel.LastPostAt
	if lastViewedAt == 0 {
		lastViewedAt = channel.CreatedMillis()
	}
	for _, username := range channel.MembersUsernames {
		p := &imports.DirectChannelMemberImportData{
			Username:     model.NewPointer(username),
			LastViewedAt: model.NewPointer(lastViewedAt),
			SchemeUser:   model.NewPointer(true),
		}
		if channel.MsgCount > 0 {
			p.MsgCount = model.NewPointer(channel.MsgCount)
			p.MsgCountRoot = model.NewPointer(channel.MsgCountRoot)
		}
		participants = append(participants, p)
	}

	return &imports.LineImportData{
		Type: "direct_channel",
		DirectChannel: &imports.DirectChannelImportData{
			Header:       &channel.Topic,
			Members:      &channel.MembersUsernames,
			Participants: participants,
		},
	}
}

func GetImportLineFromUser(user *IntermediateUser, team string) *imports.LineImportData {
	channelMemberships := []imports.UserChannelImportData{}
	for _, membership := range user.Memberships {
		ch := imports.UserChannelImportData{
			Name:  model.NewPointer(membership.Name),
			Roles: model.NewPointer(model.ChannelUserRoleId),
		}
		if membership.LastViewedAt > 0 {
			ch.LastViewedAt = model.NewPointer(membership.LastViewedAt)
		}
		if membership.MsgCount > 0 {
			ch.MsgCount = model.NewPointer(membership.MsgCount)
			ch.MsgCountRoot = model.NewPointer(membership.MsgCountRoot)
		}
		channelMemberships = append(channelMemberships, ch)
	}

	var channelsPtr *[]imports.UserChannelImportData
	if len(channelMemberships) > 0 {
		channelsPtr = &channelMemberships
	}

	var deleteAt *int64
	if user.DeleteAt > 0 {
		deleteAt = model.NewPointer(user.DeleteAt)
	}

	return &imports.LineImportData{
		Type: "user",
		User: &imports.UserImportData{
			Username:  model.NewPointer(user.Username),
			Email:     model.NewPointer(user.Email),
			Nickname:  model.NewPointer(""),
			FirstName: model.NewPointer(user.FirstName),
			LastName:  model.NewPointer(user.LastName),
			Position:  model.NewPointer(user.Position),
			Roles:     model.NewPointer(model.SystemUserRoleId),
			DeleteAt:  deleteAt,
			Teams: &[]imports.UserTeamImportData{
				{
					Name:     model.NewPointer(team),
					Channels: channelsPtr,
					Roles:    model.NewPointer(model.TeamUserRoleId),
				},
			},
		},
	}
}

func GetImportLineFromBot(user *IntermediateUser, owner string) *imports.LineImportData {
	var deleteAt *int64
	if user.DeleteAt > 0 {
		deleteAt = model.NewPointer(user.DeleteAt)
	}

	return &imports.LineImportData{
		Type: "bot",
		Bot: &imports.BotImportData{
			Username:    model.NewPointer(user.Username),
			DisplayName: model.NewPointer(user.DisplayName),
			Owner:       model.NewPointer(owner),
			DeleteAt:    deleteAt,
		},
	}
}

func GetAttachmentImportDataFromPaths(paths []string) []imports.AttachmentImportData {
	attachments := []imports.AttachmentImportData{}
	for _, path := range paths {
		attachmentImportData := imports.AttachmentImportData{
			Path: model.NewPointer(path),
		}
		attachments = append(attachments, attachmentImportData)
	}
	return attachments
}

// createRepliesForAttachments returns a slice of replies containing all the
// attachments above the maximum number of attachments per post. The attachments
// that would fit in a post need to be processed outside this function.
func createRepliesForAttachments(attachments []imports.AttachmentImportData, user string, createAt int64) []imports.ReplyImportData {
	replies := []imports.ReplyImportData{}

	if len(attachments) > POST_MAX_ATTACHMENTS {
		numberSplitPosts := len(attachments) / POST_MAX_ATTACHMENTS

		for i := 1; i <= numberSplitPosts; i++ {
			replyAttachments := attachments[POST_MAX_ATTACHMENTS*i:]

			// On exact multiples of POST_MAX_ATTACHMENTS the integer division in
			// numberSplitPosts over-counts by one, leaving an empty trailing
			// slice. Skip it so we don't emit a spurious empty reply.
			if len(replyAttachments) == 0 {
				break
			}

			if len(replyAttachments) > POST_MAX_ATTACHMENTS {
				replyAttachments = replyAttachments[0:POST_MAX_ATTACHMENTS]
			}

			newReply := imports.ReplyImportData{
				User:        model.NewPointer(user),
				Message:     model.NewPointer(""),
				CreateAt:    model.NewPointer(createAt + int64(i)),
				Attachments: &replyAttachments,
			}
			replies = append(replies, newReply)
		}
	}

	return replies
}

func GetImportLineFromPost(post *IntermediatePost, team string) *imports.LineImportData {
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

		// If a reply has more attachments than the maximum, create
		// more replies to contain the extra attachments
		if len(replyAttachments) > POST_MAX_ATTACHMENTS {
			replies = append(replies, createRepliesForAttachments(replyAttachments, reply.User, reply.CreateAt)...)
			replyAttachments = replyAttachments[0:POST_MAX_ATTACHMENTS]
		}

		reactions := []imports.ReactionImportData{}
		for _, reaction := range reply.Reactions {
			newReaction := imports.ReactionImportData{
				User:      &reaction.User,
				EmojiName: &reaction.EmojiName,
				CreateAt:  &reaction.CreateAt,
			}
			reactions = append(reactions, newReaction)
		}

		newReply := imports.ReplyImportData{
			User:        &reply.User,
			Message:     &reply.Message,
			CreateAt:    &reply.CreateAt,
			Attachments: &replyAttachments,
			Reactions:   &reactions,
		}
		replies = append(replies, newReply)
	}

	reactions := []imports.ReactionImportData{}
	for _, reaction := range post.Reactions {
		newReaction := imports.ReactionImportData{
			User:      &reaction.User,
			EmojiName: &reaction.EmojiName,
			CreateAt:  &reaction.CreateAt,
		}
		reactions = append(reactions, newReaction)
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
				Reactions:      &reactions,
				Attachments:    &postAttachments,
				Type:           &post.Type,
			},
		}
	} else {
		newPost = &imports.LineImportData{
			Type: "post",
			Post: &imports.PostImportData{
				Team:        model.NewPointer(team),
				Channel:     &post.Channel,
				User:        &post.User,
				Message:     &post.Message,
				Props:       &post.Props,
				CreateAt:    &post.CreateAt,
				Replies:     &replies,
				Reactions:   &reactions,
				Attachments: &postAttachments,
				Type:        &post.Type,
			},
		}
	}

	return newPost
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

func (e *Exporter) ExportVersion(writer io.Writer) error {
	version := 1
	versionLine := &imports.LineImportData{
		Type:    "version",
		Version: &version,
	}

	return ExportWriteLine(writer, versionLine)
}

// ExportChannels is valid for open or private channels, as they export with no members.
func (e *Exporter) ExportChannels(channels []*IntermediateChannel, writer io.Writer) error {
	for _, channel := range channels {
		line := GetImportLineFromChannel(e.TeamName, channel)
		if err := ExportWriteLine(writer, line); err != nil {
			return err
		}
	}

	return nil
}

// ExportDirectChannels is valid for group or direct channels, as they export with members.
func (e *Exporter) ExportDirectChannels(channels []*IntermediateChannel, writer io.Writer) error {
	for _, channel := range channels {
		if channel.LastPostAt == 0 && channel.Created <= 0 {
			e.Logger.Warnf("Direct/group channel %s has no valid creation timestamp; using current time for LastViewedAt", channel.Name)
		}
		line := GetImportLineFromDirectChannel(e.TeamName, channel)
		if err := ExportWriteLine(writer, line); err != nil {
			return err
		}
	}

	return nil
}

func (e *Exporter) ExportUsers(writer io.Writer, botOwner string) error {
	// Collect users from map and sort them by username for deterministic output
	users := make([]*IntermediateUser, 0, len(e.Intermediate.UsersById))
	bots := make([]*IntermediateUser, 0)
	for _, user := range e.Intermediate.UsersById {
		if user.IsBot {
			bots = append(bots, user)
		} else {
			users = append(users, user)
		}
	}

	// Sort by username to ensure consistent ordering
	sort.Slice(users, func(i, j int) bool {
		return users[i].Username < users[j].Username
	})
	sort.Slice(bots, func(i, j int) bool {
		return bots[i].Username < bots[j].Username
	})

	// Write regular users first (bot owner must exist before bots)
	for _, user := range users {
		line := GetImportLineFromUser(user, e.TeamName)
		if err := ExportWriteLine(writer, line); err != nil {
			return err
		}
	}

	// Write bots after users
	if len(bots) > 0 && botOwner == "" {
		return errors.New("cannot export bots without a bot owner: please provide the --bot-owner flag")
	}
	for _, bot := range bots {
		line := GetImportLineFromBot(bot, botOwner)
		if err := ExportWriteLine(writer, line); err != nil {
			return err
		}
	}

	return nil
}

func (e *Exporter) ExportPosts(writer io.Writer) error {
	for _, post := range e.Intermediate.Posts {
		line := GetImportLineFromPost(post, e.TeamName)
		if err := ExportWriteLine(writer, line); err != nil {
			return err
		}
	}
	return nil
}

func (e *Exporter) Export(outputFilePath string, botOwner string) (err error) {
	outputFile, err := os.Create(outputFilePath)
	if err != nil {
		return err
	}
	defer func() {
		// Surface a close error (e.g. a failed buffered write) when the export
		// itself otherwise succeeded, so callers don't trust a truncated file.
		if closeErr := outputFile.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()

	e.Logger.Info("Exporting version")
	if err := e.ExportVersion(outputFile); err != nil {
		return err
	}

	e.Logger.Info("Exporting public channels")
	if err := e.ExportChannels(e.Intermediate.PublicChannels, outputFile); err != nil {
		return err
	}

	e.Logger.Info("Exporting private channels")
	if err := e.ExportChannels(e.Intermediate.PrivateChannels, outputFile); err != nil {
		return err
	}

	e.Logger.Info("Exporting users")
	if err := e.ExportUsers(outputFile, botOwner); err != nil {
		return err
	}

	e.Logger.Info("Exporting group channels")
	if err := e.ExportDirectChannels(e.Intermediate.GroupChannels, outputFile); err != nil {
		return err
	}

	e.Logger.Info("Exporting direct channels")
	if err := e.ExportDirectChannels(e.Intermediate.DirectChannels, outputFile); err != nil {
		return err
	}

	e.Logger.Info("Exporting posts")
	if err := e.ExportPosts(outputFile); err != nil {
		return err
	}

	return nil
}
