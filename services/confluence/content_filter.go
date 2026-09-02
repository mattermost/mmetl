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
	// SpaceSourceID is the selected space, which space-scoped warnings name.
	SpaceSourceID string

	// Pages is parent-first emission order.
	Pages []*Page
	ByID  map[string]*Page

	Warnings []Warning

	// PagesDiscovered counts the canonical pages and blog posts of the selected
	// space, so discovered always equals emitted plus skipped. Counting every
	// Page object in the file instead would report the historical versions and
	// the other spaces' pages as if this export had passed them over.
	PagesDiscovered int

	// ObjectsScanned is every page and blog post object read, for logging.
	ObjectsScanned int

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
	scanned := 0

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

		scanned++
		candidates = append(candidates, contentCandidate(object, space, descriptor.Location))
	}

	pages, err := SelectCanonical(candidates, space.Key)
	if err != nil {
		return nil, err
	}

	selection := &ContentSelection{
		SpaceSourceID:   space.SourceID,
		ByID:            make(map[string]*Page, len(pages)),
		PagesDiscovered: len(pages),
		ObjectsScanned:  scanned,
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

// LabelSelection is the result of the label pass.
type LabelSelection struct {
	// LabelsPreserved counts the page labels attached to emitted pages.
	LabelsPreserved int

	// RestrictedPagesPreserved is always zero in this iteration.
	RestrictedPagesPreserved int

	Warnings []Warning
}

// SelectLabels reads Labelling and Label objects and attaches the labels of
// emitted pages to those pages.
//
// It also emits the standing restriction-fidelity warning. Restrictions are not
// read at all: the discovery sample contains no page-restriction object, only
// space-level SpacePermission rows, and guessing at an undocumented class would
// produce access metadata nobody could trust.
func SelectLabels(archive *SourceArchive, content *ContentSelection, refs *UserRefs) (*LabelSelection, error) {
	labellings, err := readLabellings(archive, content)
	if err != nil {
		return nil, err
	}

	selection := &LabelSelection{}
	if len(labellings) > 0 {
		if err := applyLabels(archive, labellings, content, refs, selection); err != nil {
			return nil, err
		}
	}

	SortWarnings(selection.Warnings)
	return selection, nil
}

// labelling links one label to one emitted page.
type labelling struct {
	labelKey EntityKey
	pageID   string
	ownerKey EntityKey
}

func readLabellings(archive *SourceArchive, content *ContentSelection) ([]labelling, error) {
	entities, err := archive.OpenEntities()
	if err != nil {
		return nil, err
	}
	defer func() { _ = entities.Close() }()

	decoder := NewObjectDecoder(entities)
	decoder.SetAccept(func(class ClassRef) bool { return class == ClassLabelling })

	var labellings []labelling
	for {
		object, err := decoder.Next()
		if errors.Is(err, io.EOF) {
			return labellings, nil
		}
		if err != nil {
			return nil, err
		}

		contentKey, ok := object.Reference(labellingPropContent)
		if !ok || !isContentContainer(contentKey) {
			// Space-description labellings land here. Every label in the
			// discovery sample is one of those, so this is the common case, not
			// an edge case: this iteration carries page labels only.
			continue
		}
		if _, emitted := content.ByID[contentKey.ID]; !emitted {
			continue
		}

		labelKey, ok := object.Reference(labellingPropLabel)
		if !ok {
			continue
		}
		owner, _ := object.Reference(labelPropOwningUser)
		labellings = append(labellings, labelling{labelKey: labelKey, pageID: contentKey.ID, ownerKey: owner})
	}
}

func applyLabels(
	archive *SourceArchive,
	labellings []labelling,
	content *ContentSelection,
	refs *UserRefs,
	selection *LabelSelection,
) error {
	wanted := map[string][]labelling{}
	for _, link := range labellings {
		wanted[link.labelKey.ID] = append(wanted[link.labelKey.ID], link)
	}

	entities, err := archive.OpenEntities()
	if err != nil {
		return err
	}
	defer func() { _ = entities.Close() }()

	decoder := NewObjectDecoder(entities)
	decoder.SetAccept(func(class ClassRef) bool { return class == ClassLabel })

	for {
		object, err := decoder.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}

		links, ok := wanted[object.Key.ID]
		if !ok {
			continue
		}

		name := strings.TrimSpace(object.ScalarValue(labelPropName))
		if name == "" {
			continue
		}
		namespace := strings.TrimSpace(object.ScalarValue(labelPropNamespace))
		labelOwner, _ := object.Reference(labelPropOwningUser)

		for _, link := range links {
			page, emitted := content.ByID[link.pageID]
			if !emitted {
				continue
			}

			owner := link.ownerKey
			if owner.IsZero() {
				owner = labelOwner
			}
			page.Labels = append(page.Labels, Label{Name: name, Namespace: namespace, OwnerKey: owner})
			refs.Add(owner)
			selection.LabelsPreserved++
		}
	}

	for _, page := range content.Pages {
		sortLabels(page.Labels)
	}
	return nil
}

// sortLabels orders labels by namespace then name, and drops exact duplicates,
// so the same source produces the same page props on every run.
func sortLabels(labels []Label) {
	sort.SliceStable(labels, func(i, j int) bool {
		if labels[i].Namespace != labels[j].Namespace {
			return labels[i].Namespace < labels[j].Namespace
		}
		return labels[i].Name < labels[j].Name
	})
}

