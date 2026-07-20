// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package confluence

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

// TipTap node types
const (
	NodeTypeDoc            = "doc"
	NodeTypeParagraph      = "paragraph"
	NodeTypeText           = "text"
	NodeTypeHeading        = "heading"
	NodeTypeBulletList     = "bulletList"
	NodeTypeOrderedList    = "orderedList"
	NodeTypeListItem       = "listItem"
	NodeTypeCodeBlock      = "codeBlock"
	NodeTypeBlockquote     = "blockquote"
	NodeTypeHardBreak      = "hardBreak"
	NodeTypeHorizontalRule = "horizontalRule"
	NodeTypeImage          = "image"
	NodeTypeTable          = "table"
	NodeTypeTableRow       = "tableRow"
	NodeTypeTableCell      = "tableCell"
	NodeTypeTableHeader    = "tableHeader"
)

// TipTap mark types
const (
	MarkTypeBold          = "bold"
	MarkTypeItalic        = "italic"
	MarkTypeStrike        = "strike"
	MarkTypeCode          = "code"
	MarkTypeLink          = "link"
	MarkTypeUnderline     = "underline"
	MarkTypeCommentAnchor = "commentAnchor"
)

// TipTapNode represents a node in the TipTap document structure.
type TipTapNode struct {
	Type    string         `json:"type"`
	Attrs   map[string]any `json:"attrs,omitempty"`
	Content []TipTapNode   `json:"content,omitempty"`
	Marks   []TipTapMark   `json:"marks,omitempty"`
	Text    string         `json:"text,omitempty"`
}

// TipTapMark represents a mark (formatting) in TipTap.
type TipTapMark struct {
	Type  string         `json:"type"`
	Attrs map[string]any `json:"attrs,omitempty"`
}

// ChildPageInfo contains information about a child page for generating navigation links.
type ChildPageInfo struct {
	ID    string // Confluence page ID
	Title string // Page title
}

// ConvertHTMLToTipTap converts Confluence HTML content to TipTap JSON.
func ConvertHTMLToTipTap(htmlContent string) (string, error) {
	return ConvertHTMLToTipTapWithChildren(htmlContent, nil)
}

// ConvertHTMLToTipTapWithChildren converts Confluence HTML content to TipTap JSON,
// with optional children info for generating child page links in children/pagetree macros.
func ConvertHTMLToTipTapWithChildren(htmlContent string, children []ChildPageInfo) (string, error) {
	// Pre-process Confluence-specific macros
	htmlContent = preprocessConfluenceMacros(htmlContent)

	// Parse HTML
	doc, err := html.Parse(strings.NewReader(htmlContent))
	if err != nil {
		return "", err
	}

	// Pre-scan for headings (needed for ToC generation)
	headings := scanHeadings(doc)

	// Convert to TipTap
	converter := &tiptapConverter{
		children: children,
		headings: headings,
	}
	content := converter.convertNode(doc)

	// Wrap in doc node
	tipTapDoc := TipTapNode{
		Type:    NodeTypeDoc,
		Content: flattenContent(content),
	}

	// Marshal to JSON
	jsonBytes, err := json.Marshal(tipTapDoc)
	if err != nil {
		return "", err
	}

	return string(jsonBytes), nil
}

// HeadingInfo stores information about a heading for ToC generation.
type HeadingInfo struct {
	Level int
	Text  string
	ID    string // Anchor ID for linking
}

type tiptapConverter struct {
	currentMarks []TipTapMark
	children     []ChildPageInfo // Child pages for generating navigation links
	headings     []HeadingInfo   // Headings for ToC generation
	headingIndex int             // Current heading index for ID assignment
}

