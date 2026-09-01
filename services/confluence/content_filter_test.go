package confluence

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

var testSelectedSpace = EntityKey{Package: PkgConfluenceSpaces, Class: "Space", IDName: "id", ID: "26542084"}

var testOtherSpace = EntityKey{Package: PkgConfluenceSpaces, Class: "Space", IDName: "id", ID: "63864834"}

// candidate builds a canonical candidate that the predicate accepts, so each
// test can negate exactly one condition.
func candidate(id string, mutate ...func(*ContentCandidate)) ContentCandidate {
	c := ContentCandidate{
		Page: &Page{
			Key:         EntityKey{Package: PkgConfluencePages, Class: "Page", IDName: "id", ID: id},
			SourceID:    id,
			ContentType: ContentTypePage,
			Title:       "Page " + id,
			SpaceKey:    testSelectedSpace,
		},
		Status: "current",
	}
	for _, m := range mutate {
		m(&c)
	}
	return c
}

func TestIsCanonicalCandidate(t *testing.T) {
	tests := []struct {
		name      string
		candidate ContentCandidate
		want      bool
	}{
		{
			name:      "current page in the selected space",
			candidate: candidate("1"),
			want:      true,
		},
		{
			name:      "another space",
			candidate: candidate("1", func(c *ContentCandidate) { c.Page.SpaceKey = testOtherSpace }),
		},
		{
			name:      "no space reference at all",
			candidate: candidate("1", func(c *ContentCandidate) { c.Page.SpaceKey = EntityKey{} }),
		},
		{
			name:      "draft",
			candidate: candidate("1", func(c *ContentCandidate) { c.Status = "draft" }),
		},
		{
			name:      "deleted",
			candidate: candidate("1", func(c *ContentCandidate) { c.Status = "deleted" }),
		},
		{
			name:      "status is compared case-insensitively",
			candidate: candidate("1", func(c *ContentCandidate) { c.Status = "CURRENT" }),
			want:      true,
		},
		{
			name: "has an originalVersion reference",
			candidate: candidate("1", func(c *ContentCandidate) {
				c.HasOriginalVersion = true
			}),
		},
		{
			name:      "originalVersionId names another object",
			candidate: candidate("1", func(c *ContentCandidate) { c.OriginalVersionID = "999" }),
		},
		// Confluence writes an absent original version three ways. Reading "0"
		// as a real id would exclude every current page in such an export.
		{
			name:      "originalVersionId is empty",
			candidate: candidate("1", func(c *ContentCandidate) { c.OriginalVersionID = "" }),
			want:      true,
		},
		{
			name:      "originalVersionId is zero",
			candidate: candidate("1", func(c *ContentCandidate) { c.OriginalVersionID = "0" }),
			want:      true,
		},
		{
			name:      "originalVersionId is whitespace",
			candidate: candidate("1", func(c *ContentCandidate) { c.OriginalVersionID = "  " }),
			want:      true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.want, test.candidate.IsCanonicalCandidate(testSelectedSpace))
		})
	}
}

func TestSelectCanonical(t *testing.T) {
	t.Run("excludes objects listed as historical versions", func(t *testing.T) {
		current := candidate("100", func(c *ContentCandidate) {
			c.HistoricalVersions = []EntityKey{
				{Package: PkgConfluencePages, Class: "Page", IDName: "id", ID: "101"},
			}
		})
		// A historical version that somehow still looks current on its own.
		stale := candidate("101")

		pages, err := SelectCanonical([]ContentCandidate{current, stale}, testSelectedSpace)
		require.NoError(t, err)
		require.Equal(t, []string{"100"}, pageIDs(pages))
	})

	t.Run("historical lists from excluded objects are ignored", func(t *testing.T) {
		// An object in another space must not be able to exclude a page from
		// the selected space.
		foreign := candidate("200", func(c *ContentCandidate) {
			c.Page.SpaceKey = testOtherSpace
			c.HistoricalVersions = []EntityKey{
				{Package: PkgConfluencePages, Class: "Page", IDName: "id", ID: "201"},
			}
		})
		selected := candidate("201")

		pages, err := SelectCanonical([]ContentCandidate{foreign, selected}, testSelectedSpace)
		require.NoError(t, err)
		require.Equal(t, []string{"201"}, pageIDs(pages))
	})

	t.Run("rejects two canonical objects for one logical page", func(t *testing.T) {
		// Both claim to be version-of 500 while neither carries the markers
		// that would make them historical.
		first := candidate("501", func(c *ContentCandidate) { c.Page.SourceID = "501" })
		second := candidate("502", func(c *ContentCandidate) { c.Page.SourceID = "502" })
		first.Page.Key.ID = "501"
		second.Page.Key.ID = "502"

		// Force both into one logical group by giving them the same source id,
		// which is what a corrupt export looks like after key dedup.
		second.Page.SourceID = "501"

		_, err := SelectCanonical([]ContentCandidate{first, second}, testSelectedSpace)
		require.ErrorContains(t, err, ErrCodeContentDuplicateCanonical)
		require.ErrorContains(t, err, "claim to be the current version")
	})

	t.Run("empty input", func(t *testing.T) {
		pages, err := SelectCanonical(nil, testSelectedSpace)
		require.NoError(t, err)
		require.Empty(t, pages)
	})
}

