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

### `sync-import-users`

Checks if any users in the export file already exist in the Mattermost instance and ensures both username and email are consistent between the import file and the database. This command uses the Mattermost database as the source of truth and modifies the import file accordingly to match the database's state.

**When to use this command:**
- Before importing users to prevent conflicts with existing users.
- To synchronize user data between the import file and an existing Mattermost instance.
- To resolve username/email mismatches before performing an import.

**How it works:**
- The command checks each user in the import file against the Mattermost database
  - If a username exists with a different email, **the email in the import file is updated**.
  - If an email exists with a different username, **the username in the import file is updated**.
  - In case of conflicts (two different users found - one by username, one by email), the command prioritizes active users and then gives precedence to the username match.
- The command also removes duplicate channel memberships if found

> All username changes are tracked and automatically applied to posts, channels, and memberships throughout the import file.

**Usage:**

```sh
mmetl sync-import-users --file import-file.jsonl --output output-file.jsonl
```

**Authentication:**

This command requires credentials to access your Mattermost instance. You can authenticate in two ways:

1. **Remote mode** (default): Set environment variables:
   ```sh
   export MM_SITE_URL="https://your-mattermost-instance.com"
   export MM_ADMIN_TOKEN="your-admin-token"
   mmetl sync-import-users --file import.jsonl --output synced-import.jsonl
   ```

2. **Local mode**: Use the `--local` flag to connect via Unix socket (requires local access to the Mattermost server):
   ```sh
   mmetl sync-import-users --file import.jsonl --output synced-import.jsonl --local
   ```

**Output:**

The command creates a log file named `sync-import-users.log` in the current directory containing:
- Details of all user checks performed.
- Any username or email changes made.
- Warnings about conflicts or duplicate users.
- Summary statistics of changes.

**Important Notes:**
- Always review the log file after running this command.
- Consider using `--dry-run` first to preview changes.
- Username changes are automatically propagated to all references in posts, channels, and direct messages.
```