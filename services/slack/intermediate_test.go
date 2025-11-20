package slack

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"

	log "github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mattermost/mattermost/server/public/model"
)

func TestIntermediateChannelSanitise(t *testing.T) {
	t.Run("Properties should respect the max length", func(t *testing.T) {
		channel := IntermediateChannel{
			Name:        strings.Repeat("a", 70),
			DisplayName: strings.Repeat("b", 70),
			Purpose:     strings.Repeat("c", 400),
			Header:      strings.Repeat("d", 1100),
		}

		expectedName := strings.Repeat("a", 64)
		expectedDisplayName := strings.Repeat("b", 64)
		expectedPurpose := strings.Repeat("c", 250)
		expectedHeader := strings.Repeat("d", 1024)

		channel.Sanitise(log.New())

		assert.Equal(t, expectedName, channel.Name)
		assert.Equal(t, expectedDisplayName, channel.DisplayName)
		assert.Equal(t, expectedPurpose, channel.Purpose)
		assert.Equal(t, expectedHeader, channel.Header)
	})

	t.Run("Name and DisplayName should be trimmed", func(t *testing.T) {
		channel := IntermediateChannel{
			Name:        "_-_channel--name-_-__",
			DisplayName: "-display_name--",
		}

		channel.Sanitise(log.New())

		assert.Equal(t, "channel--name", channel.Name)
		assert.Equal(t, "display_name", channel.DisplayName)
	})

	t.Run("Name and DisplayName should be longer than 1 character", func(t *testing.T) {
		channel := IntermediateChannel{
			Name:        "a",
			DisplayName: "-_---_--b----",
		}

		channel.Sanitise(log.New())

		assert.Equal(t, "slack-channel-a", channel.Name)
		assert.Equal(t, "slack-channel-b", channel.DisplayName)
	})

	t.Run("Name and DisplayName should contain valid characters or return id", func(t *testing.T) {
		channel := IntermediateChannel{
			Id:          "channelId1",
			Name:        "_-_chännel--name-_-__",
			DisplayName: "-døsplay_name--",
		}

		channel.Sanitise(log.New())

		assert.Equal(t, "channelid1", channel.Name)
		assert.Equal(t, "channelid1", channel.DisplayName)
	})
}

func TestTransformPublicChannels(t *testing.T) {
	slackTransformer := NewTransformer("test", log.New())
	slackTransformer.Intermediate.UsersById = map[string]*IntermediateUser{"m1": {}, "m2": {}, "m3": {}}

	publicChannels := []SlackChannel{
		{
			Id:      "id1",
			Name:    "channel-name-1",
			Creator: "creator1",
			Members: []string{"m1", "m2", "m3"},
			Purpose: SlackChannelSub{
				Value: "purpose1",
			},
			Topic: SlackChannelSub{
				Value: "topic1",
			},
			Type: model.ChannelTypeOpen,
		},
		{
			Id:      "id2",
			Name:    "channel-name-2",
			Creator: "creator2",
			Members: []string{"m1", "m2", "m3"},
			Purpose: SlackChannelSub{
				Value: "purpose2",
			},
			Topic: SlackChannelSub{
				Value: "topic2",
			},
			Type: model.ChannelTypeOpen,
		},
		{
			Id:      "id3",
			Name:    "channel-name-3",
			Creator: "creator3",
			Members: []string{"m1", "m2", "m3"},
			Purpose: SlackChannelSub{
				Value: "purpose3",
			},
			Topic: SlackChannelSub{
				Value: "topic3",
			},
			Type: model.ChannelTypeOpen,
		},
	}

	result := slackTransformer.TransformChannels(publicChannels)
	require.Len(t, result, len(publicChannels))

	for i := range result {
		assert.Equal(t, fmt.Sprintf("channel-name-%d", i+1), result[i].Name)
		assert.Equal(t, fmt.Sprintf("channel-name-%d", i+1), result[i].DisplayName)
		assert.Equal(t, []string{"m1", "m2", "m3"}, result[i].Members)
		assert.Equal(t, fmt.Sprintf("purpose%d", i+1), result[i].Purpose)
		assert.Equal(t, fmt.Sprintf("topic%d", i+1), result[i].Header)
		assert.Equal(t, model.ChannelTypeOpen, result[i].Type)
	}
}

func TestTransformPublicChannelsWithAnInvalidMember(t *testing.T) {
	slackTransformer := NewTransformer("test", log.New())
	slackTransformer.Intermediate.UsersById = map[string]*IntermediateUser{"m1": {}, "m2": {}}

	publicChannels := []SlackChannel{
		{
			Id:      "id1",
			Name:    "channel-name-1",
			Creator: "creator1",
			Members: []string{"m1", "m2", "m3"},
			Purpose: SlackChannelSub{
				Value: "purpose1",
			},
			Topic: SlackChannelSub{
				Value: "topic1",
			},
			Type: model.ChannelTypeOpen,
		},
		{
			Id:      "id2",
			Name:    "channel-name-2",
			Creator: "creator2",
			Members: []string{"m1", "m2", "m3"},
			Purpose: SlackChannelSub{
				Value: "purpose2",
			},
			Topic: SlackChannelSub{
				Value: "topic2",
			},
			Type: model.ChannelTypeOpen,
		},
		{
			Id:      "id3",
			Name:    "channel-name-3",
			Creator: "creator3",
			Members: []string{"m1", "m2", "m3"},
			Purpose: SlackChannelSub{
				Value: "purpose3",
			},
			Topic: SlackChannelSub{
				Value: "topic3",
			},
			Type: model.ChannelTypeOpen,
		},
	}

	result := slackTransformer.TransformChannels(publicChannels)
	require.Len(t, result, len(publicChannels))

	for i := range result {
		assert.Equal(t, fmt.Sprintf("channel-name-%d", i+1), result[i].Name)
		assert.Equal(t, fmt.Sprintf("channel-name-%d", i+1), result[i].DisplayName)
		assert.Equal(t, []string{"m1", "m2", "m3"}, result[i].Members)
		assert.Equal(t, fmt.Sprintf("purpose%d", i+1), result[i].Purpose)
		assert.Equal(t, fmt.Sprintf("topic%d", i+1), result[i].Header)
		assert.Equal(t, model.ChannelTypeOpen, result[i].Type)
	}
}

