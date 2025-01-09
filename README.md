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


### External service mapping
You can import users with already mapped to the supported external service. You need to specify external service name and provide map in CVS with format:
```
{USERNAME_OR_EMAIL},{EXTERNAL_ID}
```
The file must be named by template `{EXTERNAL_SERVICE_NAME}_map.csv`

For example, if you want to map gitlab users while importing, you must to place file `gitlab_map.csv` in running directory.


### Batching
You can split to chunks your import data while conversion process for less resource.
Important: chunk with index 0 must be always processed first.

```shell
CTLCMD=mmctl
ETLCMD=mmetl
CONVERTED=mm_myteam

# Build zip arvhices chunked by 10000 post per chunk
$ETLCMD transform slack -t myteam -d data --max-chunk-size 10000 \
  -z "$CONVERTED" --auth-service gitlab --default-email-domain unknown.myteam.com \
  -f "slackdump_export_result.zip" -o mattermost_import.jsonl

# Do upload to MM server
for file in "${CONVERTED}".*.zip; do
  $CTLCMD import upload "$file"
done

# Process archives, 0 chunk must be processed first
for i in `$CTLCMD import list available | sort -n -t. -k2`; do $CTLCMD import process $i; done

```

