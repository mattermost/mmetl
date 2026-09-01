package confluence

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
)

// XML element and attribute names in a Hibernate generic export.
const (
	xmlRootElement        = "hibernate-generic"
	xmlObjectElement      = "object"
	xmlIDElement          = "id"
	xmlCompositeIDElement = "composite-id"
	xmlPropertyElement    = "property"
	xmlCollectionElement  = "collection"
	xmlElementElement     = "element"

	xmlNameAttr      = "name"
	xmlClassAttr     = "class"
	xmlPackageAttr   = "package"
	xmlEnumClassAttr = "enum-class"
	xmlDatetimeAttr  = "datetime"
)

// ObjectDecoder streams top-level <object> elements out of a Confluence
// entities.xml. It returns one completed object at a time and keeps no
// reference to the previous one, so peak memory is the size of the largest
// single object rather than the size of the file.
//
// The decoder is deliberately untyped. entities.xml carries dozens of classes
// this exporter never reads, and new Confluence versions add more; a decoder
// that knew the schema would need changing every time one appeared.
type ObjectDecoder struct {
	dec *xml.Decoder

	accept  func(class ClassRef) bool
	entered bool
	done    bool

	datetime string
	decoded  int64
	skipped  int64
}

// NewObjectDecoder returns a decoder reading entities.xml from r.
//
// DTDs and entity declarations are rejected outright, and the underlying
// decoder resolves no entity beyond the five XML predefines, so a backup cannot
// make the exporter read a local file or expand into a decompression bomb.
func NewObjectDecoder(r io.Reader) *ObjectDecoder {
	dec := xml.NewDecoder(r)
	// Strict rejects undefined entity references. Leaving Entity nil means only
	// the five predefined XML entities resolve. Leaving CharsetReader nil means
	// a declared non-UTF-8 encoding is an error rather than a silent misread.
	dec.Strict = true

	return &ObjectDecoder{dec: dec}
}

// SetAccept installs a class filter. Objects it rejects are skipped without
// materializing their scalars, which is what lets the metadata passes walk a
// multi-gigabyte entities.xml without ever holding a page body.
//
// A nil filter accepts everything.
func (d *ObjectDecoder) SetAccept(accept func(class ClassRef) bool) {
	d.accept = accept
}

// Datetime is the export timestamp from the root element, available after the
// first Next call.
func (d *ObjectDecoder) Datetime() string { return d.datetime }

// Decoded is the number of objects returned so far.
func (d *ObjectDecoder) Decoded() int64 { return d.decoded }

// Skipped is the number of objects the accept filter rejected so far.
func (d *ObjectDecoder) Skipped() int64 { return d.skipped }

// Offset is the current byte offset into entities.xml, for diagnostics.
func (d *ObjectDecoder) Offset() int64 { return d.dec.InputOffset() }

// Next returns the next top-level object, or io.EOF when the document ends.
//
// The returned object is fully materialized and owned by the caller; the
// decoder retains nothing from it.
func (d *ObjectDecoder) Next() (*RawObject, error) {
	if d.done {
		return nil, io.EOF
	}

	for {
		offset := d.dec.InputOffset()

		token, err := d.dec.Token()
		if errors.Is(err, io.EOF) {
			d.done = true
			if !d.entered {
				return nil, fmt.Errorf("entities.xml has no <%s> root element", xmlRootElement)
			}
			return nil, io.EOF
		}
		if err != nil {
			return nil, fmt.Errorf("reading entities.xml at byte %d: %w", offset, err)
		}

		switch t := token.(type) {
		case xml.Directive:
			return nil, fmt.Errorf("entities.xml at byte %d: XML directives are rejected, including DTDs and entity declarations: <!%.40s>",
				offset, string(t))

		case xml.StartElement:
			switch {
			case t.Name.Local == xmlRootElement && !d.entered:
				d.entered = true
				d.datetime = attrValue(t, xmlDatetimeAttr)

			case t.Name.Local == xmlObjectElement && d.entered:
				object, err := d.readObject(t, offset)
				if err != nil {
					return nil, err
				}
				if object == nil {
					continue // filtered out
				}
				d.decoded++
				return object, nil

			default:
				if err := d.dec.Skip(); err != nil {
					return nil, fmt.Errorf("skipping <%s> at byte %d: %w", t.Name.Local, offset, err)
				}
			}
		}
	}
}

