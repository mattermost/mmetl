package confluence

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mattermost/mattermost/server/public/model"
)

func confluenceUserKey(key string) EntityKey {
	return EntityKey{Package: PkgConfluenceUser, Class: "ConfluenceUserImpl", IDName: "key", ID: key}
}

// sampleUsers reproduces the two user shapes the discovery sample contains: a
// directory-backed account whose Confluence username is their email, and an
// account whose username is just the account ID with no email at all. Only 35
// of the sample's 348 directory rows carry an email, so the second shape is the
// common one.
const sampleUsers = `<object class="ConfluenceUserImpl" package="com.atlassian.confluence.user">
<id name="key"><![CDATA[5d3eaa4376cb3e0d9d31cf8e]]></id>
<property name="name"><![CDATA[guillermo.vaya@mattermost.com]]></property>
<property name="lowerName"><![CDATA[guillermo.vaya@mattermost.com]]></property>
<property name="atlassianAccountId"><![CDATA[5d3eaa4376cb3e0d9d31cf8e]]></property>
</object>
<object class="InternalUser" package="com.atlassian.crowd.model.user">
<id name="id">5</id>
<property name="name"><![CDATA[guillermo.vaya@mattermost.com]]></property>
<property name="lowerName"><![CDATA[guillermo.vaya@mattermost.com]]></property>
<property name="active">true</property>
<property name="displayName"><![CDATA[Guillermo Vayá]]></property>
<property name="emailAddress"><![CDATA[guillermo.vaya@mattermost.com]]></property>
<property name="externalId"><![CDATA[5d3eaa4376cb3e0d9d31cf8e]]></property>
</object>
<object class="ConfluenceUserImpl" package="com.atlassian.confluence.user">
<id name="key"><![CDATA[712020:a924cd92-1fed-4db6-9012-05a189f295f5]]></id>
<property name="name"><![CDATA[712020:a924cd92-1fed-4db6-9012-05a189f295f5]]></property>
<property name="lowerName"><![CDATA[712020:a924cd92-1fed-4db6-9012-05a189f295f5]]></property>
<property name="atlassianAccountId"><![CDATA[712020:a924cd92-1fed-4db6-9012-05a189f295f5]]></property>
</object>
<object class="InternalUser" package="com.atlassian.crowd.model.user">
<id name="id">9</id>
<property name="name"><![CDATA[712020:a924cd92-1fed-4db6-9012-05a189f295f5]]></property>
<property name="lowerName"><![CDATA[712020:a924cd92-1fed-4db6-9012-05a189f295f5]]></property>
<property name="active">false</property>
<property name="displayName"><![CDATA[Christian Johannsen (Deactivated)]]></property>
<property name="emailAddress"><![CDATA[]]></property>
<property name="externalId"><![CDATA[712020:a924cd92-1fed-4db6-9012-05a189f295f5]]></property>
</object>
<object class="ConfluenceUserImpl" package="com.atlassian.confluence.user">
<id name="key"><![CDATA[never-referenced]]></id>
<property name="name"><![CDATA[nobody]]></property>
<property name="lowerName"><![CDATA[nobody]]></property>
<property name="atlassianAccountId"><![CDATA[never-referenced]]></property>
</object>`

const (
	testAccountWithEmail = "5d3eaa4376cb3e0d9d31cf8e"
	testAccountNoEmail   = "712020:a924cd92-1fed-4db6-9012-05a189f295f5"
	testOrganizationID   = "confluence.example.com"
)

func selectSampleUsers(t *testing.T, mapping *UserMapping) ([]*User, []Warning) {
	t.Helper()

	archive := archiveWithEntities(t, sampleUsers)

	refs := NewUserRefs()
	refs.Add(confluenceUserKey(testAccountWithEmail), confluenceUserKey(testAccountNoEmail))

	users, warnings, err := SelectUsers(archive, testOrganizationID, refs, mapping)
	require.NoError(t, err)
	return users, warnings
}

