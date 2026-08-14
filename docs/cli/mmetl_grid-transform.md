---
title: "mmetl grid-transform"
slug: "mmetl_grid-transform"
description: "CLI reference for mmetl grid-transform"
---

## mmetl grid-transform

Transforms a slack enterprise grid into multiple workspace export files.

### Synopsis

Accepts a Slack Enterprise Grid export and splits it into one zip per workspace, to be transformed separately with mmetl transform slack.

Shared channels at the archive root are moved into the originating workspace folder under teams/. The originating workspace is the Slack team ID on the first post that has a "team" field.

Workspace IDs are inferred from each folder under teams/ already in the export (native members' team_id in users.json, then posts in that folder, then any users including guests). Pass --team-map-path only to override that mapping.

--team-map-path is a path to a JSON file mapping Slack workspace IDs to folder names under teams/:

  { "T0001": "acme", "T0002": "widgets-inc" }

Keys are the Slack workspace ID as it appears in a message's "team" field (typically T...). Values must match an existing folder under teams/ in the export; they are not Mattermost team names. Use --team on transform slack for the Mattermost team.

```
mmetl grid-transform [flags]
```

### Examples

```
  grid-transform --file slackexport.zip
  grid-transform --file slackexport.zip --team-map-path teams.json
```

### Options

```
      --debug                  Whether to show debug logs or not
  -f, --file string            the Slack export file to clean
  -h, --help                   help for grid-transform
  -t, --team-map-path string   path to a JSON file mapping Slack workspace IDs to folder names under teams/; inferred from the export when omitted
```

### SEE ALSO

* [mmetl](mmetl.md)	 - ETL tool to transform the export files from different providers to be compatible with Mattermost.

