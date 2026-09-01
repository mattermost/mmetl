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
