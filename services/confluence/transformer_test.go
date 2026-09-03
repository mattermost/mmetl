package confluence

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestValidateOrganizationID pins the charset the destination can index.
//
// The Docs importer stores this in an indexed column and accepts only
// [A-Za-z0-9._:@~-], so a value it would reject has to fail here instead —
// at the flag, where the operator can still fix it.
func TestValidateOrganizationID(t *testing.T) {
	t.Run("accepted", func(t *testing.T) {
		for _, value := range []string{
			"example.atlassian.net",
			"mattermost.atlassian.net",
			"example",
			"a-b_c.d",
			"tenant:1",
			"team@example.net",
			"~personal",
			strings.Repeat("a", OrganizationIDMaxBytes),
			"  trimmed.example.net  ",
		} {
			require.NoErrorf(t, ValidateOrganizationID(value), "%q", value)
		}
	})

	t.Run("rejected", func(t *testing.T) {
		for value, wantErr := range map[string]string{
			"":                              "is required",
			"   ":                           "is required",
			"https://example.atlassian.net": "may only contain",
			"example.atlassian.net/wiki":    "may only contain",
			"example.atlassian.net?x=1":     "may only contain",
			"has space":                     "may only contain",
			"café.example.net":              "may only contain",
			"has\x00nul":                    "must not contain NUL",
			strings.Repeat("a", OrganizationIDMaxBytes+1): "over the 1024 byte limit",
		} {
			err := ValidateOrganizationID(value)
			require.Errorf(t, err, "%q", value)
			require.Containsf(t, err.Error(), wantErr, "%q", value)
		}
	})

	// The error names the value to pass, so the operator does not have to guess
	// which characters offended.
	t.Run("the error suggests a usable value", func(t *testing.T) {
		err := ValidateOrganizationID("https://example.atlassian.net/wiki")
		require.ErrorContains(t, err, `"example.atlassian.net.wiki"`)
	})

	// Nothing is normalized behind the operator's back: this value scopes every
	// source mapping, so silently rewriting it would split one site's history
	// across two identities.
	t.Run("a suggestion is never applied", func(t *testing.T) {
		require.Error(t, ValidateOrganizationID("https://example.atlassian.net"))
		require.Equal(t, "example.atlassian.net", suggestOrganizationID("https://example.atlassian.net/"))
		require.Equal(t, "example.atlassian.net", suggestOrganizationID("example.atlassian.net"))
		require.Equal(t, "example.atlassian.net", suggestOrganizationID("HTTPS://example.atlassian.net"[8:]))
		require.Equal(t, "example.atlassian.net", suggestOrganizationID("https://example.atlassian.net"))
	})

	t.Run("a value with nothing usable falls back to an example", func(t *testing.T) {
		require.Equal(t, "example.atlassian.net", suggestOrganizationID("///"))
	})
}

// TestDeriveSummaryCounts pins the four-count summary the Docs importer reads.
// It is derived from the breakdown so the two cannot disagree.
func TestDeriveSummaryCounts(t *testing.T) {
	counts := ManifestCounts{
		SpacesEmitted:      1,
		PagesEmitted:       4,
		BlogPostsEmitted:   2,
		CommentsEmitted:    7,
		AttachmentsEmitted: 3,
	}
	counts.deriveSummaryCounts()

	require.Equal(t, 1, counts.Spaces)
	require.Equal(t, 6, counts.Pages, "a blog post is emitted as a page, so it counts as one")
	require.Equal(t, 7, counts.Comments)
	require.Equal(t, 3, counts.Attachments)
}
