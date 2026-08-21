package intermediate

import "fmt"

// FormatEntityRef formats a name+ID pair for a log message, so multiple log
// lines about the same entity are easy to correlate by eye even when only one
// of the two is meaningful on its own — e.g. a post only carries a raw source
// ID with no accompanying display name, while a different log line about the
// same user (from a different call site, or after the user has already been
// removed from Intermediate) only has the display name at hand. Returns
// whichever of name/id is non-empty when the other is unknown, and just name
// when the two happen to be identical (some sources use the same value for
// both).
func FormatEntityRef(name, id string) string {
	switch {
	case name != "" && id != "" && name != id:
		return fmt.Sprintf("%s (%s)", name, id)
	case name != "":
		return name
	default:
		return id
	}
}
