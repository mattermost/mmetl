package slack

import (
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/sirupsen/logrus"
)

func TestSlackConvertUserMentions(t *testing.T) {
	type TestCases struct {
		mention  string
		expected string
	}

	var testCases = []TestCases{
		{mention: "<!here>", expected: "@here"},
		{mention: "<!channel>", expected: "@channel"},
		{mention: "<!everyone>", expected: "@all"},
		{mention: "<@U100>", expected: "@user1"},
		{mention: "<@user1>", expected: "<@user1>"},
	}

	var users = []SlackUser{
		{
			Id:       "U100",
			Username: "user1",
		},
	}
	transformer := NewTransformer("test", logrus.New())

	for _, testCase := range testCases {
		posts := map[string][]SlackPost{
			"channelName": {
				{
					Text: testCase.mention,
					Attachments: []*model.SlackAttachment{
						{Fallback: testCase.mention},
					},
				},
			},
		}
		parsedPosts := transformer.SlackConvertUserMentions(users, posts)
		post := parsedPosts["channelName"][0]
		if post.Text != testCase.expected {
			t.Errorf("In post expected %s to be converted to %s. Post: %s", testCase.mention, testCase.expected, post.Text)
		}

		if post.Attachments[0].Fallback != testCase.expected {
			t.Errorf("In fallback expected %s to be converted to %s. Post: %s", testCase.mention, testCase.expected, post.Text)
		}
	}
}
