package confluence

import (
	"errors"
	"fmt"
	"io"
	"unicode/utf8"
)

// bodyReadLimit bounds one source body. It is generous relative to the
// destination's 2 MiB page limit so an over-limit body is reported as a page
// conversion error naming its size, rather than as a truncated read.
const bodyReadLimit = 16 << 20

// readSelectedBodies performs the payload pass: it reads the storage-format
// body of every selected page and comment, and nothing else.
//
// Bodies are the bulk of an export, so only the ids the selection actually
// references are materialized.
func readSelectedBodies(archive *SourceArchive, content *ContentSelection, deps *DependencySelection) (map[string]string, error) {
	owners := map[string]string{}
	for _, page := range content.Pages {
		for _, key := range page.BodyContentKeys {
			owners[key.ID] = page.SourceID
		}
	}
	for _, comment := range deps.Comments {
		for _, key := range comment.BodyContentKeys {
			owners[key.ID] = comment.SourceID
		}
	}
	if len(owners) == 0 {
		return map[string]string{}, nil
	}

	entities, err := archive.OpenEntities()
	if err != nil {
		return nil, err
	}
	defer func() { _ = entities.Close() }()

	decoder := NewObjectDecoder(entities)
	decoder.SetAccept(func(class ClassRef) bool { return class == ClassBodyContent })

	bodies := make(map[string]string, len(owners))
	for {
		object, err := decoder.Next()
		if errors.Is(err, io.EOF) {
			return bodies, nil
		}
		if err != nil {
			return nil, err
		}

		owner, ok := owners[object.Key.ID]
		if !ok {
			continue
		}
		// A page can carry several body representations; the first non-empty
		// one wins, and an empty one never displaces real content.
		body := object.ScalarValue(bodyContentPropBody)
		if body == "" || bodies[owner] != "" {
			continue
		}
		if len(body) > bodyReadLimit {
			body = body[:bodyReadLimit]
		}
		bodies[owner] = body
	}
}

// bodyContentPropBody is the storage-format body scalar.
const bodyContentPropBody = "body"

// emitLines converts every selected entity into the bundle's JSONL records.
//
// A page whose body cannot be converted is skipped rather than emitted broken:
// its descendants are promoted, and its comments and attachments go with it, so
// the bundle never references a page it does not carry.
func (b *bundleBuilder) emitLines(
	content *ContentSelection,
	deps *DependencySelection,
	users []*User,
	bodies map[string]string,
) ([]Line, []Warning, error) {
	usernames := make(map[string]string, len(users))
	for _, user := range users {
		usernames[user.AccountID] = user.MattermostUsername
	}

	ctx := NewConversionContext(b.space, content.Pages, deps.Attachments, users)

	var warnings []Warning
	version := BundleVersion
	lines := []Line{
		{
			Type:    LineTypeVersion,
			Version: &version,
			Source: &Source{
				OrganizationID: b.options.OrganizationID,
				SpaceID:        b.space.SourceID,
				SpaceKey:       b.space.SpaceKey,
			},
		},
		{Type: LineTypeSpace, Space: b.spaceData()},
	}

	skipped := map[string]bool{}
	for _, page := range content.Pages {
		// A page under a skipped ancestor cannot be emitted with a parent that
		// is not there, so it is promoted to the top level, as if its parent
		// had been excluded by the content predicate.
		if page.ParentSourceID != "" && skipped[page.ParentSourceID] {
			warnings = append(warnings, Warning{
				Code:       WarnPageParentMissingPromoted,
				EntityType: "page",
				SourceID:   page.SourceID,
				Message: TruncateMessage(fmt.Sprintf(
					"parent page %s was skipped during conversion; page promoted to the top level", page.ParentSourceID)),
			})
			page.ParentSourceID = ""
		}

		line, pageWarnings, err := b.pageLine(page, ctx, usernames, deps, bodies[page.SourceID])
		warnings = append(warnings, pageWarnings...)
		if err != nil {
			var conversionErr *ConversionError
			if !errors.As(err, &conversionErr) {
				return nil, nil, err
			}
			warnings = append(warnings, Warning{
				Code:       conversionErr.Code,
				EntityType: "page",
				SourceID:   page.SourceID,
				Message:    TruncateMessage(conversionErr.Message + "; page skipped"),
			})
			skipped[page.SourceID] = true
			b.counts.PagesSkipped++
			continue
		}
		lines = append(lines, *line)
	}

	commentLines, commentWarnings := b.commentLines(deps, ctx, usernames, bodies, skipped)
	lines = append(lines, commentLines...)
	warnings = append(warnings, commentWarnings...)

	lines = append(lines, Line{
		Type:                     LineTypeResolveSpacePlaceholders,
		ResolveSpacePlaceholders: &ResolvePlaceholdersData{},
	})

	b.dropSkippedAttachments(deps, skipped, &warnings)
	return lines, warnings, nil
}

