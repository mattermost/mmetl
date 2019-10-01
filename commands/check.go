package commands

import (
	"archive/zip"
	"os"

	"github.com/mattermost/mmetl/services/slack"
	"github.com/spf13/cobra"
)

var CheckCmd = &cobra.Command{
	Use:   "check",
	Short: "Checks the integrity of export files.",
	Long:  "Checks the integrity and entities of export files from different providers.",
}

var CheckSlackCmd = &cobra.Command{
	Use:   "slack",
	Short: "Checks the integrity of a Slack export.",
	Args:  cobra.NoArgs,
	RunE:  checkSlackCmdF,
}

func init() {
	CheckSlackCmd.Flags().StringP("file", "f", "", "the Slack export file to transform")
	CheckSlackCmd.MarkFlagRequired("file")

	CheckCmd.AddCommand(
		CheckSlackCmd,
	)

	RootCmd.AddCommand(
		CheckCmd,
	)
}

func checkSlackCmdF(cmd *cobra.Command, args []string) error {
	inputFilePath, _ := cmd.Flags().GetString("file")

	// input file
	fileReader, err := os.Open(inputFilePath)
	if err != nil {
		return err
	}
	defer fileReader.Close()

	zipFileInfo, err := fileReader.Stat()
	if err != nil {
		return err
	}

	zipReader, err := zip.NewReader(fileReader, zipFileInfo.Size())
	if err != nil || zipReader.File == nil {
		return err
	}

	slackExport, err := slack.ParseSlackExportFile("test", zipReader, true)
	if err != nil {
		return err
	}

	intermediate, err := slack.Transform(slackExport, "", true)
	if err != nil {
		return err
	}

	slack.Check(intermediate)

	return nil
}
