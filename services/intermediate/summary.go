package intermediate

// Entity keys used both to key per-row counts and to tag the log lines emitted
// when a source transformer drops an entity (via t.Logger.WithField(EntityKeyField, ...)).
// Keeping the row list here, in the source-agnostic package, means it's shared
// verbatim by every provider (Slack, RocketChat, ...) and by the eventual
// mattermost-server/mmctl import-side counterpart.
const (
	EntityKeyField = "entity"

	EntityPublicChannel     = "public_channel"
	EntityPrivateChannel    = "private_channel"
	EntityGroupChannel      = "group_channel"
	EntityDirectChannel     = "direct_channel"
	EntityUser              = "user"
	EntityBot               = "bot"
	EntityPost              = "post"
	EntityDirectPost        = "direct_post"
	EntityReply             = "reply"
	EntityReaction          = "reaction"
	EntityAttachment        = "attachment"
	EntityChannelMembership = "channel_membership"
)

// entityRows defines the summary table's row order and display names. It is
// intentionally a fixed list (rather than derived from whatever keys happen to
// appear) so the table always has the same shape, whether or not a given
// migration produced any of a particular entity — that's what makes a Slack
// summary and a RocketChat summary (or a future mattermost-server/mmctl import
// summary) directly comparable line-by-line.
var entityRows = []struct {
	Key  string
	Name string
}{
	{EntityPublicChannel, "Public channels"},
	{EntityPrivateChannel, "Private channels"},
	{EntityGroupChannel, "Group channels"},
	{EntityDirectChannel, "Direct channels"},
	{EntityUser, "Users"},
	{EntityBot, "Bots"},
	{EntityPost, "Posts"},
	{EntityDirectPost, "Direct posts"},
	{EntityReply, "Replies"},
	{EntityReaction, "Reactions"},
	{EntityAttachment, "Attachments"},
	{EntityChannelMembership, "Channel memberships"},
}

// EntityCount is one row of the summary table.
type EntityCount struct {
	Key         string
	Name        string
	Transformed int
	Skipped     int
	// Failed is always 0 for a transform-side summary: mmetl has no per-entity
	// failure mode today (it either skips an entity gracefully, counted above,
	// or aborts the whole run via ExitFunc). The column exists so this table
	// lines up with the future mattermost-server/mmctl import-side summary,
	// which does have a meaningful per-entity Failed count.
	Failed int
}

// BuildCounts computes the Transformed count for every entity row directly
// from the final Intermediate representation. Skipped counts are filled in
// separately by the caller from a WarningCollector's tallies (BuildCounts has
// no visibility into entities that were dropped before ever reaching inter).
func BuildCounts(inter *Intermediate) []EntityCount {
	counts := make(map[string]int, len(entityRows))

	counts[EntityPublicChannel] = len(inter.PublicChannels)
	counts[EntityPrivateChannel] = len(inter.PrivateChannels)
	counts[EntityGroupChannel] = len(inter.GroupChannels)
	counts[EntityDirectChannel] = len(inter.DirectChannels)

	var replies, reactions, attachments int
	countPostTree := func(post *IntermediatePost) {
		reactions += len(post.Reactions)
		attachments += len(post.Attachments)
		for _, reply := range post.Replies {
			replies++
			reactions += len(reply.Reactions)
			attachments += len(reply.Attachments)
		}
	}

	for _, post := range inter.Posts {
		if post.IsDirect {
			counts[EntityDirectPost]++
		} else {
			counts[EntityPost]++
		}
		countPostTree(post)
	}
	counts[EntityReply] = replies
	counts[EntityReaction] = reactions
	counts[EntityAttachment] = attachments

	var users, bots, memberships int
	for _, user := range inter.UsersById {
		if user.IsBot {
			bots++
		} else {
			users++
		}
		// Only public/private channel membership is tracked on the user side
		// (IntermediateUser.Memberships) — see PopulateUserMemberships. Group
		// and direct channel membership lives on the channel side instead
		// (IntermediateChannel.Members), summed below, so both are counted.
		memberships += len(user.Memberships)
	}
	for _, ch := range inter.GroupChannels {
		memberships += len(ch.Members)
	}
	for _, ch := range inter.DirectChannels {
		memberships += len(ch.Members)
	}
	counts[EntityUser] = users
	counts[EntityBot] = bots
	counts[EntityChannelMembership] = memberships

	result := make([]EntityCount, len(entityRows))
	for i, row := range entityRows {
		result[i] = EntityCount{Key: row.Key, Name: row.Name, Transformed: counts[row.Key]}
	}
	return result
}

// ApplySkipped merges per-entity skip tallies (as collected by a
// WarningCollector) into counts, matching by Key. Unknown keys in skipped are
// ignored rather than added as new rows, keeping the table's row list fixed.
func ApplySkipped(counts []EntityCount, skipped map[string]int) []EntityCount {
	for i := range counts {
		counts[i].Skipped = skipped[counts[i].Key]
	}
	return counts
}