func TestTransformPrivateChannels(t *testing.T) {
	slackTransformer := NewTransformer("test", log.New())
	slackTransformer.Intermediate.UsersById = map[string]*IntermediateUser{"m1": {}, "m2": {}, "m3": {}}

	privateChannels := []SlackChannel{
		{
			Id:      "id1",
			Name:    "channel-name-1",
			Creator: "creator1",
			Members: []string{"m1", "m2", "m3"},
			Purpose: SlackChannelSub{
				Value: "purpose1",
			},
			Topic: SlackChannelSub{
				Value: "topic1",
			},
			Type: model.ChannelTypePrivate,
		},
		{
			Id:      "id2",
			Name:    "channel-name-2",
			Creator: "creator2",
			Members: []string{"m1", "m2", "m3"},
			Purpose: SlackChannelSub{
				Value: "purpose2",
			},
			Topic: SlackChannelSub{
				Value: "topic2",
			},
			Type: model.ChannelTypePrivate,
		},
		{
			Id:      "id3",
			Name:    "channel-name-3",
			Creator: "creator3",
			Members: []string{"m1", "m2", "m3"},
			Purpose: SlackChannelSub{
				Value: "purpose3",
			},
			Topic: SlackChannelSub{
				Value: "topic3",
			},
			Type: model.ChannelTypePrivate,
		},
	}

	result := slackTransformer.TransformChannels(privateChannels)
	require.Len(t, result, len(privateChannels))

	for i := range result {
		assert.Equal(t, fmt.Sprintf("channel-name-%d", i+1), result[i].Name)
		assert.Equal(t, fmt.Sprintf("channel-name-%d", i+1), result[i].DisplayName)
		assert.Equal(t, []string{"m1", "m2", "m3"}, result[i].Members)
		assert.Equal(t, fmt.Sprintf("purpose%d", i+1), result[i].Purpose)
		assert.Equal(t, fmt.Sprintf("topic%d", i+1), result[i].Header)
		assert.Equal(t, model.ChannelTypePrivate, result[i].Type)
	}
}

func TestTransformBigGroupChannels(t *testing.T) {
	slackTransformer := NewTransformer("test", log.New())
	slackTransformer.Intermediate.UsersById = map[string]*IntermediateUser{"m1": {}, "m2": {}, "m3": {}, "m4": {}, "m5": {}, "m6": {}, "m7": {}, "m8": {}, "m9": {}}
	channelMembers := []string{"m1", "m2", "m3", "m4", "m5", "m6", "m7", "m8", "m9"}

	bigGroupChannels := []SlackChannel{
		{
			Id:      "id1",
			Creator: "creator1",
			Members: channelMembers,
			Purpose: SlackChannelSub{
				Value: "purpose1",
			},
			Topic: SlackChannelSub{
				Value: "topic1",
			},
			Type: model.ChannelTypeGroup,
		},
		{
			Id:      "id2",
			Name:    "invalid",
			Creator: "creator2",
			Members: channelMembers,
			Purpose: SlackChannelSub{
				Value: "purpose2",
			},
			Topic: SlackChannelSub{
				Value: "topic2",
			},
			Type: model.ChannelTypeGroup,
		},
		{
			Id:      "id3",
			Creator: "creator3",
			Members: channelMembers,
			Purpose: SlackChannelSub{
				Value: "purpose3",
			},
			Topic: SlackChannelSub{
				Value: "topic3",
			},
			Type: model.ChannelTypeGroup,
		},
		{
			Id:      "id4",
			Creator: "creator4",
			Members: []string{"m1", "m2", "m3", "m4", "m5", "m6", "m7", "m8", "m10"},
			Purpose: SlackChannelSub{
				Value: "purpose4",
			},
			Topic: SlackChannelSub{
				Value: "topic4",
			},
			Type: model.ChannelTypeGroup,
		},
	}

	result := slackTransformer.TransformChannels(bigGroupChannels)
	require.Len(t, result, len(bigGroupChannels))

	for i := range result {
		assert.Equal(t, fmt.Sprintf("purpose%d", i+1), result[i].Name)
		assert.Equal(t, fmt.Sprintf("purpose%d", i+1), result[i].DisplayName)
		// First 3 channels have 9 valid members, last channel has 8 valid + 1 created deleted user (m10)
		expectedMemberCount := 9
		assert.Equal(t, expectedMemberCount, len(result[i].Members))
		assert.Equal(t, fmt.Sprintf("purpose%d", i+1), result[i].Purpose)
		assert.Equal(t, fmt.Sprintf("topic%d", i+1), result[i].Header)
		assert.Equal(t, model.ChannelTypePrivate, result[i].Type)
	}
}

func TestTransformRegularGroupChannels(t *testing.T) {
	slackTransformer := NewTransformer("test", log.New())
	slackTransformer.Intermediate.UsersById = map[string]*IntermediateUser{"m1": {}, "m2": {}, "m3": {}}

	regularGroupChannels := []SlackChannel{
		{
			Id:      "id1",
			Name:    "channel-name-1",
			Members: []string{"m1", "m2", "m3"},
			Purpose: SlackChannelSub{
				Value: "purpose1",
			},
			Topic: SlackChannelSub{
				Value: "topic1",
			},
			Type: model.ChannelTypeGroup,
		},
		{
			Id:      "id2",
			Name:    "channel-name-2",
			Creator: "creator2",
			Members: []string{"m1", "m2", "m3"},
			Purpose: SlackChannelSub{
				Value: "purpose2",
			},
			Topic: SlackChannelSub{
				Value: "topic2",
			},
			Type: model.ChannelTypeGroup,
		},
		{
			Id:      "id3",
			Name:    "channel-name-3",
			Creator: "creator3",
			Members: []string{"m1", "m2", "m3"},
			Purpose: SlackChannelSub{
				Value: "purpose3",
			},
			Topic: SlackChannelSub{
				Value: "topic3",
			},
			Type: model.ChannelTypeGroup,
		},
	}

	result := slackTransformer.TransformChannels(regularGroupChannels)
	require.Len(t, result, len(regularGroupChannels))

	for i := range result {
		assert.Equal(t, fmt.Sprintf("channel-name-%d", i+1), result[i].Name)
		assert.Equal(t, fmt.Sprintf("channel-name-%d", i+1), result[i].DisplayName)
		assert.Equal(t, []string{"m1", "m2", "m3"}, result[i].Members)
		assert.Equal(t, fmt.Sprintf("purpose%d", i+1), result[i].Purpose)
		assert.Equal(t, fmt.Sprintf("topic%d", i+1), result[i].Header)
		assert.Equal(t, model.ChannelTypeGroup, result[i].Type)
	}
}

