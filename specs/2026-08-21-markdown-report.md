# transform report file

| x       | y          |
| ------- | ---------- |
| State   | Draft      |
| Version | 0.1        |
| Date    | 2026-08-22 |

## Overview

The proposal is to implement a way for users to have a better view on which entities had
been properly converted during the transform process whithout having to peek through the
logs manually, and give context on why an entity was not migrated.

## Data storage example

In memory storage of the transform process, to record the processes for all entities and
use that to present a report to the user when the command finishes.

```go
type Report struct {
	Users UsersReport
	Channels ChannelsReport
	// ...
}

type UsersReport struct {
	Transformed int
	Skipped int
	Notes []ReportEntityNote
}

type ReportEntityNote struct {
	EntityID string // user_id, channel_id, ...
	EntityName string // username, channel_name, ... (if available)
	Message string
}
```

## Report file example

The report file created at the end of the execution, it should contain

```md
# Slack Transform Report

## Summary

| Entity                              | Transformed | Skipped | Failed |
| ----------------------------------- | ----------- | ------- | ------ |
| [Users](#Users)                     | 3           | 1       | 0      |
| [Public channels](#Public+Channels) | 0           | 0       | 0      |
| ...                                 |             |         |        |

## Entity

### Users

- **U004** (`channelless.guest`): has no public or private channel membership in the Slack export; Mattermost cannot scope a guest's access without one, so this user (and their memberships/posts) is being skipped. Use --guest-handling=user to import them as a regular member instead.

### Threads

- **1704067260.000200**: Dropping thread in channel **g001** cause user was skipped.

### EntityName

- **{entityID}** ({entityName} if present) - {Message}
```

## Changelog

| Date       | Note                                                                                             |
| ---------- | ------------------------------------------------------------------------------------------------ |
| 2026-08-22 | Intial definition of the proposal on how the data is stored and how the report should look like. |
