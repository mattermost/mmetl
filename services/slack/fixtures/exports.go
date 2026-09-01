package fixtures

import (
	"github.com/mattermost/mmetl/services/slack"
)

// ExportWithGuests creates an export with a regular user, a multi-channel
// guest (is_restricted) with access to two channels, and a single-channel
// guest (is_ultra_restricted) with access to only one.
func ExportWithGuests() *SlackExportBuilder {
	return NewSlackExportBuilder().
		AddUser(slack.SlackUser{
			Id:       "U001",
			Username: "regular.user",
			Profile: slack.SlackProfile{
				RealName: "Regular User",
				Email:    "regular.user@example.com",
			},
		}).
		AddUser(slack.SlackUser{
			Id:           "U002",
			Username:     "multi.guest",
			IsRestricted: true,
			Profile: slack.SlackProfile{
				RealName: "Multi Guest",
				Email:    "multi.guest@example.com",
			},
		}).
		AddUser(slack.SlackUser{
			Id:                "U003",
			Username:          "single.guest",
			IsRestricted:      true,
			IsUltraRestricted: true,
			Profile: slack.SlackProfile{
				RealName: "Single Guest",
				Email:    "single.guest@example.com",
			},
		}).
		AddChannel(slack.SlackChannel{
			Id:      "C001",
			Name:    "general",
			Creator: "U001",
			Created: 1704067200,
			Members: []string{"U001", "U002", "U003"},
			Purpose: slack.SlackChannelSub{Value: "General discussion"},
		}).
		AddChannel(slack.SlackChannel{
			Id:      "C002",
			Name:    "random",
			Creator: "U001",
			Created: 1704070800,
			Members: []string{"U001", "U002"},
			Purpose: slack.SlackChannelSub{Value: "Non-work banter"},
		}).
		// DM between the regular user and the multi-channel guest, to verify
		// guests are marked scheme_guest (not scheme_user) in direct channels too.
		AddDirectChannel(slack.SlackChannel{
			Id:      "D001",
			Created: 1704067200,
			Members: []string{"U001", "U002"},
		})
}

// ExportWithGuestPosts extends ExportWithGuests with posts that exercise the
// guest-handling modes end-to-end:
//   - a standalone post by the regular user (survives every mode),
//   - a standalone post by a guest,
//   - a thread whose root is authored by a guest, with a reply from the
//     regular user.
//
// In "skip" mode the guest's standalone post and the entire guest-rooted
// thread — including the non-guest reply, since its root is gone — are dropped,
// while the regular user's standalone post survives. In "guest"/"user" mode all
// posts are imported.
func ExportWithGuestPosts() *SlackExportBuilder {
	return ExportWithGuests().
		AddPost("general", slack.SlackPost{
			User:      "U001",
			Text:      "Regular user standalone post",
			TimeStamp: "1704067200.000100",
			Type:      "message",
		}).
		AddPost("general", slack.SlackPost{
			User:      "U003",
			Text:      "Guest standalone post",
			TimeStamp: "1704067260.000200",
			Type:      "message",
		}).
		AddPost("general", slack.SlackPost{
			User:      "U002",
			Text:      "Guest-rooted thread root",
			TimeStamp: "1704067320.000300",
			ThreadTS:  "1704067320.000300",
			Type:      "message",
		}).
		AddPost("general", slack.SlackPost{
			User:      "U001",
			Text:      "Regular user reply in guest thread",
			TimeStamp: "1704067380.000400",
			ThreadTS:  "1704067320.000300",
			Type:      "message",
		})
}

// SlackBasicExport creates a simple export with users and channels (no posts)
func SlackBasicExport() *SlackExportBuilder {
	return NewSlackExportBuilder().
		AddUser(slack.SlackUser{
			Id:       "U001",
			Username: "john.doe",
			IsBot:    false,
			Profile: slack.SlackProfile{
				RealName: "John Doe",
				Email:    "john.doe@example.com",
				Title:    "Software Engineer",
			},
			Deleted: false,
		}).
		AddUser(slack.SlackUser{
			Id:       "U002",
			Username: "jane.smith",
			IsBot:    false,
			Profile: slack.SlackProfile{
				RealName: "Jane Smith",
				Email:    "jane.smith@example.com",
				Title:    "Product Manager",
			},
			Deleted: false,
		}).
		AddChannel(slack.SlackChannel{
			Id:      "C001",
			Name:    "general",
			Creator: "U001",
			Created: 1704067200,
			Members: []string{"U001", "U002"},
			Purpose: slack.SlackChannelSub{Value: "Company-wide announcements"},
			Topic:   slack.SlackChannelSub{Value: "Welcome to the team!"},
		}).
		AddChannel(slack.SlackChannel{
			Id:      "C002",
			Name:    "random",
			Creator: "U002",
			Created: 1704070800,
			Members: []string{"U001", "U002"},
			Purpose: slack.SlackChannelSub{Value: "Non-work banter"},
			Topic:   slack.SlackChannelSub{Value: "Water cooler chat"},
		})
}

