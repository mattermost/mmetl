// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package confluence

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	log "github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// exportLines transforms an export and returns the emitted JSONL as decoded maps,
// keyed nothing — just the raw ordered lines — so tests can assert exact keys.
func exportLines(t *testing.T, tr *Transformer, export *ConfluenceExport) []map[string]any {
	t.Helper()
	dir := t.TempDir()
	out := filepath.Join(dir, "import.jsonl")
	require.NoError(t, tr.Transform(export))
	require.NoError(t, tr.Export(out))

	data, err := os.ReadFile(out)
	require.NoError(t, err)

	var lines []map[string]any
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 1024*1024), 8*1024*1024)
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) == "" {
			continue
		}
		var m map[string]any
		require.NoError(t, json.Unmarshal(sc.Bytes(), &m))
		lines = append(lines, m)
	}
	require.NoError(t, sc.Err())
	return lines
}

func linesOfType(lines []map[string]any, typ string) []map[string]any {
	var out []map[string]any
	for _, l := range lines {
		if l["type"] == typ {
			out = append(out, l)
		}
	}
	return out
}

func TestExport_V2SchemaAndSourceNamespace(t *testing.T) {
	files := cloneFixture(csvFixture)
	// Add an organization id so the source namespace carries both fields.
	files["exportDescriptor.properties"] += "organizationId=org-123\n"

	tr, export := parseFixture(t, files)
	lines := exportLines(t, tr, export)

	// version line: version 2 + source namespace (organization_id + space_key).
	version := linesOfType(lines, "version")
	require.Len(t, version, 1)
	assert.EqualValues(t, 2, version[0]["version"])
	src, ok := version[0]["source"].(map[string]any)
	require.True(t, ok, "version line must carry a source namespace")
	assert.Equal(t, "org-123", src["organization_id"])
	assert.Equal(t, "DOCS", src["space_key"])

	// Exactly one space line, named in Docs terms with no channel field.
	spaces := linesOfType(lines, "space")
	require.Len(t, spaces, 1)
	space := spaces[0]["space"].(map[string]any)
	assert.Equal(t, "team", space["team"])
	_, hasChannel := space["channel"]
	assert.False(t, hasChannel, "v2 space line must not carry a channel")
	spaceProps := space["props"].(map[string]any)
	assert.Equal(t, "DOCS", spaceProps["import_source_id"])

	// Pages use space_import_source_id and carry update_at.
	pages := linesOfType(lines, "page")
	require.NotEmpty(t, pages)
	for _, pl := range pages {
		p := pl["page"].(map[string]any)
		assert.Equal(t, "DOCS", p["space_import_source_id"])
		_, hasWiki := p["wiki_import_source_id"]
		assert.False(t, hasWiki, "v1 wiki_import_source_id must be gone")
		assert.Contains(t, p, "update_at", "pages should emit update_at")
	}

	// resolve line renamed to resolve_space_placeholders.
	resolve := linesOfType(lines, "resolve_space_placeholders")
	require.Len(t, resolve, 1)
	rd := resolve[0]["resolve_space_placeholders"].(map[string]any)
	assert.Equal(t, "DOCS", rd["space_import_source_id"])

	// None of the retired v1 line types survive.
	assert.Empty(t, linesOfType(lines, "wiki"))
	assert.Empty(t, linesOfType(lines, "channel"))
	assert.Empty(t, linesOfType(lines, "resolve_wiki_placeholders"))
}

func TestExport_SingleSpaceGuardrail(t *testing.T) {
	tr := newTestTransformer()
	export := &ConfluenceExport{
		Spaces: map[string]*SpaceInfo{
			"DOCS": {Key: "DOCS", Name: "Docs"},
			"ENG":  {Key: "ENG", Name: "Engineering"},
		},
		RestrictedPageIDs: map[string]bool{},
	}
	err := tr.Transform(export)
	require.Error(t, err, "a two-space export must be rejected")
	assert.Contains(t, err.Error(), "multi-space")
}