func (c *tiptapConverter) convertNode(n *html.Node) []TipTapNode {
	var result []TipTapNode

	switch n.Type {
	case html.TextNode:
		text := n.Data

		// For preformatted elements, preserve all whitespace exactly
		if n.Parent != nil && isPreformattedElement(n.Parent) {
			if text != "" {
				node := TipTapNode{
					Type: NodeTypeText,
					Text: text,
				}
				if len(c.currentMarks) > 0 {
					node.Marks = make([]TipTapMark, len(c.currentMarks))
					copy(node.Marks, c.currentMarks)
				}
				result = append(result, node)
			}
		} else {
			// For normal text, collapse whitespace but preserve word boundaries
			// Skip nodes that are purely structural whitespace (only newlines/tabs between block elements)
			if strings.TrimSpace(text) == "" {
				// Pure whitespace - skip if it's just structural (newlines between elements)
				// But preserve a single space if we're inside inline content
				if n.Parent != nil && isInlineElement(n.Parent) && strings.Contains(text, " ") {
					// Keep a single space for inline contexts
					text = " "
				} else {
					break
				}
			} else {
				// Has actual content - normalize internal whitespace but preserve leading/trailing spaces
				// This maintains word boundaries when text is adjacent to formatted elements
				text = normalizeWhitespace(text)
			}

			if text != "" {
				node := TipTapNode{
					Type: NodeTypeText,
					Text: text,
				}
				if len(c.currentMarks) > 0 {
					node.Marks = make([]TipTapMark, len(c.currentMarks))
					copy(node.Marks, c.currentMarks)
				}
				result = append(result, node)
			}
		}

	case html.ElementNode:
		result = c.convertElement(n)

	case html.DocumentNode:
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			result = append(result, c.convertNode(child)...)
		}
	}

	return result
}

func (c *tiptapConverter) convertElement(n *html.Node) []TipTapNode {
	var result []TipTapNode

	switch strings.ToLower(n.Data) {
	// Block elements
	case "p", "div":
		content := c.convertChildren(n)
		if len(content) > 0 {
			result = append(result, TipTapNode{
				Type:    NodeTypeParagraph,
				Content: content,
			})
		}

	case "h1", "h2", "h3", "h4", "h5", "h6":
		level := int(n.Data[1] - '0')
		content := c.convertChildren(n)
		attrs := map[string]any{"level": level}
		// Add anchor ID from pre-scanned headings for ToC linking
		if c.headingIndex < len(c.headings) {
			attrs["id"] = c.headings[c.headingIndex].ID
			c.headingIndex++
		}
		result = append(result, TipTapNode{
			Type:    NodeTypeHeading,
			Attrs:   attrs,
			Content: content,
		})

	case "ul":
		items := c.convertListItems(n)
		result = append(result, TipTapNode{
			Type:    NodeTypeBulletList,
			Content: items,
		})

	case "ol":
		items := c.convertListItems(n)
		attrs := map[string]any{"start": 1}
		if start := getAttr(n, "start"); start != "" {
			attrs["start"] = start
		}
		result = append(result, TipTapNode{
			Type:    NodeTypeOrderedList,
			Attrs:   attrs,
			Content: items,
		})

	case "li":
		content := c.convertChildren(n)
		// Wrap inline content in paragraph
		if len(content) > 0 && content[0].Type == NodeTypeText {
			content = []TipTapNode{{
				Type:    NodeTypeParagraph,
				Content: content,
			}}
		}
		result = append(result, TipTapNode{
			Type:    NodeTypeListItem,
			Content: content,
		})

	case "pre":
		// <pre> always creates a code block
		text := extractText(n)
		language := getAttr(n, "data-language")
		if language == "" {
			language = getAttr(n, "class") // Often contains language hint
		}
		attrs := map[string]any{}
		if language != "" {
			attrs["language"] = extractLanguageFromClass(language)
		}
		// Only create text node if content is not empty.
		// Empty text nodes with omitempty create invalid {"type": "text"} without text property.
		var preContent []TipTapNode
		if text != "" {
			preContent = []TipTapNode{{
				Type: NodeTypeText,
				Text: text,
			}}
		}
		result = append(result, TipTapNode{
			Type:    NodeTypeCodeBlock,
			Attrs:   attrs,
			Content: preContent,
		})

	case "code":
		// <code> inside <pre> is already handled by the <pre> case via extractText
		// <code> standalone is inline code (like <tt>, <samp>, <kbd>)
		if n.Parent != nil && strings.ToLower(n.Parent.Data) == "pre" {
			// Skip - parent <pre> handles this
		} else {
			// Inline code - use code mark
			c.pushMark(TipTapMark{Type: MarkTypeCode})
			result = append(result, c.convertChildren(n)...)
			c.popMark()
		}

	case "blockquote":
		content := c.convertChildren(n)
		result = append(result, TipTapNode{
			Type:    NodeTypeBlockquote,
			Content: wrapInParagraphIfNeeded(content),
		})

	case "hr":
		result = append(result, TipTapNode{Type: NodeTypeHorizontalRule})

	case "br":
		result = append(result, TipTapNode{Type: NodeTypeHardBreak})

	case "img":
		src := getAttr(n, "src")
		alt := getAttr(n, "alt")
		if src != "" {
			result = append(result, TipTapNode{
				Type: NodeTypeImage,
				Attrs: map[string]any{
					"src": src,
					"alt": alt,
				},
			})
		}

	case "table":
		rows := c.convertTableRows(n)
		result = append(result, TipTapNode{
			Type:    NodeTypeTable,
			Content: rows,
		})

	case "a":
		href := getAttr(n, "href")
		c.pushMark(TipTapMark{
			Type:  MarkTypeLink,
			Attrs: map[string]any{"href": href},
		})
		result = append(result, c.convertChildren(n)...)
		c.popMark()

	// Inline formatting
	case "strong", "b":
		c.pushMark(TipTapMark{Type: MarkTypeBold})
		result = append(result, c.convertChildren(n)...)
		c.popMark()

	case "em", "i":
		c.pushMark(TipTapMark{Type: MarkTypeItalic})
		result = append(result, c.convertChildren(n)...)
		c.popMark()

	case "u":
		c.pushMark(TipTapMark{Type: MarkTypeUnderline})
		result = append(result, c.convertChildren(n)...)
		c.popMark()

	case "s", "strike", "del":
		c.pushMark(TipTapMark{Type: MarkTypeStrike})
		result = append(result, c.convertChildren(n)...)
		c.popMark()

	case "tt", "samp", "kbd":
		c.pushMark(TipTapMark{Type: MarkTypeCode})
		result = append(result, c.convertChildren(n)...)
		c.popMark()

	// Confluence-specific elements (handled after preprocessing)
	case "ac:structured-macro":
		result = append(result, c.convertConfluenceMacro(n)...)

	case "ac:inline-comment-marker":
		// Inline comment marker - convert to commentAnchor mark
		// The ac:ref attribute contains the UUID that links this highlight to the comment
		anchorID := getAttr(n, "ac:ref")
		if anchorID != "" {
			c.pushMark(TipTapMark{
				Type:  MarkTypeCommentAnchor,
				Attrs: map[string]any{"anchorId": anchorID},
			})
			result = append(result, c.convertChildren(n)...)
			c.popMark()
		} else {
			// No anchor ID - just process children without mark
			result = append(result, c.convertChildren(n)...)
		}

	// Containers to pass through
	case "html", "body", "head", "span", "font":
		result = append(result, c.convertChildren(n)...)

	// Confluence parameter elements - skip (they contain macro configuration, not content)
	// Without this, unknown macros like "children" or "contentbylabel" would have their
	// parameter values (like "true", "1") extracted as text content
	case "ac:parameter":
		// Skip - don't extract text from parameter elements

	default:
		// Unknown element - just process children
		result = append(result, c.convertChildren(n)...)
	}

	return result
}

