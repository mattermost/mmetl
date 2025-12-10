---
title: "mmetl check slack"
slug: "mmetl_check_slack"
description: "CLI reference for mmetl check slack"
---

## mmetl check slack

Checks the integrity of a Slack export.

```
mmetl check slack [flags]
```

### Options

```
      --debug                         Whether to show debug logs or not (default true)
      --default-email-domain string   If this flag is provided: When a user's email address is empty, the output's email address will be generated from their username and the provided domain.
  -f, --file string                   the Slack export file to transform
  -h, --help                          help for slack
      --skip-empty-emails             Ignore empty email addresses from the import file. Note that this results in invalid data.
```

### SEE ALSO

* [mmetl check](mmetl_check.md)	 - Checks the integrity of export files.

