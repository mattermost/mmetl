package testhelper

import (
	"archive/zip"
	"bufio"
	"context"
	"encoding/json"
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

// === Bulk Import via REST API ===
// Since the /api/v4/imports endpoint is Enterprise-only, we simulate bulk import
// by parsing the JSONL file and creating entities via the standard REST API.

// ImportLine represents a line in a Mattermost bulk import JSONL file
type ImportLine struct {
	Type    string          `json:"type"`
	Version *int            `json:"version,omitempty"`
	Team    *ImportTeamData `json:"team,omitempty"`
	User    *ImportUser     `json:"user,omitempty"`
	Channel *ImportChannel  `json:"channel,omitempty"`
	Post    *ImportPost     `json:"post,omitempty"`
}

type ImportTeamData struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name,omitempty"`
	Type        string `json:"type,omitempty"`
}

type ImportUser struct {
	Username  string       `json:"username"`
	Email     string       `json:"email"`
	Password  string       `json:"password,omitempty"`
	FirstName string       `json:"first_name,omitempty"`
	LastName  string       `json:"last_name,omitempty"`
	Position  string       `json:"position,omitempty"`
	Roles     string       `json:"roles,omitempty"`
	DeleteAt  int64        `json:"delete_at,omitempty"`
	Teams     []ImportTeam `json:"teams,omitempty"`
}

type ImportTeam struct {
	Name     string                `json:"name"`
	Roles    string                `json:"roles,omitempty"`
	Channels []ImportChannelMember `json:"channels,omitempty"`
}

type ImportChannelMember struct {
	Name  string `json:"name"`
	Roles string `json:"roles,omitempty"`
}

type ImportChannel struct {
	Team        string `json:"team"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name,omitempty"`
	Type        string `json:"type"`
	Header      string `json:"header,omitempty"`
	Purpose     string `json:"purpose,omitempty"`
}

type ImportPost struct {
	Team     string        `json:"team"`
	Channel  string        `json:"channel"`
	User     string        `json:"user"`
	Message  string        `json:"message"`
	CreateAt int64         `json:"create_at,omitempty"`
	Replies  []ImportReply `json:"replies,omitempty"`
}

type ImportReply struct {
	User     string `json:"user"`
	Message  string `json:"message"`
	CreateAt int64  `json:"create_at,omitempty"`
}

// ImportBulkData reads a JSONL file and creates entities in Mattermost via the REST API.
// This simulates what mmctl import does, but using the standard API available in Team Edition.
func (th *TestHelper) ImportBulkData(filePath string) error {
	th.t.Logf("Importing bulk data from: %s", filePath)

	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("failed to open import file: %w", err)
	}
	defer file.Close()

	// Parse all lines first
	var lines []ImportLine
	scanner := bufio.NewScanner(file)
	// Increase buffer for large lines
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	for scanner.Scan() {
		var line ImportLine
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			return fmt.Errorf("failed to parse import line: %w", err)
		}
		lines = append(lines, line)
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("failed to read import file: %w", err)
	}

	// Create a map to track created users (username -> user)
	createdUsers := make(map[string]*model.User)
	// Map team name -> team
	teams := make(map[string]*model.Team)
	// Map "teamName:channelName" -> channel
	channels := make(map[string]*model.Channel)

	ctx := context.Background()

	// First pass: create teams
	for _, line := range lines {
		if line.Type == "team" && line.Team != nil {
			team, err := th.importTeam(ctx, line.Team, teams)
			if err != nil {
				th.t.Logf("Warning: failed to import team %s: %v", line.Team.Name, err)
			} else {
				teams[team.Name] = team
			}
		}
	}

	// Second pass: create users
	for _, line := range lines {
		if line.Type == "user" && line.User != nil {
			user, err := th.importUser(ctx, line.User, teams, channels)
			if err != nil {
				th.t.Logf("Warning: failed to import user %s: %v", line.User.Username, err)
				continue
			}
			createdUsers[user.Username] = user
		}
	}

	// Third pass: create channels
	for _, line := range lines {
		if line.Type == "channel" && line.Channel != nil {
			_, err := th.importChannel(ctx, line.Channel, teams, channels)
			if err != nil {
				th.t.Logf("Warning: failed to import channel %s: %v", line.Channel.Name, err)
			}
		}
	}

	// Fourth pass: add users to teams and channels based on their team memberships
	for _, line := range lines {
		if line.Type == "user" && line.User != nil {
			user := createdUsers[line.User.Username]
			if user == nil {
				continue
			}
			for _, teamMembership := range line.User.Teams {
				team := teams[teamMembership.Name]
				if team == nil {
					th.t.Logf("Warning: team %s not found for user %s", teamMembership.Name, line.User.Username)
					continue
				}
				// Add user to team
				_, _, err := th.Client.AddTeamMember(ctx, team.Id, user.Id)
				if err != nil && !isAlreadyMemberError(err) {
					th.t.Logf("Warning: failed to add user %s to team %s: %v", user.Username, team.Name, err)
				}
				// Add user to channels
				for _, channelMembership := range teamMembership.Channels {
					channelKey := fmt.Sprintf("%s:%s", teamMembership.Name, channelMembership.Name)
					channel := channels[channelKey]
					if channel == nil {
						th.t.Logf("Warning: channel %s not found in team %s", channelMembership.Name, teamMembership.Name)
						continue
					}
					_, _, err := th.Client.AddChannelMember(ctx, channel.Id, user.Id)
					if err != nil && !isAlreadyMemberError(err) {
						th.t.Logf("Warning: failed to add user %s to channel %s: %v", user.Username, channel.Name, err)
					}
				}
			}
		}
	}

	// Fourth pass: create posts
	for _, line := range lines {
		if line.Type == "post" && line.Post != nil {
			err := th.importPost(ctx, line.Post, teams, channels, createdUsers)
			if err != nil {
				th.t.Logf("Warning: failed to import post: %v", err)
			}
		}
	}

	th.t.Logf("Import complete: %d users, %d channels", len(createdUsers), len(channels))
	return nil
}

