package commands

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"runtime"
	"strconv"

	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	"github.com/mattermost/mmetl/services/slack"
	"github.com/n-marshall/go-cp"
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
	TransformSlackCmd.Flags().String("auth-service", "", "The authentication service to use for user accounts. If not provided, it defaults to password-based authentication.")
	TransformSlackCmd.Flags().Uint("max-chunk-size", 0, "Max count of posts in chunks. 0 if don't need to split exports. Default: 0")
	TransformSlackCmd.Flags().BoolP("allow-download", "l", false, "Allows downloading the attachments for the import file")
	TransformSlackCmd.Flags().BoolP("discard-invalid-props", "p", false, "Skips converting posts with invalid props instead discarding the props themselves")
	TransformSlackCmd.Flags().Bool("debug", false, "Whether to show debug logs or not")
	TransformSlackCmd.Flags().String("result-file", "conversion_result.json", "File with conversion result information")
	TransformSlackCmd.Flags().StringP("build-archive", "z", "", "Define filename prefix for build archive for mmctl import")

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
	authService, _ := cmd.Flags().GetString("auth-service")
	maxChunkSize, _ := cmd.Flags().GetUint("max-chunk-size")
	resultFilePath, _ := cmd.Flags().GetString("result-file")
	buildArchive, _ := cmd.Flags().GetString("build-archive")

	if !(authService == "" || authService == "gitlab" || authService == "ldap" ||
		authService == "saml" || authService == "google" || authService == "office365") {
		return fmt.Errorf("Auth serivece must be one of gitlab, ldap, saml, google, office365")
	}

	// output file
	if fileInfo, err := os.Stat(outputFilePath); err != nil && !os.IsNotExist(err) {
		return err
	} else if err == nil && fileInfo.IsDir() {
		return fmt.Errorf("Output file \"%s\" is a directory", outputFilePath)
	}

	// attachments dir
	if !skipAttachments {
		if err := createAttachmentDir(attachmentsDir); err != nil {
			return err
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

	resultFile, err := os.OpenFile(resultFilePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0666)
	if err != nil {
		return err
	}
	defer resultFile.Close()

	if debug {
		logger.Level = log.DebugLevel
		logger.Info("Debug mode enabled")
	}
	slackTransformer := slack.NewTransformer(team, logger)

	slackExport, err := slackTransformer.ParseSlackExportFile(zipReader, skipConvertPosts)
	if err != nil {
		return err
	}

	err = slackTransformer.Transform(slackExport, attachmentsDir, skipAttachments, discardInvalidProps,
		allowDownload, skipEmptyEmails, defaultEmailDomain, authService)
	if err != nil {
		return err
	}

	err, chunks, channels := slackTransformer.Export(outputFilePath, maxChunkSize)
	if err != nil {
		return err
	}

	if !skipAttachments {
		err, chunks = transformAttachments(chunks, attachmentsDir, buildArchive)
		if err != nil {
			return err
		}
	}

	if buildArchive != "" {
		err, chunks = zipChunks(chunks, buildArchive)
		if err != nil {
			return nil
		}
	}

	result, err := json.Marshal(struct {
		Chunks   []slack.ChunkInfo `json:"chunks"`
		Channels []string          `json:"channels"`
	}{
		Chunks:   chunks,
		Channels: channels,
	})
	if err != nil {
		return err
	}

	if _, err := resultFile.Write(result); err != nil {
		return err
	}

	slackTransformer.Logger.Infof("Transformation succeeded! Result written into '%s'", resultFilePath)

	return nil
}

func createAttachmentDir(attachmentsDir string) error {
	attachmentsFullDir := path.Join(attachmentsDir, attachmentsInternal)
	if fileInfo, err := os.Stat(attachmentsFullDir); os.IsNotExist(err) {
		if createErr := os.MkdirAll(attachmentsFullDir, 0755); createErr != nil {
			return createErr
		}
	} else if err != nil {
		return err
	} else if !fileInfo.IsDir() {
		return fmt.Errorf("File \"%s\" is not a directory", attachmentsDir)
	}
	return nil
}

func transformAttachments(chunks []slack.ChunkInfo, attachmentsDir string, buildArchive string) (error, []slack.ChunkInfo) {
	isNeedToChunkAttachments := (len(chunks) > 1 && buildArchive == "")

	for _, chunk := range chunks {
		if len(chunk.Attachments) > 0 {
			chunkAttachmentsDir := attachmentsDir
			if isNeedToChunkAttachments {
				chunkAttachmentsDir = fmt.Sprintf("%s.%d", attachmentsDir, chunk.Id)
				if err := createAttachmentDir(chunkAttachmentsDir); err != nil {
					return err, chunks
				}
			}
			for idx, file := range chunk.Attachments {
				newPath := path.Join(chunkAttachmentsDir, file)
				if isNeedToChunkAttachments {
					if err := cp.CopyFile(path.Join(attachmentsDir, file), newPath); err != nil {
						return err, chunks
					}
				}
				chunks[chunk.Id].Attachments[idx] = newPath
			}
		}
	}

	return nil, chunks
}

func zipChunks(chunks []slack.ChunkInfo, buildArchive string) (error, []slack.ChunkInfo) {
	for _, chunk := range chunks {
		chunks[chunk.Id].Zip = fmt.Sprintf("%s.%d.zip", buildArchive, chunk.Id)
		zipFile, err := os.Create(chunks[chunk.Id].Zip)
		if err != nil {
			return err, chunks
		}
		defer zipFile.Close()

		zipW := zip.NewWriter(zipFile)
		defer zipW.Close()

		for _, path := range append(chunk.Attachments, chunk.File) {
			src, err := os.Open(path)
			if err != nil {
				return err, chunks
			}
			defer src.Close()

			dst, err := zipW.Create(path)
			if err != nil {
				return err, chunks
			}

			_, err = io.Copy(dst, src)
			if err != nil {
				return err, chunks
			}
		}
	}
	return nil, chunks
}

var customLogFormatter = &log.JSONFormatter{
	CallerPrettyfier: func(frame *runtime.Frame) (function string, file string) {
		fileName := path.Base(frame.File) + ":" + strconv.Itoa(frame.Line)
		return "", fileName
	},
}
