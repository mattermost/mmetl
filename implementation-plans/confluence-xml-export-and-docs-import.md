# Confluence XML Export and Mattermost Docs Import — Implementation Playbook

## Purpose

This is an execution plan, not a design sketch. It is written so that a smaller implementation model can complete one task at a time without inventing requirements or architecture.

The implementer MUST:

1. Read this file before every task.
2. Implement exactly one numbered task at a time.
3. Run that task's focused tests before proceeding.
4. Stop at every explicit stop condition.
5. Never change the bundle contract without first updating this plan and the golden fixtures in both repositories.
6. Never merge or cherry-pick either reference branch. Reference code may be read and individual ideas may be reimplemented deliberately.
7. Avoid unrelated refactors.

## Repository baselines

The repositories use `master`, not `main`. In earlier discussion, “start from main” means “start from the current canonical default branch.”

At plan revision time:

```text
mmetl canonical ref:
  origin/master@07f81df74c218061981a97feb746231919c4f058

mattermost-plugin-docs canonical ref:
  origin/master@c71e3acb96ab1bb45af69835fd383b390523efde

mmetl read-only reference:
  worktree-confluence-csv-parser
  reference commit b8eacc0b04751af4db27735f5cfa893dd5565490

Docs read-only reference:
  worktree-confluence-page-import-on-master
  reference commit a5fd86e958258af21b056f6a21a743087f0af05d
```

Before implementation:

1. Fetch each repository.
2. Create clean worktrees from the latest `origin/master`.
3. Record the actual base SHAs in this section.
4. Create independent feature branches:

   ```text
   mmetl:                 confluence-xml-export
   mattermost-plugin-docs: confluence-xml-import
   ```

Do not work from either dirty local `master` checkout.

### E0 execution record (2026-09-01)

Both repositories were fetched at execution time. The pinned canonical SHAs above were
re-verified and are unchanged, so the plan baseline still holds.

```text
mmetl base:
  origin/master@07f81df74c218061981a97feb746231919c4f058
  branch    worktree-confluence-xml-export
  worktree  <mmetl>/.claude/worktrees/confluence-xml-export

mattermost-plugin-docs base:
  origin/master@c71e3acb96ab1bb45af69835fd383b390523efde
  branch    worktree-confluence-xml-import
  worktree  <mattermost-plugin-docs>/.claude/worktrees/confluence-xml-import
```

Branch names carry the tooling's `worktree-` prefix; they are otherwise the
`confluence-xml-export` / `confluence-xml-import` branches required above.
Both read-only reference worktrees were left untouched.

## Input used for discovery

Private local fixture, never commit directly:

```text
/Users/willyfrog/Downloads/Confluence-export.zip
```

Observed archive layout:

```text
entities.xml
exportDescriptor.properties
attachments/<container-content-id>/<attachment-id>/<version>
plugin-data/.../*.pdata
```

Observed entities include spaces, pages, blog posts, comments, attachments, body content, content properties, users, labels, space permissions, drafts, deleted content, and historical versions. The sample contains no page-restriction entity, so page-restriction extraction remains explicitly unverified.

---

# 1. Frozen product decisions

These are not implementation choices.

| Topic | Required behavior |
|---|---|
| Input | A Confluence Cloud XML backup ZIP containing root `entities.xml` and `exportDescriptor.properties`. CSV is unsupported. |
| Branches | Both implementations start independently from canonical `origin/master`. Reference branches remain untouched. |
| Space scope | Exactly one source space per invocation and one output bundle. |
| Selection | Accept exact source space ID, key, or unique exact name. Missing/ambiguous selection fails and prints valid spaces. |
| Included content | Current pages, current blog posts, valid current comments, current attachments, selected-space users, labels, and page-restriction metadata when recognizable. |
| Excluded content | Historical versions, drafts, deleted/trashed/archived content, audit data, notifications, arbitrary plugin state, space/global permissions. |
| Blog posts | Import as ordinary Docs pages with `confluence_content_type=blogpost`. |
| Missing body | Emit an empty page and a warning. |
| Broken comments | Skip a comment if its parent is missing/skipped; also skip descendants that depend on it. |
| Attachments | Copy bytes into the bundle and list each attachment on its destination page. |
| Space-description attachment | Assign it to the selected source home page. Skip with warning if that page is not emitted. |
| Missing attachment blob | Warn, skip that attachment, continue. |
| Attachment bundle path | `data/<page-source-id>/<attachment-source-id>/<sanitized-filename>`. |
| Users | Include only users referenced by selected content, mentions, attachments, labels, or page restrictions. |
| External mapping | Explicit mapping overrides all backup-derived Mattermost username proposals. |
| Destination user lookup | Done only by the Docs plugin, never by `mmetl`. |
| Existing real email | Reuse the Mattermost user with the same normalized real email. |
| Missing destination user | Create it. |
| Missing source email | Use a deterministic placeholder email under `users.invalid`. |
| Inactive source user | If newly created by this import, create temporarily active, import attributed content, then deactivate. Never deactivate a pre-existing reused Mattermost user. |
| External auth ID | Preserve only. Do not set or match Mattermost `AuthService`/`AuthData` yet. |
| Comments | Import as Mattermost posts in the Docs Space backing channel. |
| Labels | Preserve in source metadata and hashes. Do not build label UI/search yet. |
| Page restrictions | Preserve when recognizable. Do not enforce. Reports must state access remains Space-level. |
| Space/global permissions | Future work only. |
| Multi-space output | Future work only. |
| Import UI | API-only first iteration. Do not build a webapp wizard. |
| HA | First iteration is single-node only. Reject import creation while Mattermost clustering is enabled. |

---

# 2. Explicit first-iteration constraints

These constraints prevent the implementer from guessing around missing infrastructure.

1. The exporter is offline and never contacts Mattermost.
2. The Docs importer is available only to Mattermost system administrators.
3. The importer always creates a new Docs Space for a source namespace on first import. Reimports reuse the source-space mapping. It never imports into an arbitrary pre-existing Space.
4. Imported users are not automatically added to the team or Space. End-to-end tests must confirm pages/posts/files can retain the requested author without membership. If Mattermost rejects this, STOP for a product decision; do not silently add memberships or reattribute content.
5. A reused Mattermost user is never renamed, reactivated, or deactivated by this importer.
6. A user created by this importer is “managed by import.” Managed users may be updated on reimport and may be deactivated when the source is inactive.
7. Import bundles are retained on local disk until the job is terminal. This is why HA is unsupported in the first iteration.
8. The API rejects new imports when `ClusterSettings.Enable` is true, returning stable error `import_ha_not_supported` with HTTP 501.
9. No mutation occurs before explicit confirmation. Bundle upload and local-disk staging are not considered Mattermost data mutation.
10. Page-restriction parser completion is blocked until a real restricted-page XML fixture is available. The contract and importer storage are implemented now; XML extraction remains marked experimental/unverified.

---

# 3. Normative command-line contract

## 3.1 List spaces

```shell
mmetl transform confluence \
  --file /path/to/Confluence-export.zip \
  --list-spaces
```

Rules:

- `--file` is required.
- `--list-spaces` is mutually exclusive with `--space`, `--organization-id`, `--team`, `--output`, `--user-mapping`, `--skip-attachments`, and `--validate-only`.
- It prints no page titles, emails, usernames, or body content.
- Output columns are `KEY`, `ID`, `TYPE`, `STATUS`, `NAME`.
- Sort by key, then ID.

## 3.2 Create bundle

```shell
mmetl transform confluence \
  --file /path/to/Confluence-export.zip \
  --space ENG \
  --organization-id https://confluence.example.com \
  --team target-team \
  --output confluence-eng.zip
```

Required flags:

- `--file`
- `--space`
- `--organization-id`
- `--team`

Optional flags:

- `--output`: default `<sanitized-space-key>-confluence-docs.zip`.
- `--user-mapping`: explicit UTF-8 CSV mapping file.
- `--skip-attachments`: emit no attachment metadata or bytes and add one warning/count.
- `--validate-only`: parse, transform, and validate without creating output.
- `--debug`: debug logging.

Do not add destination URL/token flags. Do not add CSV support. Do not add a configurable hierarchy depth; Docs depth is fixed at 10.

## 3.3 Organization identity

`--organization-id` is mandatory because the sample descriptor does not contain a stable site identifier.

Rules:

