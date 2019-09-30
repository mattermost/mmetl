package commands

import (
	"archive/zip"
	"errors"
	"fmt"
	"log"
	"os"

	"github.com/mattermost/mmetl/services"
	"github.com/spf13/cobra"
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

func init() {
	TransformSlackCmd.Flags().StringP("team", "t", "", "an existing team in Mattermost to import the data into")
	TransformSlackCmd.MarkFlagRequired("team")
	TransformSlackCmd.Flags().StringP("file", "f", "", "the Slack export file to transform")
	TransformSlackCmd.MarkFlagRequired("file")
	TransformSlackCmd.Flags().StringP("output", "o", "bulk-export.jsonl", "the output path")
	TransformSlackCmd.Flags().StringP("attachments-dir", "d", "bulk-export-attachments", "the path for the attachments directory")
	TransformSlackCmd.Flags().BoolP("skip-convert-posts", "c", false, "Skips converting mentions and post markup. Only for testing purposes")
	TransformSlackCmd.Flags().BoolP("skip-attachments", "a", false, "Skips copying the attachments from the import file")

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

	// output file
	if fileInfo, err := os.Stat(outputFilePath); err != nil && !os.IsNotExist(err) {
		return err
	} else if fileInfo.IsDir() {
		return errors.New(fmt.Sprintf("Output file \"%s\" is a directory", outputFilePath))
	}

	// attachments dir
	if !skipAttachments {
		if fileInfo, err := os.Stat(attachmentsDir); os.IsNotExist(err) {
			if createErr := os.Mkdir(attachmentsDir, 0755); createErr != nil {
				return createErr
			}
		} else if err != nil {
			return err
		} else if !fileInfo.IsDir() {
			return errors.New(fmt.Sprintf("File \"%s\" is not a directory", attachmentsDir))
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

	slackExport, err := services.ParseSlackExportFile(team, zipReader, skipConvertPosts)
	if err != nil {
		return err
	}

	intermediate, err := services.Transform(slackExport, attachmentsDir, skipAttachments)
	if err != nil {
		return err
	}

	if err = services.Export(team, intermediate, outputFilePath); err != nil {
		return err
	}

	log.Println("Transformation succeeded!!")

	return nil
}
