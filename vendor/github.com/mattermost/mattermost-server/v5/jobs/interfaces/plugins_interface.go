// Copyright (c) 2018-present Mattermost, Inc. All Rights Reserved.
// See License.txt for license information.

package interfaces

import "github.com/mattermost/mattermost-server/v5/model"

type PluginsJobInterface interface {
	MakeWorker() model.Worker
	MakeScheduler() model.Scheduler
}
