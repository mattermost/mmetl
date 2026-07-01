package commands

import (
	"fmt"
	"os"
	"path"
	"strings"

	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	"github.com/mattermost/mmetl/services/rocketchat"
)

var TransformRocketChatCmd = &cobra.Command{
	Use:   "rocketchat",
	Short: "Transforms a RocketChat mongodump export.",
	Long: `Transforms a RocketChat mongodump directory into a Mattermost export JSONL file.

Before running this command, export your RocketChat MongoDB database using mongodump
(https://www.mongodb.com/docs/database-tools/mongodump/):

  mongodump --uri="mongodb://localhost:3001/meteor" --out=/tmp/rc-dump

Then pass the database subdirectory to --dump-dir (e.g. /tmp/rc-dump/meteor).`,
	Example: "  transform rocketchat --team myteam --dump-dir /tmp/rc-dump/meteor --output mm_export.jsonl",
	Args:    cobra.NoArgs,
	RunE:    transformRocketChatCmdF,
}

func init() {
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
	TransformRocketChatCmd.Flags().String("uploads-dir", "", "path to RocketChat FileSystem uploads directory (if not using GridFS)")
	TransformRocketChatCmd.Flags().BoolP("skip-attachments", "a", false, "Skips extracting file attachments")
	TransformRocketChatCmd.Flags().Bool("skip-empty-emails", false, "Ignore empty email addresses from the import file. Note that this results in invalid data.")
	TransformRocketChatCmd.Flags().String("default-email-domain", "", "If this flag is provided: When a user's email address is empty, the output's email address will be generated from their username and the provided domain.")
	TransformRocketChatCmd.Flags().Bool("debug", false, "Whether to show debug logs or not")
	TransformRocketChatCmd.Flags().String("bot-owner", "", "Username of the Mattermost user who will own all imported bots. Required if the RocketChat export contains bot users.")

	TransformCmd.AddCommand(TransformRocketChatCmd)
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
	botOwner, _ := cmd.Flags().GetString("bot-owner")

	team = strings.ToLower(team)

	// Validate output path before doing any work, matching the guard in
	// transformSlackCmdF.
	if fileInfo, err := os.Stat(outputFilePath); err != nil && !os.IsNotExist(err) {
		return err
	} else if err == nil && fileInfo.IsDir() {
		return fmt.Errorf("output file %q is a directory", outputFilePath)
	}

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

	// Validate that --bot-owner is provided if there are bot users.
	// Do this before attachment extraction so we fail fast without doing
	// expensive I/O that would be wasted.
	hasBots := false
	for _, user := range transformer.Intermediate.UsersById {
		if user.IsBot {
			hasBots = true
			break
		}
	}
	botOwner = strings.TrimSpace(botOwner)
	if hasBots && botOwner == "" {
		return fmt.Errorf("the RocketChat export contains bot users but --bot-owner was not specified. Please provide the username of a Mattermost user who will own the imported bots")
	}

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

	if err := transformer.Export(outputFilePath, botOwner); err != nil {
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
