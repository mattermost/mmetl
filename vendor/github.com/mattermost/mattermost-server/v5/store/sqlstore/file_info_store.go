// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package sqlstore

import (
	"database/sql"
	"fmt"
	"net/http"

	sq "github.com/Masterminds/squirrel"

	"github.com/mattermost/mattermost-server/v5/einterfaces"
	"github.com/mattermost/mattermost-server/v5/model"
	"github.com/mattermost/mattermost-server/v5/store"
)

type SqlFileInfoStore struct {
	SqlStore
	metrics einterfaces.MetricsInterface
}

func (fs SqlFileInfoStore) ClearCaches() {
}

func newSqlFileInfoStore(sqlStore SqlStore, metrics einterfaces.MetricsInterface) store.FileInfoStore {
	s := &SqlFileInfoStore{
		SqlStore: sqlStore,
		metrics:  metrics,
	}

	for _, db := range sqlStore.GetAllConns() {
		table := db.AddTableWithName(model.FileInfo{}, "FileInfo").SetKeys(false, "Id")
		table.ColMap("Id").SetMaxSize(26)
		table.ColMap("CreatorId").SetMaxSize(26)
		table.ColMap("PostId").SetMaxSize(26)
		table.ColMap("Path").SetMaxSize(512)
		table.ColMap("ThumbnailPath").SetMaxSize(512)
		table.ColMap("PreviewPath").SetMaxSize(512)
		table.ColMap("Name").SetMaxSize(256)
		table.ColMap("Extension").SetMaxSize(64)
		table.ColMap("MimeType").SetMaxSize(256)
	}

	return s
}

func (fs SqlFileInfoStore) createIndexesIfNotExists() {
	fs.CreateIndexIfNotExists("idx_fileinfo_update_at", "FileInfo", "UpdateAt")
	fs.CreateIndexIfNotExists("idx_fileinfo_create_at", "FileInfo", "CreateAt")
	fs.CreateIndexIfNotExists("idx_fileinfo_delete_at", "FileInfo", "DeleteAt")
	fs.CreateIndexIfNotExists("idx_fileinfo_postid_at", "FileInfo", "PostId")
}

func (fs SqlFileInfoStore) Save(info *model.FileInfo) (*model.FileInfo, *model.AppError) {
	info.PreSave()
	if err := info.IsValid(); err != nil {
		return nil, err
	}

	if err := fs.GetMaster().Insert(info); err != nil {
		return nil, model.NewAppError("SqlFileInfoStore.Save", "store.sql_file_info.save.app_error", nil, err.Error(), http.StatusInternalServerError)
	}
	return info, nil
}

func (fs SqlFileInfoStore) Get(id string) (*model.FileInfo, *model.AppError) {
	info := &model.FileInfo{}

	if err := fs.GetReplica().SelectOne(info,
		`SELECT
			*
		FROM
			FileInfo
		WHERE
			Id = :Id
			AND DeleteAt = 0`, map[string]interface{}{"Id": id}); err != nil {
		if err == sql.ErrNoRows {
			return nil, model.NewAppError("SqlFileInfoStore.Get", "store.sql_file_info.get.app_error", nil, "id="+id+", "+err.Error(), http.StatusNotFound)
		}
		return nil, model.NewAppError("SqlFileInfoStore.Get", "store.sql_file_info.get.app_error", nil, "id="+id+", "+err.Error(), http.StatusInternalServerError)
	}
	return info, nil
}

