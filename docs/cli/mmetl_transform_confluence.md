---
title: "mmetl transform confluence"
slug: "mmetl_transform_confluence"
description: "CLI reference for mmetl transform confluence"
---

## mmetl transform confluence

Transforms a Confluence Cloud XML export.

### Synopsis

Transforms one space of a Confluence Cloud XML backup into a Mattermost Docs import bundle.

The input is the ZIP produced by Confluence's XML backup, containing entities.xml and
exportDescriptor.properties at its root. Each invocation exports exactly one space.

```
mmetl transform confluence [flags]
```

### Examples

```
  transform confluence --file Confluence-export.zip --list-spaces
```

### Options

```
      --debug         Whether to show debug logs or not
  -f, --file string   the Confluence Cloud XML backup ZIP to read
  -h, --help          help for confluence
      --list-spaces   List the spaces in the export and exit, without transforming anything
```

### SEE ALSO

* [mmetl transform](mmetl_transform.md)	 - Transforms export files into Mattermost import files

