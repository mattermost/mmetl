package commands

import (
	"os"

	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	"github.com/mattermost/mmetl/services/rocketchat"
)

var CheckRocketChatCmd = &cobra.Command{
	Use:   "rocketchat",
	Short: "Checks the integrity of a RocketChat mongodump export.",
	Long: `Checks the integrity of a RocketChat mongodump export directory.

Before running this command, export your RocketChat MongoDB database using mongodump
(https://www.mongodb.com/docs/database-tools/mongodump/):

  mongodump --uri="mongodb://localhost:3001/meteor" --out=/tmp/rc-dump

Then pass the database subdirectory to --dump-dir (e.g. /tmp/rc-dump/meteor).`,
	Example: "  check rocketchat --dump-dir /tmp/rc-dump/meteor",
	Args:    cobra.NoArgs,
	RunE:    checkRocketChatCmdF,
}

func init() {
	CheckRocketChatCmd.Flags().StringP("dump-dir", "d", "", "path to the mongodump output directory")
	if err := CheckRocketChatCmd.MarkFlagRequired("dump-dir"); err != nil {
		panic(err)
	}
	CheckRocketChatCmd.Flags().Bool("debug", false, "Whether to show debug logs or not")
	CheckRocketChatCmd.Flags().Bool("skip-empty-emails", false, "Ignore empty email addresses from the import file. Note that this results in invalid data.")
	CheckRocketChatCmd.Flags().String("default-email-domain", "", "If this flag is provided: When a user's email address is empty, the output's email address will be generated from their username and the provided domain.")
	CheckRocketChatCmd.Flags().String("guest-handling", rocketchat.GuestHandlingGuest, `How guest users would be handled by "transform rocketchat", so their treatment can be previewed here. One of "guest", "user", or "skip" (see "transform rocketchat --help").`)

	CheckCmd.AddCommand(CheckRocketChatCmd)
}

func checkRocketChatCmdF(cmd *cobra.Command, args []string) error {
	dumpDir, _ := cmd.Flags().GetString("dump-dir")
	debug, _ := cmd.Flags().GetBool("debug")
	skipEmptyEmails, _ := cmd.Flags().GetBool("skip-empty-emails")
	defaultEmailDomain, _ := cmd.Flags().GetString("default-email-domain")
	guestHandling, _ := cmd.Flags().GetString("guest-handling")

	if err := rocketchat.ValidateGuestHandling(guestHandling); err != nil {
		return err
	}

	// check is a preview/diagnostic command, so log to stdout (with a plain,
	// human-readable formatter) rather than a file — otherwise the guest
	// decisions and validation warnings emitted below are invisible to the
	// operator at check time.
	logger := log.New()
	logger.SetOutput(os.Stdout)
	logger.SetFormatter(&log.TextFormatter{DisableTimestamp: true})

	if debug {
		logger.Level = log.DebugLevel
		logger.Info("Debug mode enabled")
	}

	parsed, err := rocketchat.ParseDump(dumpDir, logger)
	if err != nil {
		return err
	}

	transformer := rocketchat.NewTransformer("test", logger)

	transformer.Transform(parsed, false, skipEmptyEmails, defaultEmailDomain, guestHandling)

	transformer.CheckIntermediate()

	return nil
}
