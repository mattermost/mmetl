package intermediate

import (
	"fmt"
	"sort"
	"strings"
)

// Reason explains, once and in one place, why an entity was skipped or what was
// done to it. Every note in a Report references a registered Reason by Code, so
// the log line and the report entry can never drift apart.
type Reason struct {
	// Code is stable and machine-readable; it is also the Markdown footnote
	// anchor and the JSON key. Never change a Code without a changelog entry.
	Code string `json:"code"`

	// Short is the inline text rendered as the heading of the group of entities
	// that share this reason. It is identical for every note referencing the
	// reason — anything entity-specific belongs in Specifics.
	Short string `json:"short"`

	// Specifics is an optional fmt template for the per-entity detail rendered
	// after the entity in the Markdown bullet, and appended to the log line. It
	// is filled from ReportEntityNote.Args and carries what is unique to one
	// entity (which channel a duplicate merged into, how many posts a split
	// produced), leaving Short shared so notes can be grouped under one heading.
	Specifics string `json:"specifics,omitempty"`

	// Detail is the long explanation and remediation hint. It is rendered once,
	// as a Markdown footnote, no matter how many entities reference it.
	Detail string `json:"detail"`

	// Skip is true when this reason means the entity did not reach the import
	// file. False for notes on entities that were transformed with changes.
	Skip bool `json:"skip"`

	// Quiet suppresses the per-entity log line for high-cardinality reasons
	// (e.g. every reply of a dropped thread). The note is still recorded in
	// full; an aggregate line is logged once at the end of the run.
	Quiet bool `json:"quiet,omitempty"`
}

// specifics renders the per-entity detail for a note's args, or "" when the
// reason carries no template or the note carries no args. wrap is applied to
// each argument before it is substituted, so the Markdown renderer can put
// source-derived values in code spans while the log line keeps them plain; pass
// nil to substitute the arguments verbatim.
func (r *Reason) specifics(args []string, wrap func(string) string) string {
	if r == nil || r.Specifics == "" || len(args) == 0 {
		return ""
	}
	values := make([]any, len(args))
	for i, arg := range args {
		if wrap != nil {
			arg = wrap(arg)
		}
		values[i] = arg
	}
	return fmt.Sprintf(r.Specifics, values...)
}

// reasonRegistry holds every reason declared across the shared package and the
// provider packages, keyed by Code. It is populated from package-level
// variable initialization, which runs before any transform, so it is only ever
// read concurrently.
var reasonRegistry = map[string]*Reason{}

// RegisterReason adds a reason to the shared registry and returns it, so it can
// be assigned to a package-level variable. It panics on a duplicate or empty
// Code, which turns a collision into an init-time failure rather than a report
// that silently attributes two different problems to the same footnote.
func RegisterReason(reason *Reason) *Reason {
	if reason == nil || reason.Code == "" {
		panic("intermediate: a reason must have a non-empty Code")
	}
	if existing, ok := reasonRegistry[reason.Code]; ok {
		panic(fmt.Sprintf("intermediate: duplicate reason code %q (already registered with short text %q)", reason.Code, existing.Short))
	}
	reasonRegistry[reason.Code] = reason
	return reason
}

// LookupReason returns the registered reason for code, or nil when no reason
// with that code has been declared.
func LookupReason(code string) *Reason {
	return reasonRegistry[code]
}

