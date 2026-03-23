# Memory Consumption Analysis for Large Slack Exports

## Current Architecture

The data flow is a 3-phase pipeline where **everything is held in memory simultaneously**:

```
ZIP → Parse (SlackExport in RAM) → Transform (Intermediate in RAM) → Export (stream to JSONL file)
```

## Identified Memory Bottlenecks

### 1. `SlackExport.Posts` — `map[string][]SlackPost` (Critical)

`parse.go:224-276` — Every single post from every channel across the entire
export is loaded into a single in-memory map. For a workspace with millions of
messages, this is the dominant memory consumer. Each `SlackPost` carries `Text`,
`Attachments []*model.SlackAttachment`, `Files []*SlackFile`,
`Reactions []*SlackReaction`, and `Room *SlackRoom`.

### 2. Duplicate data: `SlackExport` + `Intermediate` coexist (Critical)

In `commands/transform.go:135-143`, `ParseSlackExportFile()` returns
`slackExport`, then `Transform()` builds `Intermediate` from it. Both
structures live in memory simultaneously until `Transform()` returns,
effectively **doubling** peak memory for posts.

### 3. `SlackParseUsers` triple-parses users (Moderate)

`parse.go:16-49` — Users JSON is read with `io.ReadAll(b)`, unmarshalled to
`[]SlackUser`, unmarshalled again to `[]map[string]any` (for debug logging),
then re-marshalled back to JSON (also for debug logging). This creates 3+
copies of user data in memory.

### 4. Regex compilation for mentions (Moderate)

`parse.go:79-115` — A compiled `*regexp.Regexp` is created for **every user**
and **every channel** in the workspace. These are held in maps and applied
across every post in every channel, creating O(users × posts) and
O(channels × posts) operations with all regexes in memory.

### 5. `Intermediate.Posts` — flat `[]*IntermediatePost` (Moderate)

`intermediate.go:150` — All transformed posts (with their `Replies`
sub-slices) are accumulated into one flat slice. This is never freed until the
process exits.

### 6. `archive/zip.NewReader` (Moderate)

`commands/transform.go:114` — Go's `zip.NewReader` reads the ZIP central
directory into memory. For a TB-scale ZIP, this directory alone can be hundreds
of MBs.

### 7. Thread maps per channel (Low-Moderate)

`intermediate.go:664-668` — For each channel, a `timestamps map[int64]bool`
and `threads map[string]*IntermediatePost` are built. These are
garbage-collected after each channel, but for a single massive channel, they
can spike memory.

## Proposed Optimizations

### Optimization 1: Channel-at-a-time streaming pipeline

**Description:** Instead of loading all posts, then transforming all posts,
then exporting all posts, process one channel at a time: parse its posts →
transform → export → discard, then move to the next channel.

**Changes required:**

- Restructure `ParseSlackExportFile` to do a first pass collecting only
  metadata (users, channels) and building an index of which ZIP entries belong
  to which channel.
- Add a second pass that iterates channel-by-channel: open the relevant ZIP
  entries, parse posts, run mention/markup conversion, transform, export to the
  JSONL writer, then release memory.
- `Transform()` and `Export()` would merge into a streaming
  `TransformAndExport()`.

**Memory savings:** ~60-80% reduction. Only one channel's posts live in memory
at a time instead of the entire export.

**Drawbacks:**

- Significantly more complex code architecture; the clean
  Parse→Transform→Export separation is lost.
- Users and channels still must be loaded first (they're needed for mention
  conversion and membership resolution), but these are typically small relative
  to posts.
- Two passes over the ZIP file (first for metadata, second for posts), which is
  slower for I/O-bound systems.
- Thread cross-references that span channels (rare but possible via shared
  channels) would need special handling.

### Optimization 2: Disk-backed intermediate storage

**Description:** After parsing each channel's posts, serialize the
`[]SlackPost` or `[]*IntermediatePost` to a temporary file (e.g., using
`encoding/gob` or a lightweight embedded DB like BoltDB/BadgerDB). Load them
back on demand during transform/export.

**Changes required:**

- Replace `map[string][]SlackPost` with a disk-backed store keyed by channel
  name.
- Implement a simple temp-file-per-channel approach or use an embedded
  key-value store.
- Modify `TransformPosts` to load one channel at a time from disk.

**Memory savings:** ~70-90% for posts. Only the currently-processed channel is
in RAM.

**Drawbacks:**

- Adds disk I/O overhead and temporary disk space requirements (could be
  comparable to the original ZIP size).
- Adds a dependency if using an embedded DB, or serialization complexity for
  raw files.
- Temp file cleanup on error/interrupt needs careful handling.
- Slower than pure in-memory processing for small exports.

### Optimization 3: Eliminate `SlackExport`/`Intermediate` overlap

**Description:** Free the `SlackExport.Posts` entries as they're consumed by
`TransformPosts`, so both representations don't coexist.

**Changes required:**

