// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package sqlstore

import (
	"database/sql"
	"fmt"
	"net/http"
	"strings"

	sq "github.com/Masterminds/squirrel"
	"github.com/mattermost/gorp"
	"github.com/mattermost/mattermost-server/v5/model"
	"github.com/mattermost/mattermost-server/v5/store"
	"github.com/mattermost/mattermost-server/v5/utils"
)

const (
	TEAM_MEMBER_EXISTS_ERROR = "store.sql_team.save_member.exists.app_error"
)

type SqlTeamStore struct {
	SqlStore
}

type teamMember struct {
	TeamId      string
	UserId      string
	Roles       string
	DeleteAt    int64
	SchemeUser  sql.NullBool
	SchemeAdmin sql.NullBool
	SchemeGuest sql.NullBool
}

func NewTeamMemberFromModel(tm *model.TeamMember) *teamMember {
	return &teamMember{
		TeamId:      tm.TeamId,
		UserId:      tm.UserId,
		Roles:       tm.ExplicitRoles,
		DeleteAt:    tm.DeleteAt,
		SchemeGuest: sql.NullBool{Valid: true, Bool: tm.SchemeGuest},
		SchemeUser:  sql.NullBool{Valid: true, Bool: tm.SchemeUser},
		SchemeAdmin: sql.NullBool{Valid: true, Bool: tm.SchemeAdmin},
	}
}

type teamMemberWithSchemeRoles struct {
	TeamId                     string
	UserId                     string
	Roles                      string
	DeleteAt                   int64
	SchemeGuest                sql.NullBool
	SchemeUser                 sql.NullBool
	SchemeAdmin                sql.NullBool
	TeamSchemeDefaultGuestRole sql.NullString
	TeamSchemeDefaultUserRole  sql.NullString
	TeamSchemeDefaultAdminRole sql.NullString
}

type teamMemberWithSchemeRolesList []teamMemberWithSchemeRoles

func (db teamMemberWithSchemeRoles) ToModel() *model.TeamMember {
	var roles []string
	var explicitRoles []string

	// Identify any scheme derived roles that are in "Roles" field due to not yet being migrated, and exclude
	// them from ExplicitRoles field.
	schemeGuest := db.SchemeGuest.Valid && db.SchemeGuest.Bool
	schemeUser := db.SchemeUser.Valid && db.SchemeUser.Bool
	schemeAdmin := db.SchemeAdmin.Valid && db.SchemeAdmin.Bool
	for _, role := range strings.Fields(db.Roles) {
		isImplicit := false
		if role == model.TEAM_GUEST_ROLE_ID {
			// We have an implicit role via the system scheme. Override the "schemeGuest" field to true.
			schemeGuest = true
			isImplicit = true
		} else if role == model.TEAM_USER_ROLE_ID {
			// We have an implicit role via the system scheme. Override the "schemeUser" field to true.
			schemeUser = true
			isImplicit = true
		} else if role == model.TEAM_ADMIN_ROLE_ID {
			// We have an implicit role via the system scheme.
			schemeAdmin = true
			isImplicit = true
		}

		if !isImplicit {
			explicitRoles = append(explicitRoles, role)
		}
		roles = append(roles, role)
	}

	// Add any scheme derived roles that are not in the Roles field due to being Implicit from the Scheme, and add
	// them to the Roles field for backwards compatibility reasons.
	var schemeImpliedRoles []string
	if db.SchemeGuest.Valid && db.SchemeGuest.Bool {
		if db.TeamSchemeDefaultGuestRole.Valid && db.TeamSchemeDefaultGuestRole.String != "" {
			schemeImpliedRoles = append(schemeImpliedRoles, db.TeamSchemeDefaultGuestRole.String)
		} else {
			schemeImpliedRoles = append(schemeImpliedRoles, model.TEAM_GUEST_ROLE_ID)
		}
	}
	if db.SchemeUser.Valid && db.SchemeUser.Bool {
		if db.TeamSchemeDefaultUserRole.Valid && db.TeamSchemeDefaultUserRole.String != "" {
			schemeImpliedRoles = append(schemeImpliedRoles, db.TeamSchemeDefaultUserRole.String)
		} else {
			schemeImpliedRoles = append(schemeImpliedRoles, model.TEAM_USER_ROLE_ID)
		}
	}
	if db.SchemeAdmin.Valid && db.SchemeAdmin.Bool {
		if db.TeamSchemeDefaultAdminRole.Valid && db.TeamSchemeDefaultAdminRole.String != "" {
			schemeImpliedRoles = append(schemeImpliedRoles, db.TeamSchemeDefaultAdminRole.String)
		} else {
			schemeImpliedRoles = append(schemeImpliedRoles, model.TEAM_ADMIN_ROLE_ID)
		}
	}
	for _, impliedRole := range schemeImpliedRoles {
		alreadyThere := false
		for _, role := range roles {
			if role == impliedRole {
				alreadyThere = true
			}
		}
		if !alreadyThere {
			roles = append(roles, impliedRole)
		}
	}

	tm := &model.TeamMember{
		TeamId:        db.TeamId,
		UserId:        db.UserId,
		Roles:         strings.Join(roles, " "),
		DeleteAt:      db.DeleteAt,
		SchemeGuest:   schemeGuest,
		SchemeUser:    schemeUser,
		SchemeAdmin:   schemeAdmin,
		ExplicitRoles: strings.Join(explicitRoles, " "),
	}
	return tm
}

func (db teamMemberWithSchemeRolesList) ToModel() []*model.TeamMember {
	tms := make([]*model.TeamMember, 0)

	for _, tm := range db {
		tms = append(tms, tm.ToModel())
	}

	return tms
}

func newSqlTeamStore(sqlStore SqlStore) store.TeamStore {
	s := &SqlTeamStore{
		sqlStore,
	}

	for _, db := range sqlStore.GetAllConns() {
		table := db.AddTableWithName(model.Team{}, "Teams").SetKeys(false, "Id")
		table.ColMap("Id").SetMaxSize(26)
		table.ColMap("DisplayName").SetMaxSize(64)
		table.ColMap("Name").SetMaxSize(64).SetUnique(true)
		table.ColMap("Description").SetMaxSize(255)
		table.ColMap("Email").SetMaxSize(128)
		table.ColMap("CompanyName").SetMaxSize(64)
		table.ColMap("AllowedDomains").SetMaxSize(1000)
		table.ColMap("InviteId").SetMaxSize(32)

		tablem := db.AddTableWithName(teamMember{}, "TeamMembers").SetKeys(false, "TeamId", "UserId")
		tablem.ColMap("TeamId").SetMaxSize(26)
		tablem.ColMap("UserId").SetMaxSize(26)
		tablem.ColMap("Roles").SetMaxSize(64)
	}

	return s
}

func (s SqlTeamStore) createIndexesIfNotExists() {
	s.CreateIndexIfNotExists("idx_teams_name", "Teams", "Name")
	s.RemoveIndexIfExists("idx_teams_description", "Teams")
	s.CreateIndexIfNotExists("idx_teams_invite_id", "Teams", "InviteId")
	s.CreateIndexIfNotExists("idx_teams_update_at", "Teams", "UpdateAt")
	s.CreateIndexIfNotExists("idx_teams_create_at", "Teams", "CreateAt")
	s.CreateIndexIfNotExists("idx_teams_delete_at", "Teams", "DeleteAt")
	s.CreateIndexIfNotExists("idx_teams_scheme_id", "Teams", "SchemeId")

	s.CreateIndexIfNotExists("idx_teammembers_team_id", "TeamMembers", "TeamId")
	s.CreateIndexIfNotExists("idx_teammembers_user_id", "TeamMembers", "UserId")
	s.CreateIndexIfNotExists("idx_teammembers_delete_at", "TeamMembers", "DeleteAt")
}

