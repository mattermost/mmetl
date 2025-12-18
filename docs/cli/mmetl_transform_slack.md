---
title: "mmetl transform slack"
slug: "mmetl_transform_slack"
description: "CLI reference for mmetl transform slack"
---

## mmetl transform slack

Transforms a Slack export.

### Synopsis

Transforms a Slack export zipfile into a Mattermost export JSONL file.

```
mmetl transform slack [flags]
```

### Examples

```
  transform slack --team myteam --file my_export.zip --output mm_export.json
```

### Options

```
  -l, --allow-download                Allows downloading the attachments for the import file
  -d, --attachments-dir string        the path for the attachments directory (default "data")
      --create-team                   Creates a team import line in the output file
      --debug                         Whether to show debug logs or not
      --default-email-domain string   If this flag is provided: When a user's email address is empty, the output's email address will be generated from their username and the provided domain.
  -p, --discard-invalid-props         Skips converting posts with invalid props instead discarding the props themselves
  -f, --file string                   the Slack export file to transform
  -h, --help                          help for slack
  -o, --output string                 the output path (default "bulk-export.jsonl")
  -a, --skip-attachments              Skips copying the attachments from the import file
  -c, --skip-convert-posts            Skips converting mentions and post markup. Only for testing purposes
      --skip-empty-emails             Ignore empty email addresses from the import file. Note that this results in invalid data.
  -t, --team string                   an existing team in Mattermost to import the data into
```

### SEE ALSO

* [mmetl transform](mmetl_transform.md)	 - Transforms export files into Mattermost import files

