package confluence

import (
	"archive/zip"
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// sampleSpaces reproduces the space shape from the private export: a
// collaboration space, a personal space, and a personal space with no home
// page, which the real sample does contain.
const sampleSpaces = `<object class="Space" package="com.atlassian.confluence.spaces">
<id name="id">26542084</id>
<property name="name"><![CDATA[dkh-space]]></property>
<property name="key"><![CDATA[dkhspace]]></property>
<property name="lowerKey"><![CDATA[dkhspace]]></property>
<property name="description" class="SpaceDescription" package="com.atlassian.confluence.spaces"><id name="id">26542083</id>
</property>
<property name="homePage" class="Page" package="com.atlassian.confluence.pages"><id name="id">26542273</id>
</property>
<property name="spaceType">collaboration</property>
<property name="spaceStatus" enum-class="SpaceStatus" package="com.atlassian.confluence.spaces">CURRENT</property>
</object>
<object class="SpaceDescription" package="com.atlassian.confluence.spaces">
<id name="id">26542083</id>
<property name="space" class="Space" package="com.atlassian.confluence.spaces"><id name="id">26542084</id>
</property>
</object>
<object class="Space" package="com.atlassian.confluence.spaces">
<id name="id">63864834</id>
<property name="name"><![CDATA[Guillermo Vayá]]></property>
<property name="key"><![CDATA[~5d3eaa4376cb3e0d9d31cf8e]]></property>
<property name="lowerKey"><![CDATA[~5d3eaa4376cb3e0d9d31cf8e]]></property>
<property name="homePage" class="Page" package="com.atlassian.confluence.pages"><id name="id">63864969</id>
</property>
<property name="spaceType">personal</property>
<property name="spaceStatus" enum-class="SpaceStatus" package="com.atlassian.confluence.spaces">CURRENT</property>
</object>
<object class="SpaceDescription" package="com.atlassian.confluence.spaces">
<id name="id">63864833</id>
<property name="space" class="Space" package="com.atlassian.confluence.spaces"><id name="id">63864834</id>
</property>
</object>
<object class="Space" package="com.atlassian.confluence.spaces">
<id name="id">25985050</id>
<property name="name"><![CDATA[Ogi Marusic]]></property>
<property name="key"><![CDATA[~71202035e13a5b8eb54664acde14e7c506753c]]></property>
<property name="lowerKey"><![CDATA[~71202035e13a5b8eb54664acde14e7c506753c]]></property>
<property name="description" class="SpaceDescription" package="com.atlassian.confluence.spaces"><id name="id">25985049</id>
</property>
<property name="spaceType">personal</property>
<property name="spaceStatus" enum-class="SpaceStatus" package="com.atlassian.confluence.spaces">CURRENT</property>
</object>
<object class="Page" package="com.atlassian.confluence.pages">
<id name="id">26542273</id>
<property name="title"><![CDATA[Never read by pass 1]]></property>
</object>`

// archiveWithEntities builds a source archive whose entities.xml is the given
// object stream.
func archiveWithEntities(t *testing.T, objects string) *SourceArchive {
	t.Helper()

	path := buildArchive(t,
		testEntry{name: DescriptorEntryName, body: sampleDescriptor, method: zip.Deflate},
		testEntry{name: EntitiesEntryName, body: wrapEntities(objects), method: zip.Deflate},
	)

	archive, err := OpenSourceArchive(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, archive.Close()) })

	return archive
}

