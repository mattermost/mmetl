package confluence

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func pageKey(id string) EntityKey {
	return EntityKey{Package: PkgConfluencePages, Class: "Page", IDName: "id", ID: id}
}

func blogKey(id string) EntityKey {
	return EntityKey{Package: PkgConfluencePages, Class: "BlogPost", IDName: "id", ID: id}
}

func descriptionKey(id string) EntityKey {
	return EntityKey{Package: PkgConfluenceSpaces, Class: "SpaceDescription", IDName: "id", ID: id}
}

func attachmentCandidate(id string, container EntityKey, mutate ...func(*AttachmentCandidate)) AttachmentCandidate {
	c := AttachmentCandidate{
		Attachment: &Attachment{
			Key:               EntityKey{Package: PkgConfluencePages, Class: "Attachment", IDName: "id", ID: id},
			SourceID:          id,
			ContainerKey:      container,
			ContainerSourceID: container.ID,
			Filename:          "file-" + id + ".png",
			Version:           1,
		},
		Status: "current",
	}
	for _, m := range mutate {
		m(&c)
	}
	return c
}

func TestAttachmentCandidateIsEligible(t *testing.T) {
	emitted := map[string]*Page{
		"100": {SourceID: "100"},
		"200": {SourceID: "200"},
	}
	spaceDescription := descriptionKey("26542083")

	tests := []struct {
		name      string
		candidate AttachmentCandidate
		want      bool
	}{
		{
			name:      "on an emitted page",
			candidate: attachmentCandidate("1", pageKey("100")),
			want:      true,
		},
		{
			name:      "on an emitted blog post",
			candidate: attachmentCandidate("1", blogKey("200")),
			want:      true,
		},
		{
			name:      "on the selected space description",
			candidate: attachmentCandidate("1", spaceDescription),
			want:      true,
		},
		{
			name:      "on a page that was not emitted",
			candidate: attachmentCandidate("1", pageKey("999")),
		},
		{
			name:      "on another space's description",
			candidate: attachmentCandidate("1", descriptionKey("999")),
		},
		{
			name:      "not current",
			candidate: attachmentCandidate("1", pageKey("100"), func(c *AttachmentCandidate) { c.Status = "deleted" }),
		},
		{
			name:      "status is compared case-insensitively",
			candidate: attachmentCandidate("1", pageKey("100"), func(c *AttachmentCandidate) { c.Status = "Current" }),
			want:      true,
		},
		{
			name: "a historical version",
			candidate: attachmentCandidate("1", pageKey("100"), func(c *AttachmentCandidate) {
				c.HasOriginalVersion = true
			}),
		},
		// A container id that merely collides with an emitted page id must not
		// pull in an attachment hanging off some other class.
		{
			name: "container class is not a page",
			candidate: attachmentCandidate("1", EntityKey{
				Package: PkgConfluenceContent, Class: "CustomContentEntityObject", IDName: "id", ID: "100",
			}),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.want, test.candidate.IsEligible(emitted, spaceDescription))
		})
	}
}

func TestResolveDestination(t *testing.T) {
	emitted := map[string]*Page{"26542273": {SourceID: "26542273"}}

	space := Space{
		Key:            testSelectedSpace,
		SourceID:       "26542084",
		HomePageKey:    pageKey("26542273"),
		DescriptionKey: descriptionKey("26542083"),
	}

	t.Run("a page attachment stays on its page", func(t *testing.T) {
		attachment := attachmentCandidate("1", pageKey("26542273")).Attachment

		pageID, warning := resolveDestination(attachment, space, emitted)
		require.Nil(t, warning)
		require.Equal(t, "26542273", pageID)
	})

	// The single attachment in the discovery sample is exactly this case.
	t.Run("a space description attachment moves to the home page", func(t *testing.T) {
		attachment := attachmentCandidate("26542119", descriptionKey("26542083")).Attachment

		pageID, warning := resolveDestination(attachment, space, emitted)
		require.Nil(t, warning)
		require.Equal(t, "26542273", pageID, "remapped onto the source home page")
		require.Equal(t, "26542083", attachment.ContainerSourceID, "the original container is preserved")
	})

	t.Run("skipped when the space has no home page", func(t *testing.T) {
		noHome := space
		noHome.HomePageKey = EntityKey{}
		attachment := attachmentCandidate("26542119", descriptionKey("26542083")).Attachment

		pageID, warning := resolveDestination(attachment, noHome, emitted)
		require.Empty(t, pageID)
		require.NotNil(t, warning)
		require.Equal(t, WarnAttachmentHomePageMissing, warning.Code)
	})

	t.Run("skipped when the home page was not emitted", func(t *testing.T) {
		attachment := attachmentCandidate("26542119", descriptionKey("26542083")).Attachment

		pageID, warning := resolveDestination(attachment, space, map[string]*Page{})
		require.Empty(t, pageID)
		require.NotNil(t, warning)
		require.Equal(t, WarnAttachmentHomePageMissing, warning.Code)
		require.Contains(t, warning.Message, "26542273")
	})
}

