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

	// Skipped-user bookkeeping (MarkUserSkipped/IsSkippedUser/SkippedUserRef)
	// is on the embedded Exporter, shared with every other source Transformer.

	// droppedPostRefs / droppedReactionRefs / droppedMembershipRefs count
	// references removed because they pointed at a skipped user, for the
	// end-of-transform summary log. Reactions are tracked separately from
	// posts so the summary doesn't overstate the number of dropped posts.
	droppedPostRefs       int
	droppedReactionRefs   int
	droppedMembershipRefs int
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
	}
}
