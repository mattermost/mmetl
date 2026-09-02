package confluence

import (
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"strconv"
	"strings"
)

// TipTap node and mark type names. Every one of these is on the Docs editor's
// allowlist; a name that is not would be stripped on import, so the converter
// emits nothing outside this set.
const (
	NodeDoc            = "doc"
	NodeParagraph      = "paragraph"
	NodeText           = "text"
	NodeHeading        = "heading"
	NodeHardBreak      = "hardBreak"
	NodeHorizontalRule = "horizontalRule"
	NodeBlockquote     = "blockquote"
	NodeCodeBlock      = "codeBlock"
	NodeBulletList     = "bulletList"
	NodeOrderedList    = "orderedList"
	NodeListItem       = "listItem"
	NodeTable          = "table"
	NodeTableRow       = "tableRow"
	NodeTableCell      = "tableCell"
	NodeTableHeader    = "tableHeader"
	NodeTaskList       = "taskList"
	NodeTaskItem       = "taskItem"
	NodeMention        = "mention"
	NodeCallout        = "callout"
	NodeImage          = "image"

	MarkBold      = "bold"
	MarkItalic    = "italic"
	MarkStrike    = "strike"
	MarkCode      = "code"
	MarkUnderline = "underline"
	MarkLink      = "link"
)

// MaxTipTapDepth and MaxTipTapNodes mirror the Docs parser's limits. Exceeding
// either is a hard per-page conversion error, because the destination would
// reject the page and a truncated body is worse than a reported skip.
const (
	MaxTipTapDepth = 100
	MaxTipTapNodes = 50_000
)

// storageNamespace is the prefix Confluence uses for its own elements. The
// storage format declares no namespaces, so encoding/xml leaves the raw prefix
// in Name.Space and matching on it is exact.
const storageNamespace = "ac"

// TipTapNode is one node of a TipTap document.
type TipTapNode struct {
	Type    string         `json:"type,omitempty"`
	Attrs   map[string]any `json:"attrs,omitempty"`
	Content []*TipTapNode  `json:"content,omitempty"`
	Marks   []TipTapMark   `json:"marks,omitempty"`
	Text    string         `json:"text,omitempty"`
}

// TipTapMark is one inline mark.
type TipTapMark struct {
	Type  string         `json:"type"`
	Attrs map[string]any `json:"attrs,omitempty"`
}

// ConversionError is a per-page conversion failure. The page is skipped, its
// descendants are promoted, and unrelated pages are unaffected.
type ConversionError struct {
	Code    string
	Message string
}

func (e *ConversionError) Error() string { return e.Code + ": " + e.Message }

// Converter turns Confluence Storage Format into TipTap JSON.
//
// It works on a parsed tree. Rewriting the storage format with string
// replacements, or patching serialized TipTap JSON afterwards, would break on
// the first body whose text happens to look like markup.
type Converter struct {
	warnings  []Warning
	nodeCount int
	sourceID  string
	context   *ConversionContext
}

// NewConverter returns a converter for one page or comment.
func NewConverter() *Converter { return &Converter{} }

// Warnings returns what the last conversion reported.
func (c *Converter) Warnings() []Warning { return c.warnings }

// Convert parses a Confluence Storage Format body and returns TipTap JSON.
//
// A body that parses to nothing becomes the canonical empty document rather
// than an error: Confluence pages are routinely empty, and section 1 requires
// an empty page plus a warning rather than a skip.
func (c *Converter) Convert(storage, sourceID string) (string, error) {
	c.warnings = nil
	c.nodeCount = 0
	c.sourceID = sourceID

	root, err := parseStorageFormat(storage)
	if err != nil {
		return "", &ConversionError{
			Code:    WarnPageContentTooLarge,
			Message: fmt.Sprintf("body could not be parsed as Confluence storage format: %v", err),
		}
	}

	blocks, err := c.convertBlocks(root.Children, 1)
	if err != nil {
		return "", err
	}
	if len(blocks) == 0 {
		return EmptyDocumentJSON, nil
	}

	doc := &TipTapNode{Type: NodeDoc, Content: blocks}
	encoded, err := MarshalCanonical(doc)
	if err != nil {
		return "", &ConversionError{Code: WarnPageContentTooLarge, Message: err.Error()}
	}
	if len(encoded) > BodyMaxBytes {
		return "", &ConversionError{
			Code:    WarnPageContentTooLarge,
			Message: fmt.Sprintf("converted body is %d bytes, over the %d byte limit", len(encoded), BodyMaxBytes),
		}
	}
	return string(encoded), nil
}

func (c *Converter) warn(code, message string) {
	c.warnings = append(c.warnings, Warning{
		Code:       code,
		EntityType: "page",
		SourceID:   c.sourceID,
		Message:    TruncateMessage(message),
	})
}

// budget accounts for one emitted node, failing the page once the destination's
// node ceiling is reached.
func (c *Converter) budget() error {
	c.nodeCount++
	if c.nodeCount > MaxTipTapNodes {
		return &ConversionError{
			Code:    WarnPageContentTooLarge,
			Message: fmt.Sprintf("converted body exceeds the %d node limit", MaxTipTapNodes),
		}
	}
	return nil
}

func (c *Converter) checkDepth(depth int) error {
	if depth > MaxTipTapDepth {
		return &ConversionError{
			Code:    WarnPageContentTooLarge,
			Message: fmt.Sprintf("converted body nests deeper than the %d level limit", MaxTipTapDepth),
		}
	}
	return nil
}

func (c *Converter) node(nodeType string, depth int) (*TipTapNode, error) {
	if err := c.budget(); err != nil {
		return nil, err
	}
	if err := c.checkDepth(depth); err != nil {
		return nil, err
	}
	return &TipTapNode{Type: nodeType}, nil
}

// convertBlocks converts a run of source children into block-level TipTap
// nodes, gathering stray inline content into paragraphs as it goes.
//
// Confluence puts bare text and inline markup directly inside layout cells and
// table cells, which TipTap's block-only containers cannot hold, so it has to
// be wrapped rather than dropped.
func (c *Converter) convertBlocks(children []*storageNode, depth int) ([]*TipTapNode, error) {
	if err := c.checkDepth(depth); err != nil {
		return nil, err
	}

	var blocks []*TipTapNode
	var pending []*TipTapNode

	flush := func() error {
		pending = trimTrailingWhitespaceNodes(pending)
		if len(pending) == 0 {
			return nil
		}
		paragraph, err := c.node(NodeParagraph, depth)
		if err != nil {
			return err
		}
		paragraph.Content = pending
		blocks = append(blocks, paragraph)
		pending = nil
		return nil
	}

	for _, child := range children {
		if child.IsText() {
			// Whitespace between block elements is layout, not content.
			// Confluence indents its storage format, so keeping it would give
			// every page a paragraph of spaces between each real one. A
			// whitespace run that separates inline content is kept, because
			// there it is the space between two words.
			if len(pending) == 0 && strings.TrimSpace(child.Text) == "" {
				continue
			}
			inline, err := c.convertInline([]*storageNode{child}, nil, depth+1)
			if err != nil {
				return nil, err
			}
			pending = append(pending, inline...)
			continue
		}

		converted, isBlock, err := c.convertElement(child, depth)
		if err != nil {
			return nil, err
		}
		if !isBlock {
			inline, err := c.convertInline([]*storageNode{child}, nil, depth+1)
			if err != nil {
				return nil, err
			}
			pending = append(pending, inline...)
			continue
		}
		if err := flush(); err != nil {
			return nil, err
		}
		blocks = append(blocks, converted...)
	}

	if err := flush(); err != nil {
		return nil, err
	}
	return blocks, nil
}

