// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package commands

import (
	"fmt"

	"github.com/spf13/cobra"
)

var (
	BuildHash = "dev mode"
	Version   = "0.1.0"
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

func versionCmdF(cmd *cobra.Command, args []string) {
	fmt.Println("mmetl " + Version + " -- " + BuildHash)
}
