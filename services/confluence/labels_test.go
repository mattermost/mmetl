package confluence

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// sampleLabels mixes the shapes the discovery sample contains with the one it
// does not. All 11 labellings in the real export target a SpaceDescription, and
// 10 of the 11 labels are personal "my" favourites rather than shared content
// metadata; the page labellings here are synthetic, because the sample has
// none.
const sampleLabels = `<object class="Label" package="com.atlassian.confluence.labels">
<id name="id">26542271</id>
<property name="name"><![CDATA[collaboration]]></property>
<property name="namespace"><![CDATA[team]]></property>
</object>
<object class="Label" package="com.atlassian.confluence.labels">
<id name="id">35389489</id>
<property name="name"><![CDATA[favourite]]></property>
<property name="namespace"><![CDATA[my]]></property>
<property name="owningUser" class="ConfluenceUserImpl" package="com.atlassian.confluence.user"><id name="key"><![CDATA[712020:c531b087]]></id>
</property>
</object>
<object class="Label" package="com.atlassian.confluence.labels">
<id name="id">26542999</id>
<property name="name"><![CDATA[runbook]]></property>
<property name="namespace"><![CDATA[global]]></property>
</object>
<object class="Labelling" package="com.atlassian.confluence.labels">
<id name="id">1001</id>
<property name="label" class="Label" package="com.atlassian.confluence.labels"><id name="id">26542999</id>
</property>
<property name="content" class="Page" package="com.atlassian.confluence.pages"><id name="id">26542273</id>
</property>
<property name="owningUser" class="ConfluenceUserImpl" package="com.atlassian.confluence.user"><id name="key"><![CDATA[5adea181]]></id>
</property>
</object>
<object class="Labelling" package="com.atlassian.confluence.labels">
<id name="id">1002</id>
<property name="label" class="Label" package="com.atlassian.confluence.labels"><id name="id">26542271</id>
</property>
<property name="content" class="Page" package="com.atlassian.confluence.pages"><id name="id">26542273</id>
</property>
</object>
<object class="Labelling" package="com.atlassian.confluence.labels">
<id name="id">1003</id>
<property name="label" class="Label" package="com.atlassian.confluence.labels"><id name="id">35389489</id>
</property>
<property name="content" class="SpaceDescription" package="com.atlassian.confluence.spaces"><id name="id">26542083</id>
</property>
</object>
<object class="Labelling" package="com.atlassian.confluence.labels">
<id name="id">1004</id>
<property name="label" class="Label" package="com.atlassian.confluence.labels"><id name="id">26542271</id>
</property>
<property name="content" class="Page" package="com.atlassian.confluence.pages"><id name="id">99999</id>
</property>
</object>`

func labelFixture(t *testing.T) (*ContentSelection, *LabelSelection, *UserRefs) {
	t.Helper()

	archive := archiveWithEntities(t, sampleLabels)

	page := &Page{SourceID: "26542273"}
	content := &ContentSelection{
		SpaceSourceID: "26542084",
		Pages:         []*Page{page},
		ByID:          map[string]*Page{"26542273": page},
	}

	refs := NewUserRefs()
	selection, err := SelectLabels(archive, content, refs)
	require.NoError(t, err)

	return content, selection, refs
}

func TestSelectLabels(t *testing.T) {
	content, selection, refs := labelFixture(t)

	page := content.ByID["26542273"]
	require.Len(t, page.Labels, 2)
	require.Equal(t, 2, selection.LabelsPreserved)

	t.Run("labels are ordered by namespace then name", func(t *testing.T) {
		require.Equal(t, []Label{
			{Name: "runbook", Namespace: "global", OwnerKey: confluenceUserKey("5adea181")},
			{Name: "collaboration", Namespace: "team"},
		}, page.Labels)
	})

	t.Run("label owners join the user closure", func(t *testing.T) {
		require.True(t, refs.Has(confluenceUserKey("5adea181")))
	})

	// This is not an edge case: every labelling in the real export is one of
	// these, so a space's labels are simply not carried in this iteration.
	t.Run("space description labellings are not page labels", func(t *testing.T) {
		for _, label := range page.Labels {
			require.NotEqual(t, "favourite", label.Name)
		}
	})

	t.Run("labellings on pages that were not emitted are ignored", func(t *testing.T) {
		require.Equal(t, 2, selection.LabelsPreserved, "the labelling on page 99999 is not counted")
	})
}

