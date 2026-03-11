---
title: "mmetl check rocketchat"
slug: "mmetl_check_rocketchat"
description: "CLI reference for mmetl check rocketchat"
---

## mmetl check rocketchat

Checks the integrity of a Rocket.Chat mongodump export.

```
mmetl check rocketchat [flags]
```

### Options

```
      --debug                         Whether to show debug logs or not
      --default-email-domain string   If this flag is provided: When a user's email address is empty, the output's email address will be generated from their username and the provided domain.
  -d, --dump-dir string               path to the mongodump output directory
  -h, --help                          help for rocketchat
      --skip-empty-emails             Ignore empty email addresses from the import file. Note that this results in invalid data.
```

### SEE ALSO

* [mmetl check](mmetl_check.md)	 - Checks the integrity of export files.

