package slack

import (
	"log"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/mattermost/mmetl/services/intermediate"
)

// The intermediate representation and the bulk-import Exporter now live in the
// shared services/intermediate package. This file retains only the
// Slack-specific name/timestamp conversion helpers.

var isValidChannelNameCharacters = regexp.MustCompile(`^[a-zA-Z0-9\-_]+$`).MatchString

func SlackConvertTimeStamp(ts string) int64 {
	timeStrings := strings.Split(ts, ".")

	tail := "0000"
	if len(timeStrings) > 1 {
		tail = timeStrings[1][:4]
	}
	timeString := timeStrings[0] + tail

	timeStamp, err := strconv.ParseInt(timeString, 10, 64)
	if err != nil {
		log.Println("Slack Import: Bad timestamp detected.")
		return 1
	}

	return int64(math.Round(float64(timeStamp) / 10)) // round for precision
}

func SlackConvertChannelName(channelName string, channelId string) string {
	newName := strings.Trim(channelName, "_-")
	if len(newName) == 1 {
		return strings.ToLower("slack-channel-" + newName)
	}

	if isValidChannelNameCharacters(newName) {
		return strings.ToLower(newName)
	}
	return strings.ToLower(channelId)
}

// SplitChannelsByMemberSize partitions group channels into those small enough
// to import as Mattermost group channels and those that have to become private
// channels, dropping the ones with a single member. It runs before
// filterValidMembers, so a channel dropped here is the one place a source
// membership would otherwise never be counted at all — hence the Seen/Skip pair.
// report may be nil, in which case the drops go unrecorded.
func SplitChannelsByMemberSize(channels []SlackChannel, limit int, report *intermediate.Report) (regularChannels, bigChannels []SlackChannel) {
	for _, channel := range channels {
		if len(channel.Members) == 1 {
			originalName := getOriginalName(channel)
			report.GroupChannels().Skip(channel.Id, originalName, ReasonDMSingleMember)

			memberships := report.ChannelMemberships()
			memberships.Seen(len(channel.Members))
			for _, member := range channel.Members {
				memberships.Skip(
					intermediate.MembershipID(channel.Id, member),
					intermediate.MembershipID(originalName, ""),
					intermediate.ReasonMembershipChannelSkipped,
				)
			}
		} else if len(channel.Members) > limit {
			bigChannels = append(bigChannels, channel)
		} else {
			regularChannels = append(regularChannels, channel)
		}
	}
	return
}
