package commands

import (
	"errors"
	"fmt"
	"os"
	"path"
	"strings"

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
	TransformRocketChatCmd.Flags().String("guest-handling", rocketchat.GuestHandlingGuest, `How to migrate RocketChat guest users (users whose roles include "guest"). One of:
  "guest" - migrate them as Mattermost guests (system_guest/team_guest/channel_guest). Highest fidelity, but the destination server must have Guest Accounts licensed (Professional/Enterprise) and enabled (GuestAccountsSettings.Enable); otherwise the accounts won't behave correctly.
  "user"  - migrate them as regular Mattermost users. Works everywhere, but grants guests full user permissions.
  "skip"  - drop guest users entirely, along with their memberships and authored posts.`)
	TransformRocketChatCmd.Flags().Bool("debug", false, "Whether to show debug logs or not")
	TransformRocketChatCmd.Flags().String("bot-owner", "", "Username of the Mattermost user who will own all imported bots. Required if the RocketChat export contains bot users.")
	TransformRocketChatCmd.Flags().Bool("dry-run", false, "Parse and transform the export without writing JSONL or copying attachments. Logs warnings and errors to the terminal. Exits non-zero if problems are found, including missing attachments that a real transform would skip.")

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
	guestHandling, _ := cmd.Flags().GetString("guest-handling")
	debug, _ := cmd.Flags().GetBool("debug")
	botOwner, _ := cmd.Flags().GetString("bot-owner")
	dryRun, _ := cmd.Flags().GetBool("dry-run")

	if err := rocketchat.ValidateGuestHandling(guestHandling); err != nil {
		return err
	}

	team = strings.ToLower(team)

	if !dryRun {
		if fileInfo, err := os.Stat(outputFilePath); err != nil && !os.IsNotExist(err) {
			return err
		} else if err == nil && fileInfo.IsDir() {
			return fmt.Errorf("output file %q is a directory", outputFilePath)
		}
	}

	logger, closeLogger, err := configureTransformLogger(dryRun, debug, "transform-rocketchat.log")
	if err != nil {
		return err
	}
	defer closeLogger()

	parsed, err := rocketchat.ParseDump(dumpDir, logger)
	if err != nil {
		return err
	}

	transformer := rocketchat.NewTransformer(team, logger)
	if err = transformer.Transform(parsed, skipAttachments, skipEmptyEmails, defaultEmailDomain, guestHandling); err != nil && !dryRun {
		return err
	}

	botOwner = strings.TrimSpace(botOwner)
	if hasBotUsers(transformer.Intermediate.UsersById) && botOwner == "" {
		err = errMissingBotOwner("RocketChat")
		if dryRun {
			transformer.RecordError(err)
		} else {
			return err
		}
	}

	if !skipAttachments {
		chunksFilePath := path.Join(dumpDir, "rocketchat_uploads.chunks.bson")
		var gridfsIndex *rocketchat.GridFSIndex
		_, statErr := os.Stat(chunksFilePath)
		if statErr == nil {
			gridfsIndex, err = rocketchat.BuildGridFSIndex(chunksFilePath)
			if err != nil {
				return fmt.Errorf("failed to index GridFS chunks from %s: %w. "+
					"Fix the dump, or re-run with --skip-attachments to proceed without attachments", chunksFilePath, err)
			}
		} else if !os.IsNotExist(statErr) {
			return fmt.Errorf("stat GridFS chunks file %s: %w", chunksFilePath, statErr)
		}

		if dryRun {
			if err = rocketchat.VerifyAttachments(parsed.UploadsByID, gridfsIndex, uploadsDir, logger); err != nil {
				transformer.RecordError(err)
			}
		} else {
			attachmentsOutput := path.Join(attachmentsDir, "bulk-export-attachments")
			if err = rocketchat.ExtractAttachments(parsed.UploadsByID, gridfsIndex, attachmentsOutput, uploadsDir, logger); err != nil {
				return err
			}
		}
	}

	if dryRun {
		transformer.CheckIntermediate()
		if err = transformer.Err(); err != nil {
			return errors.New(dryRunFailedMsg)
		}
		logger.Info("Dry-run succeeded")
		return nil
	}

	if err = transformer.Export(outputFilePath, botOwner); err != nil {
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
