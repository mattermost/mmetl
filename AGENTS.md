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
  `Intermediate` model; `export.go` defines `Exporter`, which emits the JSONL.
  **Each provider's `Transformer` embeds `intermediate.Exporter`** — so adding a
  provider means writing a parser + transformer that fill the Intermediate
  model; the export side is shared.
- `internal/tools/docgen/` — generates `docs/cli/` from the Cobra tree; do not
  hand-edit `docs/cli/`.

## Conventions

- Any bot user in the source requires `--bot-owner`, or the transform errors.
- Empty emails are invalid by default; relax with `--skip-empty-emails` or
  `--default-email-domain`.
- `intermediate.ExitFunc` / `NowFunc` are test seams for determinism — reuse
  them rather than adding new globals.

## After making code changes

Always run before considering work complete:

1. `make check-style` — lint/style (this is what `build`/`install` gate on)
2. `make test` — full suite. **Requires a running Docker daemon**: the
   `*_e2e_test.go` files in `commands/` are not build-tagged and spin up real
   Mattermost + Postgres containers via testcontainers. For fast unit tests,
   target a service package directly (e.g. `go test ./services/rocketchat/`).
3. If you changed any command or flag, also run `make docs` and commit the
   regenerated `docs/cli/*.md` (CI enforces this via `make docs-check`).