// ExportWithPosts creates an export with users, channels, and posts
func ExportWithPosts() *SlackExportBuilder {
	return SlackBasicExport().
		AddPost("general", slack.SlackPost{
			User:      "U001",
			Text:      "Hello everyone!",
			TimeStamp: "1704067200.000100",
			Type:      "message",
		}).
		AddPost("general", slack.SlackPost{
			User:      "U002",
			Text:      "Welcome to the team, @john.doe!",
			TimeStamp: "1704067260.000200",
			Type:      "message",
		}).
		AddPost("random", slack.SlackPost{
			User:      "U001",
			Text:      "Anyone up for coffee?",
			TimeStamp: "1704070800.000300",
			Type:      "message",
		})
}

// ExportWithThreads creates an export with threaded conversations
func ExportWithThreads() *SlackExportBuilder {
	return SlackBasicExport().
		AddPost("general", slack.SlackPost{
			User:      "U001",
			Text:      "Let's discuss the new feature",
			TimeStamp: "1704067200.000100",
			ThreadTS:  "1704067200.000100", // Root of thread
			Type:      "message",
		}).
		AddPost("general", slack.SlackPost{
			User:      "U002",
			Text:      "I think we should prioritize performance",
			TimeStamp: "1704067260.000200",
			ThreadTS:  "1704067200.000100", // Reply to thread
			Type:      "message",
		}).
		AddPost("general", slack.SlackPost{
			User:      "U001",
			Text:      "Good point, let's add benchmarks",
			TimeStamp: "1704067320.000300",
			ThreadTS:  "1704067200.000100", // Another reply
			Type:      "message",
		})
}

// ExportWithMentions creates an export with user and channel mentions,
// including pipe-aliased special mentions and W-prefix enterprise Grid user IDs.
func ExportWithMentions() *SlackExportBuilder {
	return SlackBasicExport().
		AddUser(slack.SlackUser{
			Id:       "W003",
			Username: "grid.user",
			IsBot:    false,
			Profile: slack.SlackProfile{
				RealName: "Grid User",
				Email:    "grid.user@example.com",
			},
		}).
		AddPost("general", slack.SlackPost{
			User:      "U001",
			Text:      "Hey <@U002>, can you review my PR?",
			TimeStamp: "1704067200.000100",
			Type:      "message",
		}).
		AddPost("general", slack.SlackPost{
			User:      "U002",
			Text:      "Sure! Also cc <#C002|random> for visibility",
			TimeStamp: "1704067260.000200",
			Type:      "message",
		}).
		AddPost("general", slack.SlackPost{
			User:      "U001",
			Text:      "<!here> important announcement!",
			TimeStamp: "1704067320.000300",
			Type:      "message",
		}).
		AddPost("general", slack.SlackPost{
			User:      "U001",
			Text:      "<!here|here> pipe-aliased here and <!channel|@channel> pipe-aliased channel",
			TimeStamp: "1704067380.000400",
			Type:      "message",
		}).
		AddPost("general", slack.SlackPost{
			User:      "U001",
			Text:      "Hey <@W003> and <@W003|grid.user>, welcome to the team!",
			TimeStamp: "1704067440.000500",
			Type:      "message",
		})
}

// ExportWithDeletedUser creates an export with a deleted user
func ExportWithDeletedUser() *SlackExportBuilder {
	return NewSlackExportBuilder().
		AddUser(slack.SlackUser{
			Id:       "U001",
			Username: "john.doe",
			IsBot:    false,
			Profile: slack.SlackProfile{
				RealName: "John Doe",
				Email:    "john.doe@example.com",
			},
			Deleted: false,
		}).
		AddUser(slack.SlackUser{
			Id:       "U003",
			Username: "deleted.user",
			IsBot:    false,
			Profile: slack.SlackProfile{
				RealName: "Former Employee",
				Email:    "former@example.com",
			},
			Deleted: true,
		}).
		AddChannel(slack.SlackChannel{
			Id:      "C001",
			Name:    "general",
			Creator: "U001",
			Created: 1704067200,
			Members: []string{"U001", "U003"},
			Purpose: slack.SlackChannelSub{Value: "General discussion"},
			Topic:   slack.SlackChannelSub{Value: ""},
		})
}

