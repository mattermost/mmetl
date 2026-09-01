package confluence

import (
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
)

// CatalogSpaces performs pass 1: it reads the descriptor and every Space and
// SpaceDescription, and nothing else.
//
// This is the only pass that runs before --space is resolved, so it must stay
// cheap on a multi-gigabyte export. It decodes two classes out of the thirty-odd
// in a real backup and retains one small struct per space.
func CatalogSpaces(archive *SourceArchive) ([]Space, Descriptor, error) {
	descriptor := archive.Descriptor()

	entities, err := archive.OpenEntities()
	if err != nil {
		return nil, descriptor, err
	}
	defer func() { _ = entities.Close() }()

	decoder := NewObjectDecoder(entities)
	decoder.SetAccept(func(class ClassRef) bool {
		return class == ClassSpace || class == ClassSpaceDescription
	})

	index := newKeyIndex()
	spacesByKey := map[EntityKey]*Space{}
	var order []EntityKey

	// descriptionBySpace lets a space recover its description from the
	// description's back-reference when the forward reference is absent.
	descriptionBySpace := map[EntityKey]EntityKey{}

	for {
		object, err := decoder.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, descriptor, err
		}
		if err := index.add(object); err != nil {
			return nil, descriptor, err
		}

		switch {
		case object.Is(ClassSpace):
			space := spaceFromObject(object)
			spacesByKey[object.Key] = &space
			order = append(order, object.Key)

		case object.Is(ClassSpaceDescription):
			if spaceKey, ok := object.Reference(contentPropSpace); ok {
				descriptionBySpace[spaceKey] = object.Key
			}
		}
	}

	spaces := make([]Space, 0, len(order))
	for _, key := range order {
		space := spacesByKey[key]
		if !space.HasDescription() {
			if descriptionKey, ok := descriptionBySpace[key]; ok {
				space.DescriptionKey = descriptionKey
			}
		}
		spaces = append(spaces, *space)
	}

	SortSpaces(spaces)
	return spaces, descriptor, nil
}

func spaceFromObject(object *RawObject) Space {
	space := Space{
		Key:      object.Key,
		SourceID: object.Key.ID,
		SpaceKey: object.ScalarValue(spacePropKey),
		LowerKey: object.ScalarValue(spacePropLowerKey),
		Name:     object.ScalarValue(spacePropName),
		Type:     object.ScalarValue(spacePropType),
		Status:   object.ScalarValue(spacePropStatus),
	}
	space.HomePageKey, _ = object.Reference(spacePropHomePage)
	space.DescriptionKey, _ = object.Reference(spacePropDescription)
	return space
}

// SortSpaces orders spaces by key, then by source ID, as the list output
// requires.
func SortSpaces(spaces []Space) {
	sort.SliceStable(spaces, func(i, j int) bool {
		if spaces[i].SpaceKey != spaces[j].SpaceKey {
			return spaces[i].SpaceKey < spaces[j].SpaceKey
		}
		return lessNumericThenLexical(spaces[i].SourceID, spaces[j].SourceID)
	})
}

// SpaceSelectionError reports that --space named zero or several spaces. It
// carries the full catalog so the command can print the valid-space table
// without recataloging.
type SpaceSelectionError struct {
	Selector string
	Matches  []Space
	Spaces   []Space
}

func (e *SpaceSelectionError) Error() string {
	if len(e.Matches) == 0 {
		if e.Selector == "" {
			return "no space selected: --space is required and must name exactly one space"
		}
		return fmt.Sprintf("no space matches %q", e.Selector)
	}

	keys := make([]string, 0, len(e.Matches))
	for _, space := range e.Matches {
		keys = append(keys, fmt.Sprintf("%s (%s)", space.SpaceKey, space.SourceID))
	}
	return fmt.Sprintf("%q matches %d spaces: %s", e.Selector, len(e.Matches), strings.Join(keys, ", "))
}

// ResolveSpace maps a --space selector onto exactly one space.
//
// Steps are tried in order and the first step that matches anything decides, so
// an exact match always beats a case-insensitive one. A step that matches
// several spaces is ambiguous and fails there rather than falling through,
// since a later, looser step could only match at least as many.
func ResolveSpace(spaces []Space, selector string) (Space, error) {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return Space{}, &SpaceSelectionError{Spaces: spaces}
	}

	folded := strings.ToLower(selector)
	steps := []func(Space) bool{
		func(s Space) bool { return s.SourceID == selector },
		func(s Space) bool { return s.SpaceKey == selector },
		func(s Space) bool { return strings.ToLower(s.SpaceKey) == folded },
		func(s Space) bool { return s.Name == selector },
		func(s Space) bool { return strings.ToLower(s.Name) == folded },
	}

	for _, matches := range steps {
		var found []Space
		for _, space := range spaces {
			if matches(space) {
				found = append(found, space)
			}
		}
		switch len(found) {
		case 0:
			continue
		case 1:
			return found[0], nil
		default:
			return Space{}, &SpaceSelectionError{Selector: selector, Matches: found, Spaces: spaces}
		}
	}

	return Space{}, &SpaceSelectionError{Selector: selector, Spaces: spaces}
}

// WriteSpaceTable renders the catalog as the KEY/ID/TYPE/STATUS/NAME table that
// --list-spaces prints and that a failed selection prints alongside its error.
//
// It deliberately carries no page titles, emails, usernames, or body content.
func WriteSpaceTable(w io.Writer, spaces []Space) error {
	ordered := make([]Space, len(spaces))
	copy(ordered, spaces)
	SortSpaces(ordered)

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "KEY\tID\tTYPE\tSTATUS\tNAME"); err != nil {
		return err
	}
	for _, space := range ordered {
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			space.SpaceKey, space.SourceID, space.Type, space.Status, space.Name); err != nil {
			return err
		}
	}
	return tw.Flush()
}
