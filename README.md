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

### Slack guest users

Slack marks guests with the `is_restricted` (multi-channel guest) or
`is_ultra_restricted` (single-channel guest) flags on the user object. Control
how they are migrated with `transform slack --guest-handling`:

- `guest` (default) — migrate them as Mattermost guests
  (`system_guest`/`team_guest`/`channel_guest`). Highest fidelity. **This only
  behaves correctly if the destination server has Guest Accounts licensed
  (Professional/Enterprise) and enabled (`GuestAccountsSettings.Enable`).** The
  import will not fail without it, but the accounts won't behave as guests —
  use `user` mode for targets without guest licensing.
- `user` — migrate them as regular Mattermost users. Works everywhere, but
  grants guests full user permissions.
- `skip` — drop guest users entirely, along with their memberships and
  authored posts.

A guest's team and channel memberships mirror their Slack access scope: they
are only added to the channels they belonged to in the Slack export. Mattermost
requires a guest to hold the guest role at the system, team, and channel level
consistently, so a `guest`-mode guest with no channel memberships in the Slack
export is imported as a regular member instead, and a warning is logged.

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