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
| Confluence Cloud | XML backup `.zip` | `mmetl transform confluence` |

## Installation

To install the project in your `$GOPATH`, just run:

```sh
go install github.com/mattermost/mmetl@latest
```

## Usage

The typical workflow is two steps — dry-run the transform to catch issues,
then write the import file. `--dry-run` takes the same flags as a real run
(`--team` is required, and `--bot-owner` if the export contains bots). It
writes nothing and exits non-zero if problems are found, including missing
attachments that a real transform would skip.

```sh
# 1. Dry-run the transform to catch issues before writing output
mmetl transform slack --team myteam --file export.zip --dry-run
mmetl transform rocketchat --team myteam --dump-dir /tmp/rc-dump/meteor --dry-run

# 2. Transform it into a Mattermost import file
mmetl transform slack --team myteam --file export.zip --output mm_export.jsonl
```

Slack **Enterprise Grid** exports must be split first. `mmetl grid-transform` infers each workspace ID from the `teams/<name>/` folders already in the archive and writes one zip per workspace:

```sh
mmetl grid-transform --file slackexport.zip
# then, for each generated zip:
mmetl transform slack --team acme --file acme.zip --dry-run
mmetl transform slack --team acme --file acme.zip --output acme.jsonl
```

Pass `--team-map-path teams.json` only if you need to override the inferred mapping. The file is a JSON object: keys are Slack workspace IDs (the `team` field on posts, typically `T...`); values must match an existing folder under `teams/` in the export — they are not Mattermost team names.

The tool is self-documented — run any command with `--help` to see its
subcommands and options:

```sh
mmetl --help
```

### Confluence Cloud

Confluence is different from the other providers: it does not produce a
Mattermost bulk-import file. It produces an import bundle for the Mattermost
Docs plugin, which imports it through the plugin's own API.

The input is the ZIP from Confluence's **XML backup** (the one containing
`entities.xml` and `exportDescriptor.properties`), not a space export or a PDF
export. Each invocation exports exactly one space.

```sh
# 1. See which spaces the backup contains
mmetl transform confluence --file Confluence-export.zip --list-spaces

# 2. Check a space converts cleanly, writing nothing
mmetl transform confluence --file Confluence-export.zip --space ENG \
  --organization-id https://example.atlassian.net --team engineering --validate-only

# 3. Produce the bundle
mmetl transform confluence --file Confluence-export.zip --space ENG \
  --organization-id https://example.atlassian.net --team engineering
```

`--space` accepts the space's numeric source ID, its key, or its name; an
ambiguous or unknown value fails and prints the valid spaces.

`--organization-id` identifies the Confluence site. **Use the same value for
every export from the same site.** It scopes every source identifier in the
bundle, so changing it makes a re-export look like a different site: the
importer will create everything again instead of updating what is already
there. Any stable string works; the site URL is the obvious choice.

The bundle is self-validating. It is written to a temporary file, checked
against the same rules the importer applies, and only then moved into place, so
a bundle that exists is a bundle that passed. Its bytes are reproducible: the
same export produces the same file, which makes two runs directly comparable.

Read `import-manifest.json` inside the bundle before importing. It records what
was emitted, what was skipped and why, and states plainly what this iteration
does not do — page restrictions are preserved as metadata but never enforced,
and labels are stored without any search or UI behaviour.

#### Users

Only users referenced by the exported content are included, never the whole
site directory. Each is proposed a Mattermost username, derived from their
Confluence username, their email's local part, or their display name, in that
order. Confluence often has no email for an account; those get a deterministic
placeholder address under `users.invalid`, which the importer never uses to
match an existing user.

To choose usernames yourself, pass `--user-mapping`. It overrides every derived
proposal and its header is exact:

```csv
confluence_account_id,confluence_user_key,confluence_username,confluence_email,mattermost_username
557058:abc,,,,alice
,,,bob@example.com,bob
```

Each row needs `mattermost_username` and at least one selector. Account IDs and
user keys match exactly; usernames and emails match case-insensitively.

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
