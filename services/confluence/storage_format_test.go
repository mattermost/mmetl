package confluence

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// convert runs the converter and fails the test on a conversion error.
func convert(t *testing.T, storage string) (*TipTapNode, []Warning) {
	t.Helper()

	converter := NewConverter()
	encoded, err := converter.Convert(storage, "26542273")
	require.NoError(t, err)

	var doc TipTapNode
	require.NoError(t, json.Unmarshal([]byte(encoded), &doc))
	require.Equal(t, NodeDoc, doc.Type)

	return &doc, converter.Warnings()
}

// blockTypes lists the top-level block types of a converted document.
func blockTypes(doc *TipTapNode) []string {
	types := make([]string, 0, len(doc.Content))
	for _, node := range doc.Content {
		types = append(types, node.Type)
	}
	return types
}

// flatten renders a node subtree as plain text, so a test can assert content
// without restating the whole tree.
func flatten(node *TipTapNode) string {
	var b strings.Builder
	var walk func(*TipTapNode)
	walk = func(n *TipTapNode) {
		b.WriteString(n.Text)
		for _, child := range n.Content {
			walk(child)
		}
	}
	walk(node)
	return b.String()
}

func markTypes(node *TipTapNode) []string {
	types := make([]string, 0, len(node.Marks))
	for _, mark := range node.Marks {
		types = append(types, mark.Type)
	}
	return types
}

func TestConvert_Paragraphs(t *testing.T) {
	doc, warnings := convert(t, `<p>First</p><p>Second</p>`)
	require.Empty(t, warnings)

	require.Equal(t, []string{NodeParagraph, NodeParagraph}, blockTypes(doc))
	require.Equal(t, "First", flatten(doc.Content[0]))
	require.Equal(t, "Second", flatten(doc.Content[1]))
}

// An empty body is a page with nothing on it, not a failure: Confluence pages
// are routinely empty and section 1 requires an empty page plus a warning.
func TestConvert_EmptyBodyIsTheCanonicalEmptyDocument(t *testing.T) {
	for _, storage := range []string{"", "   ", "<p></p>", "<ac:layout><ac:layout-section ac:type=\"single\"><ac:layout-cell></ac:layout-cell></ac:layout-section></ac:layout>"} {
		converter := NewConverter()
		encoded, err := converter.Convert(storage, "1")
		require.NoError(t, err, storage)
		require.Equal(t, EmptyDocumentJSON, encoded, storage)
	}
}

func TestConvert_Headings(t *testing.T) {
	doc, _ := convert(t, `<h1>One</h1><h3>Three</h3><h6>Six</h6>`)

	require.Equal(t, []string{NodeHeading, NodeHeading, NodeHeading}, blockTypes(doc))
	require.Equal(t, 1, int(doc.Content[0].Attrs["level"].(float64)))
	require.Equal(t, 3, int(doc.Content[1].Attrs["level"].(float64)))
	require.Equal(t, 6, int(doc.Content[2].Attrs["level"].(float64)))
	require.Equal(t, "Three", flatten(doc.Content[1]))
}

func TestConvert_Marks(t *testing.T) {
	doc, _ := convert(t,
		`<p><strong>b</strong><b>b2</b><em>i</em><i>i2</i><u>u</u><s>s</s><del>d</del><code>c</code></p>`)

	require.Len(t, doc.Content, 1)
	inline := doc.Content[0].Content
	require.Len(t, inline, 8)

	require.Equal(t, []string{MarkBold}, markTypes(inline[0]))
	require.Equal(t, []string{MarkBold}, markTypes(inline[1]))
	require.Equal(t, []string{MarkItalic}, markTypes(inline[2]))
	require.Equal(t, []string{MarkItalic}, markTypes(inline[3]))
	require.Equal(t, []string{MarkUnderline}, markTypes(inline[4]))
	require.Equal(t, []string{MarkStrike}, markTypes(inline[5]))
	require.Equal(t, []string{MarkStrike}, markTypes(inline[6]))
	require.Equal(t, []string{MarkCode}, markTypes(inline[7]))
}

