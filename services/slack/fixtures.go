package slack

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// SlackExportBuilder helps construct Slack export ZIP files for testing
type SlackExportBuilder struct {
	channels        []SlackChannel
	privateChannels []SlackChannel
	groupChannels   []SlackChannel
	directChannels  []SlackChannel
	users           []SlackUser
	posts           map[string][]SlackPost // channel name -> posts
	skipValidation  bool                   // skip consistency validation (for testing edge cases)
}

// NewSlackExportBuilder creates a new builder for Slack exports
func NewSlackExportBuilder() *SlackExportBuilder {
	return &SlackExportBuilder{
		channels:        []SlackChannel{},
		privateChannels: []SlackChannel{},
		groupChannels:   []SlackChannel{},
		directChannels:  []SlackChannel{},
		users:           []SlackUser{},
		posts:           make(map[string][]SlackPost),
	}
}

// AddChannel adds a public channel to the export
func (b *SlackExportBuilder) AddChannel(channel SlackChannel) *SlackExportBuilder {
	b.channels = append(b.channels, channel)
	return b
}

// AddPrivateChannel adds a private channel (group) to the export
func (b *SlackExportBuilder) AddPrivateChannel(channel SlackChannel) *SlackExportBuilder {
	b.privateChannels = append(b.privateChannels, channel)
	return b
}

// AddGroupChannel adds a group DM (mpim) to the export
func (b *SlackExportBuilder) AddGroupChannel(channel SlackChannel) *SlackExportBuilder {
	b.groupChannels = append(b.groupChannels, channel)
	return b
}

// AddDirectChannel adds a direct message channel to the export
func (b *SlackExportBuilder) AddDirectChannel(channel SlackChannel) *SlackExportBuilder {
	b.directChannels = append(b.directChannels, channel)
	return b
}

// AddUser adds a user to the export
func (b *SlackExportBuilder) AddUser(user SlackUser) *SlackExportBuilder {
	b.users = append(b.users, user)
	return b
}

// AddPost adds a post to a specific channel
func (b *SlackExportBuilder) AddPost(channelName string, post SlackPost) *SlackExportBuilder {
	if _, ok := b.posts[channelName]; !ok {
		b.posts[channelName] = []SlackPost{}
	}
	b.posts[channelName] = append(b.posts[channelName], post)
	return b
}

// AddPosts adds multiple posts to a specific channel
func (b *SlackExportBuilder) AddPosts(channelName string, posts []SlackPost) *SlackExportBuilder {
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
func (b *SlackExportBuilder) allChannels() []SlackChannel {
	all := make([]SlackChannel, 0, len(b.channels)+len(b.privateChannels)+len(b.groupChannels)+len(b.directChannels))
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

	channelNames := make(map[string]bool)
	for _, channel := range b.allChannels() {
		channelNames[channel.Name] = true
	}

	// Validate channel creators and members reference existing users
	for _, channel := range b.allChannels() {
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
		if !channelNames[channelName] {
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

// BasicExport creates a simple export with users and channels (no posts)
func BasicExport() *SlackExportBuilder {
	return NewSlackExportBuilder().
		AddUser(SlackUser{
			Id:       "U001",
			Username: "john.doe",
			IsBot:    false,
			Profile: SlackProfile{
				RealName: "John Doe",
				Email:    "john.doe@example.com",
				Title:    "Software Engineer",
			},
			Deleted: false,
		}).
		AddUser(SlackUser{
			Id:       "U002",
			Username: "jane.smith",
			IsBot:    false,
			Profile: SlackProfile{
				RealName: "Jane Smith",
				Email:    "jane.smith@example.com",
				Title:    "Product Manager",
			},
			Deleted: false,
		}).
		AddChannel(SlackChannel{
			Id:      "C001",
			Name:    "general",
			Creator: "U001",
			Members: []string{"U001", "U002"},
			Purpose: SlackChannelSub{Value: "Company-wide announcements"},
			Topic:   SlackChannelSub{Value: "Welcome to the team!"},
		}).
		AddChannel(SlackChannel{
			Id:      "C002",
			Name:    "random",
			Creator: "U002",
			Members: []string{"U001", "U002"},
			Purpose: SlackChannelSub{Value: "Non-work banter"},
			Topic:   SlackChannelSub{Value: "Water cooler chat"},
		})
}

// ExportWithPosts creates an export with users, channels, and posts
func ExportWithPosts() *SlackExportBuilder {
	return BasicExport().
		AddPost("general", SlackPost{
			User:      "U001",
			Text:      "Hello everyone!",
			TimeStamp: "1704067200.000100",
			Type:      "message",
		}).
		AddPost("general", SlackPost{
			User:      "U002",
			Text:      "Welcome to the team, @john.doe!",
			TimeStamp: "1704067260.000200",
			Type:      "message",
		}).
		AddPost("random", SlackPost{
			User:      "U001",
			Text:      "Anyone up for coffee?",
			TimeStamp: "1704070800.000300",
			Type:      "message",
		})
}

// ExportWithThreads creates an export with threaded conversations
func ExportWithThreads() *SlackExportBuilder {
	return BasicExport().
		AddPost("general", SlackPost{
			User:      "U001",
			Text:      "Let's discuss the new feature",
			TimeStamp: "1704067200.000100",
			ThreadTS:  "1704067200.000100", // Root of thread
			Type:      "message",
		}).
		AddPost("general", SlackPost{
			User:      "U002",
			Text:      "I think we should prioritize performance",
			TimeStamp: "1704067260.000200",
			ThreadTS:  "1704067200.000100", // Reply to thread
			Type:      "message",
		}).
		AddPost("general", SlackPost{
			User:      "U001",
			Text:      "Good point, let's add benchmarks",
			TimeStamp: "1704067320.000300",
			ThreadTS:  "1704067200.000100", // Another reply
			Type:      "message",
		})
}

// ExportWithMentions creates an export with user and channel mentions
func ExportWithMentions() *SlackExportBuilder {
	return BasicExport().
		AddPost("general", SlackPost{
			User:      "U001",
			Text:      "Hey <@U002>, can you review my PR?",
			TimeStamp: "1704067200.000100",
			Type:      "message",
		}).
		AddPost("general", SlackPost{
			User:      "U002",
			Text:      "Sure! Also cc <#C002|random> for visibility",
			TimeStamp: "1704067260.000200",
			Type:      "message",
		}).
		AddPost("general", SlackPost{
			User:      "U001",
			Text:      "<!here> important announcement!",
			TimeStamp: "1704067320.000300",
			Type:      "message",
		})
}

// ExportWithDeletedUser creates an export with a deleted user
func ExportWithDeletedUser() *SlackExportBuilder {
	return NewSlackExportBuilder().
		AddUser(SlackUser{
			Id:       "U001",
			Username: "john.doe",
			IsBot:    false,
			Profile: SlackProfile{
				RealName: "John Doe",
				Email:    "john.doe@example.com",
			},
			Deleted: false,
		}).
		AddUser(SlackUser{
			Id:       "U003",
			Username: "deleted.user",
			IsBot:    false,
			Profile: SlackProfile{
				RealName: "Former Employee",
				Email:    "former@example.com",
			},
			Deleted: true,
		}).
		AddChannel(SlackChannel{
			Id:      "C001",
			Name:    "general",
			Creator: "U001",
			Members: []string{"U001", "U003"},
			Purpose: SlackChannelSub{Value: "General discussion"},
			Topic:   SlackChannelSub{Value: ""},
		})
}