func (fs SqlFileInfoStore) GetWithOptions(page, perPage int, opt *model.GetFileInfosOptions) ([]*model.FileInfo, *model.AppError) {
	if perPage < 0 || page < 0 {
		return nil, model.NewAppError("SqlFileInfoStore.GetWithOptions",
			"store.sql_file_info.get_with_options.app_error", nil, fmt.Sprintf("page=%d and perPage=%d must be non-negative", page, perPage), http.StatusBadRequest)
	}
	if perPage == 0 {
		return nil, nil
	}

	if opt == nil {
		opt = &model.GetFileInfosOptions{}
	}

	query := fs.getQueryBuilder().
		Select("FileInfo.*").
		From("FileInfo")

	if len(opt.ChannelIds) > 0 {
		query = query.Join("Posts ON FileInfo.PostId = Posts.Id").
			Where(sq.Eq{"Posts.ChannelId": opt.ChannelIds})
	}

	if len(opt.UserIds) > 0 {
		query = query.Where(sq.Eq{"FileInfo.CreatorId": opt.UserIds})
	}

	if opt.Since > 0 {
		query = query.Where(sq.GtOrEq{"FileInfo.CreateAt": opt.Since})
	}

	if !opt.IncludeDeleted {
		query = query.Where("FileInfo.DeleteAt = 0")
	}

	if opt.SortBy == "" {
		opt.SortBy = model.FILEINFO_SORT_BY_CREATED
	}
	sortDirection := "ASC"
	if opt.SortDescending {
		sortDirection = "DESC"
	}

	switch opt.SortBy {
	case model.FILEINFO_SORT_BY_CREATED:
		query = query.OrderBy("FileInfo.CreateAt " + sortDirection)
	case model.FILEINFO_SORT_BY_SIZE:
		query = query.OrderBy("FileInfo.Size " + sortDirection)
	default:
		return nil, model.NewAppError("SqlFileInfoStore.GetWithOptions",
			"store.sql_file_info.get_with_options.app_error", nil, "invalid sort option", http.StatusBadRequest)
	}

	query = query.OrderBy("FileInfo.Id ASC") // secondary sort for sort stability

	query = query.Limit(uint64(perPage)).Offset(uint64(perPage * page))

	queryString, args, err := query.ToSql()
	if err != nil {
		return nil, model.NewAppError("SqlFileInfoStore.GetWithOptions",
			"store.sql.build_query.app_error", nil, err.Error(), http.StatusInternalServerError)
	}
	var infos []*model.FileInfo
	if _, err := fs.GetReplica().Select(&infos, queryString, args...); err != nil {
		return nil, model.NewAppError("SqlFileInfoStore.GetWithOptions",
			"store.sql_file_info.get_with_options.app_error", nil, err.Error(), http.StatusInternalServerError)
	}
	return infos, nil
}

func (fs SqlFileInfoStore) GetByPath(path string) (*model.FileInfo, *model.AppError) {
	info := &model.FileInfo{}

	if err := fs.GetReplica().SelectOne(info,
		`SELECT
				*
			FROM
				FileInfo
			WHERE
				Path = :Path
				AND DeleteAt = 0
			LIMIT 1`, map[string]interface{}{"Path": path}); err != nil {
		return nil, model.NewAppError("SqlFileInfoStore.GetByPath", "store.sql_file_info.get_by_path.app_error", nil, "path="+path+", "+err.Error(), http.StatusInternalServerError)
	}
	return info, nil
}

func (fs SqlFileInfoStore) InvalidateFileInfosForPostCache(postId string, deleted bool) {
}

func (fs SqlFileInfoStore) GetForPost(postId string, readFromMaster, includeDeleted, allowFromCache bool) ([]*model.FileInfo, *model.AppError) {
	var infos []*model.FileInfo

	dbmap := fs.GetReplica()

	if readFromMaster {
		dbmap = fs.GetMaster()
	}

	query := fs.getQueryBuilder().
		Select("*").
		From("FileInfo").
		Where(sq.Eq{"PostId": postId}).
		OrderBy("CreateAt")

	if !includeDeleted {
		query = query.Where("DeleteAt = 0")
	}

	queryString, args, err := query.ToSql()
	if err != nil {
		return nil, model.NewAppError("SqlFileInfoStore.GetForPost", "store.sql_file_info.get_for_post.app_error", nil, err.Error(), http.StatusInternalServerError)
	}

	if _, err := dbmap.Select(&infos, queryString, args...); err != nil {
		return nil, model.NewAppError("SqlFileInfoStore.GetForPost",
			"store.sql_file_info.get_for_post.app_error", nil, "post_id="+postId+", "+err.Error(), http.StatusInternalServerError)
	}
	return infos, nil
}

func (fs SqlFileInfoStore) GetForUser(userId string) ([]*model.FileInfo, *model.AppError) {
	var infos []*model.FileInfo

	dbmap := fs.GetReplica()

	if _, err := dbmap.Select(&infos,
		`SELECT
				*
			FROM
				FileInfo
			WHERE
				CreatorId = :CreatorId
				AND DeleteAt = 0
			ORDER BY
				CreateAt`, map[string]interface{}{"CreatorId": userId}); err != nil {
		return nil, model.NewAppError("SqlFileInfoStore.GetForUser",
			"store.sql_file_info.get_for_user_id.app_error", nil, "creator_id="+userId+", "+err.Error(), http.StatusInternalServerError)
	}
	return infos, nil
}

