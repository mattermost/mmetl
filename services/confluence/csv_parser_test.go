// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package confluence

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"testing"

	log "github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// csvFixture holds the raw file contents of a synthetic Confluence Cloud CSV
// export. Values are exact excerpts modeled on a real export's schema. Files
// whose name ends in ".gz" are gzip-compressed when the fixture zip is built,
// exercising the gzip-magic sniff.
var csvFixture = map[string]string{
	"exportDescriptor.properties": "#comment line\nexportFormat=csv\nexportType=space\nspaceKey=DOCS\n",

	"spaces.csv": "spaceid,spacename,spacekey,spacedescid,homepage,spacetype,spacestatus\n" +
		"900,Docs Space,DOCS,,100,global,CURRENT\n",

	"user_mapping.csv": "user_key,username,lower_username,aaid\n" +
		"ukey1,uname1,uname1,aaid1\n" +
		"ukey2,aaid2,aaid2,aaid2\n",

	// contentid,contenttype,title,version,creator,creationdate,lastmodifier,lastmoddate,prevver,content_status,pageid,spaceid,child_position,parentid,parentcommentid
	"content.csv": "contentid,contenttype,title,version,creator,creationdate,lastmodifier,lastmoddate,prevver,content_status,pageid,spaceid,child_position,parentid,parentcommentid\n" +
		"100,PAGE,Home,1,ukey1,2024-01-01 10:00:00.0,ukey1,2024-01-02 10:00:00.0,,current,,900,745,,\n" +
		"101,PAGE,Child,3,ukey1,2024-01-03 10:00:00.0,ukey2,2024-01-05 10:00:00.0,,current,,900,836,100,\n" +
		"102,PAGE,Child,1,ukey1,2024-01-03 10:00:00.0,ukey1,2024-01-03 10:00:00.0,101,current,,,836,,\n" +
		"103,PAGE,Draft page,1,ukey1,2024-01-06 10:00:00.0,ukey1,2024-01-06 10:00:00.0,,draft,,900,900,100,\n" +
		"200,COMMENT,,1,ukey2,2024-01-07 10:00:00.0,ukey2,2024-01-07 10:00:00.0,,current,101,,,,\n" +
		"201,COMMENT,,1,ukey1,2024-01-08 10:00:00.0,ukey1,2024-01-08 10:00:00.0,,current,101,,,,200\n" +
		"202,COMMENT,,1,ukey2,2024-01-07 09:00:00.0,ukey2,2024-01-07 09:00:00.0,200,current,101,,,,\n" +
		"900,CUSTOM,content-appearance,1,ukey1,2024-01-01 10:00:00.0,ukey1,2024-01-01 10:00:00.0,,current,100,,,,\n",

	// bodycontentid,body,contentid,bodytypeid — gzipped to exercise the sniff.
	"bodycontent.csv.gz": "bodycontentid,body,contentid,bodytypeid\n" +
		"1,\"<p>Home mentions <ac:link><ri:user ri:account-id=\"\"aaid2\"\" /></ac:link></p>\",100,2\n" +
		"2,\"<p>Child mentions <ac:link><ri:user ri:userkey=\"\"ukey2\"\" /></ac:link></p>\",101,2\n" +
		"3,<p>A top comment</p>,200,2\n" +
		"4,<p>A reply</p>,201,2\n",

	// propertyid,propertyname,stringval,longval,dateval,contentid
	"contentproperties.csv": "propertyid,propertyname,stringval,longval,dateval,contentid\n" +
		"p1,inline-marker-ref,uuid-abc,,,200\n" +
		"p2,inline-original-selection,highlighted text,,,200\n" +
		"p3,status,resolved,,,200\n",

	"label.csv":         "labelid,name,namespace\nL1,important,global\n",
	"content_label.csv": "id,labelid,contentid,labelableid,labelabletype\nx,L1,101,101,CONTENT\n",
}

// buildFixtureZip builds an in-memory zip.Reader from the given file map,
// gzip-compressing any entry whose name ends in ".gz".
func buildFixtureZip(t *testing.T, files map[string]string) *zip.Reader {
	t.Helper()
	buf := &bytes.Buffer{}
	zw := zip.NewWriter(buf)
	for name, content := range files {
		w, err := zw.Create(name)
		require.NoError(t, err)
		data := []byte(content)
		if len(name) > 3 && name[len(name)-3:] == ".gz" {
			gzBuf := &bytes.Buffer{}
			gw := gzip.NewWriter(gzBuf)
			_, err = gw.Write(data)
			require.NoError(t, err)
			require.NoError(t, gw.Close())
			data = gzBuf.Bytes()
		}
		_, err = w.Write(data)
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())

	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	require.NoError(t, err)
	return zr
}

func newTestTransformer() *Transformer {
	logger := log.New()
	logger.SetLevel(log.PanicLevel)
	return NewTransformer("team", logger, &TransformConfig{SkipAttachments: true, MaxDepth: 10})
}

func parseFixture(t *testing.T, files map[string]string) (*Transformer, *ConfluenceExport) {
	t.Helper()
	tr := newTestTransformer()
	export, err := tr.ParseConfluenceExport(buildFixtureZip(t, files))
	require.NoError(t, err)
	require.NotNil(t, export)
	return tr, export
}

func TestParseCSVExport_SpacesAndUsers(t *testing.T) {
	_, export := parseFixture(t, csvFixture)

	require.Contains(t, export.Spaces, "DOCS")
	assert.Equal(t, "100", export.Spaces["DOCS"].HomePageID)
	assert.Equal(t, "DOCS", export.SpaceKey)

	// export.Users is keyed by BOTH aaid and user_key (aliases to same record).
	require.Contains(t, export.Users, "aaid2", "should be keyed by aaid")
	require.Contains(t, export.Users, "ukey2", "should also be aliased by user_key")
	assert.Same(t, export.Users["aaid2"], export.Users["ukey2"])
	assert.Equal(t, "aaid2", export.Users["ukey2"].AccountID)
}

func TestParseCSVExport_HistoricalAndDraftFiltering(t *testing.T) {
	tr, export := parseFixture(t, csvFixture)

	// Page 102 is a historical version (current status, blank spaceid, prevver set).
	assert.True(t, export.HistoricalPageIDs["102"], "historical version should be flagged")
	// Comment 202 is a historical version (prevver set).
	assert.True(t, export.HistoricalCommentIDs["202"])

	// creator user_key must be translated to the aaid on the parsed page.
	var home *ConfluencePage
	for _, p := range export.Pages {
		if p.ID == "100" {
			home = p
		}
	}
	require.NotNil(t, home)
	assert.Equal(t, "aaid1", home.CreatedBy, "user_key should be translated to aaid")

	require.NoError(t, tr.Transform(export))

	// Only current, space-linked pages survive: 100 and 101 (102 historical, 103 draft, 900 CUSTOM dropped).
	gotPages := map[string]bool{}
	for _, p := range tr.Intermediate.Pages {
		gotPages[p.ImportSourceID] = true
	}
	assert.Equal(t, map[string]bool{"100": true, "101": true}, gotPages)

	// Comments: 200 and 201 survive; 202 (historical) is filtered.
	gotComments := map[string]bool{}
	for _, c := range tr.Intermediate.Comments {
		gotComments[c.ImportSourceID] = true
	}
	assert.Equal(t, map[string]bool{"200": true, "201": true}, gotComments)
}

func TestParseCSVExport_MentionResolution(t *testing.T) {
	tr, export := parseFixture(t, csvFixture)
	require.NoError(t, tr.Transform(export))

	var contents string
	for _, p := range tr.Intermediate.Pages {
		contents += p.Content
	}
	// Both the ri:account-id mention (page 100) and the ri:userkey mention (page 101)
	// must resolve — no unresolved placeholders left behind.
	assert.NotContains(t, contents, "{{CONF_USER", "mentions should resolve via both aaid and user_key keys")
	assert.Contains(t, contents, "@", "resolved mention should render as @username")
}

func TestParseCSVExport_IdentityProps(t *testing.T) {
	tr, export := parseFixture(t, csvFixture)
	require.NoError(t, tr.Transform(export))

	for _, p := range tr.Intermediate.Pages {
		if p.ImportSourceID == "100" {
			assert.Equal(t, "aaid1", p.Props["confluence_author_account_id"])
		}
	}
	for _, c := range tr.Intermediate.Comments {
		if c.ImportSourceID == "200" {
			assert.Equal(t, "aaid2", c.Props["confluence_author_account_id"])
		}
	}
}

func TestParseCSVExport_InlineAnchorAndResolved(t *testing.T) {
	_, export := parseFixture(t, csvFixture)

	var comment *ConfluenceComment
	for _, c := range export.Comments {
		if c.ID == "200" {
			comment = c
		}
	}
	require.NotNil(t, comment)
	assert.True(t, comment.IsResolved, "status=resolved property should set IsResolved")
	require.NotNil(t, comment.InlineAnchor)
	assert.Equal(t, "uuid-abc", comment.InlineAnchor.AnchorID)
	assert.Equal(t, "highlighted text", comment.InlineAnchor.OriginalSelection)
}

func TestParseCSVExport_Labels(t *testing.T) {
	_, export := parseFixture(t, csvFixture)
	for _, p := range export.Pages {
		if p.ID == "101" {
			assert.Contains(t, p.Labels, "important")
		}
	}
}

func TestParseCSVExport_BodiesAreStorageFormat(t *testing.T) {
	_, export := parseFixture(t, csvFixture)
	for _, p := range export.Pages {
		if p.ID == "100" {
			assert.Contains(t, p.Content, "<ac:link>", "gzipped body should decompress and attach verbatim storage format")
		}
	}
}

func TestParseConfluenceExport_UnsupportedFormat(t *testing.T) {
	tr := newTestTransformer()
	_, err := tr.ParseConfluenceExport(buildFixtureZip(t, map[string]string{
		"entities.xml": "<hibernate-generic></hibernate-generic>",
	}))
	assert.ErrorIs(t, err, ErrUnsupportedExportFormat)
}

func TestParseCSVTimestamp(t *testing.T) {
	assert.False(t, parseCSVTimestamp("2024-05-31 10:03:18.78").IsZero())
	assert.False(t, parseCSVTimestamp("2024-05-31 10:03:18").IsZero())
	assert.False(t, parseCSVTimestamp("2024-05-31").IsZero())
	assert.True(t, parseCSVTimestamp("").IsZero())
	assert.True(t, parseCSVTimestamp("not-a-date").IsZero())
}
