package confluence

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	log "github.com/sirupsen/logrus"
)

// OrganizationIDMaxBytes bounds --organization-id. It is echoed into the
// manifest and every source mapping key, so an unbounded value would bloat
// every row the importer stores.
const OrganizationIDMaxBytes = 1024

// TransformOptions is one invocation of the exporter.
type TransformOptions struct {
	// SourcePath is the Confluence Cloud XML backup ZIP.
	SourcePath string

	// SpaceSelector is the --space value: a source ID, a key, or a name.
	SpaceSelector string

	// OrganizationID scopes every source ID in the output. The same Confluence
	// site must always be given the same value, or a re-export will look like a
	// different site and be imported again rather than reconciled.
	OrganizationID string

	// Team is the destination Mattermost team.
	Team string

	// OutputPath defaults to <sanitized-space-key>-confluence-docs.zip.
	OutputPath string

	// UserMappingPath is an optional explicit mapping CSV.
	UserMappingPath string

	SkipAttachments bool

	// ValidateOnly runs the whole transform and reports, without writing
	// anything.
	ValidateOnly bool

	// GeneratorVersion is recorded in the manifest.
	GeneratorVersion string

	// Now supplies the manifest timestamp. Tests inject a fixed clock.
	Now func() time.Time

	Logger *log.Logger
}

// TransformResult summarizes what an invocation produced.
type TransformResult struct {
	Space      Space
	OutputPath string
	Manifest   *Manifest
	Lines      []Line
}

// ValidateOptions checks the flags that can be judged before the archive is
// opened.
func (o *TransformOptions) ValidateOptions() error {
	if strings.TrimSpace(o.SourcePath) == "" {
		return errors.New("--file is required")
	}
	if strings.TrimSpace(o.Team) == "" {
		return errors.New("--team is required")
	}
	if err := ValidateOrganizationID(o.OrganizationID); err != nil {
		return err
	}
	if strings.TrimSpace(o.SpaceSelector) == "" {
		return errors.New("--space is required and must name exactly one space")
	}
	return nil
}

// ValidateOrganizationID enforces the section 3.3 rules.
//
// The value is preserved exactly, including case and any trailing path, because
// it is the scope of every source mapping: normalizing it here would silently
// split one site's history across two identities.
func ValidateOrganizationID(raw string) error {
	trimmed := strings.TrimSpace(raw)
	switch {
	case trimmed == "":
		return errors.New("--organization-id is required; use the same value for every export from one Confluence site")
	case strings.ContainsRune(trimmed, 0):
		return errors.New("--organization-id must not contain NUL")
	case len(trimmed) > OrganizationIDMaxBytes:
		return fmt.Errorf("--organization-id is %d bytes, over the %d byte limit", len(trimmed), OrganizationIDMaxBytes)
	case !utf8.ValidString(trimmed):
		return errors.New("--organization-id must be valid UTF-8")
	}
	return nil
}

// DefaultOutputPath is the bundle name used when --output is not given.
func DefaultOutputPath(space Space) string {
	key := sanitizeOutputKey(space.SpaceKey)
	if key == "" {
		key = sanitizeOutputKey(space.SourceID)
	}
	if key == "" {
		key = "confluence"
	}
	return key + "-confluence-docs.zip"
}

// sanitizeOutputKey reduces a space key to something safe as a filename.
// Personal space keys start with "~" and site keys can hold anything.
func sanitizeOutputKey(raw string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(raw)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-._")
}

// Transform runs the whole export: catalog, select, convert, copy, and write.
func Transform(options TransformOptions) (*TransformResult, error) {
	if err := options.ValidateOptions(); err != nil {
		return nil, err
	}
	logger := options.Logger
	if logger == nil {
		logger = log.New()
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}

	mapping, err := loadUserMapping(options.UserMappingPath)
	if err != nil {
		return nil, err
	}

	archive, err := OpenSourceArchive(options.SourcePath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = archive.Close() }()

	spaces, descriptor, err := CatalogSpaces(archive)
	if err != nil {
		return nil, err
	}
	space, err := ResolveSpace(spaces, options.SpaceSelector)
	if err != nil {
		return nil, err
	}
	logger.Infof("Exporting space %s (%s) from %s", space.SpaceKey, space.SourceID, options.SourcePath)

	builder := &bundleBuilder{
		options:    options,
		logger:     logger,
		archive:    archive,
		descriptor: descriptor,
		space:      space,
		mapping:    mapping,
		now:        now,
	}
	return builder.run()
}

func loadUserMapping(path string) (*UserMapping, error) {
	if strings.TrimSpace(path) == "" {
		return NewUserMapping(), nil
	}

	file, err := os.Open(path) //nolint:gosec // the operator names this file
	if err != nil {
		return nil, fmt.Errorf("opening user mapping: %w", err)
	}
	defer func() { _ = file.Close() }()

	return ParseUserMapping(file)
}