// trimTrailingWhitespaceNodes drops whitespace-only text runs from the end of a
// gathered paragraph, which is the indentation before the next block element
// rather than part of the sentence.
func trimTrailingWhitespaceNodes(nodes []*TipTapNode) []*TipTapNode {
	for len(nodes) > 0 {
		last := nodes[len(nodes)-1]
		if last.Type != NodeText || strings.TrimSpace(last.Text) != "" {
			break
		}
		nodes = nodes[:len(nodes)-1]
	}
	return nodes
}

// convertElement converts one block-level element. isBlock is false when the
// element is inline, so the caller can gather it into a paragraph instead.
func (c *Converter) convertElement(element *storageNode, depth int) (nodes []*TipTapNode, isBlock bool, err error) {
	name := element.LocalName()

	switch {
	// Checked before the Confluence branch: ac:layout and its sections and
	// cells are Confluence-namespaced but purely structural.
	case isTransparentBlock(name):
		nodes, err = c.convertBlocks(element.Children, depth+1)
		return nodes, true, err

	// Confluence emits ac:link at block level when a link is rendered as a card,
	// and an emoticon or comment marker can sit alone between paragraphs. They
	// are inline content wherever they appear, so they are reported as inline
	// and the caller gathers them into a paragraph. Converting them as blocks
	// would drop the link and keep only its text.
	case element.IsConfluence() && isInlineConfluenceElement(name):
		return nil, false, nil

	case element.IsConfluence():
		nodes, err = c.convertConfluenceElement(element, depth)
		return nodes, true, err

	case name == "p":
		// Confluence puts images, task lists and block macros inside <p>. TipTap
		// paragraphs hold inline content only, so such a paragraph is treated as
		// a block container and splits around them.
		if containsBlockElement(element.Children) {
			nodes, err = c.convertBlocks(element.Children, depth+1)
			return nodes, true, err
		}
		node, err := c.node(NodeParagraph, depth)
		if err != nil {
			return nil, true, err
		}
		node.Content, err = c.convertInline(element.Children, nil, depth+1)
		return []*TipTapNode{node}, true, err

	case isHeading(name):
		return c.convertHeading(element, name, depth)

	case name == "blockquote":
		node, err := c.node(NodeBlockquote, depth)
		if err != nil {
			return nil, true, err
		}
		node.Content, err = c.convertBlocks(element.Children, depth+1)
		if err != nil {
			return nil, true, err
		}
		if len(node.Content) == 0 {
			return nil, true, nil
		}
		return []*TipTapNode{node}, true, nil

	case name == "pre":
		return c.convertCodeBlock(element, depth)

	case name == "ul", name == "ol":
		return c.convertList(element, name, depth)

	case name == "table":
		return c.convertTable(element, depth)

	case name == "hr":
		node, err := c.node(NodeHorizontalRule, depth)
		if err != nil {
			return nil, true, err
		}
		return []*TipTapNode{node}, true, nil

	default:
		return nil, false, nil
	}
}

func (c *Converter) convertHeading(element *storageNode, name string, depth int) ([]*TipTapNode, bool, error) {
	node, err := c.node(NodeHeading, depth)
	if err != nil {
		return nil, true, err
	}
	level, _ := strconv.Atoi(name[1:])
	node.Attrs = map[string]any{"level": level}
	node.Content, err = c.convertInline(element.Children, nil, depth+1)
	return []*TipTapNode{node}, true, err
}

// convertCodeBlock flattens a code block to plain text. TipTap's codeBlock
// holds text only, and any markup inside a Confluence <pre> is display markup
// rather than content.
func (c *Converter) convertCodeBlock(element *storageNode, depth int) ([]*TipTapNode, bool, error) {
	node, err := c.node(NodeCodeBlock, depth)
	if err != nil {
		return nil, true, err
	}

	text := element.PlainText()
	if text == "" {
		return nil, true, nil
	}
	textNode, err := c.textNode(text, nil, depth+1)
	if err != nil {
		return nil, true, err
	}
	node.Content = []*TipTapNode{textNode}
	return []*TipTapNode{node}, true, nil
}

func (c *Converter) convertList(element *storageNode, name string, depth int) ([]*TipTapNode, bool, error) {
	listType := NodeBulletList
	if name == "ol" {
		listType = NodeOrderedList
	}

	list, err := c.node(listType, depth)
	if err != nil {
		return nil, true, err
	}

	for _, child := range element.Children {
		if child.IsText() || child.LocalName() != "li" {
			continue
		}

		item, err := c.node(NodeListItem, depth+1)
		if err != nil {
			return nil, true, err
		}
		item.Content, err = c.convertBlocks(child.Children, depth+2)
		if err != nil {
			return nil, true, err
		}
		// TipTap requires a listItem to hold at least one block.
		if len(item.Content) == 0 {
			empty, err := c.node(NodeParagraph, depth+2)
			if err != nil {
				return nil, true, err
			}
			item.Content = []*TipTapNode{empty}
		}
		list.Content = append(list.Content, item)
	}

	if len(list.Content) == 0 {
		return nil, true, nil
	}
	return []*TipTapNode{list}, true, nil
}

// convertTable builds table > tableRow > (tableHeader|tableCell) > blocks.
//
// thead, tbody and tfoot are transparent: TipTap has no row-group node, and
// their rows belong directly to the table.
func (c *Converter) convertTable(element *storageNode, depth int) ([]*TipTapNode, bool, error) {
	table, err := c.node(NodeTable, depth)
	if err != nil {
		return nil, true, err
	}

	var walkRows func(children []*storageNode) error
	walkRows = func(children []*storageNode) error {
		for _, child := range children {
			if child.IsText() {
				continue
			}
			switch child.LocalName() {
			case "thead", "tbody", "tfoot":
				if err := walkRows(child.Children); err != nil {
					return err
				}
			case "tr":
				row, err := c.convertTableRow(child, depth+1)
				if err != nil {
					return err
				}
				if row != nil {
					table.Content = append(table.Content, row)
				}
			}
		}
		return nil
	}

	if err := walkRows(element.Children); err != nil {
		return nil, true, err
	}
	if len(table.Content) == 0 {
		return nil, true, nil
	}
	return []*TipTapNode{table}, true, nil
}

func (c *Converter) convertTableRow(element *storageNode, depth int) (*TipTapNode, error) {
	row, err := c.node(NodeTableRow, depth)
	if err != nil {
		return nil, err
	}

	for _, child := range element.Children {
		if child.IsText() {
			continue
		}
		cellType := ""
		switch child.LocalName() {
		case "th":
			cellType = NodeTableHeader
		case "td":
			cellType = NodeTableCell
		default:
			continue
		}

		cell, err := c.node(cellType, depth+1)
		if err != nil {
			return nil, err
		}
		cell.Attrs = tableCellAttrs(child)
		cell.Content, err = c.convertBlocks(child.Children, depth+2)
		if err != nil {
			return nil, err
		}
		// A TipTap cell must hold at least one block.
		if len(cell.Content) == 0 {
			empty, err := c.node(NodeParagraph, depth+2)
			if err != nil {
				return nil, err
			}
			cell.Content = []*TipTapNode{empty}
		}
		row.Content = append(row.Content, cell)
	}

	if len(row.Content) == 0 {
		return nil, nil
	}
	return row, nil
}

// tableCellAttrs carries colspan and rowspan through. Dropping them would
// silently reshape a merged table.
func tableCellAttrs(element *storageNode) map[string]any {
	attrs := map[string]any{}
	for _, name := range []string{"colspan", "rowspan"} {
		raw := element.Attr(name)
		if raw == "" {
			continue
		}
		if value, err := strconv.Atoi(raw); err == nil && value > 1 {
			attrs[name] = value
		}
	}
	if len(attrs) == 0 {
		return nil
	}
	return attrs
}

// convertInline converts inline content, accumulating marks down the tree.
func (c *Converter) convertInline(children []*storageNode, marks []TipTapMark, depth int) ([]*TipTapNode, error) {
	if err := c.checkDepth(depth); err != nil {
		return nil, err
	}

	var nodes []*TipTapNode
	for _, child := range children {
		if child.IsText() {
			if child.Text == "" {
				continue
			}
			node, err := c.textNode(child.Text, marks, depth)
			if err != nil {
				return nil, err
			}
			nodes = append(nodes, node)
			continue
		}

		converted, err := c.convertInlineElement(child, marks, depth)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, converted...)
	}
	return nodes, nil
}

func (c *Converter) convertInlineElement(element *storageNode, marks []TipTapMark, depth int) ([]*TipTapNode, error) {
	name := element.LocalName()

	if element.IsConfluence() {
		return c.convertConfluenceInline(element, marks, depth)
	}

	if mark, ok := inlineMarkFor(name); ok {
		return c.convertInline(element.Children, appendMark(marks, TipTapMark{Type: mark}), depth+1)
	}

	switch name {
	case "br":
		node, err := c.node(NodeHardBreak, depth)
		if err != nil {
			return nil, err
		}
		return []*TipTapNode{node}, nil

	case "a":
		return c.convertLink(element, marks, depth)

	default:
		// Anything else, span and font included, contributes its children. The
		// style attribute a Confluence span carries is stripped by the
		// destination sanitizer anyway, so keeping the wrapper would gain
		// nothing.
		return c.convertInline(element.Children, marks, depth+1)
	}
}

// convertLink applies a link mark, or degrades to plain text when the target is
// not a scheme the destination will render.
func (c *Converter) convertLink(element *storageNode, marks []TipTapMark, depth int) ([]*TipTapNode, error) {
	href := element.Attr("href")
	if href == "" {
		return c.convertInline(element.Children, marks, depth+1)
	}

	safe := SanitizeURL(href)
	if safe == "" {
		c.warn(WarnDangerousURLRemoved,
			"link target used a scheme that is not allowed; the link text was kept without the link")
		return c.convertInline(element.Children, marks, depth+1)
	}

	linked := appendMark(marks, TipTapMark{Type: MarkLink, Attrs: map[string]any{"href": safe}})
	nodes, err := c.convertInline(element.Children, linked, depth+1)
	if err != nil {
		return nil, err
	}
	// A link with no text would vanish, so the target becomes the label.
	if len(nodes) == 0 {
		node, err := c.textNode(safe, linked, depth)
		if err != nil {
			return nil, err
		}
		return []*TipTapNode{node}, nil
	}
	return nodes, nil
}

func (c *Converter) textNode(text string, marks []TipTapMark, depth int) (*TipTapNode, error) {
	node, err := c.node(NodeText, depth)
	if err != nil {
		return nil, err
	}
	node.Text = text
	node.Marks = marks
	return node, nil
}

// appendMark copies before appending: marks accumulate down a shared tree, and
// appending in place would leak a sibling's mark onto its neighbours.
func appendMark(marks []TipTapMark, mark TipTapMark) []TipTapMark {
	out := make([]TipTapMark, 0, len(marks)+1)
	out = append(out, marks...)
	return append(out, mark)
}

func inlineMarkFor(name string) (string, bool) {
	switch name {
	case "strong", "b":
		return MarkBold, true
	case "em", "i":
		return MarkItalic, true
	case "u", "ins":
		return MarkUnderline, true
	case "s", "strike", "del":
		return MarkStrike, true
	case "code", "tt":
		return MarkCode, true
	}
	return "", false
}

func isHeading(name string) bool {
	return len(name) == 2 && name[0] == 'h' && name[1] >= '1' && name[1] <= '6'
}

// isTransparentBlock lists containers whose children are the content.
//
// ac:layout and its sections and cells are how Confluence Cloud wraps every
// body in the discovery sample; TipTap has no multi-column layout node, so the
// columns are flattened into document order.
func isTransparentBlock(name string) bool {
	switch name {
	case "div", "section", "article", "main", "body", "html",
		"layout", "layout-section", "layout-cell",
		"thead", "tbody", "tfoot", "colgroup", "col", "caption":
		return true
	}
	return false
}

// SanitizeURL mirrors the Docs sanitizer: it returns the URL unchanged when its
// scheme is allowed or it is a relative reference, and "" otherwise.
//
// The scheme is read the way a browser would, after stripping the control
// characters and decoding the HTML entities that a dangerous scheme can hide
// behind, so "java&Tab;script:alert(1)" is caught.
func SanitizeURL(url string) string {
	scheme, hasScheme := urlSchemeOf(url)
	if !hasScheme {
		return url
	}
	switch scheme {
	case "http", "https", "mailto", "tel":
		return url
	default:
		return ""
	}
}

var urlStripChars = strings.NewReplacer("\t", "", "\n", "", "\r", "", "\x00", "")

func urlSchemeOf(url string) (string, bool) {
	cleaned := urlStripChars.Replace(url)
	cleaned = html.UnescapeString(cleaned)
	cleaned = urlStripChars.Replace(cleaned)
	cleaned = strings.TrimFunc(cleaned, func(r rune) bool { return r <= ' ' })
	lower := strings.ToLower(cleaned)

	for i, r := range lower {
		if r == ':' {
			if i == 0 {
				return "", false
			}
			return lower[:i], true
		}
		if r == '/' || r == '?' || r == '#' {
			return "", false
		}
		valid := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '+' || r == '-' || r == '.'
		if !valid {
			return "", false
		}
	}
	return "", false
}

// storageNode is one node of the parsed storage-format tree: an element, or a
// text run when Name is zero.
type storageNode struct {
	Name     xml.Name
	Attrs    []xml.Attr
	Text     string
	Children []*storageNode
}

// IsText reports whether the node is a text run.
func (n *storageNode) IsText() bool { return n.Name.Local == "" }

// LocalName is the lowercased element name without its namespace prefix.
func (n *storageNode) LocalName() string { return strings.ToLower(n.Name.Local) }

// IsConfluence reports whether the element is one of Confluence's own.
func (n *storageNode) IsConfluence() bool { return n.Name.Space == storageNamespace }

// Attr reads an attribute by local name, ignoring its namespace, because
// Confluence writes ac:name and a plain name interchangeably.
func (n *storageNode) Attr(name string) string {
	for _, attr := range n.Attrs {
		if strings.EqualFold(attr.Name.Local, name) {
			return attr.Value
		}
	}
	return ""
}

// PlainText is the concatenated text of the subtree.
func (n *storageNode) PlainText() string {
	var b strings.Builder
	n.appendText(&b)
	return b.String()
}

func (n *storageNode) appendText(b *strings.Builder) {
	if n.IsText() {
		b.WriteString(n.Text)
		return
	}
	for _, child := range n.Children {
		child.appendText(b)
	}
}

// storageMaxDepth bounds the parse itself, before any conversion, so a
// pathologically nested body cannot exhaust the stack.
const storageMaxDepth = 500

// storageAutoClose is xml.HTMLAutoClose without "link".
//
// The decoder matches auto-close names on the local name alone, so the stock
// HTML list treats Confluence's own <ac:link> as the HTML void element <link>:
// it closes the tag immediately and then chokes on the real </ac:link>. That
// broke a real body in the discovery sample and would have silently destroyed
// every page and attachment link. No other Confluence element name collides
// with the list.
var storageAutoClose = []string{
	"basefont", "br", "area", "img", "param", "hr",
	"input", "col", "frame", "isindex", "base", "meta",
}

// The storage format is a fragment with several roots, so it is parsed inside a
// synthetic wrapper that is then discarded.
const (
	syntheticRootName  = "mmetl-storage-root"
	syntheticRootOpen  = "<" + syntheticRootName + ">"
	syntheticRootClose = "</" + syntheticRootName + ">"
)

// parseStorageFormat parses a Confluence Storage Format fragment.
//
// The body is a fragment with several roots and undeclared ac: and ri:
// prefixes, so it is wrapped in a synthetic root. HTML entities are resolved
// because the storage format is XHTML-ish and uses &nbsp; and friends, which
// are not XML predefined entities and would otherwise fail the parse.
func parseStorageFormat(storage string) (*storageNode, error) {
	decoder := xml.NewDecoder(strings.NewReader(syntheticRootOpen + storage + syntheticRootClose))
	decoder.Strict = false
	decoder.Entity = xml.HTMLEntity
	decoder.AutoClose = storageAutoClose

	// Consume the wrapper's own start tag, so the tree's top level is the
	// fragment's own nodes rather than a container holding them.
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}

	root := &storageNode{Name: xml.Name{Local: syntheticRootName}}
	stack := []*storageNode{root}

	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}

		switch t := token.(type) {
		case xml.StartElement:
			if len(stack) > storageMaxDepth {
				return nil, fmt.Errorf("storage format nests deeper than %d levels", storageMaxDepth)
			}
			child := &storageNode{Name: t.Name, Attrs: t.Attr}
			parent := stack[len(stack)-1]
			parent.Children = append(parent.Children, child)
			stack = append(stack, child)

		case xml.EndElement:
			if len(stack) > 1 {
				stack = stack[:len(stack)-1]
			}

		case xml.CharData:
			if len(t) == 0 {
				continue
			}
			parent := stack[len(stack)-1]
			parent.Children = append(parent.Children, &storageNode{Text: string(t)})
		}
	}

	return root, nil
}

// Confluence storage-format element names.
const (
	sfStructuredMacro = "structured-macro"
	sfPlaceholder     = "placeholder"
	sfRichTextBody    = "rich-text-body"
	sfPlainTextBody   = "plain-text-body"
	sfParameter       = "parameter"
	sfMacroNameAttr   = "name"

	sfLink                = "link"
	sfImage               = "image"
	sfEmoticon            = "emoticon"
	sfTaskList            = "task-list"
	sfInlineCommentMarker = "inline-comment-marker"
	sfADFExtension        = "adf-extension"
)

// isInlineConfluenceElement lists the Confluence elements that are inline
// content wherever they appear in the document.
func isInlineConfluenceElement(name string) bool {
	switch name {
	case sfLink, sfEmoticon, sfInlineCommentMarker, sfPlaceholder, sfParameter:
		return true
	}
	return false
}

// isBlockElement reports whether an element becomes block-level output, so a
// paragraph containing one can be split around it.
func isBlockElement(node *storageNode) bool {
	if node.IsText() {
		return false
	}
	name := node.LocalName()
	if node.IsConfluence() {
		if isInlineConfluenceElement(name) {
			return false
		}
		switch name {
		case sfStructuredMacro, sfTaskList, sfImage, sfADFExtension, sfRichTextBody, sfPlainTextBody:
			return true
		default:
			return isTransparentBlock(name)
		}
	}
	switch name {
	case "p", "blockquote", "pre", "ul", "ol", "table", "hr":
		return true
	}
	return isHeading(name) || isTransparentBlock(name)
}

func containsBlockElement(children []*storageNode) bool {
	for _, child := range children {
		if isBlockElement(child) {
			return true
		}
	}
	return false
}

// convertADFExtension renders an embedded ADF feature as a marker naming it.
// These are newer Confluence Cloud constructs with no storage-format body to
// recover, so there is nothing to keep but the name.
func (c *Converter) convertADFExtension(element *storageNode, depth int) ([]*TipTapNode, error) {
	name := firstNonEmpty(adfExtensionName(element), "adf-extension")
	c.warn(WarnUnsupportedMacro, fmt.Sprintf(
		"embedded Confluence feature %q has no destination equivalent and was replaced with a marker", name))

	paragraph, err := c.node(NodeParagraph, depth)
	if err != nil {
		return nil, err
	}
	text, err := c.textNode(fmt.Sprintf("[Unsupported Confluence macro: %s]", name), nil, depth+1)
	if err != nil {
		return nil, err
	}
	paragraph.Content = []*TipTapNode{text}
	return []*TipTapNode{paragraph}, nil
}

