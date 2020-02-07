// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See License.txt for license information.

package sqlstore

import (
	"database/sql"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"

	sq "github.com/Masterminds/squirrel"
	"github.com/mattermost/mattermost-server/v5/einterfaces"
	"github.com/mattermost/mattermost-server/v5/mlog"
	"github.com/mattermost/mattermost-server/v5/model"
	"github.com/mattermost/mattermost-server/v5/store"
	"github.com/mattermost/mattermost-server/v5/utils"
)

type SqlPostStore struct {
	SqlStore
	metrics           einterfaces.MetricsInterface
	lastPostTimeCache *utils.Cache
	lastPostsCache    *utils.Cache
	maxPostSizeOnce   sync.Once
	maxPostSizeCached int
}

const (
	LAST_POST_TIME_CACHE_SIZE = 25000
	LAST_POST_TIME_CACHE_SEC  = 900 // 15 minutes

	LAST_POSTS_CACHE_SIZE = 1000
	LAST_POSTS_CACHE_SEC  = 900 // 15 minutes
)

func (s *SqlPostStore) ClearCaches() {
	s.lastPostTimeCache.Purge()
	s.lastPostsCache.Purge()

	if s.metrics != nil {
		s.metrics.IncrementMemCacheInvalidationCounter("Last Post Time - Purge")
		s.metrics.IncrementMemCacheInvalidationCounter("Last Posts Cache - Purge")
	}
}

func NewSqlPostStore(sqlStore SqlStore, metrics einterfaces.MetricsInterface) store.PostStore {
	s := &SqlPostStore{
		SqlStore:          sqlStore,
		metrics:           metrics,
		lastPostTimeCache: utils.NewLru(LAST_POST_TIME_CACHE_SIZE),
		lastPostsCache:    utils.NewLru(LAST_POSTS_CACHE_SIZE),
		maxPostSizeCached: model.POST_MESSAGE_MAX_RUNES_V1,
	}

	for _, db := range sqlStore.GetAllConns() {
		table := db.AddTableWithName(model.Post{}, "Posts").SetKeys(false, "Id")
		table.ColMap("Id").SetMaxSize(26)
		table.ColMap("UserId").SetMaxSize(26)
		table.ColMap("ChannelId").SetMaxSize(26)
		table.ColMap("RootId").SetMaxSize(26)
		table.ColMap("ParentId").SetMaxSize(26)
		table.ColMap("OriginalId").SetMaxSize(26)
		table.ColMap("Message").SetMaxSize(model.POST_MESSAGE_MAX_BYTES_V2)
		table.ColMap("Type").SetMaxSize(26)
		table.ColMap("Hashtags").SetMaxSize(1000)
		table.ColMap("Props").SetMaxSize(8000)
		table.ColMap("Filenames").SetMaxSize(model.POST_FILENAMES_MAX_RUNES)
		table.ColMap("FileIds").SetMaxSize(150)
	}

	return s
}

func (s *SqlPostStore) CreateIndexesIfNotExists() {
	s.CreateIndexIfNotExists("idx_posts_update_at", "Posts", "UpdateAt")
	s.CreateIndexIfNotExists("idx_posts_create_at", "Posts", "CreateAt")
	s.CreateIndexIfNotExists("idx_posts_delete_at", "Posts", "DeleteAt")
	s.CreateIndexIfNotExists("idx_posts_channel_id", "Posts", "ChannelId")
	s.CreateIndexIfNotExists("idx_posts_root_id", "Posts", "RootId")
	s.CreateIndexIfNotExists("idx_posts_user_id", "Posts", "UserId")
	s.CreateIndexIfNotExists("idx_posts_is_pinned", "Posts", "IsPinned")

	s.CreateCompositeIndexIfNotExists("idx_posts_channel_id_update_at", "Posts", []string{"ChannelId", "UpdateAt"})
	s.CreateCompositeIndexIfNotExists("idx_posts_channel_id_delete_at_create_at", "Posts", []string{"ChannelId", "DeleteAt", "CreateAt"})

	s.CreateFullTextIndexIfNotExists("idx_posts_message_txt", "Posts", "Message")
	s.CreateFullTextIndexIfNotExists("idx_posts_hashtags_txt", "Posts", "Hashtags")
}

func (s *SqlPostStore) Save(post *model.Post) (*model.Post, *model.AppError) {
	if len(post.Id) > 0 {
		return nil, model.NewAppError("SqlPostStore.Save", "store.sql_post.save.existing.app_error", nil, "id="+post.Id, http.StatusBadRequest)
	}

	maxPostSize := s.GetMaxPostSize()

	post.PreSave()
	if err := post.IsValid(maxPostSize); err != nil {
		return nil, err
	}

	if err := s.GetMaster().Insert(post); err != nil {
		return nil, model.NewAppError("SqlPostStore.Save", "store.sql_post.save.app_error", nil, "id="+post.Id+", "+err.Error(), http.StatusInternalServerError)
	}

	time := post.UpdateAt

	if !post.IsJoinLeaveMessage() {
		if _, err := s.GetMaster().Exec("UPDATE Channels SET LastPostAt = GREATEST(:LastPostAt, LastPostAt), TotalMsgCount = TotalMsgCount + 1 WHERE Id = :ChannelId", map[string]interface{}{"LastPostAt": time, "ChannelId": post.ChannelId}); err != nil {
			mlog.Error("Error updating Channel LastPostAt.", mlog.Err(err))
		}
	} else {
		// don't update TotalMsgCount for unimportant messages so that the channel isn't marked as unread
		if _, err := s.GetMaster().Exec("UPDATE Channels SET LastPostAt = :LastPostAt WHERE Id = :ChannelId AND LastPostAt < :LastPostAt", map[string]interface{}{"LastPostAt": time, "ChannelId": post.ChannelId}); err != nil {
			mlog.Error("Error updating Channel LastPostAt.", mlog.Err(err))
		}
	}

	if len(post.RootId) > 0 {
		if _, err := s.GetMaster().Exec("UPDATE Posts SET UpdateAt = :UpdateAt WHERE Id = :RootId", map[string]interface{}{"UpdateAt": time, "RootId": post.RootId}); err != nil {
			mlog.Error("Error updating Post UpdateAt.", mlog.Err(err))
		}
	}

	return post, nil
}

func (s *SqlPostStore) Update(newPost *model.Post, oldPost *model.Post) (*model.Post, *model.AppError) {
	newPost.UpdateAt = model.GetMillis()
	newPost.PreCommit()

	oldPost.DeleteAt = newPost.UpdateAt
	oldPost.UpdateAt = newPost.UpdateAt
	oldPost.OriginalId = oldPost.Id
	oldPost.Id = model.NewId()
	oldPost.PreCommit()

	maxPostSize := s.GetMaxPostSize()

	if err := newPost.IsValid(maxPostSize); err != nil {
		return nil, err
	}

	if _, err := s.GetMaster().Update(newPost); err != nil {
		return nil, model.NewAppError("SqlPostStore.Update", "store.sql_post.update.app_error", nil, "id="+newPost.Id+", "+err.Error(), http.StatusInternalServerError)
	}

	time := model.GetMillis()
	s.GetMaster().Exec("UPDATE Channels SET LastPostAt = :LastPostAt  WHERE Id = :ChannelId AND LastPostAt < :LastPostAt", map[string]interface{}{"LastPostAt": time, "ChannelId": newPost.ChannelId})

	if len(newPost.RootId) > 0 {
		s.GetMaster().Exec("UPDATE Posts SET UpdateAt = :UpdateAt WHERE Id = :RootId AND UpdateAt < :UpdateAt", map[string]interface{}{"UpdateAt": time, "RootId": newPost.RootId})
	}

	// mark the old post as deleted
	s.GetMaster().Insert(oldPost)

	return newPost, nil
}

