# Add additional metadata to the import file

| x       | y          |
| ------- | ---------- |
| State   | Final      |
| Version | 1.0        |
| Date    | 2026-08-26 |

## Overview

Every `mmetl transform <provider>` run produces a bulk import JSONL file, and
today that file says nothing about where it came from. Given one six months
later, there is no way to tell which mmetl built it, from which export, with
which flags, or how much it was supposed to contain.

The proposal is to fill the slot the bulk import format already reserves for
exactly this — the `info` object on the version line — with the run's own
metadata, and to make that metadata the single model the transform report is
also rendered from, so the import file and the report next to it cannot
disagree about what produced them.

## Background — the target format

The Mattermost bulk import JSONL format has a built-in, unused-by-the-server
slot for this. The first line of the file must be the version line, and it may
carry an `info` object:

```go
// server/channels/app/imports/import_types.go
type LineImportData struct {
    Type    string                 `json:"type"`
    Version *int                   `json:"version,omitempty"`
    Info    *VersionInfoImportData `json:"info,omitempty"`
    // ... one pointer field per line type
}

type VersionInfoImportData struct {
    Generator  string          `json:"generator"`
    Version    string          `json:"version"`
    Created    string          `json:"created"`
    Additional json.RawMessage `json:"additional,omitempty"`
}
```

Four facts about that slot constrain everything below. All were verified
against mattermost-server, and none of them should be worked around:

- **`Additional` is `json.RawMessage`**, so its contents are schema-free.
  Anything that is valid JSON is accepted.
- **The server ignores `info` entirely.** `processImportDataFileVersionLine` in
  `server/channels/app/import.go` reads only `line.Type` and `line.Version` and
  discards the rest; `mmctl import validate` likewise checks only that the
  version line is line 1 and that `version == 1`. The change is therefore
  backward and forward compatible with every server version — but also means
  nothing downstream consumes the metadata. It is for humans and for tooling
  that reads the file directly, and mmetl must never depend on the server
  round-tripping it.
- **`type` must stay `"version"` and `version` must stay `1`.** A new line type
  (e.g. `{"type":"metadata"}`) is not an option: the server's `importLine` has
  a `default` case that fails the whole import with `unknown_line_type.error`
  on any type it does not recognise.
- **Lines are read by a `bufio.Scanner` capped at 16 MB**, by both the server
  and mmctl. The metadata shares that budget with the version line, so it must
  stay bounded — no unbounded lists, no per-entity detail.

## Current behaviour

**mmetl does not carry its own copy of the import structs.** It imports the
server package directly:

    services/intermediate/export.go:11
    "github.com/mattermost/mattermost/server/v8/channels/app/imports"

and the pinned version already exposes both `LineImportData.Info
*VersionInfoImportData` and `VersionInfoImportData{Generator, Version, Created,
Additional}` — verified with `go doc` against this module's `go.mod`. Nothing
has to be redeclared; the `info` slot is reachable today and simply left nil.

**There is exactly one place the version line is written**, and it is the first
thing every export does — `Exporter.ExportVersion` in
`services/intermediate/export.go`:

```go
func (e *Exporter) ExportVersion(writer io.Writer) error {
	version := 1
	versionLine := &imports.LineImportData{
		Type:    "version",
		Version: &version,
	}
	return ExportWriteLine(writer, versionLine)
}
```

`Exporter.Export` calls it before any other line. Both providers embed
`intermediate.Exporter` (`services/slack/transformer.go:15`,
`services/rocketchat/transformer.go:19`), so one change covers Slack and
RocketChat, and any future provider gets it for free. `grid-transform` splits a
Grid zip into per-workspace zips and never emits JSONL, so it is untouched.

**Most of the metadata already exists.** `intermediate.RunMetadata`
(`services/intermediate/report.go:91`) hangs off `Exporter.Report.Metadata` and
is filled by `startTransformReport` in `commands/transform.go:227` from the
cobra command, for both providers:

