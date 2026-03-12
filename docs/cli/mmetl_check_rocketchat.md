---
title: "mmetl check rocketchat"
slug: "mmetl_check_rocketchat"
description: "CLI reference for mmetl check rocketchat"
---

## mmetl check rocketchat

Checks the integrity of a Rocket.Chat mongodump export.

### Synopsis

Checks the integrity of a Rocket.Chat mongodump export directory.

Before running this command, export your Rocket.Chat MongoDB database using mongodump
(https://www.mongodb.com/docs/database-tools/mongodump/):

  mongodump --uri="mongodb://localhost:3001/meteor" --out=/tmp/rc-dump

Then pass the database subdirectory to --dump-dir (e.g. /tmp/rc-dump/meteor).

```
mmetl check rocketchat [flags]
```

### Examples

```
  check rocketchat --dump-dir /tmp/rc-dump/meteor
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

