package commands

import (
	"archive/zip"
	"os"

	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	"github.com/mattermost/mmetl/services/rocketchat"
	"github.com/mattermost/mmetl/services/slack"
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

var CheckRocketChatCmd = &cobra.Command{
	Use:   "rocketchat",
	Short: "Checks the integrity of a Rocket.Chat mongodump export.",
	Long: `Checks the integrity of a Rocket.Chat mongodump export directory.

Before running this command, export your Rocket.Chat MongoDB database using mongodump
(https://www.mongodb.com/docs/database-tools/mongodump/):

  mongodump --uri="mongodb://localhost:3001/meteor" --out=/tmp/rc-dump

Then pass the database subdirectory to --dump-dir (e.g. /tmp/rc-dump/meteor).`,
	Example: "  check rocketchat --dump-dir /tmp/rc-dump/meteor",
	Args:    cobra.NoArgs,
	RunE:    checkRocketChatCmdF,
}

func init() {
	CheckSlackCmd.Flags().StringP("file", "f", "", "the Slack export file to transform")
	CheckSlackCmd.Flags().Bool("debug", true, "Whether to show debug logs or not")
	CheckSlackCmd.Flags().Bool("skip-empty-emails", false, "Ignore empty email addresses from the import file. Note that this results in invalid data.")
	CheckSlackCmd.Flags().String("default-email-domain", "", "If this flag is provided: When a user's email address is empty, the output's email address will be generated from their username and the provided domain.")

	if err := CheckSlackCmd.MarkFlagRequired("file"); err != nil {
		panic(err)
	}

	CheckRocketChatCmd.Flags().StringP("dump-dir", "d", "", "path to the mongodump output directory")
	if err := CheckRocketChatCmd.MarkFlagRequired("dump-dir"); err != nil {
		panic(err)
	}
	CheckRocketChatCmd.Flags().Bool("debug", false, "Whether to show debug logs or not")
	CheckRocketChatCmd.Flags().Bool("skip-empty-emails", false, "Ignore empty email addresses from the import file. Note that this results in invalid data.")
	CheckRocketChatCmd.Flags().String("default-email-domain", "", "If this flag is provided: When a user's email address is empty, the output's email address will be generated from their username and the provided domain.")

	CheckCmd.AddCommand(
		CheckSlackCmd,
		CheckRocketChatCmd,
	)

	RootCmd.AddCommand(
		CheckCmd,
	)
}

func checkRocketChatCmdF(cmd *cobra.Command, args []string) error {
	dumpDir, _ := cmd.Flags().GetString("dump-dir")
	debug, _ := cmd.Flags().GetBool("debug")
	skipEmptyEmails, _ := cmd.Flags().GetBool("skip-empty-emails")
	defaultEmailDomain, _ := cmd.Flags().GetString("default-email-domain")

	logger := log.New()
	logFile, err := os.OpenFile("check-rocketchat.log", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0666)
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

	parsed, err := rocketchat.ParseDump(dumpDir, logger)
	if err != nil {
		return err
	}

	transformer := rocketchat.NewTransformer("test", logger)

	transformer.Transform(parsed, false, skipEmptyEmails, defaultEmailDomain)

	transformer.CheckIntermediate()

	return nil
}

func checkSlackCmdF(cmd *cobra.Command, args []string) error {
	inputFilePath, _ := cmd.Flags().GetString("file")
	debug, _ := cmd.Flags().GetBool("debug")
	skipEmptyEmails, _ := cmd.Flags().GetBool("skip-empty-emails")
	defaultEmailDomain, _ := cmd.Flags().GetString("default-email-domain")

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
	logFile, err := os.OpenFile("check-slack.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
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
	slackTransformer := slack.NewTransformer("test", logger)

	valid := slackTransformer.Precheck(zipReader)
	if !valid {
		return nil
	}

	slackExport, err := slackTransformer.ParseSlackExportFile(zipReader, true)
	if err != nil {
		return err
	}

	err = slackTransformer.Transform(slackExport, "", true, true, false, skipEmptyEmails, defaultEmailDomain)
	if err != nil {
		return err
	}

	slackTransformer.CheckIntermediate()

	return nil
}
