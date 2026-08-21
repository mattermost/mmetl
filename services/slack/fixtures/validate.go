package fixtures

import (
	"fmt"

	"github.com/mattermost/mmetl/services/slack"
)

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