func (c *tiptapConverter) convertChildren(n *html.Node) []TipTapNode {
	var result []TipTapNode
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		result = append(result, c.convertNode(child)...)
	}
	return result
}

func (c *tiptapConverter) convertListItems(n *html.Node) []TipTapNode {
	var items []TipTapNode
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		if child.Type == html.ElementNode && strings.ToLower(child.Data) == "li" {
			items = append(items, c.convertElement(child)...)
		}
	}
	return items
}

func (c *tiptapConverter) convertTableRows(n *html.Node) []TipTapNode {
	var rows []TipTapNode

	var processNode func(*html.Node)
	processNode = func(node *html.Node) {
		if node.Type == html.ElementNode {
			switch strings.ToLower(node.Data) {
			case "tr":
				cells := c.convertTableCells(node)
				rows = append(rows, TipTapNode{
					Type:    NodeTypeTableRow,
					Content: cells,
				})
			case "thead", "tbody", "tfoot":
				for child := node.FirstChild; child != nil; child = child.NextSibling {
					processNode(child)
				}
			}
		}
	}

	for child := n.FirstChild; child != nil; child = child.NextSibling {
		processNode(child)
	}

	return rows
}

func (c *tiptapConverter) convertTableCells(tr *html.Node) []TipTapNode {
	var cells []TipTapNode
	for child := tr.FirstChild; child != nil; child = child.NextSibling {
		if child.Type == html.ElementNode {
			tag := strings.ToLower(child.Data)
			if tag == "td" || tag == "th" {
				nodeType := NodeTypeTableCell
				if tag == "th" {
					nodeType = NodeTypeTableHeader
				}

				content := c.convertChildren(child)
				attrs := map[string]any{}

				if colspan := getAttr(child, "colspan"); colspan != "" {
					attrs["colspan"] = colspan
				}
				if rowspan := getAttr(child, "rowspan"); rowspan != "" {
					attrs["rowspan"] = rowspan
				}

				cells = append(cells, TipTapNode{
					Type:    nodeType,
					Attrs:   attrs,
					Content: wrapInParagraphIfNeeded(content),
				})
			}
		}
	}
	return cells
}

