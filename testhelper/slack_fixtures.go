package testhelper

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/mattermost/mmetl/services/slack"
)

// SlackExportBuilder helps construct Slack export ZIP files for testing
type SlackExportBuilder struct {
	channels        []slack.SlackChannel
	privateChannels []slack.SlackChannel
	groupChannels   []slack.SlackChannel
	directChannels  []slack.SlackChannel
	users           []slack.SlackUser
	posts           map[string][]slack.SlackPost // channel name -> posts
	skipValidation  bool                         // skip consistency validation (for testing edge cases)
}

// NewSlackExportBuilder creates a new builder for Slack exports
func NewSlackExportBuilder() *SlackExportBuilder {
	return &SlackExportBuilder{
		channels:        []slack.SlackChannel{},
		privateChannels: []slack.SlackChannel{},
		groupChannels:   []slack.SlackChannel{},
		directChannels:  []slack.SlackChannel{},
		users:           []slack.SlackUser{},
		posts:           make(map[string][]slack.SlackPost),
	}
}

// AddChannel adds a public channel to the export
func (b *SlackExportBuilder) AddChannel(channel slack.SlackChannel) *SlackExportBuilder {
	b.channels = append(b.channels, channel)
	return b
}

// AddPrivateChannel adds a private channel (group) to the export
func (b *SlackExportBuilder) AddPrivateChannel(channel slack.SlackChannel) *SlackExportBuilder {
	b.privateChannels = append(b.privateChannels, channel)
	return b
}

// AddGroupChannel adds a group DM (mpim) to the export
func (b *SlackExportBuilder) AddGroupChannel(channel slack.SlackChannel) *SlackExportBuilder {
	b.groupChannels = append(b.groupChannels, channel)
	return b
}

// AddDirectChannel adds a direct message channel to the export
func (b *SlackExportBuilder) AddDirectChannel(channel slack.SlackChannel) *SlackExportBuilder {
	b.directChannels = append(b.directChannels, channel)
	return b
}

// AddUser adds a user to the export
func (b *SlackExportBuilder) AddUser(user slack.SlackUser) *SlackExportBuilder {
	b.users = append(b.users, user)
	return b
}

// AddPost adds a post to a specific channel
func (b *SlackExportBuilder) AddPost(channelName string, post slack.SlackPost) *SlackExportBuilder {
	if _, ok := b.posts[channelName]; !ok {
		b.posts[channelName] = []slack.SlackPost{}
	}
	b.posts[channelName] = append(b.posts[channelName], post)
	return b
}

// AddPosts adds multiple posts to a specific channel
func (b *SlackExportBuilder) AddPosts(channelName string, posts []slack.SlackPost) *SlackExportBuilder {
	for _, post := range posts {
		b.AddPost(channelName, post)
	}
	return b
}

// SkipValidation disables consistency validation during Build().
// Use this when testing how mmetl handles inconsistent Slack exports
// (e.g., posts from deleted users, channels with missing members).
func (b *SlackExportBuilder) SkipValidation() *SlackExportBuilder {
	b.skipValidation = true
	return b
}

// allChannels returns all channels from all types (public, private, group, direct)
func (b *SlackExportBuilder) allChannels() []slack.SlackChannel {
	all := make([]slack.SlackChannel, 0, len(b.channels)+len(b.privateChannels)+len(b.groupChannels)+len(b.directChannels))
	all = append(all, b.channels...)
	all = append(all, b.privateChannels...)
	all = append(all, b.groupChannels...)
	all = append(all, b.directChannels...)
	return all
}