func (fs SqlFileInfoStore) AttachToPost(fileId, postId, creatorId string) *model.AppError {
	sqlResult, err := fs.GetMaster().Exec(`
		UPDATE
			FileInfo
		SET
			PostId = :PostId
		WHERE
			Id = :Id
			AND PostId = ''
			AND (CreatorId = :CreatorId OR CreatorId = 'nouser')
	`, map[string]interface{}{
		"PostId":    postId,
		"Id":        fileId,
		"CreatorId": creatorId,
	})
	if err != nil {
		return model.NewAppError("SqlFileInfoStore.AttachToPost",
			"store.sql_file_info.attach_to_post.app_error", nil, "post_id="+postId+", file_id="+fileId+", err="+err.Error(), http.StatusInternalServerError)
	}

	count, err := sqlResult.RowsAffected()
	if err != nil {
		// RowsAffected should never fail with the MySQL or Postgres drivers
		return model.NewAppError("SqlFileInfoStore.AttachToPost",
			"store.sql_file_info.attach_to_post.app_error", nil, "post_id="+postId+", file_id="+fileId+", err="+err.Error(), http.StatusInternalServerError)
	} else if count == 0 {
		// Could not attach the file to the post
		return model.NewAppError("SqlFileInfoStore.AttachToPost",
			"store.sql_file_info.attach_to_post.app_error", nil, "post_id="+postId+", file_id="+fileId, http.StatusBadRequest)
	}
	return nil
}

func (fs SqlFileInfoStore) DeleteForPost(postId string) (string, *model.AppError) {
	if _, err := fs.GetMaster().Exec(
		`UPDATE
				FileInfo
			SET
				DeleteAt = :DeleteAt
			WHERE
				PostId = :PostId`, map[string]interface{}{"DeleteAt": model.GetMillis(), "PostId": postId}); err != nil {
		return "", model.NewAppError("SqlFileInfoStore.DeleteForPost",
			"store.sql_file_info.delete_for_post.app_error", nil, "post_id="+postId+", err="+err.Error(), http.StatusInternalServerError)
	}
	return postId, nil
}

func (fs SqlFileInfoStore) PermanentDelete(fileId string) *model.AppError {
	if _, err := fs.GetMaster().Exec(
		`DELETE FROM
				FileInfo
			WHERE
				Id = :FileId`, map[string]interface{}{"FileId": fileId}); err != nil {
		return model.NewAppError("SqlFileInfoStore.PermanentDelete",
			"store.sql_file_info.permanent_delete.app_error", nil, "file_id="+fileId+", err="+err.Error(), http.StatusInternalServerError)
	}
	return nil
}

func (fs SqlFileInfoStore) PermanentDeleteBatch(endTime int64, limit int64) (int64, *model.AppError) {
	var query string
	if fs.DriverName() == "postgres" {
		query = "DELETE from FileInfo WHERE Id = any (array (SELECT Id FROM FileInfo WHERE CreateAt < :EndTime LIMIT :Limit))"
	} else {
		query = "DELETE from FileInfo WHERE CreateAt < :EndTime LIMIT :Limit"
	}

	sqlResult, err := fs.GetMaster().Exec(query, map[string]interface{}{"EndTime": endTime, "Limit": limit})
	if err != nil {
		return 0, model.NewAppError("SqlFileInfoStore.PermanentDeleteBatch", "store.sql_file_info.permanent_delete_batch.app_error", nil, ""+err.Error(), http.StatusInternalServerError)
	}

	rowsAffected, err := sqlResult.RowsAffected()
	if err != nil {
		return 0, model.NewAppError("SqlFileInfoStore.PermanentDeleteBatch", "store.sql_file_info.permanent_delete_batch.app_error", nil, ""+err.Error(), http.StatusInternalServerError)
	}

	return rowsAffected, nil
}

func (fs SqlFileInfoStore) PermanentDeleteByUser(userId string) (int64, *model.AppError) {
	query := "DELETE from FileInfo WHERE CreatorId = :CreatorId"

	sqlResult, err := fs.GetMaster().Exec(query, map[string]interface{}{"CreatorId": userId})
	if err != nil {
		return 0, model.NewAppError("SqlFileInfoStore.PermanentDeleteByUser", "store.sql_file_info.PermanentDeleteByUser.app_error", nil, ""+err.Error(), http.StatusInternalServerError)
	}

	rowsAffected, err := sqlResult.RowsAffected()
	if err != nil {
		return 0, model.NewAppError("SqlFileInfoStore.PermanentDeleteByUser", "store.sql_file_info.PermanentDeleteByUser.app_error", nil, ""+err.Error(), http.StatusInternalServerError)
	}

	return rowsAffected, nil
}