func (c *tiptapConverter) convertConfluenceMacro(n *html.Node) []TipTapNode {
	macroName := getAttr(n, "ac:name")

	switch macroName {
	case "code":
		// Code macro - extract language and content
		language := ""
		content := ""
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			if child.Type == html.ElementNode {
				if strings.ToLower(child.Data) == "ac:parameter" && getAttr(child, "ac:name") == "language" {
					language = extractText(child)
				}
				if strings.ToLower(child.Data) == "ac:plain-text-body" {
					content = extractText(child)
				}
			}
		}
		attrs := map[string]any{}
		if language != "" {
			attrs["language"] = language
		}
		// Only create text node if content is not empty.
		// Empty text nodes with omitempty create invalid {"type": "text"} without text property.
		var codeContent []TipTapNode
		if content != "" {
			codeContent = []TipTapNode{{
				Type: NodeTypeText,
				Text: content,
			}}
		}
		return []TipTapNode{{
			Type:    NodeTypeCodeBlock,
			Attrs:   attrs,
			Content: codeContent,
		}}

	case "info", "note", "warning", "tip":
		// Panel macros - convert to blockquote with marker
		content := c.convertChildren(n)
		return []TipTapNode{{
			Type:    NodeTypeBlockquote,
			Content: wrapInParagraphIfNeeded(content),
		}}

	case "expand":
		// Expand macro - just include the content
		return c.convertChildren(n)

	case "toc":
		// Table of contents - generate static list from collected headings
		if len(c.headings) > 0 {
			return c.generateToCList()
		}
		return nil

	case "jira":
		// JIRA macro - extract the issue key only
		// Structure: <ac:structured-macro ac:name="jira">
		//   <ac:parameter ac:name="key">MM-50469</ac:parameter>
		//   <ac:parameter ac:name="server">System JIRA</ac:parameter>
		//   <ac:parameter ac:name="serverId">UUID</ac:parameter>
		// </ac:structured-macro>
		jiraKey := ""
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			if child.Type == html.ElementNode && strings.ToLower(child.Data) == "ac:parameter" {
				paramName := getAttr(child, "ac:name")
				if paramName == "key" {
					jiraKey = extractText(child)
					break
				}
			}
		}
		if jiraKey != "" {
			return []TipTapNode{{
				Type: NodeTypeText,
				Text: jiraKey,
			}}
		}
		return nil

	case "children", "pagetree", "pagetreesearch":
		// Dynamic navigation macros - these display child pages at runtime
		// If we have children info, generate a bullet list with links
		if len(c.children) > 0 {
			return c.generateChildPageList()
		}
		// No children info available - generate placeholder
		return []TipTapNode{{
			Type: NodeTypeParagraph,
			Content: []TipTapNode{{
				Type:  NodeTypeText,
				Text:  "[This section displayed a list of child pages in Confluence]",
				Marks: []TipTapMark{{Type: MarkTypeItalic}},
			}},
		}}

	case "contentbylabel":
		// Content by label macro - dynamically lists pages with specific labels
		return []TipTapNode{{
			Type: NodeTypeParagraph,
			Content: []TipTapNode{{
				Type:  NodeTypeText,
				Text:  "[This section displayed pages by label in Confluence]",
				Marks: []TipTapMark{{Type: MarkTypeItalic}},
			}},
		}}

	case "recently-updated":
		// Recently updated macro - dynamically shows recent changes
		return []TipTapNode{{
			Type: NodeTypeParagraph,
			Content: []TipTapNode{{
				Type:  NodeTypeText,
				Text:  "[This section displayed recently updated content in Confluence]",
				Marks: []TipTapMark{{Type: MarkTypeItalic}},
			}},
		}}

	default:
		// Unknown macro - try to extract content
		return c.convertChildren(n)
	}
}