// validate checks that the export data is internally consistent
func (b *SlackExportBuilder) validate() error {
	// Build lookup maps for quick validation
	userIDs := make(map[string]bool)
	for _, user := range b.users {
		userIDs[user.Id] = true
	}

	allCh := b.allChannels()

	// Build lookup by both name and ID. DM/group channels have no Name in
	// Slack exports, so their posts are stored in directories named by ID.
	channelLookup := make(map[string]bool)
	for _, channel := range allCh {
		if channel.Name != "" {
			channelLookup[channel.Name] = true
		}
		if channel.Id != "" {
			channelLookup[channel.Id] = true
		}
	}

	// Validate channel creators and members reference existing users
	for _, channel := range allCh {
		if channel.Creator != "" && !userIDs[channel.Creator] {
			return fmt.Errorf("channel %q references non-existent creator user %q", channel.Name, channel.Creator)
		}
		for _, memberID := range channel.Members {
			if !userIDs[memberID] {
				return fmt.Errorf("channel %q references non-existent member user %q", channel.Name, memberID)
			}
		}
	}

	// Validate posts reference existing channels and users
	for channelName, posts := range b.posts {
		if !channelLookup[channelName] {
			return fmt.Errorf("posts exist for non-existent channel %q", channelName)
		}
		for i, post := range posts {
			// Only validate User field if it's set (bot messages might use BotId instead)
			if post.User != "" && !userIDs[post.User] {
				return fmt.Errorf("post %d in channel %q references non-existent user %q", i, channelName, post.User)
			}
		}
	}

	return nil
}

// Build creates a ZIP file at the specified path containing the Slack export.
// By default, it validates data consistency before building, returning an error if:
// - A post references a channel that doesn't exist
// - A post references a user that doesn't exist
// - A channel member references a user that doesn't exist
// - A channel creator references a user that doesn't exist
//
// Use SkipValidation() to disable validation when testing edge case handling.
func (b *SlackExportBuilder) Build(outputPath string) error {
	// Validate data consistency before building (unless explicitly skipped)
	if !b.skipValidation {
		if err := b.validate(); err != nil {
			return fmt.Errorf("validation failed: %w", err)
		}
	}

	// Create a temporary directory to build the export structure
	tempDir, err := os.MkdirTemp("", "slack-export-*")
	if err != nil {
		return fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer os.RemoveAll(tempDir)

	// Write channels.json (public channels)
	if err := b.writeJSONFile(tempDir, "channels.json", b.channels); err != nil {
		return err
	}

	// Write groups.json (private channels)
	if len(b.privateChannels) > 0 {
		if err := b.writeJSONFile(tempDir, "groups.json", b.privateChannels); err != nil {
			return err
		}
	}

	// Write mpims.json (group DMs)
	if len(b.groupChannels) > 0 {
		if err := b.writeJSONFile(tempDir, "mpims.json", b.groupChannels); err != nil {
			return err
		}
	}

	// Write dms.json (direct messages)
	if len(b.directChannels) > 0 {
		if err := b.writeJSONFile(tempDir, "dms.json", b.directChannels); err != nil {
			return err
		}
	}

	// Write users.json
	if err := b.writeJSONFile(tempDir, "users.json", b.users); err != nil {
		return err
	}

	// Write posts for each channel in channel-name/date.json format
	for channelName, posts := range b.posts {
		channelDir := filepath.Join(tempDir, channelName)
		// Guard against path traversal via malformed channel names
		if rel, err := filepath.Rel(tempDir, channelDir); err != nil || strings.HasPrefix(rel, "..") {
			return fmt.Errorf("channel name %q results in path traversal", channelName)
		}
		if err := os.MkdirAll(channelDir, 0755); err != nil {
			return fmt.Errorf("failed to create channel dir %s: %w", channelName, err)
		}
		// Use a fixed date for test consistency
		if err := b.writeJSONFile(channelDir, "2025-01-01.json", posts); err != nil {
			return err
		}
	}

	// Create the ZIP file
	return b.createZipFile(outputPath, tempDir)
}

// writeJSONFile writes data as JSON to a file in the given directory
func (b *SlackExportBuilder) writeJSONFile(dir, filename string, data any) error {
	filePath := filepath.Join(dir, filename)
	file, err := os.Create(filePath)
	if err != nil {
		return fmt.Errorf("failed to create %s: %w", filename, err)
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(data); err != nil {
		return fmt.Errorf("failed to encode %s: %w", filename, err)
	}
	return nil
}

// createZipFile creates a ZIP file from the directory contents
func (b *SlackExportBuilder) createZipFile(outputPath, sourceDir string) error {
	zipFile, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("failed to create zip file: %w", err)
	}
	defer zipFile.Close()

	archive := zip.NewWriter(zipFile)
	defer archive.Close()

	return filepath.Walk(sourceDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Skip the root directory
		if path == sourceDir {
			return nil
		}

		// Get relative path for the archive
		relPath, err := filepath.Rel(sourceDir, path)
		if err != nil {
			return err
		}
		// ZIP spec requires forward slashes regardless of OS
		relPath = filepath.ToSlash(relPath)

		if info.IsDir() {
			// For directories, add trailing slash
			_, createErr := archive.Create(relPath + "/")
			return createErr
		}

		// Create file header
		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		header.Name = relPath
		header.Method = zip.Deflate

		writer, err := archive.CreateHeader(header)
		if err != nil {
			return err
		}

		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()

		_, err = io.Copy(writer, file)
		return err
	})
}

