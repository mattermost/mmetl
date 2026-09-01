package commands

import (
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/mattermost/mmetl/services/confluence"
)

// TransformConfluenceCmd converts a Confluence Cloud XML backup into a
// Mattermost Docs import bundle.
//
// Only --list-spaces is implemented so far. The transform flags described in
// the implementation plan arrive with the exporter itself.
var TransformConfluenceCmd = &cobra.Command{
	Use:   "confluence",
	Short: "Transforms a Confluence Cloud XML export.",
	Long: "Transforms one space of a Confluence Cloud XML backup into a Mattermost Docs import bundle.\n\n" +
		"The input is the ZIP produced by Confluence's XML backup, containing entities.xml and\n" +
		"exportDescriptor.properties at its root. Each invocation exports exactly one space.",
	Example: "  transform confluence --file Confluence-export.zip --list-spaces",
	Args:    cobra.NoArgs,
	RunE:    transformConfluenceCmdF,
}

func init() {
	TransformConfluenceCmd.Flags().StringP("file", "f", "", "the Confluence Cloud XML backup ZIP to read")
	if err := TransformConfluenceCmd.MarkFlagRequired("file"); err != nil {
		panic(err)
	}
	TransformConfluenceCmd.Flags().Bool("list-spaces", false, "List the spaces in the export and exit, without transforming anything")
	TransformConfluenceCmd.Flags().Bool("debug", false, "Whether to show debug logs or not")

	TransformCmd.AddCommand(
		TransformConfluenceCmd,
	)
}

func transformConfluenceCmdF(cmd *cobra.Command, _ []string) error {
	inputFilePath, _ := cmd.Flags().GetString("file")
	listSpaces, _ := cmd.Flags().GetBool("list-spaces")

	if !listSpaces {
		return errors.New("only --list-spaces is implemented for Confluence so far; " +
			"rerun with --list-spaces to see the spaces in the export")
	}

	return listConfluenceSpaces(cmd.OutOrStdout(), inputFilePath)
}

func listConfluenceSpaces(out io.Writer, inputFilePath string) error {
	archive, err := confluence.OpenSourceArchive(inputFilePath)
	if err != nil {
		return err
	}
	defer func() { _ = archive.Close() }()

	spaces, _, err := confluence.CatalogSpaces(archive)
	if err != nil {
		return err
	}
	if len(spaces) == 0 {
		return fmt.Errorf("%s contains no spaces", inputFilePath)
	}

	return confluence.WriteSpaceTable(out, spaces)
}
