package confluence

import (
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// e11Context indexes a small selected space: two pages with distinct titles,
// two sharing a title, one attachment, and one user.
func e11Context(t *testing.T) *ConversionContext {
	t.Helper()

	home := &Page{SourceID: "100", Title: "Engineering Home"}
	child := &Page{SourceID: "101", Title: "Runbook"}
	twinA := &Page{SourceID: "102", Title: "Notes"}
	twinB := &Page{SourceID: "103", Title: "Notes"}
	home.Children = []*Page{child}

	attachments := map[string][]*Attachment{
		"100": {{SourceID: "900", Filename: "diagram.png", PageSourceID: "100"}},
	}
	users := []*User{{
		AccountID:          "557058:abc",
		ConfluenceUserKey:  "user-key-1",
		DisplayName:        "Aline Turner",
		MattermostUsername: "aline.turner",
	}}

	return NewConversionContext(
		Space{SpaceKey: "ENG"},
		[]*Page{home, child, twinA, twinB},
		attachments,
		users,
	)
}

// convertWithContext converts a body for page 100 with the reference index in
// place.
func convertWithContext(t *testing.T, storage string) (*TipTapNode, []Warning) {
	t.Helper()

	converter := NewConverter()
	converter.SetContext(e11Context(t))

	encoded, err := converter.Convert(storage, "100")
	require.NoError(t, err)

	return decodeTipTap(t, encoded), converter.Warnings()
}

func TestConvert_UserMention(t *testing.T) {
	t.Run("resolved by user key", func(t *testing.T) {
		doc, warnings := convertWithContext(t, `<p><ac:link><ri:user ri:userkey="user-key-1"/></ac:link></p>`)
		require.Empty(t, warnings)

		mention := doc.Content[0].Content[0]
		require.Equal(t, NodeMention, mention.Type)
		require.Equal(t, "{{CONF_USER_ID:557058:abc}}", mention.Attrs["id"])
		require.Equal(t, "Aline Turner", mention.Attrs["label"])
	})

	// A Cloud export writes the account id into ri:userkey, so both indexes
	// have to be tried or every mention in the sample would fall back to text.
	t.Run("resolved when the user key is really the account id", func(t *testing.T) {
		doc, _ := convertWithContext(t, `<p><ac:link><ri:user ri:userkey="557058:abc"/></ac:link></p>`)
		require.Equal(t, NodeMention, doc.Content[0].Content[0].Type)
	})

	t.Run("resolved by account id", func(t *testing.T) {
		doc, _ := convertWithContext(t, `<p><ac:link><ri:user ri:account-id="557058:abc"/></ac:link></p>`)
		require.Equal(t, NodeMention, doc.Content[0].Content[0].Type)
	})

	t.Run("a user outside the export degrades to text", func(t *testing.T) {
		doc, warnings := convertWithContext(t, `<p><ac:link><ri:user ri:userkey="stranger"/></ac:link></p>`)

		node := doc.Content[0].Content[0]
		require.Equal(t, NodeText, node.Type)
		require.Equal(t, "@stranger", node.Text)
		require.Len(t, warnings, 1)
		require.Equal(t, WarnXMLUnknownReferenceClass, warnings[0].Code)
	})

	t.Run("without a context every mention is text", func(t *testing.T) {
		doc, _ := convert(t, `<p><ac:link><ri:user ri:userkey="user-key-1"/></ac:link></p>`)
		require.Equal(t, NodeText, doc.Content[0].Content[0].Type)
	})
}

func TestConvert_PageLink(t *testing.T) {
	t.Run("resolved to a page placeholder", func(t *testing.T) {
		doc, warnings := convertWithContext(t,
			`<p><ac:link><ri:page ri:content-title="Runbook"/><ac:plain-text-link-body>the runbook</ac:plain-text-link-body></ac:link></p>`)
		require.Empty(t, warnings)

		node := doc.Content[0].Content[0]
		require.Equal(t, "the runbook", node.Text)
		require.Equal(t, "{{CONF_PAGE_ID:101}}", node.Marks[0].Attrs["href"])
	})

	t.Run("a link with no body is labelled by the page title", func(t *testing.T) {
		doc, _ := convertWithContext(t, `<p><ac:link><ri:page ri:content-title="Runbook"/></ac:link></p>`)
		require.Equal(t, "Runbook", doc.Content[0].Content[0].Text)
	})

	// Section 11 forbids a title-based placeholder, so an ambiguous title must
	// not be resolved: guessing would link readers to the wrong page.
	t.Run("an ambiguous title degrades to text", func(t *testing.T) {
		doc, warnings := convertWithContext(t, `<p><ac:link><ri:page ri:content-title="Notes"/></ac:link></p>`)

		node := doc.Content[0].Content[0]
		require.Equal(t, "Notes", node.Text)
		require.Empty(t, node.Marks)
		require.Len(t, warnings, 1)
		require.Contains(t, warnings[0].Message, "2 exported pages are titled")
	})

	t.Run("a cross-space link degrades to text", func(t *testing.T) {
		doc, warnings := convertWithContext(t,
			`<p><ac:link><ri:page ri:space-key="OPS" ri:content-title="Runbook"/></ac:link></p>`)

		require.Empty(t, doc.Content[0].Content[0].Marks)
		require.Contains(t, warnings[0].Message, "space \"OPS\"")
	})

	t.Run("a link to a page that was not exported degrades to text", func(t *testing.T) {
		doc, warnings := convertWithContext(t, `<p><ac:link><ri:page ri:content-title="Gone"/></ac:link></p>`)

		require.Equal(t, "Gone", doc.Content[0].Content[0].Text)
		require.Contains(t, warnings[0].Message, "no exported page is titled")
	})

	t.Run("a same-space link is resolved", func(t *testing.T) {
		doc, warnings := convertWithContext(t,
			`<p><ac:link><ri:page ri:space-key="eng" ri:content-title="Runbook"/></ac:link></p>`)
		require.Empty(t, warnings, "the space key is matched case-insensitively")
		require.Equal(t, "{{CONF_PAGE_ID:101}}", doc.Content[0].Content[0].Marks[0].Attrs["href"])
	})
}

func TestConvert_AttachmentReferences(t *testing.T) {
	t.Run("an attachment link carries the attachment placeholder", func(t *testing.T) {
		doc, warnings := convertWithContext(t,
			`<p><ac:link><ri:attachment ri:filename="diagram.png"/></ac:link></p>`)
		require.Empty(t, warnings)

		node := doc.Content[0].Content[0]
		require.Equal(t, "diagram.png", node.Text)
		require.Equal(t, "{{CONF_ATTACHMENT_ID:900}}", node.Marks[0].Attrs["href"])
	})

	t.Run("an attachment image carries the placeholder as its source", func(t *testing.T) {
		doc, warnings := convertWithContext(t,
			`<ac:image ac:alt="The diagram"><ri:attachment ri:filename="diagram.png"/></ac:image>`)
		require.Empty(t, warnings)

		require.Equal(t, []string{NodeImage}, blockTypes(doc))
		require.Equal(t, "{{CONF_ATTACHMENT_ID:900}}", doc.Content[0].Attrs["src"])
		require.Equal(t, "The diagram", doc.Content[0].Attrs["alt"])
	})

	t.Run("an unresolved attachment keeps its filename as text", func(t *testing.T) {
		doc, warnings := convertWithContext(t,
			`<p><ac:link><ri:attachment ri:filename="missing.png"/></ac:link></p>`)

		node := doc.Content[0].Content[0]
		require.Equal(t, "missing.png", node.Text)
		require.Empty(t, node.Marks)
		require.Contains(t, warnings[0].Message, "no exported attachment is named")
	})
}

// Every image in the discovery sample is an external URL rather than an
// attachment.
func TestConvert_ExternalImage(t *testing.T) {
	doc, warnings := convertWithContext(t,
		`<ac:image ac:align="center" ac:alt="spaces.png" ac:height="362"><ri:url ri:value="https://cdn.example.com/a.png"/></ac:image>`)
	require.Empty(t, warnings)

	require.Equal(t, []string{NodeImage}, blockTypes(doc))
	require.Equal(t, "https://cdn.example.com/a.png", doc.Content[0].Attrs["src"])
	require.Equal(t, "spaces.png", doc.Content[0].Attrs["alt"])

	t.Run("a dangerous image source is dropped", func(t *testing.T) {
		doc, warnings := convertWithContext(t,
			`<ac:image ac:alt="bad"><ri:url ri:value="javascript:alert(1)"/></ac:image>`)

		require.NotContains(t, blockTypes(doc), NodeImage)
		require.Equal(t, "bad", flatten(doc))
		require.Equal(t, WarnDangerousURLRemoved, warnings[0].Code)
	})
}

func TestConvert_Emoticon(t *testing.T) {
	// A real emoticon from the sample: the export carries the character itself.
	doc, _ := convertWithContext(t,
		`<p>hi <ac:emoticon ac:name="blue-star" ac:emoji-shortname=":crying_cat_face:" ac:emoji-fallback="X"/></p>`)

	require.Equal(t, "hi X", flatten(doc.Content[0]))
}

func TestConvert_TaskList(t *testing.T) {
	doc, _ := convertWithContext(t, `<ac:task-list>`+
		`<ac:task><ac:task-id>1</ac:task-id><ac:task-status>complete</ac:task-status><ac:task-body>done thing</ac:task-body></ac:task>`+
		`<ac:task><ac:task-id>2</ac:task-id><ac:task-status>incomplete</ac:task-status><ac:task-body><strong>open</strong> thing</ac:task-body></ac:task>`+
		`</ac:task-list>`)

	require.Equal(t, []string{NodeTaskList}, blockTypes(doc))
	items := doc.Content[0].Content
	require.Len(t, items, 2)

	require.Equal(t, NodeTaskItem, items[0].Type)
	require.Equal(t, true, items[0].Attrs["checked"])
	require.Equal(t, "done thing", flatten(items[0]))

	require.Equal(t, false, items[1].Attrs["checked"])
	require.Equal(t, "open thing", flatten(items[1]))
}

// An inline comment anchor has no destination mapping yet. Its text is content
// and is kept; the marker itself is dropped.
func TestConvert_InlineCommentMarker(t *testing.T) {
	doc, warnings := convertWithContext(t,
		`<p>please <ac:inline-comment-marker ac:ref="65239a23">wait</ac:inline-comment-marker> here</p>`)

	require.Empty(t, warnings)
	require.Equal(t, "please wait here", flatten(doc.Content[0]))
}

func TestConvert_MacroMatrix(t *testing.T) {
	t.Run("code macro becomes a code block with its language", func(t *testing.T) {
		doc, warnings := convertWithContext(t,
			`<ac:structured-macro ac:name="code"><ac:parameter ac:name="language">go</ac:parameter>`+
				`<ac:plain-text-body>package main</ac:plain-text-body></ac:structured-macro>`)
		require.Empty(t, warnings)

		require.Equal(t, []string{NodeCodeBlock}, blockTypes(doc))
		require.Equal(t, "go", doc.Content[0].Attrs["language"])
		require.Equal(t, "package main", flatten(doc.Content[0]))
	})

	t.Run("admonition macros become callouts", func(t *testing.T) {
		for macro, calloutType := range map[string]string{
			"info": "info", "note": "note", "warning": "warning", "tip": "success", "panel": "info",
		} {
			doc, warnings := convertWithContext(t,
				`<ac:structured-macro ac:name="`+macro+`"><ac:rich-text-body><p>heed</p></ac:rich-text-body></ac:structured-macro>`)
			require.Empty(t, warnings, macro)

			require.Equal(t, []string{NodeCallout}, blockTypes(doc), macro)
			require.Equal(t, calloutType, doc.Content[0].Attrs["type"], macro)
			require.Equal(t, "heed", flatten(doc.Content[0]), macro)
		}
	})

	t.Run("an empty callout still holds a block", func(t *testing.T) {
		doc, _ := convertWithContext(t, `<ac:structured-macro ac:name="info"/>`)
		require.Equal(t, NodeParagraph, doc.Content[0].Content[0].Type)
	})

	t.Run("status macro becomes its label", func(t *testing.T) {
		doc, warnings := convertWithContext(t,
			`<ac:structured-macro ac:name="status"><ac:parameter ac:name="title">in progress</ac:parameter>`+
				`<ac:parameter ac:name="colour">Yellow</ac:parameter></ac:structured-macro>`)
		require.Empty(t, warnings)
		require.Equal(t, "[in progress]", flatten(doc))
	})

	// The macro is live in Confluence, so a frozen list of real links is closer
	// to what the reader saw than a marker would be.
	t.Run("children macro becomes a list of real child links", func(t *testing.T) {
		doc, warnings := convertWithContext(t, `<ac:structured-macro ac:name="children"/>`)
		require.Empty(t, warnings)

		require.Equal(t, []string{NodeBulletList}, blockTypes(doc))
		item := doc.Content[0].Content[0]
		link := item.Content[0].Content[0]
		require.Equal(t, "Runbook", link.Text)
		require.Equal(t, "{{CONF_PAGE_ID:101}}", link.Marks[0].Attrs["href"])
	})

	t.Run("children macro with no children falls back to a marker", func(t *testing.T) {
		converter := NewConverter()
		converter.SetContext(e11Context(t))
		encoded, err := converter.Convert(`<ac:structured-macro ac:name="children"/>`, "101")
		require.NoError(t, err)

		require.Contains(t, flatten(decodeTipTap(t, encoded)), "[Unsupported Confluence macro: children]")
		require.NotEmpty(t, converter.Warnings())
	})

	t.Run("jira macro links the key when a server url is present", func(t *testing.T) {
		doc, _ := convertWithContext(t,
			`<ac:structured-macro ac:name="jira"><ac:parameter ac:name="key">MM-1234</ac:parameter>`+
				`<ac:parameter ac:name="serverId-url">https://jira.example.com/</ac:parameter></ac:structured-macro>`)

		node := doc.Content[0].Content[0]
		require.Equal(t, "MM-1234", node.Text)
		require.Equal(t, "https://jira.example.com/browse/MM-1234", node.Marks[0].Attrs["href"])
	})

	t.Run("jira macro without a server url keeps the bare key", func(t *testing.T) {
		doc, _ := convertWithContext(t,
			`<ac:structured-macro ac:name="jira"><ac:parameter ac:name="key">MM-1234</ac:parameter></ac:structured-macro>`)

		node := doc.Content[0].Content[0]
		require.Equal(t, "MM-1234", node.Text)
		require.Empty(t, node.Marks)
	})

	t.Run("an ADF extension is named by its node type", func(t *testing.T) {
		doc, warnings := convertWithContext(t,
			`<ac:adf-extension><ac:adf-node type="decision-list"><ac:adf-attribute key="local-id">x</ac:adf-attribute></ac:adf-node></ac:adf-extension>`)

		require.Contains(t, flatten(doc), "[Unsupported Confluence macro: decision-list]")
		require.Equal(t, WarnUnsupportedMacro, warnings[0].Code)
	})
}

// A paragraph holding an image must split rather than nest a block node inside
// inline content.
func TestConvert_ParagraphSplitsAroundBlockContent(t *testing.T) {
	doc, _ := convertWithContext(t,
		`<p>before<ac:image><ri:url ri:value="https://cdn.example.com/a.png"/></ac:image>after</p>`)

	require.Equal(t, []string{NodeParagraph, NodeImage, NodeParagraph}, blockTypes(doc))
	require.Equal(t, "before", flatten(doc.Content[0]))
	require.Equal(t, "after", flatten(doc.Content[2]))
}

// TestConvert_BlockLevelLinkKeepsItsLink is a regression test for a real body.
//
// Confluence emits ac:link at block level, outside any paragraph, when a link
// is rendered as a card (ac:card-appearance="block"). The block-level branch
// converted it as a container and kept only its text, so the link silently
// disappeared while an identical inline link a paragraph above it worked.
func TestConvert_BlockLevelLinkKeepsItsLink(t *testing.T) {
	doc, warnings := convertWithContext(t,
		`<p><ac:link ac:card-appearance="inline"><ri:page ri:content-title="Runbook"/>`+
			`<ac:link-body>inline one</ac:link-body></ac:link></p>`+
			`<ac:link ac:card-appearance="block"><ri:page ri:content-title="Runbook"/>`+
			`<ac:link-body>block one</ac:link-body></ac:link>`)
	require.Empty(t, warnings)

	require.Equal(t, []string{NodeParagraph, NodeParagraph}, blockTypes(doc),
		"the block-level link is gathered into its own paragraph")

	for i, expected := range []string{"inline one", "block one"} {
		node := doc.Content[i].Content[0]
		require.Equal(t, expected, node.Text)
		require.Equal(t, []string{MarkLink}, markTypes(node), expected)
		require.Equal(t, "{{CONF_PAGE_ID:101}}", node.Marks[0].Attrs["href"], expected)
	}
}

// An emoticon or comment marker can also sit alone between paragraphs.
func TestConvert_BlockLevelInlineElements(t *testing.T) {
	doc, _ := convertWithContext(t,
		`<p>before</p><ac:emoticon ac:emoji-fallback="Y"/><p>after</p>`)

	require.Equal(t, []string{NodeParagraph, NodeParagraph, NodeParagraph}, blockTypes(doc))
	require.Equal(t, "Y", flatten(doc.Content[1]))
}

// Section 11 allows exactly three placeholders, and only in typed attributes.
func TestPlaceholderFormats(t *testing.T) {
	require.Equal(t, "{{CONF_PAGE_ID:101}}", PagePlaceholder("101"))
	require.Equal(t, "{{CONF_ATTACHMENT_ID:900}}", AttachmentPlaceholder("900"))
	require.Equal(t, "{{CONF_USER_ID:557058:abc}}", UserPlaceholder("557058:abc"))
}

// readBodies loads the storage-format body of each selected page, which is the
// payload pass a later task performs for real.
func readBodies(t *testing.T, archive *SourceArchive, pages []*Page) map[string]string {
	t.Helper()

	wanted := map[string]string{}
	for _, page := range pages {
		for _, key := range page.BodyContentKeys {
			wanted[key.ID] = page.SourceID
		}
	}

	entities, err := archive.OpenEntities()
	require.NoError(t, err)
	defer func() { require.NoError(t, entities.Close()) }()

	decoder := NewObjectDecoder(entities)
	decoder.SetAccept(func(class ClassRef) bool { return class == ClassBodyContent })

	bodies := map[string]string{}
	for {
		object, err := decoder.Next()
		if errors.Is(err, io.EOF) {
			return bodies
		}
		require.NoError(t, err)

		if pageID, ok := wanted[object.Key.ID]; ok {
			bodies[pageID] = object.ScalarValue("body")
		}
	}
}

// TestConvert_PrivateSampleWithContext is the E11 gate: real bodies converted
// with the real reference index, so mentions and links resolve the way an
// actual export would resolve them.
//
//	CONFLUENCE_SAMPLE_ZIP=/path/to/Confluence-export.zip go test ./services/confluence/...
func TestConvert_PrivateSampleWithContext(t *testing.T) {
	path := os.Getenv("CONFLUENCE_SAMPLE_ZIP")
	if path == "" {
		t.Skip("set CONFLUENCE_SAMPLE_ZIP to run against a real Confluence export")
	}

	archive, err := OpenSourceArchive(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, archive.Close()) }()

	spaces, descriptor, err := CatalogSpaces(archive)
	require.NoError(t, err)

	var totalPages, totalMentions, unresolved int
	warningCounts := map[string]int{}

	for _, space := range spaces {
		content, err := SelectPageMetadata(archive, space, descriptor)
		require.NoError(t, err)
		deps, err := SelectDependencies(archive, space, descriptor, content, false)
		require.NoError(t, err)

		refs := NewUserRefs()
		refs.AddFromContent(content)
		refs.AddFromDependencies(deps)
		users, _, err := SelectUsers(archive, testOrganizationID, refs, NewUserMapping())
		require.NoError(t, err)

		ctx := NewConversionContext(space, content.Pages, deps.Attachments, users)
		bodies := readBodies(t, archive, content.Pages)

		for _, page := range content.Pages {
			converter := NewConverter()
			converter.SetContext(ctx)

			encoded, err := converter.Convert(bodies[page.SourceID], page.SourceID)
			require.NoErrorf(t, err, "space %s page %s", space.SpaceKey, page.SourceID)
			totalPages++

			for _, warning := range converter.Warnings() {
				warningCounts[warning.Code]++
			}

			doc := decodeTipTap(t, encoded)
			var walk func(node *TipTapNode)
			walk = func(node *TipTapNode) {
				if node.Type == NodeMention {
					totalMentions++
					id, _ := node.Attrs["id"].(string)
					require.Truef(t, strings.HasPrefix(id, "{{CONF_USER_ID:"),
						"mention on page %s carries %q", page.SourceID, id)
				}
				// A placeholder may only appear in an approved attribute, never
				// as visible text.
				require.NotContainsf(t, node.Text, "{{CONF_", "page %s leaked a placeholder into text", page.SourceID)
				for _, child := range node.Content {
					walk(child)
				}
			}
			walk(doc)
		}
	}

	unresolved = warningCounts[WarnXMLUnknownReferenceClass]

	t.Logf("converted %d pages with the reference index", totalPages)
	t.Logf("  mentions resolved to placeholders: %d", totalMentions)
	for _, code := range []string{WarnUnsupportedMacro, WarnXMLUnknownReferenceClass, WarnDangerousURLRemoved} {
		t.Logf("  %-28s %d", code, warningCounts[code])
	}

	require.Positive(t, totalMentions, "the sample is full of user mentions; none resolving means the index is not wired")
	require.Less(t, unresolved, totalMentions,
		"most references must resolve once the real index is in place")
}
