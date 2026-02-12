package testhelper

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/v8/cmd/mmctl/commands/importer"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/exec"
)

// TestHelper provides helper functions and containers for integration testing
type TestHelper struct {
	t         *testing.T
	tearDowns []TearDownFunc

	SiteURL             string
	Client              *model.Client4
	MattermostContainer testcontainers.Container

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
	mattermostContainer, siteURL, mattermostTearDown, err := CreateMattermostContainer(ctx, dockerNet.Name)
	require.NoError(t, err, "failed to create mattermost container")
	th.tearDowns = append(th.tearDowns, mattermostTearDown)
	th.MattermostContainer = mattermostContainer
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

// === Bulk Import via mmctl ===

// ImportBulkData imports a JSONL file into Mattermost using the real mmctl binary.
// This uses the actual mmctl import process command to ensure we're testing the documented behavior.
func (th *TestHelper) ImportBulkData(filePath string) error {
	th.t.Logf("Importing bulk data from: %s", filePath)

	ctx := context.Background()

	// Wrap JSONL in a zip if needed (mmctl requires zip files)
	importPath := filePath
	var tempZip string
	if !isZipFile(filePath) {
		th.t.Log("Wrapping JSONL in zip archive for mmctl import...")
		var err error
		tempZip, err = wrapJSONLInZip(filePath)
		if err != nil {
			return fmt.Errorf("failed to wrap JSONL in zip: %w", err)
		}
		defer os.Remove(tempZip)
		importPath = tempZip
	}

	// Copy the import file to the container
	containerPath := "/tmp/import.zip"
	th.t.Logf("Copying import file to container at %s", containerPath)
	err := th.MattermostContainer.CopyFileToContainer(ctx, importPath, containerPath, 0644)
	if err != nil {
		return fmt.Errorf("failed to copy import file to container: %w", err)
	}

	// Execute mmctl import process command inside the container
	th.t.Log("Executing mmctl import process inside container...")
	cmd := []string{"/mattermost/bin/mmctl", "import", "process", containerPath, "--local", "--bypass-upload"}

	exitCode, reader, err := th.MattermostContainer.Exec(ctx, cmd, exec.Multiplexed())
	if err != nil {
		return fmt.Errorf("failed to execute mmctl command: %w", err)
	}

	// Read the output
	output, err := io.ReadAll(reader)
	if err != nil {
		return fmt.Errorf("failed to read mmctl output: %w", err)
	}

	th.t.Logf("mmctl output:\n%s", string(output))

	if exitCode != 0 {
		return fmt.Errorf("mmctl import failed with exit code %d: %s", exitCode, string(output))
	}

	// Extract job ID from output (format: "Import process job successfully created, ID: <job_id>")
	jobID := extractJobID(string(output))
	if jobID == "" {
		return fmt.Errorf("failed to extract job ID from mmctl output: %s", string(output))
	}

	th.t.Logf("Import job created with ID: %s, waiting for completion...", jobID)

	// Poll the job status until it completes
	err = th.waitForImportJobCompletion(ctx, jobID)
	if err != nil {
		return fmt.Errorf("import job failed: %w", err)
	}

	th.t.Log("Import completed successfully via mmctl")
	return nil
}

// extractJobID extracts the job ID from mmctl import process output
func extractJobID(output string) string {
	// Expected format: "Import process job successfully created, ID: <job_id>"
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		if strings.Contains(line, "Import process job successfully created") {
			parts := strings.Split(line, "ID: ")
			if len(parts) == 2 {
				return strings.TrimSpace(parts[1])
			}
		}
	}
	return ""
}

