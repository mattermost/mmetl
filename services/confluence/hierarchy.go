package confluence

import (
	"fmt"
	"sort"
)

// flattenParentDepth is the deepest output depth a flattened page may be
// attached under. A page reparented there lands at MaxPageDepth.
const flattenParentDepth = MaxPageDepth - 1

// BuildHierarchy resolves the output page tree and returns the pages in
// parent-first emission order.
//
// It promotes pages whose parent was not emitted, flattens anything deeper than
// the destination limit, orders siblings deterministically, and rejects cycles.
// It returns the warnings it raised and the number of pages it flattened.
func BuildHierarchy(pages []*Page) (ordered []*Page, warnings []Warning, flattened int, err error) {
	byID := make(map[string]*Page, len(pages))
	for _, page := range pages {
		byID[page.SourceID] = page
	}

	roots, warnings := linkParents(pages, byID)

	if err := checkNoCycles(pages, byID); err != nil {
		return nil, nil, 0, err
	}

	sortSiblings(roots)
	for _, page := range pages {
		sortSiblings(page.Children)
	}

	flattenWarnings, flattened := assignDepths(roots, byID)
	warnings = append(warnings, flattenWarnings...)

	// Flattening moves pages between parents, so sibling order is only final
	// once every page has landed.
	sortSiblings(roots)
	for _, page := range pages {
		sortSiblings(page.Children)
	}

	ordered = emissionOrder(roots)

	// Every selected page must appear exactly once. Any reparenting bug would
	// otherwise drop a subtree out of the walk silently, which is the one
	// failure mode of this function that no other check would catch.
	if len(ordered) != len(pages) {
		return nil, nil, 0, fmt.Errorf(
			"page hierarchy lost pages: %d selected but %d reachable from the roots", len(pages), len(ordered))
	}
	return ordered, warnings, flattened, nil
}

// linkParents resolves each page's declared parent against the emitted set.
//
// A page whose parent was excluded is promoted to root rather than dropped:
// losing a subtree because one ancestor was archived would silently discard
// current content.
func linkParents(pages []*Page, byID map[string]*Page) (roots []*Page, warnings []Warning) {
	for _, page := range pages {
		page.Children = nil
		page.ParentSourceID = ""
		page.Depth = 0
	}

	for _, page := range pages {
		if page.SourceParentKey.IsZero() {
			roots = append(roots, page)
			continue
		}

		parent, ok := byID[page.SourceParentKey.ID]
		if !ok {
			warnings = append(warnings, Warning{
				Code:       WarnPageParentMissingPromoted,
				EntityType: "page",
				SourceID:   page.SourceID,
				Message: TruncateMessage(fmt.Sprintf(
					"parent %s was not emitted; page promoted to the top level", page.SourceParentKey.ID)),
			})
			roots = append(roots, page)
			continue
		}

		page.ParentSourceID = parent.SourceID
		parent.Children = append(parent.Children, page)
	}
	return roots, warnings
}

// checkNoCycles rejects a parent chain that loops.
//
// A cycle is a hard error, not a promotion: it means the source hierarchy is
// self-contradictory, and any repair would be a guess about which edge is the
// wrong one.
func checkNoCycles(pages []*Page, byID map[string]*Page) error {
	const (
		unvisited = 0
		onStack   = 1
		done      = 2
	)
	state := make(map[string]int, len(pages))

	for _, start := range pages {
		if state[start.SourceID] != unvisited {
			continue
		}

		var chain []string
		for page := start; page != nil; {
			switch state[page.SourceID] {
			case onStack:
				return fmt.Errorf("page hierarchy contains a cycle: %s", cycleDescription(chain, page.SourceID))
			case done:
				page = nil
				continue
			}

			state[page.SourceID] = onStack
			chain = append(chain, page.SourceID)

			if page.ParentSourceID == "" {
				page = nil
				continue
			}
			page = byID[page.ParentSourceID]
		}

		for _, id := range chain {
			state[id] = done
		}
	}
	return nil
}