func TestTransformDirectChannels(t *testing.T) {
	slackTransformer := NewTransformer("test", log.New())
	slackTransformer.Intermediate.UsersById = map[string]*IntermediateUser{"m1": {}, "m2": {}, "m3": {}}

	directChannels := []SlackChannel{
		{
			Id:      "id1",
			Creator: "creator1",
			Members: []string{"m1", "m2", "m3"},
			Type:    model.ChannelTypeDirect,
		},
		{
			Id:      "id2",
			Creator: "creator2",
			Members: []string{"m1", "m2", "m3"},
			Type:    model.ChannelTypeDirect,
		},
		{
			Id:      "id2",
			Creator: "creator2",
			Members: []string{"m1", "m2", "m3"},
			Type:    model.ChannelTypeDirect,
		},
	}

	result := slackTransformer.TransformChannels(directChannels)
	require.Len(t, result, len(directChannels))

	for i := range result {
		assert.Equal(t, []string{"m1", "m2", "m3"}, result[i].Members)
		assert.Equal(t, model.ChannelTypeDirect, result[i].Type)
	}
}

func TestTransformChannelWithOneValidMember(t *testing.T) {
	slackTransformer := NewTransformer("test", log.New())
	slackTransformer.Intermediate.UsersById = map[string]*IntermediateUser{"m1": {}}

	t.Run("A direct channel with only one valid member should not be transformed", func(t *testing.T) {
		directChannels := []SlackChannel{
			{
				Id:      "id1",
				Creator: "creator1",
				Members: []string{"m1", "m2", "m3"},
				Type:    model.ChannelTypeDirect,
			},
		}

		result := slackTransformer.TransformChannels(directChannels)
		// With the new behavior, missing members (m2, m3) are created as deleted users,
		// so the channel now has 3 members and will be transformed
		require.Len(t, result, 1)
	})

	t.Run("A group channel with only one valid member should not be transformed", func(t *testing.T) {
		groupChannels := []SlackChannel{
			{
				Id:      "id1",
				Name:    "channel-name-1",
				Members: []string{"m1", "m2", "m3"},
				Purpose: SlackChannelSub{
					Value: "purpose1",
				},
				Topic: SlackChannelSub{
					Value: "topic1",
				},
				Type: model.ChannelTypeGroup,
			},
		}

		result := slackTransformer.TransformChannels(groupChannels)
		// With the new behavior, missing members (m2, m3) are created as deleted users,
		// so the channel now has 3 members and will be transformed
		require.Len(t, result, 1)
	})
}

func assertUserFieldsWithinLimits(t *testing.T, user *IntermediateUser) {
	t.Helper()
	assert.LessOrEqual(t, len([]rune(user.FirstName)), model.UserFirstNameMaxRunes, "FirstName should not exceed max runes")
	assert.LessOrEqual(t, len([]rune(user.LastName)), model.UserLastNameMaxRunes, "LastName should not exceed max runes")
	assert.LessOrEqual(t, len([]rune(user.Position)), model.UserPositionMaxRunes, "Position should not exceed max runes")
}

