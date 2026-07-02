package slack

import (
	log "github.com/sirupsen/logrus"

	"github.com/mattermost/mmetl/services/intermediate"
)

// Transformer drives the Slack → Mattermost transformation. It embeds
// intermediate.Exporter, which provides the TeamName, Intermediate, and Logger
// fields along with all the Export* methods.
type Transformer struct {
	intermediate.Exporter
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