- Trim surrounding whitespace.
- Preserve the resulting string exactly in the manifest and version line.
- Reject empty, NUL-containing, or values longer than 1024 UTF-8 bytes.
- The user must supply the same value for later exports from the same Confluence site.
- Source mappings are scoped by `(organization_id, space_id)`, never by bare page IDs or space keys.

## 3.4 Space selector

Resolution order:

1. Exact numeric source space ID.
2. Exact case-sensitive space key.
3. Unique case-insensitive space key.
4. Exact case-sensitive name if unique.
5. Unique case-insensitive name.

If zero or multiple matches remain, fail and print the valid-space table.

## 3.5 Explicit mapping CSV

Header is mandatory and exact:

```csv
confluence_account_id,confluence_user_key,confluence_username,confluence_email,mattermost_username
```

Rules:

- `mattermost_username` is required for every row.
- At least one source selector column is required.
- Account ID and user key match exactly.
- Username and email match case-insensitively after trimming.
- Exact duplicate rows are ignored.
- Conflicting rows for the same selector are a hard error.
- Match precedence is account ID, user key, email, username.
- The selected row sets manifest `username_proposal_source=explicit_mapping`.

---

# 4. Normative source-archive rules

Required normalized root entries:

```text
entities.xml
exportDescriptor.properties
```

Accepted data entries:

```text
attachments/<numeric-container-id>/<numeric-attachment-id>/<numeric-version>
```

Ignored entries:

```text
plugin-data/**
```

Path normalization:

1. Convert neither backslashes nor drive prefixes; reject both.
2. Remove any number of leading literal `./` segments.
3. Reject NUL, empty path, absolute path, `.`/`..` after normalization, symlink, device, encrypted entry, unsupported compression method, duplicate raw path, and duplicate normalized path.
4. The sample's leading `./plugin-data/...` is therefore tolerated and ignored.
5. Ignore unknown safe root entries with warning `source_archive_unknown_entry`; never extract them.

Use `archive/zip` and stream entry bodies. Never extract the source archive wholesale.

---

# 5. Normative XML object model

Create these exact generic types in `services/confluence/xml_entities.go`:

```go
type EntityKey struct {
    Package string
    Class   string
    IDName  string
    ID      string
}

type RawScalar struct {
    Present bool
    Value   string
}

type RawObject struct {
    Key         EntityKey
    Scalars     map[string]RawScalar
    References  map[string]EntityKey
    Collections map[string][]EntityKey
}
```

Key rules:

- Entity identity is `(Package, Class, IDName, ID)`.
- Class matching always checks package and class.
- `ConfluenceUserImpl` uses `IDName=key`.
- Duplicate non-composite entity keys are a hard error.
- Composite IDs may be decoded for diagnostics but are not imported in this iteration.
- Missing scalar and present-empty scalar remain distinguishable.
- Unknown scalar/collection names on known classes are ignored with bounded debug logging.
- Unknown referenced classes create structured warning `xml_unknown_reference_class` only when selected content depends on them.
- External entities and DTD declarations are rejected.

Create `Decoder.Next() (*RawObject, error)` using `encoding/xml.Decoder`. It returns one completed top-level Hibernate `<object>` at a time and releases decoder state before reading the next object.

Date rules:

- Parse explicit RFC3339 offsets when present.
- Otherwise parse `yyyy-MM-dd HH:mm:ss.SSS` in `exportDescriptor.properties` `timezoneId`.
- Missing/invalid required creation time emits warning and omits the JSON field; it must never create a year-1 negative timestamp.

---

# 6. Exact filtering predicates

Implement pure functions with table-driven tests in `content_filter.go`.

## 6.1 Canonical page/blog predicate

A Page or BlogPost is emitted only if all are true:

```text
direct space reference == selected Space EntityKey
lower(contentStatus) == "current"
originalVersion reference is absent
originalVersionId is absent, empty, or numeric zero
object is not listed in another canonical object's historicalVersions collection
```

If more than one object claims to be canonical for the same logical source content, fail with `content_duplicate_canonical`.

## 6.2 Comment predicate

A Comment is initially eligible only if:

```text
lower(contentStatus) == "current"
originalVersion reference is absent
originalVersionId is absent, empty, or numeric zero
containerContent points to an emitted Page or BlogPost
```

Then build the comment graph:

- Parent absent: top-level comment.
- Parent eligible and on the same destination page: retain.
- Parent missing, excluded, on another page, or cyclic: skip this comment and all descendants.
- Emit one structured warning per skipped root cause, not one per descendant.

## 6.3 Attachment predicate

An Attachment is eligible only if:

```text
lower(contentStatus) == "current"
originalVersion reference is absent
its container is an emitted Page/BlogPost
OR its container is the selected SpaceDescription
```

SpaceDescription attachments map to the selected source home-page ID. If the source home page is not emitted, skip and warn.

## 6.4 Hierarchy

- Use Page/BlogPost typed `parent` as authoritative.
- Root depth is 1, matching `model.MaxPageDepth` in Docs.
- Missing/excluded parent promotes the page to root with warning `page_parent_missing_promoted`.
- Any cycle is a hard error.
- For original depth greater than 10, reparent the page to the nearest emitted ancestor whose output depth is 9, making the page depth 10.
- Recompute descendant depths after every flattening decision.
- Sort siblings by source position, then create time, then numeric source ID when numeric, then lexical source ID.

---

# 7. Selected-space data model

Create provider-owned models in `services/confluence/types.go`. Do not use `services/intermediate.Exporter`; it emits the standard Mattermost bulk-import contract, while this feature emits the custom Docs v2 contract.

Required aggregate:

```go
type SelectedSpace struct {
    Space       Space
    Pages       []*Page
    Comments    []*Comment
    Attachments map[string][]*Attachment // destination page source ID
    Users       map[string]*User         // canonical source account ID
    Warnings    []Warning
    Stats       Stats
}
```

A `Page` must contain source ID, source class (`page` or `blogpost`), title, output parent source ID, raw body handle, creator/modifier source IDs, timestamps, source position, labels, restrictions, and attachments.

A `Comment` must contain source ID, page source ID, immediate parent source ID, thread-root source ID, converted message, creator/modifier source IDs, timestamps, resolved state, and source props.

An `Attachment` must contain source ID, original container ID, destination page source ID, version, original filename, sanitized filename, MIME type, expected size, archive path, SHA-256, and source author.

---

# 8. Streaming passes

Do not load the complete XML or all bodies into memory.

## Pass 1 — catalog

Read only descriptor, Space, SpaceDescription, and source home-page relationships. Resolve `--space` after this pass.

## Pass 2 — canonical content metadata

Read Page and BlogPost metadata. Select canonical current objects with the predicate in section 6. Record hierarchy, body IDs, content-property IDs, authors, and historical-version membership. Do not retain body strings.

## Pass 3 — dependencies

With selected content IDs known, read Comment, Attachment, Labelling, Label references, known restriction objects, and user references. Build comment filtering and destination attachment ownership.

## Pass 4 — payloads

Read only selected BodyContent, ContentProperty, Label, ConfluenceUserImpl, and InternalUser objects.

Body storage:

- Bodies up to 1 MiB may remain in memory.
- Larger bodies are written to a command-scoped temporary directory as `0600` files.
- Temporary directory mode is `0700`.
- Cleanup occurs on every return path.

Expected memory complexity is selected metadata plus one active body/attachment stream, not total body or attachment bytes.

---

# 9. User derivation

Join:

```text
content user reference
  -> ConfluenceUserImpl.key
  -> name/lowerName/atlassianAccountId
  -> InternalUser.lowerName
  -> displayName/email/externalId/active
```

Canonical manifest `account_id` precedence:

1. Non-empty Atlassian account ID.
2. Confluence user key.
3. SHA-256 of `organization_id + NUL + package + NUL + class + NUL + source ID`, encoded lowercase hex.

Always retain `confluence_user_key` separately when available.

Included users are those referenced by selected page/blog/comment creators or modifiers, attachment authors, mention targets, label owners, or recognized page restrictions. Do not include every site user.

Username proposal precedence:

1. Explicit mapping.
2. Valid Confluence username.
3. Local part of a real email.
4. Slugified display name.
5. `confluence_user_<first-12-hex-of-sha256-account-id>`.

Sanitize to Mattermost username rules. Resolve proposal collisions within the bundle by appending `_2`, `_3`, etc. in canonical account-ID order.

Placeholder email:

```text
confluence-<first-24-hex-of-sha256-organization-and-account>@users.invalid
```

