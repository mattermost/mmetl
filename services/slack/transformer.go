package slack

import (
	log "github.com/sirupsen/logrus"

	"github.com/mattermost/mmetl/services/intermediate"
)

type Transformer struct {
	intermediate.Exporter // provides TeamName, Intermediate, Logger, and all export methods
}

func NewTransformer(teamName string, logger log.FieldLogger) *Transformer {
	return &Transformer{
		Exporter: intermediate.Exporter{
			TeamName:     teamName,
			Intermediate: &Intermediate{},
			Logger:       logger,
		},
	}
}