func TestSelectUsers(t *testing.T) {
	users, warnings := selectSampleUsers(t, NewUserMapping())

	require.Len(t, users, 2, "only referenced users are exported")
	require.Equal(t, []string{testAccountWithEmail, testAccountNoEmail}, userAccountIDs(users),
		"users are ordered by canonical account id")

	t.Run("directory-backed user", func(t *testing.T) {
		user := users[0]
		require.Equal(t, testAccountWithEmail, user.AccountID)
		require.Equal(t, testAccountWithEmail, user.ConfluenceUserKey)
		require.Equal(t, "guillermo.vaya@mattermost.com", user.ConfluenceUsername)
		require.Equal(t, "Guillermo Vayá", user.DisplayName)
		require.Equal(t, "guillermo.vaya@mattermost.com", user.Email)
		require.False(t, user.EmailIsPlaceholder)
		require.True(t, user.Active)
		require.Equal(t, testAccountWithEmail, user.ExternalID)

		// Confluence's username here is an email, which is not a valid
		// Mattermost username, so the proposal comes from the local part
		// rather than from mangling the address.
		require.Equal(t, "guillermo.vaya", user.MattermostUsername)
		require.Equal(t, UsernameProposalSourceEmail, user.UsernameProposalSource)
	})

	t.Run("user with no email", func(t *testing.T) {
		user := users[1]
		require.Equal(t, testAccountNoEmail, user.AccountID)
		require.False(t, user.Active, "the directory row says deactivated")

		require.True(t, user.EmailIsPlaceholder)
		require.True(t, strings.HasSuffix(user.Email, "@"+PlaceholderEmailDomain))
		require.True(t, strings.HasPrefix(user.Email, "confluence-"))

		// The account ID contains a colon, so it is not a usable username, and
		// there is no real email; the display name is next.
		require.Equal(t, "christian-johannsen-deactivated", user.MattermostUsername)
		require.Equal(t, UsernameProposalDisplayName, user.UsernameProposalSource)
	})

	t.Run("placeholder emails are reported", func(t *testing.T) {
		require.Len(t, warnings, 1)
		require.Equal(t, WarnUserPlaceholderEmail, warnings[0].Code)
		require.Equal(t, testAccountNoEmail, warnings[0].SourceID)
	})
}

// The placeholder must be reproducible so a re-export maps to the same user
// instead of creating a second one, and it must differ per site so two
// Confluence instances cannot collide.
func TestPlaceholderEmailIsDeterministicAndScoped(t *testing.T) {
	first, _ := selectSampleUsers(t, NewUserMapping())
	second, _ := selectSampleUsers(t, NewUserMapping())
	require.Equal(t, first[1].Email, second[1].Email)

	archive := archiveWithEntities(t, sampleUsers)
	refs := NewUserRefs()
	refs.Add(confluenceUserKey(testAccountNoEmail))

	other, _, err := SelectUsers(archive, "other.example.com", refs, NewUserMapping())
	require.NoError(t, err)
	require.NotEqual(t, first[1].Email, other[0].Email, "a different site must not produce the same address")
}

func TestSelectUsers_ExplicitMappingWins(t *testing.T) {
	mapping, err := ParseUserMapping(strings.NewReader(strings.Join([]string{
		strings.Join(userMappingHeader, ","),
		"5d3eaa4376cb3e0d9d31cf8e,,,,gvaya",
		",,,,ignored-without-a-selector",
	}[:2], "\n")))
	require.NoError(t, err)

	users, _ := selectSampleUsers(t, mapping)
	require.Equal(t, "gvaya", users[0].MattermostUsername)
	require.Equal(t, UsernameProposalExplicitMapping, users[0].UsernameProposalSource)

	require.Equal(t, UsernameProposalDisplayName, users[1].UsernameProposalSource,
		"an unmapped user still gets a derived proposal")
}

func userAccountIDs(users []*User) []string {
	ids := make([]string, 0, len(users))
	for _, user := range users {
		ids = append(ids, user.AccountID)
	}
	return ids
}