func TestIntermediateUserSanitise(t *testing.T) {
	t.Run("If there is no email, and --default-email-domain and --skip-empty-emails flags are not provided, we should exit the program.", func(t *testing.T) {
		user := &IntermediateUser{
			Username: "test-username",
			Email:    "",
		}

		exitCode := -1
		exitFunc = func(code int) {
			exitCode = code
		}
		defer func() {
			exitFunc = os.Exit
		}()

		user.Sanitise(log.New(), "", false)

		require.Equal(t, 1, exitCode)
	})

	t.Run("If there is no email, and --default-email-domain flag is provided, use domain to create an email address.", func(t *testing.T) {
		user := &IntermediateUser{
			Username: "test-username",
			Email:    "",
		}

		exitCode := -1
		exitFunc = func(code int) {
			exitCode = code
		}
		defer func() {
			exitFunc = os.Exit
		}()

		logger := log.New()
		logOutput := logger.Out
		buf := &bytes.Buffer{}
		log.SetOutput(buf)
		defer func() {
			log.SetOutput(logOutput)
		}()

		defaultEmailDomain := "testdomain.com"
		skipEmptyEmails := false
		user.Sanitise(logger, defaultEmailDomain, skipEmptyEmails)

		expectedEmail := "test-username@testdomain.com"
		require.Equal(t, expectedEmail, user.Email)
		require.Equal(t, -1, exitCode)
	})

	t.Run("If there is no email, and --skip-empty-emails flag is provided, set email to blank.", func(t *testing.T) {
		user := &IntermediateUser{
			Username: "test-username",
			Email:    "",
		}

		exitCode := -1
		exitFunc = func(code int) {
			exitCode = code
		}
		defer func() {
			exitFunc = os.Exit
		}()

		user.Sanitise(log.New(), "", true)

		require.Equal(t, "", user.Email)
		require.Equal(t, -1, exitCode)
	})

	t.Run("If there is an email, program should continue with no error logged.", func(t *testing.T) {
		user := &IntermediateUser{
			Username: "test-username",
			Email:    "test-email@otherdomain.com",
		}

		exitCode := -1
		exitFunc = func(code int) {
			exitCode = code
		}
		defer func() {
			exitFunc = os.Exit
		}()

		user.Sanitise(log.New(), "", false)

		expectedEmail := "test-email@otherdomain.com"
		require.Equal(t, expectedEmail, user.Email)
		require.Equal(t, -1, exitCode)
	})

	t.Run("Properties should respect the max length", func(t *testing.T) {
		user := &IntermediateUser{
			Username:  "test-username",
			Email:     "test-email@otherdomain.com",
			FirstName: strings.Repeat("a", model.UserFirstNameMaxRunes+4),
			LastName:  strings.Repeat("b", model.UserLastNameMaxRunes+4),
			Position:  strings.Repeat("c", model.UserPositionMaxRunes+4),
		}

		expectedFirstName := strings.Repeat("a", model.UserFirstNameMaxRunes)
		expectedLastName := strings.Repeat("b", model.UserLastNameMaxRunes)
		expectedPosition := strings.Repeat("c", model.UserPositionMaxRunes)

		user.Sanitise(log.New(), "", false)

		// Verify fields are not greater than max allowed runes
		assertUserFieldsWithinLimits(t, user)

		// Verify exact truncated values
		assert.Equal(t, expectedFirstName, user.FirstName)
		assert.Equal(t, expectedLastName, user.LastName)
		assert.Equal(t, expectedPosition, user.Position)
	})

	t.Run("Properties should not be truncated if under max length", func(t *testing.T) {
		user := &IntermediateUser{
			Username:  "test-username",
			Email:     "test-email@otherdomain.com",
			FirstName: "John",
			LastName:  "Doe",
			Position:  "Software Engineer",
		}

		expectedFirstName := "John"
		expectedLastName := "Doe"
		expectedPosition := "Software Engineer"

		user.Sanitise(log.New(), "", false)

		// Verify fields are not greater than max allowed runes
		assertUserFieldsWithinLimits(t, user)

		assert.Equal(t, expectedFirstName, user.FirstName)
		assert.Equal(t, expectedLastName, user.LastName)
		assert.Equal(t, expectedPosition, user.Position)
	})

	t.Run("Properties should not be truncated if exactly at max length", func(t *testing.T) {
		user := &IntermediateUser{
			Username:  "test-username",
			Email:     "test-email@otherdomain.com",
			FirstName: strings.Repeat("a", model.UserFirstNameMaxRunes),
			LastName:  strings.Repeat("b", model.UserLastNameMaxRunes),
			Position:  strings.Repeat("c", model.UserPositionMaxRunes),
		}

		expectedFirstName := strings.Repeat("a", model.UserFirstNameMaxRunes)
		expectedLastName := strings.Repeat("b", model.UserLastNameMaxRunes)
		expectedPosition := strings.Repeat("c", model.UserPositionMaxRunes)

		user.Sanitise(log.New(), "", false)

		// Verify fields are not greater than max allowed runes
		assertUserFieldsWithinLimits(t, user)

		assert.Equal(t, expectedFirstName, user.FirstName)
		assert.Equal(t, expectedLastName, user.LastName)
		assert.Equal(t, expectedPosition, user.Position)
	})

	t.Run("Properties with multi-byte characters should be truncated correctly", func(t *testing.T) {
		// Using emoji and other multi-byte characters to ensure rune counting works correctly
		// Each emoji is 1 rune but multiple bytes
		user := &IntermediateUser{
			Username:  "test-username",
			Email:     "test-email@otherdomain.com",
			FirstName: strings.Repeat("😀", model.UserFirstNameMaxRunes+4),
			LastName:  strings.Repeat("日", model.UserLastNameMaxRunes+5),
			Position:  strings.Repeat("🎯", model.UserPositionMaxRunes+3),
		}

		user.Sanitise(log.New(), "", false)

		// Verify fields are not greater than max allowed runes
		assertUserFieldsWithinLimits(t, user)

		// Verify truncation happened by checking exact rune count (in this case they should be exactly at max)
		assert.Equal(t, model.UserFirstNameMaxRunes, len([]rune(user.FirstName)))
		assert.Equal(t, model.UserLastNameMaxRunes, len([]rune(user.LastName)))
		assert.Equal(t, model.UserPositionMaxRunes, len([]rune(user.Position)))

		// Verify content is preserved up to max runes
		expectedFirstName := strings.Repeat("😀", model.UserFirstNameMaxRunes)
		expectedLastName := strings.Repeat("日", model.UserLastNameMaxRunes)
		expectedPosition := strings.Repeat("🎯", model.UserPositionMaxRunes)

		assert.Equal(t, expectedFirstName, user.FirstName)
		assert.Equal(t, expectedLastName, user.LastName)
		assert.Equal(t, expectedPosition, user.Position)
	})
}

func TestTransformUsers(t *testing.T) {
	id1 := "id1"
	id2 := "id2"
	id3 := "id3"

	slackTransformer := NewTransformer("test", log.New())
	users := []SlackUser{
		{
			Id:       id1,
			Username: "username1",
			Profile: SlackProfile{
				RealName: "firstname1 lastname1",
				Title:    "position1",
				Email:    "email1@example.com",
			},
		},
		{
			Id:       id2,
			Username: "username2",
			Profile: SlackProfile{
				RealName: "firstname2 lastname2",
				Title:    "position2",
				Email:    "email2@example.com",
			},
		},
		{
			Id:       id3,
			Username: "username3",
			Profile: SlackProfile{
				RealName: "firstname3 lastname3",
				Title:    "position3",
				Email:    "email3@example.com",
			},
		},
	}

	defaultEmailDomain := ""
	skipEmptyEmails := false
	slackTransformer.TransformUsers(users, skipEmptyEmails, defaultEmailDomain)
	require.Len(t, slackTransformer.Intermediate.UsersById, len(users))

	for i, id := range []string{id1, id2, id3} {
		assert.Equal(t, fmt.Sprintf("id%d", i+1), slackTransformer.Intermediate.UsersById[id].Id)
		assert.Equal(t, fmt.Sprintf("username%d", i+1), slackTransformer.Intermediate.UsersById[id].Username)
		assert.Equal(t, fmt.Sprintf("firstname%d", i+1), slackTransformer.Intermediate.UsersById[id].FirstName)
		assert.Equal(t, fmt.Sprintf("lastname%d", i+1), slackTransformer.Intermediate.UsersById[id].LastName)
		assert.Equal(t, fmt.Sprintf("position%d", i+1), slackTransformer.Intermediate.UsersById[id].Position)
		assert.Equal(t, fmt.Sprintf("email%d@example.com", i+1), slackTransformer.Intermediate.UsersById[id].Email)
		assert.Zero(t, slackTransformer.Intermediate.UsersById[id].DeleteAt)
	}
}

