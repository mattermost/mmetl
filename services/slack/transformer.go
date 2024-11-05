package slack

import log "github.com/sirupsen/logrus"

type Transformer struct {
	Logger           log.FieldLogger
	TeamName         string
	Intermediate     *Intermediate
	MaxMessageLength int
	ChannelOnly      string
}

func NewTransformer(teamName string, logger log.FieldLogger) *Transformer {
	return &Transformer{
		TeamName:         teamName,
		Intermediate:     &Intermediate{},
		Logger:           logger,
		MaxMessageLength: 16383,
	}
}
