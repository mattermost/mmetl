package slack

import (
	"testing"
	"unicode/utf8"

	"github.com/mattermost/mattermost/server/public/model"
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

	t.Run("Test post not truncated during markdown conversion", func(t *testing.T) {
		transformer := NewTransformer("test", logrus.New())

		parsedPosts := transformer.SlackConvertPostsMarkup(posts)
		post := parsedPosts["channelName"][0]

		// Posts should NOT be truncated during markdown conversion
		// They will be split into thread replies later in the transformation phase
		if utf8.RuneCountInString(post.Text) <= model.PostMessageMaxRunesV2 {
			t.Errorf("Test expects a long post, but got length %d", utf8.RuneCountInString(post.Text))
		}
	})
}

func TestSlackConvertUserMentions(t *testing.T) {
	type TestCases struct {
		name     string
		mention  string
		expected string
	}

	var testCases = []TestCases{
		{name: "special: <!here>", mention: "<!here>", expected: "@here"},
		{name: "special: <@here>", mention: "<@here>", expected: "@here"},
		{name: "special: <!channel>", mention: "<!channel>", expected: "@channel"},
		{name: "special: <!everyone>", mention: "<!everyone>", expected: "@all"},
		{name: "special pipe-aliased: <!here|here>", mention: "<!here|here>", expected: "@here"},
		{name: "special pipe-aliased: <!channel|@channel>", mention: "<!channel|@channel>", expected: "@channel"},
		{name: "special pipe-aliased: <!everyone|all>", mention: "<!everyone|all>", expected: "@all"},
		{name: "user ID without pipe", mention: "<@U100>", expected: "@user1"},
		{name: "user ID with pipe and username", mention: "<@U100|user1>", expected: "@user1"},
		{name: "enterprise Grid W-prefix user ID", mention: "<@W100>", expected: "@user2"},
		{name: "enterprise Grid W-prefix user ID with pipe", mention: "<@W100|user2>", expected: "@user2"},
		{name: "lowercase username is not converted", mention: "<@user1>", expected: "<@user1>"},
		{name: "unknown user ID left unchanged", mention: "<@U999>", expected: "<@U999>"},
		{name: "multiple mentions in one message", mention: "Hey <@U100> and <!here>", expected: "Hey @user1 and @here"},
		{name: "mention embedded in text", mention: "Please ask <@U100> about this", expected: "Please ask @user1 about this"},
		{name: "no mentions in text", mention: "Hello world", expected: "Hello world"},
		{name: "empty text", mention: "", expected: ""},
	}

	var users = []SlackUser{
		{
			Id:       "U100",
			Username: "user1",
		},
		{
			Id:       "W100",
			Username: "user2",
		},
	}
	transformer := NewTransformer("test", logrus.New())

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
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
				t.Errorf("In post expected %q to be converted to %q, got %q", testCase.mention, testCase.expected, post.Text)
			}

			if post.Attachments[0].Fallback != testCase.expected {
				t.Errorf("In fallback expected %q to be converted to %q, got %q", testCase.mention, testCase.expected, post.Attachments[0].Fallback)
			}
		})
	}
}

func TestSlackConvertUserMentionsMultipleUsers(t *testing.T) {
	users := []SlackUser{
		{Id: "U001", Username: "alice"},
		{Id: "U002", Username: "bob"},
		{Id: "U003", Username: "charlie"},
	}
	transformer := NewTransformer("test", logrus.New())

	posts := map[string][]SlackPost{
		"general": {
			{
				Text: "<@U001> assigned this to <@U002> and <@U003>",
			},
		},
	}
	parsedPosts := transformer.SlackConvertUserMentions(users, posts)
	post := parsedPosts["general"][0]

	expected := "@alice assigned this to @bob and @charlie"
	if post.Text != expected {
		t.Errorf("Expected %q, got %q", expected, post.Text)
	}
}

func TestSlackConvertChannelMentions(t *testing.T) {
	type TestCases struct {
		name     string
		mention  string
		expected string
	}

	var testCases = []TestCases{
		{name: "channel ID without pipe", mention: "<#C001>", expected: "~general"},
		{name: "channel ID with pipe and name", mention: "<#C001|general>", expected: "~general"},
		{name: "unknown channel ID left unchanged", mention: "<#C999>", expected: "<#C999>"},
		{name: "lowercase channel ref not converted", mention: "<#general>", expected: "<#general>"},
		{name: "multiple channel mentions", mention: "See <#C001> and <#C002>", expected: "See ~general and ~random"},
		{name: "channel mention embedded in text", mention: "Post in <#C001|general> please", expected: "Post in ~general please"},
		{name: "no mentions in text", mention: "Hello world", expected: "Hello world"},
		{name: "empty text", mention: "", expected: ""},
	}

	var channels = []SlackChannel{
		{Id: "C001", Name: "general"},
		{Id: "C002", Name: "random"},
	}
	transformer := NewTransformer("test", logrus.New())

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
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
			parsedPosts := transformer.SlackConvertChannelMentions(channels, posts)
			post := parsedPosts["channelName"][0]
			if post.Text != testCase.expected {
				t.Errorf("In post expected %q to be converted to %q, got %q", testCase.mention, testCase.expected, post.Text)
			}

			if post.Attachments[0].Fallback != testCase.expected {
				t.Errorf("In fallback expected %q to be converted to %q, got %q", testCase.mention, testCase.expected, post.Attachments[0].Fallback)
			}
		})
	}
}

func TestReplaceMentions(t *testing.T) {
	lookup := map[string]string{
		"U100": "@user1",
		"U200": "@user2",
	}

	tests := []struct {
		name     string
		text     string
		expected string
	}{
		{
			name:     "ID only",
			text:     "<@U100>",
			expected: "@user1",
		},
		{
			name:     "ID with pipe",
			text:     "<@U100|user1>",
			expected: "@user1",
		},
		{
			name:     "unknown ID unchanged",
			text:     "<@U999>",
			expected: "<@U999>",
		},
		{
			name:     "multiple replacements",
			text:     "<@U100> and <@U200>",
			expected: "@user1 and @user2",
		},
		{
			name:     "pipe with different display name",
			text:     "<@U100|some display name>",
			expected: "@user1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := replaceMentions(tt.text, slackUserMentionRe, 2, lookup)
			if result != tt.expected {
				t.Errorf("replaceMentions(%q) = %q, want %q", tt.text, result, tt.expected)
			}
		})
	}
}
