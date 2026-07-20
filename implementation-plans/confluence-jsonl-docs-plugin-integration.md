# Confluence JSONL → Mattermost Docs integration

> **Status:** Producer and CRUD foundations are available; the Docs import
> consumer is not. `mmetl` emits the v2 bundle, Mattermost core Space-channel
> support landed in
> [mattermost#37321](https://github.com/mattermost/mattermost/pull/37321), and
> Docs Space/Page CRUD landed in
> [mattermost-plugin-docs#4](https://github.com/mattermost/mattermost-plugin-docs/pull/4).
> The remaining work is the durable Docs plugin import API/job and its
> end-to-end validation.

## Bottom line

The v2 bundle produced by `mmetl` cannot yet be imported because the Docs plugin
has no import API/job. It must not be sent to Mattermost's standard bulk
importer, which remains unaware of Docs-specific JSONL entities.

The recommended architecture is:

```text
Confluence export
  → mmetl versioned ZIP bundle
  → Docs plugin admin import API/job
      → DOCS_Space / DOCS_Page / import mappings
      → threaded Mattermost posts in the Space backing channel for comments
      → Mattermost plugin APIs for channels, users, membership, and files
```

The Docs plugin owns bundle parsing, validation, import jobs, idempotency,
hierarchy, threaded comments, attachments, and link resolution. Mattermost core
owns the opaque `ChannelTypeSpace` primitive and posts, but remains unaware of
Docs JSONL line types.

## Current incompatibilities

- `mmetl` emits custom v2 `space`, `page`, `page_comment`, and
  `resolve_space_placeholders` lines
  ([export.go](../services/confluence/export.go)).
- Docs `master` now has Space/Page CRUD and creates a `ChannelTypeSpace` backing
  channel, but it still has no bundle import API, durable import job, import
  mappings, or complete attachment import path.
- Mattermost bulk import has no fields for these entities and rejects unknown
  line types ([import_types.go](/Users/willyfrog/Proyectos/mattermost/server/channels/app/imports/import_types.go:15),
  [import.go](/Users/willyfrog/Proyectos/mattermost/server/channels/app/import.go:398)).
- The old [`wiki-poc` importer](/Users/willyfrog/.supacode/repos/mattermost/wiki-poc/server/channels/app/import_wiki_functions.go:74)
  remains useful as an algorithm and test reference, but writes obsolete core
  Wiki/Page models rather than plugin tables and Space-channel posts.

## Changes required in `mmetl`

> **Status:** The producer work is substantially complete; see
> [confluence-docs-contract-alignment.md](confluence-docs-contract-alignment.md).
> Input is Confluence Cloud CSV, output is the v2 single-Space bundle, identity
> metadata is preserved, and restrictions are detected. The legacy channel
> surface is fully removed: no `--channel` flag, no `target.channel` manifest
> field, no channel constructor arguments, and no server-side channel
> validation — mmetl never names the Space backing channel. Bundle `team`
> values are advisory metadata; the import request is authoritative. The only
> material data blocker still on the producer side is verification of the
> attachment byte layout against a real attachment-bearing CSV export.

### 1. Freeze and version the contract

- v2 is defined and documented as `space`, `page`, `page_comment`, and
  `resolve_space_placeholders`; there is no shipped v1 consumer to preserve.
- The bundle-level source namespace scopes numeric page/comment IDs and space
  keys across Confluence instances.
- The import request is authoritative for target team and access policy. Bundle
  `team` values are advisory metadata: record them for audit and warn on a
  mismatch, but never route the import from them.
- The bundle never carries or selects the Space backing channel. The Docs plugin
  creates its own `ChannelTypeSpace` channel in the requested team.

### 2. Fix comment scoping

Resolve every comment through the current import job's page and comment mapping
tables. Never perform a global lookup by bare `page_import_source_id` or comment
source ID. The single-Space bundle and source namespace bound collisions, while
the job mapping supplies the local page/post IDs needed for idempotent retries.
`props.confluence_author_account_id` remains the attribution key and is
orthogonal to scoping.

### 3. Produce a complete bundle

Implemented by `--bundle`; the archive contains:

```text
import.jsonl
import-manifest.json
data/<page-id>/<attachment>
```

The importer should reject archives that do not contain the JSONL and manifest,
and verify the manifest checksums before writing anything.

### 4. Align validation with Docs

- The producer warns on the Docs Body/Props limits, validates TipTap JSON, emits
  `update_at`, and surfaces source users in the manifest.
- `SearchText` is importer-derived from TipTap, so its authoritative 2 MiB check
  belongs in the plugin preflight/import-specific page method.
- The importer must validate mapped/fallback usernames against the target server
  and preserve source timestamps through import-specific methods; interactive
  CRUD methods overwrite them.

### 5. Fix fidelity blockers

- Attachment metadata and `CONF_ATTACHMENT` → `CONF_FILE` normalization are
  implemented. The on-disk CSV attachment layout remains unverified; keep both
  placeholder forms supported until a real fixture proves extraction.
- One bundle equals one Space; migrating N spaces means N independent jobs.
- View restrictions are detected, warned, included in the manifest, and can fail
  production with `--fail-on-restricted`. They are not converted into an ACL.

### 6. Add tests

Parser and exact-schema producer tests exist. Remaining integration coverage is
plugin-side: archive/checksum validation, source-ID collisions, threaded comment
scoping, hierarchy, retries/re-imports, membership, attachments, and one full
`mmetl → Docs` fixture.

## Changes required in `mattermost-plugin-docs`

Space/Page CRUD from mattermost-plugin-docs#4 is now on `master`: it creates an
opaque `ChannelTypeSpace` backing channel and exposes scoped Space/Page APIs.
The remaining work starts from that baseline. There is still no import API
(`/imports`) or durable import job.

The plugin should ingest `mmetl`'s bundle artifacts directly: the manifest
`users` list as the source-user → Mattermost-username proposal, and each
entity's `props.confluence_author_account_id` for attribution, rather than
re-deriving identity from raw content. The target server must validate the
proposed usernames before writes.

### 1. Add durable import persistence

Suggested tables:

- `DOCS_ImportJob`: state, phase, actor, target, checksum, counts, errors, timestamps.
- `DOCS_ImportEntity`: `(source_namespace, entity_type, external_id, scope_id) → local_id`.
- Page-file ownership tables. Comments use Mattermost posts in the Space
  backing channel; their source IDs still use `DOCS_ImportEntity` mappings.

Use explicit source-ID columns/tables instead of repeatedly scanning JSONB Props. Confirmed necessary: `DOCS_Page` today has a `Props` JSONB column but **no source-ID column** — the closest field, `OriginalId` ([page.go:61](/Users/willyfrog/Proyectos/mattermost-plugin-docs/server/model/page.go:61)), is a version-snapshot pointer, not an external import key. So without new mapping tables the importer would have to scan `Props` for `import_source_id`. Seed `DOCS_ImportEntity` from `mmetl`'s manifest `users` list (for user identities) plus the per-line `*_import_source_id` fields (for spaces/pages/comments).

### 2. Add import-specific services

Normal interactive `CreatePage` does not accept source props or timestamps ([page.go](/Users/willyfrog/Proyectos/mattermost-plugin-docs/server/app/page.go:29)). Add dedicated methods such as:

- `ImportSpace`
- `ImportPage`
- `ImportComment`
- `AttachImportedFile`
- `FinalizeImport`

These methods should preserve source metadata atomically, avoid per-entity
notifications, derive and validate `SearchText` from TipTap, and enforce the
existing depth-10 hierarchy limit. `ImportComment` creates a Mattermost post in
the Space backing channel and records the source-comment → post mapping; it does
not write a plugin-owned comment row.

### 3. Implement a restartable job

1. Verify ZIP limits, paths, manifest, and checksum.
2. Parse and preflight without writes. Treat bundle `team` as advisory
   metadata; the request's target team is authoritative, and a mismatch is
   recorded as a warning/audit detail.
3. Resolve the requested team, users, source IDs, hierarchy, and capabilities;
   require Mattermost 11.10+ with `EnableDocs=true`.
4. Create Spaces/backing channels through the Docs/plugin API, persist the
   returned channel ID immediately, and compensate for orphan channels.
5. Insert pages topologically in bounded transactions.
6. Import comments and attachments.
7. Repair hierarchy and resolve links.
8. Compare final counts with the manifest and mark the job complete.

Retries should resume from committed phases; re-running the same bundle should be a no-op.

### 4. Import comments as Space-channel post threads

Comments are Mattermost posts in the Space's `ChannelTypeSpace` backing
channel, not plugin-owned comment rows:

- A top-level Confluence page comment becomes a root post carrying the local
  Docs page ID and source metadata in Props.
- A reply resolves `parent_comment_import_source_id` through the job mapping and
  is created in the corresponding Mattermost thread (`RootId`). Preserve the
  immediate source-parent ID in Props when Confluence nesting is deeper than
  Mattermost's flat reply model.
- Record every source comment ID → post ID mapping in `DOCS_ImportEntity` before
  dependent replies run. Retries reuse the mapping and never duplicate posts.
- Preserve author, source timestamps, page ID, inline anchor, resolved state,
  and `confluence_author_account_id` in the post/Props contract.
- Use an import-specific post path that suppresses notifications and ordinary
  interactive side effects. Space membership supplies post authorization, and
  core excludes Space-channel posts from normal chat unread/read-state queries.

Do not silently discard comments or report a full import when a thread cannot
be reconstructed.

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

JSONL contains authors but no reader membership. The import request must specify
the authoritative target team and an access policy, such as an existing
membership-source channel or an explicit member list. Bundle `team` is advisory
metadata only. Create one backing Space channel per bundle and copy/sync the
authorized membership; default to private/admin-only when restrictions are
unknown.

`DOCS_Space.ChannelId` ([space.go](/Users/willyfrog/Proyectos/mattermost-plugin-docs/server/model/space.go))
is the durable linkage and must be persisted immediately after creation. Space
channels are intentionally absent from generic get/list/search APIs, so recovery
must use the stored ID and plugin `GetChannelOfType`, never channel discovery by
name. Add/remove members through the plugin/Docs APIs.

Standard channel ABAC, privacy conversion, schemes, and move operations do not
govern `ChannelTypeSpace`. Space membership is the available channel-level
authorization boundary; finer page restrictions require a future plugin-owned
ACL. View-restricted Confluence pages must therefore remain warning/fail items,
not be treated as faithfully migrated.

## Mattermost baseline and constraints

Core support landed in mattermost#37321 (merge `5f7f967a`):

- `ChannelTypeSpace = "S"` and `EnableDocs` (default false).
- Plugin `RestoreChannel` and `GetChannelOfType` APIs, documented for Mattermost
  11.10+.
- Plugin lifecycle and member operations support Space channels, while generic
  `/channels` creation/get/list/search/export/analytics paths intentionally hide
  or reject them.
- Space membership participates in authorization, but ordinary chat unread,
  read-state, lifecycle posts, and channel events are suppressed where
  appropriate.

The importer must preflight Mattermost 11.10+ and `EnableDocs=true`, create the
backing channel through the Docs/plugin path, and persist its returned ID. It
must not use generic channel REST APIs to discover or manage the Space.

The standard bulk importer still rejects unknown Docs line types with a 400
([import.go](/Users/willyfrog/Proyectos/mattermost/server/channels/app/import.go)),
so Docs JSONL must never reach it. No additional core import parsing is planned.
Core changes may still be needed if the archive/file design requires generic
durable plugin uploads or first-class Page-owned `FileInfo`.

Follow-up: a post-merge review noted that plugin `RestoreChannel` does not emit
the equivalent core audit record. Until that is fixed upstream, the Docs import
job must audit compensation/restore operations itself.

Do not extend standard `mmctl import` with Docs-specific parsing. Optionally add:

```text
mmctl spaces import <bundle.zip>
mmctl spaces import validate <bundle.zip>
mmctl spaces import status <job-id>
```

**Naming note:** an `mmctl docs` command already exists — it generates mmctl's
own cobra documentation
([docs.go](/Users/willyfrog/Proyectos/mattermost/server/cmd/mmctl/commands/docs.go)).
Use a non-conflicting command such as `mmctl spaces`; it should call the plugin
API rather than parse Docs entities in core.

Docs search and UI belong primarily in the plugin. Mattermost global search does not index `DOCS_Page.SearchText`; AI/RAG integration is a separate Docs ↔ Mattermost AI project.

## Rollout sequence

1. **Done:** land Docs CRUD, core `ChannelTypeSpace`, and the mmetl v2 contract.
2. Implement import jobs/mapping tables plus Spaces, pages, hierarchy, target
   team authority, and idempotency.
3. Import `page_comment` records as Space-channel post threads; add attachments,
   access policy, and final link resolution.
4. Add upload/status APIs and optional `mmctl spaces import` commands.
5. Run a three-repository E2E test covering counts, hierarchy, links, threaded
   comments, authors, membership, restrictions, retries, and checksums.
6. Enable production Confluence imports only after those tests pass.

A page-only milestone is acceptable only if unsupported comments, attachments, and restricted pages are rejected or require an explicit `allow_partial` acknowledgement. The importer must not report full success while silently dropping them.

## End-state operator flow

```bash
mmetl transform confluence \
  --team myteam \
  --file confluence-export.zip \
  --bundle confluence-docs.zip

mmctl spaces import --team myteam confluence-docs.zip
mmctl spaces import status <job-id>
```

The import command's `--team` is authoritative. The bundle's `team` value is an
advisory record of producer/operator intent; a mismatch is warned and audited
but does not redirect the import. No channel argument is accepted: the Docs
plugin creates and owns the Space backing channel.
