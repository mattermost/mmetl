package confluence

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// attachmentPathSegments is the exact number of segments in a contract
// attachment path: data/<page-source-id>/<attachment-source-id>/<filename>.
const attachmentPathSegments = 4

var hex64Re = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ValidateLines checks the decoded import.jsonl stream against the version 2
// contract: line sequence, the trailing sentinel, entity uniqueness,
// parent-before-child ordering, thread-root semantics, required props, and
// attachment path shape.
//
// It is the shared gate for the producer's self-validation and for the fixture
// tests that the Docs importer mirrors. It never inspects attachment bytes;
// ValidateBundleFS does that.
func ValidateLines(lines []Line) error {
	if len(lines) == 0 {
		return errors.New("import.jsonl is empty")
	}

	v := &lineValidator{
		pages:       map[string]*PageData{},
		pageOrder:   []string{},
		comments:    map[string]*PageCommentData{},
		attachments: map[string]string{},
	}

	if err := v.walk(lines); err != nil {
		return err
	}
	return v.finish()
}

type lineValidator struct {
	source *Source
	space  *SpaceData

	pages     map[string]*PageData
	pageOrder []string

	comments    map[string]*PageCommentData
	attachments map[string]string // attachment source ID -> owning page source ID

	sawSentinel bool
	sawComment  bool
}

