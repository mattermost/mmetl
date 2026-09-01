package confluence

import (
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
)

// AttachmentCandidate is an attachment as read from the source, before the
// eligibility predicate has been applied.
type AttachmentCandidate struct {
	Attachment *Attachment

	Status             string
	HasOriginalVersion bool
}

// IsEligible applies the section 6.3 attachment predicate.
//
// Unlike the page predicate this deliberately does not test originalVersionId:
// section 6.3 lists only the originalVersion reference, and adding the stricter
// test would silently drop attachments in exports that spell the marker the
// other way.
//
// A space-description attachment is eligible even though its container is not a
// page. It is remapped onto the space's home page by resolveDestination.
func (c AttachmentCandidate) IsEligible(emittedPages map[string]*Page, spaceDescription EntityKey) bool {
	if !strings.EqualFold(c.Status, contentStatusCurrent) || c.HasOriginalVersion {
		return false
	}
	if !spaceDescription.IsZero() && c.Attachment.ContainerKey == spaceDescription {
		return true
	}
	_, onEmittedPage := emittedPages[c.Attachment.ContainerKey.ID]
	return onEmittedPage && isContentContainer(c.Attachment.ContainerKey)
}

// isContentContainer reports whether a container key names a page or blog post,
// so an id that merely collides with an emitted page id cannot pull an
// unrelated attachment in.
func isContentContainer(key EntityKey) bool {
	return ClassPage.Matches(key) || ClassBlogPost.Matches(key)
}

// attachmentFromObject reads an attachment's metadata. Size and media type live
// in separate ContentProperty objects and are filled in later by
// ResolveAttachmentProperties.
func attachmentFromObject(object *RawObject, loc *time.Location) AttachmentCandidate {
	attachment := &Attachment{
		Key:                 object.Key,
		SourceID:            object.Key.ID,
		Filename:            object.ScalarValue(contentPropTitle),
		ContentPropertyKeys: object.Collection(contentCollContentProperties),
	}
	attachment.ContainerKey, _ = object.Reference(contentPropContainerContent)
	attachment.ContainerSourceID = attachment.ContainerKey.ID
	attachment.CreatorKey, _ = object.Reference(contentPropCreator)
	attachment.LastModifierKey, _ = object.Reference(contentPropLastModifier)
	attachment.CreatedAt, attachment.HasCreatedAt = SourceTimeMillis(object.ScalarValue(contentPropCreationDate), loc)
	attachment.UpdatedAt, attachment.HasUpdatedAt = SourceTimeMillis(object.ScalarValue(contentPropLastModification), loc)

	if version, err := strconv.Atoi(object.ScalarValue(contentPropVersion)); err == nil {
		attachment.Version = version
	}

	_, hasOriginalVersion := object.Reference(contentPropOriginalVersion)
	return AttachmentCandidate{
		Attachment:         attachment,
		Status:             object.ScalarValue(contentPropStatus),
		HasOriginalVersion: hasOriginalVersion,
	}
}

// resolveDestination decides which emitted page an eligible attachment is
// listed on, or reports why it cannot be emitted.
//
// A space-description attachment has no page of its own, so it is assigned to
// the space's home page. When that page was not emitted there is nowhere to put
// it, and it is skipped with a warning rather than silently dropped.
func resolveDestination(attachment *Attachment, space Space, emittedPages map[string]*Page) (string, *Warning) {
	if !space.DescriptionKey.IsZero() && attachment.ContainerKey == space.DescriptionKey {
		if !space.HasHomePage() {
			return "", &Warning{
				Code:       WarnAttachmentHomePageMissing,
				EntityType: "attachment",
				SourceID:   attachment.SourceID,
				Message:    "space description attachment has no home page to attach to; attachment skipped",
			}
		}
		homePageID := space.HomePageKey.ID
		if _, emitted := emittedPages[homePageID]; !emitted {
			return "", &Warning{
				Code:       WarnAttachmentHomePageMissing,
				EntityType: "attachment",
				SourceID:   attachment.SourceID,
				Message: TruncateMessage(fmt.Sprintf(
					"space description attachment maps to home page %s, which was not emitted; attachment skipped",
					homePageID)),
			}
		}
		return homePageID, nil
	}
	return attachment.ContainerKey.ID, nil
}

