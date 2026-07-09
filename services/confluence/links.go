// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package confluence

import (
	"regexp"
	"strings"
)

// Link placeholder formats
const (
	// PlaceholderPageByID links to page by Confluence page ID
	PlaceholderPageByID = "{{CONF_PAGE_ID:%s}}"
	// PlaceholderPageByTitle links to page by title (for resolution by title lookup)
	PlaceholderPageByTitle = "{{CONF_PAGE_TITLE:%s}}"
	// PlaceholderFile links to attachment by Confluence attachment ID
	PlaceholderFile = "{{CONF_FILE:%s}}"
	// PlaceholderUser links to a user mention
	PlaceholderUser = "{{CONF_USER:%s}}"
)

// LinkConverter handles conversion of Confluence links to placeholders
// and resolution of placeholders to Mattermost URLs.
type LinkConverter struct {
	// PageIDToTitle maps Confluence page IDs to titles
	PageIDToTitle map[string]string
	// PageTitleToID maps page titles to Confluence IDs
	PageTitleToID map[string]string
	// AttachmentPaths maps attachment identifiers to file paths
	AttachmentPaths map[string]string
}

// NewLinkConverter creates a new link converter.
func NewLinkConverter() *LinkConverter {
	return &LinkConverter{
		PageIDToTitle:   make(map[string]string),
		PageTitleToID:   make(map[string]string),
		AttachmentPaths: make(map[string]string),
	}
}

// BuildMappings builds the link mappings from a Confluence export.
func (lc *LinkConverter) BuildMappings(export *ConfluenceExport) {
	for _, page := range export.Pages {
		lc.PageIDToTitle[page.ID] = page.Title
		lc.PageTitleToID[strings.ToLower(page.Title)] = page.ID
	}

	for pageID, attachments := range export.Attachments {
		for _, att := range attachments {
			key := pageID + ":" + att.FileName
			lc.AttachmentPaths[key] = att.FilePath
			// Also map just by filename for simpler lookups
			lc.AttachmentPaths[att.FileName] = att.FilePath
		}
	}
}

// ConvertConfluenceLinks converts Confluence link elements to placeholders.
// This is called during HTML preprocessing before TipTap conversion.
func ConvertConfluenceLinks(content string) string {
	// Handle ac:link with ri:page by content-title
	// <ac:link><ri:page ri:content-title="Page Title"/></ac:link>
	acLinkTitleRegex := regexp.MustCompile(`(?s)<ac:link[^>]*>.*?<ri:page[^>]*ri:content-title="([^"]+)"[^>]*/?>.*?</ac:link>`)
	content = acLinkTitleRegex.ReplaceAllStringFunc(content, func(match string) string {
		titleMatch := regexp.MustCompile(`ri:content-title="([^"]+)"`).FindStringSubmatch(match)
		if len(titleMatch) < 2 {
			return match
		}
		title := titleMatch[1]

		// Check for custom link body
		linkBody := extractLinkBody(match)
		if linkBody == "" {
			linkBody = title
		}

		return `<a href="{{CONF_PAGE_TITLE:` + escapeForPlaceholder(title) + `}}">` + linkBody + `</a>`
	})

	// Handle ac:link with ri:page by content-id
	// <ac:link><ri:page ri:content-id="123456"/></ac:link>
	acLinkIDRegex := regexp.MustCompile(`(?s)<ac:link[^>]*>.*?<ri:page[^>]*ri:content-id="([^"]+)"[^>]*/?>.*?</ac:link>`)
	content = acLinkIDRegex.ReplaceAllStringFunc(content, func(match string) string {
		idMatch := regexp.MustCompile(`ri:content-id="([^"]+)"`).FindStringSubmatch(match)
		if len(idMatch) < 2 {
			return match
		}
		pageID := idMatch[1]

		// Check for custom link body
		linkBody := extractLinkBody(match)
		if linkBody == "" {
			linkBody = "Page " + pageID
		}

		return `<a href="{{CONF_PAGE_ID:` + pageID + `}}">` + linkBody + `</a>`
	})

	// Handle ac:link with ri:attachment
	// <ac:link><ri:attachment ri:filename="doc.pdf"/></ac:link>
	acAttachmentRegex := regexp.MustCompile(`(?s)<ac:link[^>]*>.*?<ri:attachment[^>]*ri:filename="([^"]+)"[^>]*/?>.*?</ac:link>`)
	content = acAttachmentRegex.ReplaceAllStringFunc(content, func(match string) string {
		fileMatch := regexp.MustCompile(`ri:filename="([^"]+)"`).FindStringSubmatch(match)
		if len(fileMatch) < 2 {
			return match
		}
		filename := fileMatch[1]

		linkBody := extractLinkBody(match)
		if linkBody == "" {
			linkBody = filename
		}

		return `<a href="{{CONF_ATTACHMENT:` + escapeForPlaceholder(filename) + `}}">` + linkBody + `</a>`
	})

	// Handle ac:link with ri:url (external links)
	// <ac:link><ri:url ri:value="https://example.com"/></ac:link>
	acURLRegex := regexp.MustCompile(`(?s)<ac:link[^>]*>.*?<ri:url[^>]*ri:value="([^"]+)"[^>]*/?>.*?</ac:link>`)
	content = acURLRegex.ReplaceAllStringFunc(content, func(match string) string {
		urlMatch := regexp.MustCompile(`ri:value="([^"]+)"`).FindStringSubmatch(match)
		if len(urlMatch) < 2 {
			return match
		}
		url := urlMatch[1]

		linkBody := extractLinkBody(match)
		if linkBody == "" {
			linkBody = url
		}

		return `<a href="` + url + `">` + linkBody + `</a>`
	})

	// Handle standalone ri:url (simpler form)
	riURLRegex := regexp.MustCompile(`<ri:url ri:value="([^"]+)"\s*/>`)
	content = riURLRegex.ReplaceAllString(content, `<a href="$1">$1</a>`)

	// Handle user mentions
	// <ac:link><ri:user ri:account-id="user123"/></ac:link>
	// We use the placeholder directly as the display text so it can be resolved later
	userMentionRegex := regexp.MustCompile(`(?s)<ac:link[^>]*>.*?<ri:user[^>]*ri:(?:account-id|userkey)="([^"]+)"[^>]*/?>.*?</ac:link>`)
	content = userMentionRegex.ReplaceAllString(content, `{{CONF_USER:$1}}`)

	// Handle ac:image elements
	// <ac:image><ri:attachment ri:filename="image.png"/></ac:image>
	acImageRegex := regexp.MustCompile(`(?s)<ac:image[^>]*>.*?<ri:attachment[^>]*ri:filename="([^"]+)"[^>]*/?>.*?</ac:image>`)
	content = acImageRegex.ReplaceAllString(content, `<img src="{{CONF_ATTACHMENT:$1}}" alt="$1"/>`)

	// Handle ac:image with ri:url (external images)
	acImageURLRegex := regexp.MustCompile(`(?s)<ac:image[^>]*>.*?<ri:url[^>]*ri:value="([^"]+)"[^>]*/?>.*?</ac:image>`)
	content = acImageURLRegex.ReplaceAllString(content, `<img src="$1" alt=""/>`)

	// Handle emoticons
	// <ac:emoticon ac:name="smile"/>
	emoticonRegex := regexp.MustCompile(`<ac:emoticon[^>]*ac:name="([^"]+)"[^>]*/>`)
	content = emoticonRegex.ReplaceAllStringFunc(content, func(match string) string {
		nameMatch := regexp.MustCompile(`ac:name="([^"]+)"`).FindStringSubmatch(match)
		if len(nameMatch) < 2 {
			return match
		}
		return emoticonToEmoji(nameMatch[1])
	})

	return content
}