func (s *SqlPostStore) Overwrite(post *model.Post) (*model.Post, *model.AppError) {
	post.UpdateAt = model.GetMillis()

	maxPostSize := s.GetMaxPostSize()
	if appErr := post.IsValid(maxPostSize); appErr != nil {
		return nil, appErr
	}

	if _, err := s.GetMaster().Update(post); err != nil {
		return nil, model.NewAppError("SqlPostStore.Overwrite", "store.sql_post.overwrite.app_error", nil, "id="+post.Id+", "+err.Error(), http.StatusInternalServerError)
	}

	return post, nil
}

func (s *SqlPostStore) GetFlaggedPosts(userId string, offset int, limit int) (*model.PostList, *model.AppError) {
	pl := model.NewPostList()

	var posts []*model.Post
	if _, err := s.GetReplica().Select(&posts, "SELECT * FROM Posts WHERE Id IN (SELECT Name FROM Preferences WHERE UserId = :UserId AND Category = :Category) AND DeleteAt = 0 ORDER BY CreateAt DESC LIMIT :Limit OFFSET :Offset", map[string]interface{}{"UserId": userId, "Category": model.PREFERENCE_CATEGORY_FLAGGED_POST, "Offset": offset, "Limit": limit}); err != nil {
		return nil, model.NewAppError("SqlPostStore.GetFlaggedPosts", "store.sql_post.get_flagged_posts.app_error", nil, err.Error(), http.StatusInternalServerError)
	}

	for _, post := range posts {
		pl.AddPost(post)
		pl.AddOrder(post.Id)
	}

	return pl, nil
}

func (s *SqlPostStore) GetFlaggedPostsForTeam(userId, teamId string, offset int, limit int) (*model.PostList, *model.AppError) {
	pl := model.NewPostList()

	var posts []*model.Post

	query := `
            SELECT
                A.*
            FROM
                (SELECT
                    *
                FROM
                    Posts
                WHERE
                    Id
                IN
                    (SELECT
                        Name
                    FROM
                        Preferences
                    WHERE
                        UserId = :UserId
                        AND Category = :Category)
                        AND DeleteAt = 0
                ) as A
            INNER JOIN Channels as B
                ON B.Id = A.ChannelId
            WHERE B.TeamId = :TeamId OR B.TeamId = ''
            ORDER BY CreateAt DESC
            LIMIT :Limit OFFSET :Offset`

	if _, err := s.GetReplica().Select(&posts, query, map[string]interface{}{"UserId": userId, "Category": model.PREFERENCE_CATEGORY_FLAGGED_POST, "Offset": offset, "Limit": limit, "TeamId": teamId}); err != nil {
		return nil, model.NewAppError("SqlPostStore.GetFlaggedPostsForTeam", "store.sql_post.get_flagged_posts.app_error", nil, err.Error(), http.StatusInternalServerError)
	}

	for _, post := range posts {
		pl.AddPost(post)
		pl.AddOrder(post.Id)
	}

	return pl, nil
}

func (s *SqlPostStore) GetFlaggedPostsForChannel(userId, channelId string, offset int, limit int) (*model.PostList, *model.AppError) {
	pl := model.NewPostList()

	var posts []*model.Post
	query := `
		SELECT
			*
		FROM Posts
		WHERE
			Id IN (SELECT Name FROM Preferences WHERE UserId = :UserId AND Category = :Category)
			AND ChannelId = :ChannelId
			AND DeleteAt = 0
		ORDER BY CreateAt DESC
		LIMIT :Limit OFFSET :Offset`

	if _, err := s.GetReplica().Select(&posts, query, map[string]interface{}{"UserId": userId, "Category": model.PREFERENCE_CATEGORY_FLAGGED_POST, "ChannelId": channelId, "Offset": offset, "Limit": limit}); err != nil {
		return nil, model.NewAppError("SqlPostStore.GetFlaggedPostsForChannel", "store.sql_post.get_flagged_posts.app_error", nil, err.Error(), http.StatusInternalServerError)
	}
	for _, post := range posts {
		pl.AddPost(post)
		pl.AddOrder(post.Id)
	}

	return pl, nil
}

func (s *SqlPostStore) Get(id string) (*model.PostList, *model.AppError) {
	pl := model.NewPostList()

	if len(id) == 0 {
		return nil, model.NewAppError("SqlPostStore.GetPost", "store.sql_post.get.app_error", nil, "id="+id, http.StatusBadRequest)
	}

	var post model.Post
	err := s.GetReplica().SelectOne(&post, "SELECT * FROM Posts WHERE Id = :Id AND DeleteAt = 0", map[string]interface{}{"Id": id})
	if err != nil {
		return nil, model.NewAppError("SqlPostStore.GetPost", "store.sql_post.get.app_error", nil, "id="+id+err.Error(), http.StatusNotFound)
	}

	pl.AddPost(&post)
	pl.AddOrder(id)

	rootId := post.RootId

	if rootId == "" {
		rootId = post.Id
	}

	if len(rootId) == 0 {
		return nil, model.NewAppError("SqlPostStore.GetPost", "store.sql_post.get.app_error", nil, "root_id="+rootId, http.StatusInternalServerError)
	}

	var posts []*model.Post
	_, err = s.GetReplica().Select(&posts, "SELECT * FROM Posts WHERE (Id = :Id OR RootId = :RootId) AND DeleteAt = 0", map[string]interface{}{"Id": rootId, "RootId": rootId})
	if err != nil {
		return nil, model.NewAppError("SqlPostStore.GetPost", "store.sql_post.get.app_error", nil, "root_id="+rootId+err.Error(), http.StatusInternalServerError)
	}

	for _, p := range posts {
		pl.AddPost(p)
		pl.AddOrder(p.Id)
	}

	return pl, nil
}

func (s *SqlPostStore) GetSingle(id string) (*model.Post, *model.AppError) {
	var post model.Post
	err := s.GetReplica().SelectOne(&post, "SELECT * FROM Posts WHERE Id = :Id AND DeleteAt = 0", map[string]interface{}{"Id": id})
	if err != nil {
		return nil, model.NewAppError("SqlPostStore.GetSingle", "store.sql_post.get.app_error", nil, "id="+id+err.Error(), http.StatusNotFound)
	}
	return &post, nil
}

type etagPosts struct {
	Id       string
	UpdateAt int64
}

func (s *SqlPostStore) InvalidateLastPostTimeCache(channelId string) {
	s.lastPostTimeCache.Remove(channelId)

	// Keys are "{channelid}{limit}" and caching only occurs on limits of 30 and 60
	s.lastPostsCache.Remove(channelId + "30")
	s.lastPostsCache.Remove(channelId + "60")

	if s.metrics != nil {
		s.metrics.IncrementMemCacheInvalidationCounter("Last Post Time - Remove by Channel Id")
		s.metrics.IncrementMemCacheInvalidationCounter("Last Posts Cache - Remove by Channel Id")
	}
}

func (s *SqlPostStore) GetEtag(channelId string, allowFromCache bool) string {
	if allowFromCache {
		if cacheItem, ok := s.lastPostTimeCache.Get(channelId); ok {
			if s.metrics != nil {
				s.metrics.IncrementMemCacheHitCounter("Last Post Time")
			}
			return fmt.Sprintf("%v.%v", model.CurrentVersion, cacheItem.(int64))
		} else {
			if s.metrics != nil {
				s.metrics.IncrementMemCacheMissCounter("Last Post Time")
			}
		}
	} else {
		if s.metrics != nil {
			s.metrics.IncrementMemCacheMissCounter("Last Post Time")
		}
	}

	var et etagPosts
	err := s.GetReplica().SelectOne(&et, "SELECT Id, UpdateAt FROM Posts WHERE ChannelId = :ChannelId ORDER BY UpdateAt DESC LIMIT 1", map[string]interface{}{"ChannelId": channelId})
	var result string
	if err != nil {
		result = fmt.Sprintf("%v.%v", model.CurrentVersion, model.GetMillis())
	} else {
		result = fmt.Sprintf("%v.%v", model.CurrentVersion, et.UpdateAt)
	}

	s.lastPostTimeCache.AddWithExpiresInSecs(channelId, et.UpdateAt, LAST_POST_TIME_CACHE_SEC)
	return result
}

