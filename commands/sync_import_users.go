package commands

import (
	"errors"
	"os"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mmetl/services/data_integrity"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

const (
	syncImportUsersLong = `Checks if any users in the export file already exist in the Mattermost instance and ensures both username and email are consistent between the import file and the database. This command uses the Mattermost database as the source of truth and modifies the import file accordingly to match the database's state.`

	syncImportUsersExample = `  # Remote mode with environment variables
  export MM_SITE_URL="https://your-mattermost-instance.com"
  export MM_ADMIN_TOKEN="your-admin-token"
  mmetl sync-import-users --file import.jsonl --output synced-import.jsonl

  # Local mode using Unix socket
  mmetl sync-import-users --file import.jsonl --output synced-import.jsonl --local

  # Dry run to preview changes without modifying the file
  mmetl sync-import-users --file import.jsonl --dry-run`

	syncImportUsersDocsExtra = `## When to Use This Command

- Before importing users to prevent conflicts with existing users.
- To synchronize user data between the import file and an existing Mattermost instance.
- To resolve username/email mismatches before performing an import.

## How It Works

- The command checks each user in the import file against the Mattermost database.
- If a username exists with a different email, the email in the import file is updated.
- If an email exists with a different username, the username in the import file is updated.
- In case of conflicts (two different users found - one by username, one by email), the command prioritizes active users and then gives precedence to the username match.
- The command also removes duplicate channel memberships if found.
- All username changes are tracked and automatically applied to posts, channels, and memberships throughout the import file.

## Authentication

This command requires credentials to access your Mattermost instance. You can authenticate in two ways:

**1. Remote mode (default):** Set environment variables:

` + "```sh" + `
export MM_SITE_URL="https://your-mattermost-instance.com"
export MM_ADMIN_TOKEN="your-admin-token"
mmetl sync-import-users --file import.jsonl --output synced-import.jsonl
` + "```" + `

**2. Local mode:** Use the ` + "`--local`" + ` flag to connect via Unix socket (requires local access to the Mattermost server):

` + "```sh" + `
mmetl sync-import-users --file import.jsonl --output synced-import.jsonl --local
` + "```" + `

## Output

The command creates a log file named ` + "`sync-import-users.log`" + ` in the current directory containing:

- Details of all user checks performed.
- Any username or email changes made.
- Warnings about conflicts or duplicate users.
- Summary statistics of changes.

## Important Notes

- Always review the log file after running this command.
- Consider using ` + "`--dry-run`" + ` first to preview changes.
- Username changes are automatically propagated to all references in posts, channels, and direct messages.
`
)

var SyncImportUsersCmd = &cobra.Command{
	Use:     "sync-import-users",
	Short:   "Checks if any users in the export file already exist in the Mattermost instance, and makes sure both username and email are the same in both cases.",
	Long:    syncImportUsersLong,
	Example: syncImportUsersExample,
	Annotations: map[string]string{
		"docs_extra": syncImportUsersDocsExtra,
	},
	RunE: syncImportUsersCmdF,
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