Set `email_is_placeholder=true`. Placeholder emails are never used by the importer to match an existing user.

Manifest field `username_proposal_source` is required and one of:

```text
explicit_mapping
source_username
source_email
source_display_name
fallback
```

---

# 10. Storage-format conversion contract

Canonical empty document:

```json
{"type":"doc","content":[{"type":"paragraph"}]}
```

The exporter must ensure every body passes equivalent constraints to current Docs:

- Page title: at most 255 runes.
- Body: at most 2 MiB serialized JSON.
- Search projection: at most 2 MiB.
- Props: at most 64 KiB; exporter targets at most 48 KiB to leave importer metadata room.
- TipTap nodes: at most 50,000.
- TipTap depth: use the current Docs parser limit.

Policies:

- Empty title becomes `Untitled <source-id>` with warning.
- Long title is truncated to 255 runes with warning.
- Body, node-count, TipTap-depth, or source-props overflow is a hard per-page conversion error. The page is skipped, descendants are promoted to root, and comments/attachments targeting it are skipped. Record the page as skipped; do not fail unrelated pages.
- Dangerous URLs are removed with warning.

Minimum conversion matrix:

| Confluence input | TipTap output | Fallback/warning |
|---|---|---|
| `p`, text, `br` | paragraph/text/hardBreak | plain text |
| `h1`…`h6` | heading level 1…6 | paragraph |
| `strong`, `b` | bold mark | text |
| `em`, `i` | italic mark | text |
| `u` | underline mark if accepted; otherwise text | warning |
| `s`, `strike` | strike mark | text |
| `a href` | link mark after URL sanitation | text + warning |
| `ul`, `ol`, `li` | bulletList/orderedList/listItem | paragraphs |
| `blockquote` | blockquote | paragraph |
| `pre`, `code` | codeBlock/code mark | plain text |
| `table`, `tr`, `th`, `td` | table/tableRow/tableHeader/tableCell | flattened paragraphs if invalid |
| `hr` | horizontalRule if accepted | paragraph separator |
| Confluence code macro | codeBlock | bounded plain text |
| info/note/warning/tip macro | callout | blockquote if callout rejected |
| children/pagetree macro | bullet list of selected child links | visible fallback text |
| Jira macro | linked key when URL exists, otherwise key text | warning if no key |
| status macro | text containing status | none |
| unsupported macro | paragraph `[Unsupported Confluence macro: <name>]` plus extracted plain text | `unsupported_macro` |
| `ac:image` attachment | image node with source attachment placeholder | text link if unresolved |
| attachment link | link mark with source attachment placeholder | filename text |
| user reference | mention node with source user placeholder | `@proposed_username` text |
| page reference | link mark with source page placeholder | page title text |

Implement the converter using a parsed tree. Do not implement the conversion as a sequence of regex replacements and never mutate serialized TipTap JSON.

---

# 11. Placeholder contract

Only these placeholders are allowed, only in typed TipTap attributes:

```text
{{CONF_PAGE_ID:<source-page-id>}}
{{CONF_ATTACHMENT_ID:<source-attachment-id>}}
{{CONF_USER_ID:<canonical-account-id>}}
```

Rules:

- Page placeholder appears only in link-mark `href`.
- Attachment placeholder appears only in link-mark `href` or image-node `src`.
- User placeholder appears only in mention-node `attrs.id`; `attrs.label` contains the bounded source display/proposed username.
- Exporter resolves title/filename references to source IDs before serialization. Ambiguous title/filename references become visible text and warnings; no title-based placeholder is emitted.
- Importer walks the parsed TipTap tree and rewrites approved attributes only.
- Destination page href is relative:

  ```text
  /<url-escaped-team-name>/spaces/<destination-space-id>/<destination-page-id>
  ```

- Destination file href/src is:

  ```text
  /api/v4/files/<destination-file-id>
  ```

- Destination mention replaces `attrs.id` with Mattermost user ID and `attrs.label` with destination username.
- Unknown/cross-space references retain visible text but lose the unresolved executable attribute and generate a warning.

The trailing `resolve_space_placeholders` line is an ordering sentinel. The importer performs resolution during page execution after mappings are known; it must reject a missing or duplicate sentinel.

---

# 12. Exact bundle contract (version 2)

## 12.1 Layout

```text
import.jsonl
import-manifest.json
data/<page-source-id>/<attachment-source-id>/<sanitized-filename>
```

ZIP entry order is manifest, JSONL, then attachment paths sorted lexically. Use deflate. Set ZIP entry timestamps to Unix epoch for deterministic bytes. Manifest `created_at` is the only intentionally variable field in non-golden output; golden tests inject a fixed clock.

## 12.2 JSONL order

```text
version
space
page...             # parent before child
page_comment...     # thread root before descendants
resolve_space_placeholders
```

## 12.3 Go-equivalent contract structs

The producer and consumer must mirror these fields exactly.

```go
type Line struct {
    Type                     string                   `json:"type"`
    Version                  *int                     `json:"version,omitempty"`
    Source                   *Source                  `json:"source,omitempty"`
    Space                    *SpaceData               `json:"space,omitempty"`
    Page                     *PageData                `json:"page,omitempty"`
    PageComment              *PageCommentData         `json:"page_comment,omitempty"`
    ResolveSpacePlaceholders *ResolvePlaceholdersData `json:"resolve_space_placeholders,omitempty"`
}

type Source struct {
    OrganizationID string `json:"organization_id"`
    SpaceID       string `json:"space_id"`
    SpaceKey      string `json:"space_key"`
}

type SpaceData struct {
    Team        string         `json:"team"`
    Title       string         `json:"title"`
    Description string         `json:"description,omitempty"`
    Props       map[string]any `json:"props"`
}

type PageData struct {
    Team                 string           `json:"team"`
    SpaceImportSourceID  string           `json:"space_import_source_id"`
    User                 string           `json:"user"`
    Title                string           `json:"title"`
    Content              string           `json:"content"`
    ParentImportSourceID string           `json:"parent_import_source_id,omitempty"`
    CreateAt             int64            `json:"create_at,omitempty"`
    UpdateAt             int64            `json:"update_at,omitempty"`
    Props                map[string]any   `json:"props"`
    Attachments          []AttachmentData `json:"attachments,omitempty"`
}

type AttachmentData struct {
    Path  string         `json:"path"`
    Props map[string]any `json:"props"`
}

type PageCommentData struct {
    PageImportSourceID          string         `json:"page_import_source_id"`
    ParentCommentImportSourceID string         `json:"parent_comment_import_source_id,omitempty"`
    ThreadRootImportSourceID    string         `json:"thread_root_import_source_id"`
    User                        string         `json:"user"`
    Content                     string         `json:"content"`
    CreateAt                    int64          `json:"create_at,omitempty"`
    UpdateAt                    int64          `json:"update_at,omitempty"`
    IsResolved                  bool           `json:"is_resolved"`
    Props                       map[string]any `json:"props"`
}
```

Identity rules:

- `source.space_id`, `space.props.import_source_id`, and every page's `space_import_source_id` are the selected source Space object's numeric/string ID, not its key.
- `source.space_key` and `confluence_space_key` retain the human-readable source key.
- `page.props.import_source_id`, `comment.props.import_source_id`, and `attachment.props.import_source_id` are source entity IDs.
- All IDs are interpreted only inside `(organization_id, source space_id)`.

Required page props:

```text
import_source_id                 string
import_source                    "confluence"
confluence_space_key             string
confluence_content_type          "page" | "blogpost"
confluence_author_account_id     string
import_labels                    []string
confluence_labels                []{name:string, namespace:string}
confluence_restrictions          object; may be empty
```

Required attachment props:

```text
import_source_id                 string
confluence_container_source_id   string
filename                         string
media_type                       string; may be empty
size                             integer >= 0
sha256                           lowercase 64-char hex
```

Required comment props:

```text
import_source_id                 string
import_source                    "confluence"
confluence_author_account_id     string
```

For a top-level comment, `thread_root_import_source_id` equals that comment's own `props.import_source_id`. For every descendant, it equals the top-level Confluence comment source ID. It is never empty.

## 12.4 Manifest

