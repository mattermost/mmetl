# Confluence import bundle contract, version 2

This document is normative. It defines the bundle that `mmetl transform confluence`
produces and that the Mattermost Docs plugin importer consumes. Both sides mirror it:

| Side | Implementation | Golden fixtures |
|---|---|---|
| Producer | `services/confluence/contract.go`, `services/confluence/validation.go` | `services/confluence/testdata/contract/` |
| Consumer | `mattermost-plugin-docs/server/importer/contract.go` | `mattermost-plugin-docs/server/importer/testdata/contract/` |

Nothing in this document may change without changing both implementations, both
fixture sets, and the implementation plan in the same review. A consumer that
accepts a bundle this document rejects is a bug, and so is the reverse.

## 1. Layout

```text
import-manifest.json
import.jsonl
data/<page-source-id>/<attachment-source-id>/<sanitized-filename>
```

ZIP entry order is the manifest, then the JSONL, then attachment paths in
lexical order. Entries use deflate and a Unix-epoch modification time so the
same input produces the same bytes. `import-manifest.json.created_at` is the
only intentionally variable field in non-golden output; golden tests inject a
fixed clock.

No other entry may appear. A bundle carrying an attachment blob no line declares
is rejected, as is a line declaring a blob the bundle does not carry.

## 2. Canonical encoding

* `import.jsonl`: one JSON object per line, compact, no HTML escaping, each line
  terminated by a single `LF`.
* `import-manifest.json`: the same encoding, indented with two spaces, with a
  trailing `LF`.
* Object keys are emitted in sorted order. Every dynamic object in the contract
  is a string-keyed map, so this falls out of the Go encoder.
* Go's default `encoding/json` escaping of `&`, `<`, and `>` is **not** used. It
  would change the bytes and therefore every checksum.
* Both sides decode with unknown fields disallowed, so producer drift fails
  loudly instead of silently dropping data.

## 3. Line sequence

```text
version                       exactly one, first line
space                         exactly one, second line
page...                       zero or more, parent strictly before child
page_comment...               zero or more, thread root strictly before descendants
resolve_space_placeholders    exactly one, last line
```

A line whose `type` does not match the payload it carries is rejected, as is a
line carrying no payload at all. `resolve_space_placeholders` is an ordering
sentinel with an empty payload: it exists so a truncated stream is always
detectable. A missing or duplicated sentinel is rejected. The importer performs
placeholder resolution during page execution, once destination mappings exist,
not when it reads this line.

## 4. Line payloads

See `services/confluence/contract.go` for the exact Go structs and JSON tags. The
rules that are not expressible as a struct:

### version

* `version` is `2`. No other value is accepted.
* `source.organization_id`, `source.space_id`, and `source.space_key` are all
  non-empty.
* `source.space_id` is the numeric source Space **object ID**, never the key.
  Every source ID in the bundle is interpreted only inside
  `(organization_id, space_id)`.

### space

* `space.props.import_source_id` equals `source.space_id`.
* `space.title` is non-empty and at most 255 runes.
* `space.team` is the team every page must also name.

### page

* `page.space_import_source_id` equals `source.space_id`.
* `page.team` equals `space.team`.
* `page.user` is non-empty and appears as a `mattermost_username` in the manifest.
* `page.title` is non-empty and at most 255 runes.
* `page.content` is non-empty serialized TipTap JSON, at most 2 MiB. A page with
  no usable source body carries exactly
  `{"type":"doc","content":[{"type":"paragraph"}]}`.
* `page.parent_import_source_id`, when set, names a page emitted **earlier** in
  the stream and is never the page's own ID.
* Required `page.props`:

  | Key | Type | Rule |
  |---|---|---|
  | `import_source_id` | string | non-empty, unique across pages |
  | `import_source` | string | `"confluence"` |
  | `confluence_space_key` | string | equals `source.space_key` |
  | `confluence_content_type` | string | `"page"` or `"blogpost"` |
  | `confluence_author_account_id` | string | may be empty; when set, must appear in manifest users |
  | `import_labels` | array of string | may be empty |
  | `confluence_labels` | array of `{name, namespace}` | may be empty |
  | `confluence_restrictions` | object | may be empty; see section 8 |

