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

	// Children is the output hierarchy, in emission order.
	Children []*Page
}

// IsBlogPost reports whether the page came from a Confluence blog post.
func (p *Page) IsBlogPost() bool { return p.ContentType == ContentTypeBlogPost }

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