```go
type Manifest struct {
    Version          string            `json:"version"` // "2"
    Generator        string            `json:"generator"` // "mmetl-confluence-xml"
    GeneratorVersion string            `json:"generator_version"`
    CreatedAt        time.Time         `json:"created_at"`
    Source           ManifestSource    `json:"source"`
    Target           ManifestTarget    `json:"target"`
    Counts           ManifestCounts    `json:"counts"`
    Checksums        ManifestChecksums `json:"checksums"`
    Users            []ManifestUser    `json:"users"`
    Fidelity         ManifestFidelity  `json:"fidelity"`
    Warnings         []Warning         `json:"warnings"`
    Errors           []Issue           `json:"errors"`
}

type ManifestSource struct {
    Type           string `json:"type"` // "confluence"
    OrganizationID string `json:"organization_id"`
    SpaceID       string `json:"space_id"`
    SpaceKey      string `json:"space_key"`
    SpaceName     string `json:"space_name"`
    ExportFile    string `json:"export_file"`
    TimezoneID    string `json:"timezone_id"`
}

type ManifestTarget struct {
    Team string `json:"team"`
}

type ManifestCounts struct {
    SpacesEmitted           int `json:"spaces_emitted"`
    PagesDiscovered         int `json:"pages_discovered"`
    PagesEmitted            int `json:"pages_emitted"`
    PagesSkipped            int `json:"pages_skipped"`
    BlogPostsEmitted        int `json:"blogposts_emitted"`
    CommentsDiscovered      int `json:"comments_discovered"`
    CommentsEmitted         int `json:"comments_emitted"`
    CommentsSkipped         int `json:"comments_skipped"`
    AttachmentsDiscovered   int `json:"attachments_discovered"`
    AttachmentsEmitted      int `json:"attachments_emitted"`
    AttachmentsSkipped      int `json:"attachments_skipped"`
    UsersEmitted            int `json:"users_emitted"`
    UsersInactive           int `json:"users_inactive"`
    UsersPlaceholderEmail   int `json:"users_placeholder_email"`
    LabelsPreserved         int `json:"labels_preserved"`
    RestrictedPagesPreserved int `json:"restricted_pages_preserved"`
    PagesFlattened          int `json:"pages_flattened"`
    MissingBodies           int `json:"missing_bodies"`
}

type ManifestChecksums struct {
    JSONLSHA256       string `json:"jsonl_sha256"`
    AttachmentsSHA256 string `json:"attachments_sha256"`
}

type ManifestFidelity struct {
    Pages           string `json:"pages"` // "imported"
    BlogPosts       string `json:"blogposts"` // "imported_as_pages"
    Comments        string `json:"comments"` // "imported_as_posts"
    Attachments     string `json:"attachments"` // "imported"
    Labels          string `json:"labels"` // "preserved_not_applied"
    PageRestrictions string `json:"page_restrictions"` // "restriction_extraction_unverified"
    SpacePermissions string `json:"space_permissions"` // "not_imported"
    ExternalAuth    string `json:"external_auth"` // "identifier_preserved_not_applied"
}

type Issue struct {
    Code       string `json:"code"`
    EntityType string `json:"entity_type,omitempty"`
    SourceID   string `json:"source_id,omitempty"`
    Message    string `json:"message"`
}

type ManifestUser struct {
    AccountID              string `json:"account_id"`
    ConfluenceUserKey      string `json:"confluence_user_key,omitempty"`
    ConfluenceUsername     string `json:"confluence_username,omitempty"`
    DisplayName            string `json:"display_name,omitempty"`
    Email                  string `json:"email"`
    ExternalID             string `json:"external_id,omitempty"`
    Active                 bool   `json:"active"`
    MattermostUsername     string `json:"mattermost_username"`
    EmailIsPlaceholder     bool   `json:"email_is_placeholder"`
    UsernameProposalSource string `json:"username_proposal_source"`
}

type Warning struct {
    Code       string `json:"code"`
    EntityType string `json:"entity_type,omitempty"`
    SourceID   string `json:"source_id,omitempty"`
    Message    string `json:"message"`
}
```

`Warnings` and `Errors` are structured, deterministically sorted by code, entity type, and source ID. Messages are bounded to 2048 UTF-8 bytes and contain no body text or email.

Manifest errors make the bundle invalid and therefore should normally prevent output. Recoverable entity problems belong in warnings and skipped counts.

## 12.5 Count semantics

Every count name is exact:

```text
spaces_emitted
pages_discovered
pages_emitted
pages_skipped
blogposts_emitted
comments_discovered
comments_emitted
comments_skipped
attachments_discovered
attachments_emitted
attachments_skipped
users_emitted
users_inactive
users_placeholder_email
labels_preserved
restricted_pages_preserved
pages_flattened
missing_bodies
```

Counts describe producer discovery/emission, not successful destination import.

## 12.6 Checksums

- `jsonl_sha256`: SHA-256 of exact `import.jsonl` bytes.
- `attachments_sha256`: SHA-256 over attachments in lexical path order using this framing for each:

  ```text
  uint64 big-endian path byte length
  path UTF-8 bytes
  uint64 big-endian file byte length
  file bytes
  ```

- Each attachment also carries its own SHA-256.

## 12.7 Golden fixture gate

Before XML parsing or importer persistence work proceeds:

1. Add one minimal valid bundle fixture.
2. Add one full valid bundle fixture containing every field.
3. Add invalid fixtures for sequence, duplicate IDs, bad parent, bad checksum, missing attachment, and unsafe path.
4. Producer golden test emits byte-identical fixed-clock output.
5. Consumer inspection accepts the same full fixture.

No task may independently alter fixture semantics.

---

# 13. Exporter file map

Create from canonical `origin/master`:

```text
commands/transform_confluence.go
commands/transform_confluence_test.go
services/confluence/archive.go
services/confluence/archive_test.go
services/confluence/attachments.go
services/confluence/attachments_test.go
services/confluence/bundle.go
services/confluence/bundle_test.go
services/confluence/content_filter.go
services/confluence/content_filter_test.go
services/confluence/contract.go
services/confluence/contract_test.go
services/confluence/hierarchy.go
services/confluence/hierarchy_test.go
services/confluence/manifest.go
services/confluence/parser.go
services/confluence/selector.go
services/confluence/selector_test.go
services/confluence/storage_format.go
services/confluence/storage_format_test.go
services/confluence/transformer.go
services/confluence/types.go
services/confluence/user_mapping.go
services/confluence/user_mapping_test.go
services/confluence/validation.go
services/confluence/xml_decoder.go
services/confluence/xml_decoder_test.go
services/confluence/xml_entities.go
services/confluence/xml_index.go
services/confluence/testdata/**
docs/confluence-jsonl-contract.md
```

Register the command from the existing transform command initialization following current Cobra conventions. Keep orchestration in `transform_confluence.go`; do not expand unrelated provider handlers.

Generated CLI docs are produced with the repository doc generator. Do not hand-edit generated files.

## 13.1 Docs importer file map

Create or change only these areas unless a numbered task explicitly proves another current-`master` dependency:

```text
server/api.go                              # register import routes only
server/api_import.go                       # upload/read/confirm/cancel handlers
server/api_import_test.go
server/plugin.go                           # start/stop worker and cleanup only
server/plugin_test.go
server/importer/archive.go                 # pure ZIP validation
server/importer/archive_test.go
server/importer/contract.go                # exact section-12 structs
server/importer/contract_test.go
server/importer/hash.go                    # versioned canonical hashes
server/importer/hash_test.go
server/importer/inspect.go                 # streaming contract validation
server/importer/inspect_test.go
server/importer/tiptap.go                  # parse/validate/rewrite helpers
server/importer/tiptap_test.go
server/importer/testdata/**
server/model/import.go                     # job/entity enums and public views
server/model/import_test.go
server/app/import.go                       # service-level orchestration
server/app/import_preflight.go
server/app/import_users.go
server/app/import_execute.go
server/app/import_cleanup.go
server/app/import_report.go
server/app/import_test.go
server/store/import_job_store.go
server/store/import_staging_store.go
server/store/import_mapping_store.go
server/store/import_execution_store.go
server/store/import_report_store.go
server/store/import_store_test.go
server/store/migrations/<next>_create_imports.up.sql
server/store/migrations/<next>_create_imports.down.sql
```

Implementation boundaries:

- Package `importer` remains pure: no plugin API, HTTP, database, or filesystem mutation beyond reading an already-open archive.
- Package `model` owns enums, validation, and API response types; it performs no I/O.
- Package `store` owns SQL transactions and mapping uniqueness.
- Package `app` owns preflight and execution policy plus plugin API calls.
- Root package `main` owns HTTP handlers, authorization, multipart spooling, route registration, and plugin lifecycle.
- Do not add importer behavior to existing page/space HTTP handlers.
- Add importer-specific page store methods rather than weakening public `CreatePage`/`UpdatePage` invariants.
- Do not add webapp files in this iteration.