func (c *tiptapConverter) pushMark(mark TipTapMark) {
	c.currentMarks = append(c.currentMarks, mark)
}

func (c *tiptapConverter) popMark() {
	if len(c.currentMarks) > 0 {
		c.currentMarks = c.currentMarks[:len(c.currentMarks)-1]
	}
}

// scanHeadings extracts all headings from the parsed HTML document.
// Used for generating static Table of Contents.
func scanHeadings(doc *html.Node) []HeadingInfo {
	var headings []HeadingInfo
	usedIDs := make(map[string]int) // Track used IDs for uniqueness

	var scan func(*html.Node)
	scan = func(n *html.Node) {
		if n.Type == html.ElementNode {
			tag := strings.ToLower(n.Data)
			if len(tag) == 2 && tag[0] == 'h' && tag[1] >= '1' && tag[1] <= '6' {
				level := int(tag[1] - '0')
				text := extractText(n)
				text = strings.TrimSpace(text)
				if text != "" {
					// Generate unique anchor ID
					id := slugify(text)
					if count, exists := usedIDs[id]; exists {
						usedIDs[id] = count + 1
						id = fmt.Sprintf("%s-%d", id, count+1)
					} else {
						usedIDs[id] = 1
					}
					headings = append(headings, HeadingInfo{
						Level: level,
						Text:  text,
						ID:    id,
					})
				}
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			scan(child)
		}
	}
	scan(doc)

	return headings
}

// slugify converts text to a URL-friendly anchor ID.
func slugify(text string) string {
	// Convert to lowercase
	result := strings.ToLower(text)
	// Replace spaces with hyphens
	result = strings.ReplaceAll(result, " ", "-")
	// Remove non-alphanumeric characters except hyphens
	var cleaned strings.Builder
	for _, r := range result {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			cleaned.WriteRune(r)
		}
	}
	result = cleaned.String()
	// Collapse multiple hyphens
	for strings.Contains(result, "--") {
		result = strings.ReplaceAll(result, "--", "-")
	}
	// Trim leading/trailing hyphens
	result = strings.Trim(result, "-")
	if result == "" {
		result = "heading"
	}
	return result
}

// generateToCList creates a bullet list from collected headings for Table of Contents.
// Each item is a link that navigates to the corresponding heading anchor.
func (c *tiptapConverter) generateToCList() []TipTapNode {
	if len(c.headings) == 0 {
		return nil
	}

	// Create list items for each heading as anchor links
	var listItems []TipTapNode
	for _, heading := range c.headings {
		listItem := TipTapNode{
			Type: NodeTypeListItem,
			Content: []TipTapNode{{
				Type: NodeTypeParagraph,
				Content: []TipTapNode{{
					Type: NodeTypeText,
					Text: heading.Text,
					Marks: []TipTapMark{{
						Type: MarkTypeLink,
						Attrs: map[string]any{
							"href": "#" + heading.ID,
						},
					}},
				}},
			}},
		}
		listItems = append(listItems, listItem)
	}

	// Wrap in a bullet list
	return []TipTapNode{{
		Type:    NodeTypeBulletList,
		Content: listItems,
	}}
}

// generateChildPageList creates a bullet list of links to child pages.
// Uses CONF_PAGE_ID placeholders that get resolved to actual wiki page URLs during import.
func (c *tiptapConverter) generateChildPageList() []TipTapNode {
	if len(c.children) == 0 {
		return nil
	}

	// Create list items for each child page
	var listItems []TipTapNode
	for _, child := range c.children {
		// Create a link with CONF_PAGE_ID placeholder that gets resolved during import
		linkURL := fmt.Sprintf("{{CONF_PAGE_ID:%s}}", child.ID)
		listItem := TipTapNode{
			Type: NodeTypeListItem,
			Content: []TipTapNode{{
				Type: NodeTypeParagraph,
				Content: []TipTapNode{{
					Type: NodeTypeText,
					Text: child.Title,
					Marks: []TipTapMark{{
						Type: MarkTypeLink,
						Attrs: map[string]any{
							"href": linkURL,
						},
					}},
				}},
			}},
		}
		listItems = append(listItems, listItem)
	}

	// Wrap in a bullet list
	return []TipTapNode{{
		Type:    NodeTypeBulletList,
		Content: listItems,
	}}
}

