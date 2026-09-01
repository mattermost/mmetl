package confluence

// Space is one Confluence space in the source backup.
//
// SourceID is the numeric Space object ID and is the only identity the bundle
// scopes source IDs by. SpaceKey is the human-readable key; it is stable enough
// to show a human and to select on, but not stable enough to key mappings by,
// because a space can be renamed.
type Space struct {
	Key EntityKey

	SourceID string
	SpaceKey string
	LowerKey string
	Name     string

	// Type is Confluence's spaceType, such as "collaboration" or "personal".
	Type string

	// Status is Confluence's spaceStatus, such as "CURRENT" or "ARCHIVED".
	Status string

	// HomePageKey is the space's home page. It is zero for a space that has
	// none, which the sample export does contain.
	HomePageKey EntityKey

	// DescriptionKey is the space's SpaceDescription. Attachments hanging off it
	// are remapped onto the home page.
	DescriptionKey EntityKey
}

// HasHomePage reports whether the space declares a home page.
func (s Space) HasHomePage() bool { return !s.HomePageKey.IsZero() }

// HasDescription reports whether the space declares a description.
func (s Space) HasDescription() bool { return !s.DescriptionKey.IsZero() }

// Page is one canonical Confluence page or blog post selected for export.
//
// It carries metadata only. Bodies are read in a later pass and are never held
// alongside the whole page set, so BodyContentKeys is a handle rather than
// content.
type Page struct {
	Key      EntityKey
	SourceID string

	// ContentType is ContentTypePage or ContentTypeBlogPost. Blog posts import
	// as ordinary Docs pages and are distinguished only by this prop.
	ContentType string

	Title    string
	SpaceKey EntityKey

	// SourceParentKey is the parent Confluence declared. ParentSourceID is the
	// parent actually emitted, which differs when a parent was excluded or when
	// the page was flattened to fit the destination depth limit.
	SourceParentKey EntityKey
	ParentSourceID  string

	// Depth is the output depth, with a root page at 1.
	Depth int

	CreatorKey      EntityKey
	LastModifierKey EntityKey

	// CreatedAt and UpdatedAt are Unix milliseconds. HasCreatedAt and
	// HasUpdatedAt distinguish "no timestamp" from the epoch.
	CreatedAt    int64
	HasCreatedAt bool
	UpdatedAt    int64
	HasUpdatedAt bool

	// Position orders siblings. Confluence omits it on blog posts.
	Position    int64
	HasPosition bool

	BodyContentKeys     []EntityKey
	ContentPropertyKeys []EntityKey

	// Labels are the Confluence labels applied to this page, in a stable order.
	Labels []Label

	// Restrictions is always empty in this iteration; see the Restrictions doc
	// comment for why.
	Restrictions Restrictions

	// Children is the output hierarchy, in emission order.
	Children []*Page
}

// IsBlogPost reports whether the page came from a Confluence blog post.
func (p *Page) IsBlogPost() bool { return p.ContentType == ContentTypeBlogPost }

// Label is one Confluence label applied to a page.
//
// Namespace matters and is preserved. Confluence's "my" namespace holds one
// user's private favourites rather than shared content metadata, and the
// discovery sample is 10 of those to 1 real team label, so a consumer that
// ignored the namespace would present someone's private bookmark as a property
// of the page.
type Label struct {
	Name      string
	Namespace string

	// OwnerKey is the user who applied the label, which for a personal-namespace
	// label is the only person it means anything to.
	OwnerKey EntityKey
}

// Restrictions is the page-restriction shape the contract carries.
//
// Extraction is unverified: the discovery sample contains no page-restriction
// object at all, only space-level SpacePermission rows, which this iteration
// does not import. The type and its plumbing exist so the importer can store
// restrictions the day a real restricted-page fixture proves how Confluence
// spells them; until then every export reports the restriction fidelity as
// unverified and no bundle claims to have found any.
type Restrictions struct {
	ViewUsers  []string `json:"view_users"`
	ViewGroups []string `json:"view_groups"`
	EditUsers  []string `json:"edit_users"`
	EditGroups []string `json:"edit_groups"`
}