Current canonical integration points:

- Register routes in `server/api.go:initRouter` alongside existing `/api/v1` routes.
- Start and stop the worker from `server/plugin.go:OnActivate` and `OnDeactivate`.
- Reuse `server/model/page.go` limits and `server/model/page_content.go` parsing/search helpers.
- Reuse `server/store/store.go` migration and transaction conventions.
- The current migration sequence ends at `000005`; use the next free number after updating `origin/master`.

---

# 14. Docs importer API contract

API-only first iteration. Do not add webapp files.

Routes under `/api/v1`:

```text
POST /imports/preflight
GET  /imports
GET  /imports/{job_id}
GET  /imports/{job_id}/issues
GET  /imports/{job_id}/report
POST /imports/{job_id}/confirm
POST /imports/{job_id}/cancel
```

`POST /imports/preflight` is multipart with parts in this order:

1. `request`: JSON, max 64 KiB.
2. `bundle`: ZIP.

Request:

```json
{
  "team_id": "mattermost-team-id"
}
```

Rules:

- Actor must be a system administrator.
- Team must exist.
- Clustering must be disabled.
- First import creates a new Docs Space.
- Reimport of the same `(organization_id, space_id)` reuses its mapped Space and requires the same team.

Confirmation body:

```json
{
  "preflight_revision": 1,
  "acknowledge_user_creation": true,
  "acknowledge_placeholder_emails": true,
  "acknowledge_user_deactivation": true,
  "acknowledge_unenforced_restrictions": true,
  "overwrite_page_conflicts": []
}
```

Required acknowledgements are conditional on preflight findings. Stale revision returns HTTP 409.

Stable job states:

```text
inspecting
awaiting_confirmation
queued
running
completed
failed
canceled
```

---

# 15. Import spool and lifecycle

First iteration is explicitly single-node.

Spool root:

```text
${TMPDIR}/mattermost-plugin-docs-imports
```

- Root mode `0700`.
- Job directory `<job-id>` mode `0700`.
- Bundle file `bundle.zip` mode `0600`.
- Stream multipart upload directly to `<job-id>/bundle.zip.tmp` with a size limit, fsync, then atomic rename.
- Keep bundle through confirmation and execution.
- Delete bundle 24 hours after terminal state.
- Keep reports/mappings in PostgreSQL for 30 days; source entity mappings required for reimport are not deleted with job reports.
- On activation, clean abandoned `.tmp` files older than one hour and resume queued/running jobs.
- On deactivation, stop claiming work and allow current entity checkpoint to finish.

Initial importer limits:

```text
compressed bundle:       2 GiB
archive entries:         100,000
manifest:                8 MiB
JSONL:                   1 GiB
JSONL line:              8 MiB
pages:                   50,000
comments:                250,000
manifest users:          50,000
single attachment:       min(1 GiB, Mattermost MaxFileSize)
```

All decompressed reads use limited readers. Reject before mutation.

---

# 16. Import persistence

Add sequential migrations after the latest migration on canonical `origin/master`; do not hard-code the migration number in this plan because it may change before implementation.

Required tables or equivalent normalized schema:

```text
DOCS_ImportJob
DOCS_ImportStagedUser
DOCS_ImportStagedPage
DOCS_ImportStagedComment
DOCS_ImportStagedAttachment
DOCS_ImportIssue
DOCS_ImportResult
DOCS_ImportSourceSpace
DOCS_ImportSourceUser
DOCS_ImportSourcePage
DOCS_ImportSourceComment
DOCS_ImportSourceAttachment
```

Every source mapping key includes organization ID and source space ID. Entity mapping keys additionally include entity source ID.

Minimum durable mapping fields:

- Destination ID.
- Source hash.
- Last applied destination hash.
- Managed-by-import flag for users.
- Attachment checksum and superseded FileInfo ID when changed.
- Create/update timestamps.

Page creation/update and source-page mapping mutation must occur in one plugin SQL transaction using importer-specific store methods. Core channel/user/post/file calls cannot join that transaction and therefore use explicit provisioning/checkpoint states.

---

# 17. Import hashing and reimport policy

Use canonical JSON with sorted object keys and UTF-8 bytes. Prefix every hash with a schema label so future hash changes do not collide.

## Page source hash

Hash:

```text
"docs-confluence-page-v1\x00"
source ID
source content type
title
unresolved canonical TipTap JSON
output parent source ID
author account ID
create/update times
canonical labels
canonical restrictions
sorted attachment source IDs and SHA-256 values
```

## Destination applied hash

Hash destination title, resolved TipTap body, destination parent ID, author ID, and preserved source props after successful write.

## Comment source hash

Hash source ID, page source ID, parent source ID, thread-root source ID, author account ID, message, timestamps, resolved state, and source props.

## Attachment hash

Use byte SHA-256 plus filename, MIME type, and size.

## Reimport behavior

- Source entity missing from later bundle: report `stale_source_entity`; do not delete destination data.
- Unchanged source: reuse mapping, no mutation.
- Source changed and destination still equals last applied hash: update.
- Source changed and destination differs from last applied hash: conflict; do not overwrite unless page ID is acknowledged in `overwrite_page_conflicts`.
- Changed attachment under same source ID: upload new FileInfo, update page references, mark prior FileInfo mapping superseded; do not delete old FileInfo in this iteration.
- Changed comment: update only when destination post message/props still equal last applied hash; otherwise report conflict and preserve destination.
- Removed attachment/comment: report stale; do not delete.
- Reused pre-existing user: never mutate identity or active state.
- Managed imported user: placeholder email may be replaced by a later real email if no collision; source inactivity may deactivate after content execution.

---

# 18. Destination user policy

Preflight order:

1. Explicit mapping username: lookup exact Mattermost username.
2. If explicit target exists, reuse it.
3. If explicit target is absent, plan creation with that username.
4. Without explicit mapping, if source email is real, lookup normalized email and reuse an exact match.
5. Otherwise allocate a collision-free username from the proposal and plan creation.
6. Never match by placeholder email.
7. Never treat username collision alone as identity; suffix the new username.

Creation fields:

- Username: preflight allocation.
- Email: real or deterministic placeholder.
- First/last/nickname: derive conservatively from display name; display name may be stored in nickname when splitting is ambiguous.
- Locale: server default locale, fallback `en`.
- Password: 32 random bytes from `crypto/rand`, base64url encoded; never log or return it.
- EmailVerified: false.
- AuthService/AuthData: empty.

Execution order:

1. Create/reuse users.
2. Persist source-user mappings.
3. Import Space/pages/attachments/comments.
4. Deactivate only newly created managed users whose source `active=false`.

If `.invalid` email is rejected by server policy, fail the affected user before content mutation and return remediation; do not invent another domain.

Never deactivate the importing actor or any reused pre-existing user.

---

# 19. Space and page execution

Current Docs `origin/master` constraints that must be honored:

```text
PageTitleMaxRunes = 255
PageBodyMaxBytes = 2 MiB
PagePropsMaxBytes = 64 KiB
PageSearchTextMaxBytes = 2 MiB
MaxPageDepth = 10, root depth 1
```

Destination IDs are Mattermost IDs. Never use numeric Confluence IDs as Docs primary keys.

First import:

1. Create the Space channel and `DOCS_Space` through current app/store patterns.
2. Persist source-space mapping.
3. Store source metadata in Space props.

For each parent-first page:

1. Resolve destination parent mapping.
2. Resolve author mapping.
3. Upload/reuse that page's attachments first.
4. Rewrite attachment, page-link, and user placeholders on a parsed TipTap tree.
5. Validate via `model.ParseTipTapDocument` and derive `model.BuildSearchText`.
6. Execute importer-specific page insert/update and source mapping in one plugin SQL transaction.
7. Record immutable entity outcome.

If attachment upload fails, the page remains pending/retryable and is not committed with unresolved attachment placeholders.

---

# 20. Attachment execution

Use the underlying plugin API streaming methods, not `pluginapi.File.Upload`, because `File.Upload` buffers with `io.ReadAll`.

For each attachment:

1. Open the retained bundle and exact validated ZIP entry.
2. Reverify path, size, CRC, and SHA-256.
3. Create `model.UploadSession` with:
   - `Type=model.UploadTypeAttachment`
   - destination Space channel ID
   - mapped source attachment author ID, fallback importing actor only if source author is absent
   - filename and exact size
