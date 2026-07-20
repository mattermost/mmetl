# Confluence CSV → Docs contract alignment (mmetl)

> **Status:** Implemented by `0430bbd`, `c62beaf`, and the channel-surface
> cleanup that followed. The v2 contract, single-Space bundle, attachment
> metadata, restriction detection, validation, and tests are in place. The
> deprecated channel surface is now fully removed: the `--channel` flag,
> `Transformer.ChannelName`, `Validator.ChannelName`, `ManifestTarget.Channel`,
> the channel constructor arguments, and the optional server-side channel
> lookup/membership validation are all gone. The manifest serializes
> `target.team` with no `channel` key. The **only** remaining producer blocker
> is verifying the CSV attachment byte layout against a real attachment-bearing
> export. The target team carried by the bundle is advisory metadata; the Docs
> import request is authoritative.

## Summary

Follow-up to the CSV export work ([confluence-csv-export-support.md](confluence-csv-export-support.md))
and the cross-repo reconciliation in
[confluence-jsonl-docs-plugin-integration.md](confluence-jsonl-docs-plugin-integration.md).
This plan covers the **mmetl-side** changes only — bringing the emitted bundle in
line with the Mattermost Docs (Spaces/Pages) domain and closing the fidelity gaps
the reconciliation surfaced. It does not touch `mattermost-plugin-docs` or
`mattermost` core.

Scope, in priority order:

1. **Commit to single-space** — remove the dead multi-space scaffolding.
2. **Proper naming** — rename the `wiki`/`wiki-poc` contract to the Docs `space`
   vocabulary and version it.
3. **Attachments** — parse attachment metadata from the CSV so extraction and
   placeholder normalization actually work.
4. **Bundle, validation, restrictions, tests** — the remaining producer gaps.

## Goals / Non-Goals

**Goals**
- One bundle = one Space; simple, correct, and matches the CSV export format.
- A v2 JSONL contract named in Docs terms (`space`, `page`, `page_comment`).
- Attachment metadata populated and `CONF_ATTACHMENT` tokens resolved.
- A single self-contained `--bundle` output; validation aligned to Docs limits;
  restricted pages detected and surfaced.

**Non-Goals**
- **Multi-space in one bundle** — explicitly deferred. We may revisit if a
  site/global CSV export format appears; today each Confluence space exports
  separately, so N spaces = N bundles. (This is a reversible decision; §1 keeps a
  clean single-space seam rather than deleting the concept of a Space.)
- No import-side work (parsing, jobs, ACL) — that lives in the Docs plugin.
- No access-policy / backing-channel ownership — the import request is
  authoritative for that; mmetl stops dictating it (§3) but does not model it.
- Not resolving page **restrictions** into an ACL (still deferred) — only
  detecting and warning (§7).

## Current State (grounded references)

- Emits v2 `version`, `space`, `page`, `page_comment`, and
  `resolve_space_placeholders` lines; no channel line or entity channel field
  ([export.go](../services/confluence/export.go)).
- Enforces one bundle = one Space on the parse and transform paths.
- Parses attachment metadata and normalizes `CONF_ATTACHMENT` placeholders to
  source-ID-based `CONF_FILE` placeholders. Attachment byte extraction still
  assumes the unverified legacy path layout.
- **Done:** the `--channel` flag and all Confluence channel plumbing are
  removed. There is no `Transformer.ChannelName`, `Validator.ChannelName`,
  `ManifestTarget.Channel`, or channel constructor argument, and the optional
  server-side channel lookup/membership validation is gone. The manifest
  serializes `target.team` with no `channel` key.
- `--team` and emitted `team` values remain producer/operator intent and are
  advisory to the importer. The import request chooses the actual destination.
- Preserves identity through `props.confluence_author_account_id` and the
  manifest `users` list.
- Applies Docs Body/Props warnings, validates TipTap JSON, emits `update_at`, and
  detects View-restricted pages with warning/fail behavior.

## Implemented Approach and Remaining Follow-ups

### 1. Commit to single-space (remove multi-space scaffolding)

- Collapse `Intermediate.Wikis []` + `Intermediate.Wiki` to a **single**
  `Intermediate.Space` (see §2 for the rename). `setupWikisFromSpaces`
  ([intermediate.go:189](../services/confluence/intermediate.go)) becomes
  `setupSpace`: take the one space from `export.Spaces`/`export.SpaceKey`, build
  one `IntermediateSpace`.
- `ExportWikis` loop → `ExportSpace` (writes exactly one `space` line).
- **Guardrail**: if `len(export.Spaces) > 1` (cannot happen from CSV today),
  return a clear error — "multi-space exports are not supported; import one space
  per bundle" — so the assumption is enforced, not silently mis-handled.
- Keep `SpaceInfo`/`export.Spaces` as the parse-side shape (cheap, and the natural
  seam if multi-space returns), but the transform/export side is single-space.

