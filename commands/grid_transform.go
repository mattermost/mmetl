package commands

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"os"

	"github.com/mattermost/mmetl/services/slack"
	"github.com/mattermost/mmetl/services/slack_grid"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

var GridTransformCmd = &cobra.Command{
	Use:   "grid-transform",
	Short: "Transforms a slack enterprise grid into multiple workspace export files.",
	Long: `Accepts a Slack Enterprise Grid export and splits it into one zip per workspace, to be transformed separately with mmetl transform slack.

Shared channels at the archive root are moved into the originating workspace folder under teams/. The originating workspace is the Slack team ID on the first post that has a "team" field.

Workspace IDs are inferred from each folder under teams/ already in the export (native members' team_id in users.json, then posts in that folder, then any users including guests). Pass --team-map-path only to override that mapping.

--team-map-path is a path to a JSON file mapping Slack workspace IDs to folder names under teams/:

  { "T0001": "acme", "T0002": "widgets-inc" }

Keys are the Slack workspace ID as it appears in a message's "team" field (typically T...). Values must match an existing folder under teams/ in the export; they are not Mattermost team names. Use --team on transform slack for the Mattermost team.`,
	Example: "  grid-transform --file slackexport.zip\n  grid-transform --file slackexport.zip --team-map-path teams.json",
	Args:    cobra.NoArgs,
	RunE:    gridTransformCmdF,
}

func init() {
	GridTransformCmd.Flags().StringP("file", "f", "", "the Slack export file to clean")
	GridTransformCmd.Flags().StringP("team-map-path", "t", "", "path to a JSON file mapping Slack workspace IDs to folder names under teams/; inferred from the export when omitted")

	GridTransformCmd.Flags().Bool("debug", false, "Whether to show debug logs or not")

	if err := GridTransformCmd.MarkFlagRequired("file"); err != nil {
		panic(err)
	}

	RootCmd.AddCommand(
		GridTransformCmd,
	)
}

func gridTransformCmdF(cmd *cobra.Command, args []string) error {
	inputFilePath, _ := cmd.Flags().GetString("file")
	teamMapPath, _ := cmd.Flags().GetString("team-map-path")

	debug, _ := cmd.Flags().GetBool("debug")

	logger := log.New()
	logFile, err := os.OpenFile("grid-transform-slack.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		logger.WithError(err).Error("error creating log file")
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
		logger.WithError(err).Error("error opening input file")
		return err
	}
	defer fileReader.Close()

	zipFileInfo, err := fileReader.Stat()
	if err != nil {
		logger.WithError(err).Error("error getting file info")
		return err
	}

	zipReader, err := zip.NewReader(fileReader, zipFileInfo.Size())
	if err != nil || zipReader.File == nil {
		logger.WithError(err).Error("error reading zip file")
		return err
	}

	slackTransformer := slack_grid.NewGridTransformer(logger)

	if teamMapPath != "" {
		teamMapFile, openErr := os.Open(teamMapPath)
		if openErr != nil {
			logger.WithError(openErr).Error("error opening team map file")
			return openErr
		}
		defer teamMapFile.Close()

		if decodeErr := json.NewDecoder(teamMapFile).Decode(&slackTransformer.Teams); decodeErr != nil {
			logger.WithError(decodeErr).Error("error parsing team map file")
			return decodeErr
		}
	} else {
		discovered, discoverErr := slackTransformer.DiscoverTeamMap(zipReader)
		if discoverErr != nil {
			logger.WithError(discoverErr).Error("error inferring team mapping from the export")
			return discoverErr
		}
		slackTransformer.Teams = discovered
		fmt.Printf("Inferred team mapping (override with --team-map-path):\n%s", slack_grid.FormatTeamMap(discovered))
	}

	valid := slackTransformer.GridPreCheck(zipReader)
	if !valid {
		return nil
	}

	err = slackTransformer.ExtractDirectory(zipReader)
	if err != nil {
		logger.WithError(err).Error("error extracting zip file")
		return err
	}

	slackExport, err := slackTransformer.ParseGridSlackExportFile(zipReader)
	if err != nil {
		logger.WithError(err).Error("error parsing slack export")
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
			logger.WithError(err).WithField("channel_type", ct.fileType).Error("error moving channels")
			return err
		}
	}

	err = slackTransformer.ZipTeamDirectories()
	if err != nil {
		logger.WithError(err).Error("error zipping team directories")
		return err
	}

	return nil
}
