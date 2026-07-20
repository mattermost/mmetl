# Confluence Cloud CSV-Export Support for mmetl

> **Status:** Implemented by `2ca9cf1` (`feat: Confluence Cloud CSV export
> support`). This document is retained as the parser implementation record. The
> emitted Docs contract and import architecture are superseded by
> [confluence-docs-contract-alignment.md](confluence-docs-contract-alignment.md)
> and
> [confluence-jsonl-docs-plugin-integration.md](confluence-jsonl-docs-plugin-integration.md).
> Mattermost core Space-channel support subsequently landed in
> [mattermost#37321](https://github.com/mattermost/mattermost/pull/37321); the
> Docs import job remains the outstanding consumer dependency.

## Summary

Confluence Cloud's space export format has changed from a single self-describing
`entities.xml` file to a **relational dump of ~40 CSV tables**. The existing
mmetl Confluence overlay only parses the legacy XML. This plan **replaces the XML
parser with a CSV parser** that emits the *same* `ConfluenceExport` struct, so
every downstream stage — storage-format→TipTap conversion, hierarchy, links, user
mapping, export, manifest, validation — is reused **unchanged**.

**CSV is the only supported input.** We do not need the XML (or HTML) parser: the
legacy `parseEntitiesXML`/`parseHTMLExport` paths and their XML-only helpers are
**deleted**, not kept as a fallback. This removes ~800 lines of dead tokenizer
code and eliminates dual-format branching, detection ambiguity, and XML
regression surface.

The change is deliberately confined to the parser input layer plus a small
user-identity indirection. It also folds in the one-time work of porting the
uncommitted overlay onto the current `mmetl` `master`.

### Decisive fact that makes this cheap

Page/comment bodies in `bodycontent.csv` (`body` column) are **still Confluence
Storage Format** (`<ac:structured-macro>`, `<ac:layout>`, `<ri:url>`,
`<ac:adf-extension>`, `<ac:emoticon>`, …). Verified against the sample export.
Therefore `tiptap_converter.go` (the 33 KB, highest-risk component) and
everything downstream is untouched. Only *how we populate* `ConfluenceExport`
changes, not the struct or its consumers.

## Goal / Non-Goals

**Goals**
- `mmetl transform confluence -f <csv-export.zip>` works on the new Cloud CSV export.
- CSV is the sole input format; delete the XML/HTML parser and its dead helpers.
- Emit identical `ConfluenceExport` semantics from CSV so no downstream code changes.
- Land the overlay on current `master` as a clean, committed, tested feature.
- Portable, committed test fixture (current tests hardcode a colleague's local path).

**Non-Goals**
- No changes to `tiptap_converter.go`, `export.go`, `hierarchy.go`, `links.go`,
  `manifest.go`, `intermediate.go` transform logic (beyond what item 5 requires).
- **No backward compatibility with legacy `entities.xml` exports** — explicitly
  dropped at the user's direction. If a legacy XML export ever resurfaces, it is a
  separate future effort.
- No server-side Wiki/Pages import work — that is a separate, gating dependency
  (see Risks). This plan produces JSONL; consuming it is out of scope.
- Not attempting to fully support blogposts, Team Calendars, whiteboards, or
  data-classification tables (empty in the sample; parse defensively, ignore).

## Current State (grounded references)

Overlay lives **uncommitted** in a bundle, not yet in the repo:
- Patch: `~/Downloads/confluence-import-bundle/code-changes/mmetl/our-changes.patch`
  (touches `commands/transform.go` +239, `go.mod`), pinned to upstream `b87a10c`.
- Service package (14 files): `~/Downloads/confluence-import-bundle/mmetl-confluence-service/*.go`.

Key seams (bundle paths):
- `parser.go:19` `ParseConfluenceExport` — indexes zip files, then branches:
  `entities.xml` present → `parseEntitiesXML` (`parser.go:69`); else `parseHTMLExport`
  (`parser.go:492`). **Its body is replaced with a CSV-only implementation;
  `parseEntitiesXML`, `parseHTMLExport`, and every XML-only helper (`getXMLAttr`,
  `readElementText`, `parseXMLTime`, `parse*Element`, …) plus the `encoding/xml`
  import are deleted.** (Note: `tiptap_converter.go` also uses `encoding/xml` for
  storage-format bodies — that is a different file and stays.)
- `types.go` — `ConfluenceExport` (target struct) and `ConfluencePage`/`ConfluenceComment`/
  `ConfluenceUser`/`ConfluenceAttachment`. **No changes needed.**
- `intermediate.go:409` `resolveUsername(accountID, export)` — looks up
  `export.Users[accountID]` for email/username, then tries `UserMapper` by
  email→accountID→username, then heuristics. **Drives the user-key decision (item 5).**
- `user_mapper.go` — user-supplied mapping CSV keyed by `ConfluenceAccountID`
  (`GetUsername`), email, username. `ResolveUser(accountID,email,username)`.
- `attachments.go:17` `attachmentPathRegex = ^attachments/(\d+)/(\d+)/(\d+)$` —
  XML-export on-disk layout. **Unverified for CSV exports (item 6).**
- `validation.go:65` `ValidateExportFormat` — **unconditionally requires
  `entities.xml`** and appends a fatal error otherwise; called from `ValidateAll`
  (`validation.go:212`), which the ported command runs *after* parsing and then
  hard-aborts on `!Valid` (`our-changes.patch:202,218`). **This blocks every CSV
  export and must be fixed (item 7).**
- `confluence_test.go:19` — `TestParseRealExport`/`TestTransformRealExport`/
  `TestExportJSONL` read a hardcoded local **XML** zip and `t.Skip` if absent (not
  CI-portable). These XML-fixture tests are **removed** (or replaced with CSV
  equivalents); XML-macro converter tests that don't depend on the parser
  (`TestConvertHTMLToTipTap_*`) stay — they exercise `tiptap_converter.go`, which
  is unaffected.

Divergence blocking a clean `git apply` onto `master`:
- `commands/transform.go`: `master` added the `--bot-owner` flag + bot-export path
  (commit history) after the patch's base; context lines won't match.
- `go.mod`: `master` has `golang.org/x/text v0.34.0` (patch assumes `v0.32.0`) and
  the `golang.org/x/net` line has moved. The patch promotes `x/net` from indirect
  to direct.

### Sample export ground truth (verified)

- `~/Downloads/Confluence-export-mattermost.atlassian.net-~5d3eaa4376cb3e0d9d31cf8e/`
- `exportDescriptor.properties(.gz)`: `exportFormat=csv`, `exportType=space`,
  `source=cloud`, `spaceKey=~5d3eaa…`, `backupAttachments=true`,
  `inlineTasksFileIncluded=true`. Java `.properties` (`#comment` first line, `k=v`).
- **`.gz` files are PLAIN TEXT in this sample** (first bytes are `contentid,…`, not
  gzip magic `1f 8b`). `bodycontent.csv` has no `.gz` suffix at all. → must sniff
  the 2-byte gzip magic and decompress conditionally, ignoring the extension.
- First row of every CSV is a **header** with the exact column names below.

Confirmed headers:
- `content.csv`: `contentid,hibernateversion,contenttype,title,lowertitle,version,creator,creationdate,lastmodifier,lastmoddate,versioncomment,prevver,content_status,pageid,spaceid,child_position,parentid,messageid,pluginkey,pluginver,parentccid,draftpageid,draftspacekey,drafttype,draftpageversion,parentcommentid,username,navigationtype`
- `bodycontent.csv`: `bodycontentid,body,contentid,bodytypeid` (bodytypeid: 2=page/comment storage, 0=space desc, 1=custom property)
- `spaces.csv`: `spaceid,spacename,spacekey,spacedescid,homepage,creator,…,spacetype,spacestatus,lowerspacekey`
- `user_mapping.csv`: `user_key,username,lower_username,aaid`
- `contentproperties.csv`: `propertyid,propertyname,stringval,longval,dateval,contentid`
- `links.csv`: `linkid,destpagetitle,destspacekey,contentid,creator,…,lowerdestpagetitle,lowerdestspacekey`

**User-identity reality (decisive for item 5):** in `content.csv`, `creator`/
`lastmodifier` hold the internal **`user_key`**. `user_mapping.csv` maps
`user_key → aaid` (Atlassian account ID). The XML export path put the **account
ID** directly into `CreatedBy`, and `resolveUsername`/the user-supplied mapping
CSV both key on account ID. So the CSV parser must translate `user_key → aaid`
and store the **aaid** in `CreatedBy`/`UpdatedBy`, keying `export.Users` by both
the aaid and the user_key (see item 5). Note body **@-mentions embed the account
ID** (`<ri:user ri:account-id="…">`, verified — matches the `aaid` column), so
aaid keying is what makes mentions resolve, not just the external mapping.

## Proposed Approach

Add `services/confluence/csv_parser.go` implementing `parseCSVExport(zipReader,
export)`, called directly by a slimmed-down `ParseConfluenceExport`. It loads the
relevant tables into row structs, joins them, applies current-vs-historical
filtering, and populates the existing `ConfluenceExport`. The XML/HTML parser is
deleted (item 2). All other files are reused as-is except attachment handling
(item 6) and the validation gate (item 7).

### 1. Port the overlay onto `master` (prerequisite)

- Copy `~/Downloads/confluence-import-bundle/mmetl-confluence-service/*.go` →
  `services/confluence/` (14 files, including `confluence_test.go`).
- **Manually re-apply** the `commands/transform.go` half of `our-changes.patch`
  (do not `git apply`): add `TransformConfluenceCmd`, its flags, its
  `transformConfluenceCmdF`, and the `slackAttachmentsDir`/`confluenceAttachmentsDir`
  const split — preserving `master`'s `--bot-owner` additions. Reconcile by hand.
- `go.mod`: add the `confluence` package's real imports, then `go mod tidy` to let
  it promote `golang.org/x/net` to a direct dependency naturally. Do **not**
  hand-edit versions to match the stale patch (keep `x/text v0.34.0`).
- Gate: `go build ./...` succeeds; `mmetl transform confluence --help` lists flags.
- Commit this as an isolated "port overlay" commit before touching the parser.

### 2. Replace the parser body; delete the XML/HTML paths (`parser.go:42-53`)

`ParseConfluenceExport` no longer branches — it validates that this is a CSV
export and calls `parseCSVExport` directly:
1. If `exportDescriptor.properties` parses `exportFormat=csv`, or `content.csv`/
   `content.csv.gz` is present → `parseCSVExport`. (Prefer the `exportFormat=csv`
   single-line signal; a multi-space/full-site export may lay files out
   differently.)
2. Otherwise → return a clear error ("unsupported export: expected a Confluence
   Cloud CSV export"). **No XML/HTML fallback.**

Delete `parseEntitiesXML`, `parseHTMLExport`, `parseHTMLPage`, all `parse*Element`
readers, and the XML-only helpers (`getXMLAttr`, `readElementText`,
`readIDElement`, `parseXMLTime`, `skipElement`, …) plus the now-unused
`encoding/xml` import from `parser.go`. Go won't error on unused *functions*, but
`make check-style` (golangci `unused`) and the dropped import will — so remove
them in the same change. Add a CSV timestamp parser (dates look like
`2024-05-31 10:03:18.78`, not the XML format `parseXMLTime` handled).

Add a `readMaybeGzipCSV(f *zip.File) ([][]string, error)` helper: open, peek 2
bytes, wrap in `gzip.Reader` iff magic == `0x1f 0x8b`, else read plain; parse
with `encoding/csv` (`FieldsPerRecord=-1`, header row → column-index map by name
so we are resilient to column reordering).

### 3. Table loading + joins (`csv_parser.go`)

Load into a small in-memory model keyed by `contentid`. Steps, in dependency order:

1. **`exportDescriptor.properties`** → space key, `exportFormat`, `backupAttachments`.
2. **`spaces.csv`** → `SpaceInfo{Key,Name,Description,HomePageID}`; set
   `export.Spaces[key]` and legacy `export.SpaceKey/SpaceName`. Populate
   `HomePageID` **directly from the explicit `homepage` column** (verified:
   `homepage=2317516965` → the "Overview" page). Do *not* infer root by
   `parentid==""` — historical/draft rows also have empty `parentid`.
   `spacedescid` = space-description content row.
3. **`user_mapping.csv`** (`user_key,username,lower_username,aaid`) → build a
   `userKey → aaid` translation map. See item 5 for how the identity key is used.
   Note: this table has **no email column**, and in the sample `username`/`aaid`
   are themselves opaque account IDs, not human-readable — so it is a translation
   table only, not a source of display names/emails.
4. **`content.csv`** → iterate rows, switch on `contenttype`:
   - `PAGE`: build `ConfluencePage` (translate `creator`/`lastmodifier` per item 5).
     Apply the **visible-page filter**: `content_status=current` AND `spaceid`
     non-empty → real page. **`content_status=current` is set on every version
     row, not just the live one** (verified: 11 of 22 "current" PAGE rows are
     actually historical, with blank `spaceid`), so `content_status` alone is NOT
     a sufficient discriminator — `spaceid` presence is the real signal.
   - `COMMENT`: build `ConfluenceComment` with `PageID=pageid`,
     `ParentID=parentcommentid` (flat, page-scoped threading — verified). Reuse the
     existing PageID-backfill-from-parent loop (`intermediate.go:330-337`) for
     defensiveness.
     Also carry `ParentID=parentid`, `SpaceKey`, `Version`, and
     `Position=child_position` — `child_position` is an **opaque position-tree
     key**, not a compact rank (verified values span `745`…`381695930`); it sorts
     monotonically so `comparePageOrder` (`hierarchy.go:196`) works unchanged —
     pass it through as-is, do not renumber or infer depth from magnitude.
     (`content.csv` also has a trailing `username` column distinct from
     `creator`/`lastmodifier`; it is empty in all sampled rows — **ignore it**,
     do not use it for identity.)
   - `SPACEDESCRIPTION`: attach to space description body.
   - `CUSTOM`: **drop** (plugin content-property storage; ~74% of rows). Note some
     CUSTOM rows carry a `pageid` FK (e.g. `content-appearance-draft`) — do not
     mistake that for a child/version/attachment relationship.
5. **Historical/draft resolution (`prevver`)** — verified empirically:
   **`prevver` points DIRECTLY at the canonical current `contentid`, in one hop —
   it does NOT chain version-to-version.** All historical rows of a page share the
   same `prevver` value (the live page's id). So:
   - `HistoricalPageIDs[id] = true` for every `PAGE` row with
     `content_status=current && spaceid=="" ` (these all carry a non-empty `prevver`).
     Attach nothing to them; resolve to the canonical page via `prevver` in one step
     (no chain-walking).
   - `content_status=draft` rows also use `prevver` (pointing at the page they draft)
     — skip them; do **not** conflate drafts with historical versions.
6. **`bodycontent.csv`** (`bodycontentid,body,contentid,bodytypeid`) → join **by
   `contentid`** (not the `bodycontentid` PK). **Index bodies into a
   `map[contentid]body` first, then look up per page/comment** to avoid an O(n²)
   join. Store in `export.BodyContents`; set `page.Content` / `comment.Content`
   (storage format, verbatim). `bodytypeid` 2 = page/comment storage body.
7. **`contentproperties.csv`** (`propertyid,propertyname,stringval,longval,dateval,contentid`)
   → index by `contentid`. **Inline-comment anchors are first-class CSV properties
   here — no HTML scraping needed** (unlike the XML path's
   `extractInlineCommentAnchors`): read `inline-marker-ref` and
   `inline-original-selection` (the actual highlighted text) straight off the
   comment's own `contentid` rows, and `status=resolved` for `IsResolved`. This is
   simpler and more robust than the XML path — the `ContentPropertyIDs` indirection
   is unnecessary for CSV. Skip `extractInlineCommentAnchors` on the CSV path.
8. **`links.csv`** → per source `contentid`, pre-extracted links
   (`destspacekey`/`destpagetitle` or URL). Feed whatever `links.go`/
   `resolve_wiki_placeholders` expects (verify the existing shape and adapt the
   emit, not the consumer).
9. **`label.csv` + `content_label.csv`** → join labels onto pages (`labelabletype=CONTENT`).
10. **`content_perm_set.csv` + `content_perm.csv`** → **parse only; not emitted in
    v1** (see "Data fidelity" below). The join is
    `content_perm.cps_id → content_perm_set.id → content_perm_set.content_id`, and
    the `username` column holds **`user_key`** values (verified) — key off
    `user_key`, not `user_mapping.username`. `ConfluencePage.Restrictions`
    (`types.go:72`) can hold this, but neither `IntermediatePage` nor
    `PageImportData` has a restrictions field, so it cannot reach the JSONL without
    type + server-importer changes. **Recommendation: skip parsing restrictions in
    v1** to avoid populating a struct nothing reads; defer to the follow-up in the
    Data fidelity section.

### 4. Current-vs-historical / draft filtering

Applied inline during `content.csv` parsing (items 4–5 above); this is the
consolidated rule set:
- Visible page: `contenttype=PAGE && content_status=current && spaceid != ""`.
- Historical version: `PAGE/current` with `spaceid=="" && prevver != ""` →
  `HistoricalPageIDs`. Do **not** emit as a page; downstream already skips these.
- Draft: `content_status=draft` → skip.
- Comments: current unless in `HistoricalCommentIDs`; thread via `parentcommentid`.

### 5. User-key indirection (in the parser only — no shared-code changes)

`content.csv` `creator`/`lastmodifier` hold the internal **`user_key`**;
`user_mapping.csv` maps `user_key → aaid` (Atlassian account ID). Whatever string
the parser writes into `CreatedBy`/`UpdatedBy` and uses as the `export.Users` map
key is what every downstream consumer keys off (`resolveUsername`
`intermediate.go:409`; mention resolution `links.go:272`; restriction lookups).
Either choice keeps shared code unchanged **as long as it is applied
consistently**; the difference is which key reaches the external `--user-mapping`
CSV via `UserMapper`.

**Decision: store the `aaid` as the identity, and key `export.Users` by BOTH the
`aaid` and the `user_key` (alias to the same struct).**
- `CreatedBy`/`UpdatedBy` = `aaid`, so `resolveUsername` and the external
  account-ID-keyed `--user-mapping` CSV (`UserMapper.byAccountID`, `user_mapper.go`)
  resolve — orgs build that CSV from Atlassian account IDs, not Confluence's
  internal `user_key`, so aaid is the realistic key.
- Populate `export.Users[aaid] = u` **and** `export.Users[user_key] = u` (same
  `*ConfluenceUser{AccountID: aaid, ...}`). This costs nothing and closes the
  mention-resolution gap below.

**Why both keys — mention resolution (reviewer catch, verified).** Body mentions
become `{{CONF_USER:<key>}}` placeholders (`links.go:145`) that
`ResolveUserMentions`/`resolveUserMention` (`links.go:270,291`) resolve by looking
`<key>` up directly in `export.Users`. The capture regex accepts **both**
`ri:account-id` and `ri:userkey`. In the sample, **all 100 mentions use
`<ri:user ri:account-id="…">` and those account-ids exactly match the `aaid`
column** — so an aaid-keyed map is *required* for Cloud CSV mentions to resolve
(a user_key-only key would miss every mention). Adding the `user_key` alias also
covers any `ri:userkey` markup (Server/DC-origin content) without a `links.go`
change — keeping this **parser-only**. (The CSV reader un-doubles the `""`-escaped
quotes in the body field, so the post-parse markup is normal `ri:account-id="…"`.)

Important nuance (from advisor): `user_mapping.csv` has **no email column** and
its `username`/`aaid` are opaque IDs, so Confluence's own mapping yields no
human-readable names. The external `--user-mapping` CSV is therefore the
*primary* human-resolution mechanism for CSV exports; unmapped users fall through
to `resolveUsername`'s `confluence_user_<id>` last resort exactly as today.

> Resolved: the earlier aaid-vs-user_key tension (advisor preferred raw
> `user_key`) is moot once `export.Users` is keyed by **both** — external mapping
> works via the aaid, and both mention placeholder forms resolve. Identity stored
> on the page/comment stays the aaid.

### 6. Attachments (known unknown — design the seam, flag the risk)

The sample has **zero** attachments (no `attachments/` directory present at all,
despite `backupAttachments=true`), so the CSV export's on-disk attachment layout
is **unverified** — no authoritative Atlassian doc confirms the path convention
for space CSV exports. `attachments.go:15` assumes
`attachments/{pageID}/{attachmentID}/{version}`. Plan:
- Parse the attachment metadata (likely `contenttype=ATTACHMENT` rows in
  `content.csv`, or a dedicated table in exports that contain files — TBD) into
  `export.Attachments[pageID]`.
- Leave `ExtractAttachments` as-is for now; add a `// TODO(csv-attachments):
  layout unverified` note and log a clear warning if `backupAttachments=true` but
  no attachment files match the regex.
- **Explicitly out of scope to finish** until an export *with* attachments is
  available. Track as a follow-up.

### 7. Pre-flight validation must accept CSV (blocking bug)

`ValidateExportFormat` (`validation.go:65`) unconditionally fails when
`entities.xml` is absent, and the ported `transformConfluenceCmdF` calls
`ValidateAll` after parsing and returns `"pre-flight validation failed"` when
`!Valid` (`our-changes.patch:202,218`). **A CSV export has no `entities.xml`, so
without this fix every CSV run aborts after a successful parse** — directly
defeating acceptance criterion #1. Fix: **replace** the `entities.xml` requirement
in `ValidateExportFormat` with the CSV signal (`exportFormat=csv` or presence of
`content.csv`/`content.csv.gz`), mirroring the detection in item 2. The
`entities.xml` check is removed, not kept — there is no XML path anymore.

## Files to Change / Add

| File | Change |
|------|--------|
| `services/confluence/*.go` (14) | **Add** — port from bundle (item 1) |
| `commands/transform.go` | **Edit** — manually merge `transform confluence` subcommand onto master's `--bot-owner` version |
| `go.mod` / `go.sum` | **Edit** — `go mod tidy` (x/net becomes direct) |
| `services/confluence/parser.go` | **Rewrite** — `ParseConfluenceExport` becomes CSV-only; **delete** `parseEntitiesXML`, `parseHTMLExport`, `parse*Element`, XML-only helpers, and the `encoding/xml` import (item 2) |
| `services/confluence/csv_parser.go` | **Add** — `parseCSVExport` + table loaders + joins + gzip sniff + CSV timestamp parser |
| `services/confluence/csv_parser_test.go` | **Add** — unit tests over a committed fixture |
| `services/confluence/testdata/csv-export-min.zip` | **Add** — small committed CSV-export fixture |
| `services/confluence/confluence_test.go` | **Edit** — **remove** XML-fixture tests (`TestParseRealExport`/`TestTransformRealExport`/`TestExportJSONL`); keep parser-independent `TestConvertHTMLToTipTap_*` |
| `services/confluence/attachments.go` | **Edit** — add `// TODO(csv-attachments)` + warn when `backupAttachments=true` but no files match the regex (item 6) |
| `services/confluence/validation.go` | **Edit** — `ValidateExportFormat` requires the CSV signal, drops the `entities.xml` requirement (item 7 — blocking) |
| `docs/cli/` | **Regen** — `make docs` (new subcommand docs) |

No changes to: `types.go`, `tiptap_converter.go`, `intermediate.go` (transform),
`export.go`, `hierarchy.go`, `links.go` (consumer), `manifest.go`, `user_mapper.go`.
(`parser.go` and `validation.go` **do** change — see items 2 and 7.)

## Testing Strategy

- **Committed fixture**: build `testdata/csv-export-min.zip` from **exact excerpts
  of the real sample CSVs** (not synthetic) — the non-obvious behaviors are easy to
  get subtly wrong. Include specifically: the "PV React Summit Business Case"
  version triad (historical rows v1–v3 all with `prevver`→ the v4 canonical id +
  blank `spaceid`, plus the current v4), a draft row, the space homepage
  ("Overview"), a page with a parent, the inline-comment `contentproperties` rows
  (`inline-marker-ref`/`inline-original-selection`/`status=resolved`),
  `user_mapping`, and one storage-format body exercising a macro/`ac:layout` and a
  `<ri:user ri:account-id="…">` mention.
- **Unit tests** (`csv_parser_test.go`):
  - gzip-magic sniff: gzipped vs plain `.gz` both parse.
  - a non-CSV/unrecognized zip returns the clear "unsupported export" error.
  - visible-page filter: current+spaceid kept; historical (blank spaceid+prevver)
    → `HistoricalPageIDs`; draft skipped.
  - hierarchy: `parentid`/homepage produce the expected tree + root.
  - comment threading via `parentcommentid`.
  - user_key→aaid: `CreatedBy` is the aaid; `export.Users` keyed by **both** aaid
    and user_key; end-to-end `resolveUsername` with a supplied mapping CSV resolves.
  - **mention resolution**: a body `<ri:user ri:account-id="<aaid>">` resolves to
    `@user` (aaid key path); a synthetic `<ri:user ri:userkey="<user_key>">`
    resolves via the alias — neither leaves a `{{CONF_USER:…}}` placeholder.
  - bodies: storage format lands verbatim in `Content`; a full parse→transform→
    export produces valid `wiki`/`page`/`page_comment` JSONL lines.
  - inline anchors read directly from `contentproperties` (no HTML scraping);
    `IsResolved` set from `status=resolved`.
- **End-to-end integration test** over the real sample bundle (zipped):
  `ParseConfluenceExport → TransformPages → TransformComments → Export`, asserting
  final counts match the report (**11 visible pages, 8 current comments, 1 space**).
  This exercises the whole historical/draft filtering chain together.
- **Gates** (AGENTS.md): `make check-style` and `make test` must pass.

## Acceptance Criteria

- [ ] `mmetl transform confluence -f <csv-export.zip> -t team -c chan` produces a
      non-empty `wiki-import.jsonl` + manifest without errors on the sample.
- [ ] `ParseConfluenceExport` accepts CSV via `exportDescriptor.properties`; a
      non-CSV zip returns a clear "unsupported export" error.
- [ ] XML/HTML parser code and `encoding/xml` import removed from `parser.go`;
      `make check-style` reports no unused-code/import errors.
- [ ] Pre-flight validation (`ValidateExportFormat`) passes for a CSV export.
- [ ] `.gz`-suffixed-but-plaintext files and genuinely-gzipped files both parse.
- [ ] Visible pages only (current + spaced); historical versions and drafts excluded.
- [ ] Comments threaded; historical comment versions excluded.
- [ ] Users resolve through the existing `UserMapper` unchanged (aaid indirection).
- [ ] `make check-style` and `make test` pass; `make docs` up to date.
- [ ] Committed CSV fixture; no test depends on a machine-local path.

## Data Fidelity — What Is Not Migrated (and how to add it later)

This documents everything in the CSV export that v1 drops, so migrations set
expectations correctly and we can pick items up incrementally. Each item notes
what adding it would take. Two structural facts frame the whole list:

- The **output line types** (`wiki`, `page`, `page_comment`) only have the fields
  in `export.go`'s `*ImportData` structs. Anything with no field there cannot be
  emitted regardless of what the parser collects.
- Adding any dropped field is a **three-layer change**: parser → a new field on
  the `Intermediate*` + `*ImportData` struct → **server-side Wiki/Pages importer
  support** for that field. The third layer is outside this repo, so most of these
  are gated on the importer, not on mmetl.

### Content format note
- **Pages**: `content` is **TipTap JSON**. **Comments**: `content` is **Markdown**
  (`transformComment`, `intermediate.go:371`) — comments render as normal posts,
  not rich pages. Intentional; listed here so it isn't mistaken for a gap.

### Entire tables dropped
| Table(s) | Data lost | To add later |
|----------|-----------|--------------|
| `spacepermissions`, `spaceroles`, `spacerole_*`, `space_owner` | Space-level permissions / roles / ownership | Map to channel membership + roles; needs a channel-ACL design |
| `content_perm(_set)` | Per-page **view/edit restrictions** — allowlists of who may read (View) vs. edit (Edit) a specific page, overriding space defaults | New `page` restriction field + importer support; parser join already speced (item 10) |
| `likes` | Page/comment likes | Map to reactions; needs a reaction line type |
| `notifications` | Watches / subscriptions | Map to follows; low value, likely never |
| `AO_BAF3AA_AOINLINE_TASK` | Inline-task **state** (status/assignee/due date) | Task markup in the body still renders; state needs a task model |
| `content_relation` | Page "copy" relationships | Cosmetic; likely never |
| `pagetemplates`, `templateattachment*`, `templateproperties` | Space templates | Separate template-import feature |
| `AO_950DC3_*` (Team Calendars), `AO_187CCC_SIDEBAR_LINK`, `content_data_classification_mapping`, `bandana`, `os_propertyentry`, `space_alias` | Calendars, sidebar links, data classification, space config | Out of scope; mostly empty in practice |

> **⚠️ Confidentiality note (deferred — do later):** page **View** restrictions
> are security-relevant, not cosmetic — a view-restricted Confluence page was
> deliberately hidden from most of the org. Because v1 drops restrictions and has
> no per-page ACL target, **every restricted page imports fully visible to
> everyone who is a member of the imported Space** — an over-exposure of
> previously-confidential
> content. We are accepting this for v1 and will handle it later. The eventual fix
> has two parts: (1) **detect + warn/exclude** restricted pages so a human decides,
> and (2) **map restrictions to an ACL** (channel visibility/membership, or a
> real per-page restriction field once the importer supports one). Until then,
> migrations of spaces with restricted pages should be treated as widening access.

### Fields dropped within imported entities
| Entity | Dropped field(s) | To add later |
|--------|------------------|--------------|
| Page | **version number**, **last-modified time**, **last modifier**, `versioncomment` | Add `update_at`/`edited_by`/`version` to `PageImportData` + importer support |
| Page | Only the **current version** is imported — no version history | Would require a page-revision import model |
| Comment | update time, last editor | Add fields to `PageCommentImportData` + importer support |
| Attachment | media type, file size, uploader, upload time, attachment comment | Extend `AttachmentImportData.Props` (parser can fill from the attachment table) |
| `contentproperties` | All keys except `inline-marker-ref`, `inline-original-selection`, `status=resolved` (e.g. `sync-rev`, `share-id`, cover/appearance, `sourceTemplateKey`, `collabService`) | Case-by-case; most are Confluence-internal and not meaningful in Mattermost |

### Records excluded by design
- **Drafts** (`content_status=draft`) — skipped.
- **Historical versions** — skipped; only the live page/comment.
- **`CUSTOM` content** (~74% of `content.csv`) — plugin storage, not user content.
- **Blogposts** — not handled (see Risk #5); decide map-to-page vs exclude.

### Preserved in v1 (for contrast)
Page hierarchy (parent + `child_position` order), space→wiki mapping, page body
(TipTap) and comment body (Markdown), comment threads + resolved state + inline
anchors, labels (as `import_labels` prop), cross-page links
(`resolve_wiki_placeholders`), creator + creation time, attachment bytes + path.

## Risks & Open Questions

1. **A Docs plugin import job must exist** to consume the versioned
   `space`/`page`/`page_comment` bundle — these are not stock Mattermost
   bulk-import line types. Core support for the opaque `ChannelTypeSpace`
   backing channel landed in mattermost#37321, but it does not parse Docs JSONL.
   The plugin import consumer remains the true gating dependency and is
   *independent* of this parser work.
2. **Attachment layout unverified** (sample has none). Item 6 stubs the seam;
   completing it needs an export containing files.
3. **`links.csv` → downstream shape**: verify what `links.go`/
   `resolve_wiki_placeholders` currently expects (it was fed by XML-scraped
   links) and adapt the *emit*, not the consumer. Low risk, must confirm.
4. **Inline-comment anchors** move from inline XML markers to
   `contentproperties` rows — the anchor-UUID↔page-offset correlation the XML
   path relied on may not reconstruct perfectly; degrade gracefully (comment
   still imports, just not anchored) rather than failing.
5. **Blogposts unverified**: sample has zero `BLOGPOST` rows. Cloud CSV likely
   uses a distinct `contenttype=BLOGPOST`; decide explicitly whether to map them
   through the PAGE path or exclude them — do not assume they "just work".
6. **Multi-space CSV layout unverified**: sample is a single `exportType=space`
   personal space. A multi-space/full-site CSV export may use a different file
   layout (subdirectories, split tables). Parse defensively; don't assume one
   space; test separately if the org needs multi-space migration.
7. **`content.csv` column drift** across Cloud versions — index columns by header
   name, never by position.

## Rollback

The feature is gated behind a new `transform confluence` subcommand — the Slack
path is untouched, so rollback = revert the commits. Note that because the XML
parser is deleted (not kept as a fallback), reverting only the CSV commits would
also restore the XML parser; a partial rollback that keeps the port but drops CSV
is not a supported state.
