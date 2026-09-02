package confluence

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// sampleRestrictions is copied from the real export that first contained a
// restricted page: a ContentPermissionSet per restriction kind, each holding
// ContentPermission rows that name a user or a group.
//
// Only the user form has been seen in a real export. The group rows here are
// synthetic, which is why a group restriction still warns.
const sampleRestrictions = `<object class="ContentPermissionSet" package="com.atlassian.confluence.security">
<id name="id">65536011</id>
<property name="type"><![CDATA[View]]></property>
<collection name="contentPermissions" class="java.util.SortedSet"><element class="ContentPermission" package="com.atlassian.confluence.security"><id name="id">65536012</id>
</element>
</collection>
<property name="owningContent" class="Page" package="com.atlassian.confluence.pages"><id name="id">65536001</id>
</property>
<property name="creationDate">2026-09-01 12:13:50.248</property>
</object>
<object class="ContentPermission" package="com.atlassian.confluence.security">
<id name="id">65536012</id>
<property name="type"><![CDATA[View]]></property>
<property name="userSubject" class="ConfluenceUserImpl" package="com.atlassian.confluence.user"><id name="key"><![CDATA[5d3eaa4376cb3e0d9d31cf8e]]></id>
</property>
<property name="groupName"/><property name="owningSet" class="ContentPermissionSet" package="com.atlassian.confluence.security"><id name="id">65536011</id>
</property>
</object>
<object class="ContentPermissionSet" package="com.atlassian.confluence.security">
<id name="id">65536009</id>
<property name="type"><![CDATA[Edit]]></property>
<collection name="contentPermissions" class="java.util.SortedSet"><element class="ContentPermission" package="com.atlassian.confluence.security"><id name="id">65536010</id>
</element>
<element class="ContentPermission" package="com.atlassian.confluence.security"><id name="id">65536013</id>
</element>
</collection>
<property name="owningContent" class="Page" package="com.atlassian.confluence.pages"><id name="id">65536001</id>
</property>
</object>
<object class="ContentPermission" package="com.atlassian.confluence.security">
<id name="id">65536010</id>
<property name="type"><![CDATA[Edit]]></property>
<property name="userSubject" class="ConfluenceUserImpl" package="com.atlassian.confluence.user"><id name="key"><![CDATA[5d3eaa4376cb3e0d9d31cf8e]]></id>
</property>
<property name="groupName"/><property name="owningSet" class="ContentPermissionSet" package="com.atlassian.confluence.security"><id name="id">65536009</id>
</property>
</object>
<object class="ContentPermission" package="com.atlassian.confluence.security">
<id name="id">65536013</id>
<property name="type"><![CDATA[Edit]]></property>
<property name="groupName"><![CDATA[confluence-engineering]]></property>
<property name="owningSet" class="ContentPermissionSet" package="com.atlassian.confluence.security"><id name="id">65536009</id>
</property>
</object>
<object class="ContentPermissionSet" package="com.atlassian.confluence.security">
<id name="id">65536020</id>
<property name="type"><![CDATA[View]]></property>
<property name="owningContent" class="Page" package="com.atlassian.confluence.pages"><id name="id">99999</id>
</property>
</object>`

func restrictionFixture(t *testing.T) (*ContentSelection, *UserRefs, int, []Warning) {
	t.Helper()

	archive := archiveWithEntities(t, sampleRestrictions)

	restricted := &Page{SourceID: "65536001"}
	plain := &Page{SourceID: "65536002"}
	content := &ContentSelection{
		SpaceSourceID: "63864834",
		Pages:         []*Page{restricted, plain},
		ByID:          map[string]*Page{"65536001": restricted, "65536002": plain},
	}

	refs := NewUserRefs()
	count, warnings, err := SelectRestrictions(archive, content, refs)
	require.NoError(t, err)

	return content, refs, count, warnings
}

func TestSelectRestrictions(t *testing.T) {
	content, refs, count, warnings := restrictionFixture(t)

	require.Equal(t, 1, count, "one page carries a restriction")

	restricted := content.ByID["65536001"].SourceRestrictions
	require.False(t, restricted.IsEmpty())

	t.Run("view and edit are kept apart", func(t *testing.T) {
		require.Equal(t, []EntityKey{confluenceUserKey("5d3eaa4376cb3e0d9d31cf8e")}, restricted.ViewUserKeys)
		require.Equal(t, []EntityKey{confluenceUserKey("5d3eaa4376cb3e0d9d31cf8e")}, restricted.EditUserKeys)
		require.Empty(t, restricted.ViewGroups)
		require.Equal(t, []string{"confluence-engineering"}, restricted.EditGroups)
	})

	// A page restricted to someone is a reference to that person, so the
	// manifest has to be able to name them.
	t.Run("restricted users join the user closure", func(t *testing.T) {
		require.True(t, refs.Has(confluenceUserKey("5d3eaa4376cb3e0d9d31cf8e")))
	})

	t.Run("an unrestricted page stays unrestricted", func(t *testing.T) {
		require.True(t, content.ByID["65536002"].SourceRestrictions.IsEmpty())
	})

	t.Run("a restriction on a page that was not emitted is ignored", func(t *testing.T) {
		require.Equal(t, 1, count)
	})

	// The user form is verified against a real export; the group form is not,
	// and a bundle carrying one has to say so rather than imply otherwise.
	t.Run("a group restriction is reported as unverified", func(t *testing.T) {
		var found bool
		for _, warning := range warnings {
			if warning.Code == WarnRestrictionUnverified && warning.SourceID == "65536001" {
				found = true
				require.Contains(t, warning.Message, "confluence-engineering")
				require.Contains(t, warning.Message, "not been verified")
			}
		}
		require.True(t, found)
	})
}