func (s *SqlPostStore) Delete(postId string, time int64, deleteByID string) *model.AppError {

	appErr := func(errMsg string) *model.AppError {
		return model.NewAppError("SqlPostStore.Delete", "store.sql_post.delete.app_error", nil, "id="+postId+", err="+errMsg, http.StatusInternalServerError)
	}

	var post model.Post
	err := s.GetReplica().SelectOne(&post, "SELECT * FROM Posts WHERE Id = :Id AND DeleteAt = 0", map[string]interface{}{"Id": postId})
	if err != nil {
		return appErr(err.Error())
	}

	post.Props[model.POST_PROPS_DELETE_BY] = deleteByID

	_, err = s.GetMaster().Exec("UPDATE Posts SET DeleteAt = :DeleteAt, UpdateAt = :UpdateAt, Props = :Props WHERE Id = :Id OR RootId = :RootId", map[string]interface{}{"DeleteAt": time, "UpdateAt": time, "Id": postId, "RootId": postId, "Props": model.StringInterfaceToJson(post.Props)})
	if err != nil {
		return appErr(err.Error())
	}

	return nil
}

func (s *SqlPostStore) permanentDelete(postId string) *model.AppError {
	_, err := s.GetMaster().Exec("DELETE FROM Posts WHERE Id = :Id OR RootId = :RootId", map[string]interface{}{"Id": postId, "RootId": postId})
	if err != nil {
		return model.NewAppError("SqlPostStore.Delete", "store.sql_post.permanent_delete.app_error", nil, "id="+postId+", err="+err.Error(), http.StatusInternalServerError)
	}
	return nil
}

func (s *SqlPostStore) permanentDeleteAllCommentByUser(userId string) *model.AppError {
	_, err := s.GetMaster().Exec("DELETE FROM Posts WHERE UserId = :UserId AND RootId != ''", map[string]interface{}{"UserId": userId})
	if err != nil {
		return model.NewAppError("SqlPostStore.permanentDeleteAllCommentByUser", "store.sql_post.permanent_delete_all_comments_by_user.app_error", nil, "userId="+userId+", err="+err.Error(), http.StatusInternalServerError)
	}
	return nil
}

func (s *SqlPostStore) PermanentDeleteByUser(userId string) *model.AppError {
	// First attempt to delete all the comments for a user
	if err := s.permanentDeleteAllCommentByUser(userId); err != nil {
		return err
	}

	// Now attempt to delete all the root posts for a user. This will also
	// delete all the comments for each post
	found := true
	count := 0

	for found {
		var ids []string
		_, err := s.GetMaster().Select(&ids, "SELECT Id FROM Posts WHERE UserId = :UserId LIMIT 1000", map[string]interface{}{"UserId": userId})
		if err != nil {
			return model.NewAppError("SqlPostStore.PermanentDeleteByUser.select", "store.sql_post.permanent_delete_by_user.app_error", nil, "userId="+userId+", err="+err.Error(), http.StatusInternalServerError)
		}

		found = false
		for _, id := range ids {
			found = true
			if err := s.permanentDelete(id); err != nil {
				return err
			}
		}

		// This is a fail safe, give up if more than 10k messages
		count++
		if count >= 10 {
			return model.NewAppError("SqlPostStore.PermanentDeleteByUser.toolarge", "store.sql_post.permanent_delete_by_user.too_many.app_error", nil, "userId="+userId, http.StatusInternalServerError)
		}
	}

	return nil
}

func (s *SqlPostStore) PermanentDeleteByChannel(channelId string) *model.AppError {
	if _, err := s.GetMaster().Exec("DELETE FROM Posts WHERE ChannelId = :ChannelId", map[string]interface{}{"ChannelId": channelId}); err != nil {
		return model.NewAppError("SqlPostStore.PermanentDeleteByChannel", "store.sql_post.permanent_delete_by_channel.app_error", nil, "channel_id="+channelId+", "+err.Error(), http.StatusInternalServerError)
	}
	return nil
}

func (s *SqlPostStore) GetPosts(channelId string, offset int, limit int, allowFromCache bool) (*model.PostList, *model.AppError) {
	if limit > 1000 {
		return nil, model.NewAppError("SqlPostStore.GetLinearPosts", "store.sql_post.get_posts.app_error", nil, "channelId="+channelId, http.StatusBadRequest)
	}

	// Caching only occurs on limits of 30 and 60, the common limits requested by MM clients
	if allowFromCache && offset == 0 && (limit == 60 || limit == 30) {
		if cacheItem, ok := s.lastPostsCache.Get(fmt.Sprintf("%s%v", channelId, limit)); ok {
			if s.metrics != nil {
				s.metrics.IncrementMemCacheHitCounter("Last Posts Cache")
			}
			return cacheItem.(*model.PostList), nil
		}
	}

	if s.metrics != nil {
		s.metrics.IncrementMemCacheMissCounter("Last Posts Cache")
	}

	rpc := make(chan store.StoreResult, 1)
	go func() {
		posts, err := s.getRootPosts(channelId, offset, limit)
		rpc <- store.StoreResult{Data: posts, Err: err}
		close(rpc)
	}()
	cpc := make(chan store.StoreResult, 1)
	go func() {
		posts, err := s.getParentsPosts(channelId, offset, limit)
		cpc <- store.StoreResult{Data: posts, Err: err}
		close(cpc)
	}()

	var err *model.AppError
	list := model.NewPostList()

	rpr := <-rpc
	if rpr.Err != nil {
		return nil, rpr.Err
	}

	cpr := <-cpc
	if cpr.Err != nil {
		return nil, cpr.Err
	}

	posts := rpr.Data.([]*model.Post)
	parents := cpr.Data.([]*model.Post)

	for _, p := range posts {
		list.AddPost(p)
		list.AddOrder(p.Id)
	}

	for _, p := range parents {
		list.AddPost(p)
	}

	list.MakeNonNil()

	// Caching only occurs on limits of 30 and 60, the common limits requested by MM clients
	if offset == 0 && (limit == 60 || limit == 30) {
		s.lastPostsCache.AddWithExpiresInSecs(fmt.Sprintf("%s%v", channelId, limit), list, LAST_POSTS_CACHE_SEC)
	}

	return list, err
}