func isAlreadyMemberError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return strings.Contains(errStr, "already") || strings.Contains(errStr, "member")
}

func (th *TestHelper) importTeam(ctx context.Context, importTeam *ImportTeamData, teams map[string]*model.Team) (*model.Team, error) {
	// Check if team already exists
	existingTeam, _, err := th.Client.GetTeamByName(ctx, importTeam.Name, "")
	if err == nil && existingTeam != nil {
		th.t.Logf("Team %s already exists", importTeam.Name)
		return existingTeam, nil
	}

	displayName := importTeam.DisplayName
	if displayName == "" {
		displayName = importTeam.Name
	}

	teamType := importTeam.Type
	if teamType == "" {
		teamType = "O" // Default to open team
	}

	team := &model.Team{
		Name:        importTeam.Name,
		DisplayName: displayName,
		Type:        teamType,
	}

	createdTeam, _, err := th.Client.CreateTeam(ctx, team)
	if err != nil {
		return nil, fmt.Errorf("failed to create team: %w", err)
	}

	th.t.Logf("Created team: %s (%s) type=%s", createdTeam.Name, createdTeam.DisplayName, createdTeam.Type)
	return createdTeam, nil
}

func (th *TestHelper) importUser(ctx context.Context, importUser *ImportUser, teams map[string]*model.Team, channels map[string]*model.Channel) (*model.User, error) {
	// Check if user already exists
	existingUser, _, err := th.Client.GetUserByUsername(ctx, importUser.Username, "")
	if err == nil && existingUser != nil {
		th.t.Logf("User %s already exists", importUser.Username)
		return existingUser, nil
	}

	password := importUser.Password
	if password == "" {
		password = "testpassword123"
	}

	user := &model.User{
		Username:  importUser.Username,
		Email:     importUser.Email,
		Password:  password,
		FirstName: importUser.FirstName,
		LastName:  importUser.LastName,
		Position:  importUser.Position,
	}

	createdUser, _, err := th.Client.CreateUser(ctx, user)
	if err != nil {
		return nil, fmt.Errorf("failed to create user: %w", err)
	}

	th.t.Logf("Created user: %s (%s)", createdUser.Username, createdUser.Email)

	// If user should be deactivated
	if importUser.DeleteAt > 0 {
		_, err = th.Client.DeleteUser(ctx, createdUser.Id)
		if err != nil {
			th.t.Logf("Warning: failed to deactivate user %s: %v", createdUser.Username, err)
		}
		// Fetch updated user
		createdUser, _, _ = th.Client.GetUser(ctx, createdUser.Id, "")
	}

	return createdUser, nil
}