func pageIDs(pages []*Page) []string {
	ids := make([]string, 0, len(pages))
	for _, page := range pages {
		ids = append(ids, page.SourceID)
	}
	return ids
}

// sampleContent reproduces the shapes the private export contains: a canonical
// root page, three canonical children with positions, a historical version
// carrying both exclusion markers, a page from another space, and a blog post.
const sampleContent = `<object class="Page" package="com.atlassian.confluence.pages">
<id name="id">26542273</id>
<property name="title"><![CDATA[dkh-space]]></property>
<property name="creationDate">2025-11-14 16:53:35.571</property>
<property name="lastModificationDate">2025-11-14 16:53:35.795</property>
<property name="originalVersionId"/><property name="contentStatus"><![CDATA[current]]></property>
<property name="creator" class="ConfluenceUserImpl" package="com.atlassian.confluence.user"><id name="key"><![CDATA[5adea181]]></id>
</property>
<property name="space" class="Space" package="com.atlassian.confluence.spaces"><id name="id">26542084</id>
</property>
<property name="position">875</property>
<collection name="bodyContents" class="java.util.Collection"><element class="BodyContent" package="com.atlassian.confluence.core"><id name="id">26542274</id>
</element>
</collection>
<collection name="historicalVersions" class="java.util.Collection"><element class="Page" package="com.atlassian.confluence.pages"><id name="id">26542999</id>
</element>
</collection>
</object>
<object class="Page" package="com.atlassian.confluence.pages">
<id name="id">26542289</id>
<property name="title"><![CDATA[Template - Project plan]]></property>
<property name="creationDate">2025-11-14 16:53:36.000</property>
<property name="originalVersionId"/><property name="contentStatus"><![CDATA[current]]></property>
<property name="space" class="Space" package="com.atlassian.confluence.spaces"><id name="id">26542084</id>
</property>
<property name="parent" class="Page" package="com.atlassian.confluence.pages"><id name="id">26542273</id>
</property>
<property name="position">540</property>
</object>
<object class="Page" package="com.atlassian.confluence.pages">
<id name="id">26542301</id>
<property name="title"><![CDATA[Template - Decision documentation]]></property>
<property name="creationDate">2025-11-14 16:53:37.000</property>
<property name="originalVersionId"/><property name="contentStatus"><![CDATA[current]]></property>
<property name="space" class="Space" package="com.atlassian.confluence.spaces"><id name="id">26542084</id>
</property>
<property name="parent" class="Page" package="com.atlassian.confluence.pages"><id name="id">26542273</id>
</property>
<property name="position">1344</property>
</object>
<object class="Page" package="com.atlassian.confluence.pages">
<id name="id">26542313</id>
<property name="title"><![CDATA[Template - Meeting notes]]></property>
<property name="creationDate">2025-11-14 16:53:38.000</property>
<property name="originalVersionId"/><property name="contentStatus"><![CDATA[current]]></property>
<property name="space" class="Space" package="com.atlassian.confluence.spaces"><id name="id">26542084</id>
</property>
<property name="parent" class="Page" package="com.atlassian.confluence.pages"><id name="id">26542273</id>
</property>
<property name="position">1620</property>
</object>
<object class="Page" package="com.atlassian.confluence.pages">
<id name="id">26542999</id>
<property name="title"><![CDATA[dkh-space]]></property>
<property name="originalVersionId">26542273</property>
<property name="contentStatus"><![CDATA[current]]></property>
<property name="originalVersion" class="Page" package="com.atlassian.confluence.pages"><id name="id">26542273</id>
</property>
<property name="space" class="Space" package="com.atlassian.confluence.spaces"><id name="id">26542084</id>
</property>
</object>
<object class="Page" package="com.atlassian.confluence.pages">
<id name="id">63864969</id>
<property name="title"><![CDATA[Another space entirely]]></property>
<property name="originalVersionId"/><property name="contentStatus"><![CDATA[current]]></property>
<property name="space" class="Space" package="com.atlassian.confluence.spaces"><id name="id">63864834</id>
</property>
</object>
<object class="Page" package="com.atlassian.confluence.pages">
<id name="id">26542400</id>
<property name="title"><![CDATA[Archived page]]></property>
<property name="originalVersionId"/><property name="contentStatus"><![CDATA[deleted]]></property>
<property name="space" class="Space" package="com.atlassian.confluence.spaces"><id name="id">26542084</id>
</property>
</object>
<object class="BlogPost" package="com.atlassian.confluence.pages">
<id name="id">26542500</id>
<property name="title"><![CDATA[Quarterly update]]></property>
<property name="creationDate">2026-01-08 09:00:00.000</property>
<property name="originalVersionId"/><property name="contentStatus"><![CDATA[current]]></property>
<property name="space" class="Space" package="com.atlassian.confluence.spaces"><id name="id">26542084</id>
</property>
</object>`