// readObject materializes one <object>, or skips it and returns nil when the
// accept filter rejects its class.
func (d *ObjectDecoder) readObject(start xml.StartElement, offset int64) (*RawObject, error) {
	class := ClassRef{
		Package: attrValue(start, xmlPackageAttr),
		Class:   attrValue(start, xmlClassAttr),
	}
	if class.Class == "" {
		return nil, fmt.Errorf("entities.xml at byte %d: <%s> has no %s attribute", offset, xmlObjectElement, xmlClassAttr)
	}
	if class.Package == "" {
		return nil, fmt.Errorf("entities.xml at byte %d: <%s class=%q> has no %s attribute",
			offset, xmlObjectElement, class.Class, xmlPackageAttr)
	}

	if d.accept != nil && !d.accept(class) {
		if err := d.dec.Skip(); err != nil {
			return nil, fmt.Errorf("skipping %s at byte %d: %w", class, offset, err)
		}
		d.skipped++
		return nil, nil
	}

	object := &RawObject{
		Key:         EntityKey{Package: class.Package, Class: class.Class},
		Scalars:     map[string]RawScalar{},
		References:  map[string]EntityKey{},
		Collections: map[string][]EntityKey{},
		Offset:      offset,
	}

	for {
		token, err := d.dec.Token()
		if err != nil {
			return nil, fmt.Errorf("reading %s at byte %d: %w", class, offset, err)
		}

		switch t := token.(type) {
		case xml.EndElement:
			if t.Name.Local == xmlObjectElement {
				if err := object.finalizeKey(); err != nil {
					return nil, fmt.Errorf("entities.xml at byte %d: %s: %w", offset, class, err)
				}
				return object, nil
			}

		case xml.Directive:
			return nil, fmt.Errorf("entities.xml at byte %d: XML directives are rejected", d.dec.InputOffset())

		case xml.StartElement:
			if err := d.readObjectChild(object, t); err != nil {
				return nil, err
			}
		}
	}
}

func (d *ObjectDecoder) readObjectChild(object *RawObject, start xml.StartElement) error {
	switch start.Name.Local {
	case xmlIDElement:
		value, err := d.readText(start)
		if err != nil {
			return err
		}
		object.IDs = append(object.IDs, RawID{Name: attrValue(start, xmlNameAttr), Value: value})
		return nil

	case xmlCompositeIDElement:
		return d.readCompositeID(object, start)

	case xmlPropertyElement:
		return d.readProperty(object, start)

	case xmlCollectionElement:
		return d.readCollection(object, start)

	default:
		return d.dec.Skip()
	}
}

// readCompositeID reads a <composite-id>, whose parts are <property> elements
// rather than <id> elements. Confluence uses it for join-table rows such as
// BucketPropertySetItem and SpacePermission. Those are never imported, but they
// must decode cleanly: a real export is full of them, and failing on one would
// abort the whole stream.
func (d *ObjectDecoder) readCompositeID(object *RawObject, start xml.StartElement) error {
	object.Composite = true

	for {
		token, err := d.dec.Token()
		if err != nil {
			return fmt.Errorf("reading <%s>: %w", xmlCompositeIDElement, err)
		}

		switch t := token.(type) {
		case xml.EndElement:
			if t.Name.Local == start.Name.Local {
				return nil
			}

		case xml.StartElement:
			if t.Name.Local != xmlPropertyElement {
				if err := d.dec.Skip(); err != nil {
					return err
				}
				continue
			}
			value, err := d.readText(t)
			if err != nil {
				return err
			}
			object.IDs = append(object.IDs, RawID{Name: attrValue(t, xmlNameAttr), Value: value})
		}
	}
}

