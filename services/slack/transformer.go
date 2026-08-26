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

	// skippedUsernames maps a skipped user ID to the username it had, so the
	// report can name the user behind a dropped membership, post or reaction
	// after the IntermediateUser itself is gone.
	skippedUsernames map[string]string

	// reportedThreads records the channel+thread keys already accounted for in
	// the report, so a thread with many replies is counted (and, when its root
	// was never imported, skipped) exactly once instead of once per reply.
	// Every dropped reply is still named individually under posts.
	reportedThreads map[string]bool

	// droppedRootAuthors maps a channel+thread key to the username of the post
	// that would have been its root, for roots that were dropped. Posts are
	// processed in timestamp order, so a root is always recorded before the
	// replies that go looking for it — which is what lets a dropped thread name
	// the author responsible for the loss rather than one of its replies.
	droppedRootAuthors map[string]string
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
			Report:       intermediate.NewReport(logger),
		},
		skippedUserIDs:     make(map[string]bool),
		skippedUsernames:   make(map[string]string),
		reportedThreads:    make(map[string]bool),
		droppedRootAuthors: make(map[string]string),
	}
}

// isSkippedUser reports whether the given Slack user ID was dropped in
// TransformUsers.
func (t *Transformer) isSkippedUser(id string) bool {
	return id != "" && t.skippedUserIDs[id]
}

// markUserSkipped records a user ID as skipped so downstream stages can drop
// memberships and posts that reference it, remembering the username so the
// report can still name the user once the IntermediateUser is gone.
func (t *Transformer) markUserSkipped(id, username string) {
	if id == "" {
		return
	}
	t.skippedUserIDs[id] = true
	if username != "" {
		if t.skippedUsernames == nil {
			t.skippedUsernames = map[string]string{}
		}
		t.skippedUsernames[id] = username
	}
}

// usernameFor resolves a Slack user ID to the username the report should name
// them by: the one remembered when they were skipped, else the one on the
// transformed user, else "" — in which case the report names them by ID alone.
func (t *Transformer) usernameFor(id string) string {
	if username, ok := t.skippedUsernames[id]; ok {
		return username
	}
	if user, ok := t.Intermediate.UsersById[id]; ok {
		return user.Username
	}
	return ""
}
