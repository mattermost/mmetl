package confluence

import (
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

// ContentCandidate is a page or blog post as read from the source, before the
// canonical predicate has been applied to it.
//
// The predicate needs facts from other objects, so candidates are collected
// first and filtered afterwards. Only metadata is retained: on a large export
// there are many candidates and none of them holds a body.
type ContentCandidate struct {
	Page *Page

	Status string

	HasOriginalVersion bool
	OriginalVersionID  string

	HistoricalVersions []EntityKey
}

// LogicalID groups the versions of one logical piece of content.
//
// A historical version points at the object it is a version of, so all versions
// of a page share one logical ID and the canonical object's own ID is that ID.
func (c ContentCandidate) LogicalID() string {
	if !isAbsentOrZeroID(c.OriginalVersionID) {
		return c.OriginalVersionID
	}
	return c.Page.SourceID
}

// IsCanonicalCandidate applies the four conditions of the canonical predicate
// that depend only on the object itself.
//
// The fifth condition, that no canonical object lists this one as a historical
// version, needs the whole candidate set and is applied by SelectCanonical.
func (c ContentCandidate) IsCanonicalCandidate(selectedSpace EntityKey) bool {
	return c.Page.SpaceKey == selectedSpace &&
		strings.EqualFold(c.Status, contentStatusCurrent) &&
		!c.HasOriginalVersion &&
		isAbsentOrZeroID(c.OriginalVersionID)
}

// SelectCanonical applies the full canonical predicate to a candidate set.
//
// Historical membership is resolved against the objects that already satisfy
// the other four conditions. Resolving it against every object instead would be
// circular: an object is excluded because a canonical object lists it, and
// canonicity is what is being decided. In a well-formed export the two agree,
// because a historical version always carries its own originalVersion marker
// as well.
func SelectCanonical(candidates []ContentCandidate, selectedSpace EntityKey) ([]*Page, error) {
	preliminary := make([]ContentCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.IsCanonicalCandidate(selectedSpace) {
			preliminary = append(preliminary, candidate)
		}
	}

	historical := map[string]bool{}
	for _, candidate := range preliminary {
		for _, version := range candidate.HistoricalVersions {
			historical[version.ID] = true
		}
	}

	byLogicalID := map[string][]ContentCandidate{}
	pages := make([]*Page, 0, len(preliminary))
	for _, candidate := range preliminary {
		if historical[candidate.Page.SourceID] {
			continue
		}
		byLogicalID[candidate.LogicalID()] = append(byLogicalID[candidate.LogicalID()], candidate)
		pages = append(pages, candidate.Page)
	}

	if err := checkSingleCanonical(byLogicalID); err != nil {
		return nil, err
	}
	return pages, nil
}

// checkSingleCanonical reports two objects claiming to be the current version
// of one logical page. Picking either would silently drop the other's content
// and make the export non-reproducible, so it is a hard error.
func checkSingleCanonical(byLogicalID map[string][]ContentCandidate) error {
	logicalIDs := make([]string, 0, len(byLogicalID))
	for logicalID := range byLogicalID {
		logicalIDs = append(logicalIDs, logicalID)
	}
	sort.Strings(logicalIDs)

	for _, logicalID := range logicalIDs {
		group := byLogicalID[logicalID]
		if len(group) < 2 {
			continue
		}
		ids := make([]string, 0, len(group))
		for _, candidate := range group {
			ids = append(ids, candidate.Page.SourceID)
		}
		sort.Strings(ids)
		return fmt.Errorf("%s: %d objects claim to be the current version of content %s: %s",
			ErrCodeContentDuplicateCanonical, len(group), logicalID, strings.Join(ids, ", "))
	}
	return nil
}

// ContentSelection is the result of pass 2: the canonical pages of the selected
// space, in emission order, with the hierarchy already resolved.
type ContentSelection struct {
	// Pages is parent-first emission order.
	Pages []*Page
	ByID  map[string]*Page

	Warnings []Warning

	PagesDiscovered  int
	PagesEmitted     int
	BlogPostsEmitted int
	PagesFlattened   int
}

// SelectPageMetadata performs pass 2: it reads Page and BlogPost metadata,
// selects the canonical current objects of the selected space, and resolves the
// output hierarchy.
//
// Bodies and content properties are referenced, never read. A later pass opens
// the ones the selected pages actually need.
func SelectPageMetadata(archive *SourceArchive, space Space, descriptor Descriptor) (*ContentSelection, error) {
	entities, openErr := archive.OpenEntities()
	if openErr != nil {
		return nil, openErr
	}
	defer func() { _ = entities.Close() }()

	decoder := NewObjectDecoder(entities)
	decoder.SetAccept(func(class ClassRef) bool {
		return class == ClassPage || class == ClassBlogPost
	})

	index := newKeyIndex()
	var candidates []ContentCandidate
	discovered := 0

	for {
		object, err := decoder.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if err := index.add(object); err != nil {
			return nil, err
		}

		discovered++
		candidates = append(candidates, contentCandidate(object, space, descriptor.Location))
	}

	pages, err := SelectCanonical(candidates, space.Key)
	if err != nil {
		return nil, err
	}

	selection := &ContentSelection{
		ByID:            make(map[string]*Page, len(pages)),
		PagesDiscovered: discovered,
	}
	for _, page := range pages {
		selection.ByID[page.SourceID] = page
	}

	ordered, warnings, flattened, err := BuildHierarchy(pages)
	if err != nil {
		return nil, err
	}

	selection.Pages = ordered
	selection.Warnings = warnings
	selection.PagesFlattened = flattened
	for _, page := range ordered {
		if page.IsBlogPost() {
			selection.BlogPostsEmitted++
		} else {
			selection.PagesEmitted++
		}
	}

	SortWarnings(selection.Warnings)
	return selection, nil
}

func contentCandidate(object *RawObject, space Space, loc *time.Location) ContentCandidate {
	contentType := ContentTypePage
	if object.Is(ClassBlogPost) {
		contentType = ContentTypeBlogPost
	}

	page := &Page{
		Key:                 object.Key,
		SourceID:            object.Key.ID,
		ContentType:         contentType,
		Title:               object.ScalarValue(contentPropTitle),
		BodyContentKeys:     object.Collection(contentCollBodyContents),
		ContentPropertyKeys: object.Collection(contentCollContentProperties),
	}
	page.SpaceKey, _ = object.Reference(contentPropSpace)
	page.SourceParentKey, _ = object.Reference(contentPropParent)
	page.CreatorKey, _ = object.Reference(contentPropCreator)
	page.LastModifierKey, _ = object.Reference(contentPropLastModifier)
	page.CreatedAt, page.HasCreatedAt = SourceTimeMillis(object.ScalarValue(contentPropCreationDate), loc)
	page.UpdatedAt, page.HasUpdatedAt = SourceTimeMillis(object.ScalarValue(contentPropLastModification), loc)
	page.Position, page.HasPosition = parseSourcePosition(object.ScalarValue(contentPropPosition))

	originalVersionID, present := object.Scalar(contentPropOriginalVersionID)
	_, hasOriginalVersion := object.Reference(contentPropOriginalVersion)

	candidate := ContentCandidate{
		Page:               page,
		Status:             object.ScalarValue(contentPropStatus),
		HasOriginalVersion: hasOriginalVersion,
		HistoricalVersions: object.Collection(contentCollHistoricalVersions),
	}
	if present {
		candidate.OriginalVersionID = originalVersionID
	}
	return candidate
}

// CommentCandidate is a comment as read from the source, before eligibility and
// thread resolution.
type CommentCandidate struct {
	Comment *Comment

	ContainerKey EntityKey
	ParentKey    EntityKey

	Status             string
	HasOriginalVersion bool
	OriginalVersionID  string
}

// IsInitiallyEligible applies the four self-contained conditions of the section
// 6.2 comment predicate. Whether the comment survives also depends on its
// ancestors, which BuildCommentThreads decides.
func (c CommentCandidate) IsInitiallyEligible(emittedPages map[string]*Page) bool {
	if !strings.EqualFold(c.Status, contentStatusCurrent) {
		return false
	}
	if c.HasOriginalVersion || !isAbsentOrZeroID(c.OriginalVersionID) {
		return false
	}
	if !isContentContainer(c.ContainerKey) {
		return false
	}
	_, emitted := emittedPages[c.ContainerKey.ID]
	return emitted
}

func commentFromObject(object *RawObject, loc *time.Location) CommentCandidate {
	comment := &Comment{
		Key:                 object.Key,
		SourceID:            object.Key.ID,
		BodyContentKeys:     object.Collection(contentCollBodyContents),
		ContentPropertyKeys: object.Collection(contentCollContentProperties),
	}
	comment.CreatorKey, _ = object.Reference(contentPropCreator)
	comment.LastModifierKey, _ = object.Reference(contentPropLastModifier)
	comment.CreatedAt, comment.HasCreatedAt = SourceTimeMillis(object.ScalarValue(contentPropCreationDate), loc)
	comment.UpdatedAt, comment.HasUpdatedAt = SourceTimeMillis(object.ScalarValue(contentPropLastModification), loc)

	candidate := CommentCandidate{
		Comment: comment,
		Status:  object.ScalarValue(contentPropStatus),
	}
	candidate.ContainerKey, _ = object.Reference(contentPropContainerContent)
	candidate.ParentKey, _ = object.Reference(contentPropParent)
	comment.PageSourceID = candidate.ContainerKey.ID

	originalVersionID, present := object.Scalar(contentPropOriginalVersionID)
	if present {
		candidate.OriginalVersionID = originalVersionID
	}
	_, candidate.HasOriginalVersion = object.Reference(contentPropOriginalVersion)

	return candidate
}

// commentSkip records why one comment was dropped and how many descendants went
// with it.
type commentSkip struct {
	sourceID    string
	reason      string
	descendants int
}

// BuildCommentThreads applies the comment predicate and resolves the thread
// graph, returning the surviving comments in emission order.
//
// A comment whose parent is missing, excluded, on another page, or part of a
// cycle is dropped along with everything below it: importing a reply whose
// parent never arrived would attach it to the wrong thread or to none.
//
// Warnings are one per root cause, not one per descendant. A broken thread of
// fifty replies is one editorial problem, and fifty warnings would bury it.
func BuildCommentThreads(candidates []CommentCandidate, pageOrder []*Page) (comments []*Comment, warnings []Warning) {
	eligiblePages := make(map[string]*Page, len(pageOrder))
	for _, page := range pageOrder {
		eligiblePages[page.SourceID] = page
	}

	eligible := map[string]CommentCandidate{}
	var eligibleOrder []string
	for _, candidate := range candidates {
		if candidate.IsInitiallyEligible(eligiblePages) {
			eligible[candidate.Comment.SourceID] = candidate
			eligibleOrder = append(eligibleOrder, candidate.Comment.SourceID)
		}
	}

	resolver := &commentResolver{eligible: eligible, state: map[string]*commentResolution{}}
	for _, id := range eligibleOrder {
		resolver.resolve(id, map[string]bool{})
	}

	kept := make([]*Comment, 0, len(eligibleOrder))
	for _, id := range eligibleOrder {
		resolution := resolver.state[id]
		if !resolution.keep {
			continue
		}
		comment := eligible[id].Comment
		comment.ParentSourceID = resolution.parentID
		comment.ThreadRootSourceID = resolution.threadRootID
		kept = append(kept, comment)
	}

	return orderComments(kept, pageOrder), resolver.warnings()
}

type commentResolution struct {
	keep         bool
	parentID     string
	threadRootID string

	// rootCauseID names the comment that actually broke, which is this comment
	// when it is the root cause and an ancestor otherwise.
	rootCauseID string
	reason      string
}

type commentResolver struct {
	eligible map[string]CommentCandidate
	state    map[string]*commentResolution
	skips    []commentSkip
	skipByID map[string]int
}

func (r *commentResolver) resolve(id string, visiting map[string]bool) *commentResolution {
	if existing, ok := r.state[id]; ok {
		return existing
	}

	candidate := r.eligible[id]
	parentID := candidate.ParentKey.ID

	switch {
	case candidate.ParentKey.IsZero():
		return r.keep(id, "", id)

	case visiting[id]:
		return r.skipRootCause(id, fmt.Sprintf("comment %s is part of a parent cycle", id))
	}

	parent, parentEligible := r.eligible[parentID]
	switch {
	case !parentEligible:
		return r.skipRootCause(id, fmt.Sprintf("parent comment %s was not emitted", parentID))

	case parent.Comment.PageSourceID != candidate.Comment.PageSourceID:
		return r.skipRootCause(id, fmt.Sprintf(
			"parent comment %s is on page %s, not %s",
			parentID, parent.Comment.PageSourceID, candidate.Comment.PageSourceID))
	}

	visiting[id] = true
	parentResolution := r.resolve(parentID, visiting)
	delete(visiting, id)

	if !parentResolution.keep {
		return r.skipDescendant(id, parentResolution)
	}
	return r.keep(id, parentID, parentResolution.threadRootID)
}

func (r *commentResolver) keep(id, parentID, threadRootID string) *commentResolution {
	resolution := &commentResolution{keep: true, parentID: parentID, threadRootID: threadRootID}
	r.state[id] = resolution
	return resolution
}

func (r *commentResolver) skipRootCause(id, reason string) *commentResolution {
	resolution := &commentResolution{rootCauseID: id, reason: reason}
	r.state[id] = resolution

	if r.skipByID == nil {
		r.skipByID = map[string]int{}
	}
	r.skipByID[id] = len(r.skips)
	r.skips = append(r.skips, commentSkip{sourceID: id, reason: reason})
	return resolution
}

func (r *commentResolver) skipDescendant(id string, parent *commentResolution) *commentResolution {
	resolution := &commentResolution{rootCauseID: parent.rootCauseID, reason: parent.reason}
	r.state[id] = resolution

	if index, ok := r.skipByID[parent.rootCauseID]; ok {
		r.skips[index].descendants++
	}
	return resolution
}

func (r *commentResolver) warnings() []Warning {
	var warnings []Warning
	for _, skip := range r.skips {
		warnings = append(warnings, Warning{
			Code:       WarnCommentParentMissingSkip,
			EntityType: "comment",
			SourceID:   skip.sourceID,
			Message:    TruncateMessage(skip.reason + "; comment skipped"),
		})
		if skip.descendants > 0 {
			warnings = append(warnings, Warning{
				Code:       WarnCommentAncestorSkipped,
				EntityType: "comment",
				SourceID:   skip.sourceID,
				Message: TruncateMessage(fmt.Sprintf(
					"%d further comment(s) below skipped comment %s were skipped with it",
					skip.descendants, skip.sourceID)),
			})
		}
	}
	return warnings
}

// orderComments groups comments by their page in page emission order, then
// emits each thread root before its descendants, as the bundle requires.
func orderComments(comments []*Comment, pageOrder []*Page) []*Comment {
	byPage := map[string][]*Comment{}
	for _, comment := range comments {
		byPage[comment.PageSourceID] = append(byPage[comment.PageSourceID], comment)
	}

	ordered := make([]*Comment, 0, len(comments))
	for _, page := range pageOrder {
		pageComments := byPage[page.SourceID]
		if len(pageComments) == 0 {
			continue
		}

		children := map[string][]*Comment{}
		var roots []*Comment
		for _, comment := range pageComments {
			if comment.ParentSourceID == "" {
				roots = append(roots, comment)
				continue
			}
			children[comment.ParentSourceID] = append(children[comment.ParentSourceID], comment)
		}

		sortComments(roots)
		for _, siblings := range children {
			sortComments(siblings)
		}

		var walk func(comment *Comment)
		walk = func(comment *Comment) {
			ordered = append(ordered, comment)
			for _, child := range children[comment.SourceID] {
				walk(child)
			}
		}
		for _, root := range roots {
			walk(root)
		}
	}
	return ordered
}

// sortComments orders comments by creation time, then by source ID numerically
// when it is numeric. Confluence carries no explicit comment position.
func sortComments(comments []*Comment) {
	sort.SliceStable(comments, func(i, j int) bool {
		a, b := comments[i], comments[j]
		if a.HasCreatedAt != b.HasCreatedAt {
			return a.HasCreatedAt
		}
		if a.HasCreatedAt && a.CreatedAt != b.CreatedAt {
			return a.CreatedAt < b.CreatedAt
		}
		return lessNumericThenLexical(a.SourceID, b.SourceID)
	})
}