func TestProposeUsername(t *testing.T) {
	tests := []struct {
		name       string
		user       User
		want       string
		wantSource string
	}{
		{
			name:       "a valid Confluence username is used as-is",
			user:       User{AccountID: "a", ConfluenceUsername: "jsmith", Email: "j@example.com"},
			want:       "jsmith",
			wantSource: UsernameProposalSourceUsername,
		},
		{
			name:       "an uppercase username is lowercased",
			user:       User{AccountID: "a", ConfluenceUsername: "JSmith"},
			want:       "jsmith",
			wantSource: UsernameProposalSourceUsername,
		},
		// Confluence's username is the email for directory-backed accounts, and
		// "j.smith-example.com" would be worse than the local part.
		{
			name:       "an email-shaped username falls through to the local part",
			user:       User{AccountID: "a", ConfluenceUsername: "j.smith@example.com", Email: "j.smith@example.com"},
			want:       "j.smith",
			wantSource: UsernameProposalSourceEmail,
		},
		{
			name:       "an account-id username with a colon falls through",
			user:       User{AccountID: "a", ConfluenceUsername: "712020:abc", Email: "k@example.com"},
			want:       "k",
			wantSource: UsernameProposalSourceEmail,
		},
		{
			name:       "a placeholder email is never used as a username source",
			user:       User{AccountID: "a", Email: "confluence-abc@users.invalid", EmailIsPlaceholder: true, DisplayName: "Ada Lovelace"},
			want:       "ada-lovelace",
			wantSource: UsernameProposalDisplayName,
		},
		{
			name:       "display name is slugified",
			user:       User{AccountID: "a", DisplayName: "Guillermo Vayá"},
			want:       "guillermo-vay",
			wantSource: UsernameProposalDisplayName,
		},
		{
			name:       "nothing usable falls back to the account hash",
			user:       User{AccountID: "5d3eaa4376cb3e0d9d31cf8e"},
			want:       usernameFallbackPrefix + "5b8f1f2c6d3f",
			wantSource: UsernameProposalFallback,
		},
		{
			name:       "a display name of only punctuation falls back",
			user:       User{AccountID: "5d3eaa4376cb3e0d9d31cf8e", DisplayName: "!!! ???"},
			want:       usernameFallbackPrefix + "5b8f1f2c6d3f",
			wantSource: UsernameProposalFallback,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			user := test.user
			got, source := proposeUsername(&user)
			require.Equal(t, test.wantSource, source)
			if test.wantSource == UsernameProposalFallback {
				require.True(t, strings.HasPrefix(got, usernameFallbackPrefix), got)
				require.Len(t, got, len(usernameFallbackPrefix)+12)
				return
			}
			require.Equal(t, test.want, got)
		})
	}
}

func TestAssignUsernames_Collisions(t *testing.T) {
	t.Run("suffixes follow canonical account-id order", func(t *testing.T) {
		users := []*User{
			{AccountID: "aaa", DisplayName: "Same Name"},
			{AccountID: "bbb", DisplayName: "Same Name"},
			{AccountID: "ccc", DisplayName: "Same Name"},
		}
		require.NoError(t, assignUsernames(users, NewUserMapping()))

		require.Equal(t, []string{"same-name", "same-name_2", "same-name_3"}, usernames(users))
		for _, user := range users {
			require.Equal(t, UsernameProposalDisplayName, user.UsernameProposalSource,
				"a collision suffix does not change where the proposal came from")
		}
	})

	// Suffixing a name the operator explicitly asked for would silently
	// disobey them, so it is an error instead.
	t.Run("two explicit mappings to one username is an error", func(t *testing.T) {
		mapping, err := ParseUserMapping(strings.NewReader(strings.Join([]string{
			strings.Join(userMappingHeader, ","),
			"acct-1,,,,shared",
			"acct-2,,,,shared",
		}, "\n")))
		require.NoError(t, err)

		err = assignUsernames([]*User{{AccountID: "acct-1"}, {AccountID: "acct-2"}}, mapping)
		require.ErrorContains(t, err, "more than one Confluence user")
	})

	t.Run("a derived proposal never steals an explicitly mapped name", func(t *testing.T) {
		mapping, err := ParseUserMapping(strings.NewReader(strings.Join([]string{
			strings.Join(userMappingHeader, ","),
			"zzz,,,,ada-lovelace",
		}, "\n")))
		require.NoError(t, err)

		users := []*User{
			{AccountID: "aaa", DisplayName: "Ada Lovelace"},
			{AccountID: "zzz", DisplayName: "Someone Else"},
		}
		require.NoError(t, assignUsernames(users, mapping))

		require.Equal(t, "ada-lovelace", users[1].MattermostUsername)
		require.Equal(t, "ada-lovelace_2", users[0].MattermostUsername,
			"the derived proposal yields even though it was processed first")
	})

	t.Run("suffixing respects the username length limit", func(t *testing.T) {
		long := strings.Repeat("a", 64)
		users := []*User{
			{AccountID: "aaa", DisplayName: long},
			{AccountID: "bbb", DisplayName: long},
		}
		require.NoError(t, assignUsernames(users, NewUserMapping()))

		for _, user := range users {
			require.LessOrEqual(t, len(user.MattermostUsername), 64, user.MattermostUsername)
		}
		require.NotEqual(t, users[0].MattermostUsername, users[1].MattermostUsername)
	})
}

