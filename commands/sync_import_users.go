package commands

import (
	"errors"
	"os"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mmetl/services/data_integrity"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

var SyncImportUsersCmd = &cobra.Command{
	Use:   "sync-import-users",
	Short: "Checks if any users in the export file already exist in the Mattermost instance, and makes sure both username and email are the same in both cases.",
	Long:  "The command uses the Mattermost database as the source of truth, and edits the import file accordingly to match the database's state. In the case of there being two matches found (one for username and one for email), this command gives precedence to users that are active, and then gives precedence to the matching username. This command requires credentials or to have local mode enabled/available.",
	RunE:  syncImportUsersCmdF,
}

func init() {
	SyncImportUsersCmd.Flags().StringP("file", "f", "", "the bulk import jsonl file to check")
	SyncImportUsersCmd.Flags().StringP("output", "o", "", "the output file name")
	SyncImportUsersCmd.Flags().Bool("dry-run", false, "When true, the tool avoids updating user records in the import file.")

	SyncImportUsersCmd.Flags().Bool("debug", false, "Whether to show debug logs or not")
	SyncImportUsersCmd.Flags().Bool("local", false, "Whether to use local mode to check for existing users.")

	if err := SyncImportUsersCmd.MarkFlagRequired("file"); err != nil {
		panic(err)
	}

	RootCmd.AddCommand(
		SyncImportUsersCmd,
	)
}

func syncImportUsersCmdF(cmd *cobra.Command, args []string) error {
	importFilePath, _ := cmd.Flags().GetString("file")
	outputFilePath, _ := cmd.Flags().GetString("output")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	debug, _ := cmd.Flags().GetBool("debug")
	localMode, _ := cmd.Flags().GetBool("local")

	if !dryRun && outputFilePath == "" {
		return errors.New("output file is required when not in dry-run mode")
	}

	fileReader, err := os.Open(importFilePath)
	if err != nil {
		return err
	}
	defer fileReader.Close()

	logger := log.New()
	logFile, err := os.OpenFile("sync-import-users.log", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0666)
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

	var client *model.Client4
	if localMode {
		client = model.NewAPIv4SocketClient("/var/tmp/mattermost_local.socket")
	} else {
		siteURL := os.Getenv("MM_SITE_URL")
		adminToken := os.Getenv("MM_ADMIN_TOKEN")
		if siteURL == "" || adminToken == "" {
			return errors.New("please use the --local flag, or provide the Mattermost site URL and admin token via environment variables MM_SITE_URL and MM_ADMIN_TOKEN")
		}

		client = model.NewAPIv4Client(siteURL)
		client.SetToken(adminToken)
	}

	flags := data_integrity.SyncImportUsersFlags{
		DryRun:     dryRun,
		OutputFile: outputFilePath,
	}

	err = data_integrity.SyncImportUsers(fileReader, flags, client, logger)
	if err != nil {
		return err
	}

	return nil
}
