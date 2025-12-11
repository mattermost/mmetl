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
	"github.com/spf13/cobra/doc"
)

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
}
