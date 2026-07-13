package commands

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	"github.com/mattermost/mmetl/services/confluence"
	"github.com/mattermost/mmetl/services/slack"
)

const (
	// slackAttachmentsDir is the subdirectory for Slack attachments.
	slackAttachmentsDir = "bulk-export-attachments"
	// confluenceAttachmentsDir matches the Mattermost bulk import expected path (model.ExportDataDir).
	confluenceAttachmentsDir = "data"
)

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

var TransformConfluenceCmd = &cobra.Command{
	Use:     "confluence",
	Short:   "Transforms a Confluence export.",
	Long:    "Transforms a Confluence Cloud CSV space export into a Mattermost Wiki/Pages JSONL file.",
	Example: "  transform confluence --team myteam --channel docs --file confluence-export.zip --output wiki-import.jsonl",
	Args:    cobra.NoArgs,
	RunE:    transformConfluenceCmdF,
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

	// Confluence command flags
	TransformConfluenceCmd.Flags().StringP("team", "t", "", "the target team in Mattermost")
	if err := TransformConfluenceCmd.MarkFlagRequired("team"); err != nil {
		panic(err)
	}
	TransformConfluenceCmd.Flags().StringP("channel", "c", "", "deprecated and ignored in v2: the Space's backing channel is resolved at import time")
	TransformConfluenceCmd.Flags().StringP("file", "f", "", "the Confluence export file (ZIP) to transform")
	if err := TransformConfluenceCmd.MarkFlagRequired("file"); err != nil {
		panic(err)
	}
	TransformConfluenceCmd.Flags().StringP("output", "o", "import.jsonl", "the output JSONL file path")
	TransformConfluenceCmd.Flags().String("bundle", "", "write a single self-contained import archive (zip) at this path instead of loose files")
	TransformConfluenceCmd.Flags().StringP("attachments-dir", "d", "data", "the path for extracted attachments")
	TransformConfluenceCmd.Flags().StringP("user-mapping", "u", "", "CSV file mapping Confluence users to Mattermost users")
	TransformConfluenceCmd.Flags().String("fallback-user", "", "Mattermost username to use for unmapped Confluence users")
	TransformConfluenceCmd.Flags().Bool("require-user-mapping", false, "fail if any Confluence author is not mapped to a Mattermost user")
	TransformConfluenceCmd.Flags().BoolP("skip-attachments", "a", false, "skip extracting attachments")
	TransformConfluenceCmd.Flags().Int("max-depth", 10, "maximum page hierarchy depth (deeper pages are flattened)")
	TransformConfluenceCmd.Flags().Bool("dry-run", false, "validate without writing output files")
	TransformConfluenceCmd.Flags().Bool("validate-only", false, "only run pre-flight validation, do not transform")
	TransformConfluenceCmd.Flags().String("mattermost-url", "", "Mattermost server URL for validation (optional)")
	TransformConfluenceCmd.Flags().String("mattermost-token", "", "Mattermost auth token for validation (optional)")
	TransformConfluenceCmd.Flags().Bool("fail-on-restricted", false, "fail if any page has a View restriction (not preserved on import)")
	TransformConfluenceCmd.Flags().Bool("debug", false, "enable debug logging")

	TransformCmd.AddCommand(
		TransformSlackCmd,
		TransformConfluenceCmd,
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

	// convert team name to lowercase since Mattermost expects all team names to be lowercase
	team = strings.ToLower(team)

	// output file
	if fileInfo, err := os.Stat(outputFilePath); err != nil && !os.IsNotExist(err) {
		return err
	} else if err == nil && fileInfo.IsDir() {
		return fmt.Errorf("output file \"%s\" is a directory", outputFilePath)
	}

	// attachments dir
	attachmentsFullDir := path.Join(attachmentsDir, slackAttachmentsDir)

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

func transformConfluenceCmdF(cmd *cobra.Command, args []string) error {
	team, _ := cmd.Flags().GetString("team")
	channel, _ := cmd.Flags().GetString("channel")
	inputFilePath, _ := cmd.Flags().GetString("file")
	outputFilePath, _ := cmd.Flags().GetString("output")
	bundlePath, _ := cmd.Flags().GetString("bundle")
	attachmentsDir, _ := cmd.Flags().GetString("attachments-dir")
	userMappingPath, _ := cmd.Flags().GetString("user-mapping")
	fallbackUser, _ := cmd.Flags().GetString("fallback-user")
	requireUserMapping, _ := cmd.Flags().GetBool("require-user-mapping")
	skipAttachments, _ := cmd.Flags().GetBool("skip-attachments")
	maxDepth, _ := cmd.Flags().GetInt("max-depth")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	validateOnly, _ := cmd.Flags().GetBool("validate-only")
	mattermostURL, _ := cmd.Flags().GetString("mattermost-url")
	mattermostToken, _ := cmd.Flags().GetString("mattermost-token")
	failOnRestricted, _ := cmd.Flags().GetBool("fail-on-restricted")
	debug, _ := cmd.Flags().GetBool("debug")

	// Normalize team and channel names to lowercase, as Mattermost expects.
	team = strings.ToLower(team)
	channel = strings.ToLower(channel)

	if channel != "" {
		fmt.Println("Note: --channel is deprecated and ignored in v2; the Space's backing channel is resolved at import time.")
	}

	// When --bundle is set, stage the JSONL, manifest, and data/ into a temp dir
	// and zip it into one self-contained archive. Otherwise keep loose files.
	bundling := bundlePath != "" && !dryRun && !validateOnly
	var stagingDir string
	if bundling {
		var mkErr error
		stagingDir, mkErr = os.MkdirTemp("", "mmetl-confluence-bundle-")
		if mkErr != nil {
			return fmt.Errorf("failed to create staging dir: %w", mkErr)
		}
		defer os.RemoveAll(stagingDir)
		outputFilePath = path.Join(stagingDir, "import.jsonl")
		attachmentsDir = stagingDir
	}

	// Validate output file
	if !dryRun {
		if fileInfo, err := os.Stat(outputFilePath); err != nil && !os.IsNotExist(err) {
			return err
		} else if err == nil && fileInfo.IsDir() {
			return fmt.Errorf("output file \"%s\" is a directory", outputFilePath)
		}
	}

	// Prepare attachments directory
	attachmentsFullDir := path.Join(attachmentsDir, confluenceAttachmentsDir)
	if !skipAttachments && !dryRun {
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

	// Open input file
	fileReader, err := os.Open(inputFilePath)
	if err != nil {
		return fmt.Errorf("failed to open input file: %w", err)
	}
	defer fileReader.Close()

	zipFileInfo, err := fileReader.Stat()
	if err != nil {
		return err
	}

	zipReader, err := zip.NewReader(fileReader, zipFileInfo.Size())
	if err != nil || zipReader.File == nil {
		return fmt.Errorf("failed to read ZIP file: %w", err)
	}

	// Set up logger
	logger := log.New()
	logFile, err := os.OpenFile("transform-confluence.log", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0666)
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

	// Create transformer config
	config := &confluence.TransformConfig{
		SkipAttachments: skipAttachments,
		AttachmentsDir:  attachmentsFullDir,
		MaxDepth:        maxDepth,
		DryRun:          dryRun,
	}

	// Create transformer
	confluenceTransformer := confluence.NewTransformer(team, channel, logger, config)
	confluenceTransformer.ExportFile = inputFilePath

	// Load user mapping if provided
	if userMappingPath != "" {
		userMapper, mapErr := confluence.NewUserMapper(userMappingPath, fallbackUser)
		if mapErr != nil {
			return fmt.Errorf("failed to load user mapping: %w", mapErr)
		}
		confluenceTransformer.UserMapper = userMapper
		logger.Infof("Loaded %d user mappings (by account ID), %d email mappings", userMapper.GetMappingCount(), userMapper.GetEmailMappingCount())
	}

	// Parse export
	logger.Info("Parsing Confluence export...")
	export, err := confluenceTransformer.ParseConfluenceExport(zipReader)
	if err != nil {
		return fmt.Errorf("failed to parse Confluence export: %w", err)
	}

	// Run pre-flight validation
	validator := confluence.NewValidator(team, channel)
	validator.RequireUserMapping = requireUserMapping
	validator.FailOnRestricted = failOnRestricted
	if mattermostURL != "" && mattermostToken != "" {
		validator.SetServerConfig(mattermostURL, mattermostToken)
	}

	validationResult := validator.ValidateAll(cmd.Context(), zipReader, export, confluenceTransformer.UserMapper)

	// Print validation results
	if len(validationResult.Warnings) > 0 {
		fmt.Println("Validation warnings:")
		for _, warning := range validationResult.Warnings {
			fmt.Printf("  ⚠️  %s\n", warning)
		}
	}
	if len(validationResult.Errors) > 0 {
		fmt.Println("Validation errors:")
		for _, validationErr := range validationResult.Errors {
			fmt.Printf("  ❌ %s\n", validationErr)
		}
	}

	if !validationResult.Valid {
		return fmt.Errorf("pre-flight validation failed")
	}

	if validateOnly {
		fmt.Println("✅ Pre-flight validation passed")
		fmt.Printf("  Pages: %d\n", len(export.Pages))
		fmt.Printf("  Comments: %d\n", len(export.Comments))
		fmt.Printf("  Users in export: %d\n", len(export.Users))
		return nil
	}

	// Extract attachments from ZIP before transform so paths are updated
	if !skipAttachments && !dryRun {
		logger.Info("Extracting attachments...")
		if extractErr := confluenceTransformer.ExtractAttachments(zipReader, export); extractErr != nil {
			return fmt.Errorf("attachment extraction failed: %w", extractErr)
		}
	}

	// Transform
	logger.Info("Transforming content...")
	if err = confluenceTransformer.Transform(export); err != nil {
		return fmt.Errorf("transformation failed: %w", err)
	}

	spaceCount := 0
	if confluenceTransformer.Intermediate.Space != nil {
		spaceCount = 1
	}

	// Export (unless dry run)
	if dryRun {
		logger.Info("Dry run complete - no output written")
		fmt.Printf("Dry run complete: %d spaces, %d pages, %d comments would be exported\n",
			spaceCount,
			len(confluenceTransformer.Intermediate.Pages),
			len(confluenceTransformer.Intermediate.Comments))
		return nil
	}

	logger.Info("Writing JSONL output...")
	if err = confluenceTransformer.ExportWithManifest(outputFilePath, export); err != nil {
		return fmt.Errorf("failed to write output: %w", err)
	}

	manifestPath := strings.TrimSuffix(outputFilePath, ".jsonl") + "-manifest.json"

	// When bundling, zip the staging dir (import.jsonl + import-manifest.json +
	// data/) into the single archive and report that instead of loose files.
	if bundling {
		if zipErr := zipDir(stagingDir, bundlePath); zipErr != nil {
			return fmt.Errorf("failed to write bundle: %w", zipErr)
		}
		logger.Info("Transformation succeeded!")
		fmt.Printf("Successfully transformed Confluence export to bundle %s\n", bundlePath)
		fmt.Printf("  Spaces: %d\n", spaceCount)
		fmt.Printf("  Pages: %d\n", len(confluenceTransformer.Intermediate.Pages))
		fmt.Printf("  Comments: %d\n", len(confluenceTransformer.Intermediate.Comments))
		fmt.Printf("  Attachments: %d\n", confluenceTransformer.Stats.AttachmentCount)
		if confluenceTransformer.Stats.UsersUnmapped > 0 {
			fmt.Printf("  Warning: %d users could not be mapped\n", confluenceTransformer.Stats.UsersUnmapped)
		}
		fmt.Printf("\nNext steps:\n")
		fmt.Printf("  1. mmctl import upload %s\n", bundlePath)
		fmt.Printf("  2. mmctl import process <import_id>\n")
		return nil
	}

	logger.Info("Transformation succeeded!")
	fmt.Printf("Successfully transformed Confluence export to %s\n", outputFilePath)
	fmt.Printf("  Spaces: %d\n", spaceCount)
	fmt.Printf("  Pages: %d\n", len(confluenceTransformer.Intermediate.Pages))
	fmt.Printf("  Comments: %d\n", len(confluenceTransformer.Intermediate.Comments))
	fmt.Printf("  Attachments: %d\n", confluenceTransformer.Stats.AttachmentCount)
	if confluenceTransformer.Stats.AttachmentsExtracted > 0 {
		fmt.Printf("  Attachments extracted: %d\n", confluenceTransformer.Stats.AttachmentsExtracted)
	}
	if confluenceTransformer.Stats.AttachmentsSkipped > 0 {
		fmt.Printf("  Attachments skipped: %d\n", confluenceTransformer.Stats.AttachmentsSkipped)
	}
	fmt.Printf("  Manifest: %s\n", manifestPath)
	if confluenceTransformer.Stats.UsersUnmapped > 0 {
		fmt.Printf("  Warning: %d users could not be mapped\n", confluenceTransformer.Stats.UsersUnmapped)
	}
	fmt.Printf("\nNext steps:\n")
	fmt.Printf("  Simplest: re-run with --bundle import.zip to get a ready-to-upload archive.\n")
	fmt.Printf("  Or build the archive manually (the JSONL must sit at the archive root):\n")
	fmt.Printf("    zip -r import.zip %s %s\n", outputFilePath, path.Join(attachmentsDir, confluenceAttachmentsDir))
	fmt.Printf("  Then: mmctl import upload import.zip && mmctl import process <import_id>\n")

	return nil
}

// zipDir writes every file under srcDir into a zip archive at destPath, using
// paths relative to srcDir so the archive contains import.jsonl,
// import-manifest.json, and data/ at its root.
func zipDir(srcDir, destPath string) error {
	out, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer out.Close()

	zw := zip.NewWriter(out)
	defer zw.Close()

	return filepath.Walk(srcDir, func(p string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(srcDir, p)
		if relErr != nil {
			return relErr
		}
		w, createErr := zw.Create(filepath.ToSlash(rel))
		if createErr != nil {
			return createErr
		}
		f, openErr := os.Open(p)
		if openErr != nil {
			return openErr
		}
		defer f.Close()
		_, copyErr := io.Copy(w, f)
		return copyErr
	})
}

var customLogFormatter = &log.JSONFormatter{
	CallerPrettyfier: func(frame *runtime.Frame) (function string, file string) {
		fileName := path.Base(frame.File) + ":" + strconv.Itoa(frame.Line)
		return "", fileName
	},
}