func usernames(users []*User) []string {
	out := make([]string, 0, len(users))
	for _, user := range users {
		out = append(out, user.MattermostUsername)
	}
	return out
}

func TestParseUserMapping(t *testing.T) {
	header := strings.Join(userMappingHeader, ",")

	t.Run("selector precedence", func(t *testing.T) {
		mapping, err := ParseUserMapping(strings.NewReader(strings.Join([]string{
			header,
			"acct-1,,,,by-account",
			",key-2,,,by-key",
			",,User.Three,,by-username",
			",,,Four@Example.COM,by-email",
		}, "\n")))
		require.NoError(t, err)

		// Account ID beats every looser selector on the same user.
		require.Equal(t, "by-account", lookup(t, mapping, &User{
			AccountID: "acct-1", ConfluenceUserKey: "key-2", ConfluenceUsername: "user.three", Email: "four@example.com",
		}))
		require.Equal(t, "by-key", lookup(t, mapping, &User{ConfluenceUserKey: "key-2"}))

		// Username and email match case-insensitively after trimming.
		require.Equal(t, "by-username", lookup(t, mapping, &User{ConfluenceUsername: "  USER.THREE  "}))
		require.Equal(t, "by-email", lookup(t, mapping, &User{Email: "four@example.com"}))

		_, ok := mapping.Lookup(&User{AccountID: "unmapped"})
		require.False(t, ok)
	})

	// Account IDs and user keys are opaque and case-sensitive; folding them
	// could merge two distinct Atlassian users.
	t.Run("account id and user key match exactly", func(t *testing.T) {
		mapping, err := ParseUserMapping(strings.NewReader(header + "\nAcct-1,,,,target"))
		require.NoError(t, err)

		_, ok := mapping.Lookup(&User{AccountID: "acct-1"})
		require.False(t, ok)
		require.Equal(t, "target", lookup(t, mapping, &User{AccountID: "Acct-1"}))
	})

	t.Run("an exactly duplicated row is ignored", func(t *testing.T) {
		mapping, err := ParseUserMapping(strings.NewReader(strings.Join([]string{
			header,
			"acct-1,,,,target",
			"acct-1,,,,target",
		}, "\n")))
		require.NoError(t, err)
		require.Equal(t, "target", lookup(t, mapping, &User{AccountID: "acct-1"}))
	})

	t.Run("a UTF-8 BOM does not break the header check", func(t *testing.T) {
		mapping, err := ParseUserMapping(strings.NewReader("\xef\xbb\xbf" + header + "\nacct-1,,,,target"))
		require.NoError(t, err)
		require.Equal(t, "target", lookup(t, mapping, &User{AccountID: "acct-1"}))
	})

	errorTests := []struct {
		name    string
		csv     string
		wantErr string
	}{
		{
			name:    "empty file",
			csv:     "",
			wantErr: "header row is mandatory",
		},
		{
			name:    "wrong header",
			csv:     "account_id,mattermost_username\n",
			wantErr: "header must be exactly",
		},
		{
			name:    "reordered header",
			csv:     "confluence_user_key,confluence_account_id,confluence_username,confluence_email,mattermost_username\n",
			wantErr: "header must be exactly",
		},
		{
			name:    "missing target",
			csv:     header + "\nacct-1,,,,",
			wantErr: "mattermost_username is required",
		},
		{
			name:    "invalid target",
			csv:     header + "\nacct-1,,,,not a username!",
			wantErr: "is not a valid Mattermost username",
		},
		{
			name:    "no selector",
			csv:     header + "\n,,,,target",
			wantErr: "at least one Confluence selector column is required",
		},
		{
			name:    "conflicting rows for one selector",
			csv:     header + "\nacct-1,,,,first\nacct-1,,,,second",
			wantErr: "cannot also map it to",
		},
		{
			name:    "conflicting rows differing only by case in the email",
			csv:     header + "\n,,,a@example.com,first\n,,,A@Example.com,second",
			wantErr: "cannot also map it to",
		},
		{
			name:    "wrong column count",
			csv:     header + "\nacct-1,,,target",
			wantErr: "reading user mapping line 2",
		},
	}

	for _, test := range errorTests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ParseUserMapping(strings.NewReader(test.csv))
			require.ErrorContains(t, err, test.wantErr)
		})
	}
}

