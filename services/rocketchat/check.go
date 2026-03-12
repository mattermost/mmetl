package rocketchat

import (
	"sort"
	"strings"

	"github.com/mattermost/mmetl/services/intermediate"
)

// CheckIntermediate performs basic validation of the intermediate data,
// logging warnings for inconsistencies such as duplicate channel names,
// posts referencing missing channels, and channels with invalid members.
func (t *Transformer) CheckIntermediate() {
	t.Logger.Info("Checking intermediate resources")

	// Build channel index (name → channel) for public and private channels.
	channelsByName := map[string]*intermediate.IntermediateChannel{}
	for _, ch := range t.Intermediate.PublicChannels {
		if _, ok := channelsByName[ch.Name]; ok {
			t.Logger.Warnf("Duplicate public channel name: %s", ch.Name)
			continue
		}
		channelsByName[ch.Name] = ch
	}
	for _, ch := range t.Intermediate.PrivateChannels {
		if _, ok := channelsByName[ch.Name]; ok {
			t.Logger.Warnf("Duplicate private channel name: %s", ch.Name)
			continue
		}
		channelsByName[ch.Name] = ch
	}

	// Build DM channel name from sorted members (using usernames, matching
	// the names stored on posts' ChannelMembers).
	getDirectName := func(members []string) string {
		sorted := append([]string{}, members...)
		sort.Strings(sorted)
		return strings.Join(sorted, "_")
	}

	for _, ch := range t.Intermediate.GroupChannels {
		name := getDirectName(ch.MembersUsernames)
		if _, ok := channelsByName[name]; ok {
			t.Logger.Warnf("Duplicate group channel: %s", name)
			continue
		}
		channelsByName[name] = ch
	}
	for _, ch := range t.Intermediate.DirectChannels {
		name := getDirectName(ch.MembersUsernames)
		if _, ok := channelsByName[name]; ok {
			t.Logger.Warnf("Duplicate direct channel: %s", name)
			continue
		}
		channelsByName[name] = ch
	}

	// Validate named-channel members exist in UsersById.
	for _, ch := range append(t.Intermediate.PublicChannels, t.Intermediate.PrivateChannels...) {
		for _, memberID := range ch.Members {
			if _, ok := t.Intermediate.UsersById[memberID]; !ok {
				t.Logger.Warnf("Channel %s has member %s not found in users", ch.Name, memberID)
			}
		}
	}

	// Build post-by-channel index.
	postsByChannelName := map[string][]*intermediate.IntermediatePost{}
	for _, post := range t.Intermediate.Posts {
		channelName := post.Channel
		if post.IsDirect && len(post.ChannelMembers) != 0 {
			channelName = getDirectName(post.ChannelMembers)
		}
		postsByChannelName[channelName] = append(postsByChannelName[channelName], post)
	}

	// Per-channel debug summary: type, post count, resolved member usernames.
	visitedChannels := map[string]bool{}
	for channelName, ch := range channelsByName {
		visitedChannels[channelName] = true

		usernames := []string{}
		for _, memberID := range ch.Members {
			if user, ok := t.Intermediate.UsersById[memberID]; ok {
				usernames = append(usernames, user.Username)
			} else {
				t.Logger.Warnf("-- Invalid member: %s", memberID)
			}
		}

		t.Logger.Debugf("Channel: %q Type: %q Post count: %d Members: %q",
			channelName, ch.Type, len(postsByChannelName[channelName]), strings.Join(usernames, ", "))
	}

	// Detect posts whose channel was not found in the channel index.
	for channelName, posts := range postsByChannelName {
		if !visitedChannels[channelName] {
			t.Logger.Warnf("-- Channel %s has %d posts but is not a known channel", channelName, len(posts))
		}
	}

	// Count bot users so the operator knows whether --bot-owner is needed.
	botCount := 0
	for _, u := range t.Intermediate.UsersById {
		if u.IsBot {
			botCount++
		}
	}
	if botCount > 0 {
		t.Logger.Infof("Found %d bot user(s). You will need to provide the --bot-owner flag when running the transform command.", botCount)
	}

	t.Logger.Infof("Check complete: %d users (%d bots), %d public channels, %d private channels, %d direct/group channels, %d posts",
		len(t.Intermediate.UsersById),
		botCount,
		len(t.Intermediate.PublicChannels),
		len(t.Intermediate.PrivateChannels),
		len(t.Intermediate.DirectChannels)+len(t.Intermediate.GroupChannels),
		len(t.Intermediate.Posts),
	)
}
