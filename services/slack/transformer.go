package slack

import log "github.com/sirupsen/logrus"

type Transformer struct {
	TeamName      string
	WorkspaceName string
	Intermediate  *Intermediate
	Logger        log.FieldLogger
}

func NewTransformer(teamName string, workspaceName string, logger log.FieldLogger) *Transformer {
	return &Transformer{
		TeamName:      teamName,
		WorkspaceName: workspaceName,
		Intermediate:  &Intermediate{},
		Logger:        logger,
	}
}
