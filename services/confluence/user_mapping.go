package confluence

import (
	"bufio"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"

	"github.com/mattermost/mattermost/server/public/model"
)

// User is one Confluence user referenced by selected content.
//
// AccountID is the canonical identity every mapping is keyed by. It is the
// Atlassian account ID when there is one, and a deterministic fallback
// otherwise, so a user without one still maps consistently across re-exports.
type User struct {
	Key EntityKey

	AccountID          string
	ConfluenceUserKey  string
	ConfluenceUsername string
	DisplayName        string
	ExternalID         string
	Active             bool

	Email              string
	EmailIsPlaceholder bool

	MattermostUsername     string
	UsernameProposalSource string
}

// PlaceholderEmailDomain is reserved by RFC 6761 and can never receive mail, so
// a placeholder address can never reach a real person by accident.
const PlaceholderEmailDomain = "users.invalid"

// usernameFallbackPrefix names a user by nothing but their account hash, which
// is the last resort when no username, email, or display name is usable.
const usernameFallbackPrefix = "confluence_user_"

// ConfluenceUserImpl and InternalUser property names.
const (
	userPropName               = "name"
	userPropLowerName          = "lowerName"
	userPropAtlassianAccountID = "atlassianAccountId"
	userPropDisplayName        = "displayName"
	userPropEmailAddress       = "emailAddress"
	userPropExternalID         = "externalId"
	userPropActive             = "active"
)

// UserRefs accumulates the users referenced by selected content.
//
// Only referenced users are exported. A Confluence site's directory holds every
// account that ever touched it, and the discovery sample carries 350 for a
// space whose content names a handful.
type UserRefs struct {
	keys map[EntityKey]bool
}

func NewUserRefs() *UserRefs {
	return &UserRefs{keys: map[EntityKey]bool{}}
}

// Add records user references, ignoring the zero key that an absent reference
// decodes to.
func (r *UserRefs) Add(keys ...EntityKey) {
	for _, key := range keys {
		if !key.IsZero() {
			r.keys[key] = true
		}
	}
}

// AddFromContent records the creators and modifiers of the selected pages.
func (r *UserRefs) AddFromContent(content *ContentSelection) {
	for _, page := range content.Pages {
		r.Add(page.CreatorKey, page.LastModifierKey)
	}
}

// AddFromDependencies records comment authors and attachment authors.
func (r *UserRefs) AddFromDependencies(deps *DependencySelection) {
	for _, comment := range deps.Comments {
		r.Add(comment.CreatorKey, comment.LastModifierKey)
	}
	for _, attachment := range deps.AttachmentsByID {
		r.Add(attachment.CreatorKey, attachment.LastModifierKey)
	}
}

// Len is the number of distinct referenced users.
func (r *UserRefs) Len() int { return len(r.keys) }

// Has reports whether a key was referenced.
func (r *UserRefs) Has(key EntityKey) bool { return r.keys[key] }

// SelectUsers reads the referenced users and resolves each one's canonical
// account ID, email, and proposed Mattermost username.
//
// It runs two streaming passes. The first reads the referenced
// ConfluenceUserImpl objects, which is where the account ID lives; the second
// reads the InternalUser rows they join to, which is where the display name,
// email, and active flag live. Two passes rather than one because the join key
// is only known after the first, and buffering every InternalUser on a large
// site would hold the whole directory in memory.
func SelectUsers(
	archive *SourceArchive,
	organizationID string,
	refs *UserRefs,
	mapping *UserMapping,
) ([]*User, []Warning, error) {
	users, byLowerName, err := readConfluenceUsers(archive, organizationID, refs)
	if err != nil {
		return nil, nil, err
	}
	if err := applyInternalUsers(archive, byLowerName); err != nil {
		return nil, nil, err
	}

	sort.Slice(users, func(i, j int) bool { return users[i].AccountID < users[j].AccountID })

	warnings := assignEmails(users, organizationID)
	if err := assignUsernames(users, mapping); err != nil {
		return nil, nil, err
	}
	return users, warnings, nil
}