func TestDeleteAt(t *testing.T) {
	id1 := "id1"
	id2 := "id2"
	id3 := "id3"
	id4 := "id4"

	slackTransformer := NewTransformer("test", log.New())
	activeUsers := []SlackUser{
		{
			Id:       id1,
			Username: "username1",
			Profile: SlackProfile{
				RealName: "firstname1 lastname1",
				Title:    "position1",
				Email:    "email1@example.com",
			},
		},
		{
			Id:       id2,
			Username: "username2",
			Deleted:  false,
			Profile: SlackProfile{
				RealName: "firstname2 lastname2",
				Title:    "position2",
				Email:    "email2@example.com",
			},
		},
	}

	inactiveUsers := []SlackUser{
		{
			Id:       id3,
			Username: "username3",
			Deleted:  true,
			Profile: SlackProfile{
				RealName: "firstname3 lastname3",
				Title:    "position3",
				Email:    "email3@example.com",
			},
		},
		{
			Id:       id4,
			Username: "username4",
			Deleted:  true,
			Profile: SlackProfile{
				RealName: "firstname4 lastname4",
				Title:    "position4",
				Email:    "email4@example.com",
			},
		},
	}

	users := append(activeUsers, inactiveUsers...)

	defaultEmailDomain := ""
	skipEmptyEmails := false
	slackTransformer.TransformUsers(users, skipEmptyEmails, defaultEmailDomain)
	require.Zero(t, slackTransformer.Intermediate.UsersById[activeUsers[0].Id].DeleteAt)
	require.Zero(t, slackTransformer.Intermediate.UsersById[activeUsers[1].Id].DeleteAt)
	require.NotZero(t, slackTransformer.Intermediate.UsersById[inactiveUsers[0].Id].DeleteAt)
	require.NotZero(t, slackTransformer.Intermediate.UsersById[inactiveUsers[1].Id].DeleteAt)
}

func TestPopulateUserMemberships(t *testing.T) {
	slackTransformer := NewTransformer("test", log.New())

	slackTransformer.Intermediate = &Intermediate{
		UsersById: map[string]*IntermediateUser{"id1": {}, "id2": {}, "id3": {}},
		PublicChannels: []*IntermediateChannel{
			{
				Name:    "c1",
				Members: []string{"id1", "id3"},
			},
			{
				Name:    "c2",
				Members: []string{"id1", "id2"},
			},
		},
		PrivateChannels: []*IntermediateChannel{
			{
				Name:    "c3",
				Members: []string{"id3"},
			},
		},
	}

	slackTransformer.PopulateUserMemberships()

	assert.Equal(t, []string{"c1", "c2"}, slackTransformer.Intermediate.UsersById["id1"].Memberships)
	assert.Equal(t, []string{"c2"}, slackTransformer.Intermediate.UsersById["id2"].Memberships)
	assert.Equal(t, []string{"c1", "c3"}, slackTransformer.Intermediate.UsersById["id3"].Memberships)
}

func TestPopulateChannelMemberships(t *testing.T) {
	slackTransformer := NewTransformer("test", log.New())

	c1 := IntermediateChannel{
		Name:    "c1",
		Members: []string{"id1", "id3"},
	}
	c2 := IntermediateChannel{
		Name:    "c2",
		Members: []string{"id1", "id2"},
	}
	c3 := IntermediateChannel{
		Name:    "c3",
		Members: []string{"id3"},
	}

	slackTransformer.Intermediate = &Intermediate{
		UsersById: map[string]*IntermediateUser{
			"id1": {Username: "u1"},
			"id2": {Username: "u2"},
			"id3": {Username: "u3"},
		},
		GroupChannels:  []*IntermediateChannel{&c1, &c2},
		DirectChannels: []*IntermediateChannel{&c3},
	}

	slackTransformer.PopulateChannelMemberships()

	assert.Equal(t, []string{"u1", "u3"}, c1.MembersUsernames)
	assert.Equal(t, []string{"u1", "u2"}, c2.MembersUsernames)
	assert.Equal(t, []string{"u3"}, c3.MembersUsernames)
}

func TestAddPostToThreads(t *testing.T) {
	t.Run("Avoid duplicated timestamps", func(t *testing.T) {
		testCases := []struct {
			Name               string
			Post               *IntermediatePost
			Timestamps         map[int64]bool
			ExpectedTimestamp  int64
			ExpectedTimestamps map[int64]bool
		}{
			{
				Name:               "Adding a post with no collisions",
				Post:               &IntermediatePost{CreateAt: 1549307811071},
				Timestamps:         map[int64]bool{},
				ExpectedTimestamp:  1549307811071,
				ExpectedTimestamps: map[int64]bool{1549307811071: true},
			},
			{
				Name:               "Adding a post with an existing timestamp",
				Post:               &IntermediatePost{CreateAt: 1549307811071},
				Timestamps:         map[int64]bool{1549307811071: true},
				ExpectedTimestamp:  1549307811072,
				ExpectedTimestamps: map[int64]bool{1549307811071: true, 1549307811072: true},
			},
			{
				Name:               "Adding a post with several sequential existing timestamps",
				Post:               &IntermediatePost{CreateAt: 1549307811071},
				Timestamps:         map[int64]bool{1549307811071: true, 1549307811072: true},
				ExpectedTimestamp:  1549307811073,
				ExpectedTimestamps: map[int64]bool{1549307811071: true, 1549307811072: true, 1549307811073: true},
			},
		}

		for _, tc := range testCases {
			t.Run(tc.Name, func(t *testing.T) {
				original := SlackPost{TimeStamp: "thread-ts"}
				channel := &IntermediateChannel{Type: model.ChannelTypeOpen}
				threads := map[string]*IntermediatePost{}

				AddPostToThreads(original, tc.Post, threads, channel, tc.Timestamps)
				newPost := threads["thread-ts"]
				require.NotNil(t, newPost)
				require.Equal(t, tc.Post, newPost)
				require.Equal(t, tc.ExpectedTimestamp, newPost.CreateAt)
				require.EqualValues(t, tc.ExpectedTimestamps, tc.Timestamps)
			})
		}
	})
}

