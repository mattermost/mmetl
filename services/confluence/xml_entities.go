package confluence

import (
	"fmt"
	"strings"
)

// EntityKey is the identity of one Hibernate object: its package, its class,
// the name of its id element, and the id value.
//
// The id element name matters. Most Confluence classes key on "id", but
// ConfluenceUserImpl keys on "key", and treating those as the same namespace
// would silently join unrelated entities.
type EntityKey struct {
	Package string
	Class   string
	IDName  string
	ID      string
}

func (k EntityKey) String() string {
	return fmt.Sprintf("%s.%s[%s=%s]", k.Package, k.Class, k.IDName, k.ID)
}

// IsZero reports whether the key names nothing.
func (k EntityKey) IsZero() bool {
	return k.Package == "" && k.Class == "" && k.IDName == "" && k.ID == ""
}

// ClassRef is a package and class pair. Class names alone are not unique across
// Confluence packages, so every class comparison goes through this type.
type ClassRef struct {
	Package string
	Class   string
}

func (c ClassRef) String() string { return c.Package + "." + c.Class }

// Matches reports whether the key belongs to this class.
func (c ClassRef) Matches(k EntityKey) bool {
	return k.Package == c.Package && k.Class == c.Class
}

// Confluence packages observed in a Cloud XML backup.
const (
	PkgConfluencePages   = "com.atlassian.confluence.pages"
	PkgConfluenceSpaces  = "com.atlassian.confluence.spaces"
	PkgConfluenceCore    = "com.atlassian.confluence.core"
	PkgConfluenceUser    = "com.atlassian.confluence.user"
	PkgConfluenceContent = "com.atlassian.confluence.content"
	PkgConfluenceLabels  = "com.atlassian.confluence.labels"
	PkgCrowdUser         = "com.atlassian.crowd.model.user"
)

// The classes this exporter reads. Everything else in the backup is ignored.
var (
	ClassPage             = ClassRef{PkgConfluencePages, "Page"}
	ClassBlogPost         = ClassRef{PkgConfluencePages, "BlogPost"}
	ClassComment          = ClassRef{PkgConfluencePages, "Comment"}
	ClassAttachment       = ClassRef{PkgConfluencePages, "Attachment"}
	ClassSpace            = ClassRef{PkgConfluenceSpaces, "Space"}
	ClassSpaceDescription = ClassRef{PkgConfluenceSpaces, "SpaceDescription"}
	ClassBodyContent      = ClassRef{PkgConfluenceCore, "BodyContent"}
	ClassContentProperty  = ClassRef{PkgConfluenceContent, "ContentProperty"}
	ClassLabel            = ClassRef{PkgConfluenceLabels, "Label"}
	ClassLabelling        = ClassRef{PkgConfluenceLabels, "Labelling"}

	// ClassConfluenceUser keys on "key", not "id".
	ClassConfluenceUser = ClassRef{PkgConfluenceUser, "ConfluenceUserImpl"}
	ClassInternalUser   = ClassRef{PkgCrowdUser, "InternalUser"}
)

// RawScalar is one scalar property. A missing property and a property that is
// present and empty are different facts: Confluence writes `<property
// name="originalVersionId"/>` to mean "no original version", which the
// canonical-content predicate must distinguish from the property being absent
// on a class that never carries it.
type RawScalar struct {
	Present bool
	Value   string
}

// RawID is one identity part: an <id> element, or one <property> inside a
// <composite-id>.
type RawID struct {
	Name  string
	Value string
}

// compositeIDName is the synthetic id name given to composite-keyed objects, so
// their keys stay distinguishable from single-id objects of the same class.
const compositeIDName = "composite-id"

// compositeIDSeparator joins composite parts into one diagnostic key. NUL
// cannot appear in XML character data, so no pair of distinct composite keys
// can join to the same string.
const compositeIDSeparator = "\x00"

// RawObject is one decoded top-level Hibernate object, untyped. Mapping it onto
// a Confluence entity is the caller's job; the decoder makes no assumptions
// about which classes or property names exist.
type RawObject struct {
	Key EntityKey

	// IDs holds every identity part in source order.
	IDs []RawID

	// Composite marks an object keyed by <composite-id> rather than <id>.
	// Confluence uses composite keys for join-table rows such as
	// BucketPropertySetItem and SpacePermission. This iteration decodes them for
	// diagnostics and imports none of them.
	Composite bool

	Scalars     map[string]RawScalar
	References  map[string]EntityKey
	Collections map[string][]EntityKey

	// Offset is the byte offset of the object's start tag in entities.xml,
	// which is the only stable way to point a human at one of 5,000 objects.
	Offset int64
}

// Class is the object's package and class.
func (o *RawObject) Class() ClassRef {
	return ClassRef{Package: o.Key.Package, Class: o.Key.Class}
}

// Is reports whether the object belongs to the given class.
func (o *RawObject) Is(class ClassRef) bool { return class.Matches(o.Key) }

// IsComposite reports whether the object is keyed by more than one value.
func (o *RawObject) IsComposite() bool { return o.Composite || len(o.IDs) > 1 }

// finalizeKey derives Key from the decoded identity parts.
//
// A composite key is folded into one diagnostic string rather than dropped:
// leaving it empty would make every composite row of a class collide, which
// would turn a normal export into a duplicate-key error.
func (o *RawObject) finalizeKey() error {
	if len(o.IDs) == 0 {
		return fmt.Errorf("has no <id> or <%s> element", compositeIDName)
	}

	if !o.IsComposite() {
		o.Key.IDName = o.IDs[0].Name
		o.Key.ID = o.IDs[0].Value
		return nil
	}

	parts := make([]string, 0, len(o.IDs))
	for _, id := range o.IDs {
		parts = append(parts, id.Name+"="+id.Value)
	}
	o.Key.IDName = compositeIDName
	o.Key.ID = strings.Join(parts, compositeIDSeparator)
	return nil
}

// Scalar returns a scalar property and whether it was present at all.
func (o *RawObject) Scalar(name string) (string, bool) {
	scalar, ok := o.Scalars[name]
	if !ok {
		return "", false
	}
	return scalar.Value, scalar.Present
}

// ScalarValue returns a scalar property's value, or "" when it is absent. Use
// Scalar when absent and empty must be told apart.
func (o *RawObject) ScalarValue(name string) string {
	value, _ := o.Scalar(name)
	return value
}

// HasScalar reports whether a scalar property was present, whatever its value.
func (o *RawObject) HasScalar(name string) bool {
	_, present := o.Scalar(name)
	return present
}

// Reference returns a typed reference property.
func (o *RawObject) Reference(name string) (EntityKey, bool) {
	key, ok := o.References[name]
	return key, ok
}

// Collection returns a collection of references. The result is empty both when
// the collection is absent and when it is present and empty; no current rule
// distinguishes the two.
func (o *RawObject) Collection(name string) []EntityKey {
	return o.Collections[name]
}
