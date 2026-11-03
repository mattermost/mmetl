package commands

import (
	"os"

	"github.com/spf13/cobra"
)

var RootCmd = &cobra.Command{
	Use:   "mmetl",
	Short: "ETL tool to transform the export files from different providers to be compatible with Mattermost.",
}

func Execute() {
	silenceUsageDuringRun(RootCmd)
	if err := RootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func silenceUsageDuringRun(cmd *cobra.Command) {
	if run := cmd.RunE; run != nil {
		cmd.RunE = func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			return run(cmd, args)
		}
	} else if run := cmd.Run; run != nil {
		cmd.Run = func(cmd *cobra.Command, args []string) {
			cmd.SilenceUsage = true
			run(cmd, args)
		}
	}

	for _, child := range cmd.Commands() {
		silenceUsageDuringRun(child)
	}
}