func TestConvert_NestedMarks(t *testing.T) {
	doc, _ := convert(t, `<p><strong>bold <em>and italic</em></strong></p>`)

	inline := doc.Content[0].Content
	require.Len(t, inline, 2)
	require.Equal(t, []string{MarkBold}, markTypes(inline[0]))
	require.Equal(t, []string{MarkBold, MarkItalic}, markTypes(inline[1]))
}

// Marks accumulate down a shared tree. Appending in place would leak one
// sibling's mark onto the next.
func TestConvert_MarksDoNotLeakBetweenSiblings(t *testing.T) {
	doc, _ := convert(t, `<p><em><strong>a</strong></em><em>b</em></p>`)

	inline := doc.Content[0].Content
	require.Len(t, inline, 2)
	require.Equal(t, []string{MarkItalic, MarkBold}, markTypes(inline[0]))
	require.Equal(t, []string{MarkItalic}, markTypes(inline[1]))
}

func TestConvert_HardBreak(t *testing.T) {
	doc, _ := convert(t, `<p>before<br/>after</p>`)

	inline := doc.Content[0].Content
	require.Len(t, inline, 3)
	require.Equal(t, NodeHardBreak, inline[1].Type)
}

func TestConvert_Lists(t *testing.T) {
	doc, _ := convert(t, `<ul><li><p>one</p></li><li>two</li></ul><ol><li>first</li></ol>`)

	require.Equal(t, []string{NodeBulletList, NodeOrderedList}, blockTypes(doc))

	bullet := doc.Content[0]
	require.Len(t, bullet.Content, 2)
	require.Equal(t, NodeListItem, bullet.Content[0].Type)

	// A list item must hold blocks, so bare text inside <li> is wrapped.
	require.Equal(t, NodeParagraph, bullet.Content[1].Content[0].Type)
	require.Equal(t, "two", flatten(bullet.Content[1]))
}

func TestConvert_NestedLists(t *testing.T) {
	doc, _ := convert(t, `<ul><li>outer<ul><li>inner</li></ul></li></ul>`)

	outer := doc.Content[0]
	require.Equal(t, NodeBulletList, outer.Type)
	item := outer.Content[0]
	require.Equal(t, []string{NodeParagraph, NodeBulletList}, blockTypes(item))
	require.Equal(t, "inner", flatten(item.Content[1]))
}

func TestConvert_Blockquote(t *testing.T) {
	doc, _ := convert(t, `<blockquote><p>quoted</p></blockquote>`)

	require.Equal(t, []string{NodeBlockquote}, blockTypes(doc))
	require.Equal(t, NodeParagraph, doc.Content[0].Content[0].Type)
	require.Equal(t, "quoted", flatten(doc.Content[0]))
}

// A TipTap codeBlock holds text only. Markup inside a Confluence <pre> is
// display markup rather than content, so it is flattened.
func TestConvert_CodeBlock(t *testing.T) {
	doc, _ := convert(t, "<pre>func main() {\n\tprintln(\"hi\")\n}</pre>")

	require.Equal(t, []string{NodeCodeBlock}, blockTypes(doc))
	require.Equal(t, "func main() {\n\tprintln(\"hi\")\n}", flatten(doc.Content[0]))

	withMarkup, _ := convert(t, `<pre><span>a</span><b>b</b></pre>`)
	require.Equal(t, "ab", flatten(withMarkup.Content[0]))
	require.Empty(t, withMarkup.Content[0].Content[0].Marks)
}

func TestConvert_HorizontalRule(t *testing.T) {
	doc, _ := convert(t, `<p>a</p><hr/><p>b</p>`)
	require.Equal(t, []string{NodeParagraph, NodeHorizontalRule, NodeParagraph}, blockTypes(doc))
}