### 2. Proper naming — v2 Docs contract

Rename the wiki-poc vocabulary to Docs terms and bump the version. New line types
and fields:

| v1 (now) | v2 (target) |
|----------|-------------|
| `wiki` line / `WikiImportData` | `space` line / `SpaceImportData` |
| `wiki_import_source_id` | `space_import_source_id` |
| `resolve_wiki_placeholders` / `ResolveWikiPlaceholdersImportData` | `resolve_space_placeholders` / `ResolveSpacePlaceholdersImportData` |
| `page`, `page_comment` | unchanged |
| `version: 1` | `version: 2` |

- Rename the Go identifiers to match (`IntermediateWiki`→`IntermediateSpace`,
  `ExportWiki(s)`→`ExportSpace`, etc.) and update the `transform.go` summary
  prints (`Wikis:` → `Spaces:`).
- **Version + source namespace**: emit `version: 2` and add a bundle-level
  **source namespace** so numeric page IDs / space keys can't collide across
  Confluence instances. Carry it once (not per line): add
  `source.organization_id` and `source.space_key` to the manifest (derive
  `organization_id` from `exportDescriptor.properties` `organizationId`, already
  present), and include the same in the `version` line's payload. Entity source
  IDs stay bare and are interpreted within the bundle namespace; the importer must
  scope all source-ID lookups to the job (never global).
- This is a v2-only contract; there is no shipped consumer of v1, so no
  compatibility shim is required. Document the schema in `docs/`.

### 3. Stop dictating the backing channel; make team advisory — **done**

The Docs plugin owns the Space's opaque `ChannelTypeSpace` backing channel;
mmetl must not decide or attempt to discover it. The import request is
authoritative for the actual target team and access policy.

Completed:

- **Done:** dropped the `channel` line and `ExportChannel`/`AutoCreateChannel`/`--create-channel`.
- **Done:** removed `Channel` from the `space`/`page` contract (the backing channel
  is created and resolved by the Docs importer).
- **Done:** removed the `target.channel` manifest field (the manifest serializes
  `target.team` with no `channel` key) and the optional server-side channel
  lookup/membership validation. A Space backing channel is hidden from generic
  channel lookup, and there is no channel field in v2.
- **Done:** removed the `--channel` flag entirely, along with
  `Transformer.ChannelName`, `Validator.ChannelName`, and the `channelName`
  arguments to `NewTransformer`/`NewValidator`/`NewManifest`. mmetl no longer
  creates, identifies, discovers, or validates the Space backing channel.
- **Done:** `--team` remains required and its emitted `team` values are
  **advisory operator metadata**; its CLI description now says so explicitly.
  Optional server validation may still confirm the advisory team exists, but it
  never inspects any regular or Space channel. The importer records the team for
  audit and may warn when it differs from the requested team, but must never
  route an import from the bundle value.

### 4. Attachments

Root cause: the CSV parser never populates `export.Attachments`.

- In `csv_parser.go` `loadContent`, handle `contenttype=ATTACHMENT` rows: build
  `ConfluenceAttachment{ID: contentid, PageID: pageid, FileName: title,
  CreatedBy: resolveActor(creator), CreatedAt: creationdate}` and append to
  `export.Attachments[pageid]`. Pull `MediaType`/`FileSize` from
  `contentproperties` if present (e.g. `media-type`, `file-size`) — best-effort,
  since the sample has none.
- With `export.Attachments` populated, `filenameToID` is non-empty and
  `ConvertAttachmentPlaceholdersToFileIDs` rewrites `CONF_ATTACHMENT` → `CONF_FILE`
  ([intermediate.go:247](../services/confluence/intermediate.go)); pages then get
  their `attachments` arrays and `ExtractAttachments` can place files.
- **Verify the on-disk file layout** (blocking, needs data): the current regex
  `attachments/{pageID}/{attachmentID}/{version}` ([attachments.go:17](../services/confluence/attachments.go))
  is the XML-era layout and is **unverified for CSV**. Acquire a real CSV export
  that contains attachments, confirm the path convention, and adjust the regex
  (make it a small set of candidate patterns if needed). Until verified, ship the
  metadata parsing + the existing warning and gate closure on a real fixture.
- Keep emitting both `CONF_ATTACHMENT` (unresolved) and `CONF_FILE` (normalized)
  handling downstream until attachments are fully verified.

### 5. Single self-contained bundle

- Add `--bundle <out.zip>` producing one archive:

  ```text
  import.jsonl
  import-manifest.json
  data/<page-id>/<attachment>
  ```

- When `--bundle` is set, write into a temp staging dir and zip; otherwise keep
  the current loose-file behavior. Fix the misleading "Next steps" recipe
  ([transform.go:397](../commands/transform.go)) so it never omits the JSONL.