func TestTransformPosts(t *testing.T) {
	t.Run("huddle threads are converted to posts", func(t *testing.T) {
		slackTransformer := NewTransformer("test", log.New())
		slackTransformer.Intermediate.UsersById = map[string]*IntermediateUser{"m1": {Username: "m1"}, "m2": {Username: "m2"}}
		slackTransformer.Intermediate.PublicChannels = []*IntermediateChannel{
			{
				Name:         "channel1",
				OriginalName: "channel1",
			},
		}

		slackExport := &SlackExport{
			Posts: map[string][]SlackPost{
				"channel1": {
					{
						User: "USLACKBOT",
						Text: "",
						Room: &SlackRoom{
							CreatedBy: "m1",
							DateStart: 1695219818,
							DateEnd:   1695220775,
						},
						TimeStamp: "1695219818.000100",
						SubType:   "huddle_thread",
						Type:      "message",
					},
					{
						User:      "m2",
						Text:      "reply text",
						ThreadTS:  "1695219818.000100",
						TimeStamp: "1695219818.000101",
						Type:      "message",
					},
				},
			},
		}

		err := slackTransformer.TransformPosts(slackExport, "", false, false, false)
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if len(slackTransformer.Intermediate.Posts) != 1 {
			t.Errorf("expected 1 post, got %d", len(slackTransformer.Intermediate.Posts))
		}

		post := slackTransformer.Intermediate.Posts[0]
		if post.User != "m1" {
			t.Errorf("expected user to be m1, got %s", post.User)
		}

		if post.Message != "Call ended" {
			t.Errorf("expected message to be 'Call ended', got %s", post.Message)
		}

		if post.Props["attachments"] == nil {
			t.Errorf("expected attachments to be set")
		}

		if len(post.Replies) != 1 {
			t.Errorf("expected 1 post reply, got %d", len(post.Replies))
		}
	})

	t.Run("reactions are imported", func(t *testing.T) {
		slackTransformer := NewTransformer("test", log.New())
		slackTransformer.Intermediate.UsersById = map[string]*IntermediateUser{"m1": {Username: "m1"}, "m2": {Username: "m2"}}
		slackTransformer.Intermediate.PublicChannels = []*IntermediateChannel{
			{
				Name:         "channel1",
				OriginalName: "channel1",
			},
		}

		reactions1 := []*SlackReaction{{
			Name:  "+1",
			Count: 2,
			Users: []string{"m1", "m2"},
		}}
		reactions2 := []*SlackReaction{{
			Name:  "+1::skin-tone-3",
			Count: 1,
			Users: []string{"m1"},
		}}
		slackExport := &SlackExport{
			Posts: map[string][]SlackPost{
				"channel1": {
					{
						User:      "m1",
						Text:      "hi everyone let's talk about this",
						TimeStamp: "1695219818.000100",
						Type:      "message",
						Reactions: reactions1,
					},
					{
						User:      "m2",
						Text:      "reply text",
						ThreadTS:  "1695219818.000100",
						TimeStamp: "1695219818.000101",
						Type:      "message",
						Reactions: reactions2,
					},
				},
			},
		}

		err := slackTransformer.TransformPosts(slackExport, "", false, false, false)
		require.NoError(t, err)
		require.Equal(t, 1, len(slackTransformer.Intermediate.Posts))

		post := slackTransformer.Intermediate.Posts[0]
		require.Equal(t, "m1", post.User)
		require.Equal(t, "hi everyone let's talk about this", post.Message)
		require.Equal(t, 2, len(post.Reactions))
		require.Equal(t, "+1", post.Reactions[0].EmojiName)
		require.Equal(t, "+1", post.Reactions[1].EmojiName)
		require.Equal(t, "m1", post.Reactions[0].User)
		require.Equal(t, int64(1695219818001), post.Reactions[0].CreateAt)
		require.Equal(t, 1, len(post.Replies))
		require.Equal(t, 1, len(post.Replies[0].Reactions))
	})

	t.Run("long posts are split into thread replies", func(t *testing.T) {
		slackTransformer := NewTransformer("test", log.New())
		slackTransformer.Intermediate.UsersById = map[string]*IntermediateUser{"m1": {Username: "m1"}}
		slackTransformer.Intermediate.PublicChannels = []*IntermediateChannel{
			{
				Name:         "channel1",
				OriginalName: "channel1",
			},
		}

		// Create a post with text that exceeds the maximum rune limit
		longText := model.NewRandomString(model.PostMessageMaxRunesV2 * 2)
		slackExport := &SlackExport{
			Posts: map[string][]SlackPost{
				"channel1": {
					{
						User:      "m1",
						Text:      longText,
						TimeStamp: "1695219818.000100",
						Type:      "message",
					},
				},
			},
		}

		err := slackTransformer.TransformPosts(slackExport, "", false, false, false)
		require.NoError(t, err)
		require.Equal(t, 1, len(slackTransformer.Intermediate.Posts))

		post := slackTransformer.Intermediate.Posts[0]
		require.Equal(t, "m1", post.User)

		// Verify the first chunk is within the limit
		require.LessOrEqual(t, len([]rune(post.Message)), model.PostMessageMaxRunesV2)

		// Verify continuation chunks were created as replies
		require.Greater(t, len(post.Replies), 0, "Expected split post to have replies")

		// Verify all reply chunks are within the limit
		for _, reply := range post.Replies {
			require.LessOrEqual(t, len([]rune(reply.Message)), model.PostMessageMaxRunesV2)
		}
	})

	t.Run("very long main post gets split into root post and multiple replies", func(t *testing.T) {
		slackTransformer := NewTransformer("test", log.New())
		slackTransformer.Intermediate.UsersById = map[string]*IntermediateUser{"m1": {Username: "m1"}}
		slackTransformer.Intermediate.PublicChannels = []*IntermediateChannel{
			{
				Name:         "channel1",
				OriginalName: "channel1",
			},
		}

		// Create a post with text that is 2.5x the maximum rune limit
		longText := model.NewRandomString((model.PostMessageMaxRunesV2 * 5) / 2)
		reactions := []*SlackReaction{{
			Name:  "thumbsup",
			Count: 1,
			Users: []string{"m1"},
		}}

		slackExport := &SlackExport{
			Posts: map[string][]SlackPost{
				"channel1": {
					{
						User:      "m1",
						Text:      longText,
						TimeStamp: "1695219818.000100",
						Type:      "message",
						Reactions: reactions,
					},
				},
			},
		}

		err := slackTransformer.TransformPosts(slackExport, "", false, false, false)
		require.NoError(t, err)
		require.Equal(t, 1, len(slackTransformer.Intermediate.Posts))

		post := slackTransformer.Intermediate.Posts[0]
		require.Equal(t, "m1", post.User)
		require.Equal(t, "channel1", post.Channel)

		// Verify the main post message is within the limit
		require.LessOrEqual(t, len([]rune(post.Message)), model.PostMessageMaxRunesV2)
		require.Greater(t, len(post.Message), 0, "Main post should not be empty")

		// Verify continuation chunks were created as replies
		require.Greater(t, len(post.Replies), 1, "Expected at least 2 reply chunks for 2.5x text")

		// Verify all reply chunks are within the limit
		for i, reply := range post.Replies {
			require.LessOrEqual(t, len([]rune(reply.Message)), model.PostMessageMaxRunesV2,
				"Reply chunk %d exceeds maximum runes", i)
			require.Greater(t, len(reply.Message), 0, "Reply chunk %d should not be empty", i)

			// Verify reply has correct metadata
			require.Equal(t, "m1", reply.User, "Reply chunk %d should have same user", i)
			require.Equal(t, "channel1", reply.Channel, "Reply chunk %d should have same channel", i)

			// Verify timestamps are sequential
			require.Equal(t, post.CreateAt+int64(i+1), reply.CreateAt,
				"Reply chunk %d should have sequential timestamp", i)

			// Verify no reactions or attachments on continuation chunks
			require.Nil(t, reply.Reactions, "Reply chunk %d should not have reactions", i)
			require.Empty(t, reply.Attachments, "Reply chunk %d should not have attachments", i)
		}

		// Verify reactions are only on the root post
		require.Equal(t, 1, len(post.Reactions), "Root post should have reactions")
		require.Equal(t, "thumbsup", post.Reactions[0].EmojiName)

		// Verify all chunks together approximately equal original text length
		totalLength := len([]rune(post.Message))
		for _, reply := range post.Replies {
			totalLength += len([]rune(reply.Message))
		}
		originalLength := len([]rune(longText))
		// Allow some variance due to potential whitespace adjustments at split points
		require.InDelta(t, originalLength, totalLength, float64(originalLength)*0.01,
			"Total length of all chunks should approximately match original")
	})

	t.Run("very long reply to main post gets split into multiple replies to same root", func(t *testing.T) {
		slackTransformer := NewTransformer("test", log.New())
		slackTransformer.Intermediate.UsersById = map[string]*IntermediateUser{
			"m1": {Username: "m1"},
			"m2": {Username: "m2"},
		}
		slackTransformer.Intermediate.PublicChannels = []*IntermediateChannel{
			{
				Name:         "channel1",
				OriginalName: "channel1",
			},
		}

		// Create a thread with a normal root post and a very long reply (3x the limit)
		rootText := "This is a normal root post"
		longReplyText := model.NewRandomString(model.PostMessageMaxRunesV2 * 3)
		rootTimestamp := "1695219818.000100"

		reactions := []*SlackReaction{{
			Name:  "heart",
			Count: 1,
			Users: []string{"m2"},
		}}

		slackExport := &SlackExport{
			Posts: map[string][]SlackPost{
				"channel1": {
					// Root post
					{
						User:      "m1",
						Text:      rootText,
						TimeStamp: rootTimestamp,
						ThreadTS:  rootTimestamp,
						Type:      "message",
					},
					// Very long reply
					{
						User:      "m2",
						Text:      longReplyText,
						TimeStamp: "1695219818.000101",
						ThreadTS:  rootTimestamp,
						Type:      "message",
						Reactions: reactions,
					},
				},
			},
		}

		err := slackTransformer.TransformPosts(slackExport, "", false, false, false)
		require.NoError(t, err)
		require.Equal(t, 1, len(slackTransformer.Intermediate.Posts))

		rootPost := slackTransformer.Intermediate.Posts[0]
		require.Equal(t, "m1", rootPost.User)
		require.Equal(t, rootText, rootPost.Message, "Root post should remain unchanged")

		// Verify the root has replies
		require.Greater(t, len(rootPost.Replies), 2,
			"Expected at least 3 reply chunks for 3x text (original long reply split)")

		// The first reply is the split long reply - verify it's been split
		firstReply := rootPost.Replies[0]
		require.Equal(t, "m2", firstReply.User)
		require.LessOrEqual(t, len([]rune(firstReply.Message)), model.PostMessageMaxRunesV2,
			"First reply chunk should be within limit")
		require.Greater(t, len(firstReply.Message), 0, "First reply chunk should not be empty")

		// Verify the first reply chunk has reactions (original reply's reactions)
		require.Equal(t, 1, len(firstReply.Reactions), "First reply chunk should have reactions")
		require.Equal(t, "heart", firstReply.Reactions[0].EmojiName)

		// Track how many reply chunks are from m2 (the split long reply)
		m2ReplyCount := 0
		var m2Replies []*IntermediatePost
		for _, reply := range rootPost.Replies {
			if reply.User == "m2" {
				m2ReplyCount++
				m2Replies = append(m2Replies, reply)
			}
		}

		require.Greater(t, m2ReplyCount, 2, "Expected at least 3 chunks from m2's long reply")

		// Verify all m2 reply chunks are within the limit and have correct metadata
		for i, reply := range m2Replies {
			require.LessOrEqual(t, len([]rune(reply.Message)), model.PostMessageMaxRunesV2,
				"m2 reply chunk %d exceeds maximum runes", i)
			require.Greater(t, len(reply.Message), 0, "m2 reply chunk %d should not be empty", i)
			require.Equal(t, "m2", reply.User, "Reply chunk %d should belong to m2", i)
			require.Equal(t, "channel1", reply.Channel, "Reply chunk %d should be in channel1", i)

			// Verify only the first chunk has reactions
			if i == 0 {
				require.Equal(t, 1, len(reply.Reactions), "First m2 reply chunk should have reactions")
			} else {
				require.Nil(t, reply.Reactions, "Continuation chunk %d should not have reactions", i)
			}

			// Verify no attachments on any continuation chunks (or first if it had none)
			if i > 0 {
				require.Empty(t, reply.Attachments, "Continuation chunk %d should not have attachments", i)
			}
		}

		// Verify all m2 chunks together approximately equal original reply text length
		totalM2Length := 0
		for _, reply := range m2Replies {
			totalM2Length += len([]rune(reply.Message))
		}
		originalReplyLength := len([]rune(longReplyText))
		// Allow some variance due to potential whitespace adjustments at split points
		require.InDelta(t, originalReplyLength, totalM2Length, float64(originalReplyLength)*0.01,
			"Total length of all m2 reply chunks should approximately match original long reply")

		// Verify timestamps are sequential for the split reply
		for i := 1; i < len(m2Replies); i++ {
			require.Greater(t, m2Replies[i].CreateAt, m2Replies[i-1].CreateAt,
				"Reply chunk %d should have later timestamp than previous chunk", i)
		}
	})

	t.Run("replies are properly ordered by CreateAt after splitting", func(t *testing.T) {
		slackTransformer := NewTransformer("test", log.New())
		slackTransformer.Intermediate.UsersById = map[string]*IntermediateUser{
			"m1": {Username: "m1"},
			"m2": {Username: "m2"},
			"m3": {Username: "m3"},
		}
		slackTransformer.Intermediate.PublicChannels = []*IntermediateChannel{
			{
				Name:         "channel1",
				OriginalName: "channel1",
			},
		}

		// Create a thread with:
		// - Root post at timestamp 100
		// - Reply 1 at timestamp 101 (normal)
		// - Reply 2 at timestamp 102 (long, will split into 3+ chunks: 102, 103, 104)
		// - Reply 3 at timestamp 105 (normal, should come after all Reply 2 chunks)
		rootTimestamp := "1695219818.000100"
		longReplyText := model.NewRandomString(model.PostMessageMaxRunesV2 * 3)

		slackExport := &SlackExport{
			Posts: map[string][]SlackPost{
				"channel1": {
					// Root post
					{
						User:      "m1",
						Text:      "Root post",
						TimeStamp: rootTimestamp,
						ThreadTS:  rootTimestamp,
						Type:      "message",
					},
					// Reply 1 - normal
					{
						User:      "m2",
						Text:      "First reply",
						TimeStamp: "1695219818.000101",
						ThreadTS:  rootTimestamp,
						Type:      "message",
					},
					// Reply 2 - very long (will be split)
					{
						User:      "m3",
						Text:      longReplyText,
						TimeStamp: "1695219818.000102",
						ThreadTS:  rootTimestamp,
						Type:      "message",
					},
					// Reply 3 - normal (should come after all Reply 2 chunks)
					{
						User:      "m2",
						Text:      "Last reply after the long one",
						TimeStamp: "1695219818.000105",
						ThreadTS:  rootTimestamp,
						Type:      "message",
					},
				},
			},
		}

		err := slackTransformer.TransformPosts(slackExport, "", false, false, false)
		require.NoError(t, err)
		require.Equal(t, 1, len(slackTransformer.Intermediate.Posts))

		rootPost := slackTransformer.Intermediate.Posts[0]
		require.Equal(t, "m1", rootPost.User)
		require.Equal(t, "Root post", rootPost.Message)

		// Verify we have at least 5 replies:
		// - 1 from m2 (first reply)
		// - 3+ from m3 (split long reply)
		// - 1 from m2 (last reply)
		require.GreaterOrEqual(t, len(rootPost.Replies), 5,
			"Expected at least 5 replies (1 normal + 3+ split chunks + 1 normal)")

		// Verify all replies are ordered by CreateAt
		for i := 1; i < len(rootPost.Replies); i++ {
			require.Less(t, rootPost.Replies[i-1].CreateAt, rootPost.Replies[i].CreateAt,
				"Reply %d (CreateAt=%d) should be before Reply %d (CreateAt=%d)",
				i-1, rootPost.Replies[i-1].CreateAt, i, rootPost.Replies[i].CreateAt)
		}

		// Verify the specific expected order:
		// First reply should be from m2
		require.Equal(t, "m2", rootPost.Replies[0].User, "First reply should be from m2")
		require.Equal(t, "First reply", rootPost.Replies[0].Message)
		// Note: timestamp may be adjusted by AddPostToThreads collision avoidance

		// Find where m3's chunks end and m2's last reply begins
		lastM3Index := -1
		firstM2LastReplyIndex := -1
		for i, reply := range rootPost.Replies {
			if reply.User == "m3" {
				lastM3Index = i
			}
			if reply.User == "m2" && reply.Message == "Last reply after the long one" {
				firstM2LastReplyIndex = i
				break
			}
		}

		require.NotEqual(t, -1, lastM3Index, "Should have found m3's replies")
		require.NotEqual(t, -1, firstM2LastReplyIndex, "Should have found m2's last reply")

		// m2's last reply should come after all m3 chunks
		require.Greater(t, firstM2LastReplyIndex, lastM3Index,
			"m2's last reply (timestamp 105) should come after all m3 chunks")

		// Verify m2's last reply has the correct timestamp
		lastReply := rootPost.Replies[firstM2LastReplyIndex]
		require.Equal(t, "m2", lastReply.User)
		require.Equal(t, "Last reply after the long one", lastReply.Message)

		// Verify all m3 chunks are in sequence (though not necessarily contiguous,
		// as m2's last reply may be interleaved if it has a timestamp between chunks)
		m3ChunkTimestamps := []int64{}
		for _, reply := range rootPost.Replies {
			if reply.User == "m3" {
				m3ChunkTimestamps = append(m3ChunkTimestamps, reply.CreateAt)
			}
		}

		require.GreaterOrEqual(t, len(m3ChunkTimestamps), 3, "m3 should have at least 3 chunks")

		// m3 chunk timestamps should be strictly increasing (maintaining order from the split)
		for i := 1; i < len(m3ChunkTimestamps); i++ {
			require.Greater(t, m3ChunkTimestamps[i], m3ChunkTimestamps[i-1],
				"m3 chunk timestamps should be strictly increasing")
		}

		// Verify the overall ordering is correct:
		// All replies should be ordered by their CreateAt values
		// This test ensures that sorting works correctly even with split chunks
	})
}