// Helper functions

func getAttr(n *html.Node, key string) string {
	for _, attr := range n.Attr {
		if strings.EqualFold(attr.Key, key) {
			return attr.Val
		}
	}
	return ""
}

func extractText(n *html.Node) string {
	var text strings.Builder
	var extract func(*html.Node)
	extract = func(node *html.Node) {
		if node.Type == html.TextNode {
			text.WriteString(node.Data)
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			extract(child)
		}
	}
	extract(n)
	return text.String()
}

func isPreformattedElement(n *html.Node) bool {
	if n.Type != html.ElementNode {
		return false
	}
	tag := strings.ToLower(n.Data)
	return tag == "pre" || tag == "code" || tag == "ac:plain-text-body"
}

// isInlineElement returns true if the element is an inline/phrasing element
// where whitespace between children should be preserved.
func isInlineElement(n *html.Node) bool {
	if n.Type != html.ElementNode {
		return false
	}
	tag := strings.ToLower(n.Data)
	// Common inline elements where whitespace matters for word separation
	switch tag {
	case "p", "span", "a", "strong", "b", "em", "i", "u", "s", "strike", "del",
		"code", "tt", "samp", "kbd", "sub", "sup", "small", "mark", "q",
		"abbr", "cite", "dfn", "time", "var", "li", "td", "th", "h1", "h2",
		"h3", "h4", "h5", "h6", "label", "legend", "caption", "figcaption":
		return true
	}
	return false
}

// normalizeWhitespace collapses multiple whitespace characters into single spaces
// while preserving leading and trailing spaces for word boundary preservation.
func normalizeWhitespace(s string) string {
	// Check for leading/trailing whitespace before normalization
	hasLeadingSpace := len(s) > 0 && (s[0] == ' ' || s[0] == '\t' || s[0] == '\n' || s[0] == '\r')
	hasTrailingSpace := len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t' || s[len(s)-1] == '\n' || s[len(s)-1] == '\r')

	// Collapse all whitespace sequences to single space
	result := strings.Join(strings.Fields(s), " ")

	// Restore leading/trailing space if they existed (for word boundaries)
	if hasLeadingSpace && len(result) > 0 {
		result = " " + result
	}
	if hasTrailingSpace && len(result) > 0 {
		result = result + " "
	}

	return result
}

func extractLanguageFromClass(class string) string {
	// Extract language from class like "language-go" or "brush: java"
	if strings.HasPrefix(class, "language-") {
		return strings.TrimPrefix(class, "language-")
	}
	if strings.Contains(class, "brush:") {
		parts := strings.Split(class, ":")
		if len(parts) > 1 {
			return strings.TrimSpace(parts[1])
		}
	}
	return ""
}

func flattenContent(nodes []TipTapNode) []TipTapNode {
	var result []TipTapNode
	for _, node := range nodes {
		if node.Type == "" {
			continue
		}
		result = append(result, node)
	}
	if len(result) == 0 {
		// Empty doc needs at least one paragraph
		return []TipTapNode{{Type: NodeTypeParagraph, Content: []TipTapNode{}}}
	}
	return result
}

func wrapInParagraphIfNeeded(content []TipTapNode) []TipTapNode {
	if len(content) == 0 {
		return []TipTapNode{{Type: NodeTypeParagraph, Content: []TipTapNode{}}}
	}
	// If first node is inline (text), wrap in paragraph
	if content[0].Type == NodeTypeText {
		return []TipTapNode{{
			Type:    NodeTypeParagraph,
			Content: content,
		}}
	}
	return content
}

// ConvertHTMLToMarkdown converts Confluence HTML content to Markdown.
// This is used for page comments which should be plain text/markdown, not TipTap JSON.
func ConvertHTMLToMarkdown(htmlContent string) string {
	// Pre-process Confluence-specific macros
	htmlContent = preprocessConfluenceMacros(htmlContent)

	// Parse HTML
	doc, err := html.Parse(strings.NewReader(htmlContent))
	if err != nil {
		// Fallback to stripped HTML
		return stripHTMLForMarkdown(htmlContent)
	}

	// Convert to Markdown
	converter := &markdownConverter{}
	return strings.TrimSpace(converter.convertNode(doc))
}

type markdownConverter struct{}

