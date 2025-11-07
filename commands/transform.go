package commands

import (
	"archive/zip"
	"fmt"
	"os"
	"path"
	"runtime"
	"strconv"
	"strings"

	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

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
	TransformSlackCmd.Flags().String("slack-workspace-name", "", "the name of the workspace to import from multi-workspace Slack exports (e.g., 'team1' for exports with teams/team1/ structure)")
	TransformSlackCmd.Flags().StringP("attachments-dir", "d", "data", "the path for the attachments directory")
	TransformSlackCmd.Flags().BoolP("skip-convert-posts", "c", false, "Skips converting mentions and post markup. Only for testing purposes")
	TransformSlackCmd.Flags().BoolP("skip-attachments", "a", false, "Skips copying the attachments from the import file")
	TransformSlackCmd.Flags().Bool("skip-empty-emails", false, "Ignore empty email addresses from the import file. Note that this results in invalid data.")
	TransformSlackCmd.Flags().String("default-email-domain", "", "If this flag is provided: When a user's email address is empty, the output's email address will be generated from their username and the provided domain.")
	TransformSlackCmd.Flags().BoolP("allow-download", "l", false, "Allows downloading the attachments for the import file")
	TransformSlackCmd.Flags().BoolP("discard-invalid-props", "p", false, "Skips converting posts with invalid props instead discarding the props themselves")
	TransformSlackCmd.Flags().Bool("debug", false, "Whether to show debug logs or not")

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
	slackWorkspaceName, _ := cmd.Flags().GetString("slack-workspace-name")
	attachmentsDir, _ := cmd.Flags().GetString("attachments-dir")
	skipConvertPosts, _ := cmd.Flags().GetBool("skip-convert-posts")
	skipAttachments, _ := cmd.Flags().GetBool("skip-attachments")
	skipEmptyEmails, _ := cmd.Flags().GetBool("skip-empty-emails")
	defaultEmailDomain, _ := cmd.Flags().GetString("default-email-domain")
	allowDownload, _ := cmd.Flags().GetBool("allow-download")
	discardInvalidProps, _ := cmd.Flags().GetBool("discard-invalid-props")
	debug, _ := cmd.Flags().GetBool("debug")

	// convert team name to lowercase since Mattermost expects all team names to be lowercase
	team = strings.ToLower(team)

	// output file
	if fileInfo, err := os.Stat(outputFilePath); err != nil && !os.IsNotExist(err) {
		return err
	} else if err == nil && fileInfo.IsDir() {
		return fmt.Errorf("Output file \"%s\" is a directory", outputFilePath)
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
			return fmt.Errorf("File \"%s\" is not a directory", attachmentsDir)
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

	logger := log.New()
	logFile, err := os.OpenFile("transform-slack.log", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0666)
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

	// Detect available workspaces in the export
	workspaces := slack.DetectWorkspaces(zipReader)

	if len(workspaces) > 0 && slackWorkspaceName == "" {
		// Multi-workspace export without flag specified - use first workspace
		slackWorkspaceName = workspaces[0]
		logger.Infof("Detected multi-workspace export. Processing workspace: %s", slackWorkspaceName)
		if len(workspaces) > 1 {
			logger.Infof("Other workspaces available: %v", workspaces[1:])
			logger.Info("Use --slack-workspace-name to select a different workspace")
		}
	} else if slackWorkspaceName != "" && len(workspaces) > 0 {
		// Validate that the specified workspace exists
		found := false
		for _, ws := range workspaces {
			if ws == slackWorkspaceName {
				found = true
				break
			}
		}
		if !found {
			logger.Errorf("Specified workspace '%s' not found in export. Available workspaces: %v", slackWorkspaceName, workspaces)
			return fmt.Errorf("workspace '%s' not found", slackWorkspaceName)
		}
		logger.Infof("Processing specified workspace: %s", slackWorkspaceName)
	} else if slackWorkspaceName == "" {
		logger.Info("Processing single workspace export")
	}

	slackTransformer := slack.NewTransformer(team, slackWorkspaceName, logger)

	slackExport, err := slackTransformer.ParseSlackExportFile(zipReader, skipConvertPosts)
	if err != nil {
		return err
	}

	err = slackTransformer.Transform(slackExport, attachmentsDir, skipAttachments, discardInvalidProps, allowDownload, skipEmptyEmails, defaultEmailDomain)
	if err != nil {
		return err
	}

	if err = slackTransformer.Export(outputFilePath); err != nil {
		return err
	}

	slackTransformer.Logger.Info("Transformation succeeded!")

	return nil
}

var customLogFormatter = &log.JSONFormatter{
	CallerPrettyfier: func(frame *runtime.Frame) (function string, file string) {
		fileName := path.Base(frame.File) + ":" + strconv.Itoa(frame.Line)
		return "", fileName
	},
}