func (s *SqlPostStore) GetPostsSince(channelId string, time int64, allowFromCache bool) (*model.PostList, *model.AppError) {
	if allowFromCache {
		// If the last post in the channel's time is less than or equal to the time we are getting posts since,
		// we can safely return no posts.
		if cacheItem, ok := s.lastPostTimeCache.Get(channelId); ok && cacheItem.(int64) <= time {
			if s.metrics != nil {
				s.metrics.IncrementMemCacheHitCounter("Last Post Time")
			}
			list := model.NewPostList()
			return list, nil
		}
	}

	if s.metrics != nil {
		s.metrics.IncrementMemCacheMissCounter("Last Post Time")
	}

	var posts []*model.Post
	_, err := s.GetReplica().Select(&posts,
		`(SELECT
			*
		FROM
			Posts
		WHERE
			(UpdateAt > :Time
				AND ChannelId = :ChannelId)
			LIMIT 1000)
		UNION
			(SELECT
			    *
			FROM
			    Posts
			WHERE
			    Id
			IN
			    (SELECT * FROM (SELECT
			        RootId
			    FROM
			        Posts
			    WHERE
			        UpdateAt > :Time
						AND ChannelId = :ChannelId
				LIMIT 1000) temp_tab))
		ORDER BY CreateAt DESC`,
		map[string]interface{}{"ChannelId": channelId, "Time": time})

	if err != nil {
		return nil, model.NewAppError("SqlPostStore.GetPostsSince", "store.sql_post.get_posts_since.app_error", nil, "channelId="+channelId+err.Error(), http.StatusInternalServerError)
	}

	list := model.NewPostList()

	latestUpdate := time

	for _, p := range posts {
		list.AddPost(p)
		if p.UpdateAt > time {
			list.AddOrder(p.Id)
		}
		if latestUpdate < p.UpdateAt {
			latestUpdate = p.UpdateAt
		}
	}

	s.lastPostTimeCache.AddWithExpiresInSecs(channelId, latestUpdate, LAST_POST_TIME_CACHE_SEC)

	return list, nil
}

func (s *SqlPostStore) GetPostsBefore(channelId string, postId string, limit int, offset int) (*model.PostList, *model.AppError) {
	return s.getPostsAround(channelId, postId, limit, offset, true)
}

func (s *SqlPostStore) GetPostsAfter(channelId string, postId string, limit int, offset int) (*model.PostList, *model.AppError) {
	return s.getPostsAround(channelId, postId, limit, offset, false)
}

func (s *SqlPostStore) getPostsAround(channelId string, postId string, limit int, offset int, before bool) (*model.PostList, *model.AppError) {
	var direction, sort string
	if before {
		direction = "<"
		sort = "DESC"
	} else {
		direction = ">"
		sort = "ASC"
	}

	var posts, parents []*model.Post
	_, err := s.GetReplica().Select(&posts,
		`SELECT
			*
		FROM
			Posts
		WHERE
			CreateAt `+direction+` (SELECT CreateAt FROM Posts WHERE Id = :PostId)
				AND ChannelId = :ChannelId
				AND DeleteAt = 0
		ORDER BY CreateAt `+sort+`
		LIMIT :Limit
		OFFSET :Offset`,
		map[string]interface{}{"ChannelId": channelId, "PostId": postId, "Limit": limit, "Offset": offset})
	if err != nil {
		return nil, model.NewAppError("SqlPostStore.GetPostContext", "store.sql_post.get_posts_around.get.app_error", nil, "channelId="+channelId+err.Error(), http.StatusInternalServerError)
	}

	if len(posts) > 0 {
		rootIds := []string{}
		for _, post := range posts {
			rootIds = append(rootIds, post.Id)
			if post.RootId != "" {
				rootIds = append(rootIds, post.RootId)
			}
		}

		keys, params := MapStringsToQueryParams(rootIds, "PostId")

		params["ChannelId"] = channelId
		params["PostId"] = postId
		params["Limit"] = limit
		params["Offset"] = offset

		_, err = s.GetReplica().Select(&parents,
			`SELECT
				*
			FROM
				Posts
			WHERE
				(Id IN `+keys+` OR RootId IN `+keys+`)
				AND ChannelId = :ChannelId
				AND DeleteAt = 0
			ORDER BY CreateAt DESC`,
			params)

		if err != nil {
			return nil, model.NewAppError("SqlPostStore.GetPostContext", "store.sql_post.get_posts_around.get_parent.app_error", nil, "channelId="+channelId+err.Error(), http.StatusInternalServerError)
		}
	}

	list := model.NewPostList()

	// We need to flip the order if we selected backwards
	if before {
		for _, p := range posts {
			list.AddPost(p)
			list.AddOrder(p.Id)
		}
	} else {
		l := len(posts)
		for i := range posts {
			list.AddPost(posts[l-i-1])
			list.AddOrder(posts[l-i-1].Id)
		}
	}

	for _, p := range parents {
		list.AddPost(p)
	}

	return list, nil
}

func (s *SqlPostStore) GetPostIdBeforeTime(channelId string, time int64) (string, *model.AppError) {
	return s.getPostIdAroundTime(channelId, time, true)
}

func (s *SqlPostStore) GetPostIdAfterTime(channelId string, time int64) (string, *model.AppError) {
	return s.getPostIdAroundTime(channelId, time, false)
}

func (s *SqlPostStore) getPostIdAroundTime(channelId string, time int64, before bool) (string, *model.AppError) {
	var direction sq.Sqlizer
	var sort string
	if before {
		direction = sq.Lt{"CreateAt": time}
		sort = "DESC"
	} else {
		direction = sq.Gt{"CreateAt": time}
		sort = "ASC"
	}

	query := s.getQueryBuilder().
		Select("Id").
		From("Posts").
		Where(sq.And{
			direction,
			sq.Eq{"ChannelId": channelId},
			sq.Eq{"DeleteAt": int(0)},
		}).
		OrderBy("CreateAt " + sort).
		Limit(1)

	queryString, args, err := query.ToSql()
	if err != nil {
		return "", model.NewAppError("SqlPostStore.getPostIdAroundTime", "store.sql_post.get_post_id_around.app_error", nil, err.Error(), http.StatusInternalServerError)
	}

	var postId string
	if err := s.GetMaster().SelectOne(&postId, queryString, args...); err != nil {
		if err != sql.ErrNoRows {
			return "", model.NewAppError("SqlPostStore.getPostIdAroundTime", "store.sql_post.get_post_id_around.app_error", nil, "channelId="+channelId+err.Error(), http.StatusInternalServerError)
		}
	}

	return postId, nil
}

func (s *SqlPostStore) GetPostAfterTime(channelId string, time int64) (*model.Post, *model.AppError) {
	query := s.getQueryBuilder().
		Select("*").
		From("Posts").
		Where(sq.And{
			sq.Gt{"CreateAt": time},
			sq.Eq{"ChannelId": channelId},
			sq.Eq{"DeleteAt": int(0)},
		}).
		OrderBy("CreateAt ASC").
		Limit(1)

	queryString, args, err := query.ToSql()
	if err != nil {
		return nil, model.NewAppError("SqlPostStore.GetPostAfterTime", "store.sql_post.get_post_after_time.app_error", nil, err.Error(), http.StatusInternalServerError)
	}

	var post *model.Post
	if err := s.GetMaster().SelectOne(&post, queryString, args...); err != nil {
		if err != sql.ErrNoRows {
			return nil, model.NewAppError("SqlPostStore.GetPostAfterTime", "store.sql_post.get_post_after_time.app_error", nil, "channelId="+channelId+err.Error(), http.StatusInternalServerError)
		}
	}

	return post, nil
}

func (s *SqlPostStore) getRootPosts(channelId string, offset int, limit int) ([]*model.Post, *model.AppError) {
	var posts []*model.Post
	_, err := s.GetReplica().Select(&posts, "SELECT * FROM Posts WHERE ChannelId = :ChannelId AND DeleteAt = 0 ORDER BY CreateAt DESC LIMIT :Limit OFFSET :Offset", map[string]interface{}{"ChannelId": channelId, "Offset": offset, "Limit": limit})
	if err != nil {
		return nil, model.NewAppError("SqlPostStore.GetLinearPosts", "store.sql_post.get_root_posts.app_error", nil, "channelId="+channelId+err.Error(), http.StatusInternalServerError)
	}
	return posts, nil
}

