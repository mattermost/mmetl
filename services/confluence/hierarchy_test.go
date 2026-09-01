package confluence

import (
	"fmt"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

// hierPage builds a page with a declared parent, or a root when parentID is "".
func hierPage(id, parentID string, mutate ...func(*Page)) *Page {
	page := &Page{
		Key:         EntityKey{Package: PkgConfluencePages, Class: "Page", IDName: "id", ID: id},
		SourceID:    id,
		ContentType: ContentTypePage,
		Title:       "Page " + id,
	}
	if parentID != "" {
		page.SourceParentKey = EntityKey{Package: PkgConfluencePages, Class: "Page", IDName: "id", ID: parentID}
	}
	for _, m := range mutate {
		m(page)
	}
	return page
}

func withPosition(position int64) func(*Page) {
	return func(p *Page) { p.Position, p.HasPosition = position, true }
}

func withCreatedAt(millis int64) func(*Page) {
	return func(p *Page) { p.CreatedAt, p.HasCreatedAt = millis, true }
}

func TestBuildHierarchy_ParentFirstOrder(t *testing.T) {
	pages := []*Page{
		hierPage("3", "1"),
		hierPage("1", ""),
		hierPage("4", "3"),
		hierPage("2", "1"),
	}

	ordered, warnings, flattened, err := BuildHierarchy(pages)
	require.NoError(t, err)
	require.Empty(t, warnings)
	require.Zero(t, flattened)

	require.Equal(t, []string{"1", "2", "3", "4"}, pageIDs(ordered))
	require.Equal(t, []int{1, 2, 2, 3}, pageDepths(ordered))
}

func TestBuildHierarchy_SiblingOrdering(t *testing.T) {
	t.Run("position wins", func(t *testing.T) {
		pages := []*Page{
			hierPage("1", ""),
			hierPage("20", "1", withPosition(300), withCreatedAt(1)),
			hierPage("10", "1", withPosition(100), withCreatedAt(9)),
			hierPage("30", "1", withPosition(200), withCreatedAt(5)),
		}
		ordered, _, _, err := BuildHierarchy(pages)
		require.NoError(t, err)
		require.Equal(t, []string{"1", "10", "30", "20"}, pageIDs(ordered))
	})

	t.Run("creation time breaks a position tie", func(t *testing.T) {
		pages := []*Page{
			hierPage("1", ""),
			hierPage("20", "1", withPosition(100), withCreatedAt(900)),
			hierPage("10", "1", withPosition(100), withCreatedAt(100)),
		}
		ordered, _, _, err := BuildHierarchy(pages)
		require.NoError(t, err)
		require.Equal(t, []string{"1", "10", "20"}, pageIDs(ordered))
	})

	t.Run("numeric source id breaks the remaining tie", func(t *testing.T) {
		pages := []*Page{
			hierPage("1", ""),
			hierPage("100", "1", withPosition(1), withCreatedAt(1)),
			hierPage("20", "1", withPosition(1), withCreatedAt(1)),
			hierPage("3", "1", withPosition(1), withCreatedAt(1)),
		}
		ordered, _, _, err := BuildHierarchy(pages)
		require.NoError(t, err)
		require.Equal(t, []string{"1", "3", "20", "100"}, pageIDs(ordered),
			"numeric ids sort numerically, not lexically")
	})

	// Confluence omits position on blog posts and on some imported content.
	// Treating that as position zero would move it ahead of siblings that were
	// deliberately ordered.
	t.Run("a page with no position sorts after every positioned sibling", func(t *testing.T) {
		pages := []*Page{
			hierPage("1", ""),
			hierPage("2", "1"),
			hierPage("3", "1", withPosition(9000)),
		}
		ordered, _, _, err := BuildHierarchy(pages)
		require.NoError(t, err)
		require.Equal(t, []string{"1", "3", "2"}, pageIDs(ordered))
	})
}

func TestBuildHierarchy_PromotesPagesWithMissingParents(t *testing.T) {
	pages := []*Page{
		hierPage("1", "999"),
		hierPage("2", "1"),
	}

	ordered, warnings, _, err := BuildHierarchy(pages)
	require.NoError(t, err)

	require.Equal(t, []string{"1", "2"}, pageIDs(ordered))
	require.Equal(t, []int{1, 2}, pageDepths(ordered), "the promoted page's subtree comes with it")
	require.Empty(t, ordered[0].ParentSourceID)

	require.Len(t, warnings, 1)
	require.Equal(t, WarnPageParentMissingPromoted, warnings[0].Code)
	require.Equal(t, "1", warnings[0].SourceID)
}

func TestBuildHierarchy_RejectsCycles(t *testing.T) {
	t.Run("two-page cycle", func(t *testing.T) {
		_, _, _, err := BuildHierarchy([]*Page{
			hierPage("1", "2"),
			hierPage("2", "1"),
		})
		require.ErrorContains(t, err, "cycle")
	})

	t.Run("longer cycle", func(t *testing.T) {
		_, _, _, err := BuildHierarchy([]*Page{
			hierPage("1", "3"),
			hierPage("2", "1"),
			hierPage("3", "2"),
		})
		require.ErrorContains(t, err, "cycle")
	})

	t.Run("self parent", func(t *testing.T) {
		_, _, _, err := BuildHierarchy([]*Page{hierPage("1", "1")})
		require.ErrorContains(t, err, "cycle")
	})

	t.Run("a cycle hanging off a valid tree is still rejected", func(t *testing.T) {
		_, _, _, err := BuildHierarchy([]*Page{
			hierPage("1", ""),
			hierPage("2", "1"),
			hierPage("3", "4"),
			hierPage("4", "3"),
		})
		require.ErrorContains(t, err, "cycle")
	})
}

// TestBuildHierarchy_Flattening covers the destination depth limit. Docs allows
// 10 levels with the root at 1, so an 11th level has to be reparented.
func TestBuildHierarchy_Flattening(t *testing.T) {
	t.Run("a chain exactly at the limit is untouched", func(t *testing.T) {
		pages := chain(MaxPageDepth)

		ordered, warnings, flattened, err := BuildHierarchy(pages)
		require.NoError(t, err)
		require.Empty(t, warnings)
		require.Zero(t, flattened)
		require.Equal(t, MaxPageDepth, ordered[len(ordered)-1].Depth)
	})

	t.Run("one level too deep is reparented to depth 9", func(t *testing.T) {
		pages := chain(MaxPageDepth + 1)

		ordered, warnings, flattened, err := BuildHierarchy(pages)
		require.NoError(t, err)
		require.Equal(t, 1, flattened)
		require.Len(t, warnings, 1)
		require.Equal(t, WarnPageDepthFlattened, warnings[0].Code)
		require.Equal(t, "p11", warnings[0].SourceID)

		deepest := ordered[len(ordered)-1]
		require.Equal(t, "p11", deepest.SourceID)
		require.Equal(t, MaxPageDepth, deepest.Depth)
		require.Equal(t, "p9", deepest.ParentSourceID, "reparented under the nearest ancestor at depth 9")
	})

	// A long over-deep chain collapses into siblings at the limit rather than
	// each page pushing the next one further past it.
	t.Run("a much deeper chain collapses into siblings at the limit", func(t *testing.T) {
		pages := chain(MaxPageDepth + 5)

		ordered, warnings, flattened, err := BuildHierarchy(pages)
		require.NoError(t, err)
		require.Equal(t, 5, flattened)
		require.Len(t, warnings, 5)

		anchor := findPage(t, ordered, "p9")
		require.Equal(t, flattenParentDepth, anchor.Depth)

		for i := MaxPageDepth + 1; i <= MaxPageDepth+5; i++ {
			page := findPage(t, ordered, fmt.Sprintf("p%d", i))
			require.Equal(t, MaxPageDepth, page.Depth, page.SourceID)
			require.Equal(t, "p9", page.ParentSourceID, page.SourceID)
		}

		require.Len(t, ordered, MaxPageDepth+5, "no page is lost while flattening")
		require.Equal(t, []string{"p10", "p11", "p12", "p13", "p14", "p15"}, pageIDs(anchor.Children))
	})

	t.Run("a subtree hanging below the limit is flattened with its parent", func(t *testing.T) {
		pages := chain(MaxPageDepth + 1)
		// Two extra children under the over-deep page.
		pages = append(pages,
			hierPage("extraA", "p11", withPosition(2)),
			hierPage("extraB", "p11", withPosition(1)),
		)

		ordered, _, flattened, err := BuildHierarchy(pages)
		require.NoError(t, err)
		require.Equal(t, 3, flattened)

		// Flattened pages join the anchor's children and are re-sorted with
		// them by source position: extraB(1), extraA(2), p10(10), p11(11).
		anchor := findPage(t, ordered, "p9")
		require.Equal(t, []string{"extraB", "extraA", "p10", "p11"}, pageIDs(anchor.Children))

		for _, id := range []string{"p11", "extraA", "extraB"} {
			require.Equal(t, MaxPageDepth, findPage(t, ordered, id).Depth, id)
		}
	})

	t.Run("every page still appears exactly once", func(t *testing.T) {
		pages := chain(MaxPageDepth + 12)

		ordered, _, _, err := BuildHierarchy(pages)
		require.NoError(t, err)
		require.Len(t, ordered, MaxPageDepth+12)

		seen := map[string]bool{}
		for _, page := range ordered {
			require.False(t, seen[page.SourceID], page.SourceID)
			seen[page.SourceID] = true
			require.LessOrEqual(t, page.Depth, MaxPageDepth)
			require.GreaterOrEqual(t, page.Depth, 1)
		}
	})
}

// chain builds p1 -> p2 -> ... -> pN, each positioned so sibling order is
// deterministic.
func chain(length int) []*Page {
	pages := make([]*Page, 0, length)
	for i := 1; i <= length; i++ {
		parent := ""
		if i > 1 {
			parent = "p" + strconv.Itoa(i-1)
		}
		pages = append(pages, hierPage("p"+strconv.Itoa(i), parent, withPosition(int64(i))))
	}
	return pages
}

func findPage(t *testing.T, pages []*Page, id string) *Page {
	t.Helper()

	for _, page := range pages {
		if page.SourceID == id {
			return page
		}
	}
	t.Fatalf("page %q not found", id)
	return nil
}

func pageDepths(pages []*Page) []int {
	depths := make([]int, 0, len(pages))
	for _, page := range pages {
		depths = append(depths, page.Depth)
	}
	return depths
}

func TestBuildHierarchy_Empty(t *testing.T) {
	ordered, warnings, flattened, err := BuildHierarchy(nil)
	require.NoError(t, err)
	require.Empty(t, ordered)
	require.Empty(t, warnings)
	require.Zero(t, flattened)
}
