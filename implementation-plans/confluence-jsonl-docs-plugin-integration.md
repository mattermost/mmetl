# Confluence JSONL → Mattermost Docs integration

## Bottom line

The JSONL produced by `mmetl` cannot currently be imported into the checked-out Docs plugin or Mattermost `master` as-is. It targets the older Mattermost `wiki-poc` import contract, while ownership of Spaces and Pages has moved into `mattermost-plugin-docs`.

The recommended architecture is:

```text
Confluence export
  → mmetl versioned ZIP bundle
  → Docs plugin admin import API/job
      → DOCS_Space / DOCS_Page / comments / mappings
      → Mattermost APIs for channels, users, membership, and files
```

The Docs plugin should own parsing, validation, import jobs, idempotency, hierarchy, comments, attachments, and link resolution. Mattermost core should remain unaware of Docs-specific JSONL entities.

## Current incompatibilities

- `mmetl` emits custom `wiki`, `page`, `page_comment`, and `resolve_wiki_placeholders` lines ([export.go](../services/confluence/export.go:20)).
- Docs `master` contains foundational Space/Page storage but no import API, import job, comments, attachment ownership, or search implementation ([api.go](/Users/willyfrog/Proyectos/mattermost-plugin-docs/server/api.go:14)).
- Mattermost bulk import has no fields for these entities and rejects unknown line types ([import_types.go](/Users/willyfrog/Proyectos/mattermost/server/channels/app/imports/import_types.go:15), [import.go](/Users/willyfrog/Proyectos/mattermost/server/channels/app/import.go:398)).
- The old [`wiki-poc` importer](/Users/willyfrog/.supacode/repos/mattermost/wiki-poc/server/channels/app/import_wiki_functions.go:74) is useful as an algorithm and test reference, but writes obsolete core Wiki/Page models rather than plugin tables.

## Changes required in `mmetl`

> **Status update — CSV support has since landed** (branch
> `worktree-confluence-csv-parser`, see [confluence-csv-export-support.md](confluence-csv-export-support.md)).
> This changes the baseline several items below were written against:
> - Input is now **Confluence Cloud CSV** exports (the legacy `entities.xml`/HTML
>   parser was removed). The producer emits the **same v1 line types** — `wiki`,
>   `page`, `page_comment`, `resolve_wiki_placeholders` — so every contract item
>   below (§1 versioning, §3 bundle, source namespace) is still open.
> - **Identity is now preserved** for downstream matching: every `page`/`page_comment`
>   carries `props.confluence_author_account_id` (the Atlassian account ID), and the
>   manifest now includes a `users` list of `{account_id, confluence_username,
>   mattermost_username}`. The Docs importer should consume these (see §4 and the
>   plugin-side mapping tables) rather than re-deriving identity.
> - `ValidateExportFormat` now accepts the CSV signal (it no longer requires
>   `entities.xml`).
> - **CSV space exports are single-space by construction** (`exportType=space`),
>   which changes the multi-space item in §5.

### 1. Freeze and version the contract

- Continue accepting v1 `wiki` records as an adapter format, but define each one as a Docs Space.
- For v2, rename `wiki` to `space`.
- Do not treat `wiki.channel` as the Space backing channel; the plugin creates its own `ChannelTypeSpace` channel.
- Make the import request authoritative for target team and access policy.
- Add a source namespace, such as Confluence cloud/site ID. Numeric page IDs and space keys can collide across Confluence instances.

### 2. Fix comment scoping

