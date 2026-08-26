package commands

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
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
	TransformRocketChatCmd.Flags().StringP("output", "o", "bulk-export.jsonl", "the output path for the bulk import file. The transform report and log are written to the same directory.")
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

	TransformCmd.AddCommand(TransformRocketChatCmd)
}

// The named return is what the deferred report write reads, so a run that
// aborts still leaves a report explaining how far it got.
func transformRocketChatCmdF(cmd *cobra.Command, args []string) (runErr error) {
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

	if err := rocketchat.ValidateGuestHandling(guestHandling); err != nil {
		return err
	}

	team = strings.ToLower(team)

	// Validate output path before doing any work, matching the guard in
	// transformSlackCmdF.
	if fileInfo, err := os.Stat(outputFilePath); err != nil && !os.IsNotExist(err) {
		return err
	} else if err == nil && fileInfo.IsDir() {
		return fmt.Errorf("output file %q is a directory", outputFilePath)
	}

	// Every artifact of a run lands next to the bulk import file, so one
	// migration leaves one self-contained directory.
	artifactsDir := filepath.Dir(outputFilePath)
	logger, logFile, err := newTransformLogger(artifactsDir, "rocketchat", debug)
	if err != nil {
		return err
	}
	defer logFile.Close()

	transformer := rocketchat.NewTransformer(team, logger)
	report := transformer.Report
	startTransformReport(cmd, report, "rocketchat", dumpDir, team, outputFilePath)
	// The flag validation and output-path checks above run before there is
	// anywhere to write, so they are the only failures that leave no report.
	defer writeTransformReport(report, artifactsDir, &runErr)

	parsed, err := rocketchat.ParseDump(dumpDir, logger)
	if err != nil {
		return err
	}

	if err := transformer.Transform(parsed, skipAttachments, skipEmptyEmails, defaultEmailDomain, guestHandling); err != nil {
		return err
	}

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
		var gridfsIndex *rocketchat.GridFSIndex
		if _, err := os.Stat(chunksFilePath); err == nil {
			gridfsIndex, err = rocketchat.BuildGridFSIndex(chunksFilePath)
			if err != nil {
				return fmt.Errorf("failed to index GridFS chunks from %s: %w. "+
					"Fix the dump, or re-run with --skip-attachments to proceed without attachments", chunksFilePath, err)
			}
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("stat GridFS chunks file %s: %w", chunksFilePath, err)
		}

		attachmentsOutput := path.Join(attachmentsDir, "bulk-export-attachments")
		if err := rocketchat.ExtractAttachments(parsed.UploadsByID, gridfsIndex, attachmentsOutput, uploadsDir, report); err != nil {
			return err
		}
	}

	if err := transformer.Export(outputFilePath, botOwner); err != nil {
		return err
	}

	logger.Info("Transformation succeeded!")

	return nil
}
