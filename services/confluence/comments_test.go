package confluence

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

func commentKey(id string) EntityKey {
	return EntityKey{Package: PkgConfluencePages, Class: "Comment", IDName: "id", ID: id}
}

// commentCandidate builds an initially eligible comment on the given page.
func commentCandidate(id, pageID, parentID string, mutate ...func(*CommentCandidate)) CommentCandidate {
	c := CommentCandidate{
		Comment: &Comment{
			Key:          commentKey(id),
			SourceID:     id,
			PageSourceID: pageID,
		},
		ContainerKey: pageKey(pageID),
		Status:       "current",
	}
	if parentID != "" {
		c.ParentKey = commentKey(parentID)
	}
	for _, m := range mutate {
		m(&c)
	}
	return c
}

func withCommentCreatedAt(millis int64) func(*CommentCandidate) {
	return func(c *CommentCandidate) { c.Comment.CreatedAt, c.Comment.HasCreatedAt = millis, true }
}

func TestCommentCandidateIsInitiallyEligible(t *testing.T) {
	emitted := map[string]*Page{"100": {SourceID: "100"}}

	tests := []struct {
		name      string
		candidate CommentCandidate
		want      bool
	}{
		{
			name:      "current comment on an emitted page",
			candidate: commentCandidate("1", "100", ""),
			want:      true,
		},
		{
			name:      "on a page that was not emitted",
			candidate: commentCandidate("1", "999", ""),
		},
		{
			name:      "not current",
			candidate: commentCandidate("1", "100", "", func(c *CommentCandidate) { c.Status = "deleted" }),
		},
		{
			name:      "status is compared case-insensitively",
			candidate: commentCandidate("1", "100", "", func(c *CommentCandidate) { c.Status = "CURRENT" }),
			want:      true,
		},
		{
			name: "a historical version",
			candidate: commentCandidate("1", "100", "", func(c *CommentCandidate) {
				c.HasOriginalVersion = true
			}),
		},
		{
			name: "originalVersionId names another object",
			candidate: commentCandidate("1", "100", "", func(c *CommentCandidate) {
				c.OriginalVersionID = "55"
			}),
		},
		{
			name: "originalVersionId is zero",
			candidate: commentCandidate("1", "100", "", func(c *CommentCandidate) {
				c.OriginalVersionID = "0"
			}),
			want: true,
		},
		{
			name: "container is not page-like",
			candidate: commentCandidate("1", "100", "", func(c *CommentCandidate) {
				c.ContainerKey = descriptionKey("100")
			}),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.want, test.candidate.IsInitiallyEligible(emitted))
		})
	}
}

func TestBuildCommentThreads(t *testing.T) {
	pages := []*Page{{SourceID: "100"}, {SourceID: "200"}}

	t.Run("thread root is the top-level comment, not the immediate parent", func(t *testing.T) {
		comments, warnings := BuildCommentThreads([]CommentCandidate{
			commentCandidate("1", "100", "", withCommentCreatedAt(10)),
			commentCandidate("2", "100", "1", withCommentCreatedAt(20)),
			commentCandidate("3", "100", "2", withCommentCreatedAt(30)),
		}, pages)

		require.Empty(t, warnings)
		require.Equal(t, []string{"1", "2", "3"}, commentIDs(comments))

		require.Equal(t, "1", comments[0].ThreadRootSourceID)
		require.Empty(t, comments[0].ParentSourceID)

		// Mattermost threads are flat: the grandchild's root is the top-level
		// comment, while its immediate parent stays in the mapping.
		require.Equal(t, "1", comments[2].ThreadRootSourceID)
		require.Equal(t, "2", comments[2].ParentSourceID)
	})

	t.Run("ordering", func(t *testing.T) {
		comments, _ := BuildCommentThreads([]CommentCandidate{
			commentCandidate("30", "200", "", withCommentCreatedAt(1)),
			commentCandidate("20", "100", "", withCommentCreatedAt(50)),
			commentCandidate("10", "100", "", withCommentCreatedAt(10)),
			commentCandidate("11", "100", "10", withCommentCreatedAt(999)),
		}, pages)

		// Page 100 comes first because pages are in emission order, even though
		// page 200's comment is the oldest. Within the page, roots follow
		// creation time and a reply follows its own root.
		require.Equal(t, []string{"10", "11", "20", "30"}, commentIDs(comments))
	})

	t.Run("siblings tie-break on numeric source id", func(t *testing.T) {
		comments, _ := BuildCommentThreads([]CommentCandidate{
			commentCandidate("100", "100", "", withCommentCreatedAt(5)),
			commentCandidate("20", "100", "", withCommentCreatedAt(5)),
			commentCandidate("3", "100", "", withCommentCreatedAt(5)),
		}, pages)
		require.Equal(t, []string{"3", "20", "100"}, commentIDs(comments))
	})
}

