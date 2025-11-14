package slack

import (
	"strings"
	"testing"

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
		if len(post.Text) <= model.PostMessageMaxRunesV2 {
			t.Errorf("Test expects a long post, but got length %d", len(post.Text))
		}

	})
}

func TestSplitTextIntoChunks(t *testing.T) {
	t.Run("Text within limit should return single chunk", func(t *testing.T) {
		text := "Short text"
		chunks := splitTextIntoChunks(text, 100)

		if len(chunks) != 1 {
			t.Errorf("Expected 1 chunk, got %d", len(chunks))
		}
		if chunks[0] != text {
			t.Errorf("Expected chunk to equal original text")
		}
	})

	t.Run("Long text should be split into multiple chunks", func(t *testing.T) {
		text := model.NewRandomString(model.PostMessageMaxRunesV2 * 2)
		chunks := splitTextIntoChunks(text, model.PostMessageMaxRunesV2)

		if len(chunks) < 2 {
			t.Errorf("Expected at least 2 chunks, got %d", len(chunks))
		}

		// Verify each chunk is within the limit
		for i, chunk := range chunks {
			runeCount := len([]rune(chunk))
			if runeCount > model.PostMessageMaxRunesV2 {
				t.Errorf("Chunk %d exceeds limit: %d > %d", i, runeCount, model.PostMessageMaxRunesV2)
			}
		}
	})

	t.Run("Should split on word boundaries when possible", func(t *testing.T) {
		// Create text with clear word boundaries
		word := "word "
		repeatCount := (model.PostMessageMaxRunesV2 / len(word)) + 100
		text := strings.Repeat(word, repeatCount)

		chunks := splitTextIntoChunks(text, model.PostMessageMaxRunesV2)

		// First chunk should end with a space (word boundary)
		if len(chunks) > 1 && chunks[0][len(chunks[0])-1] != ' ' {
			t.Errorf("Expected first chunk to end with word boundary (space)")
		}
	})
}

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
