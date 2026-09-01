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

--organization-id identifies the Confluence site. Use the same value for every export from
the same site: it scopes every source identifier in the bundle, so changing it makes a
re-export look like a different site and import again instead of updating what is there.

```
mmetl transform confluence [flags]
```

### Examples

```
  transform confluence --file Confluence-export.zip --list-spaces
  transform confluence --file Confluence-export.zip --space ENG \
    --organization-id https://example.atlassian.net --team engineering
```

### Options

```
      --debug                    Whether to show debug logs or not
  -f, --file string              the Confluence Cloud XML backup ZIP to read
  -h, --help                     help for confluence
      --list-spaces              List the spaces in the export and exit, without transforming anything
      --organization-id string   Stable identifier for the Confluence site; use the same value for every export from one site
  -o, --output string            the output bundle path (default "<space-key>-confluence-docs.zip")
  -a, --skip-attachments         Skip attachments entirely, including their metadata
      --space string             The space to export, given as its numeric source ID, its key, or its name
  -t, --team string              an existing team in Mattermost to import the data into
      --user-mapping string      CSV mapping Confluence users to Mattermost usernames; overrides every derived proposal
      --validate-only            Run the full transform and report, without writing a bundle
```

### SEE ALSO

* [mmetl transform](mmetl_transform.md)	 - Transforms export files into Mattermost import files

