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