| Wanted | Already on `RunMetadata` | Gap |
| --- | --- | --- |
| source platform | `Provider` | — |
| export format/version | — | neither source carries one (see below) |
| input filename | `Input` | full path, needs base-naming |
| input size in bytes | — | not captured |
| mmetl version + build hash | `Version` (`"v1.2.3 (abc123)"`) | concatenated, needs splitting |
| subcommand | — | not captured; `cmd.CommandPath()` |
| flags | `Flags` (joined string) | path values leak, needs redaction |
| target team | `Team` | — |
| produced counts | — | derivable from `Intermediate` |

`RunMetadata` is therefore not extended but **replaced** by the two structs
below, which become the single model behind the version line, the report
Markdown and the report JSON alike. Its fields all survive; they move.

Three properties of the existing code the implementation has to respect:

- *Counts cannot come from the report.* `EntityReport.Transformed` is only
  derived in `Report.Finish`, which runs from the deferred
  `writeTransformReport` — i.e. **after** `Export` has already written line 1.
  Counts must be read straight off `e.Intermediate`, which is fully populated
  by then because `Transform` completes before `Export` is called.
- *A nil `Report` is valid.* `Exporter` is documented as working with
  `Report == nil` and several tests build it as a struct literal
  (`services/intermediate/export_test.go:107`,
  `services/intermediate/emoji_name_test.go:126`). `ExportVersion` must still
  emit a well-formed line when there is no metadata to read.
- *`intermediate.NowFunc`, not `time.Now`.* It is the repo's documented
  determinism seam (`services/intermediate/types.go:22`); `AGENTS.md` says to
  reuse it rather than add another global.

**On the export's own format and version:** Slack export zips carry no version
marker anywhere mmetl parses (`services/slack/parse.go`), and the four
RocketChat collections mmetl reads (`users`, `rocketchat_room`,
`rocketchat_message`, `rocketchat_subscription`) carry no server version
either. The export *shape* is knowable and is recorded (`slack-export-zip`,
`rocketchat-mongodump`); a version field is omitted rather than invented.

## The model

One model, three consumers. `Info` mirrors the server's
`VersionInfoImportData`; `Additional` is the payload that hangs off it. Between
them they hold everything a run knows about itself.

New file `services/intermediate/metadata.go`:

```go
// Info is mmetl's typed mirror of imports.VersionInfoImportData — the `info`
// object on the version line of the bulk import file. It is also the whole of
// what the transform report records about a run: Report.Metadata is an Info,
// the report's `## Run` table is rendered from it, and it is the `metadata`
// object in transform-report.json.
//
// The server ignores `info` entirely, so nothing downstream reads this; it is
// for humans and for tooling that parses the file directly.
//
// Because the same value is written into a file that gets shipped to a
// Mattermost server, every field here must be safe to ship: no tokens, no
// absolute paths, no PII. Full local paths belong in the transform log.
type Info struct {
	Generator  string      `json:"generator"` // always "mmetl"
	Version    string      `json:"version"`   // "<version> (<build hash>)"
	Created    string      `json:"created"`   // RFC3339Nano UTC
	Additional *Additional `json:"additional,omitempty"`
}

// Additional is the schema-free object the server accepts under
// info.additional (json.RawMessage on its side, this struct on ours).
//
// Keep it bounded: the version line shares the importer's 16 MB per-line
// scanner budget, so this stays fixed-cardinality — no lists, no per-entity
// detail. That is what the transform report's Details sections are for.
type Additional struct {
	Source SourceInfo `json:"source"`
	Target TargetInfo `json:"target"`
	Run    RunInfo    `json:"run"`
	Counts Counts     `json:"counts"`
}

// SourceInfo describes the export being converted. File is a base name, never
// a path.
type SourceInfo struct {
	Platform  string `json:"platform"`         // "slack", "rocketchat"
	Format    string `json:"format,omitempty"` // "slack-export-zip", ...
	File      string `json:"file,omitempty"`
	SizeBytes int64  `json:"size_bytes,omitempty"`
}

// TargetInfo describes where the export is headed. mmetl targets exactly one
// team per run.
type TargetInfo struct {
	Team   string `json:"team,omitempty"`
	Output string `json:"output,omitempty"` // base name of the import file
}