func readConfluenceUsers(
	archive *SourceArchive,
	organizationID string,
	refs *UserRefs,
) ([]*User, map[string][]*User, error) {
	entities, err := archive.OpenEntities()
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = entities.Close() }()

	decoder := NewObjectDecoder(entities)
	decoder.SetAccept(func(class ClassRef) bool { return class == ClassConfluenceUser })

	var users []*User
	byLowerName := map[string][]*User{}

	for {
		object, err := decoder.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, nil, err
		}
		if !refs.Has(object.Key) {
			continue
		}

		user := &User{
			Key:                object.Key,
			ConfluenceUserKey:  object.Key.ID,
			ConfluenceUsername: object.ScalarValue(userPropName),
			// Active defaults to true: a user referenced by current content is
			// assumed active unless an InternalUser row says otherwise.
			Active: true,
		}
		user.AccountID = canonicalAccountID(organizationID, object.Key, object.ScalarValue(userPropAtlassianAccountID))

		users = append(users, user)

		lowerName := strings.ToLower(strings.TrimSpace(object.ScalarValue(userPropLowerName)))
		if lowerName == "" {
			lowerName = strings.ToLower(strings.TrimSpace(user.ConfluenceUsername))
		}
		if lowerName != "" {
			byLowerName[lowerName] = append(byLowerName[lowerName], user)
		}
	}
	return users, byLowerName, nil
}

// applyInternalUsers joins the Crowd directory rows onto the Confluence users.
//
// The join key is lowerName, which section 9 specifies and the discovery sample
// confirms: a Confluence user's name is their email for directory-backed
// accounts and their account ID otherwise, and InternalUser.lowerName matches
// either spelling.
func applyInternalUsers(archive *SourceArchive, byLowerName map[string][]*User) error {
	if len(byLowerName) == 0 {
		return nil
	}

	entities, err := archive.OpenEntities()
	if err != nil {
		return err
	}
	defer func() { _ = entities.Close() }()

	decoder := NewObjectDecoder(entities)
	decoder.SetAccept(func(class ClassRef) bool { return class == ClassInternalUser })

	for {
		object, err := decoder.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}

		lowerName := strings.ToLower(strings.TrimSpace(object.ScalarValue(userPropLowerName)))
		matches, ok := byLowerName[lowerName]
		if !ok {
			continue
		}

		for _, user := range matches {
			user.DisplayName = strings.TrimSpace(object.ScalarValue(userPropDisplayName))
			user.ExternalID = strings.TrimSpace(object.ScalarValue(userPropExternalID))
			user.Email = strings.TrimSpace(object.ScalarValue(userPropEmailAddress))
			if active, present := object.Scalar(userPropActive); present {
				user.Active = active == "true"
			}
		}
	}
}

// canonicalAccountID resolves the identity every source mapping is keyed by.
//
// The hashed fallback is scoped by organization ID so two Confluence sites
// cannot collide, and is deterministic so re-exporting the same backup produces
// the same identity and the importer reuses its mapping instead of creating a
// second user.
func canonicalAccountID(organizationID string, key EntityKey, atlassianAccountID string) string {
	if trimmed := strings.TrimSpace(atlassianAccountID); trimmed != "" {
		return trimmed
	}
	if key.ID != "" {
		return key.ID
	}

	sum := sha256.Sum256([]byte(strings.Join(
		[]string{organizationID, key.Package, key.Class, key.ID}, "\x00")))
	return hex.EncodeToString(sum[:])
}

// assignEmails fills in a deterministic placeholder for every user the source
// has no real address for.
//
// The address is derived from the organization and account rather than being
// sequential, so it is stable across re-exports and the importer can recognise
// its own earlier creation. It is never used to match an existing Mattermost
// user.
func assignEmails(users []*User, organizationID string) []Warning {
	var warnings []Warning
	for _, user := range users {
		if user.Email != "" {
			continue
		}

		sum := sha256.Sum256([]byte(organizationID + "\x00" + user.AccountID))
		user.Email = fmt.Sprintf("confluence-%s@%s", hex.EncodeToString(sum[:])[:24], PlaceholderEmailDomain)
		user.EmailIsPlaceholder = true

		warnings = append(warnings, Warning{
			Code:       WarnUserPlaceholderEmail,
			EntityType: "user",
			SourceID:   user.AccountID,
			Message:    "source user has no email address; a deterministic placeholder was generated",
		})
	}
	return warnings
}

