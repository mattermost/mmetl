package slack

import (
	"fmt"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"
)

var posts = map[string][]SlackPost{
	"mentions": {
		{
			Text: "test <!here> test",
		},
		{
			Text: "test <!channel> test",
		},
		{
			Text: "test <!everyone> test",
		},
		{
			Text: "test <@U100> test",
		},
		{
			Text: "test <@user1> test",
		},
	},
}

var users = []SlackUser{
	{
		Id:       "U100",
		Username: "user1",
	},
}

func TestSlackConvertUserMentions(t *testing.T) {
	transformer := NewTransformer("test", logrus.New())
	parsedPosts := transformer.SlackConvertUserMentions(users, posts)
	for _, post := range parsedPosts["mentions"] {
		fmt.Println(post.Text)
		if strings.Contains(post.Text, "!here") {
			t.Errorf("Expected !here to be converted to @here. Post: %s", post.Text)
		}

		if strings.Contains(post.Text, "!channel") {
			t.Errorf("Expected !channel to be converted to @channel. Post: %s", post.Text)
		}

		if strings.Contains(post.Text, "!all") {
			t.Errorf("Expected !all to be converted to @all. Post: %s", post.Text)
		}

		if strings.Contains(post.Text, "@U100") {
			t.Errorf("Expected !U100 to be converted to @user1. Post: %s", post.Text)
		}
	}
}
