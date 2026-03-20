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

// PostMaxAttachments is the maximum number of file attachments per post or reply.
const PostMaxAttachments = 5

// Exporter holds the state needed to write Mattermost bulk-import JSONL.
type Exporter struct {
	TeamName     string
	Intermediate *Intermediate
	Logger       log.FieldLogger
}

// ExportWriteLine marshals line as JSON and writes it followed by a newline.
func ExportWriteLine(writer io.Writer, line *imports.LineImportData) error {
	b, err := json.Marshal(line)
	if err != nil {
		return errors.Wrap(err, "error marshalling JSON for export")
	}
	if _, err := writer.Write(append(b, '\n')); err != nil {
		return errors.Wrap(err, "error writing export line")
	}
	return nil
}

// Export writes the full Mattermost bulk import JSONL to outputFilePath.
// Export order: version, public channels, private channels, users,
// direct channels (group + direct), posts, direct posts.
func (e *Exporter) Export(outputFilePath string, botOwner string) error {
	f, err := os.Create(outputFilePath)
	if err != nil {
		return errors.Wrap(err, "error creating output file")
	}
	defer f.Close()

	e.Logger.Info("Exporting version")
	if err := e.ExportVersion(f); err != nil {
		return err
	}

	e.Logger.Info("Exporting public channels")
	if err := e.ExportChannels(e.Intermediate.PublicChannels, f); err != nil {
		return err
	}

	e.Logger.Info("Exporting private channels")
	if err := e.ExportChannels(e.Intermediate.PrivateChannels, f); err != nil {
		return err
	}

	e.Logger.Info("Exporting users")
	if err := e.ExportUsers(f, botOwner); err != nil {
		return err
	}

	e.Logger.Info("Exporting group channels")
	if err := e.ExportDirectChannels(e.Intermediate.GroupChannels, f); err != nil {
		return err
	}

	e.Logger.Info("Exporting direct channels")
	if err := e.ExportDirectChannels(e.Intermediate.DirectChannels, f); err != nil {
		return err
	}

	e.Logger.Info("Exporting posts")
	if err := e.ExportPosts(f); err != nil {
		return err
	}

	return nil
}

// ExportVersion writes the Mattermost bulk import version line.
func (e *Exporter) ExportVersion(w io.Writer) error {
	version := 1
	line := &imports.LineImportData{
		Type:    "version",
		Version: &version,
	}
	return ExportWriteLine(w, line)
}

// ExportChannels writes public or private channel import lines.
func (e *Exporter) ExportChannels(channels []*IntermediateChannel, w io.Writer) error {
	for _, ch := range channels {
		line := &imports.LineImportData{
			Type: "channel",
			Channel: &imports.ChannelImportData{
				Team:        model.NewPointer(e.TeamName),
				Name:        model.NewPointer(ch.Name),
				DisplayName: model.NewPointer(ch.DisplayName),
				Type:        &ch.Type,
				Header:      model.NewPointer(ch.Header),
				Purpose:     model.NewPointer(ch.Purpose),
			},
		}
		if err := ExportWriteLine(w, line); err != nil {
			return err
		}
	}
	return nil
}

// ExportDirectChannels writes direct and group channel import lines.
func (e *Exporter) ExportDirectChannels(channels []*IntermediateChannel, w io.Writer) error {
	for _, ch := range channels {
		members := ch.MembersUsernames
		dc := &imports.DirectChannelImportData{
			Members: &members,
		}
		if ch.Header != "" {
			dc.Header = model.NewPointer(ch.Header)
		}
		line := &imports.LineImportData{
			Type:          "direct_channel",
			DirectChannel: dc,
		}
		if err := ExportWriteLine(w, line); err != nil {
			return err
		}
	}
	return nil
}

// ExportUsers writes user import lines, sorted by username for deterministic output.
func (e *Exporter) ExportUsers(w io.Writer, botOwner string) error {
	users := make([]*IntermediateUser, 0, len(e.Intermediate.UsersById))
	bots := make([]*IntermediateUser, 0)
	for _, u := range e.Intermediate.UsersById {
		if u.IsBot {
			bots = append(bots, u)
		} else {
			users = append(users, u)
		}
	}

	sort.Slice(users, func(i, j int) bool {
		return users[i].Username < users[j].Username
	})
	sort.Slice(bots, func(i, j int) bool {
		return bots[i].Username < bots[j].Username
	})

	// Fail fast if bots exist but no owner is specified, before writing any data.
	if len(bots) > 0 && botOwner == "" {
		return errors.New("cannot export bots without a bot owner: please provide the --bot-owner flag")
	}

	// Write regular users first (bot owner must exist before bots).
	for _, u := range users {
		line := UserImportLine(u, e.TeamName)
		if err := ExportWriteLine(w, line); err != nil {
			return err
		}
	}

	// Write bots after users.
	for _, bot := range bots {
		line := BotImportLine(bot, botOwner)
		if err := ExportWriteLine(w, line); err != nil {
			return err
		}
	}

	return nil
}

// ExportPosts writes post and direct_post import lines.
func (e *Exporter) ExportPosts(w io.Writer) error {
	for _, post := range e.Intermediate.Posts {
		line := PostImportLine(post, e.TeamName)
		if err := ExportWriteLine(w, line); err != nil {
			return err
		}
	}
	return nil
}

