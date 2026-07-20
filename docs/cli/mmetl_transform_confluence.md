---
title: "mmetl transform confluence"
slug: "mmetl_transform_confluence"
description: "CLI reference for mmetl transform confluence"
---

## mmetl transform confluence

Transforms a Confluence export.

### Synopsis

Transforms a Confluence Cloud CSV space export into a Mattermost Docs (Spaces/Pages) import bundle.

```
mmetl transform confluence [flags]
```

### Examples

```
  transform confluence --team myteam --file confluence-export.zip --bundle import.zip
```

### Options

```
  -d, --attachments-dir string    the path for extracted attachments (default "data")
      --bundle string             write a single self-contained import archive (zip) at this path instead of loose files
      --debug                     enable debug logging
      --dry-run                   validate without writing output files
      --fail-on-restricted        fail if any page has a View restriction (not preserved on import)
      --fallback-user string      Mattermost username to use for unmapped Confluence users
  -f, --file string               the Confluence export file (ZIP) to transform
  -h, --help                      help for confluence
      --mattermost-token string   Mattermost auth token for validation (optional)
      --mattermost-url string     Mattermost server URL for validation (optional)
      --max-depth int             maximum page hierarchy depth (deeper pages are flattened) (default 10)
  -o, --output string             the output JSONL file path (default "import.jsonl")
      --require-user-mapping      fail if any Confluence author is not mapped to a Mattermost user
  -a, --skip-attachments          skip extracting attachments
  -t, --team string               advisory destination team recorded in the bundle; the Docs import request selects the actual target team
  -u, --user-mapping string       CSV file mapping Confluence users to Mattermost users
      --validate-only             only run pre-flight validation, do not transform
```

### SEE ALSO

* [mmetl transform](mmetl_transform.md)	 - Transforms export files into Mattermost import files

