package slack

import (
	"fmt"

	log "github.com/sirupsen/logrus"

	"github.com/mattermost/mmetl/services/intermediate"
)

// Transformer drives the Slack → Mattermost transformation. It embeds
// intermediate.Exporter, which provides the TeamName, Intermediate, and Logger
// fields along with all the Export* methods.
type Transformer struct {
	intermediate.Exporter

	// skippedUserIDs records users dropped during TransformUsers (guests under
	// --guest-handling=skip) so that later stages can drop channel memberships
	// and posts referencing them, leaving no dangling references in the export.
	skippedUserIDs map[string]bool

	// skippedUserNames remembers the username for each skipped user ID, captured
	// at skip time (before the user is removed from Intermediate.UsersById, if
	// it ever was), so later log messages that only carry the raw Slack ID
	// (e.g. a post's User field) can still reference the user by name too. See
	// skippedUserRef.
	skippedUserNames map[string]string

	// droppedPostRefs / droppedReactionRefs / droppedMembershipRefs count
	// references removed because they pointed at a skipped user, for the
	// end-of-transform summary log. Reactions are tracked separately from
	// posts so the summary doesn't overstate the number of dropped posts.
	droppedPostRefs       int
	droppedReactionRefs   int
	droppedMembershipRefs int

	// warnedDroppedThreads records channel+thread keys already warned about when
	// a thread was dropped because its root was never imported (e.g. a skipped
	// guest started it), so a thread with many replies emits one WARN, not one
	// per reply. droppedPostRefs still counts every dropped reply.
	warnedDroppedThreads map[string]bool
}

// Guest handling modes for the --guest-handling flag.
const (
	// GuestHandlingGuest migrates Slack guests as Mattermost guest accounts.
	GuestHandlingGuest = "guest"
	// GuestHandlingUser migrates Slack guests as regular Mattermost users.
	GuestHandlingUser = "user"
	// GuestHandlingSkip drops Slack guests entirely.
	GuestHandlingSkip = "skip"
)

// ValidateGuestHandling returns an error if the given guest-handling mode is
// not one of the supported values.
func ValidateGuestHandling(mode string) error {
	switch mode {
	case GuestHandlingGuest, GuestHandlingUser, GuestHandlingSkip:
		return nil
	default:
		return fmt.Errorf("invalid --guest-handling value %q: must be one of %q, %q, or %q",
			mode, GuestHandlingGuest, GuestHandlingUser, GuestHandlingSkip)
	}
}

func NewTransformer(teamName string, logger log.FieldLogger) *Transformer {
	return &Transformer{
		Exporter: intermediate.Exporter{
			TeamName:     teamName,
			Intermediate: &intermediate.Intermediate{},
			Logger:       logger,
		},
		skippedUserIDs:   make(map[string]bool),
		skippedUserNames: make(map[string]string),
	}
}

// isSkippedUser reports whether the given Slack user ID was dropped in
// TransformUsers.
func (t *Transformer) isSkippedUser(id string) bool {
	return id != "" && t.skippedUserIDs[id]
}

// markUserSkipped records a user ID (and its username, if known at the call
// site) as skipped so downstream stages can drop memberships and posts that
// reference it.
func (t *Transformer) markUserSkipped(id, username string) {
	if id == "" {
		return
	}
	t.skippedUserIDs[id] = true
	if username != "" {
		t.skippedUserNames[id] = username
	}
}

// skippedUserRef formats a skipped user's ID for a log message, including
// their username when it was captured at skip time — see skippedUserNames.
func (t *Transformer) skippedUserRef(id string) string {
	return intermediate.FormatEntityRef(t.skippedUserNames[id], id)
}