// UserImportLine creates a Mattermost user import line.
func UserImportLine(u *IntermediateUser, team string) *imports.LineImportData {
	channelMemberships := make([]imports.UserChannelImportData, 0, len(u.Memberships))
	for _, chName := range u.Memberships {
		channelMemberships = append(channelMemberships, imports.UserChannelImportData{
			Name:  model.NewPointer(chName),
			Roles: model.NewPointer(model.ChannelUserRoleId),
		})
	}

	var channelsPtr *[]imports.UserChannelImportData
	if len(channelMemberships) > 0 {
		channelsPtr = &channelMemberships
	}

	var deleteAt *int64
	if u.DeleteAt > 0 {
		deleteAt = &u.DeleteAt
	}

	userData := &imports.UserImportData{
		Username:  model.NewPointer(u.Username),
		Email:     model.NewPointer(u.Email),
		Nickname:  model.NewPointer(""),
		FirstName: model.NewPointer(u.FirstName),
		LastName:  model.NewPointer(u.LastName),
		Position:  model.NewPointer(u.Position),
		Roles:     model.NewPointer(model.SystemUserRoleId),
		Teams: &[]imports.UserTeamImportData{
			{
				Name:     model.NewPointer(team),
				Roles:    model.NewPointer(model.TeamUserRoleId),
				Channels: channelsPtr,
			},
		},
		DeleteAt: deleteAt,
	}

	return &imports.LineImportData{
		Type: "user",
		User: userData,
	}
}

// BotImportLine creates a Mattermost bot import line for a bot user.
func BotImportLine(u *IntermediateUser, owner string) *imports.LineImportData {
	var deleteAt *int64
	if u.DeleteAt > 0 {
		deleteAt = &u.DeleteAt
	}

	return &imports.LineImportData{
		Type: "bot",
		Bot: &imports.BotImportData{
			Username:    model.NewPointer(u.Username),
			DisplayName: model.NewPointer(u.DisplayName),
			Owner:       model.NewPointer(owner),
			DeleteAt:    deleteAt,
		},
	}
}

// PostImportLine creates a Mattermost post or direct_post import line.
func PostImportLine(post *IntermediatePost, team string) *imports.LineImportData {
	reactions := convertReactionsForExport(post.Reactions)
	replies, postAttachments := buildRepliesAndAttachments(post, team)

	if post.IsDirect {
		return &imports.LineImportData{
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
	}

	return &imports.LineImportData{
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

// GetAttachmentImportData converts file paths to import attachment data.
func GetAttachmentImportData(paths []string) []imports.AttachmentImportData {
	attachments := make([]imports.AttachmentImportData, 0, len(paths))
	for _, p := range paths {
		attachments = append(attachments, imports.AttachmentImportData{
			Path: model.NewPointer(p),
		})
	}
	return attachments
}

func buildRepliesAndAttachments(post *IntermediatePost, team string) ([]imports.ReplyImportData, []imports.AttachmentImportData) {
	postAttachments := GetAttachmentImportData(post.Attachments)
	var extraReplies []imports.ReplyImportData

	if len(postAttachments) > PostMaxAttachments {
		extraReplies = append(extraReplies, createRepliesForAttachments(postAttachments[PostMaxAttachments:], post.User, post.CreateAt)...)
		postAttachments = postAttachments[:PostMaxAttachments]
	}

	var replies []imports.ReplyImportData
	for _, reply := range post.Replies {
		replyAttachments := GetAttachmentImportData(reply.Attachments)
		if len(replyAttachments) > PostMaxAttachments {
			extraReplies = append(extraReplies, createRepliesForAttachments(replyAttachments[PostMaxAttachments:], reply.User, reply.CreateAt)...)
			replyAttachments = replyAttachments[:PostMaxAttachments]
		}

		replyReactions := convertReactionsForExport(reply.Reactions)
		replies = append(replies, imports.ReplyImportData{
			User:        &reply.User,
			Message:     &reply.Message,
			CreateAt:    &reply.CreateAt,
			Attachments: &replyAttachments,
			Reactions:   &replyReactions,
		})
	}

	replies = append(replies, extraReplies...)

	// Sort all replies (real + synthetic overflow) by timestamp so that the
	// exported thread is in chronological order regardless of append order.
	sort.Slice(replies, func(i, j int) bool {
		return *replies[i].CreateAt < *replies[j].CreateAt
	})

	// Deduplicate timestamps: if a synthetic overflow reply lands on a timestamp
	// already used by a preceding reply, bump it forward by one millisecond.
	for i := 1; i < len(replies); i++ {
		if *replies[i].CreateAt <= *replies[i-1].CreateAt {
			ts := *replies[i-1].CreateAt + 1
			replies[i].CreateAt = &ts
		}
	}

	return replies, postAttachments
}

func createRepliesForAttachments(attachments []imports.AttachmentImportData, user string, createAt int64) []imports.ReplyImportData {
	var replies []imports.ReplyImportData
	for i := 0; i < len(attachments); i += PostMaxAttachments {
		end := i + PostMaxAttachments
		if end > len(attachments) {
			end = len(attachments)
		}
		chunk := attachments[i:end]
		msg := ""
		ts := createAt + int64(i/PostMaxAttachments+1)
		replies = append(replies, imports.ReplyImportData{
			User:        &user,
			Message:     &msg,
			CreateAt:    &ts,
			Attachments: &chunk,
		})
	}
	return replies
}

func convertReactionsForExport(reactions []*IntermediateReaction) []imports.ReactionImportData {
	result := make([]imports.ReactionImportData, 0, len(reactions))
	for _, r := range reactions {
		result = append(result, imports.ReactionImportData{
			User:      &r.User,
			EmojiName: &r.EmojiName,
			CreateAt:  &r.CreateAt,
		})
	}
	return result
}