* Serialized `page.props` is at most 64 KiB. The producer targets 48 KiB to
  leave the importer room for its own metadata namespace.

### page attachments

* `path` is exactly `data/<page import_source_id>/<attachment import_source_id>/<sanitized filename>`:
  four `/`-separated segments, already cleaned, relative, no backslash, no NUL,
  no `.` or `..` segment. Segments 2 and 3 must match the owning page and the
  attachment's own `import_source_id`.
* Required `props`:

  | Key | Type | Rule |
  |---|---|---|
  | `import_source_id` | string | non-empty, unique across the whole bundle |
  | `confluence_container_source_id` | string | original container; differs from the page for space-description attachments |
  | `filename` | string | original filename, before sanitation |
  | `media_type` | string | may be empty |
  | `size` | integer | `>= 0`, equals the blob's byte length |
  | `sha256` | string | 64 lowercase hex characters, of the blob's bytes |

### page_comment

* `page_import_source_id` names a page emitted earlier in the stream.
* `parent_comment_import_source_id`, when set, names a comment emitted earlier
  on the **same page**.
* `thread_root_import_source_id` is never empty. For a top-level comment it is
  that comment's own `props.import_source_id`. For every descendant, however
  deep, it is the top-level Confluence comment's source ID, matching the
  parent's thread root.
* `user` appears as a `mattermost_username` in the manifest.
* Required `props`: `import_source_id` (unique across comments), `import_source`
  (`"confluence"`), and `confluence_author_account_id` (may be empty; when set,
  must appear in manifest users).

## 5. Manifest

See `Manifest` in `services/confluence/contract.go` for the exact shape. Rules:

* `version` is `"2"`, `generator` is `"mmetl-confluence-xml"`,
  `source.type` is `"confluence"`.
* `source.organization_id`, `source.space_id`, `source.space_key`, and
  `target.team` are non-empty and agree with the `version` line.
* Counts describe **producer discovery and emission**, never successful
  destination import. `spaces_emitted`, `pages_emitted`, `blogposts_emitted`,
  `comments_emitted`, `attachments_emitted`, and `users_emitted` must agree
  exactly with the stream.
* `users[]` has unique `account_id` and unique `mattermost_username`.
  `username_proposal_source` is one of `explicit_mapping`, `source_username`,
  `source_email`, `source_display_name`, `fallback`.
* Every `fidelity` field is non-empty. The fidelity block states what the
  bundle actually claims. `page_restrictions` remains
  `restriction_extraction_unverified`: user restrictions are now extracted from
  a real export, but group restrictions are not yet verified and nothing is
  enforced at the destination. See section 8.
* `warnings` are sorted by code, entity type, source ID, then message; each
  message is at most 2048 UTF-8 bytes and contains no body text or email.
* `errors` **must be empty**. A manifest carrying an error describes a bundle
  that is not importable. Recoverable entity problems belong in `warnings` and
  the `*_skipped` counts.

## 6. Checksums

* `checksums.jsonl_sha256` is the SHA-256 of the exact `import.jsonl` bytes.
* `checksums.attachments_sha256` is the SHA-256 over all attachments in lexical
  path order, each framed as:

  ```text
  uint64 big-endian path byte length
  path UTF-8 bytes
  uint64 big-endian file byte length
  file bytes
  ```

  The length prefixes are load-bearing: without them
  `("data/1/2/ab.txt", "")` and `("data/1/2/a", "b.txt")` would collide. A
  bundle with no attachments hashes the empty byte stream.
* Each attachment additionally carries its own `props.sha256`.

## 7. Placeholders

Only these three placeholders may appear, and only inside typed TipTap
attributes. They are never substituted into serialized JSON as strings; both
sides walk the parsed tree.

