---
title: "mmetl grid-transform"
slug: "mmetl_grid-transform"
description: "CLI reference for mmetl grid-transform"
---

## mmetl grid-transform

Transforms a slack enterprise grid into multiple workspace export files.

### Synopsis

Accepts a Slack Enterprise Grid export file and transforms it into multiple workspace export files to be imported separately into Mattermost.

```
mmetl grid-transform [flags]
```

### Options

```
      --debug            Whether to show debug logs or not
  -f, --file string      the Slack export file to clean
  -h, --help             help for grid-transform
  -t, --teamMap string   The team mapping file to use
```

### SEE ALSO

* [mmetl](mmetl.md)	 - ETL tool to transform the export files from different providers to be compatible with Mattermost.

