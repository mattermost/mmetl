package commands

import (
	"archive/zip"
	"errors"
	"fmt"
	"os"
	"path"
	"runtime"
	"strconv"
	"strings"

	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	"github.com/mattermost/mmetl/services/intermediate"
	"github.com/mattermost/mmetl/services/slack"
)

const attachmentsInternal = "bulk-export-attachments"

var TransformCmd = &cobra.Command{
	Use:   "transform",
	Short: "Transforms export files into Mattermost import files",
}

var TransformSlackCmd = &cobra.Command{
	Use:     "slack",
	Short:   "Transforms a Slack export.",
	Long:    "Transforms a Slack export zipfile into a Mattermost export JSONL file.",
	Example: "  transform slack --team myteam --file my_export.zip --output mm_export.json",
	Args:    cobra.NoArgs,
	RunE:    transformSlackCmdF,
}

func init() {
	TransformSlackCmd.Flags().StringP("team", "t", "", "an existing team in Mattermost to import the data into")
	if err := TransformSlackCmd.MarkFlagRequired("team"); err != nil {
		panic(err)
	}
	TransformSlackCmd.Flags().StringP("file", "f", "", "the Slack export file to transform")
	if err := TransformSlackCmd.MarkFlagRequired("file"); err != nil {
		panic(err)
	}
	TransformSlackCmd.Flags().StringP("output", "o", "bulk-export.jsonl", "the output path")
	TransformSlackCmd.Flags().StringP("attachments-dir", "d", "data", "the path for the attachments directory")
	TransformSlackCmd.Flags().BoolP("skip-convert-posts", "c", false, "Skips converting mentions and post markup. Only for testing purposes")
	TransformSlackCmd.Flags().BoolP("skip-attachments", "a", false, "Skips copying the attachments from the import file")
	TransformSlackCmd.Flags().Bool("skip-empty-emails", false, "Ignore empty email addresses from the import file. Note that this results in invalid data.")
	TransformSlackCmd.Flags().String("default-email-domain", "", "If this flag is provided: When a user's email address is empty, the output's email address will be generated from their username and the provided domain.")
	TransformSlackCmd.Flags().String("guest-handling", slack.GuestHandlingGuest, `How to migrate Slack guest users (single- and multi-channel guests). One of:
  "guest" - migrate them as Mattermost guests (system_guest/team_guest/channel_guest). Highest fidelity, but the destination server must have Guest Accounts licensed (Professional/Enterprise) and enabled (GuestAccountsSettings.Enable); otherwise the accounts won't behave correctly.
  "user"  - migrate them as regular Mattermost users. Works everywhere, but grants guests full user permissions.
  "skip"  - drop guest users entirely, along with their memberships and authored posts/reactions.`)
	TransformSlackCmd.Flags().BoolP("allow-download", "l", false, "Allows downloading the attachments for the import file")
	TransformSlackCmd.Flags().BoolP("discard-invalid-props", "p", false, "Skips converting posts with invalid props instead discarding the props themselves")
	TransformSlackCmd.Flags().Bool("debug", false, "Whether to show debug logs or not")
	TransformSlackCmd.Flags().String("bot-owner", "", "Username of the Mattermost user who will own all imported bots. Required if the Slack export contains bot users.")
	TransformSlackCmd.Flags().Bool("dry-run", false, "Parse and transform the export without writing JSONL or copying attachments. Logs warnings and errors to the terminal. Exits non-zero if problems are found, including missing attachments that a real transform would skip.")

	TransformCmd.AddCommand(
		TransformSlackCmd,
	)

	RootCmd.AddCommand(
		TransformCmd,
	)
}