func (v *lineValidator) walk(lines []Line) error {
	for i, line := range lines {
		lineNo := i + 1
		if v.sawSentinel {
			return fmt.Errorf("line %d: %q must be the final line", lineNo, LineTypeResolveSpacePlaceholders)
		}
		if err := v.checkPayloadMatchesType(lineNo, line); err != nil {
			return err
		}

		var err error
		switch line.Type {
		case LineTypeVersion:
			err = v.version(lineNo, line)
		case LineTypeSpace:
			err = v.spaceLine(lineNo, line)
		case LineTypePage:
			err = v.page(lineNo, line)
		case LineTypePageComment:
			err = v.comment(lineNo, line)
		case LineTypeResolveSpacePlaceholders:
			err = v.sentinel(lineNo)
		default:
			err = fmt.Errorf("line %d: unknown line type %q", lineNo, line.Type)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// checkPayloadMatchesType rejects a line carrying a payload other than the one
// its type names, which would otherwise let a producer smuggle data past a
// consumer that switches on Type.
func (v *lineValidator) checkPayloadMatchesType(lineNo int, line Line) error {
	// Ordered so a line carrying several payloads always reports the same one.
	payloads := []struct {
		lineType string
		present  bool
	}{
		{LineTypeVersion, line.Version != nil || line.Source != nil},
		{LineTypeSpace, line.Space != nil},
		{LineTypePage, line.Page != nil},
		{LineTypePageComment, line.PageComment != nil},
		{LineTypeResolveSpacePlaceholders, line.ResolveSpacePlaceholders != nil},
	}

	var own bool
	var known bool
	for _, payload := range payloads {
		if payload.lineType == line.Type {
			known, own = true, payload.present
		}
	}
	if !known {
		return fmt.Errorf("line %d: unknown line type %q", lineNo, line.Type)
	}
	for _, payload := range payloads {
		if payload.present && payload.lineType != line.Type {
			return fmt.Errorf("line %d: type %q carries a %q payload", lineNo, line.Type, payload.lineType)
		}
	}
	if !own {
		return fmt.Errorf("line %d: type %q is missing its payload", lineNo, line.Type)
	}
	return nil
}

func (v *lineValidator) version(lineNo int, line Line) error {
	if v.source != nil {
		return fmt.Errorf("line %d: duplicate %q line", lineNo, LineTypeVersion)
	}
	if lineNo != 1 {
		return fmt.Errorf("line %d: %q must be the first line", lineNo, LineTypeVersion)
	}
	if line.Version == nil {
		return fmt.Errorf("line %d: missing version", lineNo)
	}
	if *line.Version != BundleVersion {
		return fmt.Errorf("line %d: unsupported bundle version %d, want %d", lineNo, *line.Version, BundleVersion)
	}
	if line.Source == nil {
		return fmt.Errorf("line %d: missing source", lineNo)
	}
	for name, value := range map[string]string{
		"organization_id": line.Source.OrganizationID,
		"space_id":        line.Source.SpaceID,
		"space_key":       line.Source.SpaceKey,
	} {
		if value == "" {
			return fmt.Errorf("line %d: source.%s is empty", lineNo, name)
		}
	}
	v.source = line.Source
	return nil
}

func (v *lineValidator) spaceLine(lineNo int, line Line) error {
	if v.source == nil {
		return fmt.Errorf("line %d: %q before %q", lineNo, LineTypeSpace, LineTypeVersion)
	}
	if v.space != nil {
		return fmt.Errorf("line %d: duplicate %q line; one bundle carries exactly one space", lineNo, LineTypeSpace)
	}
	if lineNo != 2 {
		return fmt.Errorf("line %d: %q must be the second line", lineNo, LineTypeSpace)
	}

	space := line.Space
	if space.Team == "" {
		return fmt.Errorf("line %d: space.team is empty", lineNo)
	}
	if space.Title == "" {
		return fmt.Errorf("line %d: space.title is empty", lineNo)
	}
	if err := checkRuneLimit("space.title", space.Title); err != nil {
		return fmt.Errorf("line %d: %w", lineNo, err)
	}
	sourceID, err := requiredStringProp(space.Props, PropImportSourceID)
	if err != nil {
		return fmt.Errorf("line %d: space.props: %w", lineNo, err)
	}
	if sourceID != v.source.SpaceKey {
		return fmt.Errorf("line %d: space.props.%s is %q, want source.space_key %q",
			lineNo, PropImportSourceID, sourceID, v.source.SpaceKey)
	}
	if err := checkPropsSize(fmt.Sprintf("line %d: space.props", lineNo), space.Props); err != nil {
		return err
	}

	v.space = space
	return nil
}

func (v *lineValidator) page(lineNo int, line Line) error {
	if v.space == nil {
		return fmt.Errorf("line %d: %q before %q", lineNo, LineTypePage, LineTypeSpace)
	}
	if v.sawComment {
		return fmt.Errorf("line %d: %q after %q; all pages precede all comments", lineNo, LineTypePage, LineTypePageComment)
	}

	page := line.Page
	if page.Team != v.space.Team {
		return fmt.Errorf("line %d: page.team %q differs from space.team %q", lineNo, page.Team, v.space.Team)
	}
	if page.SpaceImportSourceID != v.source.SpaceKey {
		return fmt.Errorf("line %d: page.space_import_source_id is %q, want source.space_key %q",
			lineNo, page.SpaceImportSourceID, v.source.SpaceKey)
	}
	if page.User == "" {
		return fmt.Errorf("line %d: page.user is empty", lineNo)
	}
	if page.Title == "" {
		return fmt.Errorf("line %d: page.title is empty", lineNo)
	}
	if err := checkRuneLimit("page.title", page.Title); err != nil {
		return fmt.Errorf("line %d: %w", lineNo, err)
	}
	if len(page.Content) > BodyMaxBytes {
		return fmt.Errorf("line %d: page.content is %d bytes, limit is %d", lineNo, len(page.Content), BodyMaxBytes)
	}
	if page.Content == "" {
		return fmt.Errorf("line %d: page.content is empty; an empty page carries the canonical empty document", lineNo)
	}

	sourceID, err := v.pageProps(lineNo, page)
	if err != nil {
		return err
	}
	if _, exists := v.pages[sourceID]; exists {
		return fmt.Errorf("line %d: duplicate page %s %q", lineNo, PropImportSourceID, sourceID)
	}
	if page.ParentImportSourceID != "" {
		if page.ParentImportSourceID == sourceID {
			return fmt.Errorf("line %d: page %q is its own parent", lineNo, sourceID)
		}
		if _, ok := v.pages[page.ParentImportSourceID]; !ok {
			return fmt.Errorf("line %d: page %q references parent %q, which is not an earlier page",
				lineNo, sourceID, page.ParentImportSourceID)
		}
	}

	if err := v.pageAttachments(lineNo, page, sourceID); err != nil {
		return err
	}

	v.pages[sourceID] = page
	v.pageOrder = append(v.pageOrder, sourceID)
	return nil
}

func (v *lineValidator) pageProps(lineNo int, page *PageData) (string, error) {
	prefix := fmt.Sprintf("line %d: page.props", lineNo)

	sourceID, err := requiredStringProp(page.Props, PropImportSourceID)
	if err != nil {
		return "", fmt.Errorf("%s: %w", prefix, err)
	}
	if err = requireExactStringProp(page.Props, PropImportSource, SourceType); err != nil {
		return "", fmt.Errorf("%s: %w", prefix, err)
	}
	spaceKey, err := requiredStringProp(page.Props, PropConfluenceSpaceKey)
	if err != nil {
		return "", fmt.Errorf("%s: %w", prefix, err)
	}
	if spaceKey != v.source.SpaceKey {
		return "", fmt.Errorf("%s: %s is %q, want source.space_key %q", prefix, PropConfluenceSpaceKey, spaceKey, v.source.SpaceKey)
	}
	contentType, err := requiredStringProp(page.Props, PropConfluenceContentType)
	if err != nil {
		return "", fmt.Errorf("%s: %w", prefix, err)
	}
	if contentType != ContentTypePage && contentType != ContentTypeBlogPost {
		return "", fmt.Errorf("%s: %s is %q, want %q or %q", prefix, PropConfluenceContentType, contentType, ContentTypePage, ContentTypeBlogPost)
	}
	if _, ok := page.Props[PropConfluenceAuthorAccountID].(string); !ok {
		return "", fmt.Errorf("%s: %s must be a string", prefix, PropConfluenceAuthorAccountID)
	}
	if err := requireStringSliceProp(page.Props, PropImportLabels); err != nil {
		return "", fmt.Errorf("%s: %w", prefix, err)
	}
	if err := requireConfluenceLabelsProp(page.Props); err != nil {
		return "", fmt.Errorf("%s: %w", prefix, err)
	}
	if _, ok := page.Props[PropConfluenceRestrictions].(map[string]any); !ok {
		return "", fmt.Errorf("%s: %s must be an object", prefix, PropConfluenceRestrictions)
	}
	if err := checkPropsSize(prefix, page.Props); err != nil {
		return "", err
	}
	return sourceID, nil
}

func (v *lineValidator) pageAttachments(lineNo int, page *PageData, pageSourceID string) error {
	for i, att := range page.Attachments {
		prefix := fmt.Sprintf("line %d: page %q attachment %d", lineNo, pageSourceID, i)

		attSourceID, err := requiredStringProp(att.Props, PropImportSourceID)
		if err != nil {
			return fmt.Errorf("%s props: %w", prefix, err)
		}
		if owner, exists := v.attachments[attSourceID]; exists {
			return fmt.Errorf("%s: duplicate attachment %s %q, already listed on page %q",
				prefix, PropImportSourceID, attSourceID, owner)
		}
		if _, err = requiredStringProp(att.Props, PropConfluenceContainerSourceID); err != nil {
			return fmt.Errorf("%s props: %w", prefix, err)
		}
		if _, err = requiredStringProp(att.Props, PropFilename); err != nil {
			return fmt.Errorf("%s props: %w", prefix, err)
		}
		if _, ok := att.Props[PropMediaType].(string); !ok {
			return fmt.Errorf("%s props: %s must be a string", prefix, PropMediaType)
		}
		size, err := intProp(att.Props, PropSize)
		if err != nil {
			return fmt.Errorf("%s props: %w", prefix, err)
		}
		if size < 0 {
			return fmt.Errorf("%s props: %s is %d, want >= 0", prefix, PropSize, size)
		}
		sum, err := requiredStringProp(att.Props, PropSHA256)
		if err != nil {
			return fmt.Errorf("%s props: %w", prefix, err)
		}
		if !hex64Re.MatchString(sum) {
			return fmt.Errorf("%s props: %s must be 64 lowercase hex characters", prefix, PropSHA256)
		}
		if err := checkAttachmentPath(att.Path, pageSourceID, attSourceID); err != nil {
			return fmt.Errorf("%s: %w", prefix, err)
		}

		v.attachments[attSourceID] = pageSourceID
	}
	return nil
}

func (v *lineValidator) comment(lineNo int, line Line) error {
	if v.space == nil {
		return fmt.Errorf("line %d: %q before %q", lineNo, LineTypePageComment, LineTypeSpace)
	}
	v.sawComment = true

	c := line.PageComment
	prefix := fmt.Sprintf("line %d: page_comment", lineNo)

	sourceID, err := requiredStringProp(c.Props, PropImportSourceID)
	if err != nil {
		return fmt.Errorf("%s.props: %w", prefix, err)
	}
	if err := requireExactStringProp(c.Props, PropImportSource, SourceType); err != nil {
		return fmt.Errorf("%s.props: %w", prefix, err)
	}
	if _, ok := c.Props[PropConfluenceAuthorAccountID].(string); !ok {
		return fmt.Errorf("%s.props: %s must be a string", prefix, PropConfluenceAuthorAccountID)
	}
	if err := checkPropsSize(prefix+".props", c.Props); err != nil {
		return err
	}
	if _, exists := v.comments[sourceID]; exists {
		return fmt.Errorf("%s: duplicate comment %s %q", prefix, PropImportSourceID, sourceID)
	}
	if _, ok := v.pages[c.PageImportSourceID]; !ok {
		return fmt.Errorf("%s %q: page_import_source_id %q is not an emitted page", prefix, sourceID, c.PageImportSourceID)
	}
	if c.User == "" {
		return fmt.Errorf("%s %q: user is empty", prefix, sourceID)
	}
	if c.ThreadRootImportSourceID == "" {
		return fmt.Errorf("%s %q: thread_root_import_source_id is empty", prefix, sourceID)
	}

	if c.ParentCommentImportSourceID == "" {
		if c.ThreadRootImportSourceID != sourceID {
			return fmt.Errorf("%s %q: top-level comment thread root is %q, want its own source ID",
				prefix, sourceID, c.ThreadRootImportSourceID)
		}
	} else {
		parent, ok := v.comments[c.ParentCommentImportSourceID]
		if !ok {
			return fmt.Errorf("%s %q: parent %q is not an earlier comment", prefix, sourceID, c.ParentCommentImportSourceID)
		}
		if parent.PageImportSourceID != c.PageImportSourceID {
			return fmt.Errorf("%s %q: parent %q is on page %q, not %q",
				prefix, sourceID, c.ParentCommentImportSourceID, parent.PageImportSourceID, c.PageImportSourceID)
		}
		if c.ThreadRootImportSourceID != parent.ThreadRootImportSourceID {
			return fmt.Errorf("%s %q: thread root is %q, want the parent's thread root %q",
				prefix, sourceID, c.ThreadRootImportSourceID, parent.ThreadRootImportSourceID)
		}
	}

	v.comments[sourceID] = c
	return nil
}

func (v *lineValidator) sentinel(lineNo int) error {
	if v.space == nil {
		return fmt.Errorf("line %d: %q before %q", lineNo, LineTypeResolveSpacePlaceholders, LineTypeSpace)
	}
	v.sawSentinel = true
	return nil
}

func (v *lineValidator) finish() error {
	if v.source == nil {
		return fmt.Errorf("missing %q line", LineTypeVersion)
	}
	if v.space == nil {
		return fmt.Errorf("missing %q line", LineTypeSpace)
	}
	if !v.sawSentinel {
		return fmt.Errorf("missing trailing %q line", LineTypeResolveSpacePlaceholders)
	}
	return nil
}

// checkAttachmentPath enforces the exact contract path shape and rejects every
// path that could escape the bundle root during extraction.
func checkAttachmentPath(p, pageSourceID, attachmentSourceID string) error {
	if p == "" {
		return errors.New("path is empty")
	}
	if strings.ContainsRune(p, 0) {
		return errors.New("path contains NUL")
	}
	if strings.Contains(p, `\`) {
		return fmt.Errorf("path %q contains a backslash", p)
	}
	if strings.HasPrefix(p, "/") {
		return fmt.Errorf("path %q is absolute", p)
	}
	if path.Clean(p) != p {
		return fmt.Errorf("path %q is not in cleaned form", p)
	}
	if !utf8.ValidString(p) {
		return fmt.Errorf("path %q is not valid UTF-8", p)
	}

	segments := strings.Split(p, "/")
	if len(segments) != attachmentPathSegments {
		return fmt.Errorf("path %q must have exactly %d segments", p, attachmentPathSegments)
	}
	for _, segment := range segments {
		if segment == "" || segment == "." || segment == ".." {
			return fmt.Errorf("path %q has an unsafe segment %q", p, segment)
		}
	}
	if segments[0] != AttachmentDirName {
		return fmt.Errorf("path %q must start with %q/", p, AttachmentDirName)
	}
	if segments[1] != pageSourceID {
		return fmt.Errorf("path %q must name page %q in segment 2", p, pageSourceID)
	}
	if segments[2] != attachmentSourceID {
		return fmt.Errorf("path %q must name attachment %q in segment 3", p, attachmentSourceID)
	}
	return nil
}

func checkRuneLimit(field, value string) error {
	if n := utf8.RuneCountInString(value); n > TitleMaxRunes {
		return fmt.Errorf("%s is %d runes, limit is %d", field, n, TitleMaxRunes)
	}
	return nil
}

func checkPropsSize(prefix string, props map[string]any) error {
	encoded, err := MarshalCanonical(props)
	if err != nil {
		return fmt.Errorf("%s: %w", prefix, err)
	}
	if len(encoded) > PropsMaxBytes {
		return fmt.Errorf("%s: %d bytes, limit is %d", prefix, len(encoded), PropsMaxBytes)
	}
	return nil
}

func requiredStringProp(props map[string]any, key string) (string, error) {
	raw, ok := props[key]
	if !ok {
		return "", fmt.Errorf("missing %s", key)
	}
	value, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("%s must be a string", key)
	}
	if value == "" {
		return "", fmt.Errorf("%s is empty", key)
	}
	return value, nil
}

func requireExactStringProp(props map[string]any, key, want string) error {
	value, err := requiredStringProp(props, key)
	if err != nil {
		return err
	}
	if value != want {
		return fmt.Errorf("%s is %q, want %q", key, value, want)
	}
	return nil
}

func requireStringSliceProp(props map[string]any, key string) error {
	raw, ok := props[key]
	if !ok {
		return fmt.Errorf("missing %s", key)
	}
	items, ok := raw.([]any)
	if !ok {
		return fmt.Errorf("%s must be an array", key)
	}
	for i, item := range items {
		if _, ok := item.(string); !ok {
			return fmt.Errorf("%s[%d] must be a string", key, i)
		}
	}
	return nil
}

func requireConfluenceLabelsProp(props map[string]any) error {
	raw, ok := props[PropConfluenceLabels]
	if !ok {
		return fmt.Errorf("missing %s", PropConfluenceLabels)
	}
	items, ok := raw.([]any)
	if !ok {
		return fmt.Errorf("%s must be an array", PropConfluenceLabels)
	}
	for i, item := range items {
		label, ok := item.(map[string]any)
		if !ok {
			return fmt.Errorf("%s[%d] must be an object", PropConfluenceLabels, i)
		}
		for _, field := range []string{"name", "namespace"} {
			if _, ok := label[field].(string); !ok {
				return fmt.Errorf("%s[%d].%s must be a string", PropConfluenceLabels, i, field)
			}
		}
	}
	return nil
}

// intProp reads an integral prop. JSON decoding yields float64, so an integral
// float is accepted and a fractional one is not.
func intProp(props map[string]any, key string) (int64, error) {
	raw, ok := props[key]
	if !ok {
		return 0, fmt.Errorf("missing %s", key)
	}
	switch value := raw.(type) {
	case int:
		return int64(value), nil
	case int64:
		return value, nil
	case float64:
		if value != float64(int64(value)) {
			return 0, fmt.Errorf("%s must be an integer", key)
		}
		return int64(value), nil
	default:
		return 0, fmt.Errorf("%s must be an integer", key)
	}
}

// ValidateManifest checks import-manifest.json in isolation. Cross-checks
// against the JSONL stream live in ValidateBundleFS.
func ValidateManifest(m *Manifest) error {
	if m.Version != ManifestVersion {
		return fmt.Errorf("manifest version is %q, want %q", m.Version, ManifestVersion)
	}
	if m.Generator != Generator {
		return fmt.Errorf("manifest generator is %q, want %q", m.Generator, Generator)
	}
	if m.GeneratorVersion == "" {
		return errors.New("manifest generator_version is empty")
	}
	if m.Source.Type != SourceType {
		return fmt.Errorf("manifest source.type is %q, want %q", m.Source.Type, SourceType)
	}
	for name, value := range map[string]string{
		"source.organization_id": m.Source.OrganizationID,
		"source.space_id":        m.Source.SpaceID,
		"source.space_key":       m.Source.SpaceKey,
		"target.team":            m.Target.Team,
	} {
		if value == "" {
			return fmt.Errorf("manifest %s is empty", name)
		}
	}
	if !hex64Re.MatchString(m.Checksums.JSONLSHA256) {
		return errors.New("manifest checksums.jsonl_sha256 must be 64 lowercase hex characters")
	}
	if !hex64Re.MatchString(m.Checksums.AttachmentsSHA256) {
		return errors.New("manifest checksums.attachments_sha256 must be 64 lowercase hex characters")
	}
	if len(m.Errors) > 0 {
		return fmt.Errorf("manifest reports %d error(s); a bundle with errors is not importable: first is %q",
			len(m.Errors), m.Errors[0].Code)
	}
	if err := validateFidelity(m.Fidelity); err != nil {
		return err
	}
	if err := validateManifestUsers(m.Users); err != nil {
		return err
	}
	return validateIssueOrder(m.Warnings)
}

func validateFidelity(f ManifestFidelity) error {
	for name, value := range map[string]string{
		"pages":             f.Pages,
		"blogposts":         f.BlogPosts,
		"comments":          f.Comments,
		"attachments":       f.Attachments,
		"labels":            f.Labels,
		"page_restrictions": f.PageRestrictions,
		"space_permissions": f.SpacePermissions,
		"external_auth":     f.ExternalAuth,
	} {
		if value == "" {
			return fmt.Errorf("manifest fidelity.%s is empty", name)
		}
	}
	return nil
}

func validateManifestUsers(users []ManifestUser) error {
	valid := map[string]bool{
		UsernameProposalExplicitMapping: true,
		UsernameProposalSourceUsername:  true,
		UsernameProposalSourceEmail:     true,
		UsernameProposalDisplayName:     true,
		UsernameProposalFallback:        true,
	}

	seenAccounts := map[string]bool{}
	seenUsernames := map[string]bool{}
	for i, u := range users {
		if u.AccountID == "" {
			return fmt.Errorf("manifest users[%d].account_id is empty", i)
		}
		if seenAccounts[u.AccountID] {
			return fmt.Errorf("manifest users[%d]: duplicate account_id %q", i, u.AccountID)
		}
		seenAccounts[u.AccountID] = true

		if u.Email == "" {
			return fmt.Errorf("manifest users[%d] (%s): email is empty", i, u.AccountID)
		}
		if u.MattermostUsername == "" {
			return fmt.Errorf("manifest users[%d] (%s): mattermost_username is empty", i, u.AccountID)
		}
		if seenUsernames[u.MattermostUsername] {
			return fmt.Errorf("manifest users[%d]: duplicate mattermost_username %q", i, u.MattermostUsername)
		}
		seenUsernames[u.MattermostUsername] = true

		if !valid[u.UsernameProposalSource] {
			return fmt.Errorf("manifest users[%d] (%s): unknown username_proposal_source %q", i, u.AccountID, u.UsernameProposalSource)
		}
	}
	return nil
}

func validateIssueOrder(warnings []Warning) error {
	sorted := make([]Warning, len(warnings))
	copy(sorted, warnings)
	SortWarnings(sorted)
	for i := range warnings {
		if warnings[i] != sorted[i] {
			return errors.New("manifest warnings are not sorted by code, entity type, source ID, message")
		}
		if len(warnings[i].Message) > MessageMaxBytes {
			return fmt.Errorf("manifest warnings[%d] message exceeds %d bytes", i, MessageMaxBytes)
		}
		if warnings[i].Code == "" {
			return fmt.Errorf("manifest warnings[%d] has an empty code", i)
		}
	}
	return nil
}

// ValidateBundleFS validates a complete bundle laid out as a filesystem. It
// accepts both a *zip.Reader and an os.DirFS view of an unpacked bundle, so the
// producer self-validates the exact bytes it is about to ship and the golden
// fixtures stay reviewable as plain files.
//
// It verifies the manifest, the JSONL checksum, the contract rules, that the
// declared attachment set exactly matches the blobs present, each attachment's
// size and SHA-256, the aggregate attachment checksum, and the emitted counts.
func ValidateBundleFS(fsys fs.FS) (*Manifest, []Line, error) {
	manifest, err := readManifest(fsys)
	if err != nil {
		return nil, nil, err
	}
	if err = ValidateManifest(manifest); err != nil {
		return nil, nil, err
	}

	jsonlBytes, err := fs.ReadFile(fsys, JSONLFilename)
	if err != nil {
		return nil, nil, fmt.Errorf("reading %s: %w", JSONLFilename, err)
	}
	if sum := SHA256Hex(jsonlBytes); sum != manifest.Checksums.JSONLSHA256 {
		return nil, nil, fmt.Errorf("%s checksum is %s, manifest declares %s", JSONLFilename, sum, manifest.Checksums.JSONLSHA256)
	}

	lines, err := DecodeJSONL(strings.NewReader(string(jsonlBytes)))
	if err != nil {
		return nil, nil, err
	}
	if err := ValidateLines(lines); err != nil {
		return nil, nil, err
	}
	if err := checkManifestAgainstLines(manifest, lines); err != nil {
		return nil, nil, err
	}
	if err := checkBundleEntries(fsys, lines); err != nil {
		return nil, nil, err
	}
	if err := checkAttachmentBlobs(fsys, manifest, lines); err != nil {
		return nil, nil, err
	}
	return manifest, lines, nil
}

func readManifest(fsys fs.FS) (*Manifest, error) {
	f, err := fsys.Open(ManifestFilename)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", ManifestFilename, err)
	}
	defer func() { _ = f.Close() }()
	return DecodeManifest(f)
}

func checkManifestAgainstLines(m *Manifest, lines []Line) error {
	source := lines[0].Source
	if m.Source.OrganizationID != source.OrganizationID ||
		m.Source.SpaceID != source.SpaceID ||
		m.Source.SpaceKey != source.SpaceKey {
		return errors.New("manifest source does not match the version line source")
	}

	// Every author named by the stream must be resolvable from the manifest, or
	// the importer has no way to map the content to a destination user.
	knownUsernames := map[string]bool{}
	knownAccounts := map[string]bool{}
	for _, u := range m.Users {
		knownUsernames[u.MattermostUsername] = true
		knownAccounts[u.AccountID] = true
	}

	var pages, blogposts, comments, attachments int
	for _, line := range lines {
		switch line.Type {
		case LineTypePage:
			if line.Page.Props[PropConfluenceContentType] == ContentTypeBlogPost {
				blogposts++
			} else {
				pages++
			}
			attachments += len(line.Page.Attachments)
			if line.Page.Team != m.Target.Team {
				return fmt.Errorf("page team %q does not match manifest target.team %q", line.Page.Team, m.Target.Team)
			}
			if !knownUsernames[line.Page.User] {
				return fmt.Errorf("page user %q is not listed in manifest users", line.Page.User)
			}
			if err := checkKnownAccount(knownAccounts, "page", line.Page.Props); err != nil {
				return err
			}
		case LineTypePageComment:
			comments++
			if !knownUsernames[line.PageComment.User] {
				return fmt.Errorf("comment user %q is not listed in manifest users", line.PageComment.User)
			}
			if err := checkKnownAccount(knownAccounts, "comment", line.PageComment.Props); err != nil {
				return err
			}
		}
	}

	for _, check := range []struct {
		name          string
		got, declared int
	}{
		{"spaces_emitted", 1, m.Counts.SpacesEmitted},
		{"pages_emitted", pages, m.Counts.PagesEmitted},
		{"blogposts_emitted", blogposts, m.Counts.BlogPostsEmitted},
		{"comments_emitted", comments, m.Counts.CommentsEmitted},
		{"attachments_emitted", attachments, m.Counts.AttachmentsEmitted},
		{"users_emitted", len(m.Users), m.Counts.UsersEmitted},
	} {
		if check.got != check.declared {
			return fmt.Errorf("manifest counts.%s is %d, stream contains %d", check.name, check.declared, check.got)
		}
	}
	return nil
}

// checkKnownAccount allows an absent source author, which the exporter records
// as an empty account ID, but rejects one that names a user the manifest omits.
func checkKnownAccount(known map[string]bool, entity string, props map[string]any) error {
	accountID, _ := props[PropConfluenceAuthorAccountID].(string)
	if accountID != "" && !known[accountID] {
		return fmt.Errorf("%s %s %q is not listed in manifest users", entity, PropConfluenceAuthorAccountID, accountID)
	}
	return nil
}

// checkBundleEntries rejects any entry that is neither a required root file nor
// a declared attachment blob, so a bundle can never smuggle unreferenced bytes
// past the importer.
func checkBundleEntries(fsys fs.FS, lines []Line) error {
	declared := map[string]bool{}
	for _, line := range lines {
		if line.Type != LineTypePage {
			continue
		}
		for _, att := range line.Page.Attachments {
			declared[att.Path] = true
		}
	}

	present := map[string]bool{}
	err := fs.WalkDir(fsys, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || name == "." {
			return nil
		}
		switch {
		case name == JSONLFilename, name == ManifestFilename:
			return nil
		case strings.HasPrefix(name, AttachmentDirName+"/"):
			present[name] = true
			return nil
		default:
			return fmt.Errorf("unexpected bundle entry %q", name)
		}
	})
	if err != nil {
		return err
	}

	var missing, extra []string
	for name := range declared {
		if !present[name] {
			missing = append(missing, name)
		}
	}
	for name := range present {
		if !declared[name] {
			extra = append(extra, name)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 {
		return fmt.Errorf("bundle is missing declared attachment blob(s): %s", strings.Join(missing, ", "))
	}
	if len(extra) > 0 {
		return fmt.Errorf("bundle carries undeclared attachment blob(s): %s", strings.Join(extra, ", "))
	}
	return nil
}

func checkAttachmentBlobs(fsys fs.FS, m *Manifest, lines []Line) error {
	var blobs []AttachmentBlob
	for _, line := range lines {
		if line.Type != LineTypePage {
			continue
		}
		for _, att := range line.Page.Attachments {
			declaredSize, err := intProp(att.Props, PropSize)
			if err != nil {
				return err
			}
			body, err := fs.ReadFile(fsys, att.Path)
			if err != nil {
				return fmt.Errorf("reading attachment %q: %w", att.Path, err)
			}
			if int64(len(body)) != declaredSize {
				return fmt.Errorf("attachment %q is %d bytes, props declare %d", att.Path, len(body), declaredSize)
			}
			if sum := SHA256Hex(body); sum != att.Props[PropSHA256] {
				return fmt.Errorf("attachment %q checksum is %s, props declare %v", att.Path, sum, att.Props[PropSHA256])
			}
			blobs = append(blobs, AttachmentBlob{
				Path: att.Path,
				Size: declaredSize,
				Open: func() (io.ReadCloser, error) { return fsys.Open(att.Path) },
			})
		}
	}

	aggregate, err := AttachmentsSHA256(blobs)
	if err != nil {
		return err
	}
	if aggregate != m.Checksums.AttachmentsSHA256 {
		return fmt.Errorf("aggregate attachment checksum is %s, manifest declares %s", aggregate, m.Checksums.AttachmentsSHA256)
	}
	return nil
}