// Save adds the team to the database if a team with the same name does not already
// exist in the database. It returns the team added if the operation is successful.
func (s SqlTeamStore) Save(team *model.Team) (*model.Team, *model.AppError) {
	if len(team.Id) > 0 {
		return nil, model.NewAppError("SqlTeamStore.Save",
			"store.sql_team.save.existing.app_error", nil, "id="+team.Id, http.StatusBadRequest)
	}

	team.PreSave()

	if err := team.IsValid(); err != nil {
		return nil, err
	}

	if err := s.GetMaster().Insert(team); err != nil {
		if IsUniqueConstraintError(err, []string{"Name", "teams_name_key"}) {
			return nil, model.NewAppError("SqlTeamStore.Save", "store.sql_team.save.domain_exists.app_error", nil, "id="+team.Id+", "+err.Error(), http.StatusBadRequest)
		}
		return nil, model.NewAppError("SqlTeamStore.Save", "store.sql_team.save.app_error", nil, "id="+team.Id+", "+err.Error(), http.StatusInternalServerError)
	}
	return team, nil
}

// Update updates the details of the team passed as the parameter using the team Id
// if the team exists in the database.
// It returns the updated team if the operation is successful.
func (s SqlTeamStore) Update(team *model.Team) (*model.Team, *model.AppError) {

	team.PreUpdate()

	if err := team.IsValid(); err != nil {
		return nil, err
	}

	oldResult, err := s.GetMaster().Get(model.Team{}, team.Id)
	if err != nil {
		return nil, model.NewAppError("SqlTeamStore.Update", "store.sql_team.update.finding.app_error", nil, "id="+team.Id+", "+err.Error(), http.StatusInternalServerError)

	}

	if oldResult == nil {
		return nil, model.NewAppError("SqlTeamStore.Update", "store.sql_team.update.find.app_error", nil, "id="+team.Id, http.StatusBadRequest)
	}

	oldTeam := oldResult.(*model.Team)
	team.CreateAt = oldTeam.CreateAt
	team.UpdateAt = model.GetMillis()

	count, err := s.GetMaster().Update(team)
	if err != nil {
		return nil, model.NewAppError("SqlTeamStore.Update", "store.sql_team.update.updating.app_error", nil, "id="+team.Id+", "+err.Error(), http.StatusInternalServerError)
	}
	if count != 1 {
		return nil, model.NewAppError("SqlTeamStore.Update", "store.sql_team.update.app_error", nil, "id="+team.Id, http.StatusInternalServerError)
	}

	return team, nil
}

func (s SqlTeamStore) Get(id string) (*model.Team, *model.AppError) {
	obj, err := s.GetReplica().Get(model.Team{}, id)
	if err != nil {
		return nil, model.NewAppError("SqlTeamStore.Get", "store.sql_team.get.finding.app_error", nil, "id="+id+", "+err.Error(), http.StatusInternalServerError)
	}
	if obj == nil {
		return nil, model.NewAppError("SqlTeamStore.Get", "store.sql_team.get.find.app_error", nil, "id="+id, http.StatusNotFound)
	}

	return obj.(*model.Team), nil
}

func (s SqlTeamStore) GetByInviteId(inviteId string) (*model.Team, *model.AppError) {
	team := model.Team{}

	err := s.GetReplica().SelectOne(&team, "SELECT * FROM Teams WHERE InviteId = :InviteId", map[string]interface{}{"InviteId": inviteId})
	if err != nil {
		return nil, model.NewAppError("SqlTeamStore.GetByInviteId", "store.sql_team.get_by_invite_id.finding.app_error", nil, "inviteId="+inviteId+", "+err.Error(), http.StatusNotFound)
	}

	if len(inviteId) == 0 || team.InviteId != inviteId {
		return nil, model.NewAppError("SqlTeamStore.GetByInviteId", "store.sql_team.get_by_invite_id.find.app_error", nil, "inviteId="+inviteId, http.StatusNotFound)
	}
	return &team, nil
}

func (s SqlTeamStore) GetByName(name string) (*model.Team, *model.AppError) {

	team := model.Team{}

	err := s.GetReplica().SelectOne(&team, "SELECT * FROM Teams WHERE Name = :Name", map[string]interface{}{"Name": name})
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, model.NewAppError("SqlTeamStore.GetByName", "store.sql_team.get_by_name.missing.app_error", nil, "name="+name+","+err.Error(), http.StatusNotFound)
		}
		return nil, model.NewAppError("SqlTeamStore.GetByName", "store.sql_team.get_by_name.app_error", nil, "name="+name+", "+err.Error(), http.StatusInternalServerError)
	}
	return &team, nil
}

func (s SqlTeamStore) GetByNames(names []string) ([]*model.Team, *model.AppError) {
	uniqueNames := utils.RemoveDuplicatesFromStringArray(names)

	query := s.getQueryBuilder().
		Select("*").
		From("Teams").
		Where(sq.Eq{"Name": uniqueNames})

	queryString, args, err := query.ToSql()
	if err != nil {
		return nil, model.NewAppError("SqlTeamStore.GetByNames", "store.sql_team.get_by_names.app_error", nil, err.Error(), http.StatusInternalServerError)
	}

	teams := []*model.Team{}
	_, err = s.GetReplica().Select(&teams, queryString, args...)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, model.NewAppError("SqlTeamStore.GetByNames", "store.sql_team.get_by_names.missing.app_error", nil, err.Error(), http.StatusNotFound)
		}
		return nil, model.NewAppError("SqlTeamStore.GetByNames", "store.sql_team.get_by_names.app_error", nil, err.Error(), http.StatusInternalServerError)
	}
	if len(teams) != len(uniqueNames) {
		return nil, model.NewAppError("SqlTeamStore.GetByNames", "store.sql_team.get_by_names.missing.app_error", nil, "", http.StatusNotFound)
	}
	return teams, nil
}

func (s SqlTeamStore) SearchAll(term string) ([]*model.Team, *model.AppError) {
	var teams []*model.Team

	term = sanitizeSearchTerm(term, "\\")

	if _, err := s.GetReplica().Select(&teams, "SELECT * FROM Teams WHERE Name LIKE :Term OR DisplayName LIKE :Term", map[string]interface{}{"Term": term + "%"}); err != nil {
		return nil, model.NewAppError("SqlTeamStore.SearchAll", "store.sql_team.search_all_team.app_error", nil, "term="+term+", "+err.Error(), http.StatusInternalServerError)
	}

	return teams, nil
}

// SearchAllPaged returns a teams list and the total count of teams that matched the search.
func (s SqlTeamStore) SearchAllPaged(term string, page int, perPage int) ([]*model.Team, int64, *model.AppError) {
	var teams []*model.Team
	var totalCount int64
	offset := page * perPage

	term = sanitizeSearchTerm(term, "\\")

	if _, err := s.GetReplica().Select(&teams, "SELECT * FROM Teams WHERE Name LIKE :Term OR DisplayName LIKE :Term ORDER BY DisplayName, Name LIMIT :Limit OFFSET :Offset", map[string]interface{}{"Term": term + "%", "Limit": perPage, "Offset": offset}); err != nil {
		return nil, 0, model.NewAppError("SqlTeamStore.SearchAllPage", "store.sql_team.search_all_team.app_error", nil, "term="+term+", "+err.Error(), http.StatusInternalServerError)
	}

	totalCount, err := s.GetReplica().SelectInt("SELECT COUNT(*) FROM Teams WHERE Name LIKE :Term OR DisplayName LIKE :Term", map[string]interface{}{"Term": term + "%"})
	if err != nil {
		return nil, 0, model.NewAppError("SqlTeamStore.SearchAllPage", "store.sql_team.search_all_team.app_error", nil, "term="+term+", "+err.Error(), http.StatusInternalServerError)
	}

	return teams, totalCount, nil
}