func (b *bundleBuilder) spaceData() *SpaceData {
	return &SpaceData{
		Team:  b.options.Team,
		Title: firstNonEmpty(b.space.Name, b.space.SpaceKey, b.space.SourceID),
		Props: map[string]any{
			PropImportSourceID:     b.space.SourceID,
			PropImportSource:       SourceType,
			PropConfluenceSpaceKey: b.space.SpaceKey,
		},
	}
}

// pageLine builds one page record, converting its body and listing the
// attachments that survived the copy.
func (b *bundleBuilder) pageLine(
	page *Page,
	ctx *ConversionContext,
	usernames map[string]string,
	deps *DependencySelection,
	body string,
) (*Line, []Warning, error) {
	var warnings []Warning

	title, titleWarning := b.pageTitle(page)
	if titleWarning != nil {
		warnings = append(warnings, *titleWarning)
	}

	content := EmptyDocumentJSON
	if body == "" {
		b.counts.MissingBodies++
		warnings = append(warnings, Warning{
			Code:       WarnPageMissingBody,
			EntityType: "page",
			SourceID:   page.SourceID,
			Message:    "no body content was found; an empty page was emitted",
		})
	} else {
		converter := NewConverter()
		converter.SetContext(ctx)

		converted, err := converter.Convert(body, page.SourceID)
		warnings = append(warnings, converter.Warnings()...)
		if err != nil {
			return nil, warnings, err
		}
		content = converted
	}

	author := usernames[b.accountFor(page.CreatorKey, ctx)]
	data := &PageData{
		Team:                 b.options.Team,
		SpaceImportSourceID:  b.space.SourceID,
		User:                 firstNonEmpty(author, b.fallbackUsername(usernames)),
		Title:                title,
		Content:              content,
		ParentImportSourceID: page.ParentSourceID,
		Props:                b.pageProps(page, ctx),
	}
	if page.HasCreatedAt {
		data.CreateAt = page.CreatedAt
	}
	if page.HasUpdatedAt {
		data.UpdateAt = page.UpdatedAt
	}
	for _, attachment := range deps.Attachments[page.SourceID] {
		data.Attachments = append(data.Attachments, attachmentData(attachment))
	}

	return &Line{Type: LineTypePage, Page: data}, warnings, nil
}

// pageTitle applies the section 10 title policy.
func (b *bundleBuilder) pageTitle(page *Page) (string, *Warning) {
	title := page.Title
	if title == "" {
		return "Untitled " + page.SourceID, &Warning{
			Code:       WarnPageTitleDefaulted,
			EntityType: "page",
			SourceID:   page.SourceID,
			Message:    "the source page has no title; a placeholder title was generated",
		}
	}
	if utf8.RuneCountInString(title) > TitleMaxRunes {
		runes := []rune(title)
		return string(runes[:TitleMaxRunes]), &Warning{
			Code:       WarnPageTitleTruncated,
			EntityType: "page",
			SourceID:   page.SourceID,
			Message: TruncateMessage(fmt.Sprintf(
				"title was %d runes and was truncated to %d", len(runes), TitleMaxRunes)),
		}
	}
	return title, nil
}

func (b *bundleBuilder) pageProps(page *Page, ctx *ConversionContext) map[string]any {
	labels := make([]any, 0, len(page.Labels))
	for _, label := range page.Labels {
		labels = append(labels, map[string]any{"name": label.Name, "namespace": label.Namespace})
	}
	names := make([]any, 0, len(page.Labels))
	for _, name := range LabelNames(page.Labels) {
		names = append(names, name)
	}

	return map[string]any{
		PropImportSourceID:            page.SourceID,
		PropImportSource:              SourceType,
		PropConfluenceSpaceKey:        b.space.SpaceKey,
		PropConfluenceContentType:     page.ContentType,
		PropConfluenceAuthorAccountID: b.accountFor(page.CreatorKey, ctx),
		PropImportLabels:              names,
		PropConfluenceLabels:          labels,
		PropConfluenceRestrictions:    map[string]any{},
	}
}

