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
	"github.com/mattermost/mmetl/services/confluence"
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
<property name="creationDate">2025-11-14 16:53:35.571</property>
<property name="lastModificationDate">2025-11-14 16:53:35.795</property>
<property name="originalVersionId"/><property name="contentStatus"><![CDATA[current]]></property>
<property name="creator" class="ConfluenceUserImpl" package="com.atlassian.confluence.user"><id name="key"><![CDATA[5adea181]]></id>
</property>
<property name="space" class="Space" package="com.atlassian.confluence.spaces"><id name="id">26542084</id>
</property>
<collection name="bodyContents" class="java.util.Collection"><element class="BodyContent" package="com.atlassian.confluence.core"><id name="id">26542274</id>
</element>
</collection>
</object>
<object class="BodyContent" package="com.atlassian.confluence.core">
<id name="id">26542274</id>
<property name="body"><![CDATA[<p>Hello from <strong>Confluence</strong>.</p>]]></property>
</object>
<object class="ConfluenceUserImpl" package="com.atlassian.confluence.user">
<id name="key"><![CDATA[5adea181]]></id>
<property name="name"><![CDATA[dylan@example.com]]></property>
<property name="lowerName"><![CDATA[dylan@example.com]]></property>
<property name="atlassianAccountId"><![CDATA[5adea181]]></property>
</object>
<object class="InternalUser" package="com.atlassian.crowd.model.user">
<id name="id">5</id>
<property name="name"><![CDATA[dylan@example.com]]></property>
<property name="lowerName"><![CDATA[dylan@example.com]]></property>
<property name="active">true</property>
<property name="displayName"><![CDATA[Dylan Haussermann]]></property>
<property name="emailAddress"><![CDATA[dylan@example.com]]></property>
<property name="externalId"><![CDATA[5adea181]]></property>
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
}

// TestTransformConfluenceEndToEnd runs the whole exporter and checks the bundle
// it produces, which is the only test that exercises every pass together.
func TestTransformConfluenceEndToEnd(t *testing.T) {
	export := writeConfluenceExport(t, testEntities)
	output := filepath.Join(t.TempDir(), "bundle.zip")

	out, err := runTransformConfluence(t,
		"--file", export,
		"--space", "dkhspace",
		"--organization-id", "https://example.atlassian.net",
		"--team", "Engineering",
		"--output", output,
	)
	require.NoError(t, err)
	require.Contains(t, out, "Wrote "+output)

	manifest, lines := readBundle(t, output)

	require.Equal(t, "2", manifest.Version)
	require.Equal(t, "https://example.atlassian.net", manifest.Source.OrganizationID)
	require.Equal(t, "26542084", manifest.Source.SpaceID)
	require.Equal(t, "dkhspace", manifest.Source.SpaceKey)
	require.Equal(t, "engineering", manifest.Target.Team, "the team is lowercased, as the other transforms do")
	require.Empty(t, manifest.Errors)

	require.Equal(t, confluence.LineTypeVersion, lines[0].Type)
	require.Equal(t, confluence.LineTypeSpace, lines[1].Type)
	require.Equal(t, confluence.LineTypeResolveSpacePlaceholders, lines[len(lines)-1].Type)

	require.Equal(t, 1, manifest.Counts.PagesEmitted)
	require.Equal(t, "Secret page title", lines[2].Page.Title)
}

// --validate-only must run the whole transform and write nothing.
func TestTransformConfluenceValidateOnly(t *testing.T) {
	export := writeConfluenceExport(t, testEntities)
	output := filepath.Join(t.TempDir(), "bundle.zip")

	out, err := runTransformConfluence(t,
		"--file", export,
		"--space", "dkhspace",
		"--organization-id", "https://example.atlassian.net",
		"--team", "engineering",
		"--output", output,
		"--validate-only",
	)
	require.NoError(t, err)
	require.Contains(t, out, "Validation succeeded")

	_, statErr := os.Stat(output)
	require.True(t, os.IsNotExist(statErr), "--validate-only must not write a bundle")
}

func TestTransformConfluenceFlagErrors(t *testing.T) {
	export := writeConfluenceExport(t, testEntities)

	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			name:    "missing space",
			args:    []string{"--organization-id", "x", "--team", "t"},
			wantErr: "--space is required",
		},
		{
			name:    "missing organization id",
			args:    []string{"--space", "dkhspace", "--team", "t"},
			wantErr: "--organization-id is required",
		},
		{
			name:    "missing team",
			args:    []string{"--space", "dkhspace", "--organization-id", "x"},
			wantErr: "--team is required",
		},
		{
			name:    "organization id too long",
			args:    []string{"--space", "dkhspace", "--team", "t", "--organization-id", strings.Repeat("x", 1025)},
			wantErr: "over the 1024 byte limit",
		},
		// Listing is a read-only inspection. Accepting transform flags would let
		// an operator believe an export ran when nothing was written.
		{
			name:    "list-spaces with a transform flag",
			args:    []string{"--list-spaces", "--team", "t"},
			wantErr: "--list-spaces cannot be combined with --team",
		},
		{
			name:    "list-spaces with several transform flags",
			args:    []string{"--list-spaces", "--space", "x", "--output", "y"},
			wantErr: "--list-spaces cannot be combined with --space, --output",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := runTransformConfluence(t, append([]string{"--file", export}, test.args...)...)
			require.ErrorContains(t, err, test.wantErr)
		})
	}
}

// An unusable --space prints the valid spaces, so the operator can fix it
// without running a second command.
func TestTransformConfluenceUnknownSpaceListsTheOptions(t *testing.T) {
	export := writeConfluenceExport(t, testEntities)

	out, err := runTransformConfluence(t,
		"--file", export,
		"--space", "nope",
		"--organization-id", "x",
		"--team", "t",
	)
	require.ErrorContains(t, err, `no space matches "nope"`)
	require.Contains(t, out, "Available spaces:")
	require.Contains(t, out, "dkhspace")
}

func TestTransformConfluenceDefaultOutputName(t *testing.T) {
	require.Equal(t, "dkhspace-confluence-docs.zip",
		confluence.DefaultOutputPath(confluence.Space{SpaceKey: "dkhspace"}))

	// A personal space key starts with "~", which is not a good filename.
	require.Equal(t, "5d3eaa4376cb3e0d9d31cf8e-confluence-docs.zip",
		confluence.DefaultOutputPath(confluence.Space{SpaceKey: "~5d3eaa4376cb3e0d9d31cf8e"}))

	require.Equal(t, "26542084-confluence-docs.zip",
		confluence.DefaultOutputPath(confluence.Space{SourceID: "26542084"}))
}

// readBundle opens a written bundle and returns its manifest and lines.
func readBundle(t *testing.T, path string) (*confluence.Manifest, []confluence.Line) {
	t.Helper()

	reader, err := zip.OpenReader(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reader.Close()) })

	manifest, lines, err := confluence.ValidateBundleFS(reader)
	require.NoError(t, err, "the exporter must produce a bundle that passes the shared validation")

	return manifest, lines
}