func (s SqlTeamStore) SearchOpen(term string) ([]*model.Team, *model.AppError) {
	var teams []*model.Team

	term = sanitizeSearchTerm(term, "\\")

	if _, err := s.GetReplica().Select(&teams, "SELECT * FROM Teams WHERE Type = 'O' AND AllowOpenInvite = true AND (Name LIKE :Term OR DisplayName LIKE :Term)", map[string]interface{}{"Term": term + "%"}); err != nil {
		return nil, model.NewAppError("SqlTeamStore.SearchOpen", "store.sql_team.search_open_team.app_error", nil, "term="+term+", "+err.Error(), http.StatusInternalServerError)
	}

	return teams, nil
}

func (s SqlTeamStore) SearchPrivate(term string) ([]*model.Team, *model.AppError) {
	var teams []*model.Team

	term = sanitizeSearchTerm(term, "\\")

	query :=
		`SELECT *
		FROM
			Teams
		WHERE
			(Type != 'O' OR AllowOpenInvite = false) AND
			(Name LIKE :Term OR DisplayName LIKE :Term)`
	if _, err := s.GetReplica().Select(&teams, query, map[string]interface{}{"Term": term + "%"}); err != nil {
		return nil, model.NewAppError("SqlTeamStore.SearchPrivate", "store.sql_team.search_private_team.app_error", nil, "term="+term+", "+err.Error(), http.StatusInternalServerError)
	}
	return teams, nil
}

func (s SqlTeamStore) GetAll() ([]*model.Team, *model.AppError) {
	var teams []*model.Team

	_, err := s.GetReplica().Select(&teams, "SELECT * FROM Teams ORDER BY DisplayName")
	if err != nil {
		return nil, model.NewAppError("SqlTeamStore.GetAllTeams", "store.sql_team.get_all.app_error", nil, err.Error(), http.StatusInternalServerError)
	}
	return teams, nil
}

func (s SqlTeamStore) GetAllPage(offset int, limit int) ([]*model.Team, *model.AppError) {
	var teams []*model.Team

	if _, err := s.GetReplica().Select(&teams,
		`SELECT
			*
		FROM
			Teams
		ORDER BY
			DisplayName
		LIMIT
			:Limit
		OFFSET
			:Offset`, map[string]interface{}{"Offset": offset, "Limit": limit}); err != nil {
		return nil, model.NewAppError("SqlTeamStore.GetAllTeams",
			"store.sql_team.get_all.app_error", nil, err.Error(), http.StatusInternalServerError)
	}

	return teams, nil
}

func (s SqlTeamStore) GetTeamsByUserId(userId string) ([]*model.Team, *model.AppError) {
	var teams []*model.Team
	if _, err := s.GetReplica().Select(&teams, "SELECT Teams.* FROM Teams, TeamMembers WHERE TeamMembers.TeamId = Teams.Id AND TeamMembers.UserId = :UserId AND TeamMembers.DeleteAt = 0 AND Teams.DeleteAt = 0", map[string]interface{}{"UserId": userId}); err != nil {
		return nil, model.NewAppError("SqlTeamStore.GetTeamsByUserId", "store.sql_team.get_all.app_error", nil, err.Error(), http.StatusInternalServerError)
	}

	return teams, nil
}

func (s SqlTeamStore) GetAllPrivateTeamListing() ([]*model.Team, *model.AppError) {
	query := "SELECT * FROM Teams WHERE AllowOpenInvite = 0 ORDER BY DisplayName"

	if s.DriverName() == model.DATABASE_DRIVER_POSTGRES {
		query = "SELECT * FROM Teams WHERE AllowOpenInvite = false ORDER BY DisplayName"
	}

	var data []*model.Team
	if _, err := s.GetReplica().Select(&data, query); err != nil {
		return nil, model.NewAppError("SqlTeamStore.GetAllPrivateTeamListing", "store.sql_team.get_all_private_team_listing.app_error", nil, err.Error(), http.StatusInternalServerError)
	}

	return data, nil
}

func (s SqlTeamStore) GetAllPublicTeamPageListing(offset int, limit int) ([]*model.Team, *model.AppError) {
	query := "SELECT * FROM Teams WHERE AllowOpenInvite = 1 ORDER BY DisplayName LIMIT :Limit OFFSET :Offset"

	if s.DriverName() == model.DATABASE_DRIVER_POSTGRES {
		query = "SELECT * FROM Teams WHERE AllowOpenInvite = true ORDER BY DisplayName LIMIT :Limit OFFSET :Offset"
	}

	var data []*model.Team
	if _, err := s.GetReplica().Select(&data, query, map[string]interface{}{"Offset": offset, "Limit": limit}); err != nil {
		return nil, model.NewAppError("SqlTeamStore.GetAllPrivateTeamListing", "store.sql_team.get_all_private_team_listing.app_error", nil, err.Error(), http.StatusInternalServerError)
	}

	return data, nil
}

func (s SqlTeamStore) GetAllPrivateTeamPageListing(offset int, limit int) ([]*model.Team, *model.AppError) {
	query := "SELECT * FROM Teams WHERE AllowOpenInvite = 0 ORDER BY DisplayName LIMIT :Limit OFFSET :Offset"

	if s.DriverName() == model.DATABASE_DRIVER_POSTGRES {
		query = "SELECT * FROM Teams WHERE AllowOpenInvite = false ORDER BY DisplayName LIMIT :Limit OFFSET :Offset"
	}

	var data []*model.Team
	if _, err := s.GetReplica().Select(&data, query, map[string]interface{}{"Offset": offset, "Limit": limit}); err != nil {
		return nil, model.NewAppError("SqlTeamStore.GetAllPrivateTeamListing", "store.sql_team.get_all_private_team_listing.app_error", nil, err.Error(), http.StatusInternalServerError)
	}

	return data, nil
}

func (s SqlTeamStore) GetAllTeamListing() ([]*model.Team, *model.AppError) {
	query := "SELECT * FROM Teams WHERE AllowOpenInvite = 1 ORDER BY DisplayName"

	if s.DriverName() == model.DATABASE_DRIVER_POSTGRES {
		query = "SELECT * FROM Teams WHERE AllowOpenInvite = true ORDER BY DisplayName"
	}

	var data []*model.Team
	if _, err := s.GetReplica().Select(&data, query); err != nil {
		return nil, model.NewAppError("SqlTeamStore.GetAllTeamListing", "store.sql_team.get_all_team_listing.app_error", nil, err.Error(), http.StatusInternalServerError)
	}

	return data, nil
}

func (s SqlTeamStore) GetAllTeamPageListing(offset int, limit int) ([]*model.Team, *model.AppError) {
	query := "SELECT * FROM Teams WHERE AllowOpenInvite = 1 ORDER BY DisplayName LIMIT :Limit OFFSET :Offset"

	if s.DriverName() == model.DATABASE_DRIVER_POSTGRES {
		query = "SELECT * FROM Teams WHERE AllowOpenInvite = true ORDER BY DisplayName LIMIT :Limit OFFSET :Offset"
	}

	var teams []*model.Team
	if _, err := s.GetReplica().Select(&teams, query, map[string]interface{}{"Offset": offset, "Limit": limit}); err != nil {
		return nil, model.NewAppError("SqlTeamStore.GetAllTeamListing", "store.sql_team.get_all_team_listing.app_error", nil, err.Error(), http.StatusInternalServerError)
	}

	return teams, nil
}