func TestBuildCommentThreads_SkipsBrokenThreads(t *testing.T) {
	pages := []*Page{{SourceID: "100"}, {SourceID: "200"}}

	t.Run("parent was never emitted", func(t *testing.T) {
		comments, warnings := BuildCommentThreads([]CommentCandidate{
			commentCandidate("1", "100", "", withCommentCreatedAt(1)),
			commentCandidate("2", "100", "999", withCommentCreatedAt(2)),
		}, pages)

		require.Equal(t, []string{"1"}, commentIDs(comments))
		require.Len(t, warnings, 1)
		require.Equal(t, WarnCommentParentMissingSkip, warnings[0].Code)
		require.Equal(t, "2", warnings[0].SourceID)
		require.Contains(t, warnings[0].Message, "999")
	})

	t.Run("parent was excluded by the predicate", func(t *testing.T) {
		comments, warnings := BuildCommentThreads([]CommentCandidate{
			commentCandidate("1", "100", "", func(c *CommentCandidate) { c.Status = "deleted" }),
			commentCandidate("2", "100", "1"),
		}, pages)

		require.Empty(t, comments)
		require.Len(t, warnings, 1)
		require.Equal(t, "2", warnings[0].SourceID)
	})

	// A reply whose parent lives on another page has no correct destination:
	// putting it on either page would invent a conversation that never happened.
	t.Run("parent is on another page", func(t *testing.T) {
		comments, warnings := BuildCommentThreads([]CommentCandidate{
			commentCandidate("1", "200", ""),
			commentCandidate("2", "100", "1"),
		}, pages)

		require.Equal(t, []string{"1"}, commentIDs(comments))
		require.Len(t, warnings, 1)
		require.Contains(t, warnings[0].Message, "is on page 200, not 100")
	})

	t.Run("a cycle skips the whole thread", func(t *testing.T) {
		comments, warnings := BuildCommentThreads([]CommentCandidate{
			commentCandidate("1", "100", "2"),
			commentCandidate("2", "100", "1"),
		}, pages)

		require.Empty(t, comments)
		require.NotEmpty(t, warnings)
		require.Contains(t, warnings[0].Message, "cycle")
	})

	// A broken thread of many replies is one editorial problem. One warning per
	// descendant would bury it.
	t.Run("descendants are reported in aggregate, not one by one", func(t *testing.T) {
		candidates := []CommentCandidate{
			commentCandidate("2", "100", "999", withCommentCreatedAt(1)),
		}
		for i := 3; i <= 12; i++ {
			candidates = append(candidates, commentCandidate(strconv.Itoa(i), "100", strconv.Itoa(i-1), withCommentCreatedAt(int64(i))))
		}

		comments, warnings := BuildCommentThreads(candidates, pages)
		require.Empty(t, comments, "the whole broken thread is dropped")

		require.Len(t, warnings, 2, "one root cause plus one aggregate, not eleven")
		require.Equal(t, WarnCommentParentMissingSkip, warnings[0].Code)
		require.Equal(t, "2", warnings[0].SourceID)
		require.Equal(t, WarnCommentAncestorSkipped, warnings[1].Code)
		require.Contains(t, warnings[1].Message, "10 further comment(s)")
	})

	t.Run("a broken sibling does not take a healthy thread with it", func(t *testing.T) {
		comments, warnings := BuildCommentThreads([]CommentCandidate{
			commentCandidate("1", "100", "", withCommentCreatedAt(1)),
			commentCandidate("2", "100", "1", withCommentCreatedAt(2)),
			commentCandidate("3", "100", "999", withCommentCreatedAt(3)),
		}, pages)

		require.Equal(t, []string{"1", "2"}, commentIDs(comments))
		require.Len(t, warnings, 1)
		require.Equal(t, "3", warnings[0].SourceID)
	})
}

func TestBuildCommentThreads_Empty(t *testing.T) {
	comments, warnings := BuildCommentThreads(nil, []*Page{{SourceID: "100"}})
	require.Empty(t, comments)
	require.Empty(t, warnings)
}

func commentIDs(comments []*Comment) []string {
	ids := make([]string, 0, len(comments))
	for _, comment := range comments {
		ids = append(ids, comment.SourceID)
	}
	return ids
}