func cycleDescription(chain []string, repeated string) string {
	for i, id := range chain {
		if id == repeated {
			return fmt.Sprint(append(append([]string{}, chain[i:]...), repeated))
		}
	}
	return fmt.Sprint(append(append([]string{}, chain...), repeated))
}

// assignDepths walks the tree top-down assigning output depths, reparenting any
// page that would exceed the destination limit.
//
// A page too deep is attached to its nearest ancestor at depth
// flattenParentDepth, which puts it at MaxPageDepth. Because the walk is
// top-down, a whole over-deep chain collapses into siblings at MaxPageDepth
// under the same ancestor, and descendants are re-depthed as they are reached.
func assignDepths(roots []*Page, byID map[string]*Page) (warnings []Warning, flattened int) {
	var walk func(page *Page, parentDepth int)
	walk = func(page *Page, parentDepth int) {
		depth := parentDepth + 1
		if depth > MaxPageDepth {
			anchor := nearestAncestorAtDepth(page, byID, flattenParentDepth)
			if anchor != nil {
				detachChild(byID[page.ParentSourceID], page)
				page.ParentSourceID = anchor.SourceID
				anchor.Children = append(anchor.Children, page)
			} else {
				detachChild(byID[page.ParentSourceID], page)
				page.ParentSourceID = ""
			}

			depth = MaxPageDepth
			flattened++
			warnings = append(warnings, Warning{
				Code:       WarnPageDepthFlattened,
				EntityType: "page",
				SourceID:   page.SourceID,
				Message: TruncateMessage(fmt.Sprintf(
					"source depth %d exceeds the destination maximum of %d; page reparented to %s at depth %d",
					parentDepth+1, MaxPageDepth, flattenAnchorLabel(page), depth)),
			})
		}

		page.Depth = depth
		// Copy: walk mutates Children when a descendant is reparented.
		children := append([]*Page{}, page.Children...)
		for _, child := range children {
			walk(child, page.Depth)
		}
	}

	for _, root := range roots {
		walk(root, 0)
	}
	return warnings, flattened
}

func flattenAnchorLabel(page *Page) string {
	if page.ParentSourceID == "" {
		return "the top level"
	}
	return "page " + page.ParentSourceID
}

// nearestAncestorAtDepth walks up the already-depthed output tree for the
// closest ancestor sitting at exactly the wanted depth.
func nearestAncestorAtDepth(page *Page, byID map[string]*Page, wanted int) *Page {
	for current := byID[page.ParentSourceID]; current != nil; current = byID[current.ParentSourceID] {
		if current.Depth == wanted {
			return current
		}
		if current.ParentSourceID == "" {
			return nil
		}
	}
	return nil
}

func detachChild(parent, child *Page) {
	if parent == nil {
		return
	}
	for i, candidate := range parent.Children {
		if candidate == child {
			parent.Children = append(parent.Children[:i], parent.Children[i+1:]...)
			return
		}
	}
}

// sortSiblings orders pages by source position, then creation time, then source
// ID numerically when it is numeric and lexically otherwise.
//
// A page with no position sorts after every page that has one: Confluence omits
// position on blog posts and on some imported content, and treating that as
// position zero would move it ahead of deliberately ordered siblings.
func sortSiblings(pages []*Page) {
	sort.SliceStable(pages, func(i, j int) bool {
		a, b := pages[i], pages[j]

		if a.HasPosition != b.HasPosition {
			return a.HasPosition
		}
		if a.HasPosition && a.Position != b.Position {
			return a.Position < b.Position
		}
		if a.HasCreatedAt != b.HasCreatedAt {
			return a.HasCreatedAt
		}
		if a.HasCreatedAt && a.CreatedAt != b.CreatedAt {
			return a.CreatedAt < b.CreatedAt
		}
		return lessNumericThenLexical(a.SourceID, b.SourceID)
	})
}

// emissionOrder flattens the tree into the parent-before-child order the bundle
// requires.
func emissionOrder(roots []*Page) []*Page {
	var ordered []*Page

	var walk func(page *Page)
	walk = func(page *Page) {
		ordered = append(ordered, page)
		for _, child := range page.Children {
			walk(child)
		}
	}
	for _, root := range roots {
		walk(root)
	}
	return ordered
}
