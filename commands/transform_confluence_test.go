// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package commands

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTransformConfluence_NoChannelFlag proves the Confluence command no longer
// exposes a --channel flag. The Space's backing channel is created and owned by
// the Docs importer; mmetl never names it.
func TestTransformConfluence_NoChannelFlag(t *testing.T) {
	flags := TransformConfluenceCmd.Flags()

	assert.Nil(t, flags.Lookup("channel"), "--channel must not be registered on transform confluence")
	assert.Nil(t, flags.ShorthandLookup("c"), "-c (channel shorthand) must not be registered on transform confluence")

	// --team remains, and its help makes the advisory semantics explicit.
	team := flags.Lookup("team")
	require.NotNil(t, team, "--team must remain registered")
	assert.Contains(t, team.Usage, "advisory", "--team help must describe it as advisory metadata")
}
