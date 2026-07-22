package commands

import (
	"archive/zip"
	"io"
	"os"

	"github.com/mattermost/mmetl/services/slack"
	log "github.com/sirupsen/logrus"
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
	CheckSlackCmd.Flags().Bool("debug", true, "Whether to show debug logs or not")
	CheckSlackCmd.Flags().Bool("skip-empty-emails", false, "Ignore empty email addresses from the import file. Note that this results in invalid data.")
	CheckSlackCmd.Flags().String("default-email-domain", "", "If this flag is provided: When a user's email address is empty, the output's email address will be generated from their username and the provided domain.")
	CheckSlackCmd.Flags().String("guest-handling", slack.GuestHandlingGuest, `How to migrate Slack guest users when checking the export. One of "guest", "user", or "skip". Use the same value you plan to pass to "transform slack" so the check reflects that mode.`)

	if err := CheckSlackCmd.MarkFlagRequired("file"); err != nil {
		panic(err)
	}

	CheckCmd.AddCommand(
		CheckSlackCmd,
	)

	RootCmd.AddCommand(
		CheckCmd,
	)
}

func checkSlackCmdF(cmd *cobra.Command, args []string) error {
	inputFilePath, _ := cmd.Flags().GetString("file")
	debug, _ := cmd.Flags().GetBool("debug")
	skipEmptyEmails, _ := cmd.Flags().GetBool("skip-empty-emails")
	defaultEmailDomain, _ := cmd.Flags().GetString("default-email-domain")
	guestHandling, _ := cmd.Flags().GetString("guest-handling")

	if err := slack.ValidateGuestHandling(guestHandling); err != nil {
		return err
	}

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
	// Write to both stdout and the log file so operators see the check output
	// (including guest-handling decisions) directly, while it's still persisted
	// to check-slack.log.
	logger.SetOutput(io.MultiWriter(os.Stdout, logFile))
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

	err = slackTransformer.Transform(slackExport, "", true, true, false, skipEmptyEmails, defaultEmailDomain, guestHandling)
	if err != nil {
		return err
	}

	slackTransformer.CheckIntermediate()

	return nil
}
