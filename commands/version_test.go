// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package commands

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGetVersion(t *testing.T) {
	t.Run("returns ldflags value when set", func(t *testing.T) {
		original := Version
		t.Cleanup(func() { Version = original })

		Version = "v1.2.3"
		assert.Equal(t, "v1.2.3", getVersion())
	})

	t.Run("returns dev when ldflags not set", func(t *testing.T) {
		original := Version
		t.Cleanup(func() { Version = original })

		// In test binaries, info.Main.Version is "(devel)",
		// so getVersion should fall through to "dev".
		Version = ""
		assert.Equal(t, "dev", getVersion())
	})
}

func TestGetBuildHash(t *testing.T) {
	t.Run("returns ldflags value when set", func(t *testing.T) {
		original := BuildHash
		t.Cleanup(func() { BuildHash = original })

		BuildHash = "abc123"
		assert.Equal(t, "abc123", getBuildHash())
	})

	t.Run("returns dev mode when ldflags not set", func(t *testing.T) {
		original := BuildHash
		t.Cleanup(func() { BuildHash = original })

		// In test binaries, info.Main.Version is "(devel)",
		// so getBuildHash should skip vcs.revision and return "dev mode".
		BuildHash = ""
		assert.Equal(t, "dev mode", getBuildHash())
	})
}