// bundleBuilder carries one export through its passes.
type bundleBuilder struct {
	options    TransformOptions
	logger     *log.Logger
	archive    *SourceArchive
	descriptor Descriptor
	space      Space
	mapping    *UserMapping
	now        func() time.Time

	warnings []Warning
	counts   ManifestCounts
}

func (b *bundleBuilder) warn(warnings ...Warning) {
	b.warnings = append(b.warnings, warnings...)
}

func (b *bundleBuilder) run() (*TransformResult, error) {
	b.warn(b.archive.Warnings()...)

	content, err := SelectPageMetadata(b.archive, b.space, b.descriptor)
	if err != nil {
		return nil, err
	}
	b.warn(content.Warnings...)

	deps, err := SelectDependencies(b.archive, b.space, b.descriptor, content, b.options.SkipAttachments)
	if err != nil {
		return nil, err
	}
	b.warn(deps.Warnings...)

	refs := NewUserRefs()
	refs.AddFromContent(content)
	refs.AddFromDependencies(deps)

	labels, err := SelectLabels(b.archive, content, refs)
	if err != nil {
		return nil, err
	}
	b.warn(labels.Warnings...)

	restricted, restrictionWarnings, err := SelectRestrictions(b.archive, content, refs)
	if err != nil {
		return nil, err
	}
	b.warn(restrictionWarnings...)
	b.counts.RestrictedPagesPreserved = restricted

	// Reported on every export, not only when a restriction is found. Silence
	// would read as "nothing here is restricted", when what it has to say is
	// that restrictions are recorded and never enforced.
	b.warn(Warning{
		Code:       WarnRestrictionUnverified,
		EntityType: "space",
		SourceID:   b.space.SourceID,
		Message: TruncateMessage(fmt.Sprintf(
			"%d page restriction(s) were preserved as metadata and are not enforced; access at the "+
				"destination remains Space-level", restricted)),
	})

	users, userWarnings, err := SelectUsers(b.archive, b.options.OrganizationID, refs, b.mapping)
	if err != nil {
		return nil, err
	}
	b.warn(userWarnings...)

	outputPath := b.options.OutputPath
	if strings.TrimSpace(outputPath) == "" {
		outputPath = DefaultOutputPath(b.space)
	}

	writer, err := b.openWriter(outputPath)
	if err != nil {
		return nil, err
	}
	defer writer.Abort()

	if !b.options.SkipAttachments {
		_, copyWarnings, copyErr := CopyAttachments(b.archive, deps, writer)
		if copyErr != nil {
			return nil, copyErr
		}
		b.warn(copyWarnings...)
	}

	bodies, err := readSelectedBodies(b.archive, content, deps)
	if err != nil {
		return nil, err
	}

	lines, emitWarnings, err := b.emitLines(content, deps, users, bodies)
	if err != nil {
		return nil, err
	}
	b.warn(emitWarnings...)

	b.tallyCounts(lines, content, deps, labels, users)
	manifest := b.buildManifest(users)

	if b.options.ValidateOnly {
		b.logSummary(outputPath, manifest, true)
		return &TransformResult{Space: b.space, Manifest: manifest, Lines: lines}, nil
	}

	if err := writer.Finish(manifest, lines); err != nil {
		return nil, err
	}
	b.logSummary(outputPath, manifest, false)

	return &TransformResult{Space: b.space, OutputPath: outputPath, Manifest: manifest, Lines: lines}, nil
}

// openWriter creates the bundle writer. Validate-only still needs one, because
// the attachment pass streams through it and its checksums are what the
// manifest reports.
func (b *bundleBuilder) openWriter(outputPath string) (*BundleWriter, error) {
	if !b.options.ValidateOnly {
		return NewBundleWriter(outputPath)
	}
	return NewBundleWriter(filepath.Join(os.TempDir(), filepath.Base(outputPath)))
}

func (b *bundleBuilder) logSummary(outputPath string, manifest *Manifest, validateOnly bool) {
	verb := "Wrote"
	if validateOnly {
		verb = "Validated"
		outputPath = "(nothing written)"
	}

	b.logger.Infof("%s %s: %d pages, %d blog posts, %d comments, %d attachments, %d users",
		verb, outputPath,
		manifest.Counts.PagesEmitted, manifest.Counts.BlogPostsEmitted,
		manifest.Counts.CommentsEmitted, manifest.Counts.AttachmentsEmitted,
		manifest.Counts.UsersEmitted)

	if len(manifest.Warnings) > 0 {
		b.logger.Warnf("%d warning(s); see the manifest for details", len(manifest.Warnings))
		for _, warning := range manifest.Warnings {
			b.logger.Debugf("  %s %s %s: %s", warning.Code, warning.EntityType, warning.SourceID, warning.Message)
		}
	}
}
