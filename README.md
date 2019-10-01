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
$ mmetl --help
ETL tool to transform the export files from different providers to be compatible with Mattermost.

Usage:
  mmetl [command]

Available Commands:
  check       Checks the integrity of export files.
  help        Help about any command
  transform   Transforms export files into Mattermost import files

Flags:
  -h, --help   help for mmetl

Use "mmetl [command] --help" for more information about a command.
```