func transformSlackCmdF(cmd *cobra.Command, args []string) error {
	team, _ := cmd.Flags().GetString("team")
	inputFilePath, _ := cmd.Flags().GetString("file")
	outputFilePath, _ := cmd.Flags().GetString("output")
	attachmentsDir, _ := cmd.Flags().GetString("attachments-dir")
	skipConvertPosts, _ := cmd.Flags().GetBool("skip-convert-posts")
	skipAttachments, _ := cmd.Flags().GetBool("skip-attachments")
	skipEmptyEmails, _ := cmd.Flags().GetBool("skip-empty-emails")
	defaultEmailDomain, _ := cmd.Flags().GetString("default-email-domain")
	allowDownload, _ := cmd.Flags().GetBool("allow-download")
	discardInvalidProps, _ := cmd.Flags().GetBool("discard-invalid-props")
	debug, _ := cmd.Flags().GetBool("debug")
	botOwner, _ := cmd.Flags().GetString("bot-owner")
	guestHandling, _ := cmd.Flags().GetString("guest-handling")
	dryRun, _ := cmd.Flags().GetBool("dry-run")

	if err := slack.ValidateGuestHandling(guestHandling); err != nil {
		return err
	}

	// convert team name to lowercase since Mattermost expects all team names to be lowercase
	team = strings.ToLower(team)

	if !dryRun {
		// output file
		if fileInfo, err := os.Stat(outputFilePath); err != nil && !os.IsNotExist(err) {
			return err
		} else if err == nil && fileInfo.IsDir() {
			return fmt.Errorf("output file \"%s\" is a directory", outputFilePath)
		}

		// attachments dir
		attachmentsFullDir := path.Join(attachmentsDir, attachmentsInternal)

		if !skipAttachments {
			if fileInfo, err := os.Stat(attachmentsFullDir); os.IsNotExist(err) {
				if createErr := os.MkdirAll(attachmentsFullDir, 0755); createErr != nil {
					return createErr
				}
			} else if err != nil {
				return err
			} else if !fileInfo.IsDir() {
				return fmt.Errorf("file \"%s\" is not a directory", attachmentsDir)
			}
		}
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

	logger, closeLogger, err := configureTransformLogger(dryRun, debug, "transform-slack.log")
	if err != nil {
		return err
	}
	defer closeLogger()

	slackTransformer := slack.NewTransformer(team, logger)
	slackTransformer.DryRun = dryRun

	if err = slackTransformer.Precheck(zipReader); err != nil {
		if dryRun {
			return errors.New(dryRunFailedMsg)
		}
		return err
	}

	slackExport, err := slackTransformer.ParseSlackExportFile(zipReader, skipConvertPosts)
	if err != nil {
		return err
	}

	err = slackTransformer.Transform(slackExport, attachmentsDir, skipAttachments, discardInvalidProps, allowDownload, skipEmptyEmails, defaultEmailDomain, guestHandling)
	if err != nil && !dryRun {
		return err
	}

	botOwner = strings.TrimSpace(botOwner)
	if hasBotUsers(slackTransformer.Intermediate.UsersById) && botOwner == "" {
		err = errMissingBotOwner("Slack")
		if dryRun {
			slackTransformer.RecordError(err)
		} else {
			return err
		}
	}

	if dryRun {
		slackTransformer.CheckIntermediate()
		if err = slackTransformer.Err(); err != nil {
			return errors.New(dryRunFailedMsg)
		}
		slackTransformer.Logger.Info("Dry-run succeeded")
		return nil
	}

	if err = slackTransformer.Export(outputFilePath, botOwner); err != nil {
		return err
	}

	slackTransformer.Logger.Info("Transformation succeeded!")

	return nil
}

const dryRunFailedMsg = "dry-run failed; review the errors above"

func configureTransformLogger(dryRun, debug bool, logFileName string) (*log.Logger, func(), error) {
	logger := log.New()
	closer := func() {}

	if dryRun {
		logger.SetOutput(os.Stdout)
		logger.SetFormatter(&log.TextFormatter{ForceColors: true})
	} else {
		logFile, err := os.OpenFile(logFileName, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
		if err != nil {
			return nil, nil, err
		}
		closer = func() { logFile.Close() }
		logger.SetOutput(logFile)
		logger.SetFormatter(customLogFormatter)
		logger.SetReportCaller(true)
	}

	if debug {
		logger.Level = log.DebugLevel
		logger.Info("Debug mode enabled")
	}

	return logger, closer, nil
}

func hasBotUsers(users map[string]*intermediate.IntermediateUser) bool {
	for _, user := range users {
		if user != nil && user.IsBot {
			return true
		}
	}
	return false
}

func errMissingBotOwner(source string) error {
	return fmt.Errorf("the %s export contains bot users but --bot-owner was not specified. Please provide the username of a Mattermost user who will own the imported bots", source)
}

var customLogFormatter = &log.JSONFormatter{
	CallerPrettyfier: func(frame *runtime.Frame) (function string, file string) {
		fileName := path.Base(frame.File) + ":" + strconv.Itoa(frame.Line)
		return "", fileName
	},
}