// RegisteredReasonCodes returns every registered code in sorted order. It
// exists for tests that assert the registry is coherent.
func RegisteredReasonCodes() []string {
	codes := make([]string, 0, len(reasonRegistry))
	for code := range reasonRegistry {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	return codes
}

// The reasons below are shared: they are recorded by more than one provider, so
// they are declared here rather than in a provider package, where a second
// declaration of the same Code would panic at init.

var (
	// ReasonGuestSkipMode is recorded for guests dropped by --guest-handling=skip.
	ReasonGuestSkipMode = RegisterReason(&Reason{
		Code:  "guest_skip_mode",
		Short: "is a guest and --guest-handling=skip was given",
		Detail: "The run was asked to drop guest users entirely, so this user, their channel " +
			"memberships and everything they authored are left out of the import file. Use " +
			"`--guest-handling=guest` or `--guest-handling=user` to migrate them instead.",
		Skip: true,
	})

	// ReasonGuestNoChannel is recorded for guests that have no public or private
	// channel to scope their guest access to.
	ReasonGuestNoChannel = RegisterReason(&Reason{
		Code:  "guest_no_channel",
		Short: "has no public or private channel membership in the export",
		Detail: "Mattermost cannot scope a guest's access without at least one public or private " +
			"channel membership, so this user (and their memberships and posts) is skipped. Use " +
			"`--guest-handling=user` to import them as a regular member instead.",
		Skip: true,
	})

	// ReasonMembershipSkippedUser is recorded for a channel membership removed
	// because the member was a skipped user.
	ReasonMembershipSkippedUser = RegisterReason(&Reason{
		Code:  "membership_skipped_user",
		Short: "the member was a skipped user",
		Detail: "The membership referenced a user that is not in the import file, which the bulk " +
			"importer would reject as a dangling reference, so the membership is dropped with them.",
		Skip:  true,
		Quiet: true,
	})

	// ReasonMembershipChannelSkipped is recorded for a channel membership dropped
	// because the channel itself was skipped.
	ReasonMembershipChannelSkipped = RegisterReason(&Reason{
		Code:  "membership_channel_skipped",
		Short: "the channel the membership belonged to was skipped",
		Detail: "The channel did not reach the import file, so the memberships that pointed at it " +
			"are dropped with it. The reason the channel was skipped is listed in that channel's " +
			"own section of this report.",
		Skip:  true,
		Quiet: true,
	})

	// ReasonPostSkippedAuthor is recorded for a post dropped because its author
	// was skipped.
	ReasonPostSkippedAuthor = RegisterReason(&Reason{
		Code:  "post_skipped_author",
		Short: "the author was a skipped user",
		Detail: "The post's author was skipped, so the post is dropped to avoid a dangling " +
			"reference in the import file.",
		Skip:  true,
		Quiet: true,
	})

	// ReasonReactionSkippedUser is recorded for a reaction dropped because the
	// reacting user was skipped.
	ReasonReactionSkippedUser = RegisterReason(&Reason{
		Code:  "reaction_skipped_user",
		Short: "the reacting user was skipped",
		Detail: "The reaction was left by a user that is not in the import file, so it is dropped " +
			"to avoid a dangling reference.",
		Skip:  true,
		Quiet: true,
	})

	// ReasonThreadRootMissing is recorded for a thread (and each of its replies)
	// dropped because the root post was never imported.
	ReasonThreadRootMissing = RegisterReason(&Reason{
		Code:  "thread_root_missing",
		Short: "the thread root was not imported",
		Detail: "The thread's root post was not imported (for example, its author was a skipped " +
			"guest). Replies from other users are dropped with it so the thread is not partially " +
			"imported.",
		Skip:  true,
		Quiet: true,
	})

	// ReasonUserPlaceholderCreated is recorded when a user referenced by a
	// channel, post or reaction is missing from the source user list.
	ReasonUserPlaceholderCreated = RegisterReason(&Reason{
		Code:  "user_placeholder_created",
		Short: "was referenced by the export but missing from its user list, so a placeholder was created",
		Detail: "Channels, posts and reactions may reference users that the export does not " +
			"describe. A deactivated placeholder user is created for each one so the references " +
			"stay valid; the account carries no real name or email.",
	})

	// ReasonPostSplit is recorded for a post split into a thread because it
	// exceeded the maximum message length.
	ReasonPostSplit = RegisterReason(&Reason{
		Code:      "post_split",
		Short:     "split into a thread because it exceeded the maximum message length",
		Specifics: "split into %s posts",
		Detail: "Mattermost limits a single post's length. Longer posts are split, with the " +
			"remainder posted as replies in a thread under the original.",
	})

	// ReasonPostRepliesSplit is recorded for a post whose replies had to be split
	// because they exceeded the maximum message length.
	ReasonPostRepliesSplit = RegisterReason(&Reason{
		Code:      "post_replies_split",
		Short:     "has replies that exceeded the maximum message length and were split",
		Specifics: "replies split: %s",
		Detail: "Mattermost limits a single post's length. An oversized reply is split into " +
			"sibling replies under the same thread, in order, so none of the text is lost.",
	})

	// ReasonMPIMConvertedToPrivate is recorded for a group channel that had too
	// many members to stay a group channel.
	ReasonMPIMConvertedToPrivate = RegisterReason(&Reason{
		Code:      "mpim_converted_to_private",
		Short:     "had too many members for a group channel and was converted to a private channel",
		Specifics: "%s members",
		Detail: "Mattermost group messages support a limited number of participants. Larger group " +
			"conversations are imported as private channels instead, which preserves the members " +
			"and the history but changes the channel type.",
	})

	// ReasonEmailBlank is recorded for a user imported with an empty email
	// address under --skip-empty-emails.
	ReasonEmailBlank = RegisterReason(&Reason{
		Code:  "email_blank",
		Short: "has no email address in the export and was imported with a blank one",
		Detail: "`--skip-empty-emails` was given, so the user is imported without an email " +
			"address. This is invalid data for Mattermost: the account cannot be used to log in " +
			"until an administrator sets an email address.",
	})

	// ReasonEmailPlaceholder is recorded for a user given a generated email
	// address from --default-email-domain.
	ReasonEmailPlaceholder = RegisterReason(&Reason{
		Code:      "email_placeholder",
		Short:     "has no email address in the export, so a placeholder was generated",
		Specifics: "using %s",
		Detail: "`--default-email-domain` was given, so the address was built from the username " +
			"and that domain. The user should update their email address once logged in.",
	})

	// ReasonFirstNameTruncated is recorded when a user's first name exceeded the
	// Mattermost limit.
	ReasonFirstNameTruncated = RegisterReason(&Reason{
		Code:   "first_name_truncated",
		Short:  "first name exceeded the maximum length and was truncated",
		Detail: "Mattermost limits the first name field length; the value was truncated to fit and the user can update it after logging in.",
	})

	// ReasonLastNameTruncated is recorded when a user's last name exceeded the
	// Mattermost limit.
	ReasonLastNameTruncated = RegisterReason(&Reason{
		Code:   "last_name_truncated",
		Short:  "last name exceeded the maximum length and was truncated",
		Detail: "Mattermost limits the last name field length; the value was truncated to fit and the user can update it after logging in.",
	})

	// ReasonPositionTruncated is recorded when a user's position exceeded the
	// Mattermost limit.
	ReasonPositionTruncated = RegisterReason(&Reason{
		Code:   "position_truncated",
		Short:  "position exceeded the maximum length and was truncated",
		Detail: "Mattermost limits the position field length; the value was truncated to fit and the user can update it after logging in.",
	})

	// ReasonChannelNameTruncated is recorded when a channel handle exceeded the
	// Mattermost limit.
	ReasonChannelNameTruncated = RegisterReason(&Reason{
		Code:   "channel_name_truncated",
		Short:  "handle exceeded the maximum length and was truncated",
		Detail: "Mattermost limits the channel handle (URL name) length. The handle was truncated; the channel's display name is unaffected.",
	})

	// ReasonChannelNameInvalidChars is recorded when a channel handle could not
	// be expressed with the characters Mattermost allows.
	ReasonChannelNameInvalidChars = RegisterReason(&Reason{
		Code:      "channel_name_invalid_chars",
		Short:     "handle contained characters Mattermost does not allow, so the source channel ID was used instead",
		Specifics: "handle is now %s",
		Detail: "Mattermost channel handles are limited to letters, digits, hyphens and " +
			"underscores. When nothing usable remains the source channel ID is used so the channel " +
			"still imports; rename it after the import if the handle matters.",
	})

	// ReasonChannelDisplayTruncated is recorded when a channel display name
	// exceeded the Mattermost limit.
	ReasonChannelDisplayTruncated = RegisterReason(&Reason{
		Code:   "channel_display_truncated",
		Short:  "display name exceeded the maximum length and was truncated",
		Detail: "Mattermost limits the channel display name length; the value was truncated to fit and can be edited after the import.",
	})

	// ReasonChannelPurposeTruncated is recorded when a channel purpose exceeded
	// the Mattermost limit.
	ReasonChannelPurposeTruncated = RegisterReason(&Reason{
		Code:   "channel_purpose_truncated",
		Short:  "purpose exceeded the maximum length and was truncated",
		Detail: "Mattermost limits the channel purpose length; the value was truncated to fit and can be edited after the import.",
	})

	// ReasonChannelHeaderTruncated is recorded when a channel header exceeded the
	// Mattermost limit.
	ReasonChannelHeaderTruncated = RegisterReason(&Reason{
		Code:   "channel_header_truncated",
		Short:  "header exceeded the maximum length and was truncated",
		Detail: "Mattermost limits the channel header length; the value was truncated to fit and can be edited after the import.",
	})

	// ReasonChannelNoCreatedTimestamp is recorded when a channel carries no
	// usable creation timestamp in the export.
	ReasonChannelNoCreatedTimestamp = RegisterReason(&Reason{
		Code:  "channel_no_created_ts",
		Short: "has no valid creation timestamp in the export, so the current time was used",
		Detail: "Mattermost needs a timestamp to seed each member's read state. The export did not " +
			"provide one, so the time of the transform is used instead; the channel and its posts " +
			"are otherwise unaffected.",
	})

	// ReasonEmojiRenamed is recorded when an emoji or reaction name had to be
	// rewritten to a Mattermost-valid name.
	ReasonEmojiRenamed = RegisterReason(&Reason{
		Code:      "emoji_renamed",
		Short:     "is not a valid Mattermost emoji name and was renamed",
		Specifics: "renamed to %s",
		Detail: "Mattermost emoji names are limited to letters, digits, hyphens, underscores and " +
			"plus signs. The same source name always maps to the same new name within a run, so " +
			"reactions stay grouped.",
	})

	// ReasonGuestDemotedToUser is recorded when a guest could not be exported
	// with guest roles and fell back to a regular member.
	ReasonGuestDemotedToUser = RegisterReason(&Reason{
		Code:  "guest_demoted_to_user",
		Short: "has no channel memberships, so it is imported as a regular member instead of a guest",
		Detail: "Mattermost requires a guest to be a guest in every scope at once, which needs at " +
			"least one channel membership. Rather than emit a line the bulk importer would reject, " +
			"the user is imported as a regular member.",
	})
)

// reasonSummaryLine renders "code: short (n)" for the aggregate log line of a
// quiet reason.
func reasonSummaryLine(code string, count int) string {
	reason := LookupReason(code)
	if reason == nil {
		return fmt.Sprintf("%s=%d", code, count)
	}
	return fmt.Sprintf("%s (%s)=%d", code, strings.TrimSpace(reason.Short), count)
}