func TestConvert_Tables(t *testing.T) {
	doc, _ := convert(t, `<table><tbody>`+
		`<tr><th>Head</th><th colspan="2">Wide</th></tr>`+
		`<tr><td><p>Cell</p></td><td></td><td>bare</td></tr>`+
		`</tbody></table>`)

	require.Equal(t, []string{NodeTable}, blockTypes(doc))
	table := doc.Content[0]
	require.Len(t, table.Content, 2, "tbody is transparent, its rows belong to the table")

	header := table.Content[0]
	require.Equal(t, NodeTableRow, header.Type)
	require.Equal(t, NodeTableHeader, header.Content[0].Type)
	require.Equal(t, "Head", flatten(header.Content[0]))

	// colspan is carried through; dropping it would silently reshape the table.
	require.Equal(t, float64(2), header.Content[1].Attrs["colspan"])

	body := table.Content[1]
	require.Equal(t, NodeTableCell, body.Content[0].Type)
	require.Equal(t, "Cell", flatten(body.Content[0]))

	// A TipTap cell must hold at least one block, so an empty cell gets one.
	require.Equal(t, NodeParagraph, body.Content[1].Content[0].Type)
	require.Equal(t, "bare", flatten(body.Content[2]))
}

func TestConvert_Links(t *testing.T) {
	t.Run("safe schemes keep the link", func(t *testing.T) {
		for _, href := range []string{
			"https://example.com/page",
			"http://example.com",
			"mailto:a@example.com",
			"tel:+15551234",
			"/relative/path",
			"#anchor",
		} {
			doc, warnings := convert(t, `<p><a href="`+href+`">text</a></p>`)
			require.Empty(t, warnings, href)

			inline := doc.Content[0].Content[0]
			require.Equal(t, []string{MarkLink}, markTypes(inline), href)
			require.Equal(t, href, inline.Marks[0].Attrs["href"], href)
		}
	})

	t.Run("dangerous schemes lose the link but keep the text", func(t *testing.T) {
		for _, href := range []string{
			"javascript:alert(1)",
			"vbscript:msgbox(1)",
			// Obfuscated with an HTML entity for the tab a browser ignores.
			"java&Tab;script:alert(1)",
			"java\tscript:alert(1)",
		} {
			doc, warnings := convert(t, `<p><a href="`+href+`">click me</a></p>`)

			inline := doc.Content[0].Content[0]
			require.Empty(t, markTypes(inline), href)
			require.Equal(t, "click me", flatten(doc.Content[0]), href)

			require.Len(t, warnings, 1, href)
			require.Equal(t, WarnDangerousURLRemoved, warnings[0].Code, href)
		}
	})

	t.Run("a link with no text is labelled by its target", func(t *testing.T) {
		doc, _ := convert(t, `<p><a href="https://example.com"></a></p>`)
		require.Equal(t, "https://example.com", flatten(doc.Content[0]))
	})

	t.Run("a link with no href degrades to text", func(t *testing.T) {
		doc, _ := convert(t, `<p><a>bare</a></p>`)
		require.Equal(t, "bare", flatten(doc.Content[0]))
		require.Empty(t, doc.Content[0].Content[0].Marks)
	})
}

func TestSanitizeURL(t *testing.T) {
	for _, safe := range []string{
		"https://example.com", "HTTPS://EXAMPLE.COM", "mailto:a@b.c", "tel:+1",
		"/relative", "relative/path", "?query", "#frag", "",
	} {
		require.Equalf(t, safe, SanitizeURL(safe), "%q must be kept", safe)
	}

	for _, dangerous := range []string{
		"javascript:alert(1)", "JavaScript:alert(1)", " javascript:alert(1)",
		"java\nscript:alert(1)", "java&Tab;script&colon;alert(1)",
		"vbscript:x", "data:text/html;base64,PHNjcmlwdD4=", "file:///etc/passwd",
	} {
		require.Emptyf(t, SanitizeURL(dangerous), "%q must be dropped", dangerous)
	}
}