// RunInfo describes the invocation itself.
type RunInfo struct {
	Version   string    `json:"mmetl_version"`
	BuildHash string    `json:"mmetl_build_hash,omitempty"`
	Command   string    `json:"command,omitempty"` // "mmetl transform slack"
	Flags     string    `json:"flags,omitempty"`   // only the flags actually set
	Started   time.Time `json:"started"`
	Finished  time.Time `json:"finished,omitempty"`
}

// Counts is how many lines of each kind the import file ended up with. It is
// the export side of the ledger; the report's Summary table is the source side
// (how many entities the export contained, and how many were dropped). They
// should agree, and a mismatch is worth investigating.
type Counts struct {
	Users, Bots                     int
	PublicChannels, PrivateChannels int
	GroupChannels, DirectChannels   int
	Posts, Replies                  int
	Reactions, Attachments          int
}
```

Supporting members, all on these types so there is nowhere else to look:

- `func (i Info) VersionString() string` — `"v1.2.3 (9f2c1ab)"`, or just the
  version when there is no hash. `Info.Version` is materialized from it at
  emission; `RunInfo` keeps the two halves apart so the build hash is available
  as its own field.
- `func (r RunInfo) Duration() time.Duration` — moved verbatim from
  `RunMetadata.Duration`, same magnitude-aware rounding.
- `sourceFormats = map[string]string{"slack": "slack-export-zip",
  "rocketchat": "rocketchat-mongodump"}` — same pattern as the existing
  `providerTitles` in `report_markdown.go`. A provider that forgets an entry
  gets an omitted field rather than a wrong one.

## What each consumer renders

**Version line** (`ExportVersion`) — `Info` converted to the server's type,
with `Additional` marshalled into the `json.RawMessage`:

```json
{"type":"version","version":1,"info":{
  "generator":"mmetl",
  "version":"v1.2.3 (9f2c1ab)",
  "created":"2026-08-26T10:11:12.123456789Z",
  "additional":{
    "source":{"platform":"slack","format":"slack-export-zip","file":"my_export.zip","size_bytes":12345678},
    "target":{"team":"myteam","output":"bulk-export.jsonl"},
    "run":{"mmetl_version":"v1.2.3","mmetl_build_hash":"9f2c1ab","command":"mmetl transform slack","flags":"--guest-handling=skip --skip-attachments=true","started":"2026-08-26T10:11:10Z"},
    "counts":{"users":42,"bots":2,"public_channels":10,"private_channels":3,"group_channels":1,"direct_channels":20,"posts":1234,"replies":567,"reactions":89,"attachments":12}
  }}}
```

`type` stays `"version"` and `version` stays `1`. Fixed cardinality, ~650 bytes
— the 16 MB line budget is a non-issue.

`run.finished` is absent here and only ever present in the report: the version
line is written before the run ends. That is the one field the two renderings
differ on, and it differs by time, not by model.

**`transform-report.json`** — `metadata` becomes the same `Info`, marshalled
directly (typed, not raw):

```json
{"metadata":{"generator":"mmetl","version":"v1.2.3 (9f2c1ab)","created":"...","additional":{...}},
 "error":"", "entities":{...}, "reasons":{...}}
```

**`transform-report.md`** — `runRows()` reads the same `Info`, so the `## Run`
table keeps its current rows (Provider, mmetl, Input, Team, Output, Started,
Finished, Duration, Flags) sourced from the new field paths. A second small
`## Produced` table renders `Counts`, which the report does not show today.

## Implementation

### New: `services/intermediate/metadata.go`

The structs above, plus the one place that fills them:

- `func (e *Exporter) Info() Info` — nil-safe. With a `Report`, it takes
  `e.Report.Metadata`, stamps `Created` from `NowFunc().UTC()`, fills
  `Additional.Counts` from `e.Intermediate`, writes the result **back** to
  `e.Report.Metadata`, and returns it. That write-back is what gets the counts
  and the creation time into the report as well. With a nil `Report` it returns
  a minimal `Info` (generator, created, counts) so a struct-literal `Exporter`
  still emits a valid line.
