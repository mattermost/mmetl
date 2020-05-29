// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package app

import (
	"strconv"
	"strings"
	"time"

	goi18n "github.com/mattermost/go-i18n/i18n"
	"github.com/mattermost/mattermost-server/v5/mlog"
	"github.com/mattermost/mattermost-server/v5/model"
)

var echoSem chan bool

type EchoProvider struct {
}

const (
	CMD_ECHO = "echo"
)

func init() {
	RegisterCommandProvider(&EchoProvider{})
}

func (me *EchoProvider) GetTrigger() string {
	return CMD_ECHO
}

func (me *EchoProvider) GetCommand(a *App, T goi18n.TranslateFunc) *model.Command {
	return &model.Command{
		Trigger:          CMD_ECHO,
		AutoComplete:     true,
		AutoCompleteDesc: T("api.command_echo.desc"),
		AutoCompleteHint: T("api.command_echo.hint"),
		DisplayName:      T("api.command_echo.name"),
	}
}

func (me *EchoProvider) DoCommand(a *App, args *model.CommandArgs, message string) *model.CommandResponse {
	if len(message) == 0 {
		return &model.CommandResponse{Text: args.T("api.command_echo.message.app_error"), ResponseType: model.COMMAND_RESPONSE_TYPE_EPHEMERAL}
	}

	maxThreads := 100

	delay := 0
	if endMsg := strings.LastIndex(message, "\""); string(message[0]) == "\"" && endMsg > 1 {
		if checkDelay, err := strconv.Atoi(strings.Trim(message[endMsg:], " \"")); err == nil {
			delay = checkDelay
		}
		message = message[1:endMsg]
	} else if strings.Contains(message, " ") {
		delayIdx := strings.LastIndex(message, " ")
		delayStr := strings.Trim(message[delayIdx:], " ")

		if checkDelay, err := strconv.Atoi(delayStr); err == nil {
			delay = checkDelay
			message = message[:delayIdx]
		}
	}

	if delay > 10000 {
		return &model.CommandResponse{Text: args.T("api.command_echo.delay.app_error"), ResponseType: model.COMMAND_RESPONSE_TYPE_EPHEMERAL}
	}

	if echoSem == nil {
		// We want one additional thread allowed so we never reach channel lockup
		echoSem = make(chan bool, maxThreads+1)
	}

	if len(echoSem) >= maxThreads {
		return &model.CommandResponse{Text: args.T("api.command_echo.high_volume.app_error"), ResponseType: model.COMMAND_RESPONSE_TYPE_EPHEMERAL}
	}

	echoSem <- true
	a.Srv().Go(func() {
		defer func() { <-echoSem }()
		post := &model.Post{}
		post.ChannelId = args.ChannelId
		post.RootId = args.RootId
		post.ParentId = args.ParentId
		post.Message = message
		post.UserId = args.UserId

		time.Sleep(time.Duration(delay) * time.Second)

		if _, err := a.CreatePostMissingChannel(post, true); err != nil {
			mlog.Error("Unable to create /echo post.", mlog.Err(err))
		}
	})

	return &model.CommandResponse{}
}