func (th *TestHelper) importChannel(ctx context.Context, importChannel *ImportChannel, teams map[string]*model.Team, channels map[string]*model.Channel) (*model.Channel, error) {
	// Get or cache team
	team := teams[importChannel.Team]
	if team == nil {
		var err error
		team, _, err = th.Client.GetTeamByName(ctx, importChannel.Team, "")
		if err != nil {
			return nil, fmt.Errorf("team %s not found: %w", importChannel.Team, err)
		}
		teams[importChannel.Team] = team
	}

	channelKey := fmt.Sprintf("%s:%s", importChannel.Team, importChannel.Name)

	// Check if channel already exists
	existingChannel, _, err := th.Client.GetChannelByName(ctx, importChannel.Name, team.Id, "")
	if err == nil && existingChannel != nil {
		channels[channelKey] = existingChannel
		return existingChannel, nil
	}

	channelType := model.ChannelTypeOpen
	if importChannel.Type == "P" {
		channelType = model.ChannelTypePrivate
	}

	displayName := importChannel.DisplayName
	if displayName == "" {
		displayName = importChannel.Name
	}

	channel := &model.Channel{
		TeamId:      team.Id,
		Name:        importChannel.Name,
		DisplayName: displayName,
		Type:        channelType,
		Header:      importChannel.Header,
		Purpose:     importChannel.Purpose,
	}

	createdChannel, _, err := th.Client.CreateChannel(ctx, channel)
	if err != nil {
		return nil, fmt.Errorf("failed to create channel: %w", err)
	}

	channels[channelKey] = createdChannel
	th.t.Logf("Created channel: %s in team %s", createdChannel.Name, team.Name)
	return createdChannel, nil
}

func (th *TestHelper) importPost(ctx context.Context, importPost *ImportPost, teams map[string]*model.Team, channels map[string]*model.Channel, users map[string]*model.User) error {
	channelKey := fmt.Sprintf("%s:%s", importPost.Team, importPost.Channel)
	channel := channels[channelKey]
	if channel == nil {
		// Try to fetch it
		team := teams[importPost.Team]
		if team == nil {
			return fmt.Errorf("team %s not found for post", importPost.Team)
		}
		ch, _, err := th.Client.GetChannelByName(ctx, importPost.Channel, team.Id, "")
		if err != nil {
			return fmt.Errorf("channel %s not found in team %s: %w", importPost.Channel, importPost.Team, err)
		}
		channel = ch
		channels[channelKey] = channel
	}

	user := users[importPost.User]
	if user == nil {
		// Try to fetch the user
		u, _, err := th.Client.GetUserByUsername(ctx, importPost.User, "")
		if err != nil {
			return fmt.Errorf("user %s not found: %w", importPost.User, err)
		}
		user = u
		users[importPost.User] = user
	}

	// We need to create the post as the user, but we're logged in as admin
	// For testing purposes, we'll create the post as admin but with the user's ID
	// This is a limitation - in a real import, the post would be attributed correctly
	post := &model.Post{
		ChannelId: channel.Id,
		Message:   importPost.Message,
		UserId:    user.Id,
	}

	if importPost.CreateAt > 0 {
		post.CreateAt = importPost.CreateAt
	}

	createdPost, _, err := th.Client.CreatePost(ctx, post)
	if err != nil {
		return fmt.Errorf("failed to create post: %w", err)
	}

	// Handle replies
	for _, reply := range importPost.Replies {
		replyUser := users[reply.User]
		if replyUser == nil {
			replyUser, _, _ = th.Client.GetUserByUsername(ctx, reply.User, "")
			if replyUser != nil {
				users[reply.User] = replyUser
			}
		}
		if replyUser == nil {
			th.t.Logf("Warning: reply user %s not found", reply.User)
			continue
		}

		replyPost := &model.Post{
			ChannelId: channel.Id,
			RootId:    createdPost.Id,
			Message:   reply.Message,
			UserId:    replyUser.Id,
		}
		if reply.CreateAt > 0 {
			replyPost.CreateAt = reply.CreateAt
		}
		_, _, err := th.Client.CreatePost(ctx, replyPost)
		if err != nil {
			th.t.Logf("Warning: failed to create reply: %v", err)
		}
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
