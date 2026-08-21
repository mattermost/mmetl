# Proposal: import-side summary for `mmctl`/`mattermost-server`

This is a hand-off design doc, not an implementation — it belongs to the
`mattermost/mattermost` repo (`server/` and `server/cmd/mmctl/`), not to mmetl.
It records the investigation and design already worked out so that work
doesn't need to be re-derived when picked up.

## Context

mmetl's `transform` commands now write a `transform-<provider>-summary.md`
into the working directory, alongside the transform log file and independent
of `--output` (see `services/intermediate/summary.go`, `warning_collector.go`,
`render.go`, and the wiring in `commands/transform.go` /
`commands/transform_rocketchat.go`). It reports, per entity, how many were
**Transformed** and **Skipped** during the export step, plus a **Notes**
section explaining why.

The goal is a symmetric report on the **import** side: after running `mmctl
import process`, produce a sibling summary with the same entity rows, renamed
**Imported**/**Skipped**/**Failed**, so an operator can diff the two files and
confirm the migration was complete — this was the original ask that motivated
the mmetl-side work.

## The entity rows (must match mmetl's exactly)

Public channels · Private channels · Group channels · Direct channels ·
Users · Bots · Posts · Direct posts · Replies · Reactions · Attachments ·
Channel memberships

These come from `services/intermediate/summary.go`'s `entityRows` — treat that
as the source of truth if it changes.

## What exists today

Checked against `mattermost/mattermost` `master`, current as of this writing.
The module version this repo pins in `go.mod` was a stale pre-`v11.7`
snapshot. Verify against the current `master` or latest release again before
implementing.

- **`job.Data` has no per-entity counts.** For an `import_process` job it only
  ever contains `import_file`, `local_mode`, `extract_content`, and — only on
  failure — `line_number` (`server/channels/jobs/import_process/worker.go`).
- **Import logs aren't job-scoped.** `MakeWorker` builds one `appContext` from
  the bare server logger, outside the per-job closure
  (`server/channels/jobs/import_process/worker.go`), and that's what's
  threaded through `BulkImportWithPath` → `bulkImport` → `bulkImportWorker`
  (`server/channels/app/import.go`). The job-scoped logger `SimpleWorker.DoJob`
  builds (`worker.logger.With(JobLoggerFields(job)...)`,
  `server/channels/jobs/base_workers.go`) is only used for panic handling and
  the final success/failure log line — never for the import itself. So today,
  only "did job X succeed/fail, and at which line" is recoverable from logs;
  nothing below that granularity is attributable to a specific job.
- **Most entities don't log an outcome at all.** `importChannel`/`importUser`/
  `importTeam`/`importBot` all upsert silently (get-or-create, then always
  update) — there's no "created" vs "updated" signal anywhere. Posts/direct
  posts are matched by `(channelId, createAt, message)` and overwritten in
  place, not duplicated. The only skip cases logged anywhere in the importer
  today: an emoji name colliding with a system emoji, an attachment
  content-match dedup, an oversized image upload, and a direct/group channel
  with an invalid member count (`server/channels/app/import_functions.go`,
  `server/channels/app/import.go`'s `stopOnError`).
- **The importer is fail-fast.** Any error not in the four cases above aborts
  the whole run from that line onward (`stopOnError` in `import.go`). This is
  a deliberate safety property (no partially-consistent import) — the summary
  feature should report against it, not try to change it. A fatal error means
  "Imported" reflects progress up to the abort point, not the whole file.
- **`GET /api/v4/logs/download`** (`api/v4/source/logs.yaml`) dumps the entire
  configured log file as plain text — no job filtering, no pagination, and it
  only has anything useful when the deployment logs to a local file (not
  syslog/Loki/Vector, which many production setups use instead).

## Recommended design

**Don't parse logs. Accumulate counters directly into `job.Data` during
`bulkImportWorker`/`import_functions.go`, mirroring the exact entity-tagging
pattern used in mmetl:**

1. Add per-entity `created`/`updated`/`skipped` counters, incremented at the
   same call sites already identified above (`importChannel`, `importUser`,
   `importBot`, `importDirectChannel`, `importMultiplePostLines`,
   `importMultipleDirectPostLines`, `importEmoji`'s four skip cases). Each
   site already knows unambiguously which row it's touching and whether it
   created, updated, or skipped — this is less discovery work than the mmetl
   side needed, since there's no thread-cascade/root-vs-reply ambiguity here
   (import processes already-resolved `post`/`direct_post`/`reply` lines).
2. On completion (success **or** fail-fast abort), serialize the counters plus
   `status` (`completed`/`failed`) and, on failure, `failed_at_line` into a
   single `job.Data["import_summary"]` JSON blob. `job.Data` is a Postgres
   `jsonb` column (`server/channels/db/migrations/postgres/000060_upgrade_jobs_v6.0.up.sql`),
   so there's no meaningful size ceiling — one JSON string is fine, no need to
   flatten into many `StringMap` keys.
3. `mmctl summary <jobID>` (or `mmctl import summary`) becomes a thin
   `GetJob(ctx, jobID)` read — that API already exists and is already used by
   `mmctl import job show` (`server/cmd/mmctl/commands/import.go`) — followed
   by unmarshalling `Data["import_summary"]` and rendering the same
   table+Notes markdown format mmetl produces (reuse the shape, not
   necessarily the Go code, since it lives in a different module).
4. Support multiple job IDs (`mmctl summary <jobID> [<jobID> ...]`), summing
   counters across them, because real migrations are often split into several
   import jobs (per-team files, or a retry after fixing a bad row). Report
   each job's `status`/`failed_at_line` individually alongside the aggregate
   so a partial job doesn't silently blend into what looks like a clean total.

### Why this over parsing `/api/v4/logs/download`

Both options need the same new counting logic added to the importer — the
only choice is where the counts end up. `job.Data` avoids: the job-id log
tagging fix that would also be required for log-parsing to work at all, the
file-based-logging-only limitation of the download endpoint, log
retention/rotation risk, and an extra download+parse round trip in `mmctl`.
The counting logic itself is required either way, so this isn't extra work.

### Known limitation to carry over into that design

Because the importer is fail-fast, "Skipped" is not comparable in meaning to
mmetl's "Skipped" in one respect: on the import side, a fatal error stops
counting entirely rather than skipping just that one entity. Make sure the
rendered table/Notes surface `status: failed` and `failed_at_line` prominently
(not just as an extra column) so "483/500 posts imported" doesn't read as "17
posts were individually skipped" when the real story is "the run aborted
partway through."

## Explicitly out of scope for this proposal

- Changing the importer from fail-fast to continue-on-error. That's a much
  larger behavior change to what "a successful import" means, and would need
  its own design/review — not bundled into adding counters.
- Any change to mmetl. This document exists in the mmetl repo only so the
  investigation travels with the feature it complements; the implementation
  belongs entirely in `mattermost/mattermost`.
