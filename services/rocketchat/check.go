package rocketchat

import (
	"sort"
	"strings"
)

// CheckIntermediate performs basic validation of the intermediate data,
// logging warnings for inconsistencies such as duplicate channel names,
// posts referencing missing channels, and channels with invalid members.
func (t *Transformer) CheckIntermediate() {
	t.Logger.Info("Checking intermediate resources")

	// Build channel index.
	channelsByName := map[string]bool{}
	for _, ch := range t.Intermediate.PublicChannels {
		if channelsByName[ch.Name] {
			t.Logger.Warnf("Duplicate public channel name: %s", ch.Name)
		}
		channelsByName[ch.Name] = true
	}
	for _, ch := range t.Intermediate.PrivateChannels {
		if channelsByName[ch.Name] {
			t.Logger.Warnf("Duplicate private channel name: %s", ch.Name)
		}
		channelsByName[ch.Name] = true
	}

	// Validate channel members.
	allChannels := append(t.Intermediate.PublicChannels, t.Intermediate.PrivateChannels...)
	for _, ch := range allChannels {
		for _, memberID := range ch.Members {
			if _, ok := t.Intermediate.UsersById[memberID]; !ok {
				t.Logger.Warnf("Channel %s has member %s not found in users", ch.Name, memberID)
			}
		}
	}

	// Build DM channel name from sorted members.
	getDirectName := func(members []string) string {
		sorted := append([]string{}, members...)
		sort.Strings(sorted)
		return strings.Join(sorted, "_")
	}

	directChannelNames := map[string]bool{}
	for _, ch := range t.Intermediate.GroupChannels {
		name := getDirectName(ch.MembersUsernames)
		if directChannelNames[name] {
			t.Logger.Warnf("Duplicate group channel: %s", name)
		}
		directChannelNames[name] = true
	}
	for _, ch := range t.Intermediate.DirectChannels {
		name := getDirectName(ch.MembersUsernames)
		if directChannelNames[name] {
			t.Logger.Warnf("Duplicate direct channel: %s", name)
		}
		directChannelNames[name] = true
	}

	// Validate posts reference known channels.
	for _, post := range t.Intermediate.Posts {
		if post.IsDirect {
			continue
		}
		if post.Channel != "" && !channelsByName[post.Channel] {
			t.Logger.Warnf("Post by %s references unknown channel %s", post.User, post.Channel)
		}
	}

	t.Logger.Infof("Check complete: %d users, %d public channels, %d private channels, %d direct/group channels, %d posts",
		len(t.Intermediate.UsersById),
		len(t.Intermediate.PublicChannels),
		len(t.Intermediate.PrivateChannels),
		len(t.Intermediate.DirectChannels)+len(t.Intermediate.GroupChannels),
		len(t.Intermediate.Posts),
	)
}
