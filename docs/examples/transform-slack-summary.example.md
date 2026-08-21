# Slack Transform Summary

| Entity | Transformed | Skipped | Failed |
|---|---|---|---|
| Public channels | 0 | 0 | 0 |
| Private channels | 0 | 0 | 0 |
| Group channels | 1 | 0 | 0 |
| Direct channels | 0 | 0 | 0 |
| Users | 3 | 1 | 0 |
| Bots | 0 | 0 | 0 |
| Posts | 0 | 0 | 0 |
| Direct posts | 1 | 1 | 0 |
| Replies | 0 | 0 | 0 |
| Reactions | 0 | 0 | 0 |
| Attachments | 0 | 0 | 0 |
| Channel memberships | 0 | 1 | 0 |

## Notes

- Dropping message from channelless.guest (U004): author was a skipped user (e.g. a skipped guest)
- Guest user channelless.guest (U004) has no public or private channel membership in the Slack export; Mattermost cannot scope a guest's access without one, so this user (and their memberships/posts) is being skipped. Use --guest-handling=user to import them as a regular member instead.
- Dropping channel membership for channelless.guest (U004): user was skipped (e.g. a skipped guest)
- Dropping thread 1704067260.000200 in channel "g001": its root post was not imported (e.g. its author was a skipped guest); replies from other users are being dropped with it