// Bare text and inline markup sit directly inside Confluence layout and table
// cells, which TipTap's block-only containers cannot hold.
func TestConvert_StrayInlineContentIsWrapped(t *testing.T) {
	doc, _ := convert(t, `bare text <strong>bold</strong><p>a paragraph</p>more text`)

	require.Equal(t, []string{NodeParagraph, NodeParagraph, NodeParagraph}, blockTypes(doc))
	require.Equal(t, "bare text bold", flatten(doc.Content[0]))
	require.Equal(t, "a paragraph", flatten(doc.Content[1]))
	require.Equal(t, "more text", flatten(doc.Content[2]))
}

// sampleBody is a Confluence Cloud body copied from the private export. Every
// page in that sample is wrapped in ac:layout, so a converter that dropped the
// wrapper's children would empty every page.
const sampleBody = `<ac:layout><ac:layout-section ac:type="two_equal"><ac:layout-cell>

            <p><span style="color: rgb(151,160,175);">Say hello to your colleagues who want to know your name, pronouns, role, team and location (or if you&apos;re remote).</span></p>
        </ac:layout-cell><ac:layout-cell>

            <p />
        </ac:layout-cell></ac:layout-section><ac:layout-section ac:type="fixed-width"><ac:layout-cell>

            <h2>&#128196; Recent content that I&apos;ve worked on</h2>
        </ac:layout-cell></ac:layout-section><ac:layout-section ac:type="two_equal"><ac:layout-cell>

            <ac:structured-macro ac:name="recently-updated" ac:schema-version="1" ac:macro-id="f9d6b4ce"><ac:parameter ac:name="max">5</ac:parameter></ac:structured-macro>
        </ac:layout-cell><ac:layout-cell>

            <p>&#9993;&#65039; <ac:placeholder>Insert your email here</ac:placeholder></p>
        </ac:layout-cell></ac:layout-section></ac:layout>`

func TestConvert_RealConfluenceBody(t *testing.T) {
	doc, warnings := convert(t, sampleBody)

	text := flatten(doc)
	require.Contains(t, text, "Say hello to your colleagues")
	require.Contains(t, text, "Recent content that I've worked on")

	// Template placeholder text is visible in Confluence, so dropping it would
	// lose text a reader sees.
	require.Contains(t, text, "Insert your email here")

	// The layout wrapper has no TipTap equivalent; its columns flatten into
	// document order rather than disappearing.
	require.Contains(t, blockTypes(doc), NodeHeading)
	require.Contains(t, blockTypes(doc), NodeParagraph)

	// The macro has no destination equivalent, so it is visible in the page and
	// reported, not silently dropped.
	require.Contains(t, text, "[Unsupported Confluence macro: recently-updated]")
	require.NotContains(t, text, "5", "macro parameters are configuration, not content")

	require.Len(t, warnings, 1)
	require.Equal(t, WarnUnsupportedMacro, warnings[0].Code)
	require.Equal(t, "26542273", warnings[0].SourceID)
}

// HTML entities are not XML predefined entities. A parser that rejected them
// would fail on any Confluence body containing &nbsp;.
func TestConvert_HTMLEntities(t *testing.T) {
	doc, _ := convert(t, `<p>a&nbsp;b &amp; c &mdash; d &apos;e&apos;</p>`)

	text := flatten(doc.Content[0])
	require.Contains(t, text, "&")
	require.Contains(t, text, "—")
	require.Contains(t, text, "'e'")
	require.NotContains(t, text, "&nbsp;")
}