- `func (i *Intermediate) Counts() Counts` — one pass over the four channel
  slices, `UsersById` (splitting `IsBot`), and `Posts`, summing `len(Replies)`,
  `len(Reactions)` and `len(Attachments)` on each post and each reply. A second
  walk of the post slice on top of `ExportPosts`, but it is `len()` arithmetic
  with no allocation — noise next to the per-post JSON marshalling the export
  already does.

### `services/intermediate/export.go` — `ExportVersion`

```go
func (e *Exporter) ExportVersion(writer io.Writer) error {
	version := 1
	line := &imports.LineImportData{Type: "version", Version: &version}

	info := e.Info()
	// The server ignores `info`, so failing a whole migration because the
	// metadata would not marshal would be trading the work for a comment.
	// Log it and emit the plain version line instead.
	if additional, err := json.Marshal(info.Additional); err != nil {
		e.Logger.WithError(err).Warn("could not encode import metadata; ...")
	} else {
		line.Info = &imports.VersionInfoImportData{
			Generator:  info.Generator,
			Version:    info.VersionString(),
			Created:    info.Created,
			Additional: additional,
		}
	}

	return ExportWriteLine(writer, line)
}
```

### `services/intermediate/report.go`

- `Metadata RunMetadata` → `Metadata Info`; the `RunMetadata` type and its
  `Duration` method move to `metadata.go` as `RunInfo`.
- `Finish` sets `r.Metadata.Additional.Run.Finished` instead of
  `r.Metadata.Finished`, guarding a nil `Additional` (a `Report` built by
  `NewReport` and never stamped by the command layer).
- `NewReport` initializes `Metadata.Generator = "mmetl"` and a non-nil
  `Additional`, so nothing downstream has to nil-check it.

### `services/intermediate/report_markdown.go`

- `runRows()` reads the new paths (`Metadata.Additional.Source.Platform`,
  `.VersionString()`, `.Run.Flags`, …). Same rows, same order, same rendered
  output — the existing Markdown assertions in
  `commands/transform_report_test.go` keep passing untouched.
- `providerTitle` reads `Metadata.Additional.Source.Platform`.
- new `producedRows()` → a `## Produced` table, listing only non-zero counts so
  a Slack export with no group channels does not render a row of zeroes.

### `commands/transform.go`

- `startTransformReport` builds the `Info` instead of a `RunMetadata`:
  `Source{Platform: provider, Format: <from map>, File: filepath.Base(input),
  SizeBytes: inputSizeBytes(input)}`, `Target{Team: team, Output:
  filepath.Base(output)}`, `Run{Version: getVersion(), BuildHash:
  getBuildHash(), Command: cmd.CommandPath(), Flags: formatChangedFlags(cmd),
  Started: NowFunc().UTC()}`.
- `formatChangedFlags` gains base-naming for path-valued flags via a
  package-level `pathFlags` set (`file`, `output`, `dump-dir`,
  `attachments-dir`, `uploads-dir`, `team-map-path`), so
  `--file=/Users/x/export.zip` renders as `--file=export.zip`.