4. Call `p.API.CreateUploadSession`.
5. Call `p.API.UploadData(session, limitedReader)` and receive `FileInfo`.
6. Persist source-attachment mapping and checkpoint.
7. On retry after an uncertain failure, inspect the durable checkpoint/mapping before uploading again.

Checkpoint states:

```text
validated
uploading
uploaded
mapped
superseded
failed
```

A page is successful only after all emitted attachments required by its body are mapped and its resolved body is committed.

---

# 21. Comment execution

Comments become Mattermost posts in the Space channel. The first iteration does not add comment UI; posts are durable and source-linked for future Docs comment UI.

Exact post props:

```json
{
  "docs_page_id": "destination-page-id",
  "docs_import_source": "confluence",
  "docs_import_organization_id": "...",
  "docs_import_space_id": "...",
  "docs_import_comment_id": "...",
  "docs_import_parent_comment_id": "...",
  "docs_import_resolved": false
}
```

Thread mapping:

- A top-level Confluence comment becomes a Mattermost root post with empty `RootId`.
- Every descendant's Mattermost `RootId` is the destination post ID of the Confluence thread root, not the immediate parent.
- Immediate parent identity remains in props and the source-comment mapping.

Comment content is Markdown/plain text converted from Confluence Storage Format. If it exceeds current Mattermost post limits, skip it and its descendants with warning; do not split one source comment into multiple posts.

Reimport follows section 17. Source-deleted comments are not deleted from Mattermost.

E2E must verify source timestamps. If the public post API cannot preserve them, record actual timestamps and report `comment_timestamp_not_preserved`; do not bypass core storage directly.

---

# 22. Labels and restrictions

Page props preserve:

```text
import_labels
confluence_labels
confluence_restrictions
```

The importer copies them under one page props namespace:

```json
{
  "docs_import": {
    "source": "confluence",
    "organization_id": "...",
    "space_id": "...",
    "page_id": "...",
    "content_type": "page",
    "labels": [],
    "restrictions": {}
  }
}
```

They are included in source/applied hashes. No UI/search/ACL behavior is added.

Restriction shape:

```json
{
  "view_users": ["canonical-account-id"],
  "view_groups": ["source-group"],
  "edit_users": ["canonical-account-id"],
  "edit_groups": ["source-group"]
}
```

If the combined source props exceed the exporter 48 KiB target, skip the page with a hard per-page conversion error rather than truncate future security metadata.

Until a real restricted-page XML fixture exists:

- Contract/importer storage tests use synthetic data.
- Exporter restriction extraction is marked experimental.
- Reports use fidelity `restriction_extraction_unverified`.
- Acceptance does not claim complete restriction discovery.

---

# 23. Import worker and cancellation

Add one worker started from `Plugin.OnActivate` and stopped from `OnDeactivate`.

Single-node rules:

- One goroutine claims one queued job at a time.
- Claim uses an atomic PostgreSQL state transition `queued -> running`.
- On startup, jobs left `running` are reset to `queued` after verifying their bundle exists.
- Check cancellation between users, pages, attachments, and comments.
- Complete the current core API call and durable checkpoint before stopping.
- Every planned entity receives a final outcome, including `not_attempted_canceled` or `not_attempted_failed`.

Do not add a distributed lease in this iteration. Cluster mode is rejected at API admission.

---

# 24. Atomic implementation tasks

Each task is a separate implementation unit. A smaller model must not combine tasks unless explicitly instructed.

## E0 — Pin baselines and create clean branches

Files changed: this plan only.

Done when:

- Latest canonical base SHAs are recorded.
- Clean worktrees and feature branches exist.
- Reference branches remain unchanged.

## E1 — Freeze contract documentation and golden fixtures

Repositories: both.

Create:

```text
mmetl/docs/confluence-jsonl-contract.md
mmetl/services/confluence/testdata/contract/**
mattermost-plugin-docs/server/importer/contract.go
mattermost-plugin-docs/server/importer/testdata/**
```

Implement only contract structs and fixture parsing/generation tests. No XML or persistence.

Gate: full golden fixture is accepted by both repositories.

### E1 execution record

Delivered as specified, with these recorded decisions. None changes the bundle
contract; each is a packaging choice that both repositories follow identically.

1. `mmetl/services/confluence/contract.go` and `contract_test.go` were created
   alongside the two files E1 names for `mmetl`. Section 12.3 requires the
   producer to mirror the consumer's structs, and the fixture generator needs
   them.
2. Contract validation lives in `services/confluence/validation.go`, the file
   section 13 already reserves, with `validation_test.go` beside it. E13 reuses
   it for bundle self-validation rather than adding a second implementation.
3. `ValidateBundleFS(fs.FS)` is the single entry point. `*zip.Reader` and
   `os.DirFS` both satisfy `fs.FS`, so a real bundle and an unpacked fixture are
   validated by the same code.
4. Fixtures are committed unpacked, as plain files, so they are reviewable in a
   diff. Deterministic ZIP framing is E13's gate; the cross-repository gate here
   is the contract-defined `jsonl_sha256` and `attachments_sha256`, which do not
   depend on the Go version's deflate output.
5. Two fixtures beyond the six named in E1 were added, `invalid-missing-sentinel`
   and `invalid-duplicate-sentinel`, because section 11 requires the importer to
   reject both and neither is covered by the sequence fixture.
6. `.gitignore` gained `!services/confluence/testdata/**/*.jsonl`. The
   repository-wide `*.jsonl` rule targets development scratch data and would
   otherwise silently drop the golden fixtures.
7. On the Docs side the mirror is mechanical, not hand-written:
   `server/importer/contract.go` mirrors `contract.go` and
   `server/importer/inspect.go` mirrors `validation.go`, both differing only in
   the package clause and file comment. I0 extends `inspect.go` with
   archive-level streaming inspection on top of the same rules.
8. `server/importer/contract_limits_test.go` asserts the mirrored limits equal
   `server/model`'s real `PageTitleMaxRunes`, `PageBodyMaxBytes`,
   `PagePropsMaxBytes`, and `MaxPageDepth`. The mirror restates them as literals
   so the two implementations stay comparable, which would otherwise let a
   change to a Docs limit desynchronize the repositories silently: the exporter
   would keep emitting pages the importer accepts and the page store rejects.
9. The Docs repository lints more strictly than `mmetl` (`gosec`, `modernize`,
   `errcheck` on deferred `Close`). Those findings were fixed in the `mmetl`
   sources rather than only in the mirror, so both copies stay identical and
   both lint clean.

Verified: the two fixture trees are byte-identical; regenerating the fixtures
from the Docs-side builders reproduces `mmetl`'s bytes exactly; and each side
accepts both valid fixtures and rejects all eight invalid ones for the named
reason.

## E2 — Source archive reader

Repository: `mmetl`.

Symbols:

```go
OpenSourceArchive(path string) (*SourceArchive, error)
ParseDescriptor(io.Reader) (Descriptor, error)
```

Tests: path normalization, duplicate normalized names, sample leading `./`, traversal, symlink, encrypted/unsupported entries, missing required entries, attachment index.

## E3 — Generic XML decoder

Repository: `mmetl`.

Symbols:

```go
NewObjectDecoder(io.Reader) *ObjectDecoder
(*ObjectDecoder).Next() (*RawObject, error)
```

Gate: stream the complete private sample and report class counts without growing memory with body bytes.

## E4 — Space catalog and selector

Repository: `mmetl`.

Symbols:

```go
CatalogSpaces(*SourceArchive) ([]Space, Descriptor, error)
ResolveSpace([]Space, string) (Space, error)
```

Add list-space unit tests. No Cobra command yet.

## E5 — CLI list-spaces

Repository: `mmetl`.

Create/register `TransformConfluenceCmd`. Implement only `--file --list-spaces`. Add command tests and generated docs.

## E6 — Canonical page/blog metadata

Repository: `mmetl`.

Implement exact predicates from section 6, metadata pass, hierarchy, cycle detection, flattening, and golden selected IDs from a sanitized fixture.

## E7 — Comments and attachment metadata

Repository: `mmetl`.

Implement eligibility, thread-root derivation, broken-parent skipping, SpaceDescription-to-source-home-page mapping, and deterministic ordering. No body conversion or byte copy.

## E8 — User joins and explicit mapping

Repository: `mmetl`.

