// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package confluence

import (
	"fmt"
	"sort"
)

// buildPageHierarchy builds the page tree and returns pages in topological order.
// Parents are always returned before their children.
func (t *Transformer) buildPageHierarchy(pages []*ConfluencePage) ([]*ConfluencePage, error) {
	t.Logger.Info("Building page hierarchy")

	// Build lookup maps
	pageByID := make(map[string]*ConfluencePage)
	childrenByParent := make(map[string][]*ConfluencePage)
	rootPages := []*ConfluencePage{}

	for _, page := range pages {
		pageByID[page.ID] = page

		if page.ParentID == "" {
			rootPages = append(rootPages, page)
		} else {
			childrenByParent[page.ParentID] = append(childrenByParent[page.ParentID], page)
		}
	}

	// Detect cycles using DFS
	if err := t.detectCycles(pages, pageByID); err != nil {
		return nil, err
	}

	// Sort root pages by position (with title as fallback for deterministic output)
	sort.Slice(rootPages, func(i, j int) bool {
		return comparePageOrder(rootPages[i], rootPages[j])
	})

	// Perform topological sort (BFS from roots)
	result := make([]*ConfluencePage, 0, len(pages))
	visited := make(map[string]bool)

	var visit func(page *ConfluencePage, depth int)
	visit = func(page *ConfluencePage, depth int) {
		if visited[page.ID] {
			return
		}
		visited[page.ID] = true

		// Check max depth
		if depth > t.Config.MaxDepth {
			t.Logger.Warnf("Page %s (%s) exceeds max depth %d, flattening to max depth",
				page.ID, page.Title, t.Config.MaxDepth)
			// Find the ancestor at max depth and re-parent
			page.ParentID = t.findAncestorAtDepth(page, pageByID, t.Config.MaxDepth-1)
			t.Stats.PagesFlattened++
			t.Stats.Warnings = append(t.Stats.Warnings,
				fmt.Sprintf("Page '%s' flattened from depth %d to max depth %d", page.Title, depth, t.Config.MaxDepth))
		}

		result = append(result, page)

		// Visit children (sorted by position with title as fallback for deterministic output)
		children := childrenByParent[page.ID]
		sort.Slice(children, func(i, j int) bool {
			return comparePageOrder(children[i], children[j])
		})

		for _, child := range children {
			visit(child, depth+1)
		}
	}

	// Start from root pages
	for _, root := range rootPages {
		visit(root, 1)
	}

	// Handle orphaned pages (pages with non-existent parents)
	for _, page := range pages {
		if !visited[page.ID] {
			t.Logger.Warnf("Page %s (%s) has invalid parent %s, treating as root",
				page.ID, page.Title, page.ParentID)
			page.ParentID = "" // Make it a root page
			visit(page, 1)
		}
	}

	t.Logger.Infof("Hierarchy built: %d pages, %d root pages", len(result), len(rootPages))

	return result, nil
}

// detectCycles checks for cycles in the page hierarchy.
func (t *Transformer) detectCycles(pages []*ConfluencePage, pageByID map[string]*ConfluencePage) error {
	// Track visited state: 0 = unvisited, 1 = visiting (in current path), 2 = visited
	state := make(map[string]int)

	var dfs func(pageID string, path []string) error
	dfs = func(pageID string, path []string) error {
		if state[pageID] == 2 {
			return nil // Already fully processed
		}
		if state[pageID] == 1 {
			// Found a cycle
			cyclePath := append(path, pageID)
			return fmt.Errorf("cycle detected in page hierarchy: %v", cyclePath)
		}

		state[pageID] = 1
		path = append(path, pageID)

		page, ok := pageByID[pageID]
		if !ok {
			state[pageID] = 2
			return nil
		}

		if page.ParentID != "" {
			if err := dfs(page.ParentID, path); err != nil {
				return err
			}
		}

		state[pageID] = 2
		return nil
	}

	for _, page := range pages {
		if state[page.ID] == 0 {
			if err := dfs(page.ID, nil); err != nil {
				return err
			}
		}
	}

	return nil
}

// findAncestorAtDepth finds the ancestor at the specified depth.
func (t *Transformer) findAncestorAtDepth(page *ConfluencePage, pageByID map[string]*ConfluencePage, targetDepth int) string {
	ancestors := t.getAncestors(page, pageByID)
	if targetDepth >= len(ancestors) {
		if len(ancestors) > 0 {
			return ancestors[len(ancestors)-1].ID
		}
		return ""
	}
	return ancestors[targetDepth].ID
}

// getAncestors returns the ancestors from root to immediate parent.
func (t *Transformer) getAncestors(page *ConfluencePage, pageByID map[string]*ConfluencePage) []*ConfluencePage {
	var ancestors []*ConfluencePage
	current := page
	visited := make(map[string]bool)

	for current.ParentID != "" && !visited[current.ParentID] {
		visited[current.ParentID] = true
		parent, ok := pageByID[current.ParentID]
		if !ok {
			break
		}
		ancestors = append([]*ConfluencePage{parent}, ancestors...)
		current = parent
	}

	return ancestors
}

// GetPageDepth returns the depth of a page in the hierarchy (root = 1).
func GetPageDepth(page *ConfluencePage, pageByID map[string]*ConfluencePage) int {
	depth := 1
	current := page
	visited := make(map[string]bool)

	for current.ParentID != "" && !visited[current.ParentID] {
		visited[current.ParentID] = true
		parent, ok := pageByID[current.ParentID]
		if !ok {
			break
		}
		depth++
		current = parent
	}

	return depth
}

// comparePageOrder compares two pages for sorting.
// Pages are sorted by Position first (lower position comes first).
// Pages with Position == 0 are sorted after pages with a position.
// For pages with the same position (or both 0), sort by title alphabetically.
func comparePageOrder(a, b *ConfluencePage) bool {
	// Both have positions - sort by position
	if a.Position > 0 && b.Position > 0 {
		if a.Position != b.Position {
			return a.Position < b.Position
		}
		// Same position, fall back to title
		return a.Title < b.Title
	}

	// Only a has position - a comes first
	if a.Position > 0 {
		return true
	}

	// Only b has position - b comes first
	if b.Position > 0 {
		return false
	}

	// Neither has position - sort by title
	return a.Title < b.Title
}
