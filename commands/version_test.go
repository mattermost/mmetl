// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package commands

import (
	"runtime/debug"
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

	t.Run("returns module version from build info", func(t *testing.T) {
		origVersion := Version
		origReader := readBuildInfo
		t.Cleanup(func() {
			Version = origVersion
			readBuildInfo = origReader
		})

		Version = ""
		readBuildInfo = func() (*debug.BuildInfo, bool) {
			return &debug.BuildInfo{
				Main: debug.Module{Version: "v0.9.0"},
			}, true
		}
		assert.Equal(t, "v0.9.0", getVersion())
	})

	t.Run("returns dev when build info version is (devel)", func(t *testing.T) {
		origVersion := Version
		origReader := readBuildInfo
		t.Cleanup(func() {
			Version = origVersion
			readBuildInfo = origReader
		})

		Version = ""
		readBuildInfo = func() (*debug.BuildInfo, bool) {
			return &debug.BuildInfo{
				Main: debug.Module{Version: "(devel)"},
			}, true
		}
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

	t.Run("returns vcs.revision from build info", func(t *testing.T) {
		origHash := BuildHash
		origReader := readBuildInfo
		t.Cleanup(func() {
			BuildHash = origHash
			readBuildInfo = origReader
		})

		BuildHash = ""
		readBuildInfo = func() (*debug.BuildInfo, bool) {
			return &debug.BuildInfo{
				Main: debug.Module{Version: "v0.9.0"},
				Settings: []debug.BuildSetting{
					{Key: "vcs.revision", Value: "deadbeef1234"},
				},
			}, true
		}
		assert.Equal(t, "deadbeef1234", getBuildHash())
	})

	t.Run("returns dev mode when version is (devel)", func(t *testing.T) {
		origHash := BuildHash
		origReader := readBuildInfo
		t.Cleanup(func() {
			BuildHash = origHash
			readBuildInfo = origReader
		})

		BuildHash = ""
		readBuildInfo = func() (*debug.BuildInfo, bool) {
			return &debug.BuildInfo{
				Main: debug.Module{Version: "(devel)"},
				Settings: []debug.BuildSetting{
					{Key: "vcs.revision", Value: "deadbeef1234"},
				},
			}, true
		}
		assert.Equal(t, "dev mode", getBuildHash())
	})
}