// === Convenience builders for common test scenarios ===

// SlackBasicExport creates a simple export with users and channels (no posts)
func SlackBasicExport() *SlackExportBuilder {
	return NewSlackExportBuilder().
		AddUser(slack.SlackUser{
			Id:       "U001",
			Username: "john.doe",
			IsBot:    false,
			Profile: slack.SlackProfile{
				RealName: "John Doe",
				Email:    "john.doe@example.com",
				Title:    "Software Engineer",
			},
			Deleted: false,
		}).
		AddUser(slack.SlackUser{
			Id:       "U002",
			Username: "jane.smith",
			IsBot:    false,
			Profile: slack.SlackProfile{
				RealName: "Jane Smith",
				Email:    "jane.smith@example.com",
				Title:    "Product Manager",
			},
			Deleted: false,
		}).
		AddChannel(slack.SlackChannel{
			Id:      "C001",
			Name:    "general",
			Creator: "U001",
			Created: 1704067200,
			Members: []string{"U001", "U002"},
			Purpose: slack.SlackChannelSub{Value: "Company-wide announcements"},
			Topic:   slack.SlackChannelSub{Value: "Welcome to the team!"},
		}).
		AddChannel(slack.SlackChannel{
			Id:      "C002",
			Name:    "random",
			Creator: "U002",
			Created: 1704070800,
			Members: []string{"U001", "U002"},
			Purpose: slack.SlackChannelSub{Value: "Non-work banter"},
			Topic:   slack.SlackChannelSub{Value: "Water cooler chat"},
		})
}

// ExportWithPosts creates an export with users, channels, and posts
func ExportWithPosts() *SlackExportBuilder {
	return SlackBasicExport().
		AddPost("general", slack.SlackPost{
			User:      "U001",
			Text:      "Hello everyone!",
			TimeStamp: "1704067200.000100",
			Type:      "message",
		}).
		AddPost("general", slack.SlackPost{
			User:      "U002",
			Text:      "Welcome to the team, @john.doe!",
			TimeStamp: "1704067260.000200",
			Type:      "message",
		}).
		AddPost("random", slack.SlackPost{
			User:      "U001",
			Text:      "Anyone up for coffee?",
			TimeStamp: "1704070800.000300",
			Type:      "message",
		})
}

// ExportWithThreads creates an export with threaded conversations
func ExportWithThreads() *SlackExportBuilder {
	return SlackBasicExport().
		AddPost("general", slack.SlackPost{
			User:      "U001",
			Text:      "Let's discuss the new feature",
			TimeStamp: "1704067200.000100",
			ThreadTS:  "1704067200.000100", // Root of thread
			Type:      "message",
		}).
		AddPost("general", slack.SlackPost{
			User:      "U002",
			Text:      "I think we should prioritize performance",
			TimeStamp: "1704067260.000200",
			ThreadTS:  "1704067200.000100", // Reply to thread
			Type:      "message",
		}).
		AddPost("general", slack.SlackPost{
			User:      "U001",
			Text:      "Good point, let's add benchmarks",
			TimeStamp: "1704067320.000300",
			ThreadTS:  "1704067200.000100", // Another reply
			Type:      "message",
		})
}

