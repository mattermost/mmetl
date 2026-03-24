package slack

import (
	"log"
	"math"
	"strconv"
	"strings"
)

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

func SplitChannelsByMemberSize(channels []SlackChannel, limit int) (regularChannels, bigChannels []SlackChannel) {
	for _, channel := range channels {
		if len(channel.Members) == 1 {
			log.Println("Bulk export for direct channels containing a single member is not supported. Not importing channel " + channel.Name)
		} else if len(channel.Members) > limit {
			bigChannels = append(bigChannels, channel)
		} else {
			regularChannels = append(regularChannels, channel)
		}
	}
	return
}
