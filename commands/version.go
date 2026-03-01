// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package commands

import (
	"fmt"
	"runtime/debug"

	"github.com/spf13/cobra"
)

var (
	BuildHash = ""
	Version   = ""
)

var VersionCmd = &cobra.Command{
	Use:   "version",
	Short: "Prints the version of mmetl.",
	Args:  cobra.NoArgs,
	Run:   versionCmdF,
}

func init() {
	RootCmd.AddCommand(VersionCmd)
}

func getVersion() string {
	if Version != "" {
		return Version
	}

	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}

	return "dev"
}

func getBuildHash() string {
	if BuildHash != "" {
		return BuildHash
	}

	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" {
				return setting.Value
			}
		}
	}

	return "dev mode"
}

func versionCmdF(cmd *cobra.Command, args []string) {
	fmt.Println("mmetl " + getVersion() + " -- " + getBuildHash())
}
