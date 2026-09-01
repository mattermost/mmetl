package commands_test

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mattermost/mmetl/commands"
)

const testDescriptor = `#Tue Sep 01 10:52:44 UTC 2026
backupAttachments=true
exportType=all
source=cloud
timezoneId=America/Vancouver
`

// testEntities is a two-space export: one collaboration space and one personal
// space, matching the shapes in the real Confluence Cloud backup.
const testEntities = `<?xml version="1.0" encoding="UTF-8"?>
<hibernate-generic datetime="2026-09-01 10:52:46.614">
<object class="Space" package="com.atlassian.confluence.spaces">
<id name="id">26542084</id>
<property name="name"><![CDATA[dkh-space]]></property>
<property name="key"><![CDATA[dkhspace]]></property>
<property name="lowerKey"><![CDATA[dkhspace]]></property>
<property name="homePage" class="Page" package="com.atlassian.confluence.pages"><id name="id">26542273</id>
</property>
<property name="spaceType">collaboration</property>
<property name="spaceStatus" enum-class="SpaceStatus" package="com.atlassian.confluence.spaces">CURRENT</property>
</object>
<object class="Space" package="com.atlassian.confluence.spaces">
<id name="id">63864834</id>
<property name="name"><![CDATA[Guillermo Vaya]]></property>
<property name="key"><![CDATA[~5d3eaa4376cb3e0d9d31cf8e]]></property>
<property name="lowerKey"><![CDATA[~5d3eaa4376cb3e0d9d31cf8e]]></property>
<property name="spaceType">personal</property>
<property name="spaceStatus" enum-class="SpaceStatus" package="com.atlassian.confluence.spaces">CURRENT</property>
</object>
<object class="Page" package="com.atlassian.confluence.pages">
<id name="id">26542273</id>
<property name="title"><![CDATA[Secret page title]]></property>
</object>
</hibernate-generic>
`

// writeConfluenceExport writes a minimal Confluence Cloud backup ZIP.
func writeConfluenceExport(t *testing.T, entities string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "Confluence-export.zip")
	f, err := os.Create(path) //nolint:gosec // path is inside the test's own TempDir
	require.NoError(t, err)
	defer func() { require.NoError(t, f.Close()) }()

	zw := zip.NewWriter(f)
	for name, body := range map[string]string{
		"exportDescriptor.properties": testDescriptor,
		"entities.xml":                entities,
	} {
		w, err := zw.Create(name)
		require.NoError(t, err)
		_, err = io.WriteString(w, body)
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())

	return path
}

// runTransformConfluence executes the command through the root, as a user does,
// with its output captured.
func runTransformConfluence(t *testing.T, args ...string) (string, error) {
	t.Helper()

	c := commands.RootCmd
	resetCobraFlags(c)

	var out bytes.Buffer
	c.SetOut(&out)
	c.SetErr(&out)
	c.SetArgs(append([]string{"transform", "confluence"}, args...))
	t.Cleanup(func() {
		c.SetOut(nil)
		c.SetErr(nil)
		c.SetArgs(nil)
	})

	err := c.Execute()
	return out.String(), err
}

func TestTransformConfluenceListSpaces(t *testing.T) {
	export := writeConfluenceExport(t, testEntities)

	out, err := runTransformConfluence(t, "--file", export, "--list-spaces")
	require.NoError(t, err)

	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	require.Len(t, lines, 3)
	require.Equal(t, []string{"KEY", "ID", "TYPE", "STATUS", "NAME"}, strings.Fields(lines[0]))
	require.Contains(t, lines[1], "dkhspace")
	require.Contains(t, lines[1], "26542084")
	require.Contains(t, lines[1], "collaboration")
	require.Contains(t, lines[2], "~5d3eaa4376cb3e0d9d31cf8e")

	// Listing must expose nothing but the space catalog.
	require.NotContains(t, out, "Secret page title")
}

func TestTransformConfluenceListSpacesErrors(t *testing.T) {
	t.Run("missing file flag", func(t *testing.T) {
		_, err := runTransformConfluence(t, "--list-spaces")
		require.ErrorContains(t, err, `required flag(s) "file" not set`)
	})

	t.Run("file does not exist", func(t *testing.T) {
		_, err := runTransformConfluence(t, "--file", filepath.Join(t.TempDir(), "absent.zip"), "--list-spaces")
		require.ErrorContains(t, err, "opening source archive")
	})

	t.Run("export with no spaces", func(t *testing.T) {
		export := writeConfluenceExport(t, `<?xml version="1.0" encoding="UTF-8"?>
<hibernate-generic><object class="Page" package="com.atlassian.confluence.pages">
<id name="id">1</id>
</object></hibernate-generic>`)

		_, err := runTransformConfluence(t, "--file", export, "--list-spaces")
		require.ErrorContains(t, err, "contains no spaces")
	})

	t.Run("without --list-spaces the command explains what is implemented", func(t *testing.T) {
		export := writeConfluenceExport(t, testEntities)

		_, err := runTransformConfluence(t, "--file", export)
		require.ErrorContains(t, err, "only --list-spaces is implemented")
	})
}
