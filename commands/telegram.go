package commands

import (
	"fmt"
	"os"
	"path"
	"path/filepath"

	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	"github.com/mattermost/mmetl/services/telegram"
)

var TransformTelegramCmd = &cobra.Command{
	Use:     "telegram",
	Short:   "Transforms a Telegram export.",
	Long:    "Transforms a Telegram export JSON file into a Mattermost export JSONL file.",
	Example: "  transform telegram --team myteam --file result.json --output mm_export.json",
	Args:    cobra.NoArgs,
	RunE:    transformTelegramCmdF,
}

func init() {
	TransformTelegramCmd.Flags().StringP("team", "t", "", "an existing team in Mattermost to import the data into")
	if err := TransformTelegramCmd.MarkFlagRequired("team"); err != nil {
		panic(err)
	}
	TransformTelegramCmd.Flags().StringP("file", "f", "", "the Telegram export JSON file to transform")
	if err := TransformTelegramCmd.MarkFlagRequired("file"); err != nil {
		panic(err)
	}
	TransformTelegramCmd.Flags().StringP("output", "o", "bulk-export.jsonl", "the output path")
	TransformTelegramCmd.Flags().StringP("attachments-dir", "d", "data", "the path for the attachments directory")
	TransformTelegramCmd.Flags().BoolP("skip-attachments", "a", false, "Skips copying the attachments from the import file")
	TransformTelegramCmd.Flags().Bool("debug", false, "Whether to show debug logs or not")

	TransformCmd.AddCommand(
		TransformTelegramCmd,
	)
}

func transformTelegramCmdF(cmd *cobra.Command, args []string) error {
	team, _ := cmd.Flags().GetString("team")
	inputFilePath, _ := cmd.Flags().GetString("file")
	outputFilePath, _ := cmd.Flags().GetString("output")
	attachmentsDir, _ := cmd.Flags().GetString("attachments-dir")
	skipAttachments, _ := cmd.Flags().GetBool("skip-attachments")
	debug, _ := cmd.Flags().GetBool("debug")

	// Validate output file
	if fileInfo, err := os.Stat(outputFilePath); err != nil && !os.IsNotExist(err) {
		return err
	} else if err == nil && fileInfo.IsDir() {
		return fmt.Errorf("Output file \"%s\" is a directory", outputFilePath)
	}

	// Create attachments directory if needed
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

	// Validate input file exists
	if _, err := os.Stat(inputFilePath); os.IsNotExist(err) {
		return fmt.Errorf("Input file \"%s\" does not exist", inputFilePath)
	}

	// Setup logger
	logger := log.New()
	logFile, err := os.OpenFile("transform-telegram.log", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0666)
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

	// Parse the Telegram export
	logger.Info("Parsing Telegram export file")
	telegramExport, err := telegram.ParseTelegramExportFile(inputFilePath)
	if err != nil {
		return fmt.Errorf("failed to parse Telegram export: %v", err)
	}

	logger.Infof("Parsed Telegram export: %s (%d messages)", telegramExport.Name, len(telegramExport.Messages))

	// Validate media files if attachments are enabled
	if !skipAttachments {
		exportDir := filepath.Dir(inputFilePath)
		missingFiles := telegram.ValidateAttachmentPaths(telegramExport, exportDir)
		if len(missingFiles) > 0 {
			logger.Warnf("Missing %d media files referenced in export", len(missingFiles))
			for i, file := range missingFiles {
				if i < 5 { // Log first 5 missing files
					logger.Warnf("Missing file: %s", file)
				}
			}
			if len(missingFiles) > 5 {
				logger.Warnf("... and %d more missing files", len(missingFiles)-5)
			}
		}
	}

	// Create transformer
	exportDir := filepath.Dir(inputFilePath)
	telegramTransformer := telegram.NewTransformer(team, logger, exportDir)

	// Transform the data
	logger.Info("Starting transformation")
	err = telegramTransformer.Transform(telegramExport, attachmentsDir, skipAttachments)
	if err != nil {
		return fmt.Errorf("transformation failed: %v", err)
	}

	// Export to Mattermost format
	logger.Info("Exporting to Mattermost format")
	if err = telegramTransformer.Export(outputFilePath); err != nil {
		return fmt.Errorf("export failed: %v", err)
	}

	logger.Info("Transformation succeeded!")
	fmt.Printf("Successfully transformed Telegram export to %s\n", outputFilePath)
	fmt.Printf("Log file: transform-telegram.log\n")

	// Print summary statistics
	stats := telegram.GetMediaStatistics(telegramExport)
	fmt.Printf("Summary:\n")
	fmt.Printf("  Messages: %d\n", len(telegramExport.Messages))
	fmt.Printf("  Users: %d\n", len(telegram.GetUniqueUsers(telegramExport)))
	fmt.Printf("  Photos: %d\n", stats.Photos)
	fmt.Printf("  Videos: %d\n", stats.Videos)
	fmt.Printf("  Stickers: %d\n", stats.Stickers)
	fmt.Printf("  Animations: %d\n", stats.Animations)
	fmt.Printf("  Documents: %d\n", stats.Documents)

	return nil
}

