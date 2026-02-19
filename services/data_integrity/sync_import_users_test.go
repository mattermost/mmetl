package data_integrity

import (
	"context"
	"io"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/v8/channels/app/imports"
	log "github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockMattermostClient is a mock implementation of the MattermostClient interface
type mockMattermostClient struct {
	getUserByUsernameFunc func(ctx context.Context, username, etag string) (*model.User, *model.Response, error)
	getUserByEmailFunc    func(ctx context.Context, email, etag string) (*model.User, *model.Response, error)
}

func (m *mockMattermostClient) GetUserByUsername(ctx context.Context, username, etag string) (*model.User, *model.Response, error) {
	if m.getUserByUsernameFunc != nil {
		return m.getUserByUsernameFunc(ctx, username, etag)
	}
	return nil, &model.Response{StatusCode: 404}, &model.AppError{StatusCode: 404}
}

func (m *mockMattermostClient) GetUserByEmail(ctx context.Context, email, etag string) (*model.User, *model.Response, error) {
	if m.getUserByEmailFunc != nil {
		return m.getUserByEmailFunc(ctx, email, etag)
	}
	return nil, &model.Response{StatusCode: 404}, &model.AppError{StatusCode: 404}
}

func TestRemoveDuplicateChannelMemberships(t *testing.T) {
	logger := log.New()
	logger.SetOutput(io.Discard)
	flags := SyncImportUsersFlags{}

	t.Run("user with no teams", func(t *testing.T) {
		username := "testuser"
		user := &imports.UserImportData{
			Username: &username,
		}
		removeDuplicateChannelMemberships(user, flags, logger)
		assert.Nil(t, user.Teams)
	})

	t.Run("user with empty teams slice", func(t *testing.T) {
		username := "testuser"
		teams := []imports.UserTeamImportData{}
		user := &imports.UserImportData{
			Username: &username,
			Teams:    &teams,
		}
		removeDuplicateChannelMemberships(user, flags, logger)
		assert.Empty(t, *user.Teams)
	})

	t.Run("user with no duplicate channel memberships", func(t *testing.T) {
		username := "testuser"
		channel1 := "channel1"
		channel2 := "channel2"
		channels := []imports.UserChannelImportData{
			{Name: &channel1},
			{Name: &channel2},
		}
		teams := []imports.UserTeamImportData{
			{Channels: &channels},
		}
		user := &imports.UserImportData{
			Username: &username,
			Teams:    &teams,
		}
		removeDuplicateChannelMemberships(user, flags, logger)
		require.NotNil(t, user.Teams)
		require.Len(t, *user.Teams, 1)
		require.NotNil(t, (*user.Teams)[0].Channels)
		assert.Len(t, *(*user.Teams)[0].Channels, 2)
	})

	t.Run("user with duplicate channel memberships", func(t *testing.T) {
		username := "testuser"
		channel1 := "channel1"
		channel2 := "channel2"
		channels := []imports.UserChannelImportData{
			{Name: &channel1},
			{Name: &channel2},
			{Name: &channel1}, // duplicate
		}
		teams := []imports.UserTeamImportData{
			{Channels: &channels},
		}
		user := &imports.UserImportData{
			Username: &username,
			Teams:    &teams,
		}
		removeDuplicateChannelMemberships(user, flags, logger)
		require.NotNil(t, user.Teams)
		require.Len(t, *user.Teams, 1)
		require.NotNil(t, (*user.Teams)[0].Channels)
		assert.Len(t, *(*user.Teams)[0].Channels, 2)
		assert.Equal(t, "channel1", *(*(*user.Teams)[0].Channels)[0].Name)
		assert.Equal(t, "channel2", *(*(*user.Teams)[0].Channels)[1].Name)
	})

	t.Run("user with multiple duplicate channel memberships", func(t *testing.T) {
		username := "testuser"
		channel1 := "channel1"
		channel2 := "channel2"
		channel3 := "channel3"
		channels := []imports.UserChannelImportData{
			{Name: &channel1},
			{Name: &channel2},
			{Name: &channel1}, // duplicate
			{Name: &channel3},
			{Name: &channel2}, // duplicate
			{Name: &channel1}, // duplicate
		}
		teams := []imports.UserTeamImportData{
			{Channels: &channels},
		}
		user := &imports.UserImportData{
			Username: &username,
			Teams:    &teams,
		}
		removeDuplicateChannelMemberships(user, flags, logger)
		require.NotNil(t, user.Teams)
		require.Len(t, *user.Teams, 1)
		require.NotNil(t, (*user.Teams)[0].Channels)
		assert.Len(t, *(*user.Teams)[0].Channels, 3)
	})
}

func TestMergeImportFileUser(t *testing.T) {
	logger := log.New()
	logger.SetOutput(io.Discard)
	flags := SyncImportUsersFlags{}
	ctx := context.Background()

	t.Run("user does not exist in database", func(t *testing.T) {
		username := "newuser"
		email := "newuser@example.com"
		user := &imports.UserImportData{
			Username: &username,
			Email:    &email,
		}

		client := &mockMattermostClient{
			getUserByUsernameFunc: func(ctx context.Context, username, etag string) (*model.User, *model.Response, error) {
				return nil, &model.Response{StatusCode: 404}, &model.AppError{StatusCode: 404}
			},
			getUserByEmailFunc: func(ctx context.Context, email, etag string) (*model.User, *model.Response, error) {
				return nil, &model.Response{StatusCode: 404}, &model.AppError{StatusCode: 404}
			},
		}

		usernameChanged, emailChanged, err := mergeImportFileUser(ctx, user, flags, client, logger)
		require.NoError(t, err)
		assert.False(t, usernameChanged)
		assert.False(t, emailChanged)
		assert.Equal(t, "newuser", *user.Username)
		assert.Equal(t, "newuser@example.com", *user.Email)
	})

	t.Run("username exists with same email", func(t *testing.T) {
		username := "existinguser"
		email := "existinguser@example.com"
		user := &imports.UserImportData{
			Username: &username,
			Email:    &email,
		}

		client := &mockMattermostClient{
			getUserByUsernameFunc: func(ctx context.Context, username, etag string) (*model.User, *model.Response, error) {
				return &model.User{
					Id:       "user123",
					Username: "existinguser",
					Email:    "existinguser@example.com",
				}, &model.Response{StatusCode: 200}, nil
			},
			getUserByEmailFunc: func(ctx context.Context, email, etag string) (*model.User, *model.Response, error) {
				return &model.User{
					Id:       "user123",
					Username: "existinguser",
					Email:    "existinguser@example.com",
				}, &model.Response{StatusCode: 200}, nil
			},
		}

		usernameChanged, emailChanged, err := mergeImportFileUser(ctx, user, flags, client, logger)
		require.NoError(t, err)
		assert.False(t, usernameChanged)
		assert.False(t, emailChanged)
		assert.Equal(t, "existinguser", *user.Username)
		assert.Equal(t, "existinguser@example.com", *user.Email)
	})

	t.Run("username exists with different email", func(t *testing.T) {
		username := "existinguser"
		email := "newemail@example.com"
		user := &imports.UserImportData{
			Username: &username,
			Email:    &email,
		}

		client := &mockMattermostClient{
			getUserByUsernameFunc: func(ctx context.Context, username, etag string) (*model.User, *model.Response, error) {
				return &model.User{
					Id:       "user123",
					Username: "existinguser",
					Email:    "oldemail@example.com",
				}, &model.Response{StatusCode: 200}, nil
			},
			getUserByEmailFunc: func(ctx context.Context, email, etag string) (*model.User, *model.Response, error) {
				return nil, &model.Response{StatusCode: 404}, &model.AppError{StatusCode: 404}
			},
		}

		usernameChanged, emailChanged, err := mergeImportFileUser(ctx, user, flags, client, logger)
		require.NoError(t, err)
		assert.False(t, usernameChanged)
		assert.True(t, emailChanged)
		assert.Equal(t, "existinguser", *user.Username)
		assert.Equal(t, "oldemail@example.com", *user.Email)
	})

	t.Run("email exists with different username", func(t *testing.T) {
		username := "newusername"
		email := "existingemail@example.com"
		user := &imports.UserImportData{
			Username: &username,
			Email:    &email,
		}

		client := &mockMattermostClient{
			getUserByUsernameFunc: func(ctx context.Context, username, etag string) (*model.User, *model.Response, error) {
				return nil, &model.Response{StatusCode: 404}, &model.AppError{StatusCode: 404}
			},
			getUserByEmailFunc: func(ctx context.Context, email, etag string) (*model.User, *model.Response, error) {
				return &model.User{
					Id:       "user456",
					Username: "oldusername",
					Email:    "existingemail@example.com",
				}, &model.Response{StatusCode: 200}, nil
			},
		}

		usernameChanged, emailChanged, err := mergeImportFileUser(ctx, user, flags, client, logger)
		require.NoError(t, err)
		assert.True(t, usernameChanged)
		assert.False(t, emailChanged)
		assert.Equal(t, "oldusername", *user.Username)
		assert.Equal(t, "existingemail@example.com", *user.Email)
	})

	t.Run("duplicate users - both active - prefer email match", func(t *testing.T) {
		username := "conflictuser"
		email := "conflict@example.com"
		user := &imports.UserImportData{
			Username: &username,
			Email:    &email,
		}

		client := &mockMattermostClient{
			getUserByUsernameFunc: func(ctx context.Context, username, etag string) (*model.User, *model.Response, error) {
				return &model.User{
					Id:       "user123",
					Username: "conflictuser",
					Email:    "different@example.com",
					DeleteAt: 0, // active
				}, &model.Response{StatusCode: 200}, nil
			},
			getUserByEmailFunc: func(ctx context.Context, email, etag string) (*model.User, *model.Response, error) {
				return &model.User{
					Id:       "user456",
					Username: "differentuser",
					Email:    "conflict@example.com",
					DeleteAt: 0, // active
				}, &model.Response{StatusCode: 200}, nil
			},
		}

		usernameChanged, emailChanged, err := mergeImportFileUser(ctx, user, flags, client, logger)
		require.NoError(t, err)
		assert.True(t, usernameChanged)
		assert.False(t, emailChanged)
		assert.Equal(t, "differentuser", *user.Username)
		assert.Equal(t, "conflict@example.com", *user.Email)
	})

	t.Run("duplicate users - username match active, email match inactive", func(t *testing.T) {
		username := "conflictuser"
		email := "conflict@example.com"
		user := &imports.UserImportData{
			Username: &username,
			Email:    &email,
		}

		client := &mockMattermostClient{
			getUserByUsernameFunc: func(ctx context.Context, username, etag string) (*model.User, *model.Response, error) {
				return &model.User{
					Id:       "user123",
					Username: "conflictuser",
					Email:    "different@example.com",
					DeleteAt: 0, // active
				}, &model.Response{StatusCode: 200}, nil
			},
			getUserByEmailFunc: func(ctx context.Context, email, etag string) (*model.User, *model.Response, error) {
				return &model.User{
					Id:       "user456",
					Username: "differentuser",
					Email:    "conflict@example.com",
					DeleteAt: 12345, // inactive
				}, &model.Response{StatusCode: 200}, nil
			},
		}

		usernameChanged, emailChanged, err := mergeImportFileUser(ctx, user, flags, client, logger)
		require.NoError(t, err)
		assert.False(t, usernameChanged)
		assert.True(t, emailChanged)
		assert.Equal(t, "conflictuser", *user.Username)
		assert.Equal(t, "different@example.com", *user.Email)
	})

	t.Run("duplicate users - email match active, username match inactive", func(t *testing.T) {
		username := "conflictuser"
		email := "conflict@example.com"
		user := &imports.UserImportData{
			Username: &username,
			Email:    &email,
		}

		client := &mockMattermostClient{
			getUserByUsernameFunc: func(ctx context.Context, username, etag string) (*model.User, *model.Response, error) {
				return &model.User{
					Id:       "user123",
					Username: "conflictuser",
					Email:    "different@example.com",
					DeleteAt: 12345, // inactive
				}, &model.Response{StatusCode: 200}, nil
			},
			getUserByEmailFunc: func(ctx context.Context, email, etag string) (*model.User, *model.Response, error) {
				return &model.User{
					Id:       "user456",
					Username: "differentuser",
					Email:    "conflict@example.com",
					DeleteAt: 0, // active
				}, &model.Response{StatusCode: 200}, nil
			},
		}

		usernameChanged, emailChanged, err := mergeImportFileUser(ctx, user, flags, client, logger)
		require.NoError(t, err)
		assert.True(t, usernameChanged)
		assert.False(t, emailChanged)
		assert.Equal(t, "differentuser", *user.Username)
		assert.Equal(t, "conflict@example.com", *user.Email)
	})

	t.Run("duplicate users - both inactive - prefer email match", func(t *testing.T) {
		username := "conflictuser"
		email := "conflict@example.com"
		user := &imports.UserImportData{
			Username: &username,
			Email:    &email,
		}

		client := &mockMattermostClient{
			getUserByUsernameFunc: func(ctx context.Context, username, etag string) (*model.User, *model.Response, error) {
				return &model.User{
					Id:       "user123",
					Username: "conflictuser",
					Email:    "different@example.com",
					DeleteAt: 12345, // inactive
				}, &model.Response{StatusCode: 200}, nil
			},
			getUserByEmailFunc: func(ctx context.Context, email, etag string) (*model.User, *model.Response, error) {
				return &model.User{
					Id:       "user456",
					Username: "differentuser",
					Email:    "conflict@example.com",
					DeleteAt: 67890, // inactive
				}, &model.Response{StatusCode: 200}, nil
			},
		}

		usernameChanged, emailChanged, err := mergeImportFileUser(ctx, user, flags, client, logger)
		require.NoError(t, err)
		assert.True(t, usernameChanged)
		assert.False(t, emailChanged)
		assert.Equal(t, "differentuser", *user.Username)
		assert.Equal(t, "conflict@example.com", *user.Email)
	})

	t.Run("API error when fetching user by username", func(t *testing.T) {
		username := "testuser"
		email := "test@example.com"
		user := &imports.UserImportData{
			Username: &username,
			Email:    &email,
		}

		client := &mockMattermostClient{
			getUserByUsernameFunc: func(ctx context.Context, username, etag string) (*model.User, *model.Response, error) {
				return nil, nil, &model.AppError{Message: "network error"}
			},
		}

		_, _, err := mergeImportFileUser(ctx, user, flags, client, logger)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "error fetching user by username")
	})

	t.Run("API error when fetching user by email", func(t *testing.T) {
		username := "testuser"
		email := "test@example.com"
		user := &imports.UserImportData{
			Username: &username,
			Email:    &email,
		}

		client := &mockMattermostClient{
			getUserByUsernameFunc: func(ctx context.Context, username, etag string) (*model.User, *model.Response, error) {
				return nil, &model.Response{StatusCode: 404}, &model.AppError{StatusCode: 404}
			},
			getUserByEmailFunc: func(ctx context.Context, email, etag string) (*model.User, *model.Response, error) {
				return nil, nil, &model.AppError{Message: "network error"}
			},
		}

		_, _, err := mergeImportFileUser(ctx, user, flags, client, logger)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "error fetching user by email")
	})

	t.Run("case insensitive username and email matching", func(t *testing.T) {
		username := "TestUser"
		email := "TestUser@Example.COM"
		user := &imports.UserImportData{
			Username: &username,
			Email:    &email,
		}

		client := &mockMattermostClient{
			getUserByUsernameFunc: func(ctx context.Context, username, etag string) (*model.User, *model.Response, error) {
				assert.Equal(t, "testuser", username)
				return &model.User{
					Id:       "user123",
					Username: "testuser",
					Email:    "testuser@example.com",
				}, &model.Response{StatusCode: 200}, nil
			},
			getUserByEmailFunc: func(ctx context.Context, email, etag string) (*model.User, *model.Response, error) {
				assert.Equal(t, "testuser@example.com", email)
				return &model.User{
					Id:       "user123",
					Username: "testuser",
					Email:    "testuser@example.com",
				}, &model.Response{StatusCode: 200}, nil
			},
		}

		usernameChanged, emailChanged, err := mergeImportFileUser(ctx, user, flags, client, logger)
		require.NoError(t, err)
		assert.False(t, usernameChanged)
		assert.False(t, emailChanged)
	})
}