func TestCatalogSpaces(t *testing.T) {
	spaces, descriptor, err := CatalogSpaces(archiveWithEntities(t, sampleSpaces))
	require.NoError(t, err)

	require.Equal(t, "America/Vancouver", descriptor.TimezoneID)
	require.Len(t, spaces, 3)

	t.Run("sorted by key then id", func(t *testing.T) {
		require.Equal(t, []string{
			"dkhspace",
			"~5d3eaa4376cb3e0d9d31cf8e",
			"~71202035e13a5b8eb54664acde14e7c506753c",
		}, spaceKeys(spaces))
	})

	t.Run("collaboration space", func(t *testing.T) {
		space := spaces[0]
		require.Equal(t, "26542084", space.SourceID)
		require.Equal(t, "dkh-space", space.Name)
		require.Equal(t, "collaboration", space.Type)
		require.Equal(t, "CURRENT", space.Status)
		require.Equal(t, EntityKey{Package: PkgConfluenceSpaces, Class: "Space", IDName: "id", ID: "26542084"}, space.Key)
		require.True(t, space.HasHomePage())
		require.Equal(t, "26542273", space.HomePageKey.ID)
		require.Equal(t, "26542083", space.DescriptionKey.ID)
	})

	// The real export contains a personal space with no homePage. The catalog
	// must record that rather than inventing one, because the
	// space-description attachment rule depends on knowing it is missing.
	t.Run("space with no home page", func(t *testing.T) {
		space := spaces[2]
		require.Equal(t, "25985050", space.SourceID)
		require.False(t, space.HasHomePage())
		require.True(t, space.HasDescription())
	})

	// A space whose forward description reference is absent still finds its
	// description through the description's own back-reference.
	t.Run("description recovered from the back-reference", func(t *testing.T) {
		space := spaces[1]
		require.Equal(t, "63864834", space.SourceID)
		require.Equal(t, "63864833", space.DescriptionKey.ID)
	})
}

func TestCatalogSpaces_RejectsDuplicateKeys(t *testing.T) {
	duplicated := `<object class="Space" package="com.atlassian.confluence.spaces">
<id name="id">1</id>
<property name="key"><![CDATA[ENG]]></property>
</object>
<object class="Space" package="com.atlassian.confluence.spaces">
<id name="id">1</id>
<property name="key"><![CDATA[OPS]]></property>
</object>`

	_, _, err := CatalogSpaces(archiveWithEntities(t, duplicated))
	require.ErrorContains(t, err, "declares com.atlassian.confluence.spaces.Space[id=1] twice")
}

func TestCatalogSpaces_EmptyCatalog(t *testing.T) {
	spaces, _, err := CatalogSpaces(archiveWithEntities(t, `<object class="Page" package="com.atlassian.confluence.pages">
<id name="id">1</id>
</object>`))
	require.NoError(t, err)
	require.Empty(t, spaces)
}

func spaceKeys(spaces []Space) []string {
	keys := make([]string, 0, len(spaces))
	for _, space := range spaces {
		keys = append(keys, space.SpaceKey)
	}
	return keys
}

