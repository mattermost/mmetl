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

	"github.com/mattermost/mmetl/services/rocketchat"
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
	TransformSlackCmd.Flags().BoolP("allow-download", "l", false, "Allows downloading the attachments for the import file")
	TransformSlackCmd.Flags().BoolP("discard-invalid-props", "p", false, "Skips converting posts with invalid props instead discarding the props themselves")
	TransformSlackCmd.Flags().Bool("debug", false, "Whether to show debug logs or not")
	TransformSlackCmd.Flags().String("bot-owner", "", "Username of the Mattermost user who will own all imported bots. Required if the Slack export contains bot users.")

	TransformRocketChatCmd.Flags().StringP("team", "t", "", "an existing team in Mattermost to import the data into")
	if err := TransformRocketChatCmd.MarkFlagRequired("team"); err != nil {
		panic(err)
	}
	TransformRocketChatCmd.Flags().StringP("dump-dir", "d", "", "path to the mongodump output directory (containing .bson files)")
	if err := TransformRocketChatCmd.MarkFlagRequired("dump-dir"); err != nil {
		panic(err)
	}
	TransformRocketChatCmd.Flags().StringP("output", "o", "bulk-export.jsonl", "the output path")
	TransformRocketChatCmd.Flags().String("attachments-dir", "data", "the path for the attachments directory")
	TransformRocketChatCmd.Flags().String("uploads-dir", "", "path to Rocket.Chat FileSystem uploads directory (if not using GridFS)")
	TransformRocketChatCmd.Flags().BoolP("skip-attachments", "a", false, "Skips extracting file attachments")
	TransformRocketChatCmd.Flags().Bool("skip-empty-emails", false, "Ignore empty email addresses from the import file. Note that this results in invalid data.")
	TransformRocketChatCmd.Flags().String("default-email-domain", "", "If this flag is provided: When a user's email address is empty, the output's email address will be generated from their username and the provided domain.")
	TransformRocketChatCmd.Flags().Bool("debug", false, "Whether to show debug logs or not")

	TransformCmd.AddCommand(
		TransformSlackCmd,
		TransformRocketChatCmd,
	)

	RootCmd.AddCommand(
		TransformCmd,
	)
}

var TransformRocketChatCmd = &cobra.Command{
	Use:     "rocketchat",
	Short:   "Transforms a Rocket.Chat mongodump export.",
	Long:    "Transforms a Rocket.Chat mongodump directory into a Mattermost export JSONL file.",
	Example: "  transform rocketchat --team myteam --dump-dir /backup/meteor --output mm_export.jsonl",
	Args:    cobra.NoArgs,
	RunE:    transformRocketChatCmdF,
}

func transformRocketChatCmdF(cmd *cobra.Command, args []string) error {
	team, _ := cmd.Flags().GetString("team")
	dumpDir, _ := cmd.Flags().GetString("dump-dir")
	outputFilePath, _ := cmd.Flags().GetString("output")
	attachmentsDir, _ := cmd.Flags().GetString("attachments-dir")
	uploadsDir, _ := cmd.Flags().GetString("uploads-dir")
	skipAttachments, _ := cmd.Flags().GetBool("skip-attachments")
	skipEmptyEmails, _ := cmd.Flags().GetBool("skip-empty-emails")
	defaultEmailDomain, _ := cmd.Flags().GetString("default-email-domain")
	debug, _ := cmd.Flags().GetBool("debug")

	team = strings.ToLower(team)

	logger := log.New()
	logFile, err := os.OpenFile("transform-rocketchat.log", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0666)
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

	transformer := rocketchat.NewTransformer(team, logger)
	transformer.Transform(parsed, skipAttachments, skipEmptyEmails, defaultEmailDomain)

	if !skipAttachments {
		chunksFilePath := path.Join(dumpDir, "rocketchat_uploads.chunks.bson")
		var gridfsChunks map[string][]rocketchat.GridFSChunk
		if _, err := os.Stat(chunksFilePath); err == nil {
			gridfsChunks, err = rocketchat.LoadGridFSChunks(chunksFilePath)
			if err != nil {
				logger.Warnf("Failed to load GridFS chunks: %v", err)
			}
		}

		attachmentsOutput := path.Join(attachmentsDir, "bulk-export-attachments")
		if err := rocketchat.ExtractAttachments(parsed.UploadsByID, gridfsChunks, attachmentsOutput, uploadsDir, logger); err != nil {
			return err
		}
	}

	if err := transformer.Export(outputFilePath); err != nil {
		return err
	}

	logger.Infof("Transformation succeeded! Users: %d, Public channels: %d, Private channels: %d, Posts: %d",
		len(transformer.Intermediate.UsersById),
		len(transformer.Intermediate.PublicChannels),
		len(transformer.Intermediate.PrivateChannels),
		len(transformer.Intermediate.Posts),
	)

	return nil
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

	// convert team name to lowercase since Mattermost expects all team names to be lowercase
	team = strings.ToLower(team)

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
	slackTransformer := slack.NewTransformer(team, logger)

	slackExport, err := slackTransformer.ParseSlackExportFile(zipReader, skipConvertPosts)
	if err != nil {
		return err
	}

	err = slackTransformer.Transform(slackExport, attachmentsDir, skipAttachments, discardInvalidProps, allowDownload, skipEmptyEmails, defaultEmailDomain)
	if err != nil {
		return err
	}

	// Validate that --bot-owner is provided if there are bot users
	hasBots := false
	for _, user := range slackTransformer.Intermediate.UsersById {
		if user.IsBot {
			hasBots = true
			break
		}
	}
	botOwner = strings.TrimSpace(botOwner)
	if hasBots && botOwner == "" {
		return fmt.Errorf("the Slack export contains bot users but --bot-owner was not specified. Please provide the username of a Mattermost user who will own the imported bots")
	}

	if err = slackTransformer.Export(outputFilePath, botOwner); err != nil {
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
