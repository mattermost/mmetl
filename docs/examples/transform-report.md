# Slack Transform Report

## Run

| Field    | Value                                                                                                       |
| -------- | ----------------------------------------------------------------------------------------------------------- |
| Provider | slack                                                                                                       |
| mmetl    | v0.5.1 (4fdbe5f)                                                                                            |
| Input    | acme-inc.zip                                                                                                |
| Team     | acme                                                                                                        |
| Output   | out/mm_export.jsonl                                                                                         |
| Started  | 2026-08-24T14:52:13Z                                                                                        |
| Finished | 2026-08-24T14:52:13Z                                                                                        |
| Duration | 135ms                                                                                                       |
| Flags    | `--attachments-dir=out/data --bot-owner=admin --file=acme-inc.zip --output=out/mm_export.jsonl --team=acme` |

## Summary

| Entity                                | Transformed | Skipped |
| ------------------------------------- | ----------- | ------- |
| Users                                 | 18          | 0       |
| Bots                                  | 25          | 0       |
| [Public channels](#public-channels)   | 5           | 0       |
| [Private channels](#private-channels) | 5           | 0       |
| [Group channels](#group-channels)     | 3           | 0       |
| [Direct channels](#direct-channels)   | 30          | 0       |
| Channel memberships                   | 166         | 0       |
| Posts                                 | 603         | 0       |
| Threads                               | 24          | 0       |
| Reactions                             | 14          | 0       |
| Files                                 | 2           | 0       |
| Emoji                                 | 7           | 0       |

## Details

### Public channels

#### 1 note: is archived but has no update timestamp, so the current time was used as the archive time[^archived-no-timestamp]

- **C000025** (`feedback`)

#### 1 note: has no valid creation timestamp in the export, so the current time was used[^channel-no-created-ts]

- **C000002** (`off-topic`)

### Private channels

#### 2 notes: is archived but has no update timestamp, so the current time was used as the archive time[^archived-no-timestamp]

- **C000028** (`inc1281-network-outage`)
- **C000038** (`inc-1281-network-outage`)

#### 1 note: has no valid creation timestamp in the export, so the current time was used[^channel-no-created-ts]

- **C000020** (`ask-it`)

### Group channels

#### 1 note: has no valid creation timestamp in the export, so the current time was used[^channel-no-created-ts]

- **G000002** (`mpdm-admin--bwilson--dlee-1`)

### Direct channels

#### 7 notes: has no valid creation timestamp in the export, so the current time was used[^channel-no-created-ts]

- **D000004** (`bwilson, cgarcia`)
- **D000006** (`admin, support-ai-agent`)
- **D000014** (`helpdesk-bot, dlee`)
- **D000020** (`admin, efisher`)
- **D000035** (`admin, insights-ai`)
- **D000040** (`efisher, efisher`)
- **D000042** (`admin, bwilson`)

---

[^archived-no-timestamp]: Slack exports carry no dedicated archive timestamp. The channel is imported as archived, dated at the time of the transform rather than when it was really archived.

[^channel-no-created-ts]: Mattermost needs a timestamp to seed each member's read state. The export did not provide one, so the time of the transform is used instead; the channel and its posts are otherwise unaffected.