func TestResolveSpace(t *testing.T) {
	spaces := []Space{
		{SourceID: "1", SpaceKey: "ENG", Name: "Engineering"},
		{SourceID: "2", SpaceKey: "eng-archive", Name: "engineering"},
		{SourceID: "3", SpaceKey: "OPS", Name: "Operations"},
		{SourceID: "26542084", SpaceKey: "dkhspace", Name: "dkh-space"},
	}

	t.Run("resolution order", func(t *testing.T) {
		tests := []struct {
			selector string
			wantID   string
			why      string
		}{
			{"1", "1", "exact numeric source id"},
			{"26542084", "26542084", "exact numeric source id"},
			{"ENG", "1", "exact case-sensitive key beats a case-insensitive name match"},
			{"eng-archive", "2", "exact case-sensitive key"},
			{"OPS", "3", "exact case-sensitive key"},
			{"ops", "3", "unique case-insensitive key"},
			{"ENG-ARCHIVE", "2", "unique case-insensitive key"},
			{"Engineering", "1", "exact case-sensitive name"},
			{"engineering", "2", "exact case-sensitive name, which is a different space"},
			{"OPERATIONS", "3", "unique case-insensitive name"},
			{"  OPS  ", "3", "selector is trimmed"},
		}

		for _, test := range tests {
			t.Run(test.selector, func(t *testing.T) {
				space, err := ResolveSpace(spaces, test.selector)
				require.NoError(t, err, test.why)
				require.Equal(t, test.wantID, space.SourceID, test.why)
			})
		}
	})

	// "ENG" as a key belongs to space 1; "Engineering"/"engineering" as names
	// belong to 1 and 2. A case-insensitive name lookup for "ENGINEERING" is
	// therefore genuinely ambiguous and must not silently pick one.
	t.Run("ambiguous case-insensitive name", func(t *testing.T) {
		_, err := ResolveSpace(spaces, "ENGINEERING")
		require.Error(t, err)

		var selectionErr *SpaceSelectionError
		require.ErrorAs(t, err, &selectionErr)
		require.Len(t, selectionErr.Matches, 2)
		require.Contains(t, err.Error(), "matches 2 spaces")
		require.Len(t, selectionErr.Spaces, 4, "the catalog travels with the error so the caller can print it")
	})

	t.Run("no match", func(t *testing.T) {
		_, err := ResolveSpace(spaces, "MARKETING")
		require.ErrorContains(t, err, `no space matches "MARKETING"`)

		var selectionErr *SpaceSelectionError
		require.ErrorAs(t, err, &selectionErr)
		require.Empty(t, selectionErr.Matches)
		require.Len(t, selectionErr.Spaces, 4)
	})

	t.Run("empty selector", func(t *testing.T) {
		for _, selector := range []string{"", "   "} {
			_, err := ResolveSpace(spaces, selector)
			require.ErrorContains(t, err, "--space is required")
		}
	})

	t.Run("empty catalog", func(t *testing.T) {
		_, err := ResolveSpace(nil, "ENG")
		require.ErrorContains(t, err, `no space matches "ENG"`)
	})

	// A numeric key must not shadow the source-id step for a different space.
	t.Run("numeric id wins over an identical key", func(t *testing.T) {
		collision := []Space{
			{SourceID: "100", SpaceKey: "other"},
			{SourceID: "200", SpaceKey: "100"},
		}
		space, err := ResolveSpace(collision, "100")
		require.NoError(t, err)
		require.Equal(t, "100", space.SourceID)
	})
}

func TestWriteSpaceTable(t *testing.T) {
	var out bytes.Buffer
	require.NoError(t, WriteSpaceTable(&out, []Space{
		{SourceID: "63864834", SpaceKey: "~5d3eaa43", Name: "Guillermo Vayá", Type: "personal", Status: "CURRENT"},
		{SourceID: "26542084", SpaceKey: "dkhspace", Name: "dkh-space", Type: "collaboration", Status: "CURRENT"},
	}))

	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	require.Len(t, lines, 3)
	require.Equal(t, []string{"KEY", "ID", "TYPE", "STATUS", "NAME"}, strings.Fields(lines[0]))
	require.True(t, strings.HasPrefix(lines[1], "dkhspace"), "rows are sorted by key: %q", lines[1])
	require.Contains(t, lines[1], "26542084")
	require.True(t, strings.HasPrefix(lines[2], "~5d3eaa43"))
}

// TestCatalogSpaces_PrivateSample is the E4 gate against a real export.
//
//	CONFLUENCE_SAMPLE_ZIP=/path/to/Confluence-export.zip go test ./services/confluence/...
func TestCatalogSpaces_PrivateSample(t *testing.T) {
	path := os.Getenv("CONFLUENCE_SAMPLE_ZIP")
	if path == "" {
		t.Skip("set CONFLUENCE_SAMPLE_ZIP to run against a real Confluence export")
	}

	archive, err := OpenSourceArchive(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, archive.Close()) }()

	spaces, _, err := CatalogSpaces(archive)
	require.NoError(t, err)
	require.NotEmpty(t, spaces)

	var table bytes.Buffer
	require.NoError(t, WriteSpaceTable(&table, spaces))
	t.Logf("catalog:\n%s", table.String())

	for _, space := range spaces {
		require.NotEmpty(t, space.SourceID)
		require.NotEmpty(t, space.SpaceKey)

		// Every space must be selectable by its own id and its own key.
		byID, err := ResolveSpace(spaces, space.SourceID)
		require.NoError(t, err)
		require.Equal(t, space.SourceID, byID.SourceID)

		byKey, err := ResolveSpace(spaces, space.SpaceKey)
		require.NoError(t, err)
		require.Equal(t, space.SourceID, byKey.SourceID)
	}
}
