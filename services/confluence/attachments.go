package confluence

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
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

	// The discovered counts are scoped to the selected space: they count the
	// comments and attachments that were candidates for this export, so
	// discovered always equals emitted plus skipped. Counting every Comment and
	// Attachment object in the file instead would report another space's
	// content as if this export had passed it over.
	CommentsDiscovered int
	CommentsEmitted    int
	CommentsSkipped    int

	AttachmentsDiscovered int
	AttachmentsEmitted    int
	AttachmentsSkipped    int

	// ObjectsScanned counts every comment and attachment object read, for
	// logging.
	ObjectsScanned int
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

		selection.ObjectsScanned++
		switch {
		case object.Is(ClassComment):
			commentCandidates = append(commentCandidates, commentFromObject(object, descriptor.Location))

		case object.Is(ClassAttachment):
			attachmentCandidates = append(attachmentCandidates, attachmentFromObject(object, descriptor.Location))
		}
	}

	for _, candidate := range commentCandidates {
		if candidate.IsInitiallyEligible(content.ByID) {
			selection.CommentsDiscovered++
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
	// Eligibility is evaluated even when the flag is set, so the counts describe
	// what this space actually holds rather than what the whole file holds.
	var eligible []*Attachment
	for _, candidate := range candidates {
		if candidate.IsEligible(content.ByID, space.DescriptionKey) {
			eligible = append(eligible, candidate.Attachment)
		}
	}
	s.AttachmentsDiscovered = len(eligible)

	if skipAttachments {
		// The flag suppresses metadata as well as bytes, so a bundle produced
		// with it never claims attachments the importer cannot find.
		if len(eligible) > 0 {
			s.AttachmentsSkipped = len(eligible)
			s.Warnings = append(s.Warnings, Warning{
				Code:       WarnAttachmentSkippedByFlag,
				EntityType: "attachment",
				Message: TruncateMessage(fmt.Sprintf(
					"--skip-attachments was set; %d attachment(s) were not exported", len(eligible))),
			})
		}
		return nil
	}

	for _, attachment := range eligible {
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

// attachmentFilenameMaxRunes bounds a sanitized filename. It leaves room inside
// a ZIP entry name for the two source IDs and the data/ prefix, and no
// destination shows more than this anyway.
const attachmentFilenameMaxRunes = 128

// defaultAttachmentFilename names a blob whose source filename sanitizes to
// nothing, so the entry still has a usable name.
const defaultAttachmentFilename = "attachment"

// windowsReservedNames cannot be used as a filename on Windows even with an
// extension. A bundle extracted there would fail on one, so they are prefixed.
var windowsReservedNames = map[string]bool{
	"con": true, "prn": true, "aux": true, "nul": true,
	"com1": true, "com2": true, "com3": true, "com4": true, "com5": true,
	"com6": true, "com7": true, "com8": true, "com9": true,
	"lpt1": true, "lpt2": true, "lpt3": true, "lpt4": true, "lpt5": true,
	"lpt6": true, "lpt7": true, "lpt8": true, "lpt9": true,
}

// SanitizeAttachmentFilename reduces a Confluence filename to one that is safe
// as a ZIP entry name on any platform.
//
// It strips directory separators, control characters, and the characters
// Windows forbids, and it never returns "", ".", or "..". The original filename
// is preserved untouched in the attachment props, so nothing is lost by being
// strict here.
func SanitizeAttachmentFilename(raw string) string {
	// Take the last path segment: a Confluence filename should not contain a
	// separator, but one that does must not create a directory in the bundle.
	name := raw
	if idx := strings.LastIndexAny(name, `/\`); idx >= 0 {
		name = name[idx+1:]
	}

	var b strings.Builder
	b.Grow(len(name))
	for _, r := range name {
		switch {
		case r < 0x20, r == 0x7f:
			// Control characters, including NUL.
		case strings.ContainsRune(`<>:"|?*`, r):
			b.WriteRune('_')
		default:
			b.WriteRune(r)
		}
	}

	// A trailing dot or space is silently dropped by Windows, which would make
	// the manifest path and the extracted path disagree.
	cleaned := strings.TrimRight(strings.TrimSpace(b.String()), ". ")
	cleaned = strings.Trim(cleaned, "/")

	if runes := []rune(cleaned); len(runes) > attachmentFilenameMaxRunes {
		cleaned = strings.TrimRight(string(runes[:attachmentFilenameMaxRunes]), ". ")
	}

	switch cleaned {
	case "", ".", "..":
		return defaultAttachmentFilename
	}

	stem := cleaned
	if idx := strings.IndexByte(stem, '.'); idx > 0 {
		stem = stem[:idx]
	}
	if windowsReservedNames[strings.ToLower(stem)] {
		return "_" + cleaned
	}
	return cleaned
}

// AttachmentBlobResult is the outcome of copying one attachment's bytes.
type AttachmentBlobResult struct {
	Attachment *Attachment

	// BundlePath is where the blob lives in the output bundle.
	BundlePath string

	// Size and SHA256 are measured from the bytes actually copied, not
	// from what the source claimed.
	Size   int64
	SHA256 string
}

// AttachmentSink receives one attachment's bytes.
//
// The writer is handed a stream rather than a buffer: an attachment can be a
// gigabyte, and the whole point of the pass is that only one flows through
// memory at a time.
type AttachmentSink interface {
	WriteAttachment(bundlePath string, size int64, body io.Reader) error
}

// CopyAttachments streams each emitted attachment from the source archive into
// the sink, verifying it and recording what was actually written.
//
// An attachment that cannot be copied is skipped with a warning and removed
// from the selection, so the bundle never lists a blob it does not carry. A
// failure here is never fatal: one corrupt image should not cost an operator
// the whole space.
func CopyAttachments(archive *SourceArchive, deps *DependencySelection, sink AttachmentSink) ([]AttachmentBlobResult, []Warning, error) {
	var (
		results  []AttachmentBlobResult
		warnings []Warning
	)

	usedPaths := map[string]bool{}

	for _, pageID := range sortedAttachmentPages(deps) {
		kept := deps.Attachments[pageID][:0]

		for _, attachment := range deps.Attachments[pageID] {
			result, warning, err := copyAttachment(archive, attachment, usedPaths, sink)
			if err != nil {
				return nil, nil, err
			}
			if warning != nil {
				warnings = append(warnings, *warning)
				delete(deps.AttachmentsByID, attachment.SourceID)
				deps.AttachmentsEmitted--
				deps.AttachmentsSkipped++
				continue
			}
			kept = append(kept, attachment)
			results = append(results, *result)
		}

		if len(kept) == 0 {
			delete(deps.Attachments, pageID)
			continue
		}
		deps.Attachments[pageID] = kept
	}

	SortWarnings(warnings)
	return results, warnings, nil
}

func sortedAttachmentPages(deps *DependencySelection) []string {
	pageIDs := make([]string, 0, len(deps.Attachments))
	for pageID := range deps.Attachments {
		pageIDs = append(pageIDs, pageID)
	}
	sort.Slice(pageIDs, func(i, j int) bool { return lessNumericThenLexical(pageIDs[i], pageIDs[j]) })
	return pageIDs
}

// copyAttachment verifies and copies one blob. A non-nil warning means the
// attachment was skipped; only an error from the sink is fatal.
func copyAttachment(
	archive *SourceArchive,
	attachment *Attachment,
	usedPaths map[string]bool,
	sink AttachmentSink,
) (*AttachmentBlobResult, *Warning, error) {
	ref := AttachmentRef{
		ContainerID:  attachment.ContainerSourceID,
		AttachmentID: attachment.SourceID,
		Version:      attachment.Version,
	}
	entry, ok := archive.Attachment(ref)
	if !ok {
		return nil, &Warning{
			Code:       WarnAttachmentBlobMissing,
			EntityType: "attachment",
			SourceID:   attachment.SourceID,
			Message: TruncateMessage(fmt.Sprintf(
				"the source archive has no blob at %s; attachment skipped", ref)),
		}, nil
	}

	// The declared size comes from a Confluence content property and the entry
	// size from the ZIP directory. They disagreeing means the export is not
	// internally consistent, and copying either one would be a guess.
	if attachment.Size > 0 && attachment.Size != entry.Size {
		return nil, &Warning{
			Code:       WarnAttachmentSizeMismatch,
			EntityType: "attachment",
			SourceID:   attachment.SourceID,
			Message: TruncateMessage(fmt.Sprintf(
				"Confluence records %d bytes but the archive entry holds %d; attachment skipped",
				attachment.Size, entry.Size)),
		}, nil
	}

	sanitized := SanitizeAttachmentFilename(attachment.Filename)
	bundlePath := uniqueAttachmentPath(attachment, sanitized, usedPaths)

	digest, size, err := verifyAttachmentBlob(entry)
	if err != nil {
		return nil, &Warning{
			Code:       WarnAttachmentBlobCorrupt,
			EntityType: "attachment",
			SourceID:   attachment.SourceID,
			Message:    TruncateMessage(err.Error() + "; attachment skipped"),
		}, nil
	}

	body, err := entry.Open()
	if err != nil {
		return nil, &Warning{
			Code:       WarnAttachmentBlobCorrupt,
			EntityType: "attachment",
			SourceID:   attachment.SourceID,
			Message:    TruncateMessage(err.Error() + "; attachment skipped"),
		}, nil
	}
	defer func() { _ = body.Close() }()

	// Bounded so a ZIP directory that understates an entry's real length cannot
	// make the writer consume unbounded input.
	if err := sink.WriteAttachment(bundlePath, size, io.LimitReader(body, size)); err != nil {
		return nil, nil, fmt.Errorf("writing attachment %s: %w", bundlePath, err)
	}

	attachment.Size = size
	attachment.SanitizedFilename = sanitized
	attachment.BundlePath = bundlePath
	attachment.SHA256 = digest

	return &AttachmentBlobResult{
		Attachment: attachment,
		BundlePath: bundlePath,
		Size:       size,
		SHA256:     digest,
	}, nil, nil
}

// verifyAttachmentBlob reads the entry once to confirm it decompresses to the
// declared length with the declared CRC, and returns its SHA-256.
//
// Reading twice, once to verify and once to copy, is deliberate: writing a
// corrupt blob into the bundle and reporting it afterwards would leave the
// operator with a bundle they must not import.
func verifyAttachmentBlob(entry AttachmentEntry) (digest string, size int64, err error) {
	body, err := entry.Open()
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = body.Close() }()

	hash := sha256.New()
	crc := crc32.NewIEEE()

	// One byte over the declared size is read on purpose, so an entry that
	// decompresses to more than it declared is detected rather than truncated.
	written, err := io.Copy(io.MultiWriter(hash, crc), io.LimitReader(body, entry.Size+1))
	if err != nil {
		return "", 0, fmt.Errorf("reading %s: %w", entry.Path, err)
	}
	if written != entry.Size {
		return "", 0, fmt.Errorf("%s declares %d bytes but decompresses to %d", entry.Path, entry.Size, written)
	}
	if crc.Sum32() != entry.CRC32 {
		return "", 0, fmt.Errorf("%s fails its CRC check", entry.Path)
	}
	return hex.EncodeToString(hash.Sum(nil)), written, nil
}

// uniqueAttachmentPath keeps two attachments from colliding on one bundle path.
//
// Sanitation can map two different source filenames onto one, and the contract
// path already carries the attachment's own source ID, so a collision means the
// same attachment twice or a sanitation clash. Either way a suffix keeps both
// blobs addressable.
func uniqueAttachmentPath(attachment *Attachment, sanitized string, used map[string]bool) string {
	path := AttachmentBundlePath(attachment.PageSourceID, attachment.SourceID, sanitized)
	if !used[path] {
		used[path] = true
		return path
	}

	stem, extension := splitFilename(sanitized)
	for n := 2; ; n++ {
		candidate := AttachmentBundlePath(attachment.PageSourceID, attachment.SourceID,
			fmt.Sprintf("%s_%d%s", stem, n, extension))
		if !used[candidate] {
			used[candidate] = true
			return candidate
		}
	}
}

func splitFilename(name string) (stem, extension string) {
	if idx := strings.LastIndexByte(name, '.'); idx > 0 {
		return name[:idx], name[idx:]
	}
	return name, ""
}
