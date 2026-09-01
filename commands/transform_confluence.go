package commands

import (
	"errors"
	"fmt"
	"io"
	"strings"

	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	"github.com/mattermost/mmetl/services/confluence"
)

// listSpacesExclusiveFlags may not be combined with --list-spaces. Listing is a
// read-only inspection, and accepting transform flags alongside it would let an
// operator believe an export ran when nothing was written.
var listSpacesExclusiveFlags = []string{
	"space", "organization-id", "team", "output", "user-mapping", "skip-attachments", "validate-only",
}

// TransformConfluenceCmd converts one space of a Confluence Cloud XML backup
// into a Mattermost Docs import bundle.
var TransformConfluenceCmd = &cobra.Command{
	Use:   "confluence",
	Short: "Transforms a Confluence Cloud XML export.",
	Long: "Transforms one space of a Confluence Cloud XML backup into a Mattermost Docs import bundle.\n\n" +
		"The input is the ZIP produced by Confluence's XML backup, containing entities.xml and\n" +
		"exportDescriptor.properties at its root. Each invocation exports exactly one space.\n\n" +
		"--organization-id identifies the Confluence site. Use the same value for every export from\n" +
		"the same site: it scopes every source identifier in the bundle, so changing it makes a\n" +
		"re-export look like a different site and import again instead of updating what is there.",
	Example: "  transform confluence --file Confluence-export.zip --list-spaces\n" +
		"  transform confluence --file Confluence-export.zip --space ENG \\\n" +
		"    --organization-id https://example.atlassian.net --team engineering",
	Args: cobra.NoArgs,
	RunE: transformConfluenceCmdF,
}

func init() {
	flags := TransformConfluenceCmd.Flags()

	flags.StringP("file", "f", "", "the Confluence Cloud XML backup ZIP to read")
	if err := TransformConfluenceCmd.MarkFlagRequired("file"); err != nil {
		panic(err)
	}
	flags.Bool("list-spaces", false, "List the spaces in the export and exit, without transforming anything")
	flags.String("space", "", "The space to export, given as its numeric source ID, its key, or its name")
	flags.String("organization-id", "", "Stable identifier for the Confluence site; use the same value for every export from one site")
	flags.StringP("team", "t", "", "an existing team in Mattermost to import the data into")
	flags.StringP("output", "o", "", "the output bundle path (default \"<space-key>-confluence-docs.zip\")")
	flags.String("user-mapping", "", "CSV mapping Confluence users to Mattermost usernames; overrides every derived proposal")
	flags.BoolP("skip-attachments", "a", false, "Skip attachments entirely, including their metadata")
	flags.Bool("validate-only", false, "Run the full transform and report, without writing a bundle")
	flags.Bool("debug", false, "Whether to show debug logs or not")

	TransformCmd.AddCommand(
		TransformConfluenceCmd,
	)
}

func transformConfluenceCmdF(cmd *cobra.Command, _ []string) error {
	inputFilePath, _ := cmd.Flags().GetString("file")

	if listSpaces, _ := cmd.Flags().GetBool("list-spaces"); listSpaces {
		if err := checkListSpacesFlags(cmd); err != nil {
			return err
		}
		return listConfluenceSpaces(cmd.OutOrStdout(), inputFilePath)
	}

	debug, _ := cmd.Flags().GetBool("debug")
	logger := log.New()
	logger.SetOutput(cmd.ErrOrStderr())
	logger.SetFormatter(&log.TextFormatter{ForceColors: false, DisableTimestamp: true})
	if debug {
		logger.Level = log.DebugLevel
	}

	space, _ := cmd.Flags().GetString("space")
	organizationID, _ := cmd.Flags().GetString("organization-id")
	team, _ := cmd.Flags().GetString("team")
	output, _ := cmd.Flags().GetString("output")
	userMapping, _ := cmd.Flags().GetString("user-mapping")
	skipAttachments, _ := cmd.Flags().GetBool("skip-attachments")
	validateOnly, _ := cmd.Flags().GetBool("validate-only")

	result, err := confluence.Transform(confluence.TransformOptions{
		SourcePath: inputFilePath,
		// Mattermost team names are lowercase, matching the other transforms.
		Team:             strings.ToLower(strings.TrimSpace(team)),
		SpaceSelector:    space,
		OrganizationID:   organizationID,
		OutputPath:       output,
		UserMappingPath:  userMapping,
		SkipAttachments:  skipAttachments,
		ValidateOnly:     validateOnly,
		GeneratorVersion: getVersion(),
		Logger:           logger,
	})
	if err != nil {
		return describeConfluenceError(cmd.ErrOrStderr(), err)
	}

	if validateOnly {
		fmt.Fprintf(cmd.OutOrStdout(), "Validation succeeded for space %s; no bundle was written.\n", result.Space.SpaceKey)
		return nil
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Wrote %s\n", result.OutputPath)
	return nil
}

// checkListSpacesFlags rejects the transform flags, which listing ignores.
func checkListSpacesFlags(cmd *cobra.Command) error {
	var set []string
	for _, name := range listSpacesExclusiveFlags {
		if cmd.Flags().Changed(name) {
			set = append(set, "--"+name)
		}
	}
	if len(set) > 0 {
		return fmt.Errorf("--list-spaces cannot be combined with %s", strings.Join(set, ", "))
	}
	return nil
}

// describeConfluenceError prints the valid-space table when the failure was an
// unusable --space, so the operator can fix it without a second command.
func describeConfluenceError(out io.Writer, err error) error {
	var selectionErr *confluence.SpaceSelectionError
	if errors.As(err, &selectionErr) && len(selectionErr.Spaces) > 0 {
		fmt.Fprintln(out, "Available spaces:")
		if writeErr := confluence.WriteSpaceTable(out, selectionErr.Spaces); writeErr != nil {
			return writeErr
		}
	}
	return err
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