func (m *markdownConverter) convertNode(n *html.Node) string {
	var result strings.Builder

	switch n.Type {
	case html.TextNode:
		text := n.Data
		// Normalize whitespace for non-preformatted text
		if n.Parent == nil || !isPreformattedElement(n.Parent) {
			if strings.TrimSpace(text) == "" {
				// Preserve single space for word boundaries in inline contexts
				if n.Parent != nil && isInlineElement(n.Parent) && strings.Contains(text, " ") {
					result.WriteString(" ")
				}
			} else {
				result.WriteString(normalizeWhitespace(text))
			}
		} else {
			result.WriteString(text)
		}

	case html.ElementNode:
		result.WriteString(m.convertElement(n))

	case html.DocumentNode:
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			result.WriteString(m.convertNode(child))
		}
	}

	return result.String()
}

func (m *markdownConverter) convertElement(n *html.Node) string {
	var result strings.Builder

	switch strings.ToLower(n.Data) {
	// Block elements
	case "p", "div":
		content := m.convertChildren(n)
		if strings.TrimSpace(content) != "" {
			result.WriteString(content)
			result.WriteString("\n\n")
		}

	case "br":
		result.WriteString("\n")

	case "h1", "h2", "h3", "h4", "h5", "h6":
		level := int(n.Data[1] - '0')
		result.WriteString(strings.Repeat("#", level))
		result.WriteString(" ")
		result.WriteString(m.convertChildren(n))
		result.WriteString("\n\n")

	case "ul", "ol":
		result.WriteString(m.convertList(n, strings.ToLower(n.Data) == "ol"))
		result.WriteString("\n")

	case "li":
		result.WriteString(m.convertChildren(n))

	case "pre", "code":
		text := extractText(n)
		if n.Parent != nil && strings.ToLower(n.Parent.Data) == "pre" {
			// Inside pre, just return text (parent handles formatting)
			result.WriteString(text)
		} else if strings.ToLower(n.Data) == "pre" {
			// Code block
			result.WriteString("```\n")
			result.WriteString(text)
			result.WriteString("\n```\n\n")
		} else {
			// Inline code
			result.WriteString("`")
			result.WriteString(strings.TrimSpace(m.convertChildren(n)))
			result.WriteString("`")
		}

	case "blockquote":
		content := m.convertChildren(n)
		lines := strings.Split(strings.TrimSpace(content), "\n")
		for _, line := range lines {
			result.WriteString("> ")
			result.WriteString(line)
			result.WriteString("\n")
		}
		result.WriteString("\n")

	case "hr":
		result.WriteString("---\n\n")

	case "a":
		href := getAttr(n, "href")
		text := strings.TrimSpace(m.convertChildren(n))
		if href != "" && text != "" {
			// If text equals href, just output the URL
			if text == href {
				result.WriteString(href)
			} else {
				result.WriteString("[")
				result.WriteString(text)
				result.WriteString("](")
				result.WriteString(href)
				result.WriteString(")")
			}
		} else if text != "" {
			result.WriteString(text)
		} else if href != "" {
			result.WriteString(href)
		}

	case "strong", "b":
		content := m.convertChildren(n)
		if strings.TrimSpace(content) != "" {
			result.WriteString("**")
			result.WriteString(content)
			result.WriteString("**")
		}

	case "em", "i":
		content := m.convertChildren(n)
		if strings.TrimSpace(content) != "" {
			result.WriteString("*")
			result.WriteString(content)
			result.WriteString("*")
		}

	case "s", "strike", "del":
		content := m.convertChildren(n)
		if strings.TrimSpace(content) != "" {
			result.WriteString("~~")
			result.WriteString(content)
			result.WriteString("~~")
		}

	case "tt", "samp", "kbd":
		content := m.convertChildren(n)
		if strings.TrimSpace(content) != "" {
			result.WriteString("`")
			result.WriteString(content)
			result.WriteString("`")
		}

	case "img":
		src := getAttr(n, "src")
		alt := getAttr(n, "alt")
		if src != "" {
			result.WriteString("![")
			result.WriteString(alt)
			result.WriteString("](")
			result.WriteString(src)
			result.WriteString(")")
		}

	// Confluence-specific - inline comment marker (just pass through content)
	case "ac:inline-comment-marker":
		result.WriteString(m.convertChildren(n))

	// Confluence structured macros
	case "ac:structured-macro":
		macroName := getAttr(n, "ac:name")
		switch macroName {
		case "jira":
			// JIRA macro - extract only the issue key
			// Structure: <ac:structured-macro ac:name="jira">
			//   <ac:parameter ac:name="key">MM-50469</ac:parameter>
			//   <ac:parameter ac:name="server">System JIRA</ac:parameter>
			//   <ac:parameter ac:name="serverId">UUID</ac:parameter>
			// </ac:structured-macro>
			jiraKey := ""
			for child := n.FirstChild; child != nil; child = child.NextSibling {
				if child.Type == html.ElementNode && strings.ToLower(child.Data) == "ac:parameter" {
					paramName := getAttr(child, "ac:name")
					if paramName == "key" {
						jiraKey = extractText(child)
						break
					}
				}
			}
			if jiraKey != "" {
				result.WriteString(jiraKey)
			}
		case "code":
			// Code block macro
			text := ""
			for child := n.FirstChild; child != nil; child = child.NextSibling {
				if child.Type == html.ElementNode && strings.ToLower(child.Data) == "ac:plain-text-body" {
					text = extractText(child)
					break
				}
			}
			if text != "" {
				result.WriteString("```\n")
				result.WriteString(text)
				result.WriteString("\n```\n\n")
			}
		case "info", "note", "warning", "tip":
			// Panel macros - convert to blockquote
			content := m.convertChildren(n)
			lines := strings.Split(strings.TrimSpace(content), "\n")
			for _, line := range lines {
				result.WriteString("> ")
				result.WriteString(line)
				result.WriteString("\n")
			}
			result.WriteString("\n")
		case "expand", "toc":
			// Skip table of contents, expand macro content inline
			result.WriteString(m.convertChildren(n))
		default:
			// Unknown macro - try to extract content
			result.WriteString(m.convertChildren(n))
		}

	// Pass-through containers
	case "html", "body", "head", "span", "font", "u":
		result.WriteString(m.convertChildren(n))

	// Confluence parameter elements - skip (they contain macro configuration, not content)
	case "ac:parameter":
		// Skip - don't extract text from parameter elements

	default:
		// Unknown element - just process children
		result.WriteString(m.convertChildren(n))
	}

	return result.String()
}