// ExportWithMentions creates an export with user and channel mentions,
// including pipe-aliased special mentions and W-prefix enterprise Grid user IDs.
func ExportWithMentions() *SlackExportBuilder {
	return SlackBasicExport().
		AddUser(slack.SlackUser{
			Id:       "W003",
			Username: "grid.user",
			IsBot:    false,
			Profile: slack.SlackProfile{
				RealName: "Grid User",
				Email:    "grid.user@example.com",
			},
		}).
		AddPost("general", slack.SlackPost{
			User:      "U001",
			Text:      "Hey <@U002>, can you review my PR?",
			TimeStamp: "1704067200.000100",
			Type:      "message",
		}).
		AddPost("general", slack.SlackPost{
			User:      "U002",
			Text:      "Sure! Also cc <#C002|random> for visibility",
			TimeStamp: "1704067260.000200",
			Type:      "message",
		}).
		AddPost("general", slack.SlackPost{
			User:      "U001",
			Text:      "<!here> important announcement!",
			TimeStamp: "1704067320.000300",
			Type:      "message",
		}).
		AddPost("general", slack.SlackPost{
			User:      "U001",
			Text:      "<!here|here> pipe-aliased here and <!channel|@channel> pipe-aliased channel",
			TimeStamp: "1704067380.000400",
			Type:      "message",
		}).
		AddPost("general", slack.SlackPost{
			User:      "U001",
			Text:      "Hey <@W003> and <@W003|grid.user>, welcome to the team!",
			TimeStamp: "1704067440.000500",
			Type:      "message",
		})
}

// ExportWithDeletedUser creates an export with a deleted user
func ExportWithDeletedUser() *SlackExportBuilder {
	return NewSlackExportBuilder().
		AddUser(slack.SlackUser{
			Id:       "U001",
			Username: "john.doe",
			IsBot:    false,
			Profile: slack.SlackProfile{
				RealName: "John Doe",
				Email:    "john.doe@example.com",
			},
			Deleted: false,
		}).
		AddUser(slack.SlackUser{
			Id:       "U003",
			Username: "deleted.user",
			IsBot:    false,
			Profile: slack.SlackProfile{
				RealName: "Former Employee",
				Email:    "former@example.com",
			},
			Deleted: true,
		}).
		AddChannel(slack.SlackChannel{
			Id:      "C001",
			Name:    "general",
			Creator: "U001",
			Created: 1704067200,
			Members: []string{"U001", "U003"},
			Purpose: slack.SlackChannelSub{Value: "General discussion"},
			Topic:   slack.SlackChannelSub{Value: ""},
		})
}

// ExportWithBots creates an export with regular users and bot users
func ExportWithBots() *SlackExportBuilder {
	return NewSlackExportBuilder().
		AddUser(slack.SlackUser{
			Id:       "U001",
			Username: "john.doe",
			Profile: slack.SlackProfile{
				RealName: "John Doe",
				Email:    "john.doe@example.com",
				Title:    "Software Engineer",
			},
		}).
		AddUser(slack.SlackUser{
			Id:       "U002",
			Username: "deploybot",
			IsBot:    true,
			Profile: slack.SlackProfile{
				BotID:    "B001",
				RealName: "Deploy Bot",
				Title:    "Handles deployments",
			},
		}).
		AddUser(slack.SlackUser{
			Id:       "U003",
			Username: "alertbot",
			IsBot:    true,
			Profile: slack.SlackProfile{
				BotID:    "B002",
				RealName: "Alert Bot",
			},
		}).
		AddChannel(slack.SlackChannel{
			Id:      "C001",
			Name:    "general",
			Creator: "U001",
			Created: 1704067200,
			Members: []string{"U001"},
			Purpose: slack.SlackChannelSub{Value: "General discussion"},
			Topic:   slack.SlackChannelSub{Value: "Welcome!"},
		})
}

