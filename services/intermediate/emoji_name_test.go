package intermediate

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
	log "github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEmojiNameSanitizer(t *testing.T) {
	s := NewEmojiNameSanitizer(log.New())

	t.Run("valid names pass through", func(t *testing.T) {
		for _, name := range []string{"+1", "thumbsup", "emoji_name-1", "a"} {
			assert.Equal(t, name, s.Sanitize(name))
		}
	})

	t.Run("NFKD readable transliteration", func(t *testing.T) {
		assert.Equal(t, "cafe", NewEmojiNameSanitizer(log.New()).Sanitize("café"))
	})

	t.Run("ß expands to ss", func(t *testing.T) {
		assert.Equal(t, "strasse", NewEmojiNameSanitizer(log.New()).Sanitize("straße"))
	})

	t.Run("CJK falls back to hash", func(t *testing.T) {
		original := "リハテスト"
		got := NewEmojiNameSanitizer(log.New()).Sanitize(original)
		assert.Equal(t, fallbackEmojiName(original), got)
		assert.True(t, isValidEmojiName(got))
	})

	t.Run("empty after sanitize falls back", func(t *testing.T) {
		got := NewEmojiNameSanitizer(log.New()).Sanitize("!!!")
		assert.Equal(t, fallbackEmojiName("!!!"), got)
	})

	t.Run("collision uses fallback", func(t *testing.T) {
		s := NewEmojiNameSanitizer(log.New())
		assert.Equal(t, "cafe", s.Sanitize("café"))

		collided := s.Sanitize("cafe!")
		assert.Equal(t, fallbackEmojiName("cafe!"), collided)
		assert.NotEqual(t, "cafe", collided)
	})

	t.Run("valid name after readable rename does not merge", func(t *testing.T) {
		s := NewEmojiNameSanitizer(log.New())
		assert.Equal(t, "cafe", s.Sanitize("café"))
		// "cafe" is already claimed by "café"; the valid original must not
		// silently reuse that name.
		got := s.Sanitize("cafe")
		assert.Equal(t, fallbackEmojiName("cafe"), got)
		assert.NotEqual(t, "cafe", got)
		assert.Equal(t, "cafe", s.Sanitize("café"))
	})

	t.Run("valid name first wins over later readable collision", func(t *testing.T) {
		s := NewEmojiNameSanitizer(log.New())
		assert.Equal(t, "cafe", s.Sanitize("cafe"))
		got := s.Sanitize("café")
		assert.Equal(t, fallbackEmojiName("café"), got)
		assert.NotEqual(t, "cafe", got)
	})

	t.Run("identical originals reuse mapping", func(t *testing.T) {
		s := NewEmojiNameSanitizer(log.New())
		a := s.Sanitize("リハテスト")
		b := s.Sanitize("リハテスト")
		assert.Equal(t, a, b)
		assert.Len(t, s.mapped, 1)
	})

	t.Run("length truncation", func(t *testing.T) {
		long := strings.Repeat("a", model.EmojiNameMaxLength+10)
		got := NewEmojiNameSanitizer(log.New()).Sanitize(long)
		assert.Equal(t, strings.Repeat("a", model.EmojiNameMaxLength), got)
	})

	t.Run("mixed CJK keeps ASCII", func(t *testing.T) {
		assert.Equal(t, "hello", NewEmojiNameSanitizer(log.New()).Sanitize("helloリハ"))
	})

	t.Run("fallback is deterministic", func(t *testing.T) {
		sum := sha256.Sum256([]byte("リハテスト"))
		want := "emoji_" + hex.EncodeToString(sum[:8])
		assert.Equal(t, want, fallbackEmojiName("リハテスト"))
		assert.Equal(t,
			NewEmojiNameSanitizer(log.New()).Sanitize("リハテスト"),
			NewEmojiNameSanitizer(log.New()).Sanitize("リハテスト"),
		)
	})

	t.Run("uniqueFallback never returns an occupied name", func(t *testing.T) {
		s := NewEmojiNameSanitizer(log.New())
		original := "リハテスト"
		sum := sha256.Sum256([]byte(original))
		hexStr := hex.EncodeToString(sum[:])

		// Preclaim every hash-prefix candidate uniqueFallback tries first,
		// plus the truncated full-hash form the old code returned unchecked.
		for n := 8; n <= len(sum); n++ {
			name := "emoji_" + hexStr[:n*2]
			if len(name) > model.EmojiNameMaxLength {
				name = name[:model.EmojiNameMaxLength]
			}
			s.usedBy[name] = "other"
		}
		s.usedBy["emoji_"+hexStr[:model.EmojiNameMaxLength-len("emoji_")]] = "other"

		got := s.Sanitize(original)
		require.True(t, isValidEmojiName(got))
		assert.NotEqual(t, "other", s.usedBy[got], "sanitized name must not already be owned")
		assert.Equal(t, original, s.usedBy[got])
		assert.True(t, strings.HasPrefix(got, "emoji_"))
	})
}

func TestExporter_SanitizeEmojiName(t *testing.T) {
	e := &Exporter{Logger: log.New()}
	assert.Equal(t, "+1", e.SanitizeEmojiName("+1"))
	assert.Equal(t, "cafe", e.SanitizeEmojiName("café"))
	mapping := e.EmojiNameMapping()
	require.NotNil(t, mapping)
	assert.Equal(t, "cafe", mapping["café"])
}