// waitForImportJobCompletion polls the import job status until it completes or fails
func (th *TestHelper) waitForImportJobCompletion(ctx context.Context, jobID string) error {
	maxAttempts := 60 // 60 attempts with 1 second sleep = 1 minute max wait
	for i := 0; i < maxAttempts; i++ {
		// Execute mmctl import job show command
		cmd := []string{"/mattermost/bin/mmctl", "import", "job", "show", jobID, "--local"}
		exitCode, reader, err := th.MattermostContainer.Exec(ctx, cmd, exec.Multiplexed())
		if err != nil {
			return fmt.Errorf("failed to check job status: %w", err)
		}

		output, err := io.ReadAll(reader)
		if err != nil {
			return fmt.Errorf("failed to read job status output: %w", err)
		}

		if exitCode != 0 {
			return fmt.Errorf("failed to get job status (exit code %d): %s", exitCode, string(output))
		}

		statusOutput := string(output)

		// Check if job is complete (Status: success or Status: error)
		if strings.Contains(statusOutput, "Status: success") {
			th.t.Logf("Import job %s completed successfully", jobID)
			return nil
		}

		if strings.Contains(statusOutput, "Status: error") || strings.Contains(statusOutput, "Status: canceled") {
			return fmt.Errorf("import job failed with status: %s", statusOutput)
		}

		// Job is still in progress (Status: pending or Status: in_progress)
		th.t.Logf("Import job %s still in progress, waiting... (attempt %d/%d)", jobID, i+1, maxAttempts)
		time.Sleep(1 * time.Second)
	}

	return fmt.Errorf("import job %s did not complete within timeout", jobID)
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
	// First get the team by name
	team, err := th.GetTeam(teamName)
	require.NoError(th.t, err, "team %s should exist", teamName)
	require.NotNil(th.t, team, "team %s should not be nil", teamName)

	// Then get the channel by name and team ID
	channel, err := th.GetChannel(team.Id, channelName)
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

// === Import File Validation using mmctl importer package ===

// ValidationResult contains the results of validating an import file.
// This wraps the mmctl importer.Validator to provide a consistent interface.
type ValidationResult struct {
	Valid           bool                              `json:"valid"`
	Errors          []*importer.ImportValidationError `json:"errors"`
	LineCount       uint64                            `json:"line_count"`
	UserCount       uint64                            `json:"user_count"`
	ChannelCount    uint64                            `json:"channel_count"`
	PostCount       uint64                            `json:"post_count"`
	DirectPostCount uint64                            `json:"direct_post_count"`
	TeamCount       uint64                            `json:"team_count"`
	EmojiCount      uint64                            `json:"emoji_count"`
	Duration        time.Duration                     `json:"duration"`
}

// ValidateImportFile validates a Mattermost bulk import file (JSONL or zip archive).
// This uses the same validation logic as `mmctl import validate`.
// If the file is a raw JSONL file (not a zip), it will be automatically wrapped in a
// temporary zip archive for validation.
func ValidateImportFile(filePath string) (*ValidationResult, error) {
	// Check if the file is a zip or needs to be wrapped
	archivePath := filePath
	var tempZip string

	if !isZipFile(filePath) {
		// Wrap JSONL in a temporary zip
		var err error
		tempZip, err = wrapJSONLInZip(filePath)
		if err != nil {
			return nil, fmt.Errorf("failed to wrap JSONL in zip: %w", err)
		}
		defer os.Remove(tempZip)
		archivePath = tempZip
	}

	// Create validator with default settings
	// - ignoreAttachments: true (we don't check attachment files)
	// - createMissingTeams: true (don't fail on missing teams, they may be created)
	// - checkServerDuplicates: false (no server to check against)
	// - empty maps for server entities (not checking against a live server)
	// - maxPostSize: 65535 (default Mattermost limit)
	validator := importer.NewValidator(
		archivePath,
		true,  // ignoreAttachments
		true,  // createMissingTeams
		false, // checkServerDuplicates
		nil,   // serverTeams
		nil,   // serverChannels
		nil,   // serverUsers
		nil,   // serverEmails
		65535, // maxPostSize
	)

	result := &ValidationResult{
		Valid:  true,
		Errors: []*importer.ImportValidationError{},
	}

	// Collect errors during validation
	validator.OnError(func(err *importer.ImportValidationError) error {
		result.Valid = false
		result.Errors = append(result.Errors, err)
		return nil // Continue validation to collect all errors
	})

	// Run validation
	if err := validator.Validate(); err != nil {
		return nil, fmt.Errorf("validation failed: %w", err)
	}

	// Populate counts from validator
	result.LineCount = validator.Lines()
	result.UserCount = validator.UserCount()
	result.ChannelCount = validator.ChannelCount()
	result.PostCount = validator.PostCount()
	result.DirectPostCount = validator.DirectPostCount()
	result.TeamCount = validator.TeamCount()
	result.EmojiCount = validator.Emojis()
	result.Duration = validator.Duration()

	return result, nil
}

// isZipFile checks if a file is a valid ZIP archive by reading its magic bytes
func isZipFile(filePath string) bool {
	file, err := os.Open(filePath)
	if err != nil {
		return false
	}
	defer file.Close()

	// ZIP files start with PK (0x50 0x4B)
	header := make([]byte, 4)
	_, err = file.Read(header)
	if err != nil {
		return false
	}

	return header[0] == 0x50 && header[1] == 0x4B
}

// wrapJSONLInZip creates a temporary zip archive containing the JSONL file
// The JSONL file is added as "import.jsonl" inside the archive
func wrapJSONLInZip(jsonlPath string) (string, error) {
	// Create a temporary zip file
	tempFile, err := os.CreateTemp("", "import-*.zip")
	if err != nil {
		return "", fmt.Errorf("failed to create temp file: %w", err)
	}
	tempPath := tempFile.Name()

	// Create zip writer
	zipWriter := zip.NewWriter(tempFile)

	// Open the JSONL file
	jsonlFile, err := os.Open(jsonlPath)
	if err != nil {
		tempFile.Close()
		os.Remove(tempPath)
		return "", fmt.Errorf("failed to open JSONL file: %w", err)
	}
	defer jsonlFile.Close()

	// Get file info for the header
	info, err := jsonlFile.Stat()
	if err != nil {
		tempFile.Close()
		os.Remove(tempPath)
		return "", fmt.Errorf("failed to stat JSONL file: %w", err)
	}

	// Create file header - use the original filename or "import.jsonl"
	header, err := zip.FileInfoHeader(info)
	if err != nil {
		tempFile.Close()
		os.Remove(tempPath)
		return "", fmt.Errorf("failed to create zip header: %w", err)
	}
	header.Name = filepath.Base(jsonlPath)
	if !strings.HasSuffix(header.Name, ".jsonl") {
		header.Name = "import.jsonl"
	}
	header.Method = zip.Deflate

	// Write the file to the zip
	writer, err := zipWriter.CreateHeader(header)
	if err != nil {
		tempFile.Close()
		os.Remove(tempPath)
		return "", fmt.Errorf("failed to create zip entry: %w", err)
	}

	_, err = io.Copy(writer, jsonlFile)
	if err != nil {
		tempFile.Close()
		os.Remove(tempPath)
		return "", fmt.Errorf("failed to write to zip: %w", err)
	}

	// Close the zip writer and temp file
	if err := zipWriter.Close(); err != nil {
		tempFile.Close()
		os.Remove(tempPath)
		return "", fmt.Errorf("failed to close zip writer: %w", err)
	}

	if err := tempFile.Close(); err != nil {
		os.Remove(tempPath)
		return "", fmt.Errorf("failed to close temp file: %w", err)
	}

	return tempPath, nil
}

// ValidateImportFileOrFail validates an import file and fails the test if invalid.
// Uses the same validation as `mmctl import validate`.
func (th *TestHelper) ValidateImportFileOrFail(filePath string) *ValidationResult {
	result, err := ValidateImportFile(filePath)
	require.NoError(th.t, err, "failed to validate import file")

	if !result.Valid {
		var errMsgs []string
		for _, e := range result.Errors {
			errMsgs = append(errMsgs, e.Error())
		}
		require.Fail(th.t, "import file validation failed", strings.Join(errMsgs, "\n"))
	}

	th.t.Logf("Import file validated (mmctl importer): %d lines, %d users, %d channels, %d posts, %d direct posts (took %v)",
		result.LineCount, result.UserCount, result.ChannelCount, result.PostCount, result.DirectPostCount, result.Duration)

	return result
}
