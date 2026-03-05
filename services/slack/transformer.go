package slack

import log "github.com/sirupsen/logrus"

type TransformOptions struct {
	CreateTeam bool
}

type Transformer struct {
	TeamName     string
	Options      TransformOptions
	Intermediate *Intermediate
	Logger       log.FieldLogger
}

func NewTransformer(teamName string, options TransformOptions, logger log.FieldLogger) *Transformer {
	return &Transformer{
		TeamName:     teamName,
		Options:      options,
		Intermediate: &Intermediate{},
		Logger:       logger,
	}
}