// convertConfluenceElement converts a Confluence block element.
//
// Only the generic fallback lives here: recognized macros are converted by the
// macro matrix, and everything else becomes a visible marker plus whatever text
// the macro carried, so a reader can see something was there instead of finding
// a hole in the page.
func (c *Converter) convertConfluenceElement(element *storageNode, depth int) ([]*TipTapNode, error) {
	switch element.LocalName() {
	case sfStructuredMacro:
		return c.convertMacro(element, depth)

	case sfTaskList:
		return c.convertTaskList(element, depth)

	case sfImage:
		return c.convertImage(element, nil, depth)

	case sfADFExtension:
		return c.convertADFExtension(element, depth)

	case sfRichTextBody, sfPlainTextBody:
		return c.convertBlocks(element.Children, depth+1)

	case sfParameter:
		// A parameter is macro configuration, not page content. It is read by
		// whichever macro conversion needs it and never emitted on its own.
		return nil, nil

	default:
		return c.convertBlocks(element.Children, depth+1)
	}
}

// convertConfluenceInline converts a Confluence element found in inline
// position.
func (c *Converter) convertConfluenceInline(element *storageNode, marks []TipTapMark, depth int) ([]*TipTapNode, error) {
	switch element.LocalName() {
	case sfLink:
		return c.convertLinkElement(element, marks, depth)

	case sfImage:
		return c.convertImage(element, marks, depth)

	case sfEmoticon:
		return c.convertEmoticon(element, marks, depth)

	case sfInlineCommentMarker:
		// An anchor for a Confluence inline comment. The comment itself is
		// imported separately as a post; the anchor has no destination mapping
		// yet, so its text is kept and the marker dropped.
		return c.convertInline(element.Children, marks, depth+1)

	case sfPlaceholder:
		// Template placeholder text such as "Insert your email here". It is
		// visible in Confluence, so dropping it would lose text a reader sees.
		return c.convertInline(element.Children, marks, depth+1)

	case sfParameter:
		return nil, nil

	default:
		return c.convertInline(element.Children, marks, depth+1)
	}
}

// convertUnsupportedMacro renders a macro this iteration cannot reproduce.
//
// The marker names the macro so the gap is visible in the page rather than only
// in the report, and any text the macro carried is kept after it.
func (c *Converter) convertUnsupportedMacro(element *storageNode, depth int) ([]*TipTapNode, error) {
	name := element.Attr(sfMacroNameAttr)
	if name == "" {
		name = "unknown"
	}
	c.warn(WarnUnsupportedMacro, fmt.Sprintf("Confluence macro %q has no destination equivalent and was replaced with a marker", name))

	marker, err := c.node(NodeParagraph, depth)
	if err != nil {
		return nil, err
	}
	text, err := c.textNode(fmt.Sprintf("[Unsupported Confluence macro: %s]", name), nil, depth+1)
	if err != nil {
		return nil, err
	}
	marker.Content = []*TipTapNode{text}

	nodes := []*TipTapNode{marker}

	// Macro bodies hold real content; parameters hold configuration.
	for _, child := range element.Children {
		if child.IsText() || !child.IsConfluence() {
			continue
		}
		switch child.LocalName() {
		case sfRichTextBody, sfPlainTextBody:
			body, err := c.convertBlocks(child.Children, depth+1)
			if err != nil {
				return nil, err
			}
			nodes = append(nodes, body...)
		}
	}
	return nodes, nil
}

// Placeholder templates. Section 11 allows exactly these three, only in typed
// TipTap attributes, and the importer rewrites nothing else.
const (
	placeholderPageFormat       = "{{CONF_PAGE_ID:%s}}"
	placeholderAttachmentFormat = "{{CONF_ATTACHMENT_ID:%s}}"
	placeholderUserFormat       = "{{CONF_USER_ID:%s}}"
)

// PagePlaceholder, AttachmentPlaceholder and UserPlaceholder build the only
// placeholders the contract allows.
func PagePlaceholder(sourceID string) string {
	return fmt.Sprintf(placeholderPageFormat, sourceID)
}

func AttachmentPlaceholder(sourceID string) string {
	return fmt.Sprintf(placeholderAttachmentFormat, sourceID)
}

func UserPlaceholder(accountID string) string {
	return fmt.Sprintf(placeholderUserFormat, accountID)
}

// mentionLabelMaxRunes bounds the visible label on a mention. It is a
// destination-facing string, not content, and an unbounded display name would
// inflate every page that mentions the same user.
const mentionLabelMaxRunes = 64

// ConversionContext resolves the references a body makes into the source IDs
// the placeholders carry.
//
// Confluence links by title and filename, not by ID. Resolving them here is
// what section 11 requires: the importer only ever sees IDs, so an ambiguous
// title never reaches it as a guess.
type ConversionContext struct {
	SpaceKey string

	// pagesByTitle holds every page sharing a lowercased title, so an ambiguous
	// reference can be recognized rather than silently resolved to the first.
	pagesByTitle map[string][]*Page

	// attachmentsByPageAndName is keyed by page source ID and lowercased
	// filename, with a fallback index by filename alone for references that
	// name no page.
	attachmentsByPageAndName map[string][]*Attachment
	attachmentsByName        map[string][]*Attachment

	usersByAccountID map[string]*User
	usersByKey       map[string]*User
}

// NewConversionContext indexes the selected content for reference resolution.
func NewConversionContext(space Space, pages []*Page, attachments map[string][]*Attachment, users []*User) *ConversionContext {
	ctx := &ConversionContext{
		SpaceKey:                 space.SpaceKey,
		pagesByTitle:             map[string][]*Page{},
		attachmentsByPageAndName: map[string][]*Attachment{},
		attachmentsByName:        map[string][]*Attachment{},
		usersByAccountID:         map[string]*User{},
		usersByKey:               map[string]*User{},
	}

	for _, page := range pages {
		title := strings.ToLower(strings.TrimSpace(page.Title))
		ctx.pagesByTitle[title] = append(ctx.pagesByTitle[title], page)
	}
	for pageID, list := range attachments {
		for _, attachment := range list {
			name := strings.ToLower(strings.TrimSpace(attachment.Filename))
			ctx.attachmentsByPageAndName[pageID+"\x00"+name] = append(ctx.attachmentsByPageAndName[pageID+"\x00"+name], attachment)
			ctx.attachmentsByName[name] = append(ctx.attachmentsByName[name], attachment)
		}
	}
	for _, user := range users {
		ctx.usersByAccountID[user.AccountID] = user
		if user.ConfluenceUserKey != "" {
			ctx.usersByKey[user.ConfluenceUserKey] = user
		}
	}
	return ctx
}

// SetContext installs the reference index. Without one, every reference falls
// back to visible text, which is what a comment converted before its page set
// is known would produce.
func (c *Converter) SetContext(ctx *ConversionContext) { c.context = ctx }

