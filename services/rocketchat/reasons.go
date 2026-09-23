package rocketchat

import "github.com/mattermost/mmetl/services/intermediate"

// The reasons below are specific to the RocketChat transform. Reasons shared
// with another provider live in services/intermediate/reasons.go, because a
// Code may only be registered once per process.
var (
	// ReasonUserUnsupportedType is recorded for RocketChat accounts that are
	// neither people nor bots.
	ReasonUserUnsupportedType = intermediate.RegisterReason(&intermediate.Reason{
		Code:      "user_unsupported_type",
		Short:     "is a RocketChat account type that has no Mattermost equivalent",
		Specifics: "type %s",
		Detail: `Only accounts of type "user" and "bot" are migrated. App-owned accounts (such ` +
			"as rocket.cat) have no Mattermost counterpart, so they are skipped along with their " +
			"memberships, posts and reactions.",
		Skip: true,
	})

	// ReasonDMAllMembersSkipped is recorded for a DM or group room left with no
	// members at all.
	ReasonDMAllMembersSkipped = intermediate.RegisterReason(&intermediate.Reason{
		Code:  "dm_all_members_skipped",
		Short: "every member of the room was a skipped user",
		Detail: "A direct or group conversation needs at least one participant that is in the " +
			"import file. All of this room's members were skipped, so the room and its messages " +
			"are skipped with them.",
		Skip: true,
	})

	// ReasonDMMemberCountMismatch is recorded for a DM room whose parallel member
	// arrays disagree.
	ReasonDMMemberCountMismatch = intermediate.RegisterReason(&intermediate.Reason{
		Code:      "dm_member_count_mismatch",
		Short:     "has a malformed member list: its user IDs and usernames do not line up",
		Specifics: "%s user IDs against %s usernames",
		Detail: "RocketChat stores a direct room's members as two parallel arrays. When they are " +
			"different lengths the members cannot be paired reliably, so the room is skipped " +
			"rather than imported with the wrong participants.",
		Skip: true,
	})

	// ReasonRoomEncrypted is recorded for end-to-end encrypted rooms.
	ReasonRoomEncrypted = intermediate.RegisterReason(&intermediate.Reason{
		Code:  "room_encrypted",
		Short: "is end-to-end encrypted",
		Detail: "The dump stores the messages of an end-to-end encrypted room as ciphertext that " +
			"only its participants' clients can read, so there is nothing usable to import.",
		Skip: true,
	})

	// ReasonRoomUnknownType is recorded for rooms whose RocketChat type is not
	// one mmetl handles.
	ReasonRoomUnknownType = intermediate.RegisterReason(&intermediate.Reason{
		Code:      "room_unknown_type",
		Short:     "is of a RocketChat room type that has no Mattermost equivalent",
		Specifics: "type %s",
		Detail: `Only channels ("c"), private groups ("p") and direct rooms ("d") are migrated. ` +
			"Anything else is skipped, along with the messages in it.",
		Skip: true,
	})

	// ReasonSubscriptionUnknownUser is recorded for a subscription pointing at a
	// user the dump does not describe.
	ReasonSubscriptionUnknownUser = intermediate.RegisterReason(&intermediate.Reason{
		Code:  "subscription_unknown_user",
		Short: "references a user that is not in the dump",
		Detail: "The subscription names a user the `users` collection does not contain, so there " +
			"is no account to give the membership to. This usually means the dump is incomplete.",
		Skip: true,
	})

	// ReasonMessageUnsupportedType is recorded for RocketChat system messages
	// that produce no Mattermost post.
	ReasonMessageUnsupportedType = intermediate.RegisterReason(&intermediate.Reason{
		Code:      "message_unsupported_type",
		Short:     "is a RocketChat system message with no Mattermost equivalent",
		Specifics: "type %s",
		Detail: "RocketChat records room events (pins, privacy changes, topic edits and similar) " +
			"as messages. Mattermost has no import representation for them, so they are skipped; " +
			"joins and leaves, which do have one, are imported.",
		Skip:  true,
		Quiet: true,
	})

	// ReasonMessageUnknownRoom is recorded for a message in a room the dump does
	// not describe.
	ReasonMessageUnknownRoom = intermediate.RegisterReason(&intermediate.Reason{
		Code:      "message_unknown_room",
		Short:     "is in a room that is not in the dump",
		Specifics: "room %s",
		Detail: "The message names a room the `rocketchat_room` collection does not contain, so " +
			"there is no channel to import it into. This usually means the dump is incomplete.",
		Skip:  true,
		Quiet: true,
	})

	// ReasonUploadIncomplete is recorded for uploads RocketChat never finished
	// storing.
	ReasonUploadIncomplete = intermediate.RegisterReason(&intermediate.Reason{
		Code:  "upload_incomplete",
		Short: "was never finished uploading in RocketChat",
		Detail: "The upload record is not marked complete, so its bytes are either missing or " +
			"partial. Importing it would attach a corrupt file to the post.",
		Skip:  true,
		Quiet: true,
	})

	// ReasonUploadThumbnail is recorded for the generated thumbnails RocketChat
	// stores alongside real uploads.
	ReasonUploadThumbnail = intermediate.RegisterReason(&intermediate.Reason{
		Code:  "upload_thumbnail",
		Short: "is a thumbnail RocketChat generated for another upload",
		Detail: "Mattermost generates its own previews, so importing RocketChat's would duplicate " +
			"every image attachment. The full-size upload is imported instead.",
		Skip:  true,
		Quiet: true,
	})

	// ReasonUploadGridFSMissing is recorded for a GridFS upload with no chunks in
	// the dump.
	ReasonUploadGridFSMissing = intermediate.RegisterReason(&intermediate.Reason{
		Code:  "upload_gridfs_missing",
		Short: "is stored in GridFS but the dump contains none of its chunks",
		Detail: "The file's bytes live in `rocketchat_uploads.chunks`, which is either absent from " +
			"the dump or missing this file. Re-run mongodump including that collection to import " +
			"the attachment.",
		Skip:  true,
		Quiet: true,
	})

	// ReasonUploadNoUploadsDir is recorded for FileSystem uploads when
	// --uploads-dir was not given.
	ReasonUploadNoUploadsDir = intermediate.RegisterReason(&intermediate.Reason{
		Code:  "upload_no_uploads_dir",
		Short: "is stored on the filesystem but --uploads-dir was not given",
		Detail: "The dump only records where the file was, not its contents. Pass `--uploads-dir` " +
			"pointing at the RocketChat uploads directory to include these attachments.",
		Skip:  true,
		Quiet: true,
	})

	// ReasonUploadUnsafePath is recorded for uploads whose stored path cannot be
	// resolved safely.
	ReasonUploadUnsafePath = intermediate.RegisterReason(&intermediate.Reason{
		Code:      "upload_unsafe_path",
		Short:     "has a source path that cannot be resolved safely",
		Specifics: "path %s",
		Detail: "The upload's path would resolve outside the uploads directory, so it is not read. " +
			"A path like this in a dump is a sign the dump was tampered with.",
		Skip: true,
	})

	// ReasonUploadUnknownStore is recorded for uploads held in a storage backend
	// mmetl cannot read.
	ReasonUploadUnknownStore = intermediate.RegisterReason(&intermediate.Reason{
		Code:      "upload_unknown_store",
		Short:     "is held in a storage backend mmetl cannot read",
		Specifics: "store %s",
		Detail: "Only GridFS and FileSystem uploads can be extracted from a mongodump. Files kept " +
			"in Amazon S3, Google Cloud Storage or WebDAV have to be copied into the attachments " +
			"directory by hand.",
		Skip:  true,
		Quiet: true,
	})

	// ReasonUploadExtractFailed is recorded for uploads that could not be written
	// into the attachments directory.
	ReasonUploadExtractFailed = intermediate.RegisterReason(&intermediate.Reason{
		Code:  "upload_extract_failed",
		Short: "could not be written into the attachments directory",
		Detail: "Reading the file from the dump, or writing it into the attachments directory, " +
			"failed. Any partial file was removed so it cannot be imported as a corrupt " +
			"attachment; its post is still imported, without the attachment.",
		Skip:  true,
		Quiet: true,
	})

	// ReasonRoomNoName is recorded for a room the dump gives no name, whose
	// channel handle therefore has to be derived from its ID.
	ReasonRoomNoName = intermediate.RegisterReason(&intermediate.Reason{
		Code:  "room_no_name",
		Short: "has no name in the dump, so its ID was used to build a channel handle",
		Detail: "Mattermost channels need a handle. The room ID is used, which imports cleanly but " +
			"is not readable; rename the channel after the import.",
	})
)
