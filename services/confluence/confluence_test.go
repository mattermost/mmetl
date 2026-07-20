// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package confluence

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExtractInlineCommentAnchors(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		expected map[string]string
	}{
		{
			name:     "empty content",
			content:  "",
			expected: map[string]string{},
		},
		{
			name:     "no inline comments",
			content:  "<p>Hello world</p>",
			expected: map[string]string{},
		},
		{
			name:     "single inline comment",
			content:  `<p>Some text <ac:inline-comment-marker ac:ref="123456">highlighted text</ac:inline-comment-marker> more text</p>`,
			expected: map[string]string{"123456": "highlighted text"},
		},
		{
			name:     "multiple inline comments",
			content:  `<p><ac:inline-comment-marker ac:ref="111">first anchor</ac:inline-comment-marker> and <ac:inline-comment-marker ac:ref="222">second anchor</ac:inline-comment-marker></p>`,
			expected: map[string]string{"111": "first anchor", "222": "second anchor"},
		},
		{
			name:     "inline comment with nested HTML",
			content:  `<ac:inline-comment-marker ac:ref="456"><strong>bold</strong> and <em>italic</em> text</ac:inline-comment-marker>`,
			expected: map[string]string{"456": "bold and italic text"},
		},
		{
			name:    "real confluence format",
			content: `<p>User submits <ac:inline-comment-marker ac:ref="f9c7bf1b-0d36-42a6-8dfa-85c6a6fd2d74">Contact us form either in portal or cloud</ac:inline-comment-marker></p>`,
			expected: map[string]string{
				"f9c7bf1b-0d36-42a6-8dfa-85c6a6fd2d74": "Contact us form either in portal or cloud",
			},
		},
		{
			name:     "whitespace handling",
			content:  `<ac:inline-comment-marker ac:ref="789">   trimmed text   </ac:inline-comment-marker>`,
			expected: map[string]string{"789": "trimmed text"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := extractInlineCommentAnchors(tc.content)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestConvertHTMLToMarkdown_JiraMacro(t *testing.T) {
	tests := []struct {
		name     string
		html     string
		expected string
	}{
		{
			name: "JIRA macro extracts key only",
			html: `<p>Here's the epic for some more context: <ac:structured-macro ac:name="jira">
				<ac:parameter ac:name="server">System JIRA</ac:parameter>
				<ac:parameter ac:name="serverId">fa8b0166-b019-31be-aef3-0e1e83e7ecff</ac:parameter>
				<ac:parameter ac:name="key">MM-50469</ac:parameter>
			</ac:structured-macro></p>`,
			expected: "Here's the epic for some more context: MM-50469",
		},
		{
			name: "JIRA macro with key first",
			html: `<p>Link to <ac:structured-macro ac:name="jira">
				<ac:parameter ac:name="key">PROJ-123</ac:parameter>
				<ac:parameter ac:name="server">JIRA Server</ac:parameter>
			</ac:structured-macro> ticket</p>`,
			expected: "Link to PROJ-123 ticket",
		},
		{
			name:     "JIRA macro without key",
			html:     `<ac:structured-macro ac:name="jira"><ac:parameter ac:name="server">JIRA</ac:parameter></ac:structured-macro>`,
			expected: "",
		},
		{
			name:     "Code macro in comment",
			html:     `<p>Use this code:</p><ac:structured-macro ac:name="code"><ac:plain-text-body>const x = 1;</ac:plain-text-body></ac:structured-macro>`,
			expected: "Use this code:\n\n```\nconst x = 1;\n```",
		},
		{
			name:     "Info panel macro",
			html:     `<ac:structured-macro ac:name="info"><p>Important note</p></ac:structured-macro>`,
			expected: "> Important note",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := ConvertHTMLToMarkdown(tc.html)
			assert.Equal(t, strings.TrimSpace(tc.expected), strings.TrimSpace(result))
		})
	}
}

func TestConvertHTMLToTipTap_JiraMacro(t *testing.T) {
	tests := []struct {
		name     string
		html     string
		contains string
	}{
		{
			name: "JIRA macro extracts key",
			html: `<p>Link: <ac:structured-macro ac:name="jira">
				<ac:parameter ac:name="key">MM-50469</ac:parameter>
				<ac:parameter ac:name="server">System JIRA</ac:parameter>
			</ac:structured-macro></p>`,
			contains: "MM-50469",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result, err := ConvertHTMLToTipTap(tc.html)
			require.NoError(t, err)
			assert.Contains(t, result, tc.contains)
			// Should NOT contain the server ID or server name
			assert.NotContains(t, result, "System JIRA")
			assert.NotContains(t, result, "fa8b0166")
		})
	}
}

func TestConvertHTMLToTipTap_ChildrenMacro(t *testing.T) {
	// Test that dynamic macros like "children" generate placeholder text when no children info provided
	// Before the fix, this would produce "true1title" from the parameter values
	tests := []struct {
		name        string
		html        string
		notContains []string
		contains    string
	}{
		{
			name: "children macro should generate placeholder when no children",
			html: `<ac:structured-macro ac:name="children">
				<ac:parameter ac:name="all">true</ac:parameter>
				<ac:parameter ac:name="first">1</ac:parameter>
			</ac:structured-macro>`,
			notContains: []string{"true", "1", "true1"},
			contains:    "list of child pages",
		},
		{
			name: "contentbylabel macro should generate placeholder",
			html: `<ac:structured-macro ac:name="contentbylabel">
				<ac:parameter ac:name="cql">label = "important"</ac:parameter>
				<ac:parameter ac:name="max">10</ac:parameter>
			</ac:structured-macro>`,
			notContains: []string{"important", "10", "cql", "max"},
			contains:    "pages by label",
		},
		{
			name: "pagetree macro should generate placeholder when no children",
			html: `<ac:structured-macro ac:name="pagetree">
				<ac:parameter ac:name="root">@home</ac:parameter>
				<ac:parameter ac:name="expandCollapseAll">true</ac:parameter>
			</ac:structured-macro>`,
			notContains: []string{"@home", "expandCollapseAll"},
			contains:    "list of child pages",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result, err := ConvertHTMLToTipTap(tc.html)
			require.NoError(t, err)
			for _, notExpected := range tc.notContains {
				assert.NotContains(t, result, notExpected, "Result should not contain parameter value: %s", notExpected)
			}
			assert.Contains(t, result, tc.contains, "Result should contain placeholder text")
		})
	}
}