// resolveUser maps an ri:user reference onto an exported user.
func (c *Converter) resolveUser(element *storageNode) (*User, bool) {
	if c.context == nil {
		return nil, false
	}
	if accountID := element.Attr("account-id"); accountID != "" {
		if user, ok := c.context.usersByAccountID[accountID]; ok {
			return user, true
		}
	}
	// ri:userkey is the ConfluenceUserImpl key, which in a Cloud export is
	// usually the account ID as well, so both indexes are tried.
	if key := element.Attr("userkey"); key != "" {
		if user, ok := c.context.usersByKey[key]; ok {
			return user, true
		}
		if user, ok := c.context.usersByAccountID[key]; ok {
			return user, true
		}
	}
	return nil, false
}

// resolvePage maps an ri:page reference onto an exported page.
//
// A reference into another space, or one whose title matches several pages, is
// not resolved: section 11 forbids emitting a title-based placeholder, and
// guessing which page was meant would silently link readers to the wrong one.
func (c *Converter) resolvePage(element *storageNode) (*Page, string) {
	if c.context == nil {
		return nil, "no page index was available"
	}

	title := strings.TrimSpace(element.Attr("content-title"))
	if title == "" {
		return nil, "the reference names no page title"
	}
	if spaceKey := strings.TrimSpace(element.Attr("space-key")); spaceKey != "" &&
		!strings.EqualFold(spaceKey, c.context.SpaceKey) {
		return nil, fmt.Sprintf("the reference points into space %q, which is not being exported", spaceKey)
	}

	matches := c.context.pagesByTitle[strings.ToLower(title)]
	switch len(matches) {
	case 0:
		return nil, fmt.Sprintf("no exported page is titled %q", title)
	case 1:
		return matches[0], ""
	default:
		return nil, fmt.Sprintf("%d exported pages are titled %q", len(matches), title)
	}
}

// resolveAttachment maps an ri:attachment reference onto an exported
// attachment, preferring the page the reference names.
func (c *Converter) resolveAttachment(element *storageNode) (*Attachment, string) {
	if c.context == nil {
		return nil, "no attachment index was available"
	}

	filename := strings.TrimSpace(element.Attr("filename"))
	if filename == "" {
		return nil, "the reference names no filename"
	}
	name := strings.ToLower(filename)

	// A nested ri:page names the attachment's page; without one it belongs to
	// the page carrying the reference.
	for _, child := range element.Children {
		if child.IsText() || child.LocalName() != "page" {
			continue
		}
		page, _ := c.resolvePage(child)
		if page == nil {
			continue
		}
		if matches := c.context.attachmentsByPageAndName[page.SourceID+"\x00"+name]; len(matches) == 1 {
			return matches[0], ""
		}
	}
	if c.sourceID != "" {
		if matches := c.context.attachmentsByPageAndName[c.sourceID+"\x00"+name]; len(matches) == 1 {
			return matches[0], ""
		}
	}

	matches := c.context.attachmentsByName[name]
	switch len(matches) {
	case 0:
		return nil, fmt.Sprintf("no exported attachment is named %q", filename)
	case 1:
		return matches[0], ""
	default:
		return nil, fmt.Sprintf("%d exported attachments are named %q", len(matches), filename)
	}
}

// convertLinkElement converts ac:link, which Confluence uses for page links,
// attachment links, and user mentions alike.
func (c *Converter) convertLinkElement(element *storageNode, marks []TipTapMark, depth int) ([]*TipTapNode, error) {
	target, body := splitLinkChildren(element)
	if target == nil {
		return c.convertInline(body, marks, depth+1)
	}

	switch target.LocalName() {
	case "user":
		return c.convertUserMention(target, depth)
	case "page", "blog-post":
		return c.convertPageLink(target, body, marks, depth)
	case "attachment":
		return c.convertAttachmentLink(target, body, marks, depth)
	case "url":
		return c.convertURLLink(target, body, marks, depth)
	default:
		return c.convertInline(body, marks, depth+1)
	}
}

// splitLinkChildren separates the ri: target from the link's visible body.
func splitLinkChildren(element *storageNode) (target *storageNode, body []*storageNode) {
	for _, child := range element.Children {
		if child.IsText() {
			body = append(body, child)
			continue
		}
		switch child.LocalName() {
		case "user", "page", "blog-post", "attachment", "url", "space", "content-entity":
			if target == nil {
				target = child
			}
		case "plain-text-link-body", "link-body":
			body = append(body, child.Children...)
		default:
			body = append(body, child)
		}
	}
	return target, body
}

// convertUserMention emits a mention node carrying the user placeholder, or
// plain text when the user is not part of this export.
func (c *Converter) convertUserMention(target *storageNode, depth int) ([]*TipTapNode, error) {
	user, ok := c.resolveUser(target)
	if !ok {
		identifier := firstNonEmpty(target.Attr("account-id"), target.Attr("userkey"))
		c.warn(WarnXMLUnknownReferenceClass, fmt.Sprintf(
			"mentioned user %q is not part of this export; the mention was kept as text", identifier))

		node, err := c.textNode("@"+identifier, nil, depth)
		if err != nil {
			return nil, err
		}
		return []*TipTapNode{node}, nil
	}

	node, err := c.node(NodeMention, depth)
	if err != nil {
		return nil, err
	}
	node.Attrs = map[string]any{
		"id":    UserPlaceholder(user.AccountID),
		"label": truncateRunes(firstNonEmpty(user.DisplayName, user.MattermostUsername), mentionLabelMaxRunes),
	}
	return []*TipTapNode{node}, nil
}

func (c *Converter) convertPageLink(target *storageNode, body []*storageNode, marks []TipTapMark, depth int) ([]*TipTapNode, error) {
	label := strings.TrimSpace(target.Attr("content-title"))

	page, reason := c.resolvePage(target)
	if page == nil {
		c.warn(WarnXMLUnknownReferenceClass, fmt.Sprintf(
			"page link could not be resolved (%s); the link text was kept without the link", reason))
		return c.linkFallbackText(body, label, marks, depth)
	}
	return c.linkedBody(body, firstNonEmpty(label, page.Title), PagePlaceholder(page.SourceID), marks, depth)
}

func (c *Converter) convertAttachmentLink(target *storageNode, body []*storageNode, marks []TipTapMark, depth int) ([]*TipTapNode, error) {
	filename := strings.TrimSpace(target.Attr("filename"))

	attachment, reason := c.resolveAttachment(target)
	if attachment == nil {
		c.warn(WarnXMLUnknownReferenceClass, fmt.Sprintf(
			"attachment link could not be resolved (%s); the filename was kept as text", reason))
		return c.linkFallbackText(body, filename, marks, depth)
	}
	return c.linkedBody(body, firstNonEmpty(filename, attachment.Filename),
		AttachmentPlaceholder(attachment.SourceID), marks, depth)
}

func (c *Converter) convertURLLink(target *storageNode, body []*storageNode, marks []TipTapMark, depth int) ([]*TipTapNode, error) {
	raw := strings.TrimSpace(target.Attr("value"))
	safe := SanitizeURL(raw)
	if safe == "" {
		c.warn(WarnDangerousURLRemoved,
			"link target used a scheme that is not allowed; the link text was kept without the link")
		return c.linkFallbackText(body, raw, marks, depth)
	}
	return c.linkedBody(body, safe, safe, marks, depth)
}