func (s SqlTeamStore) PermanentDelete(teamId string) *model.AppError {
	if _, err := s.GetMaster().Exec("DELETE FROM Teams WHERE Id = :TeamId", map[string]interface{}{"TeamId": teamId}); err != nil {
		return model.NewAppError("SqlTeamStore.Delete", "store.sql_team.permanent_delete.app_error", nil, "teamId="+teamId+", "+err.Error(), http.StatusInternalServerError)
	}
	return nil
}

func (s SqlTeamStore) AnalyticsPublicTeamCount() (int64, *model.AppError) {

	c, err := s.GetReplica().SelectInt("SELECT COUNT(*) FROM Teams WHERE DeleteAt = 0 AND AllowOpenInvite = 1", map[string]interface{}{})

	if s.DriverName() == model.DATABASE_DRIVER_POSTGRES {
		c, err = s.GetReplica().SelectInt("SELECT COUNT(*) FROM Teams WHERE DeleteAt = 0 AND AllowOpenInvite = true", map[string]interface{}{})
	}

	if err != nil {
		return int64(0), model.NewAppError("SqlTeamStore.AnalyticsPublicTeamCount", "store.sql_team.analytics_public_team_count.app_error", nil, err.Error(), http.StatusInternalServerError)
	}

	return c, nil
}

func (s SqlTeamStore) AnalyticsPrivateTeamCount() (int64, *model.AppError) {
	c, err := s.GetReplica().SelectInt("SELECT COUNT(*) FROM Teams WHERE DeleteAt = 0 AND AllowOpenInvite = 0", map[string]interface{}{})

	if s.DriverName() == model.DATABASE_DRIVER_POSTGRES {
		c, err = s.GetReplica().SelectInt("SELECT COUNT(*) FROM Teams WHERE DeleteAt = 0 AND AllowOpenInvite = false", map[string]interface{}{})
	}

	if err != nil {
		return int64(0), model.NewAppError("SqlTeamStore.AnalyticsPrivateTeamCount", "store.sql_team.analytics_private_team_count.app_error", nil, err.Error(), http.StatusInternalServerError)
	}

	return c, nil
}

func (s SqlTeamStore) AnalyticsTeamCount(includeDeleted bool) (int64, *model.AppError) {
	query := s.getQueryBuilder().Select("COUNT(*) FROM Teams")
	if !includeDeleted {
		query = query.Where(sq.Eq{"DeleteAt": 0})
	}

	queryString, args, err := query.ToSql()
	if err != nil {
		return 0, model.NewAppError("SqlTeamStore.AnalyticsTeamCount", "store.sql_team.analytics_team_count.app_error", nil, err.Error(), http.StatusInternalServerError)
	}

	c, err := s.GetReplica().SelectInt(queryString, args...)

	if err != nil {
		return int64(0), model.NewAppError("SqlTeamStore.AnalyticsTeamCount", "store.sql_team.analytics_team_count.app_error", nil, err.Error(), http.StatusInternalServerError)
	}

	return c, nil
}

func (s SqlTeamStore) getTeamMembersWithSchemeSelectQuery() sq.SelectBuilder {
	return s.getQueryBuilder().
		Select(
			"TeamMembers.*",
			"TeamScheme.DefaultTeamGuestRole TeamSchemeDefaultGuestRole",
			"TeamScheme.DefaultTeamUserRole TeamSchemeDefaultUserRole",
			"TeamScheme.DefaultTeamAdminRole TeamSchemeDefaultAdminRole",
		).
		From("TeamMembers").
		LeftJoin("Teams ON TeamMembers.TeamId = Teams.Id").
		LeftJoin("Schemes TeamScheme ON Teams.SchemeId = TeamScheme.Id")
}

// SaveMember adds a team member using the team Id of the member
// if the member does not already exist in the database and if the number
// of existing team members are less than the maximum allowed users per team.
// It returns the team member added if the operation is successful.
func (s SqlTeamStore) SaveMember(member *model.TeamMember, maxUsersPerTeam int) (*model.TeamMember, *model.AppError) {
	defer s.InvalidateAllTeamIdsForUser(member.UserId)
	if err := member.IsValid(); err != nil {
		return nil, err
	}
	dbMember := NewTeamMemberFromModel(member)

	if maxUsersPerTeam >= 0 {
		count, err := s.GetMaster().SelectInt(
			`SELECT
					COUNT(0)
				FROM
					TeamMembers
				INNER JOIN
					Users
				ON
					TeamMembers.UserId = Users.Id
				WHERE
					TeamId = :TeamId
					AND TeamMembers.DeleteAt = 0
					AND Users.DeleteAt = 0`, map[string]interface{}{"TeamId": member.TeamId})

		if err != nil {
			return nil, model.NewAppError("SqlUserStore.Save", "store.sql_user.save.member_count.app_error", nil, "teamId="+member.TeamId+", "+err.Error(), http.StatusInternalServerError)
		}

		if count >= int64(maxUsersPerTeam) {
			return nil, model.NewAppError("SqlUserStore.Save", "store.sql_user.save.max_accounts.app_error", nil, "teamId="+member.TeamId, http.StatusBadRequest)
		}
	}

	if err := s.GetMaster().Insert(dbMember); err != nil {
		if IsUniqueConstraintError(err, []string{"TeamId", "teammembers_pkey", "PRIMARY"}) {
			return nil, model.NewAppError("SqlTeamStore.SaveMember", TEAM_MEMBER_EXISTS_ERROR, nil, "team_id="+member.TeamId+", user_id="+member.UserId+", "+err.Error(), http.StatusBadRequest)
		}
		return nil, model.NewAppError("SqlTeamStore.SaveMember", "store.sql_team.save_member.save.app_error", nil, "team_id="+member.TeamId+", user_id="+member.UserId+", "+err.Error(), http.StatusInternalServerError)
	}

	query := s.getTeamMembersWithSchemeSelectQuery().
		Where(sq.Eq{"TeamMembers.TeamId": dbMember.TeamId}).
		Where(sq.Eq{"TeamMembers.UserId": dbMember.UserId})

	queryString, args, err := query.ToSql()
	if err != nil {
		return nil, model.NewAppError("SqlTeamStore.SaveMember", "store.sql_team.get_member.app_error", nil, err.Error(), http.StatusInternalServerError)
	}

	var retrievedMember teamMemberWithSchemeRoles
	if err := s.GetMaster().SelectOne(&retrievedMember, queryString, args...); err != nil {
		if err == sql.ErrNoRows {
			return nil, model.NewAppError("SqlTeamStore.SaveMember", "store.sql_team.get_member.missing.app_error", nil, "team_id="+dbMember.TeamId+"user_id="+dbMember.UserId+","+err.Error(), http.StatusNotFound)
		}
		return nil, model.NewAppError("SqlTeamStore.SaveMember", "store.sql_team.get_member.app_error", nil, "team_id="+dbMember.TeamId+"user_id="+dbMember.UserId+","+err.Error(), http.StatusInternalServerError)
	}

	return retrievedMember.ToModel(), nil
}