// IsEmpty reports whether no restriction was recognized.
func (r Restrictions) IsEmpty() bool {
	return len(r.ViewUsers) == 0 && len(r.ViewGroups) == 0 &&
		len(r.EditUsers) == 0 && len(r.EditGroups) == 0
}

// Label and Labelling property names.
const (
	labelPropName          = "name"
	labelPropNamespace     = "namespace"
	labelPropOwningUser    = "owningUser"
	labellingPropLabel     = "label"
	labellingPropContent   = "content"
	labelNamespacePersonal = "my"
)

// Comment is one Confluence comment selected for export. It becomes a
// Mattermost post in the destination Space's backing channel.
type Comment struct {
	Key      EntityKey
	SourceID string

	// PageSourceID is the emitted page the comment hangs from.
	PageSourceID string

	// ParentSourceID is the immediate parent comment, empty for a top-level
	// comment. ThreadRootSourceID is the top-level comment of the thread and is
	// never empty; for a top-level comment it is its own source ID.
	//
	// Mattermost threads are flat: every reply's RootId is the thread root, not
	// the immediate parent, so both are kept.
	ParentSourceID     string
	ThreadRootSourceID string

	CreatorKey      EntityKey
	LastModifierKey EntityKey

	CreatedAt    int64
	HasCreatedAt bool
	UpdatedAt    int64
	HasUpdatedAt bool

	// IsResolved mirrors Confluence's inline-comment resolution state. No
	// resolution marker appears in the discovery sample, so this stays false
	// until a fixture proves how Confluence spells it.
	IsResolved bool

	BodyContentKeys     []EntityKey
	ContentPropertyKeys []EntityKey
}

// Attachment is one Confluence attachment selected for export.
//
// ContainerSourceID is where Confluence hung it, which for a space-description
// attachment is not a page at all. PageSourceID is the page it is emitted
// against.
type Attachment struct {
	Key      EntityKey
	SourceID string

	ContainerKey      EntityKey
	ContainerSourceID string
	PageSourceID      string

	// Version selects the exact blob in the archive. An export carries every
	// version, and only the current one is copied.
	Version int

	// Filename is Confluence's attachment title, before sanitation.
	Filename string

	MediaType string
	Size      int64

	// ArchivePath is the entry inside the source ZIP.
	ArchivePath string

	CreatorKey      EntityKey
	LastModifierKey EntityKey

	CreatedAt    int64
	HasCreatedAt bool
	UpdatedAt    int64
	HasUpdatedAt bool

	ContentPropertyKeys []EntityKey
}

// Attachment metadata carried as Confluence content properties rather than as
// object scalars.
const (
	attachmentPropMediaType = "MEDIA_TYPE"
	attachmentPropFileSize  = "FILESIZE"
)

// ContentProperty scalar names.
const (
	contentPropertyName        = "name"
	contentPropertyStringValue = "stringValue"
	contentPropertyLongValue   = "longValue"
)

// contentPropVersion is the attachment version, which selects its archive blob.
const contentPropVersion = "version"

// Page and content property names in a Confluence Cloud XML backup.
const (
	contentPropTitle              = "title"
	contentPropStatus             = "contentStatus"
	contentPropOriginalVersion    = "originalVersion"
	contentPropOriginalVersionID  = "originalVersionId"
	contentPropCreator            = "creator"
	contentPropLastModifier       = "lastModifier"
	contentPropCreationDate       = "creationDate"
	contentPropLastModification   = "lastModificationDate"
	contentPropPosition           = "position"
	contentPropParent             = "parent"
	contentPropContainerContent   = "containerContent"
	contentCollBodyContents       = "bodyContents"
	contentCollContentProperties  = "contentProperties"
	contentCollHistoricalVersions = "historicalVersions"
)

// contentStatusCurrent is the only content status this exporter emits.
const contentStatusCurrent = "current"

// Space property names in a Confluence Cloud XML backup.
const (
	spacePropName        = "name"
	spacePropKey         = "key"
	spacePropLowerKey    = "lowerKey"
	spacePropType        = "spaceType"
	spacePropStatus      = "spaceStatus"
	spacePropHomePage    = "homePage"
	spacePropDescription = "description"

	// contentPropSpace is the back-reference every space-scoped content object
	// carries.
	contentPropSpace = "space"
)