// ExportWithBotPosts creates an export with bot users and their posts
func ExportWithBotPosts() *SlackExportBuilder {
	return ExportWithBots().
		AddPost("general", slack.SlackPost{
			User:      "U001",
			Text:      "Starting the deploy",
			TimeStamp: "1704067200.000100",
			Type:      "message",
		}).
		AddPost("general", slack.SlackPost{
			BotId:     "B001",
			Text:      "Deployment started for v2.0.0",
			TimeStamp: "1704067260.000200",
			Type:      "message",
			SubType:   "bot_message",
		}).
		AddPost("general", slack.SlackPost{
			BotId:     "B002",
			Text:      "Alert: CPU usage above 90%",
			TimeStamp: "1704067320.000300",
			Type:      "message",
			SubType:   "bot_message",
		})
}

// ExportWithDeletedBot creates an export with a deleted (deactivated) bot user
func ExportWithDeletedBot() *SlackExportBuilder {
	return NewSlackExportBuilder().
		AddUser(slack.SlackUser{
			Id:       "U001",
			Username: "john.doe",
			Profile: slack.SlackProfile{
				RealName: "John Doe",
				Email:    "john.doe@example.com",
			},
		}).
		AddUser(slack.SlackUser{
			Id:       "U002",
			Username: "oldbot",
			IsBot:    true,
			Deleted:  true,
			Profile: slack.SlackProfile{
				BotID:    "B003",
				RealName: "Old Bot",
			},
		}).
		AddChannel(slack.SlackChannel{
			Id:      "C001",
			Name:    "general",
			Creator: "U001",
			Created: 1704067200,
			Members: []string{"U001"},
		})
}

// ExportWithArchivedChannels creates an export containing both active and archived channels.
// The archived channel has is_archived=true with an updated timestamp.
func ExportWithArchivedChannels() *SlackExportBuilder {
	return NewSlackExportBuilder().
		AddUser(slack.SlackUser{
			Id:       "U001",
			Username: "john.doe",
			Profile: slack.SlackProfile{
				RealName: "John Doe",
				Email:    "john.doe@example.com",
				Title:    "Software Engineer",
			},
		}).
		AddChannel(slack.SlackChannel{
			Id:         "C001",
			Name:       "general",
			Creator:    "U001",
			Members:    []string{"U001"},
			Purpose:    slack.SlackChannelSub{Value: "General discussion"},
			Topic:      slack.SlackChannelSub{Value: "Welcome!"},
			IsArchived: false,
		}).
		AddChannel(slack.SlackChannel{
			Id:         "C002",
			Name:       "old-project",
			Creator:    "U001",
			Members:    []string{"U001"},
			Purpose:    slack.SlackChannelSub{Value: "Old project channel"},
			Topic:      slack.SlackChannelSub{Value: ""},
			IsArchived: true,
			Updated:    1620000000000, // ms timestamp used as archive time
		})
}

// ExportWithDirectMessages creates an export with two users, public channels with
// posts, and a direct message channel with posts. Used to verify that last_viewed_at
// is set correctly on both regular channel members and DM participants after import.
func ExportWithDirectMessages() *SlackExportBuilder {
	return SlackBasicExport().
		AddPost("general", slack.SlackPost{
			User:      "U001",
			Text:      "Hello everyone!",
			TimeStamp: "1704067200.000100",
			Type:      "message",
		}).
		AddPost("general", slack.SlackPost{
			User:      "U002",
			Text:      "Welcome to the team!",
			TimeStamp: "1704067260.000200",
			Type:      "message",
		}).
		AddDirectChannel(slack.SlackChannel{
			Id:      "D001",
			Created: 1704067200,
			Members: []string{"U001", "U002"},
		}).
		AddPost("D001", slack.SlackPost{
			User:      "U001",
			Text:      "Hey, want to grab lunch?",
			TimeStamp: "1704067500.000100",
			Type:      "message",
		}).
		AddPost("D001", slack.SlackPost{
			User:      "U002",
			Text:      "Sure, let's go!",
			TimeStamp: "1704067560.000200",
			Type:      "message",
		})
}
