// Copyright (c) 2018-present Mattermost, Inc. All Rights Reserved.
// See License.txt for license information.

package app

import (
	"net/http"
	"reflect"
	"strings"

	"github.com/mattermost/mattermost-server/v5/model"
)

func (a *App) GetRole(id string) (*model.Role, *model.AppError) {
	return a.Srv.Store.Role().Get(id)
}

func (a *App) GetAllRoles() ([]*model.Role, *model.AppError) {
	return a.Srv.Store.Role().GetAll()
}

func (a *App) GetRoleByName(name string) (*model.Role, *model.AppError) {
	return a.Srv.Store.Role().GetByName(name)
}

func (a *App) GetRolesByNames(names []string) ([]*model.Role, *model.AppError) {
	return a.Srv.Store.Role().GetByNames(names)
}

func (a *App) PatchRole(role *model.Role, patch *model.RolePatch) (*model.Role, *model.AppError) {
	// If patch is a no-op then short-circuit the store.
	if patch.Permissions != nil && reflect.DeepEqual(*patch.Permissions, role.Permissions) {
		return role, nil
	}

	role.Patch(patch)
	role, err := a.UpdateRole(role)
	if err != nil {
		return nil, err
	}

	return role, err
}

func (a *App) CreateRole(role *model.Role) (*model.Role, *model.AppError) {
	role.Id = ""
	role.CreateAt = 0
	role.UpdateAt = 0
	role.DeleteAt = 0
	role.BuiltIn = false
	role.SchemeManaged = false

	return a.Srv.Store.Role().Save(role)

}

func (a *App) UpdateRole(role *model.Role) (*model.Role, *model.AppError) {
	savedRole, err := a.Srv.Store.Role().Save(role)
	if err != nil {
		return nil, err
	}
	a.sendUpdatedRoleEvent(savedRole)

	return savedRole, nil

}

func (a *App) CheckRolesExist(roleNames []string) *model.AppError {
	roles, err := a.GetRolesByNames(roleNames)
	if err != nil {
		return err
	}

	for _, name := range roleNames {
		nameFound := false
		for _, role := range roles {
			if name == role.Name {
				nameFound = true
				break
			}
		}
		if !nameFound {
			return model.NewAppError("CheckRolesExist", "app.role.check_roles_exist.role_not_found", nil, "role="+name, http.StatusBadRequest)
		}
	}

	return nil
}

func (a *App) sendUpdatedRoleEvent(role *model.Role) {
	message := model.NewWebSocketEvent(model.WEBSOCKET_EVENT_ROLE_UPDATED, "", "", "", nil)
	message.Add("role", role.ToJson())

	a.Srv.Go(func() {
		a.Publish(message)
	})
}

func RemoveRoles(rolesToRemove []string, roles string) string {
	roleList := strings.Fields(roles)
	newRoles := make([]string, 0)

	for _, role := range roleList {
		shouldRemove := false
		for _, roleToRemove := range rolesToRemove {
			if role == roleToRemove {
				shouldRemove = true
				break
			}
		}
		if !shouldRemove {
			newRoles = append(newRoles, role)
		}
	}

	return strings.Join(newRoles, " ")
}
