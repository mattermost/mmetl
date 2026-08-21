package fixtures

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

	// Official Slack exports include this at the archive root; transform precheck requires it.
	if err := b.writeJSONFile(tempDir, "integration_logs.json", []any{}); err != nil {
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