func (s *SqlPostStore) getParentsPosts(channelId string, offset int, limit int) ([]*model.Post, *model.AppError) {
	var posts []*model.Post
	_, err := s.GetReplica().Select(&posts,
		`SELECT
			q2.*
		FROM
			Posts q2
				INNER JOIN
			(SELECT DISTINCT
				q3.RootId
			FROM
				(SELECT
					RootId
				FROM
					Posts
				WHERE
					ChannelId = :ChannelId1
						AND DeleteAt = 0
				ORDER BY CreateAt DESC
				LIMIT :Limit OFFSET :Offset) q3
			WHERE q3.RootId != '') q1
			ON q1.RootId = q2.Id OR q1.RootId = q2.RootId
		WHERE
			ChannelId = :ChannelId2
				AND DeleteAt = 0
		ORDER BY CreateAt`,
		map[string]interface{}{"ChannelId1": channelId, "Offset": offset, "Limit": limit, "ChannelId2": channelId})
	if err != nil {
		return nil, model.NewAppError("SqlPostStore.GetLinearPosts", "store.sql_post.get_parents_posts.app_error", nil, "channelId="+channelId+" err="+err.Error(), http.StatusInternalServerError)
	}
	return posts, nil
}

var specialSearchChar = []string{
	"<",
	">",
	"+",
	"-",
	"(",
	")",
	"~",
	"@",
	":",
}

func (s *SqlPostStore) buildCreateDateFilterClause(params *model.SearchParams, queryParams map[string]interface{}) (string, map[string]interface{}) {
	searchQuery := ""
	// handle after: before: on: filters
	if len(params.OnDate) > 0 {
		onDateStart, onDateEnd := params.GetOnDateMillis()
		queryParams["OnDateStart"] = strconv.FormatInt(onDateStart, 10)
		queryParams["OnDateEnd"] = strconv.FormatInt(onDateEnd, 10)

		// between `on date` start of day and end of day
		searchQuery += "AND CreateAt BETWEEN :OnDateStart AND :OnDateEnd "
	} else {

		if len(params.ExcludedDate) > 0 {
			excludedDateStart, excludedDateEnd := params.GetExcludedDateMillis()
			queryParams["ExcludedDateStart"] = strconv.FormatInt(excludedDateStart, 10)
			queryParams["ExcludedDateEnd"] = strconv.FormatInt(excludedDateEnd, 10)

			searchQuery += "AND CreateAt NOT BETWEEN :ExcludedDateStart AND :ExcludedDateEnd "
		}

		if len(params.AfterDate) > 0 {
			afterDate := params.GetAfterDateMillis()
			queryParams["AfterDate"] = strconv.FormatInt(afterDate, 10)

			// greater than `after date`
			searchQuery += "AND CreateAt >= :AfterDate "
		}

		if len(params.BeforeDate) > 0 {
			beforeDate := params.GetBeforeDateMillis()
			queryParams["BeforeDate"] = strconv.FormatInt(beforeDate, 10)

			// less than `before date`
			searchQuery += "AND CreateAt <= :BeforeDate "
		}

		if len(params.ExcludedAfterDate) > 0 {
			afterDate := params.GetExcludedAfterDateMillis()
			queryParams["ExcludedAfterDate"] = strconv.FormatInt(afterDate, 10)

			searchQuery += "AND CreateAt < :ExcludedAfterDate "
		}

		if len(params.ExcludedBeforeDate) > 0 {
			beforeDate := params.GetExcludedBeforeDateMillis()
			queryParams["ExcludedBeforeDate"] = strconv.FormatInt(beforeDate, 10)

			searchQuery += "AND CreateAt > :ExcludedBeforeDate "
		}
	}

	return searchQuery, queryParams
}

func (s *SqlPostStore) buildSearchChannelFilterClause(channels []string, paramPrefix string, exclusion bool, queryParams map[string]interface{}) (string, map[string]interface{}) {
	if len(channels) == 0 {
		return "", queryParams
	}

	clauseSlice := []string{}
	for i, channel := range channels {
		paramName := paramPrefix + strconv.FormatInt(int64(i), 10)
		clauseSlice = append(clauseSlice, ":"+paramName)
		queryParams[paramName] = channel
	}
	clause := strings.Join(clauseSlice, ", ")
	if exclusion {
		return "AND Name NOT IN (" + clause + ")", queryParams
	}
	return "AND Name IN (" + clause + ")", queryParams
}

func (s *SqlPostStore) buildSearchUserFilterClause(users []string, paramPrefix string, exclusion bool, queryParams map[string]interface{}) (string, map[string]interface{}) {
	if len(users) == 0 {
		return "", queryParams
	}
	clauseSlice := []string{}
	for i, user := range users {
		paramName := paramPrefix + strconv.FormatInt(int64(i), 10)
		clauseSlice = append(clauseSlice, ":"+paramName)
		queryParams[paramName] = user
	}
	clause := strings.Join(clauseSlice, ", ")
	if exclusion {
		return "AND Username NOT IN (" + clause + ")", queryParams
	}
	return "AND Username IN (" + clause + ")", queryParams
}

func (s *SqlPostStore) buildSearchPostFilterClause(fromUsers []string, excludedUsers []string, queryParams map[string]interface{}) (string, map[string]interface{}) {
	if len(fromUsers) == 0 && len(excludedUsers) == 0 {
		return "", queryParams
	}

	filterQuery := `
		AND UserId IN (
			SELECT
				Id
			FROM
				Users,
				TeamMembers
			WHERE
				TeamMembers.TeamId = :TeamId
				AND Users.Id = TeamMembers.UserId
				FROM_USER_FILTER
				EXCLUDED_USER_FILTER)`

	fromUserClause, queryParams := s.buildSearchUserFilterClause(fromUsers, "FromUser", false, queryParams)
	filterQuery = strings.Replace(filterQuery, "FROM_USER_FILTER", fromUserClause, 1)

	excludedUserClause, queryParams := s.buildSearchUserFilterClause(excludedUsers, "ExcludedUser", true, queryParams)
	filterQuery = strings.Replace(filterQuery, "EXCLUDED_USER_FILTER", excludedUserClause, 1)

	return filterQuery, queryParams
}

