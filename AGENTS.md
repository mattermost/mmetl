# AGENTS.md — mmetl

## Project Overview

mmetl (Mattermost ETL) is a CLI tool written in Go that transforms export files from
other messaging platforms (Slack, Slack Enterprise Grid, Rocket.Chat) into Mattermost
bulk import format (JSONL). It follows an Extract-Transform-Load pipeline: parse the
source archive, convert to intermediate types, then export Mattermost-compatible JSONL.

Module: `github.com/mattermost/mmetl` — Go 1.24

## Repository Structure

```
mmetl.go                    # Entry point (calls commands.Execute)
commands/                   # Cobra CLI command definitions (root, transform, check, version, grid_transform)
services/intermediate/      # Shared intermediate types, sanitise logic, text splitting
services/slack/             # Slack export ETL: parse, intermediate, export, download
services/slack_grid/        # Slack Enterprise Grid export handling (embeds slack.Transformer)
services/rocketchat/        # Rocket.Chat mongodump ETL: BSON parse, transform, export
internal/tools/docgen/      # CLI documentation generator
docs/cli/                   # Auto-generated CLI reference (do not edit manually)
```

## Build / Lint / Test Commands

```sh
# Build
make build                  # Lint + build binary with version ldflags
go build                    # Quick build without linting or version info

# Lint
make golangci-lint          # Run golangci-lint (must be installed; v2 config)
make gofmt                  # Check formatting with gofmt -d -s
make check-style            # Alias for golangci-lint

# Test — all
make test                   # go test -race -v ./... -count=1

# Test — single test
go test -race -v -run TestFunctionName ./services/slack/ -count=1
go test -race -v -run TestFunctionName/SubtestName ./services/slack/ -count=1

# Test — single package
go test -race -v ./services/slack/ -count=1
go test -race -v ./commands/ -count=1

# Other targets
make tidy                   # go mod tidy
make verify-gomod           # go mod download && go mod verify
make docs                   # Regenerate CLI docs (docs/cli/)
make docs-check             # Verify docs are up-to-date (CI enforces this)
```

## Code Style Guidelines

### Formatting
- **gofmt with simplify** (`-s`) is enforced. CI runs `gofmt -d -s`.
- **goimports** is enabled via golangci-lint — imports are auto-grouped and sorted.
- Use `any` instead of `interface{}` (auto-rewritten by gofmt config).

### Import Order
Three groups separated by blank lines:
1. Standard library
2. Third-party packages
3. Internal/project packages (`github.com/mattermost/...`)

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
- **Packages**: lowercase, single word (`slack`, `commands`, `intermediate`).
  Exception: `slack_grid` uses an underscore.
- **Types**: PascalCase. Prefix intermediate data types with `Intermediate` (e.g.,
  `IntermediateChannel`, `IntermediateUser`). Source types are prefixed by platform:
  `Slack` (e.g., `SlackUser`), `RocketChat` (e.g., `RocketChatRoom`), or `RC` for
  sub-types (e.g., `RCEmail`, `RCFileRef`).
- **Exported functions**: PascalCase; methods on `*Transformer` for transform operations.
- **Unexported functions**: camelCase (e.g., `truncateRunes`, `downloadInto`).
- **Constants**: `UPPER_SNAKE_CASE` for numeric limits (e.g., `POST_MAX_ATTACHMENTS`),
  `camelCase` for internal string constants (e.g., `attachmentsInternal`).
- **Struct tags**: `json:"snake_case"` for Slack/intermediate types,
  `bson:"snake_case"` for Rocket.Chat types.
- **Sentinel errors**: `var ErrOverlapNotEqual = errors.New(...)` — PascalCase with `Err` prefix.

### Error Handling
- Use `github.com/pkg/errors` for wrapping: `errors.Wrap(err, "context")`,
  `errors.Wrapf(err, "format %s", arg)`.
- Use `fmt.Errorf("context: %w", err)` in newer code (rocketchat, download.go).
- CLI commands return errors from `RunE`; the root command prints and exits on error.
- For fatal user-facing errors in sanitise logic, log the error and call `ExitFunc(1)`
  (`intermediate.ExitFunc` is a package-level variable set to `os.Exit` but
  overridable in tests).

### Logging
- Use `github.com/sirupsen/logrus` aliased as `log`.
- Both `slack.Transformer` and `rocketchat.Transformer` hold a `log.FieldLogger`.
- Use `logger.Info`, `logger.Warn`, `logger.Warnf`, `logger.Error`, `logger.Debugf`.
- Each CLI command writes to its own log file (e.g., `transform-slack.log`,
  `check-rocketchat.log`) using `customLogFormatter` (JSON format with caller info).

### Testing Patterns
- Use `github.com/stretchr/testify` — `require` for fatal assertions, `assert` for
  non-fatal assertions.
- Use subtests: `t.Run("description", func(t *testing.T) { ... })`.
- Prefer table-driven tests with slices or maps of test case structs.
- Unit tests use the same package (e.g., `package slack`) for access to unexported
  functions. E2E tests use the external test package (e.g., `package commands_test`).
- Use `t.TempDir()` for temp directories (preferred), or `os.MkdirTemp` with
  `defer os.RemoveAll`.
- Use `net/http/httptest` for HTTP mock servers (see `download_test.go`).
- Mock `os.Exit` via `exitFunc`/`ExitFunc` package-level variables.
- Mark test helpers with `t.Helper()`.
- Tests run with `-race -v -count=1` — no caching.

### CLI Pattern (Cobra)
- Commands are defined as package-level `var` Cobra commands in `commands/`.
- Flags are registered in `init()` functions; required flags use `MarkFlagRequired`.
- Command logic lives in `RunE` handler functions named `<command>CmdF`.
- Subcommands are added via `AddCommand` in `init()`.

### Struct Design
- Shared intermediate types live in `services/intermediate/` (`IntermediateChannel`,
  `IntermediateUser`, `IntermediatePost`, `IntermediateReaction`, `Intermediate`).
- The `slack` package re-exports these as type aliases for backward compatibility.
- `Sanitise` methods (British spelling) validate and truncate fields to Mattermost
  model limits.
- Both `slack.Transformer` and `rocketchat.Transformer` hold `TeamName`,
  `*intermediate.Intermediate`, and `log.FieldLogger`.
- `GridTransformer` embeds `slack.Transformer` and adds grid-specific fields.

## Linter Configuration (golangci-lint v2)

Enabled linters: `bidichk`, `errcheck`, `govet`, `ineffassign`, `makezero`,
`misspell`, `staticcheck`, `unconvert`, `unqueryvet`, `unused`, `whitespace`.

Formatters: `gofmt` (with simplify + `interface{}` -> `any` rewrite), `goimports`.

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
| `go.mongodb.org/mongo-driver/bson` | BSON serialization for Rocket.Chat mongodump parsing |
| `golang.org/x/text` | Unicode normalization (NFC/NFKD) |
