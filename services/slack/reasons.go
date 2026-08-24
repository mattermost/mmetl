package slack

import "github.com/mattermost/mmetl/services/intermediate"

// The reasons below are specific to the Slack transform. Reasons shared with
// another provider live in services/intermediate/reasons.go, because a Code may
// only be registered once per process.
var (
	// ReasonDMSingleMember is recorded for a DM or MPIM left with fewer than two
	// members.
	ReasonDMSingleMember = intermediate.RegisterReason(&intermediate.Reason{
		Code:  "dm_single_member",
		Short: "a direct or group channel needs at least two members",
		Detail: "The Mattermost bulk import cannot express a direct or group channel with fewer " +
			"than two members, so the channel is skipped. This usually means every other member " +
			"was skipped first.",
		Skip: true,
	})

	// ReasonChannelNotFound is recorded for posts that name a channel the export
	// does not describe.
	ReasonChannelNotFound = intermediate.RegisterReason(&intermediate.Reason{
		Code:      "channel_not_found",
		Short:     "is in a channel the export does not describe",
		Specifics: "channel %s",
		Detail: "The export contains a posts directory for a channel that appears in none of " +
			"channels.json, groups.json, mpims.json or dms.json, so there is no channel to import " +
			"the posts into. This normally means the export is incomplete.",
		Skip:  true,
		Quiet: true,
	})

	// ReasonPostNoUser is recorded for a post whose author cannot be determined.
	ReasonPostNoUser = intermediate.RegisterReason(&intermediate.Reason{
		Code:  "post_no_user",
		Short: "has no user field in the export",
		Detail: "Every Mattermost post needs an author. The Slack message carried no user, so " +
			"there is nothing to attribute it to and it is skipped.",
		Skip:  true,
		Quiet: true,
	})

	// ReasonPostUnsupportedType is recorded for a Slack message subtype mmetl
	// does not translate.
	ReasonPostUnsupportedType = intermediate.RegisterReason(&intermediate.Reason{
		Code:      "post_unsupported_type",
		Short:     "is of a Slack message type that has no Mattermost equivalent",
		Specifics: "type %s",
		Detail: "Slack has message subtypes (channel bookmarks, workflow steps, and similar) that " +
			"Mattermost has no import representation for, so they are skipped rather than " +
			"imported as misleading plain messages.",
		Skip:  true,
		Quiet: true,
	})

	// ReasonPostPropsTooLarge is recorded for a post skipped because its props
	// exceeded the Mattermost limit and --discard-invalid-props was given.
	ReasonPostPropsTooLarge = intermediate.RegisterReason(&intermediate.Reason{
		Code:  "post_props_too_large",
		Short: "has props larger than Mattermost allows and --discard-invalid-props was given",
		Detail: "The post's Slack attachments do not fit in Mattermost's post props limit. " +
			"`--discard-invalid-props` asks for such posts to be skipped; drop the flag to import " +
			"the message text without its props instead.",
		Skip: true,
	})

	// ReasonPostPropsDropped is recorded for a post imported without its
	// oversized props.
	ReasonPostPropsDropped = intermediate.RegisterReason(&intermediate.Reason{
		Code:  "post_props_dropped",
		Short: "has props larger than Mattermost allows, so the message was imported without them",
		Detail: "The message text is imported unchanged; only the Slack attachment props are left " +
			"out. Use `--discard-invalid-props` to skip these posts entirely instead.",
	})

	// ReasonPostNoComments is recorded for a file comment carrying no comment.
	ReasonPostNoComments = intermediate.RegisterReason(&intermediate.Reason{
		Code:  "post_no_comments",
		Short: "is a file comment with no comment in the export",
		Detail: "The message is a file comment, but the export carries no comment body for it, so " +
			"there is no content to import.",
		Skip: true,
	})

	// ReasonFileAccessDenied is recorded for a file the export references but
	// does not describe.
	ReasonFileAccessDenied = intermediate.RegisterReason(&intermediate.Reason{
		Code:  "file_access_denied",
		Short: "is referenced by a post but has no name in the export, so access to it was denied",
		Detail: "Slack omits the metadata of files the exporting account could not read. Without a " +
			"name there is nothing to fetch or copy, so the attachment is skipped and its post is " +
			"imported without it.",
		Skip: true,
	})

	// ReasonFileAddFailed is recorded for a file that could not be copied or
	// downloaded into the attachments directory.
	ReasonFileAddFailed = intermediate.RegisterReason(&intermediate.Reason{
		Code:      "file_add_failed",
		Short:     "could not be copied into the attachments directory",
		Specifics: "%s",
		Detail: "The attachment is missing from the export zip, or could not be downloaded or " +
			"written. Its post is still imported, without the attachment.",
		Skip: true,
	})

	// ReasonBotNoBotID is recorded for a bot user whose profile carries no bot ID.
	ReasonBotNoBotID = intermediate.RegisterReason(&intermediate.Reason{
		Code:      "bot_no_bot_id",
		Short:     "has no bot ID in its Slack profile, so its user ID was used instead",
		Specifics: "using %s",
		Detail: "Slack normally identifies a bot by a dedicated bot ID. When it is absent the user " +
			"ID stands in, which is stable within this export but will not match the bot ID used " +
			"by any other Slack export of the same workspace.",
	})

	// ReasonArchivedNoTimestamp is recorded for an archived channel with no
	// timestamp to archive it at.
	ReasonArchivedNoTimestamp = intermediate.RegisterReason(&intermediate.Reason{
		Code:  "archived_no_timestamp",
		Short: "is archived but has no update timestamp, so the current time was used as the archive time",
		Detail: "Slack exports carry no dedicated archive timestamp. The channel is imported as " +
			"archived, dated at the time of the transform rather than when it was really archived.",
	})

	// ReasonMPIMMerged is recorded for a group channel merged into another with
	// the same member set.
	ReasonMPIMMerged = intermediate.RegisterReason(&intermediate.Reason{
		Code:      "mpim_merged",
		Short:     "merged into another channel with the same members",
		Specifics: "merged into %s",
		Detail: "Mattermost keys group channels by member-set hash, so two channels with identical " +
			"members would collide on import. They are merged and posts from both are routed to " +
			"the surviving channel.",
	})

	// ReasonPostDuplicateTimestamp is recorded for a post displaced by a later
	// one carrying the same Slack timestamp.
	ReasonPostDuplicateTimestamp = intermediate.RegisterReason(&intermediate.Reason{
		Code:  "post_duplicate_timestamp",
		Short: "shares its timestamp with a later message in the same channel, which replaced it",
		Detail: "Slack timestamps identify a message within its channel, so mmetl keys threads by " +
			"them. Two messages carrying the same timestamp cannot both be kept; the later one " +
			"wins. A duplicate like this means the export is malformed.",
		Skip: true,
	})
)
