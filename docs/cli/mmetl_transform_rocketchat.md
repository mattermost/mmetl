---
title: "mmetl transform rocketchat"
slug: "mmetl_transform_rocketchat"
description: "CLI reference for mmetl transform rocketchat"
---

## mmetl transform rocketchat

Transforms a RocketChat mongodump export.

### Synopsis

Transforms a RocketChat mongodump directory into a Mattermost export JSONL file.

Before running this command, export your RocketChat MongoDB database using mongodump
(https://www.mongodb.com/docs/database-tools/mongodump/):

  mongodump --uri="mongodb://localhost:3001/meteor" --out=/tmp/rc-dump

Then pass the database subdirectory to --dump-dir (e.g. /tmp/rc-dump/meteor).

```
mmetl transform rocketchat [flags]
```

### Examples

```
  transform rocketchat --team myteam --dump-dir /tmp/rc-dump/meteor --output mm_export.jsonl
```

### Options

```
      --attachments-dir string        the path for the attachments directory (default "data")
      --bot-owner string              Username of the Mattermost user who will own all imported bots. Required if the RocketChat export contains bot users.
      --debug                         Whether to show debug logs or not
      --default-email-domain string   If this flag is provided: When a user's email address is empty, the output's email address will be generated from their username and the provided domain.
  -d, --dump-dir string               path to the mongodump output directory (containing .bson files)
      --guest-handling string         How to migrate RocketChat guest users (users whose roles include "guest"). One of:
                                        "guest" - migrate them as Mattermost guests (system_guest/team_guest/channel_guest). Highest fidelity, but the destination server must have Guest Accounts licensed (Professional/Enterprise) and enabled (GuestAccountsSettings.Enable); otherwise the accounts won't behave correctly.
                                        "user"  - migrate them as regular Mattermost users. Works everywhere, but grants guests full user permissions.
                                        "skip"  - drop guest users entirely, along with their memberships and authored posts. (default "guest")
  -h, --help                          help for rocketchat
  -o, --output string                 the output path (default "bulk-export.jsonl")
  -a, --skip-attachments              Skips extracting file attachments
      --skip-empty-emails             Ignore empty email addresses from the import file. Note that this results in invalid data.
  -t, --team string                   an existing team in Mattermost to import the data into
      --uploads-dir string            path to RocketChat FileSystem uploads directory (if not using GridFS)
```

### SEE ALSO

* [mmetl transform](mmetl_transform.md)	 - Transforms export files into Mattermost import files