// UpdateMember updates the team member if the team member exists in the database.
// It returns the updated team member if the operation is successful.
func (s SqlTeamStore) UpdateMember(member *model.TeamMember) (*model.TeamMember, *model.AppError) {
	member.PreUpdate()

	if err := member.IsValid(); err != nil {
		return nil, err
	}

	if _, err := s.GetMaster().Update(NewTeamMemberFromModel(member)); err != nil {
		return nil, model.NewAppError("SqlTeamStore.UpdateMember", "store.sql_team.save_member.save.app_error", nil, err.Error(), http.StatusInternalServerError)
	}

	query := s.getTeamMembersWithSchemeSelectQuery().
		Where(sq.Eq{"TeamMembers.TeamId": member.TeamId}).
		Where(sq.Eq{"TeamMembers.UserId": member.UserId})

	queryString, args, err := query.ToSql()
	if err != nil {
		return nil, model.NewAppError("SqlTeamStore.UpdateMember", "store.sql_team.get_member.app_error", nil, err.Error(), http.StatusInternalServerError)
	}

	var retrievedMember teamMemberWithSchemeRoles
	if err := s.GetMaster().SelectOne(&retrievedMember, queryString, args...); err != nil {
		if err == sql.ErrNoRows {
			return nil, model.NewAppError("SqlTeamStore.UpdateMember", "store.sql_team.get_member.missing.app_error", nil, "team_id="+member.TeamId+"user_id="+member.UserId+","+err.Error(), http.StatusNotFound)
		}
		return nil, model.NewAppError("SqlTeamStore.UpdateMember", "store.sql_team.get_member.app_error", nil, "team_id="+member.TeamId+"user_id="+member.UserId+","+err.Error(), http.StatusInternalServerError)
	}

	return retrievedMember.ToModel(), nil
}

func (s SqlTeamStore) GetMember(teamId string, userId string) (*model.TeamMember, *model.AppError) {
	query := s.getTeamMembersWithSchemeSelectQuery().
		Where(sq.Eq{"TeamMembers.TeamId": teamId}).
		Where(sq.Eq{"TeamMembers.UserId": userId})

	queryString, args, err := query.ToSql()
	if err != nil {
		return nil, model.NewAppError("SqlTeamStore.GetMember", "store.sql_team.get_member.app_error", nil, err.Error(), http.StatusInternalServerError)
	}

	var dbMember teamMemberWithSchemeRoles
	err = s.GetReplica().SelectOne(&dbMember, queryString, args...)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, model.NewAppError("SqlTeamStore.GetMember", "store.sql_team.get_member.missing.app_error", nil, "teamId="+teamId+" userId="+userId+" "+err.Error(), http.StatusNotFound)
		}
		return nil, model.NewAppError("SqlTeamStore.GetMember", "store.sql_team.get_member.app_error", nil, "teamId="+teamId+" userId="+userId+" "+err.Error(), http.StatusInternalServerError)
	}

	return dbMember.ToModel(), nil
}

func (s SqlTeamStore) GetMembers(teamId string, offset int, limit int, restrictions *model.ViewUsersRestrictions) ([]*model.TeamMember, *model.AppError) {
	query := s.getTeamMembersWithSchemeSelectQuery().
		Where(sq.Eq{"TeamMembers.TeamId": teamId}).
		Where(sq.Eq{"TeamMembers.DeleteAt": 0}).
		OrderBy("UserId").
		Limit(uint64(limit)).
		Offset(uint64(offset))

	query = applyTeamMemberViewRestrictionsFilter(query, teamId, restrictions)

	queryString, args, err := query.ToSql()
	if err != nil {
		return nil, model.NewAppError("SqlTeamStore.GetMembers", "store.sql_team.get_members.app_error", nil, err.Error(), http.StatusInternalServerError)
	}

	var dbMembers teamMemberWithSchemeRolesList
	_, err = s.GetReplica().Select(&dbMembers, queryString, args...)
	if err != nil {
		return nil, model.NewAppError("SqlTeamStore.GetMembers", "store.sql_team.get_members.app_error", nil, "teamId="+teamId+" "+err.Error(), http.StatusInternalServerError)
	}

	return dbMembers.ToModel(), nil
}

func (s SqlTeamStore) GetTotalMemberCount(teamId string, restrictions *model.ViewUsersRestrictions) (int64, *model.AppError) {
	query := s.getQueryBuilder().
		Select("count(DISTINCT TeamMembers.UserId)").
		From("TeamMembers, Users").
		Where("TeamMembers.DeleteAt = 0").
		Where("TeamMembers.UserId = Users.Id").
		Where(sq.Eq{"TeamMembers.TeamId": teamId})

	query = applyTeamMemberViewRestrictionsFilterForStats(query, teamId, restrictions)
	queryString, args, err := query.ToSql()
	if err != nil {
		return int64(0), model.NewAppError("SqlTeamStore.GetTotalMemberCount", "store.sql_team.get_member_count.app_error", nil, err.Error(), http.StatusInternalServerError)
	}

	count, err := s.GetReplica().SelectInt(queryString, args...)
	if err != nil {
		return int64(0), model.NewAppError("SqlTeamStore.GetTotalMemberCount", "store.sql_team.get_member_count.app_error", nil, "teamId="+teamId+" "+err.Error(), http.StatusInternalServerError)
	}
	return count, nil
}

func (s SqlTeamStore) GetActiveMemberCount(teamId string, restrictions *model.ViewUsersRestrictions) (int64, *model.AppError) {
	query := s.getQueryBuilder().
		Select("count(DISTINCT TeamMembers.UserId)").
		From("TeamMembers, Users").
		Where("TeamMembers.DeleteAt = 0").
		Where("TeamMembers.UserId = Users.Id").
		Where("Users.DeleteAt = 0").
		Where(sq.Eq{"TeamMembers.TeamId": teamId})

	query = applyTeamMemberViewRestrictionsFilterForStats(query, teamId, restrictions)
	queryString, args, err := query.ToSql()
	if err != nil {
		return 0, model.NewAppError("SqlTeamStore.GetActiveMemberCount", "store.sql_team.get_active_member_count.app_error", nil, err.Error(), http.StatusInternalServerError)
	}

	count, err := s.GetReplica().SelectInt(queryString, args...)
	if err != nil {
		return 0, model.NewAppError("SqlTeamStore.GetActiveMemberCount", "store.sql_team.get_active_member_count.app_error", nil, "teamId="+teamId+" "+err.Error(), http.StatusInternalServerError)
	}

	return count, nil
}

func (s SqlTeamStore) GetMembersByIds(teamId string, userIds []string, restrictions *model.ViewUsersRestrictions) ([]*model.TeamMember, *model.AppError) {
	if len(userIds) == 0 {
		return nil, model.NewAppError("SqlTeamStore.GetMembersByIds", "store.sql_team.get_members_by_ids.app_error", nil, "Invalid list of user ids", http.StatusInternalServerError)
	}

	query := s.getTeamMembersWithSchemeSelectQuery().
		Where(sq.Eq{"TeamMembers.TeamId": teamId}).
		Where(sq.Eq{"TeamMembers.UserId": userIds}).
		Where(sq.Eq{"TeamMembers.DeleteAt": 0})

	query = applyTeamMemberViewRestrictionsFilter(query, teamId, restrictions)

	queryString, args, err := query.ToSql()
	if err != nil {
		return nil, model.NewAppError("SqlTeamStore.GetMembersByIds", "store.sql_team.get_members_by_ids.app_error", nil, err.Error(), http.StatusInternalServerError)
	}

	var dbMembers teamMemberWithSchemeRolesList
	if _, err := s.GetReplica().Select(&dbMembers, queryString, args...); err != nil {
		return nil, model.NewAppError("SqlTeamStore.GetMembersByIds", "store.sql_team.get_members_by_ids.app_error", nil, "teamId="+teamId+" "+err.Error(), http.StatusInternalServerError)
	}
	return dbMembers.ToModel(), nil
}

func (s SqlTeamStore) GetTeamsForUser(userId string) ([]*model.TeamMember, *model.AppError) {
	query := s.getTeamMembersWithSchemeSelectQuery().
		Where(sq.Eq{"TeamMembers.UserId": userId})

	queryString, args, err := query.ToSql()
	if err != nil {
		return nil, model.NewAppError("SqlTeamStore.GetMembers", "store.sql_team.get_members.app_error", nil, err.Error(), http.StatusInternalServerError)
	}

	var dbMembers teamMemberWithSchemeRolesList
	_, err = s.GetReplica().Select(&dbMembers, queryString, args...)
	if err != nil {
		return nil, model.NewAppError("SqlTeamStore.GetMembers", "store.sql_team.get_members.app_error", nil, "userId="+userId+" "+err.Error(), http.StatusInternalServerError)
	}

	return dbMembers.ToModel(), nil
}

func (s SqlTeamStore) GetTeamsForUserWithPagination(userId string, page, perPage int) ([]*model.TeamMember, *model.AppError) {
	query := s.getTeamMembersWithSchemeSelectQuery().
		Where(sq.Eq{"TeamMembers.UserId": userId}).
		Limit(uint64(perPage)).
		Offset(uint64(page * perPage))

	queryString, args, err := query.ToSql()
	if err != nil {
		return nil, model.NewAppError("SqlTeamStore.GetTeamsForUserWithPagination", "store.sql_team.get_members.app_error", nil, err.Error(), http.StatusInternalServerError)
	}

	var dbMembers teamMemberWithSchemeRolesList
	_, err = s.GetReplica().Select(&dbMembers, queryString, args...)
	if err != nil {
		return nil, model.NewAppError("SqlTeamStore.GetTeamsForUserWithPagination", "store.sql_team.get_members.app_error", nil, "userId="+userId+" "+err.Error(), http.StatusInternalServerError)
	}

	return dbMembers.ToModel(), nil
}

func (s SqlTeamStore) GetChannelUnreadsForAllTeams(excludeTeamId, userId string) ([]*model.ChannelUnread, *model.AppError) {
	var data []*model.ChannelUnread
	_, err := s.GetReplica().Select(&data,
		`SELECT
			Channels.TeamId TeamId, Channels.Id ChannelId, (Channels.TotalMsgCount - ChannelMembers.MsgCount) MsgCount, ChannelMembers.MentionCount MentionCount, ChannelMembers.NotifyProps NotifyProps
		FROM
			Channels, ChannelMembers
		WHERE
			Id = ChannelId
			AND UserId = :UserId
			AND DeleteAt = 0
			AND TeamId != :TeamId`,
		map[string]interface{}{"UserId": userId, "TeamId": excludeTeamId})

	if err != nil {
		return nil, model.NewAppError("SqlTeamStore.GetChannelUnreadsForAllTeams", "store.sql_team.get_unread.app_error", nil, "userId="+userId+" "+err.Error(), http.StatusInternalServerError)
	}

	return data, nil
}

func (s SqlTeamStore) GetChannelUnreadsForTeam(teamId, userId string) ([]*model.ChannelUnread, *model.AppError) {
	query := `
		SELECT
			Channels.TeamId TeamId, Channels.Id ChannelId, (Channels.TotalMsgCount - ChannelMembers.MsgCount) MsgCount, ChannelMembers.MentionCount MentionCount, ChannelMembers.NotifyProps NotifyProps
		FROM
			Channels, ChannelMembers
		WHERE
			Id = ChannelId
			AND UserId = :UserId
			AND TeamId = :TeamId
			AND DeleteAt = 0`

	var channels []*model.ChannelUnread
	_, err := s.GetReplica().Select(&channels, query, map[string]interface{}{"TeamId": teamId, "UserId": userId})

	if err != nil {
		return nil, model.NewAppError("SqlTeamStore.GetChannelUnreadsForTeam", "store.sql_team.get_unread.app_error", nil, "teamId="+teamId+" "+err.Error(), http.StatusInternalServerError)
	}
	return channels, nil
}

func (s SqlTeamStore) RemoveMember(teamId string, userId string) *model.AppError {
	_, err := s.GetMaster().Exec("DELETE FROM TeamMembers WHERE TeamId = :TeamId AND UserId = :UserId", map[string]interface{}{"TeamId": teamId, "UserId": userId})
	if err != nil {
		return model.NewAppError("SqlTeamStore.RemoveMember", "store.sql_team.remove_member.app_error", nil, "team_id="+teamId+", user_id="+userId+", "+err.Error(), http.StatusInternalServerError)
	}
	return nil
}

func (s SqlTeamStore) RemoveAllMembersByTeam(teamId string) *model.AppError {
	_, err := s.GetMaster().Exec("DELETE FROM TeamMembers WHERE TeamId = :TeamId", map[string]interface{}{"TeamId": teamId})
	if err != nil {
		return model.NewAppError("SqlTeamStore.RemoveMember", "store.sql_team.remove_member.app_error", nil, "team_id="+teamId+", "+err.Error(), http.StatusInternalServerError)
	}
	return nil
}

func (s SqlTeamStore) RemoveAllMembersByUser(userId string) *model.AppError {
	_, err := s.GetMaster().Exec("DELETE FROM TeamMembers WHERE UserId = :UserId", map[string]interface{}{"UserId": userId})
	if err != nil {
		return model.NewAppError("SqlTeamStore.RemoveMember", "store.sql_team.remove_member.app_error", nil, "user_id="+userId+", "+err.Error(), http.StatusInternalServerError)
	}
	return nil
}

func (s SqlTeamStore) UpdateLastTeamIconUpdate(teamId string, curTime int64) *model.AppError {
	if _, err := s.GetMaster().Exec("UPDATE Teams SET LastTeamIconUpdate = :Time, UpdateAt = :Time WHERE Id = :teamId", map[string]interface{}{"Time": curTime, "teamId": teamId}); err != nil {
		return model.NewAppError("SqlTeamStore.UpdateLastTeamIconUpdate", "store.sql_team.update_last_team_icon_update.app_error", nil, "team_id="+teamId, http.StatusInternalServerError)
	}
	return nil
}

func (s SqlTeamStore) GetTeamsByScheme(schemeId string, offset int, limit int) ([]*model.Team, *model.AppError) {
	var teams []*model.Team
	_, err := s.GetReplica().Select(&teams, "SELECT * FROM Teams WHERE SchemeId = :SchemeId ORDER BY DisplayName LIMIT :Limit OFFSET :Offset", map[string]interface{}{"SchemeId": schemeId, "Offset": offset, "Limit": limit})
	if err != nil {
		return nil, model.NewAppError("SqlTeamStore.GetTeamsByScheme", "store.sql_team.get_by_scheme.app_error", nil, "schemeId="+schemeId+" "+err.Error(), http.StatusInternalServerError)
	}
	return teams, nil
}

// This function does the Advanced Permissions Phase 2 migration for TeamMember objects. It performs the migration
// in batches as a single transaction per batch to ensure consistency but to also minimise execution time to avoid
// causing unnecessary table locks. **THIS FUNCTION SHOULD NOT BE USED FOR ANY OTHER PURPOSE.** Executing this function
// *after* the new Schemes functionality has been used on an installation will have unintended consequences.
func (s SqlTeamStore) MigrateTeamMembers(fromTeamId string, fromUserId string) (map[string]string, *model.AppError) {
	var transaction *gorp.Transaction
	var err error

	if transaction, err = s.GetMaster().Begin(); err != nil {
		return nil, model.NewAppError("SqlTeamStore.MigrateTeamMembers", "store.sql_team.migrate_team_members.open_transaction.app_error", nil, err.Error(), http.StatusInternalServerError)
	}
	defer finalizeTransaction(transaction)

	var teamMembers []teamMember
	if _, err := transaction.Select(&teamMembers, "SELECT * from TeamMembers WHERE (TeamId, UserId) > (:FromTeamId, :FromUserId) ORDER BY TeamId, UserId LIMIT 100", map[string]interface{}{"FromTeamId": fromTeamId, "FromUserId": fromUserId}); err != nil {
		return nil, model.NewAppError("SqlTeamStore.MigrateTeamMembers", "store.sql_team.migrate_team_members.select.app_error", nil, err.Error(), http.StatusInternalServerError)
	}

	if len(teamMembers) == 0 {
		// No more team members in query result means that the migration has finished.
		return nil, nil
	}

	for i := range teamMembers {
		member := teamMembers[i]
		roles := strings.Fields(member.Roles)
		var newRoles []string
		if !member.SchemeAdmin.Valid {
			member.SchemeAdmin = sql.NullBool{Bool: false, Valid: true}
		}
		if !member.SchemeUser.Valid {
			member.SchemeUser = sql.NullBool{Bool: false, Valid: true}
		}
		if !member.SchemeGuest.Valid {
			member.SchemeGuest = sql.NullBool{Bool: false, Valid: true}
		}
		for _, role := range roles {
			if role == model.TEAM_ADMIN_ROLE_ID {
				member.SchemeAdmin = sql.NullBool{Bool: true, Valid: true}
			} else if role == model.TEAM_USER_ROLE_ID {
				member.SchemeUser = sql.NullBool{Bool: true, Valid: true}
			} else if role == model.TEAM_GUEST_ROLE_ID {
				member.SchemeGuest = sql.NullBool{Bool: true, Valid: true}
			} else {
				newRoles = append(newRoles, role)
			}
		}
		member.Roles = strings.Join(newRoles, " ")

		if _, err := transaction.Update(&member); err != nil {
			return nil, model.NewAppError("SqlTeamStore.MigrateTeamMembers", "store.sql_team.migrate_team_members.update.app_error", nil, err.Error(), http.StatusInternalServerError)
		}

	}

	if err := transaction.Commit(); err != nil {
		return nil, model.NewAppError("SqlTeamStore.MigrateTeamMembers", "store.sql_team.migrate_team_members.commit_transaction.app_error", nil, err.Error(), http.StatusInternalServerError)
	}

	data := make(map[string]string)
	data["TeamId"] = teamMembers[len(teamMembers)-1].TeamId
	data["UserId"] = teamMembers[len(teamMembers)-1].UserId

	return data, nil
}

func (s SqlTeamStore) ResetAllTeamSchemes() *model.AppError {
	if _, err := s.GetMaster().Exec("UPDATE Teams SET SchemeId=''"); err != nil {
		return model.NewAppError("SqlTeamStore.ResetAllTeamSchemes", "store.sql_team.reset_all_team_schemes.app_error", nil, err.Error(), http.StatusInternalServerError)
	}
	return nil
}

func (s SqlTeamStore) ClearCaches() {}

func (s SqlTeamStore) InvalidateAllTeamIdsForUser(userId string) {}

func (s SqlTeamStore) ClearAllCustomRoleAssignments() *model.AppError {

	builtInRoles := model.MakeDefaultRoles()
	lastUserId := strings.Repeat("0", 26)
	lastTeamId := strings.Repeat("0", 26)

	for {
		var transaction *gorp.Transaction
		var err error

		if transaction, err = s.GetMaster().Begin(); err != nil {
			return model.NewAppError("SqlTeamStore.ClearAllCustomRoleAssignments", "store.sql_team.clear_all_custom_role_assignments.open_transaction.app_error", nil, err.Error(), http.StatusInternalServerError)
		}
		defer finalizeTransaction(transaction)

		var teamMembers []*teamMember
		if _, err := transaction.Select(&teamMembers, "SELECT * from TeamMembers WHERE (TeamId, UserId) > (:TeamId, :UserId) ORDER BY TeamId, UserId LIMIT 1000", map[string]interface{}{"TeamId": lastTeamId, "UserId": lastUserId}); err != nil {
			return model.NewAppError("SqlTeamStore.ClearAllCustomRoleAssignments", "store.sql_team.clear_all_custom_role_assignments.select.app_error", nil, err.Error(), http.StatusInternalServerError)
		}

		if len(teamMembers) == 0 {
			break
		}

		for _, member := range teamMembers {
			lastUserId = member.UserId
			lastTeamId = member.TeamId

			var newRoles []string

			for _, role := range strings.Fields(member.Roles) {
				for name := range builtInRoles {
					if name == role {
						newRoles = append(newRoles, role)
						break
					}
				}
			}

			newRolesString := strings.Join(newRoles, " ")
			if newRolesString != member.Roles {
				if _, err := transaction.Exec("UPDATE TeamMembers SET Roles = :Roles WHERE UserId = :UserId AND TeamId = :TeamId", map[string]interface{}{"Roles": newRolesString, "TeamId": member.TeamId, "UserId": member.UserId}); err != nil {
					return model.NewAppError("SqlTeamStore.ClearAllCustomRoleAssignments", "store.sql_team.clear_all_custom_role_assignments.update.app_error", nil, err.Error(), http.StatusInternalServerError)
				}
			}
		}

		if err := transaction.Commit(); err != nil {
			return model.NewAppError("SqlTeamStore.ClearAllCustomRoleAssignments", "store.sql_team.clear_all_custom_role_assignments.commit_transaction.app_error", nil, err.Error(), http.StatusInternalServerError)
		}
	}
	return nil
}

func (s SqlTeamStore) AnalyticsGetTeamCountForScheme(schemeId string) (int64, *model.AppError) {
	count, err := s.GetReplica().SelectInt("SELECT count(*) FROM Teams WHERE SchemeId = :SchemeId AND DeleteAt = 0", map[string]interface{}{"SchemeId": schemeId})
	if err != nil {
		return 0, model.NewAppError("SqlTeamStore.AnalyticsGetTeamCountForScheme", "store.sql_team.analytics_get_team_count_for_scheme.app_error", nil, "schemeId="+schemeId+" "+err.Error(), http.StatusInternalServerError)
	}

	return count, nil
}

func (s SqlTeamStore) GetAllForExportAfter(limit int, afterId string) ([]*model.TeamForExport, *model.AppError) {
	var data []*model.TeamForExport
	if _, err := s.GetReplica().Select(&data, `
		SELECT
			Teams.*,
			Schemes.Name as SchemeName
		FROM
			Teams
		LEFT JOIN
			Schemes ON Teams.SchemeId = Schemes.Id
		WHERE
			Teams.Id > :AfterId
		ORDER BY
			Id
		LIMIT
			:Limit`,
		map[string]interface{}{"AfterId": afterId, "Limit": limit}); err != nil {
		return nil, model.NewAppError("SqlTeamStore.GetAllTeams", "store.sql_team.get_all.app_error", nil, err.Error(), http.StatusInternalServerError)
	}

	return data, nil
}