// LabelNames is the flat label list the bundle carries as import_labels.
//
// Every namespace is included, personal ones too. Section 22 stores labels as
// inert metadata with no UI, search, or access behaviour, and the structured
// list beside it keeps the namespace, so a future consumer can tell a shared
// team label from one person's private favourite. Filtering here would lose
// that distinction rather than record it.
func LabelNames(labels []Label) []string {
	names := make([]string, 0, len(labels))
	seen := map[string]bool{}
	for _, label := range labels {
		if seen[label.Name] {
			continue
		}
		seen[label.Name] = true
		names = append(names, label.Name)
	}
	sort.Strings(names)
	return names
}

// ContentPermissionSet and ContentPermission property names.
const (
	permissionSetPropType     = "type"
	permissionSetPropOwning   = "owningContent"
	permissionSetCollPerms    = "contentPermissions"
	permissionPropUserSubject = "userSubject"
	permissionPropGroupName   = "groupName"
	permissionPropOwningSet   = "owningSet"

	// permissionTypeView and permissionTypeEdit are the two set types
	// Confluence writes.
	permissionTypeView = "view"
	permissionTypeEdit = "edit"
)

// SelectRestrictions reads the page restrictions of the selected space.
//
// Confluence stores a restriction as a ContentPermissionSet naming the page and
// the kind of restriction, holding ContentPermission rows that each name either
// a user or a group. Both objects are buffered before being joined, because
// either can appear first in the stream and a restricted page has only a
// handful of rows.
//
// Restricted users join the user closure: a page restricted to someone is a
// reference to that person, and the manifest has to be able to name them.
func SelectRestrictions(archive *SourceArchive, content *ContentSelection, refs *UserRefs) (int, []Warning, error) {
	entities, err := archive.OpenEntities()
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = entities.Close() }()

	decoder := NewObjectDecoder(entities)
	decoder.SetAccept(func(class ClassRef) bool {
		return class == ClassContentPermissionSet || class == ClassContentPermission
	})

	type permissionSet struct {
		kind   string
		pageID string
	}

	sets := map[string]permissionSet{}
	permissions := map[string][]*RawObject{}

	for {
		object, err := decoder.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return 0, nil, err
		}

		switch {
		case object.Is(ClassContentPermissionSet):
			owning, ok := object.Reference(permissionSetPropOwning)
			if !ok || !isContentContainer(owning) {
				continue
			}
			sets[object.Key.ID] = permissionSet{
				kind:   strings.ToLower(strings.TrimSpace(object.ScalarValue(permissionSetPropType))),
				pageID: owning.ID,
			}

		case object.Is(ClassContentPermission):
			if owningSet, ok := object.Reference(permissionPropOwningSet); ok {
				permissions[owningSet.ID] = append(permissions[owningSet.ID], object)
			}
		}
	}

	var warnings []Warning
	restricted := 0

	for _, setID := range sortedKeysOf(sets) {
		set := sets[setID]
		page, emitted := content.ByID[set.pageID]
		if !emitted {
			continue
		}

		for _, permission := range permissions[setID] {
			userKey, hasUser := permission.Reference(permissionPropUserSubject)
			groupName := strings.TrimSpace(permission.ScalarValue(permissionPropGroupName))

			switch {
			case hasUser:
				refs.Add(userKey)
				switch set.kind {
				case permissionTypeView:
					page.SourceRestrictions.ViewUserKeys = append(page.SourceRestrictions.ViewUserKeys, userKey)
				case permissionTypeEdit:
					page.SourceRestrictions.EditUserKeys = append(page.SourceRestrictions.EditUserKeys, userKey)
				}

			case groupName != "":
				switch set.kind {
				case permissionTypeView:
					page.SourceRestrictions.ViewGroups = append(page.SourceRestrictions.ViewGroups, groupName)
				case permissionTypeEdit:
					page.SourceRestrictions.EditGroups = append(page.SourceRestrictions.EditGroups, groupName)
				}
				// No real group-restricted page has been seen, so a bundle that
				// carries one says so rather than implying it was verified.
				warnings = append(warnings, Warning{
					Code:       WarnRestrictionUnverified,
					EntityType: "page",
					SourceID:   page.SourceID,
					Message: TruncateMessage(fmt.Sprintf(
						"page is restricted to group %q; group restrictions have not been verified against a real export "+
							"and are preserved as metadata only", groupName)),
				})
			}
		}

		if set.kind != permissionTypeView && set.kind != permissionTypeEdit {
			warnings = append(warnings, Warning{
				Code:       WarnRestrictionUnverified,
				EntityType: "page",
				SourceID:   page.SourceID,
				Message: TruncateMessage(fmt.Sprintf(
					"page carries a %q restriction, which this exporter does not recognize; it was not preserved", set.kind)),
			})
		}
	}

	for _, page := range content.Pages {
		page.SourceRestrictions.sort()
		if !page.SourceRestrictions.IsEmpty() {
			restricted++
		}
	}

	SortWarnings(warnings)
	return restricted, warnings, nil
}

func sortedKeysOf[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return lessNumericThenLexical(keys[i], keys[j]) })
	return keys
}
