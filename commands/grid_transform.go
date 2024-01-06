package commands

import (
	"archive/zip"
	"os"

	slack_bulk "github.com/mattermost/mmetl/services/slack/bulk"

	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

var GridTransformCmd = &cobra.Command{
	Use:   "grid-transform",
	Short: "Transforms a slack enterprise grid into multiple workspace export files.",
	Long:  "Accepts a Slack Enterprise Grid export file and transforms it into multiple workspace export files to be imported seperatly into Mattermost.",
	Args:  cobra.NoArgs,
	RunE:  gridTransformCmdF,
}

func init() {
	GridTransformCmd.Flags().StringP("file", "f", "", "the Slack export file to transform")
	GridTransformCmd.Flags().Bool("debug", true, "Whether to show debug logs or not")

	if err := GridTransformCmd.MarkFlagRequired("file"); err != nil {
		panic(err)
	}

	// CheckCmd.AddCommand(
	// 	CheckSlackCmd,
	// )

	GridTransformCmd.AddCommand(
		GridTransformCmd,
	)
}

func gridTransformCmdF(cmd *cobra.Command, args []string) error {
	inputFilePath, _ := cmd.Flags().GetString("file")
	debug, _ := cmd.Flags().GetBool("debug")

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

	logger := log.New()
	logFile, err := os.OpenFile("grid-transform-slack.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		return err
	}
	defer logFile.Close()
	logger.SetOutput(logFile)
	logger.SetFormatter(customLogFormatter)
	logger.SetReportCaller(true)

	if debug {
		logger.Level = log.DebugLevel
		logger.Info("Debug mode enabled")
	}

	// we do not need a team name here.
	slackTransformer := slack_bulk.NewBulkTransformer(logger)

	valid := slackTransformer.GridPreCheck(zipReader)
	if !valid {
		return nil
	}

	slackExport, err := slackTransformer.ParseBulkSlackExportFile(zipReader)
	if err != nil {
		return err
	}

	return nil
}
