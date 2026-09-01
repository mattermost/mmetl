package confluence

import "fmt"

// keyIndex detects duplicate entity keys while streaming.
//
// Two objects sharing a key would make every reference to that key ambiguous,
// and the export would silently pick whichever happened to be decoded last. It
// is a hard error rather than a warning for that reason.
//
// Composite-keyed objects are exempt: they are join-table rows this iteration
// never imports, and their folded diagnostic key is not an identity the rest of
// the exporter resolves against.
type keyIndex struct {
	offsets map[EntityKey]int64
}

func newKeyIndex() *keyIndex {
	return &keyIndex{offsets: map[EntityKey]int64{}}
}

// add records an object's key, or reports the duplicate.
func (i *keyIndex) add(object *RawObject) error {
	if object.IsComposite() {
		return nil
	}
	if previous, exists := i.offsets[object.Key]; exists {
		return fmt.Errorf("entities.xml declares %s twice, at byte %d and byte %d",
			object.Key, previous, object.Offset)
	}
	i.offsets[object.Key] = object.Offset
	return nil
}