// ResolveAttachmentProperties fills in the metadata Confluence stores as
// separate ContentProperty objects rather than as attachment scalars: the media
// type and the byte size the archive blob is expected to have.
//
// This is a pass over ContentProperty objects, so it only reads the ids the
// selected attachments actually reference.
func ResolveAttachmentProperties(archive *SourceArchive, attachments []*Attachment) error {
	wanted := map[string][]*Attachment{}
	for _, attachment := range attachments {
		for _, key := range attachment.ContentPropertyKeys {
			wanted[key.ID] = append(wanted[key.ID], attachment)
		}
	}
	if len(wanted) == 0 {
		return nil
	}

	entities, err := archive.OpenEntities()
	if err != nil {
		return err
	}
	defer func() { _ = entities.Close() }()

	decoder := NewObjectDecoder(entities)
	decoder.SetAccept(func(class ClassRef) bool { return class == ClassContentProperty })

	for {
		object, err := decoder.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}

		owners, ok := wanted[object.Key.ID]
		if !ok {
			continue
		}
		for _, attachment := range owners {
			applyAttachmentProperty(attachment, object)
		}
	}
}

func applyAttachmentProperty(attachment *Attachment, object *RawObject) {
	switch object.ScalarValue(contentPropertyName) {
	case attachmentPropMediaType:
		attachment.MediaType = object.ScalarValue(contentPropertyStringValue)
	case attachmentPropFileSize:
		if size, err := strconv.ParseInt(object.ScalarValue(contentPropertyLongValue), 10, 64); err == nil && size >= 0 {
			attachment.Size = size
		}
	}
}

// sortAttachments orders attachments by source ID, numerically when it is
// numeric, so a page's attachment list and the bundle paths are reproducible.
func sortAttachments(attachments []*Attachment) {
	sort.SliceStable(attachments, func(i, j int) bool {
		return lessNumericThenLexical(attachments[i].SourceID, attachments[j].SourceID)
	})
}

// DependencySelection is the result of pass 3: the comments and attachments
// that belong to the pages pass 2 selected.
type DependencySelection struct {
	// Comments is emission order: grouped by page, thread root before
	// descendants.
	Comments     []*Comment
	CommentsByID map[string]*Comment

	// Attachments is keyed by destination page source ID, each list ordered by
	// attachment source ID.
	Attachments     map[string][]*Attachment
	AttachmentsByID map[string]*Attachment

	Warnings []Warning

	CommentsDiscovered int
	CommentsEmitted    int
	CommentsSkipped    int

	AttachmentsDiscovered int
	AttachmentsEmitted    int
	AttachmentsSkipped    int
}