// GetUserTeamIds get the team ids to which the user belongs to. allowFromCache parameter does not have any effect in this Store
func (s SqlTeamStore) GetUserTeamIds(userID string, allowFromCache bool) ([]string, *model.AppError) {
	var teamIds []string
	_, err := s.GetReplica().Select(&teamIds,
		`SELECT
			TeamId
		FROM
			TeamMembers
		INNER JOIN
			Teams ON TeamMembers.TeamId = Teams.Id
		WHERE
			TeamMembers.UserId = :UserId
			AND TeamMembers.DeleteAt = 0
			AND Teams.DeleteAt = 0`,
		map[string]interface{}{"UserId": userID})
	if err != nil {
		return []string{}, model.NewAppError("SqlTeamStore.GetUserTeamIds", "store.sql_team.get_user_team_ids.app_error", nil, "userID="+userID+" "+err.Error(), http.StatusInternalServerError)
	}

	return teamIds, nil
}

func (s SqlTeamStore) GetTeamMembersForExport(userId string) ([]*model.TeamMemberForExport, *model.AppError) {
	var members []*model.TeamMemberForExport
	_, err := s.GetReplica().Select(&members, `
		SELECT
			TeamMembers.TeamId,
			TeamMembers.UserId,
			TeamMembers.Roles,
			TeamMembers.DeleteAt,
			(TeamMembers.SchemeGuest IS NOT NULL AND TeamMembers.SchemeGuest) as SchemeGuest,
			TeamMembers.SchemeUser,
			TeamMembers.SchemeAdmin,
			Teams.Name as TeamName
		FROM
			TeamMembers
		INNER JOIN
			Teams ON TeamMembers.TeamId = Teams.Id
		WHERE
			TeamMembers.UserId = :UserId
			AND Teams.DeleteAt = 0`,
		map[string]interface{}{"UserId": userId})
	if err != nil {
		return nil, model.NewAppError("SqlTeamStore.GetTeamMembersForExport", "store.sql_team.get_members.app_error", nil, "userId="+userId+" "+err.Error(), http.StatusInternalServerError)
	}
	return members, nil
}

func (s SqlTeamStore) UserBelongsToTeams(userId string, teamIds []string) (bool, *model.AppError) {
	idQuery := sq.Eq{
		"UserId":   userId,
		"TeamId":   teamIds,
		"DeleteAt": 0,
	}

	query, params, err := s.getQueryBuilder().Select("Count(*)").From("TeamMembers").Where(idQuery).ToSql()
	if err != nil {
		return false, model.NewAppError("SqlTeamStore.UserBelongsToTeams", "store.sql_team.user_belongs_to_teams.app_error", nil, err.Error(), http.StatusInternalServerError)
	}

	c, err := s.GetReplica().SelectInt(query, params...)
	if err != nil {
		return false, model.NewAppError("SqlTeamStore.UserBelongsToTeams", "store.sql_team.user_belongs_to_teams.app_error", nil, err.Error(), http.StatusInternalServerError)
	}

	return c > 0, nil
}

func (s SqlTeamStore) UpdateMembersRole(teamID string, userIDs []string) *model.AppError {
	sql := fmt.Sprintf(`
		UPDATE
			TeamMembers
		SET
			SchemeAdmin = CASE WHEN UserId IN ('%s') THEN
				TRUE
			ELSE
				FALSE
			END
		WHERE
			TeamId = :TeamId
			AND (SchemeGuest = false OR SchemeGuest IS NULL)
			AND DeleteAt = 0`, strings.Join(userIDs, "', '"))

	if _, err := s.GetMaster().Exec(sql, map[string]interface{}{"TeamId": teamID}); err != nil {
		return model.NewAppError("SqlTeamStore.UpdateMembersRole", "store.update_error", nil, err.Error(), http.StatusInternalServerError)
	}

	return nil
}

func applyTeamMemberViewRestrictionsFilter(query sq.SelectBuilder, teamId string, restrictions *model.ViewUsersRestrictions) sq.SelectBuilder {
	if restrictions == nil {
		return query
	}

	// If you have no access to teams or channels, return and empty result.
	if restrictions.Teams != nil && len(restrictions.Teams) == 0 && restrictions.Channels != nil && len(restrictions.Channels) == 0 {
		return query.Where("1 = 0")
	}

	teams := make([]interface{}, len(restrictions.Teams))
	for i, v := range restrictions.Teams {
		teams[i] = v
	}
	channels := make([]interface{}, len(restrictions.Channels))
	for i, v := range restrictions.Channels {
		channels[i] = v
	}

	resultQuery := query.Join("Users ru ON (TeamMembers.UserId = ru.Id)")
	if restrictions.Teams != nil && len(restrictions.Teams) > 0 {
		resultQuery = resultQuery.Join(fmt.Sprintf("TeamMembers rtm ON ( rtm.UserId = ru.Id AND rtm.DeleteAt = 0 AND rtm.TeamId IN (%s))", sq.Placeholders(len(teams))), teams...)
	}
	if restrictions.Channels != nil && len(restrictions.Channels) > 0 {
		resultQuery = resultQuery.Join(fmt.Sprintf("ChannelMembers rcm ON ( rcm.UserId = ru.Id AND rcm.ChannelId IN (%s))", sq.Placeholders(len(channels))), channels...)
	}

	return resultQuery.Distinct()
}

func applyTeamMemberViewRestrictionsFilterForStats(query sq.SelectBuilder, teamId string, restrictions *model.ViewUsersRestrictions) sq.SelectBuilder {
	if restrictions == nil {
		return query
	}

	// If you have no access to teams or channels, return and empty result.
	if restrictions.Teams != nil && len(restrictions.Teams) == 0 && restrictions.Channels != nil && len(restrictions.Channels) == 0 {
		return query.Where("1 = 0")
	}

	teams := make([]interface{}, len(restrictions.Teams))
	for i, v := range restrictions.Teams {
		teams[i] = v
	}
	channels := make([]interface{}, len(restrictions.Channels))
	for i, v := range restrictions.Channels {
		channels[i] = v
	}

	resultQuery := query
	if restrictions.Teams != nil && len(restrictions.Teams) > 0 {
		resultQuery = resultQuery.Join(fmt.Sprintf("TeamMembers rtm ON ( rtm.UserId = Users.Id AND rtm.DeleteAt = 0 AND rtm.TeamId IN (%s))", sq.Placeholders(len(teams))), teams...)
	}
	if restrictions.Channels != nil && len(restrictions.Channels) > 0 {
		resultQuery = resultQuery.Join(fmt.Sprintf("ChannelMembers rcm ON ( rcm.UserId = Users.Id AND rcm.ChannelId IN (%s))", sq.Placeholders(len(channels))), channels...)
	}

	return resultQuery
}

func (s SqlTeamStore) GroupSyncedTeamCount() (int64, *model.AppError) {
	query := s.getQueryBuilder().Select("COUNT(*)").From("Teams").Where(sq.Eq{"GroupConstrained": true, "DeleteAt": 0})

	sql, args, err := query.ToSql()
	if err != nil {
		return 0, model.NewAppError("SqlTeamStore.GroupSyncedTeamCount", "store.sql_group.app_error", nil, err.Error(), http.StatusInternalServerError)
	}

	count, err := s.GetReplica().SelectInt(sql, args...)
	if err != nil {
		return 0, model.NewAppError("SqlTeamStore.GroupSyncedTeamCount", "store.select_error", nil, err.Error(), http.StatusInternalServerError)
	}

	return count, nil
}