func TestSelectPageMetadata(t *testing.T) {
	archive := archiveWithEntities(t, sampleContent)
	spaces, descriptor, err := CatalogSpaces(archive)
	require.NoError(t, err)
	require.Empty(t, spaces, "this fixture declares no Space objects, so the space is supplied directly")

	space := Space{Key: testSelectedSpace, SourceID: "26542084", SpaceKey: "dkhspace"}

	selection, err := SelectPageMetadata(archive, space, descriptor)
	require.NoError(t, err)

	// Discovery is scoped to the selected space, so discovered equals emitted
	// plus skipped. The fixture holds 8 page objects; 5 of them are this
	// space's canonical content.
	require.Equal(t, 5, selection.PagesDiscovered)
	require.Equal(t, 8, selection.ObjectsScanned)
	require.Equal(t, 4, selection.PagesEmitted)
	require.Equal(t, 1, selection.BlogPostsEmitted)
	require.Equal(t, 0, selection.PagesFlattened)

	t.Run("emission order is parent-first and siblings follow position", func(t *testing.T) {
		require.Equal(t, []string{
			"26542273", // root, position 875
			"26542289", // child, position 540
			"26542301", // child, position 1344
			"26542313", // child, position 1620
			"26542500", // blog post, no position, sorts after the positioned root
		}, pageIDs(selection.Pages))
	})

	t.Run("root page metadata", func(t *testing.T) {
		root := selection.ByID["26542273"]
		require.Equal(t, ContentTypePage, root.ContentType)
		require.Equal(t, "dkh-space", root.Title)
		require.Equal(t, 1, root.Depth)
		require.Empty(t, root.ParentSourceID)
		require.Equal(t, "5adea181", root.CreatorKey.ID)
		require.Equal(t, "key", root.CreatorKey.IDName)
		require.Len(t, root.BodyContentKeys, 1)
		require.Equal(t, "26542274", root.BodyContentKeys[0].ID)

		// 2025-11-14 16:53:35.571 in America/Vancouver, the export's zone.
		require.True(t, root.HasCreatedAt)
		expected := time.Date(2025, time.November, 14, 16, 53, 35, 571000000, descriptor.Location)
		require.Equal(t, expected.UnixMilli(), root.CreatedAt)
	})

	t.Run("children are depth 2 under the root", func(t *testing.T) {
		for _, id := range []string{"26542289", "26542301", "26542313"} {
			child := selection.ByID[id]
			require.Equal(t, 2, child.Depth, id)
			require.Equal(t, "26542273", child.ParentSourceID, id)
		}
	})

	t.Run("blog post is a root page tagged by content type", func(t *testing.T) {
		blog := selection.ByID["26542500"]
		require.True(t, blog.IsBlogPost())
		require.Equal(t, ContentTypeBlogPost, blog.ContentType)
		require.Equal(t, 1, blog.Depth)
		require.Empty(t, blog.ParentSourceID)
		require.False(t, blog.HasPosition)
	})

	t.Run("excluded content", func(t *testing.T) {
		require.NotContains(t, selection.ByID, "26542999", "historical version")
		require.NotContains(t, selection.ByID, "63864969", "another space")
		require.NotContains(t, selection.ByID, "26542400", "deleted")
	})
}

