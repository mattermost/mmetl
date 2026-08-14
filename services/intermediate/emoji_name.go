package intermediate

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"unicode"

	"github.com/mattermost/mattermost/server/public/model"
	log "github.com/sirupsen/logrus"
	"golang.org/x/text/unicode/norm"
)

// EmojiNameSanitizer maps source emoji/reaction names to Mattermost-valid names
// ([a-zA-Z0-9\-+_], non-empty, ≤ 64 bytes). The same original always maps to the
// same sanitized name within a transform run.
type EmojiNameSanitizer struct {
	logger log.FieldLogger
	mapped map[string]string // original → sanitized
	usedBy map[string]string // sanitized → original that claimed it
}

// NewEmojiNameSanitizer creates a sanitizer that logs each original→sanitized
// rename once.
func NewEmojiNameSanitizer(logger log.FieldLogger) *EmojiNameSanitizer {
	return &EmojiNameSanitizer{
		logger: logger,
		mapped: make(map[string]string),
		usedBy: make(map[string]string),
	}
}

// Mapping returns a copy of the original→sanitized map accumulated so far.
func (s *EmojiNameSanitizer) Mapping() map[string]string {
	if s == nil {
		return nil
	}
	out := make(map[string]string, len(s.mapped))
	for k, v := range s.mapped {
		out[k] = v
	}
	return out
}

// Sanitize returns a Mattermost-valid emoji name for original. Already-valid
// names pass through. Callers should apply source-specific cleanup (e.g.
// ::skin-tone / surrounding colons) before calling Sanitize.
func (s *EmojiNameSanitizer) Sanitize(original string) string {
	if s == nil {
		return fallbackEmojiName(original)
	}
	if sanitized, ok := s.mapped[original]; ok {
		return sanitized
	}

	sanitized := s.chooseName(original)
	s.mapped[original] = sanitized
	s.usedBy[sanitized] = original

	if sanitized != original && s.logger != nil {
		s.logger.Warnf("Emoji name %q is not valid for Mattermost; renaming to %q", original, sanitized)
	}
	return sanitized
}

func (s *EmojiNameSanitizer) chooseName(original string) string {
	candidate := original
	if !isValidEmojiName(original) {
		candidate = readableEmojiName(original)
		if candidate == "" {
			candidate = fallbackEmojiName(original)
		}
	}
	return s.resolveCollision(original, candidate)
}

// resolveCollision returns candidate when free (or already owned by original);
// otherwise a deterministic hash fallback that is also free in usedBy.
func (s *EmojiNameSanitizer) resolveCollision(original, candidate string) string {
	if owner, taken := s.usedBy[candidate]; !taken || owner == original {
		return candidate
	}
	return s.uniqueFallback(original)
}

func (s *EmojiNameSanitizer) uniqueFallback(original string) string {
	sum := sha256.Sum256([]byte(original))
	hexStr := hex.EncodeToString(sum[:])
	// Prefer 8-byte (16 hex) prefix; extend on the rare hash collision.
	for n := 8; n <= len(sum); n++ {
		name := "emoji_" + hexStr[:n*2]
		if len(name) > model.EmojiNameMaxLength {
			name = name[:model.EmojiNameMaxLength]
		}
		if owner, taken := s.usedBy[name]; !taken || owner == original {
			return name
		}
	}
	return "emoji_" + hexStr[:model.EmojiNameMaxLength-len("emoji_")]
}

func isValidEmojiName(name string) bool {
	return name != "" &&
		len(name) <= model.EmojiNameMaxLength &&
		model.IsValidAlphaNumHyphenUnderscorePlus(name)
}

func isEmojiNameRune(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
		(r >= '0' && r <= '9') || r == '-' || r == '_' || r == '+'
}

// readableEmojiName applies NFKD transliteration and maps invalid runes to '_',
// then trims leading/trailing '_' and '-'. Returns "" when nothing usable remains.
func readableEmojiName(name string) string {
	// ß does not decompose under NFKD; expand it for a readable result.
	name = strings.ReplaceAll(name, "ß", "ss")
	name = norm.NFKD.String(name)

	var b strings.Builder
	b.Grow(len(name))
	for _, r := range name {
		switch {
		case isEmojiNameRune(r):
			b.WriteRune(r)
		case unicode.Is(unicode.Mn, r):
			// Drop combining marks left by NFKD (e.g. café → cafe).
		default:
			b.WriteByte('_')
		}
	}

	candidate := strings.Trim(b.String(), "_-")
	if len(candidate) > model.EmojiNameMaxLength {
		candidate = strings.TrimRight(candidate[:model.EmojiNameMaxLength], "_-")
	}
	return candidate
}

func fallbackEmojiName(original string) string {
	sum := sha256.Sum256([]byte(original))
	return "emoji_" + hex.EncodeToString(sum[:8])
}

// SanitizeEmojiName converts a reaction/custom emoji name to a Mattermost-valid
// form, using a per-Exporter sanitizer so mappings stay consistent for the run.
func (e *Exporter) SanitizeEmojiName(name string) string {
	if e.emojiNames == nil {
		e.emojiNames = NewEmojiNameSanitizer(e.Logger)
	}
	return e.emojiNames.Sanitize(name)
}

// EmojiNameMapping returns the original→sanitized emoji name map for this export
// run, or nil if no names have been sanitized yet.
func (e *Exporter) EmojiNameMapping() map[string]string {
	if e == nil || e.emojiNames == nil {
		return nil
	}
	return e.emojiNames.Mapping()
}