### 6. Validation aligned to Docs

- Lower the body warning to the Docs cap: warn/refuse Body and SearchText above
  **2 MiB** (`PageBodyMaxBytes`/`PageSearchTextMaxBytes`), and page Props above
  **64 KiB** (`PagePropsMaxBytes`).
- **Emit `update_at`**: add `update_at` to `PageImportData`/`PageCommentImportData`
  and populate from `IntermediatePage.UpdateAt` (already captured).
- **Validate TipTap** with `json.Valid` before writing each page's `content`;
  on failure keep the existing code-block fallback but count it as a warning.
- Keep leaning on the manifest `users` list + `--fallback-user` for user
  validation; add a strict `--require-user-mapping` that fails if any author is
  unmapped (opt-in).

### 7. Detect and warn on restricted pages

Restrictions are still not imported, but silently importing a view-restricted
page widens access. Add **detection** (not enforcement):

- Parse `content_perm_set.csv` (+`content_perm.csv`) enough to identify pages with
  a `View` restriction (do not resolve principals).
- Emit a loud warning listing restricted page IDs/titles and add a
  `restricted_pages` array to the manifest.
- Optional `--fail-on-restricted` for operators who want a hard stop until the ACL
  story exists.

### 8. Tests

- Extend `csv_parser_test.go` / add `export_test.go`:
  - v2 schema: assert exact emitted keys per line (`space`,
    `space_import_source_id`, `resolve_space_placeholders`, `version: 2`, source
    namespace); assert no channel line, no entity `channel`, and no manifest
    `target.channel`.
  - single-space guardrail: a synthetic two-space export errors.
  - attachments: `ATTACHMENT` rows populate `export.Attachments`; a
    `CONF_ATTACHMENT` body token becomes `CONF_FILE`; page `attachments` array
    present.
  - `--bundle`: archive contains `import.jsonl` + `import-manifest.json` + `data/`.
  - validation: >2 MiB body warns; invalid TipTap falls back + warns; `update_at`
    emitted.
  - restrictions: a page with a `View` `content_perm_set` produces a warning +
    manifest entry.
- Update the end-to-end run against the real sample; regenerate `docs/`.

## Files to Change

| File | Change |
|------|--------|
| `services/confluence/export.go` | Rename wiki→space types/fields/methods; drop `channel` line; single `ExportSpace`; `version: 2` + source namespace; `update_at` |
| `services/confluence/intermediate.go` | `setupSpace` (single); `IntermediateSpace`; multi-space guardrail; TipTap `json.Valid` check |
| `services/confluence/csv_parser.go` | Parse `ATTACHMENT` content rows → `export.Attachments`; parse `content_perm*` for restriction detection |
| `services/confluence/attachments.go` | Verify/adjust CSV attachment path layout (pending real fixture) |
| `services/confluence/validation.go` | 2 MiB / 64 KiB caps; restricted-page reporting; remove legacy target-channel validation |
| `services/confluence/manifest.go` | `source` namespace; `restricted_pages`; keep target team as advisory metadata and remove `target.channel` |
| `services/confluence/types.go` | Rename `IntermediateWiki`→`IntermediateSpace` (transform side) |
| `commands/transform.go` | `--bundle`; deprecate then remove `--channel`; document `--team` as advisory metadata; drop `--create-channel`; fix recipe; summary wording |
| `docs/cli/` | `make docs` |

## Risks & Open Questions

1. **Attachment file layout is unverified** — §4 metadata parsing is safe, but
   closing the attachments item needs a real CSV export containing files to
   confirm the on-disk path. This is the one item that can't be fully finished
   from the current sample.
2. **No shipped consumer** — the v2 producer contract exists ahead of the Docs
   import API. Freeze any remaining field changes with the plugin team before
   the first consumer lands (see the integration plan's rollout).
3. **`update_at` is advisory** — even when emitted, the plugin's interactive
   `CreatePage` overwrites timestamps; preserving them needs the plugin's
   import-specific methods. mmetl emitting it is necessary but not sufficient.
4. Dropping `channel`/`--create-channel` is intentional: mattermost#37321 makes
   Space backing channels opaque and plugin-managed. The `target.channel`
   manifest field and the `--channel` flag are now **removed** — no channel
   surface remains on the producer.

## Rollout

1. **Done:** single-Space v2 contract, channel line removal, bundle output,
   attachment metadata, validation, restriction detection, and producer tests.
2. **Done:** removed manifest `target.channel`, the legacy channel validation,
   and the `--channel` flag (plus all channel plumbing). No channel surface
   remains on the producer.
3. Close attachment extraction once a real attachment-bearing CSV export proves
   the on-disk layout. **This is the remaining producer blocker.**
4. Freeze the resulting producer schema with the Docs import consumer and run
   the cross-repository E2E suite.