// SelectDependencies performs pass 3: with the emitted page set known, it reads
// Comment and Attachment metadata and decides what hangs off which page.
//
// It reads no bodies and copies no bytes. Attachment media type and size live
// in separate ContentProperty objects and are resolved afterwards, against only
// the ids the surviving attachments reference.
func SelectDependencies(
	archive *SourceArchive,
	space Space,
	descriptor Descriptor,
	content *ContentSelection,
	skipAttachments bool,
) (*DependencySelection, error) {
	entities, openErr := archive.OpenEntities()
	if openErr != nil {
		return nil, openErr
	}
	defer func() { _ = entities.Close() }()

	decoder := NewObjectDecoder(entities)
	decoder.SetAccept(func(class ClassRef) bool {
		return class == ClassComment || class == ClassAttachment
	})

	index := newKeyIndex()
	selection := &DependencySelection{
		CommentsByID:    map[string]*Comment{},
		Attachments:     map[string][]*Attachment{},
		AttachmentsByID: map[string]*Attachment{},
	}

	var commentCandidates []CommentCandidate
	var attachmentCandidates []AttachmentCandidate

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

		switch {
		case object.Is(ClassComment):
			selection.CommentsDiscovered++
			commentCandidates = append(commentCandidates, commentFromObject(object, descriptor.Location))

		case object.Is(ClassAttachment):
			selection.AttachmentsDiscovered++
			attachmentCandidates = append(attachmentCandidates, attachmentFromObject(object, descriptor.Location))
		}
	}

	comments, commentWarnings := BuildCommentThreads(commentCandidates, content.Pages)
	selection.Comments = comments
	selection.Warnings = append(selection.Warnings, commentWarnings...)
	for _, comment := range comments {
		selection.CommentsByID[comment.SourceID] = comment
	}
	selection.CommentsEmitted = len(comments)
	selection.CommentsSkipped = selection.CommentsDiscovered - selection.CommentsEmitted

	if err := selection.collectAttachments(attachmentCandidates, space, content, skipAttachments); err != nil {
		return nil, err
	}
	if !skipAttachments {
		if err := ResolveAttachmentProperties(archive, selection.orderedAttachments()); err != nil {
			return nil, err
		}
	}

	SortWarnings(selection.Warnings)
	return selection, nil
}

// collectAttachments applies the eligibility predicate, resolves each surviving
// attachment's destination page, and locates its blob in the archive.
func (s *DependencySelection) collectAttachments(
	candidates []AttachmentCandidate,
	space Space,
	content *ContentSelection,
	skipAttachments bool,
) error {
	if skipAttachments {
		// The flag suppresses metadata as well as bytes, so a bundle produced
		// with it never claims attachments the importer cannot find.
		if len(candidates) > 0 {
			s.AttachmentsSkipped = len(candidates)
			s.Warnings = append(s.Warnings, Warning{
				Code:       WarnAttachmentSkippedByFlag,
				EntityType: "attachment",
				Message: TruncateMessage(fmt.Sprintf(
					"--skip-attachments was set; %d attachment(s) were not exported", len(candidates))),
			})
		}
		return nil
	}

	for _, candidate := range candidates {
		attachment := candidate.Attachment
		if !candidate.IsEligible(content.ByID, space.DescriptionKey) {
			s.AttachmentsSkipped++
			continue
		}

		pageSourceID, warning := resolveDestination(attachment, space, content.ByID)
		if warning != nil {
			s.Warnings = append(s.Warnings, *warning)
			s.AttachmentsSkipped++
			continue
		}
		attachment.PageSourceID = pageSourceID
		attachment.ArchivePath = AttachmentRef{
			ContainerID:  attachment.ContainerSourceID,
			AttachmentID: attachment.SourceID,
			Version:      attachment.Version,
		}.String()

		s.Attachments[pageSourceID] = append(s.Attachments[pageSourceID], attachment)
		s.AttachmentsByID[attachment.SourceID] = attachment
		s.AttachmentsEmitted++
	}

	for _, attachments := range s.Attachments {
		sortAttachments(attachments)
	}
	return nil
}

// orderedAttachments lists every emitted attachment in page emission order,
// then attachment order, so any pass over them is reproducible.
func (s *DependencySelection) orderedAttachments() []*Attachment {
	pageIDs := make([]string, 0, len(s.Attachments))
	for pageID := range s.Attachments {
		pageIDs = append(pageIDs, pageID)
	}
	sort.Slice(pageIDs, func(i, j int) bool { return lessNumericThenLexical(pageIDs[i], pageIDs[j]) })

	ordered := make([]*Attachment, 0, len(s.AttachmentsByID))
	for _, pageID := range pageIDs {
		ordered = append(ordered, s.Attachments[pageID]...)
	}
	return ordered
}