// linkedBody renders a link's visible content under a link mark, falling back
// to the label when the source gave the link no text of its own.
func (c *Converter) linkedBody(body []*storageNode, label, href string, marks []TipTapMark, depth int) ([]*TipTapNode, error) {
	linked := appendMark(marks, TipTapMark{Type: MarkLink, Attrs: map[string]any{"href": href}})

	nodes, err := c.convertInline(body, linked, depth+1)
	if err != nil {
		return nil, err
	}
	if len(nodes) > 0 {
		return nodes, nil
	}

	node, err := c.textNode(firstNonEmpty(label, href), linked, depth)
	if err != nil {
		return nil, err
	}
	return []*TipTapNode{node}, nil
}

// linkFallbackText keeps an unresolved link's text visible, without the
// executable attribute section 11 forbids emitting unresolved.
func (c *Converter) linkFallbackText(body []*storageNode, label string, marks []TipTapMark, depth int) ([]*TipTapNode, error) {
	nodes, err := c.convertInline(body, marks, depth+1)
	if err != nil {
		return nil, err
	}
	if len(nodes) > 0 {
		return nodes, nil
	}
	if label == "" {
		return nil, nil
	}

	node, err := c.textNode(label, marks, depth)
	if err != nil {
		return nil, err
	}
	return []*TipTapNode{node}, nil
}

// convertImage converts ac:image, whose target is either an attachment in this
// export or an external URL.
func (c *Converter) convertImage(element *storageNode, marks []TipTapMark, depth int) ([]*TipTapNode, error) {
	alt := strings.TrimSpace(element.Attr("alt"))

	var target *storageNode
	for _, child := range element.Children {
		if !child.IsText() {
			target = child
			break
		}
	}
	if target == nil {
		return nil, nil
	}

	var src, label string
	switch target.LocalName() {
	case "attachment":
		attachment, reason := c.resolveAttachment(target)
		if attachment == nil {
			c.warn(WarnXMLUnknownReferenceClass, fmt.Sprintf(
				"image attachment could not be resolved (%s); a text link was kept in its place", reason))
			return c.imageFallbackText(firstNonEmpty(alt, target.Attr("filename")), marks, depth)
		}
		src = AttachmentPlaceholder(attachment.SourceID)
		label = firstNonEmpty(alt, attachment.Filename)

	case "url":
		raw := strings.TrimSpace(target.Attr("value"))
		src = SanitizeURL(raw)
		if src == "" {
			c.warn(WarnDangerousURLRemoved, "image source used a scheme that is not allowed; the image was dropped")
			return c.imageFallbackText(alt, marks, depth)
		}
		label = firstNonEmpty(alt, raw)

	default:
		return c.imageFallbackText(alt, marks, depth)
	}

	node, err := c.node(NodeImage, depth)
	if err != nil {
		return nil, err
	}
	node.Attrs = map[string]any{"src": src}
	if label != "" {
		node.Attrs["alt"] = label
	}
	return []*TipTapNode{node}, nil
}

func (c *Converter) imageFallbackText(label string, marks []TipTapMark, depth int) ([]*TipTapNode, error) {
	if label == "" {
		return nil, nil
	}
	node, err := c.textNode(label, marks, depth)
	if err != nil {
		return nil, err
	}
	return []*TipTapNode{node}, nil
}

// convertEmoticon renders a Confluence emoji as text. The destination schema
// has no emoji node, and the export carries the character itself.
func (c *Converter) convertEmoticon(element *storageNode, marks []TipTapMark, depth int) ([]*TipTapNode, error) {
	text := firstNonEmpty(
		element.Attr("emoji-fallback"),
		element.Attr("emoji-shortname"),
		element.Attr("name"),
	)
	if text == "" {
		return nil, nil
	}

	node, err := c.textNode(text, marks, depth)
	if err != nil {
		return nil, err
	}
	return []*TipTapNode{node}, nil
}

// convertTaskList converts ac:task-list into a TipTap task list, preserving
// which items were ticked.
func (c *Converter) convertTaskList(element *storageNode, depth int) ([]*TipTapNode, error) {
	list, err := c.node(NodeTaskList, depth)
	if err != nil {
		return nil, err
	}

	for _, child := range element.Children {
		if child.IsText() || child.LocalName() != "task" {
			continue
		}

		item, err := c.node(NodeTaskItem, depth+1)
		if err != nil {
			return nil, err
		}
		item.Attrs = map[string]any{"checked": taskIsComplete(child)}

		for _, part := range child.Children {
			if part.IsText() || part.LocalName() != "task-body" {
				continue
			}
			body, err := c.convertBlocks(part.Children, depth+2)
			if err != nil {
				return nil, err
			}
			item.Content = append(item.Content, body...)
		}
		if len(item.Content) == 0 {
			empty, err := c.node(NodeParagraph, depth+2)
			if err != nil {
				return nil, err
			}
			item.Content = []*TipTapNode{empty}
		}
		list.Content = append(list.Content, item)
	}

	if len(list.Content) == 0 {
		return nil, nil
	}
	return []*TipTapNode{list}, nil
}

func taskIsComplete(task *storageNode) bool {
	for _, child := range task.Children {
		if !child.IsText() && child.LocalName() == "task-status" {
			return strings.EqualFold(strings.TrimSpace(child.PlainText()), "complete")
		}
	}
	return false
}

// macroCalloutTypes maps the Confluence admonition macros onto the destination's
// callout types. "panel" is Confluence's generic callout and is included: the
// section 10 matrix is a minimum, and rendering a panel as an unsupported-macro
// marker above its own body would be visibly worse than a callout.
var macroCalloutTypes = map[string]string{
	"info":    "info",
	"note":    "note",
	"warning": "warning",
	"tip":     "success",
	"panel":   "info",
}

// convertMacro converts a structured macro, falling back to the visible marker
// for anything with no destination equivalent.
func (c *Converter) convertMacro(element *storageNode, depth int) ([]*TipTapNode, error) {
	name := strings.ToLower(strings.TrimSpace(element.Attr(sfMacroNameAttr)))

	switch name {
	case "code":
		return c.convertCodeMacro(element, depth)
	case "children", "pagetree":
		return c.convertChildrenMacro(element, depth)
	case "jira":
		return c.convertJiraMacro(element, depth)
	case "status":
		return c.convertStatusMacro(element, depth)
	case "expand":
		return c.convertMacroBody(element, depth)
	}

	if calloutType, ok := macroCalloutTypes[name]; ok {
		return c.convertCalloutMacro(element, calloutType, depth)
	}
	return c.convertUnsupportedMacro(element, depth)
}

