package testhelper

import (
	"context"
	"testing"
	"time"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/stretchr/testify/require"
)

// TestHelper provides helper functions and containers for integration testing
type TestHelper struct {
	t         *testing.T
	tearDowns []TearDownFunc

	SiteURL string
	Client  *model.Client4

	// Admin user created during setup
	AdminUser     *model.User
	AdminPassword string
}

// SetupHelper initializes PostgreSQL and Mattermost containers for integration testing
func SetupHelper(t *testing.T) *TestHelper {
	th := &TestHelper{
		t:             t,
		tearDowns:     make([]TearDownFunc, 0),
		AdminPassword: "testpassword123",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Create Docker network for container communication
	dockerNet, networkTearDown, err := CreateTestNetwork(ctx)
	require.NoError(t, err, "failed to create docker network")
	th.tearDowns = append(th.tearDowns, networkTearDown)
	t.Logf("Docker network created: %s", dockerNet.Name)

	// Create PostgreSQL container
	_, postgresConnStr, postgresTearDown, err := CreatePostgresContainer(ctx, dockerNet.Name)
	require.NoError(t, err, "failed to create postgres container")
	th.tearDowns = append(th.tearDowns, postgresTearDown)
	t.Logf("PostgreSQL started with connection string: %s", postgresConnStr)

	// Create Mattermost container
	siteURL, mattermostTearDown, err := CreateMattermostContainer(ctx, dockerNet.Name)
	require.NoError(t, err, "failed to create mattermost container")
	th.tearDowns = append(th.tearDowns, mattermostTearDown)
	th.SiteURL = siteURL
	t.Logf("Mattermost started at: %s", siteURL)

	// Create API client
	th.Client = model.NewAPIv4Client(siteURL)

	// Create initial admin user
	th.setupAdminUser()

	return th
}

// setupAdminUser creates the initial admin user and logs in
func (th *TestHelper) setupAdminUser() {
	// Create admin user
	adminUser := &model.User{
		Email:    "admin@test.local",
		Username: "admin",
		Password: th.AdminPassword,
	}

	createdUser, _, err := th.Client.CreateUser(context.Background(), adminUser)
	require.NoError(th.t, err, "failed to create admin user")
	th.AdminUser = createdUser

	// Login as admin
	_, _, err = th.Client.Login(context.Background(), adminUser.Email, th.AdminPassword)
	require.NoError(th.t, err, "failed to login as admin user")

	// Make user a system admin
	_, err = th.Client.UpdateUserRoles(context.Background(), createdUser.Id, "system_admin system_user")
	require.NoError(th.t, err, "failed to make user system admin")

	// Re-login to get admin permissions
	_, _, err = th.Client.Login(context.Background(), adminUser.Email, th.AdminPassword)
	require.NoError(th.t, err, "failed to re-login as admin user")
}

// TearDown cleans up all containers
func (th *TestHelper) TearDown() {
	ctx := context.Background()
	// Tear down in reverse order
	for i := len(th.tearDowns) - 1; i >= 0; i-- {
		if err := th.tearDowns[i](ctx); err != nil {
			th.t.Logf("Error during teardown: %v", err)
		}
	}
}

// CreateUser creates a user in Mattermost and returns the created user
func (th *TestHelper) CreateUser(username, email string) *model.User {
	user := &model.User{
		Email:    email,
		Username: username,
		Password: "testpassword123",
	}

	createdUser, _, err := th.Client.CreateUser(context.Background(), user)
	require.NoError(th.t, err, "failed to create user %s", username)

	return createdUser
}

// CreateUserWithPassword creates a user with a specific password
func (th *TestHelper) CreateUserWithPassword(username, email, password string) *model.User {
	user := &model.User{
		Email:    email,
		Username: username,
		Password: password,
	}

	createdUser, _, err := th.Client.CreateUser(context.Background(), user)
	require.NoError(th.t, err, "failed to create user %s", username)

	return createdUser
}

// DeactivateUser deactivates a user (sets DeleteAt)
func (th *TestHelper) DeactivateUser(userID string) {
	_, err := th.Client.DeleteUser(context.Background(), userID)
	require.NoError(th.t, err, "failed to deactivate user %s", userID)
}

// GetUserByUsername fetches a user by username
func (th *TestHelper) GetUserByUsername(username string) (*model.User, error) {
	user, _, err := th.Client.GetUserByUsername(context.Background(), username, "")
	return user, err
}

// GetUserByEmail fetches a user by email
func (th *TestHelper) GetUserByEmail(email string) (*model.User, error) {
	user, _, err := th.Client.GetUserByEmail(context.Background(), email, "")
	return user, err
}

// GetAPIClient returns a new API client for the Mattermost instance
// This can be used to create clients with different authentication
func (th *TestHelper) GetAPIClient() *model.Client4 {
	client := model.NewAPIv4Client(th.SiteURL)
	// Login as admin
	_, _, err := client.Login(context.Background(), th.AdminUser.Email, th.AdminPassword)
	require.NoError(th.t, err, "failed to login API client")
	return client
}
