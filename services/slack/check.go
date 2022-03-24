package slack

import (
	"sort"
	"strings"
)

func getDirectChannelNameFromMembers(members []string) string {
	sort.Strings(members)
	return strings.Join(members, "_")
}

func (t *Transformer) CheckIntermediate() {
	t.Logger.Info("Checking intermediate resources")

	// create channels index
	channelsByName := map[string]*IntermediateChannel{}
	for _, channel := range t.Intermediate.PublicChannels {
		if _, ok := channelsByName[channel.Name]; ok {
			t.Logger.Warnf("WARNING -- Duplicate public channel name: %s", channel.Name)
			continue
		}
		channelsByName[channel.Name] = channel
	}

	for _, channel := range t.Intermediate.PrivateChannels {
		if _, ok := channelsByName[channel.Name]; ok {
			t.Logger.Warnf("WARNING -- Duplicate private channel name: %s", channel.Name)
			continue
		}
		channelsByName[channel.Name] = channel
	}

	// create direct channels index
	for _, channel := range t.Intermediate.GroupChannels {
		channelName := getDirectChannelNameFromMembers(channel.Members)
		if _, ok := channelsByName[channelName]; ok {
			t.Logger.Warnf("WARNING -- Duplicate group channel name: %s", channelName)
			continue
		}
		channelsByName[channelName] = channel
	}

	for _, channel := range t.Intermediate.DirectChannels {
		channelName := getDirectChannelNameFromMembers(channel.Members)
		if _, ok := channelsByName[channelName]; ok {
			t.Logger.Warnf("WARNING -- Duplicate direct channel name: %s", channelName)
			continue
		}
		channelsByName[channelName] = channel
	}

	// create post index
	postsByChannelName := map[string][]*IntermediatePost{}
	for _, post := range t.Intermediate.Posts {
		channelName := post.Channel
		if post.IsDirect && len(post.ChannelMembers) != 0 {
			channelName = getDirectChannelNameFromMembers(post.ChannelMembers)
		}
		postsByChannelName[channelName] = append(postsByChannelName[channelName], post)
	}

	visitedChannels := map[string]bool{}
	for channelName, channel := range channelsByName {
		visitedChannels[channelName] = true
		usernames := []string{}
		for _, member := range channel.Members {
			if user, ok := t.Intermediate.UsersById[member]; !ok {
				t.Logger.Warnf("-- Invalid member: %s", member)
			} else {
				usernames = append(usernames, user.Username)
			}
		}
		t.Logger.Debugf("Channel: \"%s\" Type: \"%s\" Post count: %d Members: \"%s\"", channelName, channel.Type, len(postsByChannelName[channelName]), strings.Join(usernames, ", "))
	}

	for channelName, posts := range postsByChannelName {
		if _, ok := visitedChannels[channelName]; !ok {
			t.Logger.Warnf("-- Channel %s has %d posts but not a channel", channelName, len(posts))
		}
	}
}
