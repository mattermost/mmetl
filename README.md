# mmetl

The Mattermost ETL is a tool to transform an export from another platform into
a Mattermost-compatible bulk import file (JSONL) plus an attachments directory,
ready to be imported with `mmctl import`.

## Supported providers

| Provider | Input | Command |
| --- | --- | --- |
| Slack | export `.zip` | `mmetl transform slack` |
| Slack Enterprise Grid | export `.zip` | `mmetl grid-transform` |
| Rocket.Chat | `mongodump` directory | `mmetl transform rocketchat` |

## Installation

To install the project in your `$GOPATH`, just run:

```sh
go install github.com/mattermost/mmetl@latest
```

## Usage

The typical workflow is two steps — validate the export, then transform it:

```sh
# 1. Check the export for issues before transforming
mmetl check slack --file export.zip

# 2. Transform it into a Mattermost import file
mmetl transform slack --team myteam --file export.zip --output mm_export.jsonl
```

The tool is self-documented — run any command with `--help` to see its
subcommands and options:

```sh
mmetl --help
```

Full CLI reference is generated under [docs/cli](docs/cli/mmetl.md). For the
end-to-end Slack migration guide, see the
[Mattermost docs](https://docs.mattermost.com/administration-guide/onboard/migrate-from-slack.html).

## Development

See [AGENTS.md](AGENTS.md) for architecture, conventions, and the checks to run
after making changes.

### Documentation

The CLI docs in `docs/cli/` are generated from the Cobra command definitions.
After changing any command or flag, regenerate and commit them:

```sh
make docs        # regenerate docs/cli/
make docs-check  # verify they're up-to-date (CI enforces this on PRs)
```