// extractLinkBody extracts custom link text from ac:link elements.
func extractLinkBody(acLink string) string {
	// Try ac:plain-text-link-body first
	plainTextRegex := regexp.MustCompile(`(?s)<ac:plain-text-link-body><!\[CDATA\[(.*?)\]\]></ac:plain-text-link-body>`)
	if match := plainTextRegex.FindStringSubmatch(acLink); len(match) > 1 {
		return strings.TrimSpace(match[1])
	}

	// Try ac:link-body
	linkBodyRegex := regexp.MustCompile(`(?s)<ac:link-body>(.*?)</ac:link-body>`)
	if match := linkBodyRegex.FindStringSubmatch(acLink); len(match) > 1 {
		// Strip any nested HTML tags
		text := regexp.MustCompile(`<[^>]+>`).ReplaceAllString(match[1], "")
		return strings.TrimSpace(text)
	}

	return ""
}

// escapeForPlaceholder escapes special characters in placeholder values.
func escapeForPlaceholder(s string) string {
	s = strings.ReplaceAll(s, "}", "\\}")
	s = strings.ReplaceAll(s, "{", "\\{")
	return s
}

// unescapeFromPlaceholder reverses placeholder escaping.
func unescapeFromPlaceholder(s string) string {
	s = strings.ReplaceAll(s, "\\}", "}")
	s = strings.ReplaceAll(s, "\\{", "{")
	return s
}

// emoticonToEmoji converts Confluence emoticon names to Unicode emoji.
func emoticonToEmoji(name string) string {
	emoticons := map[string]string{
		"smile":        "😊",
		"sad":          "😢",
		"cheeky":       "😜",
		"laugh":        "😂",
		"wink":         "😉",
		"thumbs-up":    "👍",
		"thumbs-down":  "👎",
		"information":  "ℹ️",
		"tick":         "✅",
		"cross":        "❌",
		"warning":      "⚠️",
		"plus":         "➕",
		"minus":        "➖",
		"question":     "❓",
		"light-on":     "💡",
		"light-off":    "💡",
		"yellow-star":  "⭐",
		"red-star":     "⭐",
		"green-star":   "⭐",
		"blue-star":    "⭐",
		"heart":        "❤️",
		"broken-heart": "💔",
	}

	if emoji, ok := emoticons[name]; ok {
		return emoji
	}
	return ":" + name + ":"
}