```text
{{CONF_PAGE_ID:<source-page-id>}}          link mark href only
{{CONF_ATTACHMENT_ID:<source-attachment-id>}}  link mark href or image node src
{{CONF_USER_ID:<canonical-account-id>}}    mention node attrs.id only
```

A mention's `attrs.label` carries the bounded source display name or proposed
username. The exporter resolves title and filename references to source IDs
before serialization; an ambiguous reference becomes visible text plus a
warning, never a title-based placeholder.

The importer rewrites approved attributes only:

```text
page       -> /<url-escaped-team-name>/spaces/<destination-space-id>/<destination-page-id>
attachment -> /api/v4/files/<destination-file-id>
mention    -> attrs.id becomes the Mattermost user ID, attrs.label the destination username
```

An unresolved or cross-space reference keeps its visible text, loses the
unresolved executable attribute, and produces a warning.

## 8. Page restrictions

A restricted page carries `confluence_restrictions` in its props:

```json
{
  "view_users":  ["<canonical-account-id>"],
  "view_groups": ["<source-group-name>"],
  "edit_users":  ["<canonical-account-id>"],
  "edit_groups": ["<source-group-name>"]
}
```

An unrestricted page carries `{}`. Every list is present when any restriction
exists, so a consumer never has to distinguish absent from empty.

Users are named by canonical account ID, never by Confluence key: the importer
resolves account IDs only. A restricted user who is not in the bundle's user
list is **omitted** rather than emitted as a raw key, because a key the importer
cannot resolve is indistinguishable from a user it has not created yet.

Confluence stores these as a `ContentPermissionSet` per restriction kind
(`View` or `Edit`) naming the page, holding `ContentPermission` rows that each
name either a `userSubject` or a `groupName`. The exporter reads that structure
directly.

**What is and is not verified.** The user form is confirmed against a real
Confluence Cloud export. The group form is not: no export seen so far contains
a group-restricted page, so the exporter emits a
`restriction_extraction_unverified` warning naming the group whenever it
encounters one. An unrecognized restriction kind is reported and dropped rather
than guessed at.

**Nothing enforces any of this.** The restrictions are inert metadata. Access at
the destination is Space-level, and every export says so in a warning whether or
not it found a restriction — silence would read as "nothing here is restricted".

## 9. Golden fixtures

`services/confluence/testdata/contract/` holds the shared fixture set. It is
generated from the builders in `services/confluence/contract_test.go`:

```shell
go test ./services/confluence/... -run TestContractFixtures -update
```

The fixtures are committed unpacked, as plain files, so they can be reviewed in
a diff. `ValidateBundleFS` accepts an `fs.FS`, so the same code validates an
unpacked fixture directory and a `*zip.Reader` over a real bundle.

| Fixture | Expectation |
|---|---|
| `minimal` | accepted: one space, one page carrying the canonical empty document, one user, no attachments or comments |
| `full` | accepted: every contract field, both content types, a page hierarchy, all three placeholder kinds, both attachment container shapes, a comment thread with a descendant, a resolved comment, labels, synthetic restrictions, all three user shapes, sorted warnings |
| `invalid-sequence` | rejected: the sentinel precedes the comments |
| `invalid-missing-sentinel` | rejected: no trailing sentinel |
| `invalid-duplicate-sentinel` | rejected: two sentinels |
| `invalid-duplicate-ids` | rejected: two pages share `import_source_id` |
| `invalid-bad-parent` | rejected: a page names a parent that was never emitted |
| `invalid-unsafe-path` | rejected: an attachment path escapes the bundle root |
| `invalid-missing-attachment` | rejected: a declared attachment blob is absent |
| `invalid-bad-checksum` | rejected: `jsonl_sha256` does not match the stream |

Each invalid fixture is generated by mutating exactly one thing in `full` and
recomputing the manifest, so it fails on the rule it names and on nothing else.
No task may alter fixture semantics on one side only: the Docs importer keeps a
byte-identical copy and asserts the same accept/reject outcomes.