func TestConvert_UnsupportedMacroKeepsItsBody(t *testing.T) {
	doc, warnings := convert(t,
		`<ac:structured-macro ac:name="expand"><ac:parameter ac:name="title">More</ac:parameter>`+
			`<ac:rich-text-body><p>hidden but real</p></ac:rich-text-body></ac:structured-macro>`)

	require.Equal(t, []string{NodeParagraph, NodeParagraph}, blockTypes(doc))
	require.Equal(t, "[Unsupported Confluence macro: expand]", flatten(doc.Content[0]))
	require.Equal(t, "hidden but real", flatten(doc.Content[1]))

	require.Len(t, warnings, 1)
	require.Contains(t, warnings[0].Message, "expand")
}

func TestConvert_RejectsOversizedContent(t *testing.T) {
	t.Run("too many nodes", func(t *testing.T) {
		storage := strings.Repeat("<p>x</p>", MaxTipTapNodes)

		converter := NewConverter()
		_, err := converter.Convert(storage, "1")
		require.Error(t, err)

		var conversionErr *ConversionError
		require.ErrorAs(t, err, &conversionErr)
		require.Equal(t, WarnPageContentTooLarge, conversionErr.Code)
		require.Contains(t, err.Error(), "node limit")
	})

	t.Run("too deeply nested", func(t *testing.T) {
		storage := strings.Repeat("<blockquote>", MaxTipTapDepth+5) +
			"<p>deep</p>" + strings.Repeat("</blockquote>", MaxTipTapDepth+5)

		converter := NewConverter()
		_, err := converter.Convert(storage, "1")
		require.Error(t, err)

		var conversionErr *ConversionError
		require.ErrorAs(t, err, &conversionErr)
		require.Contains(t, err.Error(), "level limit")
	})

	t.Run("body over the byte limit", func(t *testing.T) {
		storage := "<p>" + strings.Repeat("x", BodyMaxBytes+1) + "</p>"

		converter := NewConverter()
		_, err := converter.Convert(storage, "1")
		require.Error(t, err)
		require.Contains(t, err.Error(), "byte limit")
	})
}

// The output must be a valid TipTap document, since the destination parses it
// with the same expectations.
func TestConvert_OutputIsWellFormed(t *testing.T) {
	doc, _ := convert(t, sampleBody)

	var walk func(node *TipTapNode, depth int)
	walk = func(node *TipTapNode, depth int) {
		require.LessOrEqual(t, depth, MaxTipTapDepth)
		require.NotEmpty(t, node.Type)
		require.Contains(t, allowedNodeTypesForTest, node.Type, node.Type)

		for _, mark := range node.Marks {
			require.Contains(t, allowedMarkTypesForTest, mark.Type, mark.Type)
		}
		if node.Type == NodeText {
			require.NotEmpty(t, node.Text)
			require.Empty(t, node.Content, "a text node has no children")
		}
		for _, child := range node.Content {
			walk(child, depth+1)
		}
	}
	walk(doc, 0)
}

// allowedNodeTypesForTest and allowedMarkTypesForTest mirror the Docs editor's
// allowlists. A type outside them would be stripped on import, so the converter
// must never emit one.
var allowedNodeTypesForTest = []string{
	NodeDoc, NodeParagraph, NodeText, NodeHeading, NodeHardBreak, NodeHorizontalRule,
	NodeBlockquote, NodeCodeBlock, NodeBulletList, NodeOrderedList, NodeListItem,
	NodeTable, NodeTableRow, NodeTableCell, NodeTableHeader,
}

var allowedMarkTypesForTest = []string{
	MarkBold, MarkItalic, MarkStrike, MarkCode, MarkUnderline, MarkLink,
}