// assignUsernames proposes a Mattermost username for every user and resolves
// collisions inside the bundle.
//
// Users are processed in canonical account-ID order, so which of two colliding
// users keeps the unsuffixed name does not depend on file order.
func assignUsernames(users []*User, mapping *UserMapping) error {
	taken := map[string]bool{}

	// Explicit mappings are honoured before anything else can claim their name.
	// Suffixing a name the operator asked for would silently disobey them.
	for _, user := range users {
		explicit, ok := mapping.Lookup(user)
		if !ok {
			continue
		}
		if taken[explicit] {
			return fmt.Errorf("user mapping assigns mattermost_username %q to more than one Confluence user; "+
				"the second is account %s", explicit, user.AccountID)
		}
		taken[explicit] = true
		user.MattermostUsername = explicit
		user.UsernameProposalSource = UsernameProposalExplicitMapping
	}

	for _, user := range users {
		if user.MattermostUsername != "" {
			continue
		}
		proposal, source := proposeUsername(user)
		user.MattermostUsername = allocateUsername(proposal, taken)
		user.UsernameProposalSource = source
		taken[user.MattermostUsername] = true
	}
	return nil
}

// proposeUsername applies the section 9 precedence.
//
// A Confluence username is only used when it is already a valid Mattermost
// username. For directory-backed accounts Confluence's username is the user's
// email, and mangling that into "name-example.com" would be worse than the
// email's local part, which the next rule produces.
func proposeUsername(user *User) (proposal, source string) {
	if candidate := strings.ToLower(strings.TrimSpace(user.ConfluenceUsername)); model.IsValidUsername(candidate) {
		return candidate, UsernameProposalSourceUsername
	}

	if !user.EmailIsPlaceholder {
		if local, _, found := strings.Cut(user.Email, "@"); found {
			if candidate := sanitizeUsername(local); candidate != "" {
				return candidate, UsernameProposalSourceEmail
			}
		}
	}

	if candidate := sanitizeUsername(user.DisplayName); candidate != "" {
		return candidate, UsernameProposalDisplayName
	}

	sum := sha256.Sum256([]byte(user.AccountID))
	return usernameFallbackPrefix + hex.EncodeToString(sum[:])[:12], UsernameProposalFallback
}

// sanitizeUsername reduces arbitrary text to something Mattermost accepts, or
// to "" when nothing usable survives.
func sanitizeUsername(raw string) string {
	lowered := strings.ToLower(strings.TrimSpace(raw))

	var b strings.Builder
	b.Grow(len(lowered))
	for _, r := range lowered {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		case r == ' ':
			b.WriteRune('-')
		}
	}

	candidate := strings.Trim(b.String(), ".-_")
	if len(candidate) > model.UserNameMaxLength {
		candidate = strings.Trim(candidate[:model.UserNameMaxLength], ".-_")
	}
	if !model.IsValidUsername(candidate) {
		return ""
	}
	return candidate
}

// allocateUsername returns the first free name, suffixing _2, _3, and so on.
func allocateUsername(proposal string, taken map[string]bool) string {
	if !taken[proposal] {
		return proposal
	}
	for n := 2; ; n++ {
		suffix := fmt.Sprintf("_%d", n)
		base := proposal
		if len(base)+len(suffix) > model.UserNameMaxLength {
			base = strings.Trim(base[:model.UserNameMaxLength-len(suffix)], ".-_")
		}
		candidate := base + suffix
		if !taken[candidate] {
			return candidate
		}
	}
}

// UserMapping is the explicit --user-mapping CSV. It overrides every
// backup-derived username proposal.
type UserMapping struct {
	byAccountID map[string]string
	byUserKey   map[string]string
	byEmail     map[string]string
	byUsername  map[string]string
}

// userMappingHeader is exact and mandatory. Accepting a reordered or partial
// header would silently mis-assign columns.
var userMappingHeader = []string{
	"confluence_account_id",
	"confluence_user_key",
	"confluence_username",
	"confluence_email",
	"mattermost_username",
}

// NewUserMapping returns an empty mapping, which matches nothing.
func NewUserMapping() *UserMapping {
	return &UserMapping{
		byAccountID: map[string]string{},
		byUserKey:   map[string]string{},
		byEmail:     map[string]string{},
		byUsername:  map[string]string{},
	}
}

// Len is the number of distinct selectors the mapping carries.
func (m *UserMapping) Len() int {
	return len(m.byAccountID) + len(m.byUserKey) + len(m.byEmail) + len(m.byUsername)
}