Implement selected-user closure, ConfluenceUser/InternalUser join, canonical account IDs, mapping CSV, proposal provenance, collision handling, and placeholder email tests.

## E9 — Labels and restriction contract population

Repository: `mmetl`.

Implement labels from real sample structures. Implement synthetic restriction mapping only. Mark restriction extraction unverified. Do not infer undocumented XML classes.

### E2-E9 execution record

Delivered in order, each gated on the private sample at
`/Users/willyfrog/Downloads/Confluence-export.zip` and on `make check-style`,
`make test`, `make docs` and `make docs-check`.

A standing deviation, recorded once: source files follow section 13's file map
exactly. Test files are split for readability where a single file would become
unreadable, so the package also carries `validation_test.go`, `comments_test.go`
and `labels_test.go`, and comment logic lives in `content_filter.go` as section
6 requires rather than in a file of its own.

#### Facts the sample settled, which the plan did not state

1. Composite keys are a `<composite-id>` element whose parts are `<property>`
   children, not repeated `<id>` elements. The sample has 97 of them
   (`BucketPropertySetItem`), so the first streaming run failed outright until
   the decoder handled the real shape.
2. Attachment media type and byte size are not attachment scalars. They are
   separate `ContentProperty` objects named `MEDIA_TYPE` and `FILESIZE`,
   resolved in their own pass over only the referenced ids.
3. An attachment's archive path is keyed by its **container**, not by the page
   it is emitted against: `attachments/<container-id>/<attachment-id>/<version>`.
   The sample's only attachment hangs off a `SpaceDescription`, so this and the
   home-page remap are the exercised path, not an edge case.
4. A space can have no home page. The sample contains one.
5. `ConfluenceUserImpl.name` is the user's **email** for directory-backed
   accounts and their account ID otherwise. Only 35 of 348 directory rows carry
   an email, so placeholder addresses are the common case.
6. The sample's `entities.xml` is 4.6 MB holding 5,922 objects across 31
   classes, of which this exporter reads 10. Streaming it end to end moves the
   retained heap by roughly zero bytes.

#### Open findings for review

1. **All labels in the sample are space-level.** Every one of the 11
   `Labelling` objects targets a `SpaceDescription`, and 10 of the 11 `Label`
   objects are personal `my`-namespace favourites rather than shared content
   metadata. This iteration carries **page labels only**, so the sample exports
   zero labels. Carrying space labels would mean either remapping them onto the
   home page, as attachments are remapped, or adding a key to space props;
   both are contract decisions, so neither was invented here.
2. **Personal-namespace labels are preserved as-is.** When a page does carry a
   `my`-namespace label it is one user's private favourite. Section 22 stores
   labels as inert metadata with no UI, search or access behaviour, and the
   namespace travels with each label, so a future consumer can distinguish
   them. Worth an explicit decision before any label UI ships.
3. **No page-restriction object exists in the sample**, only space-level
   `SpacePermission` rows this iteration does not import. Stop condition 7
   therefore still stands: restriction extraction is unverified, no bundle
   claims to have found any, and every export emits
   `restriction_extraction_unverified` whether or not anything was found.
4. **No comment-resolution marker exists in the sample.** `is_resolved` is
   always false until a fixture proves how Confluence spells it.

#### Interpretations recorded

1. Section 6.3's attachment predicate deliberately omits the
   `originalVersionId` test that section 6.1 applies to pages. The text is
   followed exactly; adding the stricter test would silently drop attachments
   in exports that spell the marker the other way.
2. "More than one canonical object for the same logical source content" is read
   as the version group: an object's logical ID is its `originalVersionId` when
   set and its own ID otherwise.
3. Historical membership is resolved against objects that already satisfy the
   other four conditions of section 6.1, because resolving it against every
   object is circular.
4. "Valid Confluence username" in section 9 means already valid as a Mattermost
   username. Confluence usernames are email addresses for directory-backed
   accounts, and mangling one into `j.smith-example.com` would be worse than
   the local part the next rule produces.
5. Two explicit mapping rows claiming one `mattermost_username` is a hard
   error, not a suffix: renaming a name the operator asked for would silently
   disobey them.
6. Skipped comment threads warn once per root cause plus one aggregate for the
   descendants, satisfying "one structured warning per skipped root cause, not
   one per descendant" while still using both stable codes.

## E10 — Storage-format converter core

Repository: `mmetl`.

Implement paragraphs, headings, marks, lists, blockquote, code, tables, and safe external links. Every fixture output must pass the mirrored Docs TipTap validator assumptions.

## E11 — Macros and typed placeholders

Repository: `mmetl`.

Implement remaining conversion matrix, source-ID resolution, mentions, page links, attachment links/images, warnings, and limits. Never replace serialized JSON strings.

## E12 — Attachment byte extraction

Repository: `mmetl`.

Implement filename sanitation, exact version path, limited streaming copy, size/CRC/SHA verification, collision-safe bundle path, skip-and-warn failures, and aggregate checksum framing.

## E13 — Bundle writer and self-validator

Repository: `mmetl`.

Implement deterministic JSONL/manifest/ZIP writing to a temporary output, full self-validation, then atomic rename. Add fixed-clock golden test.

## E14 — Complete exporter CLI

Repository: `mmetl`.

Add remaining flags, transform/validate flow, logging, docs, README, command integration tests, private-sample manual validation.

Required gate:

```shell
go test ./services/confluence/... -count=1
go test ./commands/... -run Confluence -count=1
make docs
make docs-check
make check-style
make test
```

### E10-E14 execution record

The exporter is complete. `mmetl transform confluence` produces a bundle the
Docs importer validates, and the private sample exports end to end.

#### Facts the sample settled

1. Go's stock `xml.HTMLAutoClose` matches on local name alone, so it treats
   Confluence's own `<ac:link>` as the HTML void element `<link>`. It closed the
   tag immediately and then failed on the real `</ac:link>`, aborting a real
   page. Left unfixed it would have silently destroyed every page and
   attachment link once E11 read them. The exporter uses its own auto-close
   list without `link`; no other Confluence element name collides with it.
2. Confluence indents its storage format, so whitespace between block elements
   was becoming paragraphs of spaces between every real paragraph.
3. `ri:userkey` carries the account ID in a Cloud export, not a separate legacy
   key. All 223 mention references in the sample resolve once both indexes are
   tried; before that, all 223 fell back to text.
4. Every image in the sample is an external `ri:url`, not an attachment.
5. `ac:emoticon` carries the emoji character itself in `emoji-fallback`.
6. Confluence puts images, task lists and block macros inside `<p>`, which a
   TipTap paragraph cannot hold, so such a paragraph splits around them.
7. The sample's macro mix is status (27), recently-updated (18), blog-posts
   (17), panel (8), info (4), roadmap (2), contributors (1).

#### Interpretations recorded

1. `panel` is converted to a callout although section 10 does not list it. That
   matrix is a stated **minimum**, `panel` is Confluence's generic callout, and
   rendering one as an unsupported-macro marker above its own body would be
   visibly worse. It is 8 of the sample's macros.
2. The `children`/`pagetree` macro becomes a frozen list of real child links.
   The macro is live in Confluence and the destination has no equivalent, so a
   snapshot is closer to what the reader saw than a marker; with no children to
   list it falls back to the marker.
3. Discovery counts are scoped to the selected space, so `discovered` always
   equals `emitted` plus `skipped`. Counting every object in the file instead
   reported another space's content and the historical versions as if this
   export had passed them over: the sample's 4-page space claimed 78 pages and
   15 comments discovered. Emitted totals are counted from the emitted lines,
   not from the selection, because pages can still fail conversion.
4. `--validate-only` runs the whole transform including the attachment copy,
   and writes nothing.
5. `--list-spaces` refuses to be combined with any transform flag, so an
   operator cannot believe an export ran when nothing was written.

#### Cross-repository state

The Docs repository forbids `t.Skip`, so the cross-repository check is a
committed artifact rather than an environment-gated test:
`server/importer/testdata/exporter/bundle.zip` is produced by the real
exporter from a synthetic Confluence backup and validated on every run. It
carries a page hierarchy, a blog post, labels, an attachment, a comment thread,
and both placeholder kinds, and a second test asserts those shapes remain, so a
regenerated bundle that lost them cannot keep passing.

Regenerate it with:

```shell
mmetl transform confluence --file <backup>.zip --space ENG \
  --organization-id https://example.atlassian.net --team engineering \
  --output server/importer/testdata/exporter/bundle.zip
```