Standalone comments still contain only `page_import_source_id` ([export.go](../services/confluence/export.go:70)); no space/wiki source ID. Add a space source ID, or require the importer to resolve comments through a job-scoped mapping table. Never perform a global source-ID lookup. (With CSV single-space bundles the collision is bounded to *cross-instance* re-use of numeric IDs, but the job-scoped mapping is still the correct fix — see §1's source namespace.) Note the comment now carries `props.confluence_author_account_id` for author attribution, which is orthogonal to scoping.

### 3. Produce a complete bundle

Still unaddressed: the printed ZIP recipe zips only the attachments dir (`cd data && zip -r ../import.zip .`) and omits the separately written JSONL, which is emitted to the `--output` path in the cwd ([transform.go](../commands/transform.go:397)). The bundle should contain:

```text
import.jsonl
import-manifest.json
data/<page-id>/<attachment>
```

Ideally add `--bundle output.zip` so operators cannot package it incorrectly.

### 4. Align validation with Docs

- Docs pages cap Body and SearchText at 2 MiB (`PageBodyMaxBytes`/`PageSearchTextMaxBytes`, [page.go:23](/Users/willyfrog/Proyectos/mattermost-plugin-docs/server/model/page.go:23),[:29](/Users/willyfrog/Proyectos/mattermost-plugin-docs/server/model/page.go:29)); `mmetl` currently warns only above 10 MiB. Also cap Props at 64 KiB (`PagePropsMaxBytes`, [page.go:26](/Users/willyfrog/Proyectos/mattermost-plugin-docs/server/model/page.go:26)) — relevant now that `mmetl` stamps `props.confluence_author_account_id`, `import_labels`, and `confluence_space_key` into every page.
- User validation is now partly served by the manifest `users` list (`{account_id, confluence_username, mattermost_username}`): `mmetl` cannot check usernames against a live server, but it surfaces every source user and its resolved/fallback Mattermost username so an operator can review unmapped users pre-import. `--fallback-user` covers unmapped users; keep requiring an explicit valid fallback. The importer should validate the manifest users against the target server.
- `update_at` is still dropped: `IntermediatePage.UpdateAt` is populated but `PageImportData` emits only `create_at`. Emitting it is necessary but not sufficient — the plugin's interactive `CreatePage` stamps `CreateAt`/`UpdateAt`/`LastModifiedBy` itself ([page.go:29](/Users/willyfrog/Proyectos/mattermost-plugin-docs/server/app/page.go:29)), so preserving source timestamps also requires the import-specific methods in the plugin §2.
- Validate TipTap JSON before output (still not done).

### 5. Fix fidelity blockers

- The CSV parser does not populate `export.Attachments` at all ([csv_parser.go](../services/confluence/csv_parser.go)), so attachment metadata is absent, `ExtractAttachments` matches nothing, and — because normalization needs the filename→ID map — `CONF_ATTACHMENT` tokens are **not** rewritten to `CONF_FILE`. A runtime warning was added when `backupAttachments=true` but no files match ([attachments.go:38](../services/confluence/attachments.go:38)). Net: **attachments are effectively unsupported in the CSV path today** and the attachment file layout for CSV exports is still unverified.
- Multi-space is **not applicable to a single CSV bundle**: Confluence Cloud CSV exports are `exportType=space` (one space per export), and the CSV parser assigns every page to the single `export.SpaceKey`. The multi-space `Spaces`/`ExportWikis` scaffolding is XML-era carryover. Treat **one bundle = one Space**; migrating N spaces means N bundles imported separately. Either drop the multi-space scaffolding or gate it behind a verified multi-space export format — do not claim multi-space support from the current code.
- Page restrictions (`content_perm`) are currently **not parsed** in the CSV path (deliberately deferred — see [confluence-csv-export-support.md](confluence-csv-export-support.md)), and there is no runtime warning identifying restricted pages. Detect/block or explicitly acknowledge them; otherwise restricted content becomes visible to every imported Space member.
- Continue to support both unresolved `CONF_ATTACHMENT` and normalized `CONF_FILE` tokens until the attachment path is fixed and the producer always normalizes them.

### 6. Add tests

CSV-parser unit tests now exist ([csv_parser_test.go](../services/confluence/csv_parser_test.go): gzip sniff, historical/draft filtering, mention resolution, identity props, inline anchors, labels). Still missing: tests asserting the **exact emitted JSONL schema** per line type, bundle layout + checksums, source-ID collisions across instances, comment scoping, hierarchy, re-imports, and a full `mmetl → Docs` fixture. (Multi-space routing is no longer a test target — see §5; test one-bundle-one-Space instead.)

## Changes required in `mattermost-plugin-docs`

Base the work on `origin/MM-69268-page-tree-crud-url-api`, not the foundations-only `master` (verified still unmerged: `master` is 3 commits of foundations only; the branch is 62 commits ahead and adds `server/api.go` REST routes, `api_page.go`/`api_space.go`, page move/duplicate, and migration `000004`). On `master` today, `initRouter` registers **zero** routes ([api.go:14](/Users/willyfrog/Proyectos/mattermost-plugin-docs/server/api.go:14)); the Space/Page routes exist only on the branch. `CreateSpace` exists at the store layer on `master` ([space_store.go:30](/Users/willyfrog/Proyectos/mattermost-plugin-docs/server/store/space_store.go:30)) but the app/HTTP `handleCreateSpace` is on the branch. **No import API (`/imports`) exists on either.**

The plugin should ingest `mmetl`'s new bundle artifacts directly: the manifest `users` list as the authoritative source-user → Mattermost-username map, and each entity's `props.confluence_author_account_id` for attribution — rather than re-deriving identity from raw content.

### 1. Add durable import persistence

Suggested tables:

- `DOCS_ImportJob`: state, phase, actor, target, checksum, counts, errors, timestamps.
- `DOCS_ImportEntity`: `(source_namespace, entity_type, external_id, scope_id) → local_id`.
- Comment and page-file ownership tables.

Use explicit source-ID columns/tables instead of repeatedly scanning JSONB Props. Confirmed necessary: `DOCS_Page` today has a `Props` JSONB column but **no source-ID column** — the closest field, `OriginalId` ([page.go:61](/Users/willyfrog/Proyectos/mattermost-plugin-docs/server/model/page.go:61)), is a version-snapshot pointer, not an external import key. So without new mapping tables the importer would have to scan `Props` for `import_source_id`. Seed `DOCS_ImportEntity` from `mmetl`'s manifest `users` list (for user identities) plus the per-line `*_import_source_id` fields (for spaces/pages/comments).

### 2. Add import-specific services

Normal interactive `CreatePage` does not accept source props or timestamps ([page.go](/Users/willyfrog/Proyectos/mattermost-plugin-docs/server/app/page.go:29)). Add dedicated methods such as:

- `ImportSpace`
- `ImportPage`
- `ImportComment`
- `AttachImportedFile`
- `FinalizeImport`

These methods should preserve source metadata atomically, avoid per-entity notifications, derive `SearchText` from TipTap, and enforce the existing depth-10 hierarchy limit.

### 3. Implement a restartable job

1. Verify ZIP limits, paths, manifest, and checksum.
2. Parse and preflight without writes.
3. Resolve teams, users, source IDs, hierarchy, and capabilities.
4. Create Spaces/backing channels with compensation for orphan channels.
5. Insert pages topologically in bounded transactions.
6. Import comments and attachments.
7. Repair hierarchy and resolve links.
8. Compare final counts with the manifest and mark the job complete.

Retries should resume from committed phases; re-running the same bundle should be a no-op.

### 4. Add comment persistence

The plugin currently has no comment model. Add author, source timestamp, parent comment, resolved state, inline anchor, Props, and scoped source ID. Do not silently discard comments.

### 5. Add attachment ownership

`UploadFile` can place files in the Space backing channel, but Docs needs durable Page→File ownership and authorized download URLs. A plugin-owned join table may avoid a core change. If Files must have a first-class `PageId`, that becomes an additional Mattermost core change.

### 6. Resolve placeholders safely

Traverse parsed TipTap JSON rather than blindly replacing strings. Resolve page IDs/titles and attachment source IDs to canonical Docs URLs. Report unresolved references and make finalization idempotent.

### 7. Add the admin API

Add routes such as:

- `POST /api/v1/imports`
- `GET /api/v1/imports/{id}`
- `POST /api/v1/imports/{id}/retry`
- `POST /api/v1/imports/{id}/cancel`

Require system-admin authorization initially. Add audit records, progress events, and cleanup. Uploaded archives must use cluster-visible durable storage, not local `/tmp`.

### 8. Define membership and ACL semantics

JSONL contains authors but no reader membership. The import request must specify an access policy, such as an existing membership-source channel or an explicit member list. Create one backing Space channel per imported space and copy/sync authorized membership. Default to private/admin-only when restrictions are unknown. Note the linkage model: `DOCS_Space.ChannelId` ([space.go:29](/Users/willyfrog/Proyectos/mattermost-plugin-docs/server/model/space.go:29)) references a channel by ID, and that channel's `ChannelTypeSpace` (`"S"`) is a **core** concept supplied by the core branch (below) — the plugin does not define a channel-type enum itself. With CSV bundles being one-space (mmetl §5), this is exactly one backing channel per bundle.

## Changes required in Mattermost

For the intended Space-channel implementation, land `origin/MM-69268-page-tree-crud-url-api-core`, which supplies `ChannelTypeSpace` and the feature flag. Verified present on that branch and **not yet merged to master** (despite a misleadingly-titled `merge to master` commit on the branch): `ChannelTypeSpace ChannelType = "S"` ([channel.go:32](/Users/willyfrog/Proyectos/mattermost/server/public/model/channel.go:32)) and `EnableDocs` ([feature_flags.go:111](/Users/willyfrog/Proyectos/mattermost/server/public/model/feature_flags.go:111)); neither exists on `master`. The bulk importer still rejects unknown line types with a 400 ([import.go:397](/Users/willyfrog/Proyectos/mattermost/server/channels/app/import.go:397)), so Docs JSONL must never reach it.

Do not extend standard `mmctl import` with Docs-specific parsing. Optionally add:

```text
mmctl docs import <bundle.zip>
mmctl docs import validate <bundle.zip>
mmctl docs import status <job-id>
```

**Naming caveat:** an `mmctl docs` command already exists — it generates mmctl's own cobra documentation ([docs.go:13](/Users/willyfrog/Proyectos/mattermost/server/cmd/mmctl/commands/docs.go:13)). Reusing `docs` for a Docs-feature import would collide; either rename the existing doc-gen command, pick a different verb (e.g. `mmctl spaces import`), or carefully nest the new subcommands under it. These commands should call the plugin API. Core changes may be needed only if the archive/file design requires generic durable plugin uploads or first-class Page-owned `FileInfo`.

Docs search and UI belong primarily in the plugin. Mattermost global search does not index `DOCS_Page.SearchText`; AI/RAG integration is a separate Docs ↔ Mattermost AI project.

## Rollout sequence

1. Land the Docs CRUD and core `ChannelTypeSpace` branches.
2. Freeze the v2 contract and add v1 compatibility fixtures.
3. Implement import/mapping tables plus Spaces, pages, hierarchy, and idempotency.
4. Add comments, attachments, access policy, and final link resolution.
5. Add upload/status APIs and optional `mmctl docs` commands.
6. Run a three-repository E2E test covering counts, hierarchy, links, authors, permissions, retries, and checksums.
7. Enable production Confluence imports only after those tests pass.

A page-only milestone is acceptable only if unsupported comments, attachments, and restricted pages are rejected or require an explicit `allow_partial` acknowledgement. The importer must not report full success while silently dropping them.

## End-state operator flow

```bash
mkdir -p staging
mmetl transform confluence \
  --team myteam \
  --channel docs \
  --file confluence-export.zip \
  --output staging/import.jsonl \
  --attachments-dir staging

(cd staging && zip -r ../confluence-docs.zip import.jsonl import-manifest.json data)

mmctl docs import confluence-docs.zip
mmctl docs import status <job-id>
```

The `--channel` flag in this example is legacy v1 behavior. In the v2 contract it should either be removed or explicitly repurposed as an access-policy input; it must not be used as a Docs Space backing channel.

