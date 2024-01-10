package slack

import (
	"testing"

	"github.com/mattermost/mattermost-server/v6/model"
	"github.com/sirupsen/logrus"
)

func TestSlackConvertPostsMarkup(t *testing.T) {
	var posts = map[string][]SlackPost{
		"channelName": {
			{
				Text: model.NewRandomString(model.PostMessageMaxRunesV2 * 2),
			},
		},
	}

	t.Run("Test post length", func(t *testing.T) {
		transformer := NewTransformer("test", logrus.New())

		parsedPosts := transformer.SlackConvertPostsMarkup(posts)
		post := parsedPosts["channelName"][0]

		if len(post.Text) > model.PostMessageMaxRunesV2 {
			t.Errorf("Expected post length to be less than %d, got %d", model.PostMessageMaxRunesV2, len(post.Text))
		}

	})
}