func TestConvertHTMLToTipTap_ChildrenMacroWithChildren(t *testing.T) {
	// Test that children macro generates actual links when children info is provided
	children := []ChildPageInfo{
		{ID: "123456", Title: "Getting Started"},
		{ID: "789012", Title: "User Guide"},
		{ID: "345678", Title: "FAQ"},
	}

	html := `<ac:structured-macro ac:name="children">
		<ac:parameter ac:name="all">true</ac:parameter>
	</ac:structured-macro>`

	result, err := ConvertHTMLToTipTapWithChildren(html, children)
	require.NoError(t, err)

	// Should contain bullet list with child page titles
	assert.Contains(t, result, "bulletList", "Should generate a bullet list")
	assert.Contains(t, result, "Getting Started", "Should contain first child title")
	assert.Contains(t, result, "User Guide", "Should contain second child title")
	assert.Contains(t, result, "FAQ", "Should contain third child title")

	// Should contain CONF_PAGE_ID placeholders for links
	assert.Contains(t, result, "{{CONF_PAGE_ID:123456}}", "Should contain link placeholder for first child")
	assert.Contains(t, result, "{{CONF_PAGE_ID:789012}}", "Should contain link placeholder for second child")
	assert.Contains(t, result, "{{CONF_PAGE_ID:345678}}", "Should contain link placeholder for third child")

	// Should NOT contain parameter values
	assert.NotContains(t, result, `"true"`, "Should not contain parameter value")

	// Should NOT contain placeholder text since we have children
	assert.NotContains(t, result, "list of child pages", "Should not contain placeholder when children provided")
}
