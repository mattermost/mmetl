# mmetl

The Mattermost ETL is a tool to transform an export file from a given
set of providers into a Mattermost compatible export file.

## Installation

To install the project in your `$GOPATH`, just run:

```sh
go install github.com/mattermost/mmetl@latest
```

## Usage

The tool is self documented, so you can run it with with the `--help`
flag and it will print the available subcommands and the options for
the current command:

```sh
$ mmetl --help
```

You can also check the CLI generated documentation under [mmetl](docs/cli/mmetl.md).

## Rocket.Chat Migration

### Prerequisites

- [`mongodump`](https://www.mongodb.com/docs/database-tools/mongodump/) to export the Rocket.Chat database
- A Mattermost instance to import into

### Quick Start

1. Export your Rocket.Chat MongoDB database:

```sh
mongodump --uri="mongodb://localhost:3001/meteor" --out=/backup/rc
```

2. Transform the export into a Mattermost bulk import file:

```sh
mmetl transform rocketchat \
  --dump-dir /backup/rc/meteor \
  --team myteam \
  --output mm_import.jsonl \
  --skip-attachments
```

3. To include file attachments (GridFS or filesystem):

```sh
mmetl transform rocketchat \
  --dump-dir /backup/rc/meteor \
  --team myteam \
  --output mm_import.jsonl \
  --attachments-dir ./data
```

4. Import into Mattermost using the [bulk import tool](https://docs.mattermost.com/manage/bulk-export-tool.html).

### Options

| Flag | Description |
|------|-------------|
| `--team` | Name of an existing Mattermost team to import into (required) |
| `--dump-dir` | Path to the `mongodump` output directory containing `.bson` files (required) |
| `--output` | Output JSONL file path (default: `bulk-export.jsonl`) |
| `--attachments-dir` | Directory for extracted file attachments (default: `data`) |
| `--uploads-dir` | Path to Rocket.Chat FileSystem uploads directory (if not using GridFS) |
| `--skip-attachments` | Skip file attachment extraction |
| `--skip-empty-emails` | Ignore users with empty email addresses |
| `--default-email-domain` | Generate email addresses for users missing one (e.g. `myorg.com`) |
| `--debug` | Enable debug logging |

See the full CLI reference at [docs/cli/mmetl_transform_rocketchat.md](docs/cli/mmetl_transform_rocketchat.md).

## Development

### Updating Documentation

The CLI documentation in `docs/cli/` is automatically generated from the Cobra command definitions. 

To regenerate the documentation after making changes to commands:

```sh
make docs
```

To verify documentation is up-to-date (useful before committing):

```sh
make docs-check
```

**Note:** The CI pipeline will automatically check if documentation is up-to-date on pull requests. If the check fails, run `make docs` and commit the updated files.