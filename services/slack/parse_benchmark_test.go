package slack

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/sirupsen/logrus"
)

func generateUsersJSON(n int) []byte {
	users := make([]SlackUser, n)
	for i := range users {
		users[i] = SlackUser{
			Id:       fmt.Sprintf("U%06d", i),
			Username: fmt.Sprintf("user%d", i),
			IsBot:    i%20 == 0,
			Profile: SlackProfile{
				RealName: fmt.Sprintf("User Number %d", i),
				Email:    fmt.Sprintf("user%d@example.com", i),
				Title:    fmt.Sprintf("Engineer %d", i),
			},
		}
	}
	b, _ := json.Marshal(users)
	return b
}

func generatePosts(numChannels, postsPerChannel, numUsers int) (map[string][]SlackPost, []SlackUser) {
	users := make([]SlackUser, numUsers)
	for i := range users {
		users[i] = SlackUser{
			Id:       fmt.Sprintf("U%06d", i),
			Username: fmt.Sprintf("user%d", i),
		}
	}

	posts := make(map[string][]SlackPost, numChannels)
	for ch := range numChannels {
		channelName := fmt.Sprintf("channel-%d", ch)
		channelPosts := make([]SlackPost, postsPerChannel)
		for p := range postsPerChannel {
			text := fmt.Sprintf("Hello <@U%06d> and <@U%06d>, check <!here> and <!channel>", p%numUsers, (p+1)%numUsers)
			channelPosts[p] = SlackPost{
				User:      fmt.Sprintf("U%06d", p%numUsers),
				Text:      text,
				TimeStamp: fmt.Sprintf("%d.%06d", 1600000000+p, p),
				Type:      "message",
				Attachments: []*model.SlackAttachment{
					{Fallback: text},
				},
			}
		}
		posts[channelName] = channelPosts
	}

	return posts, users
}

func generateChannels(n int) []SlackChannel {
	channels := make([]SlackChannel, n)
	for i := range channels {
		channels[i] = SlackChannel{
			Id:   fmt.Sprintf("C%06d", i),
			Name: fmt.Sprintf("channel-%d", i),
		}
	}
	return channels
}

func BenchmarkSlackParseUsers(b *testing.B) {
	for _, numUsers := range []int{1000, 10000, 50000} {
		usersJSON := generateUsersJSON(numUsers)
		b.Run(fmt.Sprintf("users=%d", numUsers), func(b *testing.B) {
			logger := logrus.New()
			logger.SetLevel(logrus.WarnLevel)
			transformer := NewTransformer("test", logger)

			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				reader := bytes.NewReader(usersJSON)
				_, err := transformer.SlackParseUsers(reader)
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func deepCopyPosts(templatePosts map[string][]SlackPost) map[string][]SlackPost {
	posts := make(map[string][]SlackPost, len(templatePosts))
	for k, v := range templatePosts {
		cp := make([]SlackPost, len(v))
		for i, post := range v {
			cp[i] = post
			if len(post.Attachments) > 0 {
				cp[i].Attachments = make([]*model.SlackAttachment, len(post.Attachments))
				for j, att := range post.Attachments {
					attCopy := *att
					cp[i].Attachments[j] = &attCopy
				}
			}
		}
		posts[k] = cp
	}
	return posts
}

func BenchmarkSlackConvertUserMentions(b *testing.B) {
	for _, size := range []struct {
		channels, posts, users int
	}{
		{100, 100000, 500},
		{500, 500000, 1000},
	} {
		b.Run(fmt.Sprintf("ch=%d/posts=%d/users=%d", size.channels, size.posts, size.users), func(b *testing.B) {
			logger := logrus.New()
			logger.SetLevel(logrus.WarnLevel)
			transformer := NewTransformer("test", logger)

			templatePosts, users := generatePosts(size.channels, size.posts/size.channels, size.users)

			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				posts := deepCopyPosts(templatePosts)
				transformer.SlackConvertUserMentions(users, posts)
			}
		})
	}
}

func BenchmarkSlackConvertChannelMentions(b *testing.B) {
	for _, size := range []struct {
		channels, posts int
	}{
		{100, 100000},
		{500, 500000},
	} {
		b.Run(fmt.Sprintf("ch=%d/posts=%d", size.channels, size.posts), func(b *testing.B) {
			logger := logrus.New()
			logger.SetLevel(logrus.WarnLevel)
			transformer := NewTransformer("test", logger)

			channels := generateChannels(size.channels)
			templatePosts, _ := generatePosts(size.channels, size.posts/size.channels, 50)

			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				posts := deepCopyPosts(templatePosts)
				transformer.SlackConvertChannelMentions(channels, posts)
			}
		})
	}
}
