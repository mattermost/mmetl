# Confluence → Mattermost Docs JSONL contract (v2)

`mmetl transform confluence` emits a JSONL bundle for the Mattermost Docs
(Spaces/Pages) importer. This document describes the **v2** contract. There is
no shipped consumer of an earlier version, so no backward-compatibility shim
exists.

## Bundle layout

With `--bundle <out.zip>` the tool produces one self-contained archive:

```text
import.jsonl
import-manifest.json
data/<page-id>/<attachment>
```

Without `--bundle`, the same files are written loose (`--output` for the JSONL,
`--attachments-dir` for `data/`, and `<output>-manifest.json` alongside).

## Source namespace

Entity source IDs (numeric page IDs, space keys) are **bare** and interpreted
within the bundle's source namespace. The namespace is carried **once**, on the
`version` line, and mirrored in the manifest (`source.organization_id`,
`source.space_key`). Importers must scope every source-ID lookup to the job —
never globally — so IDs cannot collide across Confluence instances.

One bundle = one Space. A CSV export always covers a single space; a multi-space
export is rejected at transform time.

## Line types

Lines are newline-delimited JSON objects, each with a `type`. They appear in
this order: `version`, `space`, `page`×N (parents before children),
`page_comment`×N (parents before children), `resolve_space_placeholders`.

### `version`

```json
{"type":"version","version":2,"source":{"organization_id":"org-123","space_key":"DOCS"}}
```

`source` is omitted only when neither an organization id nor a space key is known.

### `space`

```json
{"type":"space","space":{"team":"myteam","title":"Docs","description":"Migrated from Confluence space: DOCS","props":{"import_source_id":"DOCS"}}}
```

The Space's backing channel is **not** in the contract — it is resolved at import
time from the import request.

### `page`

```json
{"type":"page","page":{
  "team":"myteam",
  "space_import_source_id":"DOCS",
  "user":"jdoe",
  "title":"Home",
  "content":"<TipTap JSON>",
  "parent_import_source_id":"100",
  "create_at":1704106800000,
  "update_at":1704193200000,
  "props":{"import_source_id":"101","import_source":"confluence","confluence_author_account_id":"aaid1"},
  "attachments":[{"path":"101/diagram.png","props":{"import_source_id":"300"}}]
}}
```

- `content` is validated as JSON before writing; a converter that yields invalid
  JSON falls back to a raw-HTML code block (counted as a warning).
- `update_at` is omitted when the source has no modification date.
- `parent_import_source_id`, `attachments`, and `update_at` are omitted when empty.

### `page_comment`

```json
{"type":"page_comment","page_comment":{
  "page_import_source_id":"101",
  "parent_comment_import_source_id":"200",
  "user":"jdoe",
  "content":"markdown text",
  "create_at":1704625200000,
  "update_at":1704625200000,
  "is_resolved":true,
  "props":{"import_source_id":"201","import_source":"confluence","inline_anchor":{"anchor_id":"uuid","text":"selected"}}
}}
```

`is_resolved` is omitted when false.

### `resolve_space_placeholders`

```json
{"type":"resolve_space_placeholders","resolve_space_placeholders":{"team":"myteam","space_import_source_id":"DOCS"}}
```

Emitted last; triggers space-scoped resolution of cross-page link placeholders
after all pages exist.

## Limits (Docs)

The producer warns (does not drop) when a page exceeds a Docs storage cap:

| Field       | Cap    |
|-------------|--------|
| Body        | 2 MiB  |
| SearchText  | 2 MiB  |
| Props       | 64 KiB |

## Restrictions

Page view restrictions are **detected, not imported**. Restricted pages are
listed in a loud warning and in the manifest's `restricted_pages`. Use
`--fail-on-restricted` to make an unresolved restriction a hard error.
