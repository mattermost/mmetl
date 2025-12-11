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

## Commands

For detailed documentation on all available commands, see the [CLI reference](docs/cli/mmetl.md).
s documentation](docs/cli/mmetl_sync-import-users.md) for detailed usage information.
