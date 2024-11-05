# mmetl

The Mattermost ETL is a tool to transform an export file from a given
set of providers into a Mattermost compatible export file.

## Installation

To install the project in your `$GOPATH`, just run:

```sh
go get -u github.com/mattermost/mmetl
```

## Usage

The tool is self documented, so you can run it with with the `--help`
flag and it will print the available subcommands and the options for
the current command:

```sh
ETL tool to transform the export files from different providers to be compatible with Mattermost.

Usage:
  mmetl [command]

Available Commands:
  check       Checks the integrity of export files.
  completion  Generate the autocompletion script for the specified shell
  help        Help about any command
  transform   Transforms export files into Mattermost import files
  version     Prints the version of mmetl.

Flags:
  -h, --help   help for mmetl

Use "mmetl [command] --help" for more information about a command.
```

```sh
ETL tool to transform the export files from different providers to be compatible with Mattermost.

Usage:
  mmetl [command]

Available Commands:
  check       Checks the integrity of export files.
  completion  Generate the autocompletion script for the specified shell
  help        Help about any command
  transform   Transforms export files into Mattermost import files
  version     Prints the version of mmetl.

Flags:
  -h, --help   help for mmetl

Use "mmetl [command] --help" for more information about a command.
```

```sh
Usage:
  mmetl transform slack [flags]

Examples:
  transform slack --team myteam --file my_export.zip --output mm_export.json

Flags:
  -l, --allow-download                Allows downloading the attachments for the import file
  -d, --attachments-dir string        the path for the attachments directory (default "data")
  -n, --channel-only string           Only convert messages from this specific channel <<< NEW
      --debug                         Whether to show debug logs or not
      --default-email-domain string   If this flag is provided: When a user's email address is empty, the output's email address will be generated from their username and the provided domain.
  -p, --discard-invalid-props         Skips converting posts with invalid props instead discarding the props themselves
  -f, --file string                   the Slack export file to transform
  -h, --help                          help for slack
  -m, --max-message-length int        Maximum length of a message before it needs to be split (default 16383)    <<< NEW
  -o, --output string                 the output path (default "bulk-export.jsonl")
  -a, --skip-attachments              Skips copying the attachments from the import file
  -c, --skip-convert-posts            Skips converting mentions and post markup. Only for testing purposes
      --skip-empty-emails             Ignore empty email addresses from the import file. Note that this results in invalid data.
  -t, --team string                   an existing team in Mattermost to import the data into
```