- In `TransformPosts`, after processing each channel's posts from
  `slackExport.Posts[channelName]`, delete the entry:
  `delete(slackExport.Posts, channelName)`.
- Set `slackExport.Posts = nil` after `TransformPosts` completes.

**Memory savings:** ~30-40% reduction in peak memory. The GC can reclaim
`SlackPost` data as `IntermediatePost` data is built.

**Drawbacks:**

- Minimal. The `SlackExport` is not reused after transformation, so this is
  safe.
- Requires that mention/markup conversion is done before (or during)
  per-channel transformation, not as a separate pass over all posts. Currently
  it's done before Transform, so this is already satisfied.

### Optimization 4: Fix `SlackParseUsers` redundant parsing

**Description:** Remove the debug-only triple-parse of user data.

**Changes required:**

- Remove the second `json.Unmarshal(b, &usersAsMaps)` call at
  `parse.go:34-35`.
- Remove the re-marshal at `parse.go:42-47`.
- Use `json.NewDecoder` (streaming) like `SlackParseChannels` does, instead of
  `io.ReadAll`.

**Memory savings:** Small in absolute terms (users data is usually <10MB), but
eliminates 3x amplification.

**Drawbacks:** Loss of detailed debug logging for user data. Could be gated
behind a `debug` flag if needed.

### Optimization 5: Compile mention regexes lazily / use string replacement

**Description:** Many Slack mentions follow a fixed pattern
(`<@UXXXXX|username>`) that can be matched with a single regex + a lookup
table, instead of compiling N separate regexes.

**Changes required:**

- Replace the per-user/per-channel regex maps with a single regex like
  `<@(U[A-Z0-9]+)(?:\|[^>]*)?>` paired with a `map[string]string` for
  ID→username lookup.
- Use `ReplaceAllStringFunc` with the lookup map.

**Memory savings:** Eliminates O(N) compiled regex objects (each
`*regexp.Regexp` can be several KB). For a workspace with 100K users, this
could save hundreds of MBs.

**Drawbacks:**

- Slightly different matching semantics — need to verify edge cases (e.g.,
  mentions without the `|username` part).
- The single-regex approach may be marginally slower per-match for simple
  cases, but dramatically faster overall since you iterate posts once instead of
  N times per post.

### Optimization 6: Stream JSONL export per-channel (partial)

**Description:** Currently `Export()` writes channels, users, then all posts.
If combined with Optimization 1, posts could be written as they're transformed
per-channel, eliminating the need to hold `Intermediate.Posts` at all.

**Changes required:**

- Export version, channels, and users first (these are small).
- Then iterate channels, transform posts for each, and write directly to the
  JSONL file.
- Remove `Intermediate.Posts` field entirely.

**Memory savings:** Eliminates the entire `[]*IntermediatePost` accumulation —
near-zero post memory overhead.

**Drawbacks:**

- Posts must be grouped by channel in the JSONL output (this is acceptable for
  Mattermost bulk import).
- Cannot do global post deduplication or cross-channel post ordering if needed.
- Harder to provide a progress bar or total count upfront.

### Optimization 7: Process ZIP entries without full central directory

**Description:** For TB-scale ZIPs, switch from `zip.NewReader` (which loads
the full central directory) to streaming through the ZIP sequentially, or use a
custom ZIP reader that processes entries on the fly.

**Changes required:**

- Use a streaming approach: either pre-index the ZIP to know file offsets, or
  process in two passes (first pass: index channel→file mappings; second pass:
  seek and read per-channel).
- Alternatively, require the export to be extracted to a directory first, and
  process files from the filesystem.

**Memory savings:** Avoids loading the ZIP central directory (can be 100s of
MBs for very large archives).

**Drawbacks:**

- Go's standard `archive/zip` doesn't support streaming; requires a custom
  implementation or a third-party library.
- If requiring pre-extraction, changes the user workflow.
- Seeking within a ZIP file is fast on SSDs but slow on HDDs or network
  storage.

## Prioritized Recommendation

For maximum impact with minimal risk:

| Priority | Optimization | Effort | Memory Savings | Risk |
|----------|-------------|--------|----------------|------|
| 1 | **#3** — Free SlackExport entries during transform | Low | ~30-40% peak | Very low |
| 2 | **#4** — Fix SlackParseUsers redundant parsing | Low | Small but free | Very low |
| 3 | **#5** — Single regex for mentions | Medium | Moderate + CPU savings | Low |
| 4 | **#1+#6** — Channel-at-a-time streaming pipeline | High | ~70-90% | Medium |
| 5 | **#2** — Disk-backed intermediate storage | High | ~70-90% | Medium |
| 6 | **#7** — Streaming ZIP processing | High | Moderate | High |

Optimizations #3 and #4 are low-hanging fruit that can be implemented in hours.
Optimization #5 is a medium-effort win. For truly TB-scale exports, the
architecture needs to shift to #1+#6 (channel-at-a-time streaming), which is
the most impactful single change but requires significant refactoring of the
pipeline.