func attachmentData(attachment *Attachment) AttachmentData {
	return AttachmentData{
		Path: attachment.BundlePath,
		Props: map[string]any{
			PropImportSourceID:              attachment.SourceID,
			PropConfluenceContainerSourceID: attachment.ContainerSourceID,
			PropFilename:                    attachment.Filename,
			PropMediaType:                   attachment.MediaType,
			PropSize:                        attachment.Size,
			PropSHA256:                      attachment.SHA256,
		},
	}
}

// commentLines builds the comment records, dropping any whose page was skipped.
func (b *bundleBuilder) commentLines(
	deps *DependencySelection,
	ctx *ConversionContext,
	usernames map[string]string,
	bodies map[string]string,
	skippedPages map[string]bool,
) ([]Line, []Warning) {
	var (
		lines    []Line
		warnings []Warning
	)
	skippedComments := map[string]bool{}

	for _, comment := range deps.Comments {
		switch {
		case skippedPages[comment.PageSourceID]:
			skippedComments[comment.SourceID] = true
			b.counts.CommentsSkipped++
			continue
		case comment.ParentSourceID != "" && skippedComments[comment.ParentSourceID],
			skippedComments[comment.ThreadRootSourceID]:
			// A reply whose thread root did not survive has nowhere to attach.
			skippedComments[comment.SourceID] = true
			b.counts.CommentsSkipped++
			continue
		}

		converter := NewConverter()
		converter.SetContext(ctx)

		content, err := converter.Convert(bodies[comment.SourceID], comment.SourceID)
		if err != nil {
			warnings = append(warnings, Warning{
				Code:       WarnCommentTooLargeSkipped,
				EntityType: "comment",
				SourceID:   comment.SourceID,
				Message:    TruncateMessage(err.Error() + "; comment and its replies skipped"),
			})
			skippedComments[comment.SourceID] = true
			b.counts.CommentsSkipped++
			continue
		}
		warnings = append(warnings, converter.Warnings()...)

		author := usernames[b.accountFor(comment.CreatorKey, ctx)]
		lines = append(lines, Line{
			Type: LineTypePageComment,
			PageComment: &PageCommentData{
				PageImportSourceID:          comment.PageSourceID,
				ParentCommentImportSourceID: comment.ParentSourceID,
				ThreadRootImportSourceID:    comment.ThreadRootSourceID,
				User:                        firstNonEmpty(author, b.fallbackUsername(usernames)),
				Content:                     content,
				CreateAt:                    commentTime(comment.HasCreatedAt, comment.CreatedAt),
				UpdateAt:                    commentTime(comment.HasUpdatedAt, comment.UpdatedAt),
				IsResolved:                  comment.IsResolved,
				Props: map[string]any{
					PropImportSourceID:            comment.SourceID,
					PropImportSource:              SourceType,
					PropConfluenceAuthorAccountID: b.accountFor(comment.CreatorKey, ctx),
				},
			},
		})
	}

	return lines, warnings
}

func commentTime(present bool, value int64) int64 {
	if !present {
		return 0
	}
	return value
}

// dropSkippedAttachments removes attachments belonging to skipped pages, so the
// bundle never carries a blob no page references.
func (b *bundleBuilder) dropSkippedAttachments(deps *DependencySelection, skipped map[string]bool, warnings *[]Warning) {
	for pageID := range skipped {
		for _, attachment := range deps.Attachments[pageID] {
			*warnings = append(*warnings, Warning{
				Code:       WarnAttachmentHomePageMissing,
				EntityType: "attachment",
				SourceID:   attachment.SourceID,
				Message: TruncateMessage(fmt.Sprintf(
					"page %s was skipped during conversion; its attachment was not emitted", pageID)),
			})
			delete(deps.AttachmentsByID, attachment.SourceID)
			deps.AttachmentsEmitted--
			deps.AttachmentsSkipped++
		}
		delete(deps.Attachments, pageID)
	}
}

// accountFor maps a source user reference onto the canonical account ID the
// props carry, or "" when the reference names nobody this export knows.
func (b *bundleBuilder) accountFor(key EntityKey, ctx *ConversionContext) string {
	if key.IsZero() || ctx == nil {
		return ""
	}
	if user, ok := ctx.usersByKey[key.ID]; ok {
		return user.AccountID
	}
	if user, ok := ctx.usersByAccountID[key.ID]; ok {
		return user.AccountID
	}
	return ""
}

