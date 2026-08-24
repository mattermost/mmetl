# Agents

`mmetl` is a Go/Cobra CLI that transforms exports from other platforms (Slack
zip, RocketChat `mongodump`) into a Mattermost bulk-import JSONL file plus an
attachments directory.

## Architecture

Pipeline is **Parse → Transform → Export** around a source-agnostic core:

- `commands/` — Cobra layer; one `transform_<provider>.go` / `check_<provider>.go`
  per provider. Command funcs read flags, open input, then drive a service.
- `services/<provider>/` — provider-specific Parse + Transform (`slack`,
  `rocketchat`, `slack_grid`).
- `services/intermediate/` — the source-agnostic core. `types.go` defines the
  `Intermediate` model; `export.go` defines `Exporter`, which emits the JSONL;
  `report.go` / `reasons.go` / `report_markdown.go` define the transform report
  that `Exporter` carries.
  **Each provider's `Transformer` embeds `intermediate.Exporter`** — so adding a
  provider means writing a parser + transformer that fill the Intermediate
  model; the export side is shared.
- `internal/tools/docgen/` — generates `docs/cli/` from the Cobra tree; do not
  hand-edit `docs/cli/`.

## Conventions

- Any bot user in the source requires `--bot-owner`, or the transform errors.
- Empty emails are invalid by default; relax with `--skip-empty-emails` or
  `--default-email-domain`.
- `intermediate.NowFunc` is the test seam for determinism — reuse it rather
  than adding new globals. Nothing in the transform path calls `os.Exit`;
  failures are returned as errors so the transform report still gets written.
- Every transform run writes a `Report` (`services/intermediate/report.go`)
  alongside the bulk import file. When a code path skips a source entity or
  changes it on the way in, record it: declare a `Reason` in `reasons.go` (the
  shared one, or the provider's) and call `Skip`/`Note` on the entity's section
  of `t.Report` instead of logging directly — recording is what emits the log
  line.

## After making code changes

Always run before considering work complete:

1. `make check-style` — lint/style (this is what `build`/`install` gate on)
2. `make test` — full suite. **Requires a running Docker daemon**: the
   `*_e2e_test.go` files in `commands/` are not build-tagged and spin up real
   Mattermost + Postgres containers via testcontainers. For fast unit tests,
   target a service package directly (e.g. `go test ./services/rocketchat/`).
3. If you changed any command or flag, also run `make docs` and commit the
   regenerated `docs/cli/*.md` (CI enforces this via `make docs-check`).