func (s *SqlPostStore) Search(teamId string, userId string, params *model.SearchParams) (*model.PostList, *model.AppError) {
	queryParams := map[string]interface{}{
		"TeamId": teamId,
		"UserId": userId,
	}

	list := model.NewPostList()
	if params.Terms == "" && params.ExcludedTerms == "" &&
		len(params.InChannels) == 0 && len(params.ExcludedChannels) == 0 &&
		len(params.FromUsers) == 0 && len(params.ExcludedUsers) == 0 &&
		len(params.OnDate) == 0 && len(params.AfterDate) == 0 && len(params.BeforeDate) == 0 {
		return list, nil
	}

	var posts []*model.Post

	deletedQueryPart := "AND DeleteAt = 0"
	if params.IncludeDeletedChannels {
		deletedQueryPart = ""
	}

	userIdPart := "AND UserId = :UserId"
	if params.SearchWithoutUserId {
		userIdPart = ""
	}

	searchQuery := `
			SELECT
				*
			FROM
				Posts
			WHERE
				DeleteAt = 0
				AND Type NOT LIKE '` + model.POST_SYSTEM_MESSAGE_PREFIX + `%'
				POST_FILTER
				AND ChannelId IN (
					SELECT
						Id
					FROM
						Channels,
						ChannelMembers
					WHERE
						Id = ChannelId
							AND (TeamId = :TeamId OR TeamId = '')
							` + userIdPart + `
							` + deletedQueryPart + `
							IN_CHANNEL_FILTER
							EXCLUDED_CHANNEL_FILTER)
				CREATEDATE_CLAUSE
				SEARCH_CLAUSE
				ORDER BY CreateAt DESC
			LIMIT 100`

	inChannelClause, queryParams := s.buildSearchChannelFilterClause(params.InChannels, "InChannel", false, queryParams)
	searchQuery = strings.Replace(searchQuery, "IN_CHANNEL_FILTER", inChannelClause, 1)

	excludedChannelClause, queryParams := s.buildSearchChannelFilterClause(params.ExcludedChannels, "ExcludedChannel", true, queryParams)
	searchQuery = strings.Replace(searchQuery, "EXCLUDED_CHANNEL_FILTER", excludedChannelClause, 1)

	postFilterClause, queryParams := s.buildSearchPostFilterClause(params.FromUsers, params.ExcludedUsers, queryParams)
	searchQuery = strings.Replace(searchQuery, "POST_FILTER", postFilterClause, 1)

	createDateFilterClause, queryParams := s.buildCreateDateFilterClause(params, queryParams)
	searchQuery = strings.Replace(searchQuery, "CREATEDATE_CLAUSE", createDateFilterClause, 1)

	termMap := map[string]bool{}
	terms := params.Terms
	excludedTerms := params.ExcludedTerms

	searchType := "Message"
	if params.IsHashtag {
		searchType = "Hashtags"
		for _, term := range strings.Split(terms, " ") {
			termMap[strings.ToUpper(term)] = true
		}
	}

	// these chars have special meaning and can be treated as spaces
	for _, c := range specialSearchChar {
		terms = strings.Replace(terms, c, " ", -1)
		excludedTerms = strings.Replace(excludedTerms, c, " ", -1)
	}

	if terms == "" && excludedTerms == "" {
		// we've already confirmed that we have a channel or user to search for
		searchQuery = strings.Replace(searchQuery, "SEARCH_CLAUSE", "", 1)
	} else if s.DriverName() == model.DATABASE_DRIVER_POSTGRES {
		// Parse text for wildcards
		if wildcard, err := regexp.Compile(`\*($| )`); err == nil {
			terms = wildcard.ReplaceAllLiteralString(terms, ":* ")
			excludedTerms = wildcard.ReplaceAllLiteralString(excludedTerms, ":* ")
		}

		excludeClause := ""
		if excludedTerms != "" {
			excludeClause = " & !(" + strings.Join(strings.Fields(excludedTerms), " | ") + ")"
		}

		if params.OrTerms {
			queryParams["Terms"] = "(" + strings.Join(strings.Fields(terms), " | ") + ")" + excludeClause
		} else {
			queryParams["Terms"] = "(" + strings.Join(strings.Fields(terms), " & ") + ")" + excludeClause
		}

		searchClause := fmt.Sprintf("AND to_tsvector('english', %s) @@  to_tsquery('english', :Terms)", searchType)
		searchQuery = strings.Replace(searchQuery, "SEARCH_CLAUSE", searchClause, 1)
	} else if s.DriverName() == model.DATABASE_DRIVER_MYSQL {
		searchClause := fmt.Sprintf("AND MATCH (%s) AGAINST (:Terms IN BOOLEAN MODE)", searchType)
		searchQuery = strings.Replace(searchQuery, "SEARCH_CLAUSE", searchClause, 1)

		excludeClause := ""
		if excludedTerms != "" {
			excludeClause = " -(" + excludedTerms + ")"
		}

		if params.OrTerms {
			queryParams["Terms"] = terms + excludeClause
		} else {
			splitTerms := []string{}
			for _, t := range strings.Fields(terms) {
				splitTerms = append(splitTerms, "+"+t)
			}
			queryParams["Terms"] = strings.Join(splitTerms, " ") + excludeClause
		}
	}

	_, err := s.GetSearchReplica().Select(&posts, searchQuery, queryParams)
	if err != nil {
		mlog.Warn("Query error searching posts.", mlog.Err(err))
		// Don't return the error to the caller as it is of no use to the user. Instead return an empty set of search results.
	} else {
		for _, p := range posts {
			if searchType == "Hashtags" {
				exactMatch := false
				for _, tag := range strings.Split(p.Hashtags, " ") {
					if termMap[strings.ToUpper(tag)] {
						exactMatch = true
						break
					}
				}
				if !exactMatch {
					continue
				}
			}
			list.AddPost(p)
			list.AddOrder(p.Id)
		}
	}
	list.MakeNonNil()
	return list, nil
}

func (s *SqlPostStore) AnalyticsUserCountsWithPostsByDay(teamId string) (model.AnalyticsRows, *model.AppError) {
	query :=
		`SELECT DISTINCT
		        DATE(FROM_UNIXTIME(Posts.CreateAt / 1000)) AS Name,
		        COUNT(DISTINCT Posts.UserId) AS Value
		FROM Posts`

	if len(teamId) > 0 {
		query += " INNER JOIN Channels ON Posts.ChannelId = Channels.Id AND Channels.TeamId = :TeamId AND"
	} else {
		query += " WHERE"
	}

	query += ` Posts.CreateAt >= :StartTime AND Posts.CreateAt <= :EndTime
		GROUP BY DATE(FROM_UNIXTIME(Posts.CreateAt / 1000))
		ORDER BY Name DESC
		LIMIT 30`

	if s.DriverName() == model.DATABASE_DRIVER_POSTGRES {
		query =
			`SELECT
				TO_CHAR(DATE(TO_TIMESTAMP(Posts.CreateAt / 1000)), 'YYYY-MM-DD') AS Name, COUNT(DISTINCT Posts.UserId) AS Value
			FROM Posts`

		if len(teamId) > 0 {
			query += " INNER JOIN Channels ON Posts.ChannelId = Channels.Id AND Channels.TeamId = :TeamId AND"
		} else {
			query += " WHERE"
		}

		query += ` Posts.CreateAt >= :StartTime AND Posts.CreateAt <= :EndTime
			GROUP BY DATE(TO_TIMESTAMP(Posts.CreateAt / 1000))
			ORDER BY Name DESC
			LIMIT 30`
	}

	end := utils.MillisFromTime(utils.EndOfDay(utils.Yesterday()))
	start := utils.MillisFromTime(utils.StartOfDay(utils.Yesterday().AddDate(0, 0, -31)))

	var rows model.AnalyticsRows
	_, err := s.GetReplica().Select(
		&rows,
		query,
		map[string]interface{}{"TeamId": teamId, "StartTime": start, "EndTime": end})
	if err != nil {
		return nil, model.NewAppError("SqlPostStore.AnalyticsUserCountsWithPostsByDay", "store.sql_post.analytics_user_counts_posts_by_day.app_error", nil, err.Error(), http.StatusInternalServerError)
	}
	return rows, nil
}

