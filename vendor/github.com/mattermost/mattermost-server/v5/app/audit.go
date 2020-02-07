// Copyright (c) 2017-present Mattermost, Inc. All Rights Reserved.
// See License.txt for license information.

package app

import (
	"github.com/mattermost/mattermost-server/v5/model"
)

func (a *App) GetAudits(userId string, limit int) (model.Audits, *model.AppError) {
	return a.Srv.Store.Audit().Get(userId, 0, limit)
}

func (a *App) GetAuditsPage(userId string, page int, perPage int) (model.Audits, *model.AppError) {
	return a.Srv.Store.Audit().Get(userId, page*perPage, perPage)
}