#### Verified against the private sample

```text
mmetl transform confluence --file Confluence-export.zip --space dkhspace \
  --organization-id https://mattermost.atlassian.net --team engineering

4 pages, 0 blog posts, 0 comments, 1 attachment, 1 user, 5 warnings
```

The Docs importer accepts that bundle.

---

## I0 — Importer contract/archive inspection

Repository: Docs.

Implement secure bundle inspection, exact v2 contract, checksum/count/ordering/TipTap validation, and streaming sink interfaces for users/pages/comments/attachments. Do not create jobs yet.

Gate: accept the exporter full golden fixture.

## I1 — Import migrations and model

Repository: Docs.

Add tables from section 16, model enums/validation, up/down migrations, uniqueness constraints, and PostgreSQL tests. No API or worker.

## I2 — Local spool and admission limits

Repository: Docs.

Implement spool creation, bounded multipart-to-disk helper, file permissions, cleanup, archive opening, and cluster-mode rejection helper.

## I3 — Preflight upload API

Repository: Docs.

Add `POST /imports/preflight`, system-admin authorization, team validation, job creation, bundle retention, streaming inspection into staging, and error-to-HTTP mapping.

## I4 — Read/cancel/report API

Repository: Docs.

Add list/get/issues/report/cancel endpoints with pagination, authorization, stable response schemas, and terminal cleanup metadata.

## I5 — Preflight classification

Repository: Docs.

Implement source-space lookup, user lookup/proposals, page/comment/attachment hashes, conflict classification, required acknowledgements, and durable `preflight_revision`.

## I6 — Confirmation and worker lifecycle

Repository: Docs.

Implement confirm endpoint, stale-revision check, queue transition, single worker, startup recovery, shutdown, cancellation checkpoints, and outcome completeness.

## I7 — User provisioning

Repository: Docs.

Implement exact policy in section 18, random passwords, managed-user mappings, retries, and delayed deactivation. Mock plugin API tests plus E2E user lifecycle test.

Stop if placeholder domain is rejected or license/user limits cannot be surfaced clearly.

## I8 — Space provisioning and source mapping

Repository: Docs.

Implement first-import Space creation, reimport mapping reuse, same-team invariant, source props, recovery from channel-created/space-row-failed window, and tests.

## I9 — Attachment streaming upload

Repository: Docs.

Implement upload sessions and attachment checkpoints. Verify actual Mattermost API behavior in E2E before continuing to page body resolution.

## I10 — Importer-specific page store operations

Repository: Docs.

Implement transactional page insert/update plus source mapping, imported timestamps/props/authors, parent/depth/order validation, and conflict tests. Do not bypass model TipTap validation.

## I11 — Page execution and placeholder resolution

Repository: Docs.

Resolve page/user/file mappings on typed TipTap trees, derive SearchText, apply pages parent-first, and persist outcomes. Test unresolved/cross-space fallback.

## I12 — Comment staging and execution

Repository: Docs.

Implement exact props/thread semantics, post create/update mapping, limits, idempotency, conflicts, retries, and timestamp fidelity reporting.

## I13 — Delayed deactivation and terminalization

Repository: Docs.

Deactivate newly created inactive users only after pages/comments complete. Finalize counts/fidelity, fill not-attempted outcomes, retain/delete spool according to policy.

## I14 — Docs complete verification

Required gate:

```shell
go test ./server/importer/... -count=1
go test ./server/model/... ./server/store/... ./server/app/... -count=1
make check-style
make test
make dist
make test-e2e
```

## X0 — Cross-repository end-to-end

1. Produce fixture bundle with `mmetl`.
2. Import into throwaway Docs-enabled Mattermost.
3. Reimport unchanged; verify no duplicates.
4. Reimport with changed page, attachment, comment, labels, and user activity.
5. Verify conflict behavior, no unintended deletes, delayed deactivation, truthful fidelity, and bundle cleanup.

No release until X0 passes.

---

# 25. Stable warning/error codes

Use these exact producer warning codes where applicable:

```text
source_archive_unknown_entry
xml_unknown_reference_class
page_missing_body
page_title_defaulted
page_title_truncated
page_parent_missing_promoted
page_depth_flattened
page_content_too_large
page_props_too_large
unsupported_macro
dangerous_url_removed
comment_parent_missing_skipped
comment_ancestor_skipped
comment_too_large_skipped
attachment_home_page_missing
attachment_blob_missing
attachment_blob_corrupt
attachment_size_mismatch
attachment_filename_sanitized
attachment_skipped_by_flag
user_placeholder_email
restriction_extraction_unverified
```

Use stable importer issue codes prefixed by entity/stage, for example:

```text
user_create_planned
user_email_match
user_placeholder_email
user_deactivation_planned
page_create_planned
page_update_planned
page_local_conflict
attachment_upload_planned
attachment_upload_failed
comment_create_planned
comment_local_conflict
labels_preserved_not_applied
restrictions_preserved_not_enforced
external_auth_preserved_not_applied
```

Do not key logic off human-readable messages.

---

# 26. Stop conditions

The implementation model MUST stop and ask for a decision when any occurs:

1. Canonical repository structure has materially changed from the pinned baseline.
2. Producer and consumer golden fixtures disagree.
3. The private sample cannot be streamed by the generic decoder.
4. The user does not provide stable `--organization-id` for a transform.
5. More than one canonical current object exists for one logical content item.
6. A supported entity exceeds Docs limits and no outcome is specified above.
7. Real page-restriction XML is required to claim complete extraction.
8. Cluster mode is enabled.
9. Placeholder `.invalid` emails cannot be created under destination policy.
10. Core APIs require imported users to become team/Space members.
11. Upload sessions cannot stream or return durable FileInfo on the supported Mattermost version.
12. Inactive authors cannot be used for page/post attribution under the defined delayed-deactivation sequence.
13. A task's focused tests fail.
14. Required repository style/full/E2E checks fail.
15. A change would require altering the frozen JSON contract, hashing algorithm, user matching order, deletion policy, or access semantics.

Do not “work around” these conditions silently.

---

# 27. Definition of done

## Exporter

- Lists spaces without exposing unrelated private data.
- Requires and resolves one exact selected space.
- Streams the XML and attachments.
- Emits only canonical current selected-space content.
- Preserves source hierarchy/IDs and flattens deterministically to Docs depth 10.
- Emits blog posts as pages.
- Emits empty pages for missing bodies.
- Skips broken comment trees.
- Emits only selected-space users with deterministic proposals/placeholders.
- Applies explicit mappings first.
- Copies valid attachments and remaps SpaceDescription attachments to source home page.
- Preserves labels and marks restrictions unverified until real-fixture review.
- Produces a deterministic, self-validating bundle accepted by the importer.
- Passes `make check-style` and `make test`.

## Docs importer

- Is system-admin-only and rejects HA mode.
- Retains bundles durably enough for single-node restart/resume.
- Performs preflight before mutation and requires explicit confirmation.
- Reuses real-email matches and explicit username mappings.
- Creates missing users with deterministic placeholder emails where needed.
- Never mutates reused users; deactivates only newly managed inactive users after content import.
- Creates/reuses one mapped Docs Space per source namespace.
- Imports pages parent-first with source mappings and conflict detection.
- Streams attachment uploads and rewrites typed content attributes.
- Imports comments as mapped Mattermost post threads.
- Preserves labels/restrictions as metadata without claiming functionality/enforcement.
- Reimport is idempotent and does not delete stale destination data.
- Produces complete, truthful reports for success, conflict, skip, cancellation, and failure.
- Passes focused tests, `make check-style`, `make test`, `make dist`, and `make test-e2e`.

---

# 28. Future work

Do not implement these during the first iteration:

1. Multi-space export producing one bundle per space.
2. Shared indexing so one XML decompression can feed many spaces.
3. HA-safe shared bundle spool and distributed worker lease.
4. Page-level ACL storage/enforcement across read, edit, search, hierarchy, comments, attachments, export, and notifications.
5. Validation against a real user/group-restricted Confluence XML export.
6. Space/global permission migration and group-membership reconstruction.
7. User-visible/searchable Docs labels.
8. Mapping Confluence external directory IDs to Mattermost `AuthService`/`AuthData`.
9. Comment UI in Docs.
10. Deleting source-removed pages/comments/attachments.
11. Cleanup of superseded Mattermost FileInfo records.
12. Historical versions, drafts, audit data, notifications, and arbitrary plugin/Active Objects content.