// sampleDependencies reproduces the discovery sample's shapes: one page, one
// comment on it, one comment on a page that was never emitted, and one
// space-description attachment with its media type and size in separate
// ContentProperty objects.
const sampleDependencies = `<object class="Comment" package="com.atlassian.confluence.pages">
<id name="id">33226755</id>
<property name="creationDate">2025-12-09 14:39:16.330</property>
<property name="lastModificationDate">2025-12-09 14:39:16.330</property>
<property name="originalVersionId"/><property name="contentStatus"><![CDATA[current]]></property>
<property name="creator" class="ConfluenceUserImpl" package="com.atlassian.confluence.user"><id name="key"><![CDATA[712020:35e13a5b]]></id>
</property>
<property name="containerContent" class="Page" package="com.atlassian.confluence.pages"><id name="id">26542273</id>
</property>
<collection name="bodyContents" class="java.util.Collection"><element class="BodyContent" package="com.atlassian.confluence.core"><id name="id">33226756</id>
</element>
</collection>
</object>
<object class="Comment" package="com.atlassian.confluence.pages">
<id name="id">33226800</id>
<property name="creationDate">2025-12-09 15:00:00.000</property>
<property name="originalVersionId"/><property name="contentStatus"><![CDATA[current]]></property>
<property name="containerContent" class="Page" package="com.atlassian.confluence.pages"><id name="id">99999</id>
</property>
</object>
<object class="Comment" package="com.atlassian.confluence.pages">
<id name="id">33226810</id>
<property name="creationDate">2025-12-09 16:00:00.000</property>
<property name="originalVersionId"/><property name="contentStatus"><![CDATA[deleted]]></property>
<property name="containerContent" class="Page" package="com.atlassian.confluence.pages"><id name="id">26542273</id>
</property>
</object>
<object class="Attachment" package="com.atlassian.confluence.pages">
<id name="id">26542119</id>
<property name="title"><![CDATA[dkhspace]]></property>
<collection name="contentProperties" class="java.util.Collection"><element class="ContentProperty" package="com.atlassian.confluence.content"><id name="id">26542120</id>
</element>
<element class="ContentProperty" package="com.atlassian.confluence.content"><id name="id">26542121</id>
</element>
</collection>
<property name="version">1</property>
<property name="creator" class="ConfluenceUserImpl" package="com.atlassian.confluence.user"><id name="key"><![CDATA[5adea181]]></id>
</property>
<property name="creationDate">2025-11-14 16:53:34.299</property>
<property name="originalVersionId"/><property name="contentStatus"><![CDATA[current]]></property>
<property name="containerContent" class="SpaceDescription" package="com.atlassian.confluence.spaces"><id name="id">26542083</id>
</property>
<property name="space" class="Space" package="com.atlassian.confluence.spaces"><id name="id">26542084</id>
</property>
</object>
<object class="ContentProperty" package="com.atlassian.confluence.content">
<id name="id">26542120</id>
<property name="name"><![CDATA[MEDIA_TYPE]]></property>
<property name="stringValue"><![CDATA[image/png]]></property>
<property name="longValue"/><property name="dateValue"/>
</object>
<object class="ContentProperty" package="com.atlassian.confluence.content">
<id name="id">26542121</id>
<property name="name"><![CDATA[FILESIZE]]></property>
<property name="stringValue"/><property name="longValue">8497</property>
<property name="dateValue"/>
</object>`

func dependencyFixture(t *testing.T, skipAttachments bool) *DependencySelection {
	t.Helper()

	archive := archiveWithEntities(t, sampleDependencies)
	space := Space{
		Key:            testSelectedSpace,
		SourceID:       "26542084",
		HomePageKey:    pageKey("26542273"),
		DescriptionKey: descriptionKey("26542083"),
	}
	content := &ContentSelection{
		Pages: []*Page{{SourceID: "26542273"}},
		ByID:  map[string]*Page{"26542273": {SourceID: "26542273"}},
	}

	selection, err := SelectDependencies(archive, space, Descriptor{Location: time.UTC}, content, skipAttachments)
	require.NoError(t, err)
	return selection
}

