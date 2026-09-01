package confluence

import (
	"strconv"
	"strings"
	"time"
)

// Confluence writes naive local timestamps such as "2025-11-14 16:53:35.571".
// The fractional part is optional in this layout, so one pattern covers both
// forms Confluence emits.
const confluenceTimeLayout = "2006-01-02 15:04:05.999999999"

// rfc3339Layouts are tried first, so a timestamp that already carries an offset
// is never reinterpreted in the export's timezone.
var rfc3339Layouts = []string{time.RFC3339Nano, time.RFC3339}

// ParseSourceTime parses a Confluence timestamp.
//
// A timestamp with an explicit offset is authoritative. Otherwise the value is
// naive local time in the zone named by exportDescriptor.properties, and
// loc supplies that zone.
//
// An unparseable or empty value returns ok=false rather than a zero time. A
// zero time.Time is year 1, which converts to a large negative Unix
// millisecond value; writing that into a bundle would produce pages dated
// thousands of years in the past instead of pages with no date.
func ParseSourceTime(raw string, loc *time.Location) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}

	for _, layout := range rfc3339Layouts {
		if parsed, err := time.Parse(layout, raw); err == nil {
			return parsed, true
		}
	}

	if loc == nil {
		loc = time.UTC
	}
	if parsed, err := time.ParseInLocation(confluenceTimeLayout, raw, loc); err == nil {
		return parsed, true
	}
	return time.Time{}, false
}

// SourceTimeMillis parses a Confluence timestamp into Unix milliseconds, which
// is what the bundle's create_at and update_at carry. It returns ok=false when
// the value is absent or unparseable, and the caller then omits the field.
func SourceTimeMillis(raw string, loc *time.Location) (int64, bool) {
	parsed, ok := ParseSourceTime(raw, loc)
	if !ok {
		return 0, false
	}
	return parsed.UnixMilli(), true
}

// parseSourcePosition reads a Confluence position, which orders siblings.
// Absent and unparseable are the same thing to the caller: no position.
func parseSourcePosition(raw string) (int64, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, false
	}
	return value, true
}

// isAbsentOrZeroID reports whether a Confluence id-valued scalar means "none".
//
// Confluence writes an absent original version three different ways depending
// on the object and the export version: the property missing entirely, present
// and empty, or present and "0". Section 6.1 treats all three alike, and
// reading "0" as a real id would exclude every current page in such an export.
func isAbsentOrZeroID(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return true
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	return err == nil && value == 0
}