func TestSelectPageMetadata_PromotesOrphans(t *testing.T) {
	orphaned := `<object class="Page" package="com.atlassian.confluence.pages">
<id name="id">10</id>
<property name="title"><![CDATA[Orphan]]></property>
<property name="originalVersionId"/><property name="contentStatus"><![CDATA[current]]></property>
<property name="space" class="Space" package="com.atlassian.confluence.spaces"><id name="id">26542084</id>
</property>
<property name="parent" class="Page" package="com.atlassian.confluence.pages"><id name="id">999</id>
</property>
</object>`

	archive := archiveWithEntities(t, orphaned)
	space := Space{Key: testSelectedSpace, SourceID: "26542084"}

	selection, err := SelectPageMetadata(archive, space, Descriptor{Location: time.UTC})
	require.NoError(t, err)

	require.Equal(t, []string{"10"}, pageIDs(selection.Pages), "an orphan is promoted, never dropped")
	require.Equal(t, 1, selection.ByID["10"].Depth)

	require.Len(t, selection.Warnings, 1)
	require.Equal(t, WarnPageParentMissingPromoted, selection.Warnings[0].Code)
	require.Equal(t, "10", selection.Warnings[0].SourceID)
	require.Contains(t, selection.Warnings[0].Message, "999")
}

// TestSelectPageMetadata_PrivateSample is the E6 gate: the selected IDs from a
// real export.
//
//	CONFLUENCE_SAMPLE_ZIP=/path/to/Confluence-export.zip go test ./services/confluence/...
func TestSelectPageMetadata_PrivateSample(t *testing.T) {
	path := os.Getenv("CONFLUENCE_SAMPLE_ZIP")
	if path == "" {
		t.Skip("set CONFLUENCE_SAMPLE_ZIP to run against a real Confluence export")
	}

	archive, err := OpenSourceArchive(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, archive.Close()) }()

	spaces, descriptor, err := CatalogSpaces(archive)
	require.NoError(t, err)

	totalEmitted := 0
	for _, space := range spaces {
		selection, err := SelectPageMetadata(archive, space, descriptor)
		require.NoErrorf(t, err, "space %s", space.SpaceKey)

		totalEmitted += len(selection.Pages)

		seen := map[string]bool{}
		for _, page := range selection.Pages {
			require.False(t, seen[page.SourceID], "page %s emitted twice", page.SourceID)
			seen[page.SourceID] = true

			require.NotEmpty(t, page.SourceID)
			require.Equal(t, space.Key, page.SpaceKey, "a selected page must belong to the selected space")
			require.GreaterOrEqual(t, page.Depth, 1)
			require.LessOrEqual(t, page.Depth, MaxPageDepth)

			if page.ParentSourceID != "" {
				require.True(t, seen[page.ParentSourceID],
					"page %s appears before its parent %s", page.SourceID, page.ParentSourceID)
			}
		}

		t.Logf("%-42s %2d pages, %d blog posts, %d flattened, %d warnings (of %d discovered)",
			space.SpaceKey, selection.PagesEmitted, selection.BlogPostsEmitted,
			selection.PagesFlattened, len(selection.Warnings), selection.PagesDiscovered)
	}

	// Independently counted from the sample: 28 canonical pages and blog posts
	// across all 11 spaces.
	require.Equal(t, 28, totalEmitted)
}
