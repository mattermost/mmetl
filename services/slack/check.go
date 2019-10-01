package slack

import (
	"log"
	"sort"
	"strings"
)

func getDirectChannelNameFromMembers(members []string) string {
	sort.Strings(members)
	return strings.Join(members, "_")
}

func Check(intermediate *Intermediate) {
	// create channels index
	channelsByName := map[string]*IntermediateChannel{}
	for _, channel := range intermediate.PublicChannels {
		if _, ok := channelsByName[channel.Name]; ok {
			log.Printf("WARNING -- Duplicate public channel name: %s\n", channel.Name)
			continue
		}
		channelsByName[channel.Name] = channel
	}

	for _, channel := range intermediate.PrivateChannels {
		if _, ok := channelsByName[channel.Name]; ok {
			log.Printf("WARNING -- Duplicate private channel name: %s\n", channel.Name)
			continue
		}
		channelsByName[channel.Name] = channel
	}

	// create direct channels index
	for _, channel := range intermediate.GroupChannels {
		channelName := getDirectChannelNameFromMembers(channel.Members)
		if _, ok := channelsByName[channelName]; ok {
			log.Printf("WARNING -- Duplicate group channel name: %s\n", channelName)
			continue
		}
		channelsByName[channelName] = channel
	}

	for _, channel := range intermediate.DirectChannels {
		channelName := getDirectChannelNameFromMembers(channel.Members)
		if _, ok := channelsByName[channelName]; ok {
			log.Printf("WARNING -- Duplicate direct channel name: %s\n", channelName)
			continue
		}
		channelsByName[channelName] = channel
	}

	// create post index
	postsByChannelName := map[string][]*IntermediatePost{}
	for _, post := range intermediate.Posts {
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
			if user, ok := intermediate.UsersById[member]; !ok {
				log.Printf("-- Invalid member: %s\n", member)
			} else {
				usernames = append(usernames, user.Username)
			}
		}
		log.Printf("> Channel: \"%s\" Type: \"%s\" Post count: %d Members: \"%s\"", channelName, channel.Type, len(postsByChannelName[channelName]), strings.Join(usernames, ", "))
	}

	for channelName, posts := range postsByChannelName {
		if _, ok := visitedChannels[channelName]; !ok {
			log.Printf("-- Channel %s has %d posts but not a channel\n", channelName, len(posts))
		}
	}
}