// fallbackUsername names an author when the source reference resolves to
// nobody, so a page is still importable.
func (b *bundleBuilder) fallbackUsername(usernames map[string]string) string {
	best := ""
	for _, username := range usernames {
		if best == "" || username < best {
			best = username
		}
	}
	return best
}

// tallyCounts records what the export discovered and emitted.
//
// The emitted totals are counted from the lines themselves rather than from the
// selection, because pages can still be skipped during conversion. Deriving
// them any other way lets the manifest and the stream drift apart, which the
// bundle's own validation then rejects.
func (b *bundleBuilder) tallyCounts(
	lines []Line,
	content *ContentSelection,
	deps *DependencySelection,
	labels *LabelSelection,
	users []*User,
) {
	b.counts.SpacesEmitted = 1
	b.counts.PagesDiscovered = content.PagesDiscovered
	b.counts.PagesFlattened = content.PagesFlattened

	for _, line := range lines {
		switch line.Type {
		case LineTypePage:
			if line.Page.Props[PropConfluenceContentType] == ContentTypeBlogPost {
				b.counts.BlogPostsEmitted++
			} else {
				b.counts.PagesEmitted++
			}
		case LineTypePageComment:
			b.counts.CommentsEmitted++
		}
	}

	b.counts.CommentsDiscovered = deps.CommentsDiscovered
	b.counts.CommentsSkipped = deps.CommentsSkipped + b.counts.CommentsSkipped

	b.counts.AttachmentsDiscovered = deps.AttachmentsDiscovered
	b.counts.AttachmentsEmitted = deps.AttachmentsEmitted
	b.counts.AttachmentsSkipped = deps.AttachmentsSkipped

	b.counts.LabelsPreserved = labels.LabelsPreserved
	b.counts.RestrictedPagesPreserved = labels.RestrictedPagesPreserved

	b.counts.UsersEmitted = len(users)
	for _, user := range users {
		if !user.Active {
			b.counts.UsersInactive++
		}
		if user.EmailIsPlaceholder {
			b.counts.UsersPlaceholderEmail++
		}
	}
}

func (b *bundleBuilder) buildManifest(users []*User) *Manifest {
	manifestUsers := make([]ManifestUser, 0, len(users))
	for _, user := range users {
		manifestUsers = append(manifestUsers, ManifestUser{
			AccountID:              user.AccountID,
			ConfluenceUserKey:      user.ConfluenceUserKey,
			ConfluenceUsername:     user.ConfluenceUsername,
			DisplayName:            user.DisplayName,
			Email:                  user.Email,
			ExternalID:             user.ExternalID,
			Active:                 user.Active,
			MattermostUsername:     user.MattermostUsername,
			EmailIsPlaceholder:     user.EmailIsPlaceholder,
			UsernameProposalSource: user.UsernameProposalSource,
		})
	}

	warnings := make([]Warning, len(b.warnings))
	copy(warnings, b.warnings)
	SortWarnings(warnings)

	return &Manifest{
		Version:          ManifestVersion,
		Generator:        Generator,
		GeneratorVersion: firstNonEmpty(b.options.GeneratorVersion, "dev"),
		CreatedAt:        b.now().UTC(),
		Source: ManifestSource{
			Type:           SourceType,
			OrganizationID: b.options.OrganizationID,
			SpaceID:        b.space.SourceID,
			SpaceKey:       b.space.SpaceKey,
			SpaceName:      b.space.Name,
			ExportFile:     b.archive.Path(),
			TimezoneID:     b.descriptor.TimezoneID,
		},
		Target:    ManifestTarget{Team: b.options.Team},
		Counts:    b.counts,
		Users:     manifestUsers,
		Fidelity:  exportFidelity(),
		Warnings:  warnings,
		Errors:    []Issue{},
		Checksums: ManifestChecksums{},
	}
}

// exportFidelity states what this iteration actually does, not what a reader
// might hope it does.
func exportFidelity() ManifestFidelity {
	return ManifestFidelity{
		Pages:            FidelityPagesImported,
		BlogPosts:        FidelityBlogPostsImportedAsPages,
		Comments:         FidelityCommentsImportedAsPosts,
		Attachments:      FidelityAttachmentsImported,
		Labels:           FidelityLabelsPreservedNotApplied,
		PageRestrictions: FidelityRestrictionsUnverified,
		SpacePermissions: FidelitySpacePermissionsNotImport,
		ExternalAuth:     FidelityExternalAuthPreserved,
	}
}