func (s *SqlPostStore) AnalyticsPostCountsByDay(options *model.AnalyticsPostCountsOptions) (model.AnalyticsRows, *model.AppError) {

	query :=
		`SELECT
		        DATE(FROM_UNIXTIME(Posts.CreateAt / 1000)) AS Name,
		        COUNT(Posts.Id) AS Value
		    FROM Posts`

	if options.BotsOnly {
		query += " INNER JOIN Bots ON Posts.UserId = Bots.Userid"
	}

	if len(options.TeamId) > 0 {
		query += " INNER JOIN Channels ON Posts.ChannelId = Channels.Id AND Channels.TeamId = :TeamId AND"
	} else {
		query += " WHERE"
	}

	query += ` Posts.CreateAt <= :EndTime
		            AND Posts.CreateAt >= :StartTime
		GROUP BY DATE(FROM_UNIXTIME(Posts.CreateAt / 1000))
		ORDER BY Name DESC
		LIMIT 30`

	if s.DriverName() == model.DATABASE_DRIVER_POSTGRES {
		query =
			`SELECT
				TO_CHAR(DATE(TO_TIMESTAMP(Posts.CreateAt / 1000)), 'YYYY-MM-DD') AS Name, Count(Posts.Id) AS Value
			FROM Posts`

		if options.BotsOnly {
			query += " INNER JOIN Bots ON Posts.UserId = Bots.Userid"
		}

		if len(options.TeamId) > 0 {
			query += " INNER JOIN Channels ON Posts.ChannelId = Channels.Id  AND Channels.TeamId = :TeamId AND"
		} else {
			query += " WHERE"
		}

		query += ` Posts.CreateAt <= :EndTime
			            AND Posts.CreateAt >= :StartTime
			GROUP BY DATE(TO_TIMESTAMP(Posts.CreateAt / 1000))
			ORDER BY Name DESC
			LIMIT 30`
	}

	end := utils.MillisFromTime(utils.EndOfDay(utils.Yesterday()))
	start := utils.MillisFromTime(utils.StartOfDay(utils.Yesterday().AddDate(0, 0, -31)))
	if options.YesterdayOnly {
		start = utils.MillisFromTime(utils.StartOfDay(utils.Yesterday().AddDate(0, 0, -1)))
	}

	var rows model.AnalyticsRows
	_, err := s.GetReplica().Select(
		&rows,
		query,
		map[string]interface{}{"TeamId": options.TeamId, "StartTime": start, "EndTime": end})
	if err != nil {
		return nil, model.NewAppError("SqlPostStore.AnalyticsPostCountsByDay", "store.sql_post.analytics_posts_count_by_day.app_error", nil, err.Error(), http.StatusInternalServerError)
	}
	return rows, nil
}

func (s *SqlPostStore) AnalyticsPostCount(teamId string, mustHaveFile bool, mustHaveHashtag bool) (int64, *model.AppError) {
	query :=
		`SELECT
			COUNT(Posts.Id) AS Value
		FROM
			Posts,
			Channels
		WHERE
			Posts.ChannelId = Channels.Id`

	if len(teamId) > 0 {
		query += " AND Channels.TeamId = :TeamId"
	}

	if mustHaveFile {
		query += " AND (Posts.FileIds != '[]' OR Posts.Filenames != '[]')"
	}

	if mustHaveHashtag {
		query += " AND Posts.Hashtags != ''"
	}

	v, err := s.GetReplica().SelectInt(query, map[string]interface{}{"TeamId": teamId})
	if err != nil {
		return 0, model.NewAppError("SqlPostStore.AnalyticsPostCount", "store.sql_post.analytics_posts_count.app_error", nil, err.Error(), http.StatusInternalServerError)
	}

	return v, nil
}

func (s *SqlPostStore) GetPostsCreatedAt(channelId string, time int64) ([]*model.Post, *model.AppError) {
	query := `SELECT * FROM Posts WHERE CreateAt = :CreateAt AND ChannelId = :ChannelId`

	var posts []*model.Post
	_, err := s.GetReplica().Select(&posts, query, map[string]interface{}{"CreateAt": time, "ChannelId": channelId})

	if err != nil {
		return nil, model.NewAppError("SqlPostStore.GetPostsCreatedAt", "store.sql_post.get_posts_created_att.app_error", nil, "channelId="+channelId+err.Error(), http.StatusInternalServerError)
	}
	return posts, nil
}

func (s *SqlPostStore) GetPostsByIds(postIds []string) ([]*model.Post, *model.AppError) {
	keys, params := MapStringsToQueryParams(postIds, "Post")

	query := `SELECT * FROM Posts WHERE Id IN ` + keys + ` ORDER BY CreateAt DESC`

	var posts []*model.Post
	_, err := s.GetReplica().Select(&posts, query, params)

	if err != nil {
		mlog.Error("Query error getting posts.", mlog.Err(err))
		return nil, model.NewAppError("SqlPostStore.GetPostsByIds", "store.sql_post.get_posts_by_ids.app_error", nil, "", http.StatusInternalServerError)
	}
	return posts, nil
}

func (s *SqlPostStore) GetPostsBatchForIndexing(startTime int64, endTime int64, limit int) ([]*model.PostForIndexing, *model.AppError) {
	var posts []*model.PostForIndexing
	_, err := s.GetSearchReplica().Select(&posts,
		`SELECT
			PostsQuery.*, Channels.TeamId, ParentPosts.CreateAt ParentCreateAt
		FROM (
			SELECT
				*
			FROM
				Posts
			WHERE
				Posts.CreateAt >= :StartTime
			AND
				Posts.CreateAt < :EndTime
			ORDER BY
				CreateAt ASC
			LIMIT
				1000
			)
		AS
			PostsQuery
		LEFT JOIN
			Channels
		ON
			PostsQuery.ChannelId = Channels.Id
		LEFT JOIN
			Posts ParentPosts
		ON
			PostsQuery.RootId = ParentPosts.Id`,
		map[string]interface{}{"StartTime": startTime, "EndTime": endTime, "NumPosts": limit})

	if err != nil {
		return nil, model.NewAppError("SqlPostStore.GetPostContext", "store.sql_post.get_posts_batch_for_indexing.get.app_error", nil, err.Error(), http.StatusInternalServerError)
	}
	return posts, nil
}

func (s *SqlPostStore) PermanentDeleteBatch(endTime int64, limit int64) (int64, *model.AppError) {
	var query string
	if s.DriverName() == "postgres" {
		query = "DELETE from Posts WHERE Id = any (array (SELECT Id FROM Posts WHERE CreateAt < :EndTime LIMIT :Limit))"
	} else {
		query = "DELETE from Posts WHERE CreateAt < :EndTime LIMIT :Limit"
	}

	sqlResult, err := s.GetMaster().Exec(query, map[string]interface{}{"EndTime": endTime, "Limit": limit})
	if err != nil {
		return 0, model.NewAppError("SqlPostStore.PermanentDeleteBatch", "store.sql_post.permanent_delete_batch.app_error", nil, ""+err.Error(), http.StatusInternalServerError)
	}

	rowsAffected, err := sqlResult.RowsAffected()
	if err != nil {
		return 0, model.NewAppError("SqlPostStore.PermanentDeleteBatch", "store.sql_post.permanent_delete_batch.app_error", nil, ""+err.Error(), http.StatusInternalServerError)
	}
	return rowsAffected, nil
}

func (s *SqlPostStore) GetOldest() (*model.Post, *model.AppError) {
	var post model.Post
	err := s.GetReplica().SelectOne(&post, "SELECT * FROM Posts ORDER BY CreateAt LIMIT 1")
	if err != nil {
		return nil, model.NewAppError("SqlPostStore.GetOldest", "store.sql_post.get.app_error", nil, err.Error(), http.StatusNotFound)
	}

	return &post, nil
}

