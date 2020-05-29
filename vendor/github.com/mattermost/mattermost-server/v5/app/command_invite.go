// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package app

import (
	"strings"

	goi18n "github.com/mattermost/go-i18n/i18n"

	"github.com/mattermost/mattermost-server/v5/mlog"
	"github.com/mattermost/mattermost-server/v5/model"
)

type InviteProvider struct {
}

const (
	CMD_INVITE = "invite"
)

func init() {
	RegisterCommandProvider(&InviteProvider{})
}

func (me *InviteProvider) GetTrigger() string {
	return CMD_INVITE
}

func (me *InviteProvider) GetCommand(a *App, T goi18n.TranslateFunc) *model.Command {
	return &model.Command{
		Trigger:          CMD_INVITE,
		AutoComplete:     true,
		AutoCompleteDesc: T("api.command_invite.desc"),
		AutoCompleteHint: T("api.command_invite.hint"),
		DisplayName:      T("api.command_invite.name"),
	}
}

func (me *InviteProvider) DoCommand(a *App, args *model.CommandArgs, message string) *model.CommandResponse {
	if message == "" {
		return &model.CommandResponse{
			Text:         args.T("api.command_invite.missing_message.app_error"),
			ResponseType: model.COMMAND_RESPONSE_TYPE_EPHEMERAL,
		}
	}

	splitMessage := strings.SplitN(message, " ", 2)
	targetUsername := splitMessage[0]
	targetUsername = strings.TrimPrefix(targetUsername, "@")

	userProfile, err := a.Srv().Store.User().GetByUsername(targetUsername)
	if err != nil {
		mlog.Error(err.Error())
		return &model.CommandResponse{
			Text:         args.T("api.command_invite.missing_user.app_error"),
			ResponseType: model.COMMAND_RESPONSE_TYPE_EPHEMERAL,
		}
	}

	if userProfile.DeleteAt != 0 {
		return &model.CommandResponse{
			Text:         args.T("api.command_invite.missing_user.app_error"),
			ResponseType: model.COMMAND_RESPONSE_TYPE_EPHEMERAL,
		}
	}

	var channelToJoin *model.Channel
	// User set a channel to add the invited user
	if len(splitMessage) > 1 && splitMessage[1] != "" {
		targetChannelName := strings.TrimPrefix(strings.TrimSpace(splitMessage[1]), "~")

		if channelToJoin, err = a.GetChannelByName(targetChannelName, args.TeamId, false); err != nil {
			return &model.CommandResponse{
				Text: args.T("api.command_invite.channel.error", map[string]interface{}{
					"Channel": targetChannelName,
				}),
				ResponseType: model.COMMAND_RESPONSE_TYPE_EPHEMERAL,
			}
		}
	} else {
		channelToJoin, err = a.GetChannel(args.ChannelId)
		if err != nil {
			return &model.CommandResponse{
				Text:         args.T("api.command_invite.channel.app_error"),
				ResponseType: model.COMMAND_RESPONSE_TYPE_EPHEMERAL,
			}
		}
	}

	// Permissions Check
	switch channelToJoin.Type {
	case model.CHANNEL_OPEN:
		if !a.SessionHasPermissionToChannel(args.Session, channelToJoin.Id, model.PERMISSION_MANAGE_PUBLIC_CHANNEL_MEMBERS) {
			return &model.CommandResponse{
				Text: args.T("api.command_invite.permission.app_error", map[string]interface{}{
					"User":    userProfile.Username,
					"Channel": channelToJoin.Name,
				}),
				ResponseType: model.COMMAND_RESPONSE_TYPE_EPHEMERAL,
			}
		}
	case model.CHANNEL_PRIVATE:
		if !a.SessionHasPermissionToChannel(args.Session, channelToJoin.Id, model.PERMISSION_MANAGE_PRIVATE_CHANNEL_MEMBERS) {
			if _, err = a.GetChannelMember(channelToJoin.Id, args.UserId); err == nil {
				// User doing the inviting is a member of the channel.
				return &model.CommandResponse{
					Text: args.T("api.command_invite.permission.app_error", map[string]interface{}{
						"User":    userProfile.Username,
						"Channel": channelToJoin.Name,
					}),
					ResponseType: model.COMMAND_RESPONSE_TYPE_EPHEMERAL,
				}
			} else {
				// User doing the inviting is *not* a member of the channel.
				return &model.CommandResponse{
					Text: args.T("api.command_invite.private_channel.app_error", map[string]interface{}{
						"Channel": channelToJoin.Name,
					}),
					ResponseType: model.COMMAND_RESPONSE_TYPE_EPHEMERAL,
				}
			}
		}
	default:
		return &model.CommandResponse{
			Text:         args.T("api.command_invite.directchannel.app_error"),
			ResponseType: model.COMMAND_RESPONSE_TYPE_EPHEMERAL,
		}
	}

	// Check if user is already in the channel
	_, err = a.GetChannelMember(channelToJoin.Id, userProfile.Id)
	if err == nil {
		return &model.CommandResponse{
			Text: args.T("api.command_invite.user_already_in_channel.app_error", map[string]interface{}{
				"User": userProfile.Username,
			}),
			ResponseType: model.COMMAND_RESPONSE_TYPE_EPHEMERAL,
		}
	}

	if _, err := a.AddChannelMember(userProfile.Id, channelToJoin, args.Session.UserId, ""); err != nil {
		var text string
		if err.Id == "api.channel.add_members.user_denied" {
			text = args.T("api.command_invite.group_constrained_user_denied")
		} else if err.Id == "store.sql_team.get_member.missing.app_error" ||
			err.Id == "api.channel.add_user.to.channel.failed.deleted.app_error" {
			text = args.T("api.command_invite.user_not_in_team.app_error", map[string]interface{}{
				"Username": userProfile.Username,
			})
		} else {
			text = args.T("api.command_invite.fail.app_error")
		}
		return &model.CommandResponse{
			Text:         text,
			ResponseType: model.COMMAND_RESPONSE_TYPE_EPHEMERAL,
		}
	}

	if args.ChannelId != channelToJoin.Id {
		return &model.CommandResponse{
			Text: args.T("api.command_invite.success", map[string]interface{}{
				"User":    userProfile.Username,
				"Channel": channelToJoin.Name,
			}),
			ResponseType: model.COMMAND_RESPONSE_TYPE_EPHEMERAL,
		}
	}

	return &model.CommandResponse{}
}
