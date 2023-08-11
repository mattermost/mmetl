package commands

import (
	"os"

	"github.com/mattermost/mattermost-server/v6/model"
	"github.com/mattermost/mmetl/services/data_integrity"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

var SyncImportUsersCmd = &cobra.Command{
	Use:   "sync-import-users",
	Short: "Checks if any users in the export file already exist in the Mattermost instance, and makes sure both username and email are the same in both cases.",
	Long:  "Checks if any users in the export file already exist in the Mattermost instance, and makes sure both username and email are the same in both cases. This requires credentials or to have local mode enabled/available.",
	RunE:  syncImportUsersCmdF,
}

func init() {
	SyncImportUsersCmd.Flags().StringP("file", "f", "", "the mmetl file to check")
	SyncImportUsersCmd.Flags().StringP("output", "o", "", "the output file name")
	SyncImportUsersCmd.Flags().Bool("update-users", false, "Whether to update user records in the import file")

	SyncImportUsersCmd.Flags().Bool("debug", false, "Whether to show debug logs or not")
	SyncImportUsersCmd.Flags().Bool("local", false, "Whether to use local mode to check for existing users")

	if err := SyncImportUsersCmd.MarkFlagRequired("file"); err != nil {
		panic(err)
	}

	if err := SyncImportUsersCmd.MarkFlagRequired("output"); err != nil {
		panic(err)
	}

	RootCmd.AddCommand(
		SyncImportUsersCmd,
	)
}

func syncImportUsersCmdF(cmd *cobra.Command, args []string) error {
	importFilePath, _ := cmd.Flags().GetString("file")
	outputFilePath, _ := cmd.Flags().GetString("output")
	updateUsers, _ := cmd.Flags().GetBool("update-users")
	debug, _ := cmd.Flags().GetBool("debug")
	localMode, _ := cmd.Flags().GetBool("local")

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
		// } else {
		// 	// TODO: handle site url client
	}

	flags := data_integrity.SyncImportUsersFlags{
		UpdateUsers: updateUsers,
		OutputFile:  outputFilePath,
	}

	err = data_integrity.SyncImportUsers(fileReader, flags, client, logger)
	if err != nil {
		return err
	}

	return nil
}