// TestSelectLabels_ReportsRestrictionsUnverified pins the standing warning. It
// is emitted on every export, not only when something is found: silence here
// would read as "this space has no restricted pages" when it actually means
// "restrictions were never looked for".
func TestSelectLabels_ReportsRestrictionsUnverified(t *testing.T) {
	_, selection, _ := labelFixture(t)

	require.Zero(t, selection.RestrictedPagesPreserved)

	var found bool
	for _, warning := range selection.Warnings {
		if warning.Code == WarnRestrictionUnverified {
			found = true
			require.Equal(t, "26542084", warning.SourceID)
			require.Contains(t, warning.Message, "Space-level")
		}
	}
	require.True(t, found)
}

func TestSelectLabels_NoLabels(t *testing.T) {
	archive := archiveWithEntities(t, `<object class="Page" package="com.atlassian.confluence.pages">
<id name="id">1</id>
</object>`)
	content := &ContentSelection{SpaceSourceID: "26542084", ByID: map[string]*Page{}}

	selection, err := SelectLabels(archive, content, NewUserRefs())
	require.NoError(t, err)
	require.Zero(t, selection.LabelsPreserved)
	require.Len(t, selection.Warnings, 1, "the restriction warning still stands")
}

// TestLabelNames pins the flat list the bundle carries. Personal-namespace
// labels are included: section 22 stores labels as inert metadata, and the
// structured list keeps the namespace so a consumer can still tell a shared
// team label from one person's private favourite.
func TestLabelNames(t *testing.T) {
	require.Equal(t, []string{"collaboration", "favourite", "runbook"}, LabelNames([]Label{
		{Name: "runbook", Namespace: "global"},
		{Name: "collaboration", Namespace: "team"},
		{Name: "favourite", Namespace: "my"},
	}))

	require.Equal(t, []string{"dup"}, LabelNames([]Label{
		{Name: "dup", Namespace: "global"},
		{Name: "dup", Namespace: "team"},
	}), "the flat list carries each name once")

	require.Empty(t, LabelNames(nil))
}

func TestRestrictionsIsEmpty(t *testing.T) {
	require.True(t, Restrictions{}.IsEmpty())
	require.False(t, Restrictions{ViewUsers: []string{"a"}}.IsEmpty())
	require.False(t, Restrictions{EditGroups: []string{"g"}}.IsEmpty())
}

// TestSelectLabels_PrivateSample is the E9 gate against a real export. It
// documents the finding that shaped this task: the sample carries labels, but
// none of them is on a page.
//
//	CONFLUENCE_SAMPLE_ZIP=/path/to/Confluence-export.zip go test ./services/confluence/...
func TestSelectLabels_PrivateSample(t *testing.T) {
	path := os.Getenv("CONFLUENCE_SAMPLE_ZIP")
	if path == "" {
		t.Skip("set CONFLUENCE_SAMPLE_ZIP to run against a real Confluence export")
	}

	archive, err := OpenSourceArchive(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, archive.Close()) }()

	spaces, descriptor, err := CatalogSpaces(archive)
	require.NoError(t, err)

	totalLabels := 0
	for _, space := range spaces {
		content, err := SelectPageMetadata(archive, space, descriptor)
		require.NoError(t, err)

		refs := NewUserRefs()
		selection, err := SelectLabels(archive, content, refs)
		require.NoErrorf(t, err, "space %s", space.SpaceKey)

		totalLabels += selection.LabelsPreserved
		require.Zero(t, selection.RestrictedPagesPreserved,
			"no page restriction may be claimed until a real fixture proves the format")

		for _, page := range content.Pages {
			require.True(t, page.Restrictions.IsEmpty())
		}
	}
	t.Logf("page labels preserved across all spaces: %d", totalLabels)
}
