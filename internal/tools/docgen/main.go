// Code based on https://cobra.dev/docs/how-to-guides/clis-for-llms/
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/mattermost/mmetl/commands"
	"github.com/spf13/cobra"
	"github.com/spf13/cobra/doc"
)

// DocsExtraAnnotation is the annotation key for extra documentation content
// that should appear in generated markdown files but not in CLI help.
const DocsExtraAnnotation = "docs_extra"

func main() {
	out := flag.String("out", "./docs/cli", "output directory")
	front := flag.Bool("frontmatter", false, "prepend simple YAML front matter to markdown")
	flag.Parse()

	if err := os.MkdirAll(*out, 0o755); err != nil {
		log.Fatal(err)
	}

	root := commands.RootCmd
	root.DisableAutoGenTag = true // stable, reproducible files (no timestamp footer)

	if *front {
		prep := func(filename string) string {
			base := filepath.Base(filename)
			name := strings.TrimSuffix(base, filepath.Ext(base))
			title := strings.ReplaceAll(name, "_", " ")
			return fmt.Sprintf("---\ntitle: %q\nslug: %q\ndescription: \"CLI reference for %s\"\n---\n\n", title, name, title)
		}
		link := func(name string) string { return strings.ToLower(name) }
		if err := doc.GenMarkdownTreeCustom(root, *out, prep, link); err != nil {
			log.Fatal(err)
		}
	} else {
		if err := doc.GenMarkdownTree(root, *out); err != nil {
			log.Fatal(err)
		}
	}

	// Append extra documentation from annotations
	if err := appendDocsExtra(root, *out); err != nil {
		log.Fatal(err)
	}
}

// appendDocsExtra walks the command tree and appends any docs_extra annotation
// content to the corresponding generated markdown files.
func appendDocsExtra(cmd *cobra.Command, outDir string) error {
	if extra, ok := cmd.Annotations[DocsExtraAnnotation]; ok && extra != "" {
		filename := filepath.Join(outDir, cmdFilename(cmd))
		f, err := os.OpenFile(filename, os.O_APPEND|os.O_WRONLY, 0644)
		if err != nil {
			return fmt.Errorf("failed to open %s for appending: %w", filename, err)
		}
		defer f.Close()

		if _, err := f.WriteString("\n" + extra); err != nil {
			return fmt.Errorf("failed to append docs_extra to %s: %w", filename, err)
		}
	}

	for _, child := range cmd.Commands() {
		if err := appendDocsExtra(child, outDir); err != nil {
			return err
		}
	}

	return nil
}

// cmdFilename returns the markdown filename for a command (matches cobra/doc convention).
func cmdFilename(cmd *cobra.Command) string {
	basename := strings.ReplaceAll(cmd.CommandPath(), " ", "_") + ".md"
	return basename
}