// ResolvePlaceholders resolves link placeholders in content using provided mappings.
// This is used for post-import link resolution.
func ResolvePlaceholders(content string, pageIDToMMID map[string]string, pageTitleToMMID map[string]string, baseURL string) string {
	// Resolve page ID placeholders
	pageIDRegex := regexp.MustCompile(`\{\{CONF_PAGE_ID:([^}]+)\}\}`)
	content = pageIDRegex.ReplaceAllStringFunc(content, func(match string) string {
		submatch := pageIDRegex.FindStringSubmatch(match)
		if len(submatch) < 2 {
			return match
		}
		confID := submatch[1]
		if mmID, ok := pageIDToMMID[confID]; ok {
			return baseURL + "/pages/" + mmID
		}
		return match // Leave unresolved
	})

	// Resolve page title placeholders
	pageTitleRegex := regexp.MustCompile(`\{\{CONF_PAGE_TITLE:([^}]+)\}\}`)
	content = pageTitleRegex.ReplaceAllStringFunc(content, func(match string) string {
		submatch := pageTitleRegex.FindStringSubmatch(match)
		if len(submatch) < 2 {
			return match
		}
		title := unescapeFromPlaceholder(submatch[1])
		if mmID, ok := pageTitleToMMID[strings.ToLower(title)]; ok {
			return baseURL + "/pages/" + mmID
		}
		return match // Leave unresolved
	})

	return content
}

// ResolveUserMentions resolves {{CONF_USER:userkey}} placeholders to actual usernames.
// It uses the Users map from the Confluence export and the resolveUsername logic.
func ResolveUserMentions(content string, users map[string]*ConfluenceUser) string {
	userPlaceholderRegex := regexp.MustCompile(`\{\{CONF_USER:([^}]+)\}\}`)

	return userPlaceholderRegex.ReplaceAllStringFunc(content, func(match string) string {
		submatch := userPlaceholderRegex.FindStringSubmatch(match)
		if len(submatch) < 2 {
			return match
		}
		userKey := submatch[1]

		// Look up the user by their key/account ID
		username := resolveUserMention(userKey, users)
		return username
	})
}

// resolveUserMention resolves a Confluence user key to a Mattermost username.
func resolveUserMention(userKey string, users map[string]*ConfluenceUser) string {
	// Try to find user by account ID (the key in the map)
	if user, ok := users[userKey]; ok {
		// If user has email, extract username from it
		if user.Email != "" {
			return "@" + emailToUsername(user.Email)
		}
		// If username looks like an email, extract the local part
		if user.Username != "" && strings.Contains(user.Username, "@") {
			return "@" + emailToUsername(user.Username)
		}
		// If username doesn't look like an account ID, use it
		if user.Username != "" && !looksLikeAccountID(user.Username) {
			return "@" + user.Username
		}
	}

	// Search through all users to find one with matching username that looks like an account ID
	// (some mentions reference the atlassianAccountId stored in the username field)
	for _, user := range users {
		if user.Username == userKey || user.AccountID == userKey {
			if user.Email != "" {
				return "@" + emailToUsername(user.Email)
			}
			if user.Username != "" && strings.Contains(user.Username, "@") {
				return "@" + emailToUsername(user.Username)
			}
			if user.Username != "" && !looksLikeAccountID(user.Username) {
				return "@" + user.Username
			}
		}
	}

	// Fallback: use truncated key with prefix
	if len(userKey) >= 8 {
		return "@confluence_user_" + userKey[:8]
	}
	return "@confluence_user_" + userKey
}

// CountUnresolvedPlaceholders counts unresolved placeholders in content.
func CountUnresolvedPlaceholders(content string) int {
	placeholderRegex := regexp.MustCompile(`\{\{CONF_[A-Z_]+:[^}]+\}\}`)
	return len(placeholderRegex.FindAllString(content, -1))
}

// ExtractUnresolvedPlaceholders returns all unresolved placeholders in content.
func ExtractUnresolvedPlaceholders(content string) []string {
	placeholderRegex := regexp.MustCompile(`\{\{CONF_[A-Z_]+:[^}]+\}\}`)
	return placeholderRegex.FindAllString(content, -1)
}

// ConvertAttachmentPlaceholdersToFileIDs converts filename-based attachment placeholders
// to ID-based placeholders using the provided mapping.
// Input: content with {{CONF_ATTACHMENT:filename}} placeholders
// Output: content with {{CONF_FILE:attachment_id}} placeholders
func ConvertAttachmentPlaceholdersToFileIDs(content string, filenameToID map[string]string) string {
	if len(filenameToID) == 0 {
		return content
	}

	attachmentRegex := regexp.MustCompile(`\{\{CONF_ATTACHMENT:([^}]+)\}\}`)
	return attachmentRegex.ReplaceAllStringFunc(content, func(match string) string {
		submatch := attachmentRegex.FindStringSubmatch(match)
		if len(submatch) < 2 {
			return match
		}
		filename := unescapeFromPlaceholder(submatch[1])
		if attachmentID, ok := filenameToID[filename]; ok {
			return "{{CONF_FILE:" + attachmentID + "}}"
		}
		// Leave unresolved if no mapping found
		return match
	})
}
