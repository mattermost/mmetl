package slack

import log "github.com/sirupsen/logrus"

type Transformer struct {
	TeamName     string
	CreateTeam   bool
	Intermediate *Intermediate
	Logger       log.FieldLogger
}

func NewTransformer(teamName string, logger log.FieldLogger) *Transformer {
	return &Transformer{
		TeamName:     teamName,
		Intermediate: &Intermediate{},
		Logger:       logger,
	}
}
