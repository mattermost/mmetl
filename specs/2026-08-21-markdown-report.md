# transform report file

| x       | y          |
| ------- | ---------- |
| State   | Draft      |
| Version | 0.2        |
| Date    | 2026-08-24 |

## Overview

The proposal is to implement a way for users to have a better view on which entities had
been properly converted during the transform process without having to peek through the
logs manually, and give context on why an entity was not migrated.

The report is produced by every `mmetl transform <provider>` run as two artifacts:

- a **Markdown file** for a human (the sysadmin running the migration) to read, and
- a **JSON file** holding the serialized `Report` struct, for tooling and diffing.

## Scope

In scope for v1:

- `mmetl transform slack`
- `mmetl transform rocketchat`
- any future provider, by construction (see [Placement](#placement))

Out of scope for v1, tracked as follow-ups:

- `mmetl grid-transform` — has its own drop set (unsafe team folder names, teams with no
  folder, channels with no resolvable team ID).
- `mmetl check slack` / `mmetl check rocketchat`: emit their own warnings and would
  benefit from the same registry, but the check path does not produce an import file.
  Plus the command is going to be refactored into a `-dry-run` version of the transform
  command.

## Outcomes

There are exactly **two** terminal outcomes for a source entity:

| Outcome         | Meaning                                                     |
| --------------- | ----------------------------------------------------------- |
| **Transformed** | The entity reached the Mattermost import file in some form. |
| **Skipped**     | The entity did not reach the import file, and we know why.  |

There is no `Failed` outcome. An entity that we converted, degraded, renamed, merged,
split or truncated is still **Transformed** — the change is recorded as a _note_ on the
entity, and does not affect the counters. Anything that would previously have been
called a failure is either a skip (we decided not to import it) or a hard error that
aborts the run (see [Errors](#errors)).

Cases that are Transformed **with a note**, not skipped:

- a post split into a thread because it exceeded `PostMessageMaxRunesV2`
  (`SplitPostIntoThread` / `SplitOversizedReplies`)
- an MPIM over `ChannelGroupMaxUsers` converted into a private channel
  (`services/slack/intermediate.go:478`)
- duplicate MPIMs merged into one canonical channel (`dedupByMembers`,
  `services/slack/intermediate.go:401`)
- an emoji renamed to a Mattermost-valid name (`services/intermediate/emoji_name.go:62`)
- a first/last name or position truncated (`services/intermediate/types.go:221`)
- a placeholder email substituted via `--default-email-domain`
  (`services/intermediate/types.go:212`)
- a post that kept its message but lost oversized props
  (`services/slack/intermediate.go:959`)
- a placeholder user invented for a dangling reference (`CreateIntermediateUser`)

## Counting rules

Counters count **source entities**, never emitted JSONL lines.

- One Slack post split into three Mattermost posts counts as **1 transformed post**,
  plus a note recording the split.
- Two MPIMs merged into one channel count as **2 transformed group channels**, plus a
  note on the non-canonical one recording which channel it merged into.
- An MPIM promoted to a private channel counts as **1 transformed group channel** with a
  conversion note — it is not double-counted under private channels.

`Transformed` is **derived at report time**, not incremented at each call site.
Incrementing at call sites is fragile: entities move between buckets (group → private),
get merged, and get split, so hand-maintained counters drift. Instead the report
computes, for each entity kind, `Transformed = <source entity count> - Skipped`, where
the source entity count is captured once per kind at the point the source collection is
first read.

Call sites therefore only ever record **skips and notes**. There is no `IncTransformed()`.

For this to be robust we need to make sure all code paths during transform properly:

- Transform the element
    - With or without a note
- Skip it making the count

## Placement

`Report` lives in `services/intermediate` (new files `report.go`, `report_markdown.go`,
`reasons.go`) and hangs off `intermediate.Exporter` — the struct both providers already
embed (`services/intermediate/export.go:19`). Every provider gets the report for free,
including future ones.

Provider-specific reasons live in `services/slack/reasons.go` and
`services/rocketchat/reasons.go` and register themselves into the shared registry.

## Data storage

```go
// EntityKind identifies a class of source entity in the report. Rendering order
// in the summary table is the declaration order of entityOrder, not map order.
type EntityKind string

const (
	EntityUser              EntityKind = "user"
	EntityBot               EntityKind = "bot"
	EntityPublicChannel     EntityKind = "public_channel"
	EntityPrivateChannel    EntityKind = "private_channel"
	EntityGroupChannel      EntityKind = "group_channel"
	EntityDirectChannel     EntityKind = "direct_channel"
	EntityChannelMembership EntityKind = "channel_membership"
	EntityPost              EntityKind = "post"
	EntityThread            EntityKind = "thread"
	EntityReaction          EntityKind = "reaction"
	EntityFile              EntityKind = "file"
	EntityEmoji             EntityKind = "emoji"
	EntitySubscription      EntityKind = "subscription" // RocketChat
	EntityUpload            EntityKind = "upload"       // RocketChat
)

type Report struct {
	Metadata RunMetadata                  `json:"metadata"`
	Error    string                       `json:"error,omitempty"`
	Entities map[EntityKind]*EntityReport `json:"entities"`
	// Reasons is the dictionary of every reason referenced by a note in this
	// run, emitted once so notes can refer to it by code.
	Reasons map[string]*Reason `json:"reasons"`

	logger log.FieldLogger
}

type EntityReport struct {
	Transformed int                `json:"transformed"`
	Skipped     int                `json:"skipped"`
	Notes       []ReportEntityNote `json:"notes"`

	// sourceTotal is the number of source entities seen for this kind; it is
	// the basis for the derived Transformed count. Not serialized.
	sourceTotal int
}

type ReportEntityNote struct {
	EntityID   string   `json:"entity_id"`             // user_id, channel_id, post ts, ...
	EntityName string   `json:"entity_name,omitempty"` // username, channel_name, ... (if available)
	ReasonCode string   `json:"reason_code"`
	Args       []string `json:"args,omitempty"` // formatted into Reason.Short
	Count      int      `json:"count,omitempty"` // >0 only for aggregate notes

	// reason points at the single registry instance for ReasonCode. The prose
	// is never copied per note.
	reason *Reason
}
```

### Reasons

A reason is declared once, in code, and referenced by every note that uses it. This is
the single source of truth for both the log line and the report entry, so the two can
never drift.

```go
type Reason struct {
	// Code is stable and machine-readable; it is also the Markdown footnote
	// anchor and the JSON key. Never change a Code without a changelog entry.
	Code string

	// Short is the inline text, rendered right after the entity. It may contain
	// fmt verbs, filled from ReportEntityNote.Args.
	Short string

	// Detail is the long explanation and remediation hint. It is rendered once,
	// as a Markdown footnote, no matter how many entities reference it.
	Detail string

	// Skip is true when this reason means the entity did not reach the import
	// file. False for notes on entities that were transformed with changes.
	Skip bool

	// Quiet suppresses the per-entity log line for high-cardinality reasons
	// (e.g. every reply of a dropped thread). The note is still recorded in
	// full; an aggregate line is logged once at the end of the run.
	Quiet bool
}
```

Example, replacing the current three-sentence message at
`services/slack/intermediate.go:526`:

```go
var ReasonGuestNoChannel = intermediate.RegisterReason(&intermediate.Reason{
	Code:  "guest_no_channel",
	Short: "has no public or private channel membership in the Slack export",
	Detail: "Mattermost cannot scope a guest's access without at least one public or " +
		"private channel membership, so this user (and their memberships and posts) " +
		"is skipped. Use --guest-handling=user to import them as a regular member instead.",
	Skip: true,
})
```

`RegisterReason` panics on a duplicate `Code`, so collisions are caught at init.

### Recording API

```go
// For returns the EntityReport for a kind, creating it on first use.
func (r *Report) For(kind EntityKind) *EntityReport

// Named helpers over For, for the hot paths.
func (r *Report) Users() *EntityReport
func (r *Report) Posts() *EntityReport
// ...

// Seen records that n source entities of this kind exist. Called once per
// source collection; it is what Transformed is derived from.
func (e *EntityReport) Seen(n int)

// Skip records that one entity was not transformed, and why.
func (e *EntityReport) Skip(id, name string, reason *Reason, args ...string)

// SkipN records that n entities with no individually meaningful ID were not
// transformed (channel memberships, reactions). Produces one aggregate note.
func (e *EntityReport) SkipN(n int, reason *Reason, args ...string)

// Note records something that happened to an entity that WAS transformed
// (split, merged, renamed, truncated). Does not touch the counters.
func (e *EntityReport) Note(id, name string, reason *Reason, args ...string)
```

Constraints on the implementation:

- **Nil-safe.** `Exporter` is built as a struct literal in several places, including
  tests, so a nil `*Report` receiver must make every method a no-op rather than panic.
- **Not concurrency-safe.** The transform pipeline is single-goroutine; state that
  explicitly in the doc comment rather than paying for a mutex.
- **Logging is a side effect of recording.** `Skip`/`Note` emit the log line themselves
  (at Warn for `Skip`, Info for `Note`), with structured fields `entity_kind`,
  `entity_id`, `reason_code`, and the message built from `Short` + `Detail`. Call sites
  stop calling `t.Logger.Warnf` directly for anything that is also a report entry.

### Memory

Notes are kept in full — one per skipped entity, uncapped. Identifying _which_ users were
omitted is the point of the report, so truncation is deliberately not implemented. The
cost is bounded in practice because a note holds only IDs and a pointer to a shared
`Reason`; the prose is stored once per reason, not once per entity.

The one place this could still be large is per-post reasons on a multi-million-post
export (e.g. `post_no_user`, which fires from roughly ten call sites in
`services/slack/intermediate.go:969-1080`). Those reasons are marked `Quiet` so they do
not also multiply the log file, but their notes are retained.

## Report file example

```md
# Slack Transform Report

## Stopped because of an error

The transform did not finish. The report below covers everything processed up to the
point of failure.

    failed to retrieve file with id F12345

## Run

| Field    | Value                                             |
| -------- | ------------------------------------------------- |
| Provider | slack                                             |
| mmetl    | v0.5.1 (721d761)                                  |
| Input    | my_export.zip                                     |
| Team     | myteam                                            |
| Output   | mm_export.jsonl                                   |
| Started  | 2026-08-24T10:14:03Z                              |
| Finished | 2026-08-24T10:16:41Z                              |
| Duration | 2m38s                                             |
| Flags    | `--guest-handling=guest --skip-attachments=false` |

## Summary

| Entity                              | Transformed | Skipped |
| ----------------------------------- | ----------- | ------- |
| [Users](#users)                     | 3           | 1       |
| [Public channels](#public-channels) | 12          | 0       |
| [Group channels](#group-channels)   | 4           | 1       |
| [Posts](#posts)                     | 8231        | 17      |
| [Threads](#threads)                 | 106         | 1       |

## Details

### Users

- **U004** (`channelless.guest`): has no public or private channel membership in the Slack export[^guest_no_channel]
- **U009** (`long.name`): position exceeded the maximum length and was truncated[^position_truncated]

### Group channels

- **G002** (`mpdm-ana--bob--cy-1`): merged into `mpdm-ana--bob--cy` because both have the same members[^mpim_merged]

### Posts

- **1704067260.000200**: split into 3 posts in a thread because it exceeded the maximum message length[^post_split]
- **17 posts**: the author was a skipped user[^post_skipped_author]

### Threads

- **1704067260.000200** (`g001`): the thread root was not imported[^thread_root_missing]

[^guest_no_channel]: Mattermost cannot scope a guest's access without at least one public or private channel membership, so this user (and their memberships and posts) is skipped. Use `--guest-handling=user` to import them as a regular member instead.

[^position_truncated]: Mattermost limits the position field length; the value was truncated to fit and the user can update it after logging in.

[^mpim_merged]: Mattermost keys group channels by member-set hash, so two channels with identical members would collide on import. They are merged and posts from both are routed to the surviving channel.

[^post_split]: Mattermost limits a single post's length. Longer posts are split, with the remainder posted as replies in a thread under the original.

[^post_skipped_author]: The post's author was skipped, so the post is dropped to avoid a dangling reference in the import file.

[^thread_root_missing]: The thread's root post was not imported (for example, its author was a skipped guest). Replies from other users are dropped with it so the thread is not partially imported.
```

Rendering rules:

- Section anchors are GitHub-flavored Markdown: lowercased, spaces to hyphens, so
  `## Public channels` is linked as `#public-channels`. (v0.1 used `#Users` /
  `#Public+Channels`, which do not resolve.)
- A note line is `- **{EntityID}** (`{EntityName}`): {Short}[^{Code}]`, with the
  `({EntityName})` part omitted when the name is unknown.
- An aggregate note (from `SkipN`) renders as `- **{Count} {plural kind}**: {Short}[^{Code}]`.
- Entity IDs and names are escaped for Markdown; channel and user names from a source
  export can contain backticks, pipes and brackets.
- A footnote block is emitted once per distinct reason actually used in the run, in
  first-use order.
- An entity kind with `Transformed == 0 && Skipped == 0 && len(Notes) == 0` is omitted
  from both the summary table and the details.
- The `## Stopped because of an error` section is present only when the run aborted.

### Ordering and determinism

Notes are sorted before rendering, by `(ReasonCode, EntityID)`, and the same order is
used in the JSON. Without this the report is non-deterministic: users are iterated from
`Intermediate.UsersById`, a Go map whose iteration order is randomized per run, so two
transforms of the same export would emit the same notes in a different order. That makes
two reports impossible to diff and makes golden-file tests flaky.

Timestamps in `## Run` use the existing `intermediate.NowFunc` seam so tests stay
deterministic, per the convention in `AGENTS.md`.

## JSON output

The same `Report` struct, marshalled with `json.MarshalIndent`. Reasons appear once
under `reasons`; notes refer to them by `reason_code`. Consumers can render their own
view, diff two runs, or assert on a report in a test without parsing Markdown.

## CLI surface

One new flag on each transform command:

```
--report-path string   Path to write the transform report (default "transform-report.md").
                       The JSON report is written alongside it with a .json extension.
                       Set to an empty string to disable the report.
```

Given `--report-path out/report.md`, mmetl writes `out/report.md` and `out/report.json`.
If the path has no extension, `.json` is appended for the JSON file.

A short summary (the contents of the `## Summary` table, plus the report path) is also
printed to stdout at the end of the run, so an operator who never opens the file still
sees the counts.

Adding a flag requires regenerating CLI docs: run `make docs` and commit the resulting
`docs/cli/*.md`. CI enforces this through `make docs-check`.

## Errors

The report is written even when the transform aborts, because a partial report explains
how far the run got. The command wires the write through a `defer`, and stores the error
message in `Report.Error`, which renders as the `## Stopped because of an error` section
at the top of the Markdown file.

One path bypasses `defer`: `IntermediateUser.Sanitise` calls `ExitFunc(1)` — i.e.
`os.Exit` — when a user has no email and neither `--skip-empty-emails` nor
`--default-email-domain` was given (`services/intermediate/types.go:217`). Recommended
fix, lowest risk: the transform commands install a wrapper before running,

```go
inner := intermediate.ExitFunc
intermediate.ExitFunc = func(code int) {
	writeReport(report, reportPath, errors.New("..."))
	inner(code)
}
```

which keeps the existing `ExitFunc` test seam intact. The alternative — making
`Sanitise` return an error and propagating it — is cleaner but changes behaviour on a
path that several tests depend on; leave it as a follow-up.

## Plumbing changes required

These skip sites are free functions or logger-only methods and need a `*Report` (or a
receiver) threaded through them:

- `rocketchat.ExtractAttachments(...)` — `services/rocketchat/attachments.go:21`.
  Exported, takes a `logger`, and owns six distinct upload-skip reasons.
- `IntermediateUser.Sanitise(logger, ...)` — `services/intermediate/types.go:199`.
- `IntermediateChannel.SanitiseWithPrefix(logger, ...)` — `services/intermediate/types.go:137`.
- `slack.SplitChannelsByMemberSize` — `services/slack/export.go:47`. Uses the **stdlib**
  `log`, so its single-member-DM drop is not even in `transform-slack.log` today. The
  same drop is logged with different wording at `services/slack/intermediate.go:229` and
  `:558` — three sites, two messages, one reason. Good first customer for the registry.
- `EmojiNameSanitizer` — `services/intermediate/emoji_name.go` — already holds a logger;
  give it the report too.

These transformer fields are superseded by the report and must be removed so there is
only one source of truth:

- `services/slack/transformer.go:22-36` — `droppedPostRefs`, `droppedReactionRefs`,
  `droppedMembershipRefs`, `warnedDroppedThreads`. The warn-once-per-thread behaviour is
  preserved by marking the thread reason `Quiet`.
- `services/rocketchat/transformer.go:45-50` — `droppedPostRefs`,
  `droppedMembershipRefs`.
- The end-of-transform summary logs at `services/slack/intermediate.go:1217` and
  `services/rocketchat/transformer.go:129` are replaced by the report summary.

## Reason inventory

Every reason to migrate into the registry, from the current code. Slack:

| Code                        | Site                                                      | Skip |
| --------------------------- | --------------------------------------------------------- | ---- |
| `guest_skip_mode`           | `slack/intermediate.go:132`                               | yes  |
| `guest_no_channel`          | `slack/intermediate.go:526`                               | yes  |
| `dm_single_member`          | `slack/intermediate.go:229`, `:558`, `slack/export.go:52` | yes  |
| `channel_not_found`         | `slack/intermediate.go:904`                               | yes  |
| `post_no_user`              | `slack/intermediate.go:969-1080` (~10 sites)              | yes  |
| `post_unsupported_type`     | `slack/intermediate.go:1116`                              | yes  |
| `post_props_too_large`      | `slack/intermediate.go:956`, `:996`                       | yes  |
| `post_props_dropped`        | `slack/intermediate.go:959`, `:999`                       | no   |
| `post_no_comments`          | `slack/intermediate.go:1009`                              | yes  |
| `post_skipped_author`       | `slack/intermediate.go` (droppedPostRefs)                 | yes  |
| `thread_root_missing`       | `slack/intermediate.go:641`                               | yes  |
| `membership_skipped_user`   | `slack/intermediate.go:203`, `:556`                       | yes  |
| `reaction_skipped_user`     | `slack/intermediate.go` (droppedReactionRefs)             | yes  |
| `file_access_denied`        | `slack/intermediate.go:808`                               | yes  |
| `file_add_failed`           | `slack/intermediate.go:803`, `:812`                       | yes  |
| `bot_no_bot_id`             | `slack/intermediate.go:174`                               | no   |
| `archived_no_timestamp`     | `slack/intermediate.go:261`                               | no   |
| `channel_no_created_ts`     | `slack/intermediate.go:335`                               | no   |
| `mpim_converted_to_private` | `slack/intermediate.go:478`                               | no   |
| `mpim_merged`               | `slack/intermediate.go:401`                               | no   |
| `post_split`                | `SplitPostIntoThread`                                     | no   |

Shared (`services/intermediate`):

| Code                         | Site               | Skip |
| ---------------------------- | ------------------ | ---- |
| `email_blank`                | `types.go:206`     | no   |
| `email_placeholder`          | `types.go:212`     | no   |
| `first_name_truncated`       | `types.go:221`     | no   |
| `last_name_truncated`        | `types.go:226`     | no   |
| `position_truncated`         | `types.go:231`     | no   |
| `channel_name_truncated`     | `types.go:144`     | no   |
| `channel_name_invalid_chars` | `types.go:151`     | no   |
| `channel_display_truncated`  | `types.go:157`     | no   |
| `channel_purpose_truncated`  | `types.go:164`     | no   |
| `channel_header_truncated`   | `types.go:170`     | no   |
| `emoji_renamed`              | `emoji_name.go:62` | no   |
| `guest_demoted_to_user`      | `export.go:422`    | no   |

RocketChat:

| Code                        | Site                         | Skip |
| --------------------------- | ---------------------------- | ---- |
| `user_unsupported_type`     | `transformer.go:221`         | yes  |
| `guest_skip_mode`           | `transformer.go:230`         | yes  |
| `guest_no_channel`          | `transformer.go:306`         | yes  |
| `dm_all_members_skipped`    | `transformer.go:336`, `:446` | yes  |
| `dm_member_count_mismatch`  | `transformer.go:347`, `:457` | yes  |
| `room_encrypted`            | `transformer.go:414`         | yes  |
| `room_unknown_type`         | `transformer.go:493`         | yes  |
| `subscription_unknown_user` | `transformer.go:628`         | yes  |
| `thread_root_missing`       | `transformer.go:739`         | yes  |
| `message_unsupported_type`  | `transformer.go:793`         | yes  |
| `message_unknown_room`      | `transformer.go:918`         | yes  |
| `upload_incomplete`         | `attachments.go:47`          | yes  |
| `upload_thumbnail`          | `attachments.go:52`          | yes  |
| `upload_gridfs_missing`     | `attachments.go:79`          | yes  |
| `upload_no_uploads_dir`     | `attachments.go:86`          | yes  |
| `upload_unsafe_path`        | `attachments.go:97`          | yes  |
| `upload_unknown_store`      | `attachments.go:105`         | yes  |
| `upload_extract_failed`     | `attachments.go:116`         | yes  |

## Implementation phases

1. **Core.** `report.go`, `reasons.go`, `report_markdown.go` in `services/intermediate`;
   `Report` field on `Exporter`; nil-safe recording API; Markdown and JSON renderers with
   golden-file unit tests.
2. **Slack.** Migrate every Slack reason, thread `*Report` through the free functions,
   remove the superseded counters, wire `--report-path` into `transform slack`, run
   `make docs`.
3. **RocketChat.** Same for the RocketChat transformer and `ExtractAttachments`.
4. **Verification.** Extend the guest-handling e2e tests
   (`TestTransformSlackE2EGuestSkip`, `TestTransformSlackE2EChannellessGuestMpimThread`,
   `TestTransformRocketChatE2EGuestImport`) to assert on the JSON report, which is a
   stronger and more readable assertion than the current output-file inspection.

Per `AGENTS.md`, `make check-style` and `make test` gate the work, and any flag change
also requires `make docs` with the regenerated `docs/cli/*.md` committed.

## Changelog

| Date       | Note                                                                                                                                                                                                                                                                          |
| ---------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 2026-08-22 | Initial definition of the proposal on how the data is stored and how the report should look like.                                                                                                                                                                             |
| 2026-08-24 | v0.2: two outcomes only (no Failed); source-entity counting with derived Transformed; shared Reason registry with Markdown footnotes; generic EntityReport; JSON + Markdown output; report on error; GFM anchors; determinism rules; full reason inventory and plumbing list. |
