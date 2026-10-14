package commands

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

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
	TransformSlackCmd.Flags().StringP("output", "o", "bulk-export.jsonl", "the output path for the bulk import file. The transform report and log are written to the same directory.")
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

// The named return is what the deferred report write reads, so a run that
// aborts still leaves a report explaining how far it got.
func transformSlackCmdF(cmd *cobra.Command, args []string) (runErr error) {
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
		if fileInfo, err := os.Stat(outputFilePath); err != nil && !os.IsNotExist(err) {
			return err
		} else if err == nil && fileInfo.IsDir() {
			return fmt.Errorf("output file \"%s\" is a directory", outputFilePath)
		}
	}

	// Every artifact of a run lands next to the bulk import file, so one
	// migration leaves one self-contained directory. Set the log and the report
	// up before the remaining validation, so anything that fails from here on is
	// explained by a report rather than only by the message cobra prints.
	artifactsDir := filepath.Dir(outputFilePath)
	logger, logFile, err := newTransformLogger(artifactsDir, "slack", debug, dryRun)
	if err != nil {
		return err
	}
	defer logFile.Close()

	slackTransformer := slack.NewTransformer(team, logger)
	slackTransformer.DryRun = dryRun
	report := slackTransformer.Report
	startTransformReport(cmd, report, "slack", inputFilePath, team, outputFilePath)
	// The flag validation and output-path checks above run before there is
	// anywhere to write, so they are the only failures that leave no report.
	defer writeTransformReport(report, artifactsDir, &runErr)

	attachmentsFullDir := path.Join(attachmentsDir, attachmentsInternal)

	if !dryRun && !skipAttachments {
		if fileInfo, statErr := os.Stat(attachmentsFullDir); os.IsNotExist(statErr) {
			if createErr := os.MkdirAll(attachmentsFullDir, 0755); createErr != nil {
				return createErr
			}
		} else if statErr != nil {
			return statErr
		} else if !fileInfo.IsDir() {
			return fmt.Errorf("file \"%s\" is not a directory", attachmentsDir)
		}
	}

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
	if err != nil {
		return err
	}
	// An archive that opens but holds nothing is not something to transform.
	// This used to return a nil error, exiting 0 with no import file and no
	// explanation.
	if len(zipReader.File) == 0 {
		return fmt.Errorf("the Slack export %q contains no files", inputFilePath)
	}

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
	if err != nil {
		if dryRun {
			slackTransformer.Logger.Error(err)
			return errors.New(dryRunFailedMsg)
		}
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

// newTransformLogger opens transform-<provider>.log inside dir, which is the
// directory the bulk import file and the report are written to, so a run leaves
// every artifact in one place. The caller owns closing the returned file.
// A dry-run logs to stdout instead, so its warnings and errors land in the
// terminal rather than in a file the operator has to go looking for.
func newTransformLogger(dir, provider string, debug, dryRun bool) (*log.Logger, *os.File, error) {
	if dir == "" {
		dir = "."
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, nil, fmt.Errorf("creating output directory %s: %w", dir, err)
	}

	logPath := filepath.Join(dir, "transform-"+provider+".log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0666)
	if err != nil {
		return nil, nil, err
	}

	logger := log.New()
	if dryRun {
		logger.SetOutput(io.MultiWriter(os.Stdout, logFile))
		logger.SetFormatter(&log.TextFormatter{ForceColors: true})
	} else {
		logger.SetOutput(logFile)
		logger.SetFormatter(customLogFormatter)
		logger.SetReportCaller(true)
	}
	if debug {
		logger.Level = log.DebugLevel
		logger.Info("Debug mode enabled")
	}

	return logger, logFile, nil
}

// startTransformReport stamps the run's metadata onto its report: what produced
// it, and when the clock started.
func startTransformReport(cmd *cobra.Command, report *intermediate.Report, provider, input, team, output string) {
	report.Metadata = intermediate.RunMetadata{
		Provider: provider,
		Version:  getVersion() + " (" + getBuildHash() + ")",
		Input:    input,
		Team:     team,
		Output:   output,
		Flags:    formatChangedFlags(cmd),
		Started:  intermediate.NowFunc().UTC(),
	}
}

// writeTransformReport closes the report and writes it next to the bulk import
// file. It runs from a defer, on success and on failure alike, and takes the
// command's named error by pointer so an aborted run is recorded in the report
// rather than losing the work that was already accounted for.
//
// A failure to write the report replaces a nil command error, but never masks
// the error that stopped the transform.
func writeTransformReport(report *intermediate.Report, dir string, runErr *error) {
	report.Finish(*runErr)

	markdownPath, jsonPath, writeErr := report.Write(dir)
	if writeErr != nil {
		if *runErr == nil {
			*runErr = writeErr
		} else if logger := report.Logger(); logger != nil {
			logger.WithError(writeErr).Error("Failed to write the transform report")
		}
		return
	}

	fmt.Print(report.SummaryText(markdownPath, jsonPath))
}

// formatChangedFlags renders the flags the operator actually set, so the report
// records what produced it without listing every default.
func formatChangedFlags(cmd *cobra.Command) string {
	changed := []string{}
	cmd.Flags().Visit(func(flag *pflag.Flag) {
		changed = append(changed, "--"+flag.Name+"="+flag.Value.String())
	})
	sort.Strings(changed)
	return strings.Join(changed, " ")
}
