// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package confluence

import log "github.com/sirupsen/logrus"

// Transformer handles the transformation of Confluence exports to Mattermost import format.
type Transformer struct {
	TeamName     string
	ChannelName  string
	Intermediate *Intermediate
	UserMapper   *UserMapper
	Logger       log.FieldLogger
	Config       *TransformConfig
	Stats        *MigrationStats
	ExportFile   string // Original export file path for manifest
}

// TransformConfig holds configuration options for the transformation.
type TransformConfig struct {
	// SkipAttachments skips copying attachments from the export
	SkipAttachments bool
	// AttachmentsDir is the directory to store attachments
	AttachmentsDir string
	// MaxDepth is the maximum hierarchy depth (default 10)
	MaxDepth int
	// DryRun validates without writing output
	DryRun bool
}

// NewTransformer creates a new Confluence transformer.
func NewTransformer(teamName, channelName string, logger log.FieldLogger, config *TransformConfig) *Transformer {
	if config == nil {
		config = &TransformConfig{
			MaxDepth: 10,
		}
	}
	if config.MaxDepth == 0 {
		config.MaxDepth = 10
	}

	return &Transformer{
		TeamName:     teamName,
		ChannelName:  channelName,
		Intermediate: &Intermediate{},
		Logger:       logger,
		Config:       config,
		Stats:        &MigrationStats{},
	}
}

// Transform performs the full transformation from Confluence export to intermediate format.
func (t *Transformer) Transform(export *ConfluenceExport) error {
	t.Logger.Info("Starting Confluence transformation")

	// Transform pages (in topological order for hierarchy)
	if err := t.TransformPages(export); err != nil {
		return err
	}

	// Transform comments
	if err := t.TransformComments(export); err != nil {
		return err
	}

	t.Logger.Infof("Transformation complete: %d pages, %d comments",
		len(t.Intermediate.Pages), len(t.Intermediate.Comments))

	return nil
}
