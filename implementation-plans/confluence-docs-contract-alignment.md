# Confluence CSV → Docs contract alignment (mmetl)

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

- Emits v1 lines `version`, `channel`, `wiki`, `page`, `page_comment`,
  `resolve_wiki_placeholders` ([export.go:24,152-408](../services/confluence/export.go)).
- **Multi-space scaffolding is dead code**: `ConfluenceExport.Spaces`
  ([types.go:16](../services/confluence/types.go)), `setupWikisFromSpaces` looping
  `range export.Spaces` ([intermediate.go:189](../services/confluence/intermediate.go)),
  and `ExportWikis` emitting one `wiki` per space ([export.go:180](../services/confluence/export.go)).
  The CSV parser only ever inserts one space ([csv_parser.go:247](../services/confluence/csv_parser.go))
  and assigns every page `page.SpaceKey = export.SpaceKey` — nothing populates >1.
- **Attachments unpopulated**: `csv_parser.go` has no `ATTACHMENT` handling, so
  `export.Attachments` is empty, `ExtractAttachments` matches nothing
  ([attachments.go:38](../services/confluence/attachments.go)), and
  `ConvertAttachmentPlaceholdersToFileIDs` is a no-op because its `filenameToID`
  map is empty ([intermediate.go:247-256](../services/confluence/intermediate.go),
  [links.go:345](../services/confluence/links.go)) — so `CONF_ATTACHMENT` tokens
  are never rewritten to `CONF_FILE`.
- **Channel dictated by producer**: every `wiki`/`page` line carries Team+Channel,
  and `--create-channel` emits a `channel` line ([export.go:159](../services/confluence/export.go)).
  `--channel` is currently a **required** flag ([transform.go](../commands/transform.go)).
- **Identity already preserved** (from the CSV work): `props.confluence_author_account_id`
  on pages/comments + a manifest `users` list.
- Body size warned only >10 MiB; Docs caps Body/SearchText at 2 MiB and Props at
  64 KiB. `update_at` captured (`IntermediatePage.UpdateAt`) but not emitted.

## Proposed Approach

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

### 3. Stop dictating the backing channel

Per the reconciliation, the Docs plugin owns the Space's backing channel; mmetl
must not decide it.

- Drop the `channel` line and `ExportChannel`/`AutoCreateChannel`/`--create-channel`.
- Remove `Channel` from the `space`/`page` contract (backing channel is resolved
  at import time from the import request).
- Keep `--team` as target-team context. Make `--channel` **optional** and
  **deprecated** (documented as ignored in v2), or remove it; do not keep it as a
  required backing-channel input.

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
    namespace).
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
| `services/confluence/validation.go` | 2 MiB / 64 KiB caps; restricted-page reporting |
| `services/confluence/manifest.go` | `source` namespace; `restricted_pages` |
| `services/confluence/types.go` | Rename `IntermediateWiki`→`IntermediateSpace` (transform side) |
| `commands/transform.go` | `--bundle`; deprecate/relax `--channel`; drop `--create-channel`; fix recipe; summary wording |
| `docs/cli/` | `make docs` |

## Risks & Open Questions

1. **Attachment file layout is unverified** — §4 metadata parsing is safe, but
   closing the attachments item needs a real CSV export containing files to
   confirm the on-disk path. This is the one item that can't be fully finished
   from the current sample.
2. **No shipped consumer** — the v2 contract is being defined ahead of the Docs
   import API. Names/fields here should be agreed with the plugin team so both
   sides land together (see the integration plan's rollout).
3. **`update_at` is advisory** — even when emitted, the plugin's interactive
   `CreatePage` overwrites timestamps; preserving them needs the plugin's
   import-specific methods. mmetl emitting it is necessary but not sufficient.
4. Dropping `channel`/`--create-channel` is a behavior change; acceptable now
   because nothing consumes it, but coordinate before anyone builds on v1.

## Rollout

1. §1 single-space + §2 naming/version (contract-shaping, do first, together).
2. §3 channel removal + §5 bundle (producer surface).
3. §4 attachments metadata + §6 validation + §7 restrictions.
4. §8 tests throughout; close the attachments item once a real
   attachment-bearing CSV export is available.
