package slack

import (
	"sort"
	"strings"
)

func getDirectChannelNameFromMembers(members []string) string {
	sort.Strings(members)
	return strings.Join(members, "_")
}

func (t *Transformer) CheckIntermediate(maxMessageLength int) {
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

		t.Logger.Debugf("Post: %+v Post.Channel: %+v", post, post.Channel)
		if post.IsDirect && len(post.ChannelMembers) != 0 {
			channelName = getDirectChannelNameFromMembers(post.ChannelMembers)
		}
		postsByChannelName[channelName] = append(postsByChannelName[channelName], post)

		if maxMessageLength > 0 && len(post.Message) > maxMessageLength {
			t.Logger.Warnf("\n-- Post.Message in %s has a text length of %d (max: %d)",
				post.Channel, len(post.Message), maxMessageLength)
		}

		// loop through replies to check max length
		for i, reply := range post.Replies {
			if maxMessageLength > 0 && len(reply.Message) > maxMessageLength {
				t.Logger.Warnf("\n-- Post.Reply %d in %s has a text length of %d (max: %d)",
					i, post.Channel, len(reply.Message), maxMessageLength)
			}
		}

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