// Lookup finds the operator-supplied Mattermost username for a user.
//
// Selectors are tried most specific first: an account ID identifies exactly one
// Atlassian user forever, while a username or email can be reassigned.
func (m *UserMapping) Lookup(user *User) (string, bool) {
	if m == nil {
		return "", false
	}
	for _, candidate := range []struct {
		table map[string]string
		key   string
	}{
		{m.byAccountID, user.AccountID},
		{m.byUserKey, user.ConfluenceUserKey},
		{m.byEmail, foldSelector(user.Email)},
		{m.byUsername, foldSelector(user.ConfluenceUsername)},
	} {
		if candidate.key == "" {
			continue
		}
		if target, ok := candidate.table[candidate.key]; ok {
			return target, true
		}
	}
	return "", false
}

func foldSelector(raw string) string { return strings.ToLower(strings.TrimSpace(raw)) }

// ParseUserMapping reads the explicit mapping CSV.
func ParseUserMapping(r io.Reader) (*UserMapping, error) {
	reader := csv.NewReader(stripBOM(r))
	// The header is read with a variable field count so a wrong header is
	// reported as a wrong header, naming the columns expected, rather than as a
	// generic field-count error. Data rows are then pinned to the exact width.
	reader.FieldsPerRecord = -1

	header, err := reader.Read()
	if errors.Is(err, io.EOF) {
		return nil, errors.New("user mapping is empty; the header row is mandatory")
	}
	if err != nil {
		return nil, fmt.Errorf("reading user mapping header: %w", err)
	}
	for i := range header {
		header[i] = strings.TrimSpace(header[i])
	}
	if !slices.Equal(header, userMappingHeader) {
		return nil, fmt.Errorf("user mapping header must be exactly %q, got %q",
			strings.Join(userMappingHeader, ","), strings.Join(header, ","))
	}

	reader.FieldsPerRecord = len(userMappingHeader)

	mapping := NewUserMapping()
	for line := 2; ; line++ {
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			return mapping, nil
		}
		if err != nil {
			return nil, fmt.Errorf("reading user mapping line %d: %w", line, err)
		}
		if err := mapping.addRow(line, record); err != nil {
			return nil, err
		}
	}
}

func (m *UserMapping) addRow(line int, record []string) error {
	accountID := strings.TrimSpace(record[0])
	userKey := strings.TrimSpace(record[1])
	username := foldSelector(record[2])
	email := foldSelector(record[3])
	target := strings.TrimSpace(record[4])

	if target == "" {
		return fmt.Errorf("user mapping line %d: mattermost_username is required", line)
	}
	if !model.IsValidUsername(strings.ToLower(target)) {
		return fmt.Errorf("user mapping line %d: %q is not a valid Mattermost username", line, target)
	}
	target = strings.ToLower(target)

	if accountID == "" && userKey == "" && username == "" && email == "" {
		return fmt.Errorf("user mapping line %d: at least one Confluence selector column is required", line)
	}

	for _, selector := range []struct {
		table map[string]string
		name  string
		key   string
	}{
		{m.byAccountID, "confluence_account_id", accountID},
		{m.byUserKey, "confluence_user_key", userKey},
		{m.byUsername, "confluence_username", username},
		{m.byEmail, "confluence_email", email},
	} {
		if selector.key == "" {
			continue
		}
		// An identical repeated row is harmless; a contradictory one is not,
		// and picking either target would be a silent guess.
		if existing, ok := selector.table[selector.key]; ok && existing != target {
			return fmt.Errorf("user mapping line %d: %s %q is already mapped to %q, cannot also map it to %q",
				line, selector.name, selector.key, existing, target)
		}
		selector.table[selector.key] = target
	}
	return nil
}

// stripBOM drops a UTF-8 byte order mark, which spreadsheet tools add and which
// would otherwise become part of the first header name and fail the exact
// header check for a reason nobody can see in a diff.
func stripBOM(r io.Reader) io.Reader {
	const bom = "\xef\xbb\xbf"

	buffered := bufio.NewReader(r)
	prefix, err := buffered.Peek(len(bom))
	if err == nil && string(prefix) == bom {
		_, _ = buffered.Discard(len(bom))
	}
	return buffered
}