// ExportWithBots creates an export with regular users and bot users
func ExportWithBots() *SlackExportBuilder {
	return NewSlackExportBuilder().
		AddUser(slack.SlackUser{
			Id:       "U001",
			Username: "john.doe",
			Profile: slack.SlackProfile{
				RealName: "John Doe",
				Email:    "john.doe@example.com",
				Title:    "Software Engineer",
			},
		}).
		AddUser(slack.SlackUser{
			Id:       "U002",
			Username: "deploybot",
			IsBot:    true,
			Profile: slack.SlackProfile{
				BotID:    "B001",
				RealName: "Deploy Bot",
				Title:    "Handles deployments",
			},
		}).
		AddUser(slack.SlackUser{
			Id:       "U003",
			Username: "alertbot",
			IsBot:    true,
			Profile: slack.SlackProfile{
				BotID:    "B002",
				RealName: "Alert Bot",
			},
		}).
		AddChannel(slack.SlackChannel{
			Id:      "C001",
			Name:    "general",
			Creator: "U001",
			Created: 1704067200,
			Members: []string{"U001"},
			Purpose: slack.SlackChannelSub{Value: "General discussion"},
			Topic:   slack.SlackChannelSub{Value: "Welcome!"},
		})
}

// ExportWithBotPosts creates an export with bot users and their posts
func ExportWithBotPosts() *SlackExportBuilder {
	return ExportWithBots().
		AddPost("general", slack.SlackPost{
			User:      "U001",
			Text:      "Starting the deploy",
			TimeStamp: "1704067200.000100",
			Type:      "message",
		}).
		AddPost("general", slack.SlackPost{
			BotId:     "B001",
			Text:      "Deployment started for v2.0.0",
			TimeStamp: "1704067260.000200",
			Type:      "message",
			SubType:   "bot_message",
		}).
		AddPost("general", slack.SlackPost{
			BotId:     "B002",
			Text:      "Alert: CPU usage above 90%",
			TimeStamp: "1704067320.000300",
			Type:      "message",
			SubType:   "bot_message",
		})
}

// ExportWithDeletedBot creates an export with a deleted (deactivated) bot user
func ExportWithDeletedBot() *SlackExportBuilder {
	return NewSlackExportBuilder().
		AddUser(slack.SlackUser{
			Id:       "U001",
			Username: "john.doe",
			Profile: slack.SlackProfile{
				RealName: "John Doe",
				Email:    "john.doe@example.com",
			},
		}).
		AddUser(slack.SlackUser{
			Id:       "U002",
			Username: "oldbot",
			IsBot:    true,
			Deleted:  true,
			Profile: slack.SlackProfile{
				BotID:    "B003",
				RealName: "Old Bot",
			},
		}).
		AddChannel(slack.SlackChannel{
			Id:      "C001",
			Name:    "general",
			Creator: "U001",
			Created: 1704067200,
			Members: []string{"U001"},
		})
}

// ExportWithArchivedChannels creates an export containing both active and archived channels.
// The archived channel has is_archived=true with an updated timestamp.
func ExportWithArchivedChannels() *SlackExportBuilder {
	return NewSlackExportBuilder().
		AddUser(slack.SlackUser{
			Id:       "U001",
			Username: "john.doe",
			Profile: slack.SlackProfile{
				RealName: "John Doe",
				Email:    "john.doe@example.com",
				Title:    "Software Engineer",
			},
		}).
		AddChannel(slack.SlackChannel{
			Id:         "C001",
			Name:       "general",
			Creator:    "U001",
			Members:    []string{"U001"},
			Purpose:    slack.SlackChannelSub{Value: "General discussion"},
			Topic:      slack.SlackChannelSub{Value: "Welcome!"},
			IsArchived: false,
		}).
		AddChannel(slack.SlackChannel{
			Id:         "C002",
			Name:       "old-project",
			Creator:    "U001",
			Members:    []string{"U001"},
			Purpose:    slack.SlackChannelSub{Value: "Old project channel"},
			Topic:      slack.SlackChannelSub{Value: ""},
			IsArchived: true,
			Updated:    1620000000000, // ms timestamp used as archive time
		})
}