// An unrecognized restriction kind is reported rather than silently dropped:
// losing access metadata without saying so is the one outcome worth failing
// loudly about.
func TestSelectRestrictions_UnknownKind(t *testing.T) {
	archive := archiveWithEntities(t, `<object class="ContentPermissionSet" package="com.atlassian.confluence.security">
<id name="id">1</id>
<property name="type"><![CDATA[Export]]></property>
<property name="owningContent" class="Page" package="com.atlassian.confluence.pages"><id name="id">100</id>
</property>
</object>`)

	page := &Page{SourceID: "100"}
	content := &ContentSelection{
		SpaceSourceID: "1",
		Pages:         []*Page{page},
		ByID:          map[string]*Page{"100": page},
	}

	count, warnings, err := SelectRestrictions(archive, content, NewUserRefs())
	require.NoError(t, err)
	require.Zero(t, count)
	require.True(t, page.SourceRestrictions.IsEmpty())

	require.Len(t, warnings, 1)
	require.Contains(t, warnings[0].Message, `"export" restriction`)
}

func TestSelectRestrictions_None(t *testing.T) {
	archive := archiveWithEntities(t, `<object class="Page" package="com.atlassian.confluence.pages">
<id name="id">1</id>
</object>`)
	content := &ContentSelection{SpaceSourceID: "1", ByID: map[string]*Page{}}

	count, warnings, err := SelectRestrictions(archive, content, NewUserRefs())
	require.NoError(t, err)
	require.Zero(t, count)
	require.Empty(t, warnings)
}

// TestRestrictionProps pins the emitted shape. Users are named by their
// canonical account ID, because that is the only identity the importer
// resolves.
func TestRestrictionProps(t *testing.T) {
	ctx := NewConversionContext(
		Space{SpaceKey: "ENG"},
		nil, nil,
		[]*User{{AccountID: "557058:abc", ConfluenceUserKey: "user-key-1"}},
	)

	source := SourceRestrictions{
		ViewUserKeys: []EntityKey{confluenceUserKey("user-key-1"), confluenceUserKey("stranger")},
		EditGroups:   []string{"team-x"},
	}

	props := restrictionProps(source, ctx)
	require.Equal(t, []any{"557058:abc"}, props["view_users"],
		"a restricted user outside the export is dropped rather than emitted as a raw Confluence key")
	require.Equal(t, []any{}, props["edit_users"])
	require.Equal(t, []any{}, props["view_groups"])
	require.Equal(t, []any{"team-x"}, props["edit_groups"])

	t.Run("an unrestricted page carries an empty object", func(t *testing.T) {
		require.Equal(t, map[string]any{}, restrictionProps(SourceRestrictions{}, ctx))
	})
}

// TestSelectRestrictions_PrivateSample is the gate against a real export.
//
//	CONFLUENCE_SAMPLE_ZIP=/path/to/export-with-a-restricted-page.zip go test ./services/confluence/...
func TestSelectRestrictions_PrivateSample(t *testing.T) {
	path := os.Getenv("CONFLUENCE_SAMPLE_ZIP")
	if path == "" {
		t.Skip("set CONFLUENCE_SAMPLE_ZIP to run against a real Confluence export")
	}

	archive, err := OpenSourceArchive(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, archive.Close()) }()

	spaces, descriptor, err := CatalogSpaces(archive)
	require.NoError(t, err)

	total := 0
	for _, space := range spaces {
		content, err := SelectPageMetadata(archive, space, descriptor)
		require.NoError(t, err)

		refs := NewUserRefs()
		count, _, err := SelectRestrictions(archive, content, refs)
		require.NoErrorf(t, err, "space %s", space.SpaceKey)
		total += count

		for _, page := range content.Pages {
			restrictions := page.SourceRestrictions
			if restrictions.IsEmpty() {
				continue
			}
			// Every restricted user must be one the export can name.
			for _, key := range append(restrictions.ViewUserKeys, restrictions.EditUserKeys...) {
				require.True(t, refs.Has(key), "restricted user %s is not in the closure", key)
			}
			t.Logf("%-42s page %s: view=%d/%d edit=%d/%d (users/groups)",
				space.SpaceKey, page.SourceID,
				len(restrictions.ViewUserKeys), len(restrictions.ViewGroups),
				len(restrictions.EditUserKeys), len(restrictions.EditGroups))
		}
	}
	t.Logf("restricted pages found: %d", total)
}
