package commands

import (
	"archive/zip"
	"encoding/json"
	"os"

	"github.com/mattermost/mmetl/services/slack"
	"github.com/mattermost/mmetl/services/slack_grid"
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
	GridTransformCmd.Flags().StringP("file", "f", "", "the Slack export file to clean")
	GridTransformCmd.Flags().StringP("teamMap", "t", "", "The team mapping file to use")

	GridTransformCmd.Flags().Bool("debug", true, "Whether to show debug logs or not")

	if err := GridTransformCmd.MarkFlagRequired("file"); err != nil {
		panic(err)
	}

	if err := GridTransformCmd.MarkFlagRequired("teamMap"); err != nil {
		panic(err)
	}

	RootCmd.AddCommand(
		GridTransformCmd,
	)
}

func gridTransformCmdF(cmd *cobra.Command, args []string) error {
	inputFilePath, _ := cmd.Flags().GetString("file")
	teamMap, _ := cmd.Flags().GetString("teamMap")

	debug, _ := cmd.Flags().GetBool("debug")

	logger := log.New()
	logFile, err := os.OpenFile("grid-transform-slack.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		logger.Error("error creating zip reader: %w", err)
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

	// input file
	fileReader, err := os.Open(inputFilePath)
	if err != nil {
		logger.Error("error opening input file: %w", err)
		return err
	}
	defer fileReader.Close()

	zipFileInfo, err := fileReader.Stat()
	if err != nil {
		logger.Error("error getting file info: %w", err)
		return err
	}

	zipReader, err := zip.NewReader(fileReader, zipFileInfo.Size())
	if err != nil || zipReader.File == nil {
		logger.Error("error reading zip file %w", err)
		return err
	}

	// we do not need a team name here.
	slackTransformer := slack_grid.NewGridTransformer(logger)
	teamMapFile, err := os.Open(teamMap)
	if err != nil {
		logger.Error("error parsing teams.json: %w", err)
		return err
	}
	defer teamMapFile.Close()

	teamMapDecoder := json.NewDecoder(teamMapFile)
	err = teamMapDecoder.Decode(&slackTransformer.Teams)
	if err != nil {
		logger.Error("error parsing teams.json: %w", err)
		return err
	}

	valid := slackTransformer.GridPreCheck(zipReader)
	if !valid {
		return nil
	}

	err = slackTransformer.ExtractDirectory(zipReader)
	if err != nil {
		logger.Error("error extracting zip file. error:", err)
		return nil
	}

	slackExport, err := slackTransformer.ParseGridSlackExportFile(zipReader)
	if err != nil {
		logger.Error("error parsing slack export: %w", err)
		return err
	}

	channelTypes := []struct {
		channels []slack.SlackChannel
		fileType slack_grid.ChannelFiles
	}{
		{slackExport.Public, slack_grid.ChannelFilePublic},
		{slackExport.Private, slack_grid.ChannelFilePrivate},
		{slackExport.GMs, slack_grid.ChannelFileGM},
		{slackExport.DMs, slack_grid.ChannelFileDM},
	}

	for _, ct := range channelTypes {
		err = slackTransformer.HandleMovingChannels(ct.channels, ct.fileType)
		if err != nil {
			logger.Errorf("Error moving %v channels: %v", ct.fileType, err)
			return err
		}
	}

	err = slackTransformer.ZipTeamDirectories()
	if err != nil {
		logger.Error("error zipping team directories", err)
		return err
	}

	return nil
}