// ExportWithDirectMessages creates an export with two users, public channels with
// posts, and a direct message channel with posts. Used to verify that last_viewed_at
// is set correctly on both regular channel members and DM participants after import.
func ExportWithDirectMessages() *SlackExportBuilder {
	return SlackBasicExport().
		AddPost("general", slack.SlackPost{
			User:      "U001",
			Text:      "Hello everyone!",
			TimeStamp: "1704067200.000100",
			Type:      "message",
		}).
		AddPost("general", slack.SlackPost{
			User:      "U002",
			Text:      "Welcome to the team!",
			TimeStamp: "1704067260.000200",
			Type:      "message",
		}).
		AddDirectChannel(slack.SlackChannel{
			Id:      "D001",
			Created: 1704067200,
			Members: []string{"U001", "U002"},
		}).
		AddPost("D001", slack.SlackPost{
			User:      "U001",
			Text:      "Hey, want to grab lunch?",
			TimeStamp: "1704067500.000100",
			Type:      "message",
		}).
		AddPost("D001", slack.SlackPost{
			User:      "U002",
			Text:      "Sure, let's go!",
			TimeStamp: "1704067560.000200",
			Type:      "message",
		})
}

// ExportWithDuplicateMpims creates an export with three users and two distinct
// Slack MPIMs (group DMs) that share the exact same member set. Slack permits
// multiple MPIMs with identical membership, but Mattermost keys group channels
// by member-set hash — so emitting two `direct_channel` JSONL lines for these
// crashes the bulk importer (see MM-68736). Used to verify that mmetl
// deduplicates them and preserves posts from both Slack channels.
func ExportWithDuplicateMpims() *SlackExportBuilder {
	return NewSlackExportBuilder().
		AddUser(slack.SlackUser{
			Id:       "U001",
			Username: "alice",
			Profile: slack.SlackProfile{
				RealName: "Alice Anderson",
				Email:    "alice@example.com",
			},
		}).
		AddUser(slack.SlackUser{
			Id:       "U002",
			Username: "bob",
			Profile: slack.SlackProfile{
				RealName: "Bob Baker",
				Email:    "bob@example.com",
			},
		}).
		AddUser(slack.SlackUser{
			Id:       "U003",
			Username: "charlie",
			Profile: slack.SlackProfile{
				RealName: "Charlie Carter",
				Email:    "charlie@example.com",
			},
		}).
		// First MPIM. Created earlier; later sorted as the canonical via its
		// lexicographically smaller Id (G001).
		AddGroupChannel(slack.SlackChannel{
			Id:      "G001",
			Name:    "mpdm-alice--bob--charlie-1",
			Creator: "U001",
			Created: 1704067200,
			Members: []string{"U001", "U002", "U003"},
			Topic:   slack.SlackChannelSub{Value: "first mpim topic"},
		}).
		// Second MPIM with identical members but a different Slack channel.
		AddGroupChannel(slack.SlackChannel{
			Id:      "G002",
			Name:    "mpdm-alice--bob--charlie-2",
			Creator: "U002",
			Created: 1704070800,
			Members: []string{"U001", "U002", "U003"},
		}).
		// Two posts in the first MPIM directory.
		AddPost("mpdm-alice--bob--charlie-1", slack.SlackPost{
			User:      "U001",
			Text:      "Message from the first MPIM",
			TimeStamp: "1704067200.000100",
			Type:      "message",
		}).
		AddPost("mpdm-alice--bob--charlie-1", slack.SlackPost{
			User:      "U002",
			Text:      "Reply in the first MPIM",
			TimeStamp: "1704067260.000200",
			Type:      "message",
		}).
		// Two posts in the second MPIM directory — must still appear after dedup.
		AddPost("mpdm-alice--bob--charlie-2", slack.SlackPost{
			User:      "U003",
			Text:      "Message from the second MPIM",
			TimeStamp: "1704070900.000100",
			Type:      "message",
		}).
		AddPost("mpdm-alice--bob--charlie-2", slack.SlackPost{
			User:      "U001",
			Text:      "Another message from the second MPIM",
			TimeStamp: "1704070960.000200",
			Type:      "message",
		})
}

// ExportWithOverlappingMpims creates an export with two MPIMs that share two
// of three members but differ in the third. They must NOT be deduplicated:
// Mattermost keys group channels by full member set, so {alice,bob,charlie}
// and {alice,bob,dave} are distinct channels and both must survive.
func ExportWithOverlappingMpims() *SlackExportBuilder {
	return NewSlackExportBuilder().
		AddUser(slack.SlackUser{
			Id:       "U001",
			Username: "alice",
			Profile:  slack.SlackProfile{RealName: "Alice Anderson", Email: "alice@example.com"},
		}).
		AddUser(slack.SlackUser{
			Id:       "U002",
			Username: "bob",
			Profile:  slack.SlackProfile{RealName: "Bob Baker", Email: "bob@example.com"},
		}).
		AddUser(slack.SlackUser{
			Id:       "U003",
			Username: "charlie",
			Profile:  slack.SlackProfile{RealName: "Charlie Carter", Email: "charlie@example.com"},
		}).
		AddUser(slack.SlackUser{
			Id:       "U004",
			Username: "dave",
			Profile:  slack.SlackProfile{RealName: "Dave Dawson", Email: "dave@example.com"},
		}).
		AddGroupChannel(slack.SlackChannel{
			Id:      "G001",
			Name:    "mpdm-alice--bob--charlie",
			Creator: "U001",
			Created: 1704067200,
			Members: []string{"U001", "U002", "U003"},
		}).
		AddGroupChannel(slack.SlackChannel{
			Id:      "G002",
			Name:    "mpdm-alice--bob--dave",
			Creator: "U001",
			Created: 1704070800,
			Members: []string{"U001", "U002", "U004"},
		}).
		AddPost("mpdm-alice--bob--charlie", slack.SlackPost{
			User:      "U001",
			Text:      "Hi charlie group",
			TimeStamp: "1704067200.000100",
			Type:      "message",
		}).
		AddPost("mpdm-alice--bob--dave", slack.SlackPost{
			User:      "U002",
			Text:      "Hi dave group",
			TimeStamp: "1704070900.000100",
			Type:      "message",
		})
}

// ExportWithChannellessGuestMpim builds an export where a guest (is_restricted)
// belongs ONLY to a group DM (mpim) alongside three regular users, and to NO
// public/private channel. In default "guest" mode, dropChannellessGuests removes
// the guest (group/DM membership does not count as guest-scopable channel
// access); the MPIM survives with the three regulars (staying a group channel
// rather than collapsing to a DM). The guest starts a thread in the MPIM and a
// regular user replies — both the guest's root and the non-guest reply are
// dropped (the whole thread is skipped), while a regular user's standalone post
// in the same MPIM survives.
func ExportWithChannellessGuestMpim() *SlackExportBuilder {
	return NewSlackExportBuilder().
		AddUser(slack.SlackUser{
			Id:       "U001",
			Username: "regular1",
			Profile:  slack.SlackProfile{RealName: "Regular One", Email: "regular1@example.com"},
		}).
		AddUser(slack.SlackUser{
			Id:       "U002",
			Username: "regular2",
			Profile:  slack.SlackProfile{RealName: "Regular Two", Email: "regular2@example.com"},
		}).
		AddUser(slack.SlackUser{
			Id:       "U003",
			Username: "regular3",
			Profile:  slack.SlackProfile{RealName: "Regular Three", Email: "regular3@example.com"},
		}).
		AddUser(slack.SlackUser{
			Id:           "U004",
			Username:     "channelless.guest",
			IsRestricted: true,
			Profile:      slack.SlackProfile{RealName: "Channelless Guest", Email: "channelless.guest@example.com"},
		}).
		AddGroupChannel(slack.SlackChannel{
			Id:      "G001",
			Name:    "mpdm-regular1--regular2--regular3--channelless.guest-1",
			Creator: "U001",
			Created: 1704067200,
			Members: []string{"U001", "U002", "U003", "U004"},
		}).
		AddPost("mpdm-regular1--regular2--regular3--channelless.guest-1", slack.SlackPost{
			User:      "U001",
			Text:      "Regular standalone in mpim",
			TimeStamp: "1704067200.000100",
			Type:      "message",
		}).
		AddPost("mpdm-regular1--regular2--regular3--channelless.guest-1", slack.SlackPost{
			User:      "U004",
			Text:      "Guest thread root in mpim",
			TimeStamp: "1704067260.000200",
			ThreadTS:  "1704067260.000200",
			Type:      "message",
		}).
		AddPost("mpdm-regular1--regular2--regular3--channelless.guest-1", slack.SlackPost{
			User:      "U002",
			Text:      "Regular reply in guest mpim thread",
			TimeStamp: "1704067320.000300",
			ThreadTS:  "1704067260.000200",
			Type:      "message",
		})
}