// TestConvert_ConfluenceLinkSurvivesParsing is a regression test for a real
// body in the discovery sample. The stock xml.HTMLAutoClose list matches on
// local name alone, so it treated Confluence's <ac:link> as the HTML void
// element <link>, closed it immediately, and then failed on the real
// </ac:link>. Every page and attachment link would have been destroyed.
func TestConvert_ConfluenceLinkSurvivesParsing(t *testing.T) {
	root, err := parseStorageFormat(
		`<p><ac:link><ri:page ri:content-title="Target"/><ac:plain-text-link-body>See it</ac:plain-text-link-body></ac:link></p>`)
	require.NoError(t, err)

	require.Len(t, root.Children, 1)
	paragraph := root.Children[0]
	require.Equal(t, "p", paragraph.LocalName())

	link := paragraph.Children[0]
	require.Equal(t, "link", link.LocalName())
	require.True(t, link.IsConfluence())
	require.Len(t, link.Children, 2, "the link keeps its target and its body")
	require.Equal(t, "Target", link.Children[0].Attr("content-title"))
	require.Equal(t, "See it", link.PlainText())
}

// Void elements still auto-close, which is what the stock list was wanted for.
func TestConvert_VoidElementsStillAutoClose(t *testing.T) {
	doc, _ := convert(t, `<p>a<br>b</p>`)
	require.Equal(t, NodeHardBreak, doc.Content[0].Content[1].Type)
}

// TestConvert_PrivateSampleBodies is the E10 gate: every body in a real export
// must convert to a document the destination would accept.
//
//	CONFLUENCE_SAMPLE_ZIP=/path/to/Confluence-export.zip go test ./services/confluence/...
func TestConvert_PrivateSampleBodies(t *testing.T) {
	path := os.Getenv("CONFLUENCE_SAMPLE_ZIP")
	if path == "" {
		t.Skip("set CONFLUENCE_SAMPLE_ZIP to run against a real Confluence export")
	}

	archive, err := OpenSourceArchive(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, archive.Close()) }()

	entities, err := archive.OpenEntities()
	require.NoError(t, err)
	defer func() { require.NoError(t, entities.Close()) }()

	decoder := NewObjectDecoder(entities)
	decoder.SetAccept(func(class ClassRef) bool { return class == ClassBodyContent })

	allowedNodes := map[string]bool{}
	for _, name := range allowedNodeTypesForTest {
		allowedNodes[name] = true
	}
	allowedMarks := map[string]bool{}
	for _, name := range allowedMarkTypesForTest {
		allowedMarks[name] = true
	}

	var (
		bodies      int
		empty       int
		warningKeys = map[string]int{}
	)

	for {
		object, err := decoder.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)

		storage := object.ScalarValue("body")
		bodies++

		converter := NewConverter()
		encoded, err := converter.Convert(storage, object.Key.ID)
		require.NoErrorf(t, err, "body %s failed to convert", object.Key.ID)

		if encoded == EmptyDocumentJSON {
			empty++
		}
		for _, warning := range converter.Warnings() {
			warningKeys[warning.Code]++
		}

		var doc TipTapNode
		require.NoError(t, json.Unmarshal([]byte(encoded), &doc))
		require.Equal(t, NodeDoc, doc.Type)

		var walk func(node *TipTapNode, depth int)
		walk = func(node *TipTapNode, depth int) {
			require.LessOrEqualf(t, depth, MaxTipTapDepth, "body %s", object.Key.ID)
			require.Truef(t, allowedNodes[node.Type], "body %s emitted node %q", object.Key.ID, node.Type)
			for _, mark := range node.Marks {
				require.Truef(t, allowedMarks[mark.Type], "body %s emitted mark %q", object.Key.ID, mark.Type)
			}
			if node.Type == NodeText {
				require.NotEmptyf(t, node.Text, "body %s emitted an empty text node", object.Key.ID)
			}
			for _, child := range node.Content {
				walk(child, depth+1)
			}
		}
		walk(&doc, 0)
	}

	require.Positive(t, bodies)

	codes := make([]string, 0, len(warningKeys))
	for code := range warningKeys {
		codes = append(codes, code)
	}
	sort.Strings(codes)

	t.Logf("converted %d bodies, %d of them empty", bodies, empty)
	for _, code := range codes {
		t.Logf("  %-24s %d", code, warningKeys[code])
	}
}