func (s *SqlPostStore) determineMaxPostSize() int {
	var maxPostSizeBytes int32

	if s.DriverName() == model.DATABASE_DRIVER_POSTGRES {
		// The Post.Message column in Postgres has historically been VARCHAR(4000), but
		// may be manually enlarged to support longer posts.
		if err := s.GetReplica().SelectOne(&maxPostSizeBytes, `
			SELECT
				COALESCE(character_maximum_length, 0)
			FROM
				information_schema.columns
			WHERE
				table_name = 'posts'
			AND	column_name = 'message'
		`); err != nil {
			mlog.Error("Unable to determine the maximum supported post size", mlog.Err(err))
		}
	} else if s.DriverName() == model.DATABASE_DRIVER_MYSQL {
		// The Post.Message column in MySQL has historically been TEXT, with a maximum
		// limit of 65535.
		if err := s.GetReplica().SelectOne(&maxPostSizeBytes, `
			SELECT
				COALESCE(CHARACTER_MAXIMUM_LENGTH, 0)
			FROM
				INFORMATION_SCHEMA.COLUMNS
			WHERE
				table_schema = DATABASE()
			AND	table_name = 'Posts'
			AND	column_name = 'Message'
			LIMIT 0, 1
		`); err != nil {
			mlog.Error("Unable to determine the maximum supported post size", mlog.Err(err))
		}
	} else {
		mlog.Warn("No implementation found to determine the maximum supported post size")
	}

	// Assume a worst-case representation of four bytes per rune.
	maxPostSize := int(maxPostSizeBytes) / 4

	// To maintain backwards compatibility, don't yield a maximum post
	// size smaller than the previous limit, even though it wasn't
	// actually possible to store 4000 runes in all cases.
	if maxPostSize < model.POST_MESSAGE_MAX_RUNES_V1 {
		maxPostSize = model.POST_MESSAGE_MAX_RUNES_V1
	}

	mlog.Info("Post.Message has size restrictions", mlog.Int("max_characters", maxPostSize), mlog.Int32("max_bytes", maxPostSizeBytes))

	return maxPostSize
}

// GetMaxPostSize returns the maximum number of runes that may be stored in a post.
func (s *SqlPostStore) GetMaxPostSize() int {
	s.maxPostSizeOnce.Do(func() {
		s.maxPostSizeCached = s.determineMaxPostSize()
	})
	return s.maxPostSizeCached
}

func (s *SqlPostStore) GetParentsForExportAfter(limit int, afterId string) ([]*model.PostForExport, *model.AppError) {
	for {
		var rootIds []string
		_, err := s.GetReplica().Select(&rootIds,
			`SELECT
				Id
			FROM
				Posts
			WHERE
				Id > :AfterId
				AND RootId = ''
				AND DeleteAt = 0
			ORDER BY Id
			LIMIT :Limit`,
			map[string]interface{}{"Limit": limit, "AfterId": afterId})
		if err != nil {
			return nil, model.NewAppError("SqlPostStore.GetAllAfterForExport", "store.sql_post.get_posts.app_error",
				nil, err.Error(), http.StatusInternalServerError)
		}

		var postsForExport []*model.PostForExport
		if len(rootIds) == 0 {
			return postsForExport, nil
		}

		keys, params := MapStringsToQueryParams(rootIds, "PostId")
		_, err = s.GetSearchReplica().Select(&postsForExport, `
			SELECT
				p1.*,
				Users.Username as Username,
				Teams.Name as TeamName,
				Channels.Name as ChannelName
			FROM
				(Select * FROM Posts WHERE Id IN `+keys+`) p1
			INNER JOIN
				Channels ON p1.ChannelId = Channels.Id
			INNER JOIN
				Teams ON Channels.TeamId = Teams.Id
			INNER JOIN
				Users ON p1.UserId = Users.Id
			WHERE
				Channels.DeleteAt = 0
				AND Teams.DeleteAt = 0
			ORDER BY
				p1.Id`,
			params)
		if err != nil {
			return nil, model.NewAppError("SqlPostStore.GetAllAfterForExport", "store.sql_post.get_posts.app_error",
				nil, err.Error(), http.StatusInternalServerError)
		}

		if len(postsForExport) == 0 {
			// All of the posts were in channels or teams that were deleted.
			// Update the afterId and try again.
			afterId = rootIds[len(rootIds)-1]
			continue
		}

		return postsForExport, nil
	}
}

func (s *SqlPostStore) GetRepliesForExport(rootId string) ([]*model.ReplyForExport, *model.AppError) {
	var posts []*model.ReplyForExport
	_, err := s.GetSearchReplica().Select(&posts, `
			SELECT
				Posts.*,
				Users.Username as Username
			FROM
				Posts
			INNER JOIN
				Users ON Posts.UserId = Users.Id
			WHERE
				Posts.RootId = :RootId
				AND Posts.DeleteAt = 0
			ORDER BY
				Posts.Id`,
		map[string]interface{}{"RootId": rootId})

	if err != nil {
		return nil, model.NewAppError("SqlPostStore.GetAllAfterForExport", "store.sql_post.get_posts.app_error", nil, err.Error(), http.StatusInternalServerError)
	}

	return posts, nil
}

func (s *SqlPostStore) GetDirectPostParentsForExportAfter(limit int, afterId string) ([]*model.DirectPostForExport, *model.AppError) {
	query := s.getQueryBuilder().
		Select("p.*", "Users.Username as User").
		From("Posts p").
		Join("Channels ON p.ChannelId = Channels.Id").
		Join("Users ON p.UserId = Users.Id").
		Where(sq.And{
			sq.Gt{"p.Id": afterId},
			sq.Eq{"p.ParentId": string("")},
			sq.Eq{"p.DeleteAt": int(0)},
			sq.Eq{"Channels.DeleteAt": int(0)},
			sq.Eq{"Users.DeleteAt": int(0)},
			sq.Eq{"Channels.Type": []string{"D", "G"}},
		}).
		OrderBy("p.Id").
		Limit(uint64(limit))

	queryString, args, err := query.ToSql()
	if err != nil {
		return nil, model.NewAppError("SqlPostStore.GetDirectPostParentsForExportAfter", "store.sql_post.get_direct_posts.app_error", nil, err.Error(), http.StatusInternalServerError)
	}

	var posts []*model.DirectPostForExport
	if _, err = s.GetReplica().Select(&posts, queryString, args...); err != nil {
		return nil, model.NewAppError("SqlPostStore.GetDirectPostParentsForExportAfter", "store.sql_post.get_direct_posts.app_error", nil, err.Error(), http.StatusInternalServerError)
	}
	var channelIds []string
	for _, post := range posts {
		channelIds = append(channelIds, post.ChannelId)
	}
	query = s.getQueryBuilder().
		Select("u.Username as Username, ChannelId, UserId, cm.Roles as Roles, LastViewedAt, MsgCount, MentionCount, cm.NotifyProps as NotifyProps, LastUpdateAt, SchemeUser, SchemeAdmin, (SchemeGuest IS NOT NULL AND SchemeGuest) as SchemeGuest").
		From("ChannelMembers cm").
		Join("Users u ON ( u.Id = cm.UserId )").
		Where(sq.Eq{
			"cm.ChannelId": channelIds,
		})

	queryString, args, err = query.ToSql()
	if err != nil {
		return nil, model.NewAppError("SqlPostStore.GetDirectPostParentsForExportAfter", "store.sql_post.get_direct_posts.app_error", nil, err.Error(), http.StatusInternalServerError)
	}

	var channelMembers []*model.ChannelMemberForExport
	if _, err := s.GetReplica().Select(&channelMembers, queryString, args...); err != nil {
		return nil, model.NewAppError("SqlPostStore.GetDirectPostParentsForExportAfter", "store.sql_post.get_direct_posts.app_error", nil, err.Error(), http.StatusInternalServerError)
	}

	// Build a map of channels and their posts
	postsChannelMap := make(map[string][]*model.DirectPostForExport)
	for _, post := range posts {
		post.ChannelMembers = &[]string{}
		postsChannelMap[post.ChannelId] = append(postsChannelMap[post.ChannelId], post)
	}

	// Build a map of channels and their members
	channelMembersMap := make(map[string][]string)
	for _, member := range channelMembers {
		channelMembersMap[member.ChannelId] = append(channelMembersMap[member.ChannelId], member.Username)
	}

	// Populate each post ChannelMembers extracting it from the channelMembersMap
	for channelId := range channelMembersMap {
		for _, post := range postsChannelMap[channelId] {
			*post.ChannelMembers = channelMembersMap[channelId]
		}
	}
	return posts, nil
}
