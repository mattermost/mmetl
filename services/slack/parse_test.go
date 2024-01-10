package slack

import (
	"testing"

	"github.com/mattermost/mattermost-server/v6/model"
	"github.com/sirupsen/logrus"
)

func TestSlackConvertPostsMarkup(t *testing.T) {
	t.Run("Test post length", func(t *testing.T) {
		transformer := NewTransformer("test", logrus.New())

		posts := map[string][]SlackPost{
			"channel1": {
				{
					Text: "Hello, world",
				},
				{
					Text: model.NewRandomString(model.PostMessageMaxRunesV2 * 2),
				},
			},
		}

		parsedPosts := transformer.SlackConvertPostsMarkup(posts)

		for _, postArray := range parsedPosts {
			for _, post := range postArray {
				if len(post.Text) > model.PostMessageMaxRunesV2 {
					t.Errorf("Expected post length to be less than %d, got %d", model.PostMessageMaxRunesV2, len(post.Text))
				}
			}
		}
	})

}
