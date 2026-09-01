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

	case element.IsConfluence():
		nodes, err = c.convertConfluenceElement(element, depth)
		return nodes, true, err

	case name == "p":
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
)

// convertConfluenceElement converts a Confluence block element.
//
// Only the generic fallback lives here: recognized macros are converted by the
// macro matrix, and everything else becomes a visible marker plus whatever text
// the macro carried, so a reader can see something was there instead of finding
// a hole in the page.
func (c *Converter) convertConfluenceElement(element *storageNode, depth int) ([]*TipTapNode, error) {
	switch element.LocalName() {
	case sfStructuredMacro:
		return c.convertUnsupportedMacro(element, depth)

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