// convertCodeMacro renders a code macro as a code block, keeping the language
// when the macro names one.
func (c *Converter) convertCodeMacro(element *storageNode, depth int) ([]*TipTapNode, error) {
	node, err := c.node(NodeCodeBlock, depth)
	if err != nil {
		return nil, err
	}
	if language := macroParameter(element, "language"); language != "" {
		node.Attrs = map[string]any{"language": language}
	}

	text := macroBodyText(element)
	if text == "" {
		return nil, nil
	}
	textNode, err := c.textNode(text, nil, depth+1)
	if err != nil {
		return nil, err
	}
	node.Content = []*TipTapNode{textNode}
	return []*TipTapNode{node}, nil
}

func (c *Converter) convertCalloutMacro(element *storageNode, calloutType string, depth int) ([]*TipTapNode, error) {
	node, err := c.node(NodeCallout, depth)
	if err != nil {
		return nil, err
	}
	node.Attrs = map[string]any{"type": calloutType}

	body, err := c.convertMacroBody(element, depth+1)
	if err != nil {
		return nil, err
	}
	if len(body) == 0 {
		// A callout must hold at least one block.
		empty, err := c.node(NodeParagraph, depth+1)
		if err != nil {
			return nil, err
		}
		body = []*TipTapNode{empty}
	}
	node.Content = body
	return []*TipTapNode{node}, nil
}

// convertChildrenMacro replaces a dynamic child listing with the links it would
// have rendered at export time.
//
// The list is a snapshot: the macro is live in Confluence and the destination
// has no equivalent, so a frozen list of real links is closer to what the
// reader saw than a marker would be.
func (c *Converter) convertChildrenMacro(element *storageNode, depth int) ([]*TipTapNode, error) {
	page := c.currentPage()
	if page == nil || len(page.Children) == 0 {
		c.warn(WarnUnsupportedMacro,
			"a child-pages macro listed pages dynamically; it has no destination equivalent and no children were available to list")
		return c.convertUnsupportedMacro(element, depth)
	}

	list, err := c.node(NodeBulletList, depth)
	if err != nil {
		return nil, err
	}
	for _, child := range page.Children {
		item, err := c.node(NodeListItem, depth+1)
		if err != nil {
			return nil, err
		}
		paragraph, err := c.node(NodeParagraph, depth+2)
		if err != nil {
			return nil, err
		}
		link, err := c.textNode(child.Title, []TipTapMark{{
			Type:  MarkLink,
			Attrs: map[string]any{"href": PagePlaceholder(child.SourceID)},
		}}, depth+3)
		if err != nil {
			return nil, err
		}
		paragraph.Content = []*TipTapNode{link}
		item.Content = []*TipTapNode{paragraph}
		list.Content = append(list.Content, item)
	}
	return []*TipTapNode{list}, nil
}

// convertJiraMacro renders a Jira issue reference as a link when the macro
// carries a server URL, and as the bare key otherwise.
func (c *Converter) convertJiraMacro(element *storageNode, depth int) ([]*TipTapNode, error) {
	key := firstNonEmpty(macroParameter(element, "key"), macroParameter(element, "jqlQuery"))
	if key == "" {
		c.warn(WarnUnsupportedMacro, "a Jira macro named no issue key and was replaced with a marker")
		return c.convertUnsupportedMacro(element, depth)
	}

	paragraph, err := c.node(NodeParagraph, depth)
	if err != nil {
		return nil, err
	}

	var marks []TipTapMark
	if server := SanitizeURL(macroParameter(element, "serverId-url")); server != "" {
		marks = []TipTapMark{{
			Type:  MarkLink,
			Attrs: map[string]any{"href": strings.TrimRight(server, "/") + "/browse/" + key},
		}}
	}

	text, err := c.textNode(key, marks, depth+1)
	if err != nil {
		return nil, err
	}
	paragraph.Content = []*TipTapNode{text}
	return []*TipTapNode{paragraph}, nil
}

// convertStatusMacro renders a status lozenge as text. The destination has no
// status node, and the label is the part a reader needs.
func (c *Converter) convertStatusMacro(element *storageNode, depth int) ([]*TipTapNode, error) {
	title := macroParameter(element, "title")
	if title == "" {
		return nil, nil
	}

	paragraph, err := c.node(NodeParagraph, depth)
	if err != nil {
		return nil, err
	}
	text, err := c.textNode("["+title+"]", nil, depth+1)
	if err != nil {
		return nil, err
	}
	paragraph.Content = []*TipTapNode{text}
	return []*TipTapNode{paragraph}, nil
}

// convertMacroBody converts only a macro's body, ignoring its parameters.
func (c *Converter) convertMacroBody(element *storageNode, depth int) ([]*TipTapNode, error) {
	var nodes []*TipTapNode
	for _, child := range element.Children {
		if child.IsText() || !child.IsConfluence() {
			continue
		}
		switch child.LocalName() {
		case sfRichTextBody:
			body, err := c.convertBlocks(child.Children, depth+1)
			if err != nil {
				return nil, err
			}
			nodes = append(nodes, body...)
		case sfPlainTextBody:
			text := strings.TrimSpace(child.PlainText())
			if text == "" {
				continue
			}
			paragraph, err := c.node(NodeParagraph, depth)
			if err != nil {
				return nil, err
			}
			textNode, err := c.textNode(text, nil, depth+1)
			if err != nil {
				return nil, err
			}
			paragraph.Content = []*TipTapNode{textNode}
			nodes = append(nodes, paragraph)
		}
	}
	return nodes, nil
}

// macroParameter reads one ac:parameter by name.
func macroParameter(element *storageNode, name string) string {
	for _, child := range element.Children {
		if child.IsText() || child.LocalName() != sfParameter {
			continue
		}
		if strings.EqualFold(child.Attr(sfMacroNameAttr), name) {
			return strings.TrimSpace(child.PlainText())
		}
	}
	return ""
}

// macroBodyText is the plain text of a macro's body, ignoring its parameters.
func macroBodyText(element *storageNode) string {
	var b strings.Builder
	for _, child := range element.Children {
		if child.IsText() || !child.IsConfluence() {
			continue
		}
		switch child.LocalName() {
		case sfRichTextBody, sfPlainTextBody:
			b.WriteString(child.PlainText())
		}
	}
	return b.String()
}

// adfExtensionName names an ADF extension by its node type, so the marker says
// which feature was there.
func adfExtensionName(element *storageNode) string {
	for _, child := range element.Children {
		if child.IsText() {
			continue
		}
		if child.LocalName() == "adf-node" {
			if nodeType := child.Attr("type"); nodeType != "" {
				return nodeType
			}
		}
		if name := adfExtensionName(child); name != "" {
			return name
		}
	}
	return ""
}

func (c *Converter) currentPage() *Page {
	if c.context == nil {
		return nil
	}
	for _, pages := range c.context.pagesByTitle {
		for _, page := range pages {
			if page.SourceID == c.sourceID {
				return page
			}
		}
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func truncateRunes(s string, limit int) string {
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit])
}