func TestSelectDependencies(t *testing.T) {
	selection := dependencyFixture(t, false)

	t.Run("comments", func(t *testing.T) {
		// Only one of the three comment objects is a candidate for this space:
		// one hangs off a page that was not emitted and one is deleted.
		require.Equal(t, 1, selection.CommentsDiscovered)
		require.Equal(t, 1, selection.CommentsEmitted)
		require.Zero(t, selection.CommentsSkipped)
		require.Equal(t, 4, selection.ObjectsScanned)

		comment := selection.Comments[0]
		require.Equal(t, "33226755", comment.SourceID)
		require.Equal(t, "26542273", comment.PageSourceID)
		require.Empty(t, comment.ParentSourceID)
		require.Equal(t, "33226755", comment.ThreadRootSourceID,
			"a top-level comment is its own thread root")
		require.Equal(t, "712020:35e13a5b", comment.CreatorKey.ID)
		require.Len(t, comment.BodyContentKeys, 1)
	})

	t.Run("attachment is remapped onto the home page with its properties", func(t *testing.T) {
		require.Equal(t, 1, selection.AttachmentsDiscovered)
		require.Equal(t, 1, selection.AttachmentsEmitted)
		require.Zero(t, selection.AttachmentsSkipped)

		attachments := selection.Attachments["26542273"]
		require.Len(t, attachments, 1)

		attachment := attachments[0]
		require.Equal(t, "26542119", attachment.SourceID)
		require.Equal(t, "26542083", attachment.ContainerSourceID)
		require.Equal(t, "dkhspace", attachment.Filename)
		require.Equal(t, 1, attachment.Version)
		require.Equal(t, "image/png", attachment.MediaType)
		require.Equal(t, int64(8497), attachment.Size)

		// The path is built from the original container, not the destination
		// page, because that is where the bytes live in the archive.
		require.Equal(t, "attachments/26542083/26542119/1", attachment.ArchivePath)
	})
}

// TestSelectDependencies_SkipAttachments checks that the flag suppresses
// metadata as well as bytes: a bundle must never claim an attachment the
// importer cannot find.
func TestSelectDependencies_SkipAttachments(t *testing.T) {
	selection := dependencyFixture(t, true)

	require.Empty(t, selection.Attachments)
	require.Empty(t, selection.AttachmentsByID)
	require.Zero(t, selection.AttachmentsEmitted)
	require.Equal(t, 1, selection.AttachmentsSkipped)

	var found bool
	for _, warning := range selection.Warnings {
		if warning.Code == WarnAttachmentSkippedByFlag {
			found = true
			require.Contains(t, warning.Message, "1 attachment(s)")
		}
	}
	require.True(t, found, "the skipped attachments must be reported, not silently dropped")

	require.Equal(t, 1, selection.CommentsEmitted, "comments are unaffected")
}

// TestSelectDependencies_PrivateSample is the E7 gate against a real export.
//
//	CONFLUENCE_SAMPLE_ZIP=/path/to/Confluence-export.zip go test ./services/confluence/...
func TestSelectDependencies_PrivateSample(t *testing.T) {
	path := os.Getenv("CONFLUENCE_SAMPLE_ZIP")
	if path == "" {
		t.Skip("set CONFLUENCE_SAMPLE_ZIP to run against a real Confluence export")
	}

	archive, err := OpenSourceArchive(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, archive.Close()) }()

	spaces, descriptor, err := CatalogSpaces(archive)
	require.NoError(t, err)

	totalComments, totalAttachments := 0, 0
	for _, space := range spaces {
		content, err := SelectPageMetadata(archive, space, descriptor)
		require.NoError(t, err)

		deps, err := SelectDependencies(archive, space, descriptor, content, false)
		require.NoErrorf(t, err, "space %s", space.SpaceKey)

		totalComments += deps.CommentsEmitted
		totalAttachments += deps.AttachmentsEmitted

		for _, comment := range deps.Comments {
			require.Contains(t, content.ByID, comment.PageSourceID)
			require.NotEmpty(t, comment.ThreadRootSourceID)
			if comment.ParentSourceID == "" {
				require.Equal(t, comment.SourceID, comment.ThreadRootSourceID)
			}
		}
		for pageID, attachments := range deps.Attachments {
			require.Contains(t, content.ByID, pageID)
			for _, attachment := range attachments {
				require.NotEmpty(t, attachment.ArchivePath)
				_, ok := archive.Attachment(AttachmentRef{
					ContainerID:  attachment.ContainerSourceID,
					AttachmentID: attachment.SourceID,
					Version:      attachment.Version,
				})
				require.Truef(t, ok, "attachment %s has no blob at %s", attachment.SourceID, attachment.ArchivePath)
				require.NotEmpty(t, attachment.MediaType)
				require.Positive(t, attachment.Size)
			}
		}

		if deps.CommentsEmitted > 0 || deps.AttachmentsEmitted > 0 {
			t.Logf("%-42s %2d/%2d comments, %d/%d attachments",
				space.SpaceKey, deps.CommentsEmitted, deps.CommentsDiscovered,
				deps.AttachmentsEmitted, deps.AttachmentsDiscovered)
		}
	}

	// The sample carries exactly one attachment blob, on the dkhspace space
	// description, and it must be claimed by exactly one space.
	require.Equal(t, 1, totalAttachments)
	t.Logf("total: %d comments, %d attachments", totalComments, totalAttachments)
}