- new `inputSizeBytes(path string) int64`: `os.Stat` for a file; for a
  directory (RocketChat's `--dump-dir`) a walk summing regular-file sizes,
  which is a handful of `.bson` stats. Returns `0` on any error — a transform
  must never fail because a size could not be read.
- the full, unredacted input and output paths are logged once at start
  (`logger.WithFields(...).Info`), so nothing is lost for local debugging.

`commands/transform_rocketchat.go` needs no change: it already calls the shared
`startTransformReport`.

### Consequence of sharing the model

The `## Run` table and `transform-report.json` now show base names and redacted
flags rather than the operator's full paths. That is the price of one model,
and it is worth paying: these reports get attached to support tickets, and a
report that is safe to ship by construction is better than one that has to be
read before it is shared. The full paths remain in `transform-<provider>.log`,
which sits in the same directory and is unambiguously local.

`transform-report.json` changes shape: `metadata` gains the `generator` /
`created` / `additional` nesting, and `flags` is redacted. That format landed
on this same branch (2624109) and has not shipped, so this is free now and
would not be later.

## Out of scope

**No new line type, no new flag, no new command.** The metadata is emitted
unconditionally on every transform; there is nothing to opt into.

**Nothing sensitive.** mmetl has no auth flags at all — no tokens, no API keys,
nothing that could leak a credential. The only sensitive-shaped values in the
flag set are filesystem paths, handled by `pathFlags`. Kept, because they change
the output and are not sensitive: `--bot-owner` (a Mattermost username the
operator chose), `--default-email-domain`, `--guest-handling`, `--skip-*`.

**No source-entity names, IDs or emails** appear anywhere in `Additional`.
Naming individual entities is the transform report's job, and keeping it out is
also what keeps the version line bounded.

**`grid-transform` is untouched.** It splits a Grid zip into per-workspace zips
and emits no JSONL; the resulting zips are fed back through `transform slack`,
which does emit the metadata.

## Verification

**Unit — `services/intermediate/metadata_test.go` (new):**

- *One model, three renderings.* Build an `Exporter` with a stamped `Report`
  and a small `Intermediate`, stub `NowFunc`, run `ExportVersion` into a
  `bytes.Buffer`. Then assert on all three outputs from that one run:
  - the buffer unmarshals into `imports.LineImportData` with
    `Type == "version"`, `*Version == 1`, `Info.Generator == "mmetl"`,
    `Created` reparsing under `time.RFC3339Nano`, and `Info.Additional`
    unmarshalling into `Additional`;
  - `report.JSON()`'s `metadata` unmarshals into `Info` and its `Additional`
    is **deep-equal** to the one from the version line except for
    `Run.Finished` — the assertion that actually pins the shared model, and the
    one that fails if a future change re-forks them;
  - `report.Markdown()`'s `## Run` table shows the same team, input and flags.
- *Counts.* An `Intermediate` with known contents produces the expected
  `Counts`, including replies, reactions and attachments nested under replies.
- *Nil `Report`.* A struct-literal `Exporter` still emits a valid version line
  with counts and no panic.
- *Redaction.* Flags containing `--file=/Users/someone/export.zip` render as
  `--file=export.zip`; the marshalled line contains no `/Users/` and no path
  separator in `source.file` or `target.output`.
- *Size bound.* The marshalled version line is asserted under 8 KB, so a future
  field that adds an unbounded list trips a test rather than a 16 MB scanner.

**Command level — `commands/transform_report_test.go`:** extend the existing
`transform slack` run to read line 1 of the produced JSONL and assert it parses
as a version line whose `additional.source.platform` is `slack`,
`target.team` is `myteam`, counts are non-zero, and no absolute path appears
anywhere in the line. Existing Markdown assertions stay as they are, which is
the check that the rendered report did not regress.

**Real fixture:** `mmetl transform slack --team myteam --file
testdata/slack-export-cyber-defense-hq.zip --output /tmp/mm/bulk-export.jsonl
--skip-attachments`, then `head -1 /tmp/mm/bulk-export.jsonl | jq .` to confirm
line 1 is valid JSON in the shape above, and `jq .metadata
/tmp/mm/transform-report.json` to confirm the report carries the same object.

**`mmctl import validate`** takes an archive, not a bare JSONL, so the check is
to zip the produced `bulk-export.jsonl` (plus `data/` when attachments are on)
and run

    mmctl import validate /tmp/mm/import.zip --team myteam \
      --check-server-duplicates=false --ignore-attachments

confirming it reports no errors. If it turns out to need a server connection
despite the flags, that is to be stated explicitly rather than quietly dropped.

**Repo commands** (`AGENTS.md`): `make check-style`, then `make test` (needs a
running Docker daemon for the non-tagged `*_e2e_test.go` in `commands/`; fast
loop is `go test ./services/intermediate/ ./commands/ -run 'Report|Metadata'`).
`make docs` is not needed — this adds no command and no flag — but
`make docs-check` confirms that.