// readProperty reads either a scalar or a typed reference.
//
// The discriminator is the class attribute: a reference carries one and holds a
// nested <id>, while an enum scalar carries enum-class and package but no
// class, and would be misread as a reference by a package-based test.
func (d *ObjectDecoder) readProperty(object *RawObject, start xml.StartElement) error {
	name := attrValue(start, xmlNameAttr)
	if name == "" {
		return d.dec.Skip()
	}

	class := attrValue(start, xmlClassAttr)
	if class == "" || attrValue(start, xmlEnumClassAttr) != "" {
		value, err := d.readText(start)
		if err != nil {
			return err
		}
		object.Scalars[name] = RawScalar{Present: true, Value: value}
		return nil
	}

	key, found, err := d.readNestedID(start, class, attrValue(start, xmlPackageAttr))
	if err != nil {
		return err
	}
	if found {
		object.References[name] = key
	}
	return nil
}

func (d *ObjectDecoder) readCollection(object *RawObject, start xml.StartElement) error {
	name := attrValue(start, xmlNameAttr)
	if name == "" {
		return d.dec.Skip()
	}

	// Non-nil so a present-but-empty collection is still recorded.
	elements := []EntityKey{}
	for {
		token, err := d.dec.Token()
		if err != nil {
			return fmt.Errorf("reading collection %q: %w", name, err)
		}

		switch t := token.(type) {
		case xml.EndElement:
			if t.Name.Local == xmlCollectionElement {
				object.Collections[name] = elements
				return nil
			}

		case xml.StartElement:
			if t.Name.Local != xmlElementElement {
				if err := d.dec.Skip(); err != nil {
					return err
				}
				continue
			}
			key, found, err := d.readNestedID(t, attrValue(t, xmlClassAttr), attrValue(t, xmlPackageAttr))
			if err != nil {
				return err
			}
			if found {
				elements = append(elements, key)
			}
		}
	}
}

// readNestedID consumes an element that wraps a single <id>, returning the key
// it points at. A wrapper with no <id> is a reference to nothing and is
// reported as not found rather than as an empty key.
func (d *ObjectDecoder) readNestedID(start xml.StartElement, class, pkg string) (EntityKey, bool, error) {
	var key EntityKey
	var found bool

	for {
		token, err := d.dec.Token()
		if err != nil {
			return EntityKey{}, false, fmt.Errorf("reading <%s>: %w", start.Name.Local, err)
		}

		switch t := token.(type) {
		case xml.EndElement:
			if t.Name.Local == start.Name.Local {
				return key, found, nil
			}

		case xml.StartElement:
			if t.Name.Local != xmlIDElement {
				if err := d.dec.Skip(); err != nil {
					return EntityKey{}, false, err
				}
				continue
			}
			value, err := d.readText(t)
			if err != nil {
				return EntityKey{}, false, err
			}
			if !found {
				key = EntityKey{Package: pkg, Class: class, IDName: attrValue(t, xmlNameAttr), ID: value}
				found = true
			}
		}
	}
}

// readText consumes an element and returns its concatenated character data.
// Nested elements are skipped: a scalar that contains markup is malformed for
// this format, and dropping the markup is better than aborting the export.
func (d *ObjectDecoder) readText(start xml.StartElement) (string, error) {
	var text strings.Builder

	for {
		token, err := d.dec.Token()
		if err != nil {
			return "", fmt.Errorf("reading <%s>: %w", start.Name.Local, err)
		}

		switch t := token.(type) {
		case xml.EndElement:
			if t.Name.Local == start.Name.Local {
				return text.String(), nil
			}

		case xml.CharData:
			text.Write(t)

		case xml.StartElement:
			if err := d.dec.Skip(); err != nil {
				return "", err
			}
		}
	}
}

func attrValue(start xml.StartElement, name string) string {
	for _, attr := range start.Attr {
		if attr.Name.Local == name {
			return attr.Value
		}
	}
	return ""
}
