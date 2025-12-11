package testhelper

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
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

// === Team Management ===

// CreateTeam creates a team in Mattermost and returns the created team
func (th *TestHelper) CreateTeam(name, displayName string) *model.Team {
	team := &model.Team{
		Name:        name,
		DisplayName: displayName,
		Type:        model.TeamOpen,
	}

	createdTeam, _, err := th.Client.CreateTeam(context.Background(), team)
	require.NoError(th.t, err, "failed to create team %s", name)

	return createdTeam
}

// GetTeam fetches a team by name
func (th *TestHelper) GetTeam(name string) (*model.Team, error) {
	team, _, err := th.Client.GetTeamByName(context.Background(), name, "")
	return team, err
}

// GetTeamMembers fetches all members of a team
func (th *TestHelper) GetTeamMembers(teamID string) ([]*model.TeamMember, error) {
	members, _, err := th.Client.GetTeamMembers(context.Background(), teamID, 0, 1000, "")
	return members, err
}

// AddUserToTeam adds a user to a team
func (th *TestHelper) AddUserToTeam(teamID, userID string) *model.TeamMember {
	member, _, err := th.Client.AddTeamMember(context.Background(), teamID, userID)
	require.NoError(th.t, err, "failed to add user %s to team %s", userID, teamID)
	return member
}

// === Channel Management ===

// GetChannel fetches a channel by team ID and channel name
func (th *TestHelper) GetChannel(teamID, channelName string) (*model.Channel, error) {
	channel, _, err := th.Client.GetChannelByName(context.Background(), channelName, teamID, "")
	return channel, err
}

// GetChannelByNameForTeamName fetches a channel by team name and channel name
func (th *TestHelper) GetChannelByNameForTeamName(teamName, channelName string) (*model.Channel, error) {
	channel, _, err := th.Client.GetChannelByNameForTeamName(context.Background(), teamName, channelName, "")
	return channel, err
}

// GetChannelPosts fetches posts from a channel
func (th *TestHelper) GetChannelPosts(channelID string, page, perPage int) (*model.PostList, error) {
	posts, _, err := th.Client.GetPostsForChannel(context.Background(), channelID, page, perPage, "", false, false)
	return posts, err
}

// GetChannelMembers fetches members of a channel
func (th *TestHelper) GetChannelMembers(channelID string) (model.ChannelMembers, error) {
	members, _, err := th.Client.GetChannelMembers(context.Background(), channelID, 0, 1000, "")
	return members, err
}

// === Bulk Import via API (simulates mmctl import) ===

// UploadImportFile uploads an import file to Mattermost and returns the upload ID
// This simulates: mmctl import upload <file>
func (th *TestHelper) UploadImportFile(filePath string) (string, error) {
	ctx := context.Background()

	// Open the file
	file, err := os.Open(filePath)
	if err != nil {
		return "", fmt.Errorf("failed to open import file: %w", err)
	}
	defer file.Close()

	// Get file info for size
	fileInfo, err := file.Stat()
	if err != nil {
		return "", fmt.Errorf("failed to stat import file: %w", err)
	}

	// Create multipart form
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	// Add the file
	part, err := writer.CreateFormFile("file", filepath.Base(filePath))
	if err != nil {
		return "", fmt.Errorf("failed to create form file: %w", err)
	}

	_, err = io.Copy(part, file)
	if err != nil {
		return "", fmt.Errorf("failed to copy file content: %w", err)
	}

	// Add file size field
	err = writer.WriteField("filesize", fmt.Sprintf("%d", fileInfo.Size()))
	if err != nil {
		return "", fmt.Errorf("failed to write filesize field: %w", err)
	}

	err = writer.Close()
	if err != nil {
		return "", fmt.Errorf("failed to close multipart writer: %w", err)
	}

	// Create the request
	url := fmt.Sprintf("%s/api/v4/imports", th.SiteURL)
	req, err := http.NewRequestWithContext(ctx, "POST", url, body)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+th.Client.AuthToken)

	// Send the request
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to upload import file: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("upload failed with status %d: %s", resp.StatusCode, string(respBody))
	}

	// The response contains the upload info
	// For now, return the filename as the upload ID
	return filepath.Base(filePath), nil
}

// ProcessImport starts processing an uploaded import file
// This simulates: mmctl import process <upload_id>
func (th *TestHelper) ProcessImport(uploadID string) (string, error) {
	ctx := context.Background()

	// Create the request to start the import job
	url := fmt.Sprintf("%s/api/v4/imports/%s/process", th.SiteURL, uploadID)
	req, err := http.NewRequestWithContext(ctx, "POST", url, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+th.Client.AuthToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to start import process: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("process import failed with status %d: %s", resp.StatusCode, string(respBody))
	}

	// Return a job ID placeholder - the actual implementation would parse the response
	return uploadID, nil
}

// WaitForImportJob waits for an import job to complete
func (th *TestHelper) WaitForImportJob(jobID string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting for import job %s", jobID)
		case <-ticker.C:
			// Check job status
			jobs, _, err := th.Client.GetJobs(ctx, 0, 100)
			if err != nil {
				th.t.Logf("Error checking job status: %v", err)
				continue
			}

			for _, job := range jobs {
				if job.Type == "import_process" {
					switch job.Status {
					case model.JobStatusSuccess:
						th.t.Logf("Import job completed successfully")
						return nil
					case model.JobStatusError:
						return fmt.Errorf("import job failed: %s", job.Data["error"])
					case model.JobStatusCanceled:
						return fmt.Errorf("import job was canceled")
					default:
						th.t.Logf("Import job status: %s", job.Status)
					}
				}
			}
		}
	}
}

// ImportBulkData is a convenience method that uploads, processes, and waits for an import
// This simulates the full: mmctl import upload <file> && mmctl import process <id>
func (th *TestHelper) ImportBulkData(filePath string) error {
	th.t.Logf("Uploading import file: %s", filePath)

	uploadID, err := th.UploadImportFile(filePath)
	if err != nil {
		return fmt.Errorf("failed to upload import file: %w", err)
	}
	th.t.Logf("Upload complete, ID: %s", uploadID)

	jobID, err := th.ProcessImport(uploadID)
	if err != nil {
		return fmt.Errorf("failed to start import process: %w", err)
	}
	th.t.Logf("Import process started, job ID: %s", jobID)

	err = th.WaitForImportJob(jobID, 5*time.Minute)
	if err != nil {
		return fmt.Errorf("import job failed: %w", err)
	}

	return nil
}

// === Verification Helpers ===

// AssertUserExists verifies that a user exists with the given username
func (th *TestHelper) AssertUserExists(username string) *model.User {
	user, err := th.GetUserByUsername(username)
	require.NoError(th.t, err, "user %s should exist", username)
	require.NotNil(th.t, user, "user %s should not be nil", username)
	return user
}

// AssertTeamExists verifies that a team exists with the given name
func (th *TestHelper) AssertTeamExists(teamName string) *model.Team {
	team, err := th.GetTeam(teamName)
	require.NoError(th.t, err, "team %s should exist", teamName)
	require.NotNil(th.t, team, "team %s should not be nil", teamName)
	return team
}

// AssertChannelExists verifies that a channel exists in a team
func (th *TestHelper) AssertChannelExists(teamName, channelName string) *model.Channel {
	channel, err := th.GetChannelByNameForTeamName(teamName, channelName)
	require.NoError(th.t, err, "channel %s in team %s should exist", channelName, teamName)
	require.NotNil(th.t, channel, "channel %s should not be nil", channelName)
	return channel
}

// AssertUserInTeam verifies that a user is a member of a team
func (th *TestHelper) AssertUserInTeam(teamID, userID string) {
	members, err := th.GetTeamMembers(teamID)
	require.NoError(th.t, err, "failed to get team members")

	found := false
	for _, member := range members {
		if member.UserId == userID {
			found = true
			break
		}
	}
	require.True(th.t, found, "user %s should be a member of team %s", userID, teamID)
}
