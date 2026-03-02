# AGENTS.md — mmetl

## Project Overview

mmetl (Mattermost ETL) is a CLI tool written in Go that transforms export files from
other messaging platforms (currently Slack and Slack Enterprise Grid) into Mattermost
bulk import format (JSONL). It follows an Extract-Transform-Load pipeline: parse the
source zip, convert to intermediate types, then export Mattermost-compatible JSONL.

Module: `github.com/mattermost/mmetl` — Go 1.24+

## Repository Structure

```
mmetl.go                  # Entry point (calls commands.Execute)
commands/                 # Cobra CLI command definitions (root, transform, check, version)
services/slack/           # Core Slack export ETL: parse, intermediate, export, download
services/slack_grid/      # Slack Enterprise Grid export handling
internal/tools/docgen/    # CLI documentation generator
docs/cli/                 # Auto-generated CLI reference (do not edit manually)
```

## Build / Lint / Test Commands

### Build
```sh
make build          # Lint + build binary with version ldflags
go build            # Quick build without linting or version info
```

### Lint
```sh
make golangci-lint  # Run golangci-lint (must be installed; v2 config)
make gofmt          # Check formatting with gofmt -d -s
make check-style    # Alias for golangci-lint
```

### Test — All
```sh
make test           # go test -race -v ./... -count=1
```

### Test — Single Test
```sh
go test -race -v -run TestFunctionName ./services/slack/ -count=1
go test -race -v -run TestFunctionName/SubtestName ./services/slack/ -count=1
```

### Test — Single Package
```sh
go test -race -v ./services/slack/ -count=1
go test -race -v ./commands/ -count=1
```

### Other Useful Targets
```sh
make tidy           # go mod tidy
make verify-gomod   # go mod download && go mod verify
make docs           # Regenerate CLI docs (docs/cli/)
make docs-check     # Verify docs are up-to-date (CI enforces this)
```

## Code Style Guidelines

### Formatting
- **gofmt with simplify** (`-s`) is enforced. CI runs `gofmt -d -s`.
- **goimports** is enabled via golangci-lint — imports are auto-grouped and sorted.
- Use `any` instead of `interface{}` (auto-rewritten by gofmt config).

### Import Order
Imports are grouped in three blocks separated by blank lines:
1. Standard library (`archive/zip`, `encoding/json`, `fmt`, `os`, etc.)
2. Third-party packages (`github.com/pkg/errors`, `github.com/sirupsen/logrus`, etc.)
3. Internal/project packages (`github.com/mattermost/mmetl/...`, `github.com/mattermost/mattermost/...`)

```go
import (
    "encoding/json"
    "fmt"
    "os"

    "github.com/pkg/errors"
    log "github.com/sirupsen/logrus"

    "github.com/mattermost/mmetl/services/slack"
    "github.com/mattermost/mattermost/server/public/model"
)
```

### Naming Conventions
- **Packages**: lowercase, single word (`slack`, `commands`).
- **Types**: PascalCase. Prefix intermediate data types with `Intermediate` (e.g.,
  `IntermediateChannel`, `IntermediateUser`, `IntermediatePost`). Slack source types
  are prefixed with `Slack` (e.g., `SlackUser`, `SlackChannel`).
- **Exported functions**: PascalCase; methods on `*Transformer` for transform operations.
- **Unexported functions**: camelCase (e.g., `truncateRunes`, `downloadInto`).
- **Constants**: `UPPER_SNAKE_CASE` for numeric limits (e.g., `POST_MAX_ATTACHMENTS`),
  `camelCase` for internal string constants (e.g., `attachmentsInternal`).
- **Struct tags**: use `json:"snake_case"` for all exported struct fields.
- **Errors as variables**: `var ErrOverlapNotEqual = errors.New(...)` — PascalCase with `Err` prefix.

### Error Handling
- Use `github.com/pkg/errors` for wrapping: `errors.Wrap(err, "context")`,
  `errors.Errorf("message: %w", err)`.
- For new sentinel errors, use `errors.New` from the standard library.
- Use `fmt.Errorf("context: %w", err)` in newer code (see `download.go`).
- CLI commands return errors from `RunE`; the root command prints and exits on error.
- For fatal user-facing errors in transform logic, log the error and call `exitFunc(1)`
  (this is a package-level variable set to `os.Exit` but overridable in tests).

### Logging
- Use `github.com/sirupsen/logrus` aliased as `log`.
- The `Transformer` struct holds a `log.FieldLogger` for contextual logging.
- Use `logger.Info`, `logger.Warn`, `logger.Warnf`, `logger.Error`, `logger.Debugf`.
- Warnings are used for non-fatal data issues (truncation, missing fields).
- Debug logs include struct dumps (`%+v`) for troubleshooting.

### Testing Patterns
- Use `github.com/stretchr/testify` — `require` for fatal assertions, `assert` for
  non-fatal assertions.
- Use subtests with `t.Run("description", func(t *testing.T) { ... })`.
- Prefer table-driven tests with maps or slices of test case structs.
- Unit tests use the same package (e.g., `package slack`) for access to unexported
  functions. E2E tests use the external test package (e.g., `package commands_test`).
- Use `os.MkdirTemp` / `t.TempDir()` for temp directories; clean up with `defer`.
- Use `net/http/httptest` for HTTP mock servers.
- Mock `os.Exit` via package-level `exitFunc` variable for testing fatal paths.
- Tests run with `-race -v -count=1` — no caching.

### CLI Pattern (Cobra)
- Commands are defined as package-level `var` Cobra commands in `commands/`.
- Flags are registered in `init()` functions; required flags use `MarkFlagRequired`.
- Command logic lives in `RunE` handler functions named `<command>CmdF`.
- Subcommands are added via `AddCommand` in `init()`.

### Struct Design
- Intermediate data types carry all fields needed for Mattermost import.
- `Sanitise` methods (note British spelling) validate and truncate fields to
  Mattermost model limits.
- The `Transformer` struct holds team name, intermediate data, and logger.

## Linter Configuration (golangci-lint v2)

Enabled linters: `bidichk`, `errcheck`, `govet`, `ineffassign`, `makezero`,
`misspell`, `staticcheck`, `unconvert`, `unqueryvet`, `unused`, `whitespace`.

Formatters: `gofmt` (with simplify + `interface{}` -> `any` rewrite), `goimports`.

Exclusions: generated code is lax, comment-related and std-error-handling presets
are excluded.

## CI Pipeline

CI runs on PRs and pushes to `master`:
1. `golangci-lint` (v2.6.2)
2. `make gofmt` — formatting check
3. `make test` — all tests with race detector
4. `make docs-check` — ensures CLI docs are up-to-date

If you add or modify a Cobra command, run `make docs` and commit the generated
files in `docs/cli/`.

## Key Dependencies

| Package | Purpose |
|---------|---------|
| `github.com/mattermost/mattermost/server/public/model` | Mattermost data model types and constants |
| `github.com/mattermost/mattermost/server/v8/channels/app/imports` | Import line types for JSONL export |
| `github.com/pkg/errors` | Error wrapping (`errors.Wrap`, `errors.Errorf`) |
| `github.com/sirupsen/logrus` | Structured logging (aliased as `log`) |
| `github.com/spf13/cobra` | CLI framework |
| `github.com/stretchr/testify` | Test assertions (`require`, `assert`) |
| `golang.org/x/text` | Unicode normalization (NFC/NFKD) |
