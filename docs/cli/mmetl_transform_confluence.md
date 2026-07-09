---
title: "mmetl transform confluence"
slug: "mmetl_transform_confluence"
description: "CLI reference for mmetl transform confluence"
---

## mmetl transform confluence

Transforms a Confluence export.

### Synopsis

Transforms a Confluence Cloud CSV space export into a Mattermost Wiki/Pages JSONL file.

```
mmetl transform confluence [flags]
```

### Examples

```
  transform confluence --team myteam --channel docs --file confluence-export.zip --output wiki-import.jsonl
```

### Options

```
  -d, --attachments-dir string    the path for extracted attachments (default "data")
  -c, --channel string            the target channel in Mattermost for the wikis
      --create-channel            include channel creation in JSONL output (for new channels)
      --debug                     enable debug logging
      --dry-run                   validate without writing output files
      --fallback-user string      Mattermost username to use for unmapped Confluence users
  -f, --file string               the Confluence export file (ZIP) to transform
  -h, --help                      help for confluence
      --mattermost-token string   Mattermost auth token for validation (optional)
      --mattermost-url string     Mattermost server URL for validation (optional)
      --max-depth int             maximum page hierarchy depth (deeper pages are flattened) (default 10)
  -o, --output string             the output JSONL file path (default "wiki-import.jsonl")
  -a, --skip-attachments          skip extracting attachments
  -t, --team string               the target team in Mattermost
  -u, --user-mapping string       CSV file mapping Confluence users to Mattermost users
      --validate-only             only run pre-flight validation, do not transform
```

### SEE ALSO

* [mmetl transform](mmetl_transform.md)	 - Transforms export files into Mattermost import files

