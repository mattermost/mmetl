package slack_grid

import (
	"archive/zip"
	"bytes"
	"testing"

	"github.com/mattermost/mmetl/services/slack"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDiscoverTeamMap_FromUsersJSON(t *testing.T) {
	gt := NewGridTransformer(logrus.New())
	zipReader := buildDiscoverZip(t, []discoverTeam{{
		folder: "acme",
		users: []slack.SlackUser{
			{Id: "U1", TeamID: "T0001"},
			{Id: "U2", TeamID: "T0001"},
			{Id: "U3", TeamID: "T9999", IsRestricted: true},
		},
	}, {
		folder: "widgets-inc",
		users: []slack.SlackUser{
			{Id: "U4", TeamID: "T0002"},
		},
	}})

	got, err := gt.DiscoverTeamMap(zipReader)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{
		"T0001": "acme",
		"T0002": "widgets-inc",
	}, got)
}

func TestDiscoverTeamMap_GuestOnlyFallsBackToPosts(t *testing.T) {
	gt := NewGridTransformer(logrus.New())
	zipReader := buildDiscoverZip(t, []discoverTeam{{
		folder: "acme",
		users: []slack.SlackUser{
			{Id: "G1", TeamID: "T9999", IsRestricted: true},
			{Id: "G2", TeamID: "T9999", IsRestricted: true},
		},
		posts: []Post{{Team: "T0001"}},
	}})

	got, err := gt.DiscoverTeamMap(zipReader)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"T0001": "acme"}, got)
}

func TestDiscoverTeamMap_FromPostsWhenUsersLackTeamID(t *testing.T) {
	gt := NewGridTransformer(logrus.New())
	zipReader := buildDiscoverZip(t, []discoverTeam{{
		folder: "acme",
		users:  []slack.SlackUser{{Id: "U1"}},
		posts:  []Post{{Team: "T0001"}},
	}})

	got, err := gt.DiscoverTeamMap(zipReader)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"T0001": "acme"}, got)
}

func TestDiscoverTeamMap_Conflict(t *testing.T) {
	gt := NewGridTransformer(logrus.New())
	zipReader := buildDiscoverZip(t, []discoverTeam{{
		folder: "acme",
		users:  []slack.SlackUser{{Id: "U1", TeamID: "T0001"}},
	}, {
		folder: "widgets-inc",
		users:  []slack.SlackUser{{Id: "U2", TeamID: "T0001"}},
	}})

	_, err := gt.DiscoverTeamMap(zipReader)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "maps to both")
}

func TestDiscoverTeamMap_NoTeamsFolder(t *testing.T) {
	gt := NewGridTransformer(logrus.New())
	zipReader := buildGridZip(t, nil)

	_, err := gt.DiscoverTeamMap(zipReader)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no teams/ folders")
}

func TestDiscoverTeamMap_CannotInfer(t *testing.T) {
	gt := NewGridTransformer(logrus.New())
	zipReader := buildDiscoverZip(t, []discoverTeam{{
		folder: "acme",
		users:  []slack.SlackUser{{Id: "U1"}},
	}})

	_, err := gt.DiscoverTeamMap(zipReader)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "could not infer team mapping from the export; provide --team-map-path")
}

// TestTeamIDFromPostFile_RejectsOversizedFile guards against a corrupt or
// zip-bomb post file being fully decompressed into memory: a file whose
// declared uncompressed size exceeds maxPostFileSize must be rejected before
// it is ever opened/read.
func TestTeamIDFromPostFile_RejectsOversizedFile(t *testing.T) {
	gt := NewGridTransformer(logrus.New())

	// The size check must happen before file.Open(), so a synthetic *zip.File
	// declaring an oversized UncompressedSize64 is enough to exercise it -
	// no need to actually allocate/compress a file that large.
	file := &zip.File{FileHeader: zip.FileHeader{
		Name:               "teams/acme/general/2024-01-01.json",
		UncompressedSize64: maxPostFileSize + 1,
	}}

	teamID, err := gt.teamIDFromPostFile(file)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeding the")
	assert.Equal(t, "", teamID)
}

func TestMajorityNativeTeamID_IgnoresGuests(t *testing.T) {
	users := []slack.SlackUser{
		{Id: "U1", TeamID: "T0001"},
		{Id: "U2", TeamID: "T0001"},
		{Id: "G1", TeamID: "T9999", IsRestricted: true},
		{Id: "G2", TeamID: "T9999", IsRestricted: true},
		{Id: "G3", TeamID: "T9999", IsRestricted: true},
	}
	assert.Equal(t, "T0001", majorityNativeTeamID(users))
}

func TestMajorityTeamID_TieReturnsEmpty(t *testing.T) {
	users := []slack.SlackUser{
		{Id: "U1", TeamID: "T0001"},
		{Id: "U2", TeamID: "T0002"},
	}
	assert.Equal(t, "", majorityNativeTeamID(users))
	assert.Equal(t, "", majorityAnyTeamID(users))
}

func TestFormatTeamMap(t *testing.T) {
	got := FormatTeamMap(map[string]string{
		"T0002": "widgets-inc",
		"T0001": "acme",
	})
	assert.Equal(t, "  T0001 -> acme\n  T0002 -> widgets-inc\n", got)
}

type discoverTeam struct {
	folder string
	users  []slack.SlackUser
	posts  []Post
}

func buildDiscoverZip(t *testing.T, teams []discoverTeam) *zip.Reader {
	t.Helper()

	zipData := new(bytes.Buffer)
	zipWriter := zip.NewWriter(zipData)

	marshalAndWriteToZipFile(zipWriter, "channels.json", []slack.SlackChannel{}, t)
	marshalAndWriteToZipFile(zipWriter, "groups.json", []slack.SlackChannel{}, t)
	marshalAndWriteToZipFile(zipWriter, "dms.json", []slack.SlackChannel{}, t)
	marshalAndWriteToZipFile(zipWriter, "mpims.json", []slack.SlackChannel{}, t)

	for _, team := range teams {
		_, err := zipWriter.Create("teams/" + team.folder + "/")
		assert.NoError(t, err)

		if team.users != nil {
			marshalAndWriteToZipFile(zipWriter, "teams/"+team.folder+"/users.json", team.users, t)
		}
		if team.posts != nil {
			marshalAndWriteToZipFile(zipWriter, "teams/"+team.folder+"/general/2024-01-01.json", team.posts, t)
		}
	}

	err := zipWriter.Close()
	assert.NoError(t, err)

	zipReader, err := zip.NewReader(bytes.NewReader(zipData.Bytes()), int64(zipData.Len()))
	assert.NoError(t, err)

	return zipReader
}
