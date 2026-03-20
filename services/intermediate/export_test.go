package intermediate

import (
	"strings"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
)

func TestSplitTextIntoChunks(t *testing.T) {
	t.Run("Text within limit should return single chunk", func(t *testing.T) {
		text := "Short text"
		chunks := SplitTextIntoChunks(text, 100)

		if len(chunks) != 1 {
			t.Errorf("Expected 1 chunk, got %d", len(chunks))
		}
		if chunks[0] != text {
			t.Errorf("Expected chunk to equal original text")
		}
	})

	t.Run("Long text should be split into multiple chunks", func(t *testing.T) {
		text := model.NewRandomString(model.PostMessageMaxRunesV2 * 2)
		chunks := SplitTextIntoChunks(text, model.PostMessageMaxRunesV2)

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

		chunks := SplitTextIntoChunks(text, model.PostMessageMaxRunesV2)

		// First chunk should end with a space (word boundary)
		if len(chunks) > 1 && chunks[0][len(chunks[0])-1] != ' ' {
			t.Errorf("Expected first chunk to end with word boundary (space)")
		}
	})

	t.Run("Empty string", func(t *testing.T) {
		chunks := SplitTextIntoChunks("", 100)
		if len(chunks) != 1 || chunks[0] != "" {
			t.Errorf("Expected single empty chunk, got %v", chunks)
		}
	})

	t.Run("Text exactly at limit", func(t *testing.T) {
		text := "12345"
		chunks := SplitTextIntoChunks(text, 5)
		if len(chunks) != 1 || chunks[0] != text {
			t.Errorf("Expected single chunk with exact text, got %v", chunks)
		}
	})

	t.Run("Simple split at word boundary", func(t *testing.T) {
		text := "Hello world this is a test"
		chunks := SplitTextIntoChunks(text, 15)
		expected := []string{"Hello world ", "this is a test"}
		if len(chunks) != len(expected) {
			t.Errorf("Expected %d chunks, got %d", len(expected), len(chunks))
		}
		for i, chunk := range chunks {
			if i < len(expected) && chunk != expected[i] {
				t.Errorf("Chunk %d: expected %q, got %q", i, expected[i], chunk)
			}
		}
	})

	t.Run("Split on newline", func(t *testing.T) {
		text := "Line one\nLine two\nLine three"
		chunks := SplitTextIntoChunks(text, 15)
		expected := []string{"Line one\n", "Line two\n", "Line three"}
		if len(chunks) != len(expected) {
			t.Errorf("Expected %d chunks, got %d", len(expected), len(chunks))
		}
		for i, chunk := range chunks {
			if i < len(expected) && chunk != expected[i] {
				t.Errorf("Chunk %d: expected %q, got %q", i, expected[i], chunk)
			}
		}
	})

	t.Run("Split prefers newline over space", func(t *testing.T) {
		text := "This is line one\nThis is line two and it's longer"
		chunks := SplitTextIntoChunks(text, 25)
		expected := []string{"This is line one\n", "This is line two and ", "it's longer"}
		if len(chunks) != len(expected) {
			t.Errorf("Expected %d chunks, got %d", len(expected), len(chunks))
		}
		for i, chunk := range chunks {
			if i < len(expected) && chunk != expected[i] {
				t.Errorf("Chunk %d: expected %q, got %q", i, expected[i], chunk)
			}
		}
	})

	t.Run("No good break point - split in middle of word", func(t *testing.T) {
		text := "thisisaverylongwordwithnobreaks"
		chunks := SplitTextIntoChunks(text, 10)
		expected := []string{"thisisaver", "ylongwordw", "ithnobreak", "s"}
		if len(chunks) != len(expected) {
			t.Errorf("Expected %d chunks, got %d", len(expected), len(chunks))
		}
		for i, chunk := range chunks {
			if i < len(expected) && chunk != expected[i] {
				t.Errorf("Chunk %d: expected %q, got %q", i, expected[i], chunk)
			}
		}
	})

	t.Run("Multiple spaces", func(t *testing.T) {
		text := "Word1    Word2    Word3"
		chunks := SplitTextIntoChunks(text, 15)
		// Verify each chunk is within limit
		for i, chunk := range chunks {
			runeCount := len([]rune(chunk))
			if runeCount > 15 {
				t.Errorf("Chunk %d exceeds limit: %d > 15", i, runeCount)
			}
		}
		// Verify joining gives back original
		joined := strings.Join(chunks, "")
		if joined != text {
			t.Errorf("Joined chunks don't match original: %q != %q", joined, text)
		}
	})

	t.Run("Unicode characters (emoji and multi-byte)", func(t *testing.T) {
		text := "Hello 👋 world 🌍 test"
		chunks := SplitTextIntoChunks(text, 15)
		// Verify each chunk is within limit
		for i, chunk := range chunks {
			runeCount := len([]rune(chunk))
			if runeCount > 15 {
				t.Errorf("Chunk %d exceeds limit: %d > 15", i, runeCount)
			}
		}
		// Verify joining gives back original
		joined := strings.Join(chunks, "")
		if joined != text {
			t.Errorf("Joined chunks don't match original: %q != %q", joined, text)
		}
	})

	t.Run("Long text with newlines at various positions", func(t *testing.T) {
		text := "First line\nSecond line is longer\nThird line\nFourth line is also long"
		chunks := SplitTextIntoChunks(text, 20)
		// Verify each chunk is within limit
		for i, chunk := range chunks {
			runeCount := len([]rune(chunk))
			if runeCount > 20 {
				t.Errorf("Chunk %d exceeds limit: %d > 20", i, runeCount)
			}
		}
		// Verify joining gives back original
		joined := strings.Join(chunks, "")
		if joined != text {
			t.Errorf("Joined chunks don't match original")
		}
	})

	t.Run("Text with newline beyond search range", func(t *testing.T) {
		text := "This is a very long line without breaks for over 100 characters and then\nthere is a newline but it's too far away to be found in the search range which is limited to 100 characters"
		chunks := SplitTextIntoChunks(text, 80)
		// Verify each chunk is within limit
		for i, chunk := range chunks {
			runeCount := len([]rune(chunk))
			if runeCount > 80 {
				t.Errorf("Chunk %d exceeds limit: %d > 80, chunk: %q", i, runeCount, chunk)
			}
		}
		// Verify joining gives back original
		joined := strings.Join(chunks, "")
		if joined != text {
			t.Errorf("Joined chunks don't match original")
		}
	})

	t.Run("Very small limit", func(t *testing.T) {
		text := "Hello"
		chunks := SplitTextIntoChunks(text, 2)
		expected := []string{"He", "ll", "o"}
		if len(chunks) != len(expected) {
			t.Errorf("Expected %d chunks, got %d", len(expected), len(chunks))
		}
		for i, chunk := range chunks {
			if i < len(expected) && chunk != expected[i] {
				t.Errorf("Chunk %d: expected %q, got %q", i, expected[i], chunk)
			}
		}
	})

	t.Run("Single character chunks", func(t *testing.T) {
		text := "ABCDE"
		chunks := SplitTextIntoChunks(text, 1)
		expected := []string{"A", "B", "C", "D", "E"}
		if len(chunks) != len(expected) {
			t.Errorf("Expected %d chunks, got %d", len(expected), len(chunks))
		}
		for i, chunk := range chunks {
			if i < len(expected) && chunk != expected[i] {
				t.Errorf("Chunk %d: expected %q, got %q", i, expected[i], chunk)
			}
		}
	})

	t.Run("Newline at exact boundary", func(t *testing.T) {
		text := "1234567890\n1234567890"
		chunks := SplitTextIntoChunks(text, 11)
		expected := []string{"1234567890\n", "1234567890"}
		if len(chunks) != len(expected) {
			t.Errorf("Expected %d chunks, got %d", len(expected), len(chunks))
		}
		for i, chunk := range chunks {
			if i < len(expected) && chunk != expected[i] {
				t.Errorf("Chunk %d: expected %q, got %q", i, expected[i], chunk)
			}
		}
	})

	t.Run("Space at exact boundary", func(t *testing.T) {
		text := "1234567890 1234567890"
		chunks := SplitTextIntoChunks(text, 11)
		expected := []string{"1234567890 ", "1234567890"}
		if len(chunks) != len(expected) {
			t.Errorf("Expected %d chunks, got %d", len(expected), len(chunks))
		}
		for i, chunk := range chunks {
			if i < len(expected) && chunk != expected[i] {
				t.Errorf("Chunk %d: expected %q, got %q", i, expected[i], chunk)
			}
		}
	})

	t.Run("Mixed content with spaces and newlines", func(t *testing.T) {
		text := "First paragraph with some text.\n\nSecond paragraph with more content that needs to be split up properly."
		chunks := SplitTextIntoChunks(text, 30)
		// Verify each chunk is within limit
		for i, chunk := range chunks {
			runeCount := len([]rune(chunk))
			if runeCount > 30 {
				t.Errorf("Chunk %d exceeds limit: %d > 30", i, runeCount)
			}
		}
		// Verify joining gives back original
		joined := strings.Join(chunks, "")
		if joined != text {
			t.Errorf("Joined chunks don't match original")
		}
	})

	t.Run("Japanese characters (multi-byte unicode)", func(t *testing.T) {
		text := "これは日本語のテストです。長いテキストを分割します。"
		chunks := SplitTextIntoChunks(text, 15)
		// Verify each chunk is within limit
		for i, chunk := range chunks {
			runeCount := len([]rune(chunk))
			if runeCount > 15 {
				t.Errorf("Chunk %d exceeds limit: %d > 15", i, runeCount)
			}
		}
		// Verify joining gives back original
		joined := strings.Join(chunks, "")
		if joined != text {
			t.Errorf("Joined chunks don't match original")
		}
	})

	// Comprehensive verification for all test cases
	t.Run("All chunks preserve text integrity", func(t *testing.T) {
		testTexts := []struct {
			text     string
			maxRunes int
		}{
			{"Short text", 100},
			{"", 100},
			{"Hello world this is a test", 15},
			{model.NewRandomString(model.PostMessageMaxRunesV2 * 2), model.PostMessageMaxRunesV2},
		}

		for _, tt := range testTexts {
			chunks := SplitTextIntoChunks(tt.text, tt.maxRunes)

			// Verify each chunk is within limit
			for i, chunk := range chunks {
				runeCount := len([]rune(chunk))
				if runeCount > tt.maxRunes {
					t.Errorf("Chunk %d exceeds limit: %d > %d", i, runeCount, tt.maxRunes)
				}
			}

			// Verify joining gives back original
			joined := strings.Join(chunks, "")
			if joined != tt.text {
				t.Errorf("Joined chunks don't match original text")
			}
		}
	})
}
