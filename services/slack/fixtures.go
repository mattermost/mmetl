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

// Build creates a ZIP file at the specified path containing the Slack export
func (b *SlackExportBuilder) Build(outputPath string) error {
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
func (b *SlackExportBuilder) writeJSONFile(dir, filename string, data interface{}) error {
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
			_, err := archive.Create(relPath + "/")
			return err
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