func lookup(t *testing.T, mapping *UserMapping, user *User) string {
	t.Helper()

	target, ok := mapping.Lookup(user)
	require.True(t, ok)
	return target
}

func TestCanonicalAccountID(t *testing.T) {
	key := confluenceUserKey("user-key-1")

	require.Equal(t, "557058:abc", canonicalAccountID(testOrganizationID, key, "557058:abc"),
		"the Atlassian account id wins")
	require.Equal(t, "557058:abc", canonicalAccountID(testOrganizationID, key, "  557058:abc  "))
	require.Equal(t, "user-key-1", canonicalAccountID(testOrganizationID, key, ""),
		"the Confluence user key is next")

	empty := EntityKey{Package: PkgConfluenceUser, Class: "ConfluenceUserImpl", IDName: "key"}
	hashed := canonicalAccountID(testOrganizationID, empty, "")
	require.Len(t, hashed, 64)
	require.Equal(t, hashed, canonicalAccountID(testOrganizationID, empty, ""), "deterministic")
	require.NotEqual(t, hashed, canonicalAccountID("other.example.com", empty, ""),
		"scoped by organization so two sites cannot collide")
}

// TestSelectUsers_PrivateSample is the E8 gate against a real export.
//
//	CONFLUENCE_SAMPLE_ZIP=/path/to/Confluence-export.zip go test ./services/confluence/...
func TestSelectUsers_PrivateSample(t *testing.T) {
	path := os.Getenv("CONFLUENCE_SAMPLE_ZIP")
	if path == "" {
		t.Skip("set CONFLUENCE_SAMPLE_ZIP to run against a real Confluence export")
	}

	archive, err := OpenSourceArchive(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, archive.Close()) }()

	spaces, descriptor, err := CatalogSpaces(archive)
	require.NoError(t, err)

	for _, space := range spaces {
		content, err := SelectPageMetadata(archive, space, descriptor)
		require.NoError(t, err)
		deps, err := SelectDependencies(archive, space, descriptor, content, false)
		require.NoError(t, err)

		refs := NewUserRefs()
		refs.AddFromContent(content)
		refs.AddFromDependencies(deps)

		users, warnings, err := SelectUsers(archive, testOrganizationID, refs, NewUserMapping())
		require.NoErrorf(t, err, "space %s", space.SpaceKey)
		require.Len(t, users, refs.Len(), "every referenced user must resolve")

		seenUsernames := map[string]bool{}
		placeholders := 0
		for _, user := range users {
			require.NotEmpty(t, user.AccountID)
			require.NotEmpty(t, user.Email)
			require.NotEmpty(t, user.MattermostUsername)
			require.NotEmpty(t, user.UsernameProposalSource)
			require.Truef(t, model.IsValidUsername(user.MattermostUsername),
				"account %s proposed invalid username %q", user.AccountID, user.MattermostUsername)
			require.Falsef(t, seenUsernames[user.MattermostUsername],
				"duplicate username %q", user.MattermostUsername)
			seenUsernames[user.MattermostUsername] = true

			if user.EmailIsPlaceholder {
				placeholders++
			}
		}
		require.Len(t, warnings, placeholders)

		t.Logf("%-42s %2d users (%d placeholder emails)", space.SpaceKey, len(users), placeholders)
	}
}