func (m *markdownConverter) convertChildren(n *html.Node) string {
	var result strings.Builder
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		result.WriteString(m.convertNode(child))
	}
	return result.String()
}

func (m *markdownConverter) convertList(n *html.Node, ordered bool) string {
	var result strings.Builder
	index := 1
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		if child.Type == html.ElementNode && strings.ToLower(child.Data) == "li" {
			if ordered {
				result.WriteString(strconv.Itoa(index))
				result.WriteString(". ")
				index++
			} else {
				result.WriteString("- ")
			}
			result.WriteString(strings.TrimSpace(m.convertChildren(child)))
			result.WriteString("\n")
		}
	}
	return result.String()
}

// stripHTMLForMarkdown is a simple fallback that removes HTML tags.
func stripHTMLForMarkdown(html string) string {
	var result []rune
	inTag := false
	for _, c := range html {
		if c == '<' {
			inTag = true
			continue
		}
		if c == '>' {
			inTag = false
			continue
		}
		if !inTag {
			result = append(result, c)
		}
	}
	return strings.TrimSpace(string(result))
}

// preprocessConfluenceMacros converts Confluence-specific XML elements to standard HTML.
func preprocessConfluenceMacros(content string) string {
	// Replace CDATA sections - use (?s) flag to make . match newlines
	// This is critical for multiline content like code blocks
	// Note: Some Confluence exports have malformed CDATA with space: "]] >" instead of "]]>"
	cdataRegex := regexp.MustCompile(`(?s)<!\[CDATA\[(.*?)\]\]\s*>`)
	content = cdataRegex.ReplaceAllString(content, "$1")

	// Convert self-closing XML tags to properly closed tags.
	// HTML5 parser doesn't recognize self-closing syntax for non-void elements,
	// so <ac:structured-macro /> becomes an unclosed element that swallows subsequent content.
	// Convert: <ac:structured-macro ... /> → <ac:structured-macro ...></ac:structured-macro>
	selfClosingTagRegex := regexp.MustCompile(`<(ac:[a-zA-Z-]+)([^>]*)\s*/>`)
	content = selfClosingTagRegex.ReplaceAllString(content, "<$1$2></$1>")

	// Use comprehensive link converter
	content = ConvertConfluenceLinks(content)

	return content
}
