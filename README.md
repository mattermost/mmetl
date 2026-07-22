# mmetl

The Mattermost ETL is a tool to transform an export from another platform into
a Mattermost-compatible bulk import file (JSONL) plus an attachments directory,
ready to be imported with `mmctl import`.

## Supported providers

| Provider | Input | Command |
| --- | --- | --- |
| Slack | export `.zip` | `mmetl transform slack` |
| Slack Enterprise Grid | export `.zip` | `mmetl grid-transform` |
| RocketChat | `mongodump` directory | `mmetl transform rocketchat` |

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

### RocketChat guest users

RocketChat marks guests with a `guest` role (not a distinct user type). Control
how they are migrated with `transform rocketchat --guest-handling`:

- `guest` (default) — migrate them as Mattermost guests
  (`system_guest`/`team_guest`/`channel_guest`). Highest fidelity. **This only
  behaves correctly if the destination server has Guest Accounts licensed
  (Professional/Enterprise) and enabled (`GuestAccountsSettings.Enable`).** The
  import will not fail without it, but the accounts won't behave as guests — use
  `user` mode for targets without guest licensing.
- `user` — migrate them as regular Mattermost users. Works everywhere, but
  grants guests full user permissions.
- `skip` — drop guest users entirely, along with their memberships and authored
  posts.

Users whose RocketChat type is neither `user` nor `bot` (for example `app`
accounts like `rocket.cat`) are always skipped, and any memberships, posts, and
reactions referencing them are dropped so the import stays referentially
consistent.

Full CLI reference is generated under [docs/cli](docs/cli/mmetl.md). For the
end-to-end Slack migration guide, see the
[Mattermost docs](https://docs.mattermost.com/administration-guide/onboard/migrate-from-slack.html).

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
  authored posts/reactions.

A guest's team and channel memberships mirror their Slack access scope: they
are only added to the channels they belonged to in the Slack export. Mattermost
can only scope a guest's access through public/private channel membership, so
in `guest` mode a guest with no public/private channel in the Slack export
(for example, one present only in a DM or MPIM) cannot be validly imported as
a guest. Rather than silently promoting them to a full member, they — along
with their memberships and authored posts — are skipped, and a warning is
logged. Use `--guest-handling=user` if you'd rather those guests be imported
as regular members instead of skipped.

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