func TestExport_AttachmentsPopulatedAndResolved(t *testing.T) {
	files := map[string]string{
		"exportDescriptor.properties": "exportFormat=csv\nexportType=space\nspaceKey=DOCS\n",
		"spaces.csv":                  "spaceid,spacename,spacekey,homepage\n900,Docs,DOCS,100\n",
		"user_mapping.csv":            "user_key,username,aaid\nukey1,uname1,aaid1\n",
		"content.csv": "contentid,contenttype,title,version,creator,creationdate,lastmodifier,lastmoddate,prevver,content_status,pageid,spaceid,parentid\n" +
			"100,PAGE,Home,1,ukey1,2024-01-01 10:00:00.0,ukey1,2024-01-02 10:00:00.0,,current,,900,\n" +
			"300,ATTACHMENT,diagram.png,1,ukey1,2024-01-01 10:00:00.0,ukey1,2024-01-01 10:00:00.0,,current,100,,\n",
		"bodycontent.csv": "bodycontentid,body,contentid,bodytypeid\n" +
			"1,\"<p>See <ac:image><ri:attachment ri:filename=\"\"diagram.png\"\" /></ac:image></p>\",100,2\n",
		// The attachment bytes, under the legacy layout attachments/{page}/{id}/{version}.
		"attachments/100/300/1": "PNGDATA",
	}

	// Parse populates export.Attachments from the ATTACHMENT row.
	zr := buildFixtureZip(t, files)
	tr := NewTransformer("team", "channel", quietLogger(), &TransformConfig{MaxDepth: 10, AttachmentsDir: t.TempDir()})
	export, err := tr.ParseConfluenceExport(zr)
	require.NoError(t, err)
	require.Contains(t, export.Attachments, "100")
	require.Len(t, export.Attachments["100"], 1)
	assert.Equal(t, "diagram.png", export.Attachments["100"][0].FileName)
	assert.Equal(t, "300", export.Attachments["100"][0].ID)

	// Extraction is required before the attachment (and its content placeholder)
	// is emitted — only successfully extracted files ship.
	require.NoError(t, tr.ExtractAttachments(zr, export))
	assert.Equal(t, "100/diagram.png", export.Attachments["100"][0].FilePath)

	lines := exportLines(t, tr, export)
	pages := linesOfType(lines, "page")
	require.Len(t, pages, 1)
	page := pages[0]["page"].(map[string]any)

	// The body's filename placeholder is rewritten to the attachment id.
	content := page["content"].(string)
	assert.Contains(t, content, "{{CONF_FILE:300}}", "CONF_ATTACHMENT should resolve to CONF_FILE by id")
	assert.NotContains(t, content, "CONF_ATTACHMENT", "no unresolved attachment placeholder should remain")

	// The page carries its attachments array (source id preserved).
	atts, ok := page["attachments"].([]any)
	require.True(t, ok, "page should carry an attachments array")
	require.Len(t, atts, 1)
	attProps := atts[0].(map[string]any)["props"].(map[string]any)
	assert.Equal(t, "300", attProps["import_source_id"])
}

func TestExport_RestrictionsDetectedAndReported(t *testing.T) {
	files := cloneFixture(csvFixture)
	files["content_perm_set.csv"] = "id,cps_type,cont_id\nps1,VIEW,101\nps2,EDIT,100\n"

	_, export := parseFixture(t, files)
	// Only the VIEW-restricted page is flagged.
	assert.True(t, export.RestrictedPageIDs["101"])
	assert.False(t, export.RestrictedPageIDs["100"], "EDIT-only restriction is not a view widening")

	// Validation surfaces it as a warning by default, an error under --fail.
	v := NewValidator("team", "")
	warnResult := v.ValidateExportContent(export)
	assert.True(t, warnResult.Valid)
	require.NotEmpty(t, warnResult.Warnings)
	assert.Condition(t, func() bool {
		for _, w := range warnResult.Warnings {
			if strings.Contains(w, "View restriction") {
				return true
			}
		}
		return false
	})

	v.FailOnRestricted = true
	failResult := v.ValidateExportContent(export)
	assert.False(t, failResult.Valid, "--fail-on-restricted should fail validation")

	// Manifest records the restricted page with its resolved title.
	m := NewManifest(export, "team", "", "export.zip")
	m.SetRestrictedPages(export)
	require.Len(t, m.RestrictedPages, 1)
	assert.Equal(t, "101", m.RestrictedPages[0].ID)
	assert.Equal(t, "Child", m.RestrictedPages[0].Title)
}

func TestExport_UpdateAtOmittedWhenZero(t *testing.T) {
	// A page with no lastmoddate must omit update_at rather than emit epoch-zero.
	files := map[string]string{
		"exportDescriptor.properties": "exportFormat=csv\nspaceKey=DOCS\n",
		"spaces.csv":                  "spaceid,spacename,spacekey,homepage\n900,Docs,DOCS,100\n",
		"user_mapping.csv":            "user_key,username,aaid\nukey1,uname1,aaid1\n",
		"content.csv": "contentid,contenttype,title,version,creator,creationdate,lastmodifier,lastmoddate,prevver,content_status,pageid,spaceid,parentid\n" +
			"100,PAGE,Home,1,ukey1,2024-01-01 10:00:00.0,,,,current,,900,\n",
		"bodycontent.csv": "bodycontentid,body,contentid,bodytypeid\n1,<p>hi</p>,100,2\n",
	}
	tr, export := parseFixture(t, files)
	lines := exportLines(t, tr, export)
	page := linesOfType(lines, "page")[0]["page"].(map[string]any)
	_, hasUpdate := page["update_at"]
	assert.False(t, hasUpdate, "update_at must be omitted when the source has no modification date")
}

func cloneFixture(src map[string]string) map[string]string {
	out := make(map[string]string, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

func quietLogger() log.FieldLogger {
	l := log.New()
	l.SetLevel(log.PanicLevel)
	return l
}
