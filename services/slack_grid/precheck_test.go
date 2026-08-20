package slack_grid

import (
	"archive/zip"
	"bytes"
	"testing"

	"github.com/mattermost/mmetl/services/slack"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
)

// buildGridZip creates a minimal valid grid export zip, plus a "teams/<name>/"
// folder entry for each name given in teamFolders.
func buildGridZip(t *testing.T, teamFolders []string) *zip.Reader {
	zipData := new(bytes.Buffer)
	zipWriter := zip.NewWriter(zipData)

	marshalAndWriteToZipFile(zipWriter, "channels.json", []slack.SlackChannel{}, t)
	marshalAndWriteToZipFile(zipWriter, "groups.json", []slack.SlackChannel{}, t)
	marshalAndWriteToZipFile(zipWriter, "dms.json", []slack.SlackChannel{}, t)
	marshalAndWriteToZipFile(zipWriter, "mpims.json", []slack.SlackChannel{}, t)

	for _, teamFolder := range teamFolders {
		_, err := zipWriter.Create("teams/" + teamFolder + "/")
		assert.NoError(t, err)
	}

	err := zipWriter.Close()
	assert.NoError(t, err)

	zipReader, err := zip.NewReader(bytes.NewReader(zipData.Bytes()), int64(zipData.Len()))
	assert.NoError(t, err)

	return zipReader
}

func TestGridPreCheck_Valid(t *testing.T) {
	gt := NewGridTransformer(logrus.New())
	gt.Teams = map[string]string{
		"team1": "acme",
		"team2": "widgets-inc",
	}

	zipReader := buildGridZip(t, []string{"acme", "widgets-inc"})

	assert.True(t, gt.GridPreCheck(zipReader))
}

func TestGridPreCheck_DuplicateTeamNames(t *testing.T) {
	gt := NewGridTransformer(logrus.New())
	gt.Teams = map[string]string{
		"team1": "acme",
		"team2": "acme",
	}

	zipReader := buildGridZip(t, []string{"acme"})

	assert.False(t, gt.GridPreCheck(zipReader))
}

func TestGridPreCheck_MissingTeamFolder(t *testing.T) {
	gt := NewGridTransformer(logrus.New())
	gt.Teams = map[string]string{
		"team1": "acme",
	}

	// export only contains a folder for a different team name.
	zipReader := buildGridZip(t, []string{"widgets-inc"})

	assert.False(t, gt.GridPreCheck(zipReader))
}

// TestTeamFolderNames_RejectsEmptyName guards against a "teams//users.json"
// style entry (an empty folder name between the two slashes) registering ""
// as a valid team folder. Left unchecked, that would let a team mapped to an
// empty display name pass checkTeamFoldersExist and later resolve to the
// broken "teams//" output path.
func TestTeamFolderNames_RejectsEmptyName(t *testing.T) {
	zipData := new(bytes.Buffer)
	zipWriter := zip.NewWriter(zipData)

	_, err := zipWriter.Create("teams//users.json")
	assert.NoError(t, err)

	assert.NoError(t, zipWriter.Close())

	zipReader, err := zip.NewReader(bytes.NewReader(zipData.Bytes()), int64(zipData.Len()))
	assert.NoError(t, err)

	folders := teamFolderNames(zipReader)
	assert.NotContains(t, folders, "")
	assert.Empty(t, folders)
}

func TestGridPreCheck_EmptyTeamFolderName(t *testing.T) {
	gt := NewGridTransformer(logrus.New())
	gt.Teams = map[string]string{
		"team1": "",
	}

	zipData := new(bytes.Buffer)
	zipWriter := zip.NewWriter(zipData)

	marshalAndWriteToZipFile(zipWriter, "channels.json", []slack.SlackChannel{}, t)
	marshalAndWriteToZipFile(zipWriter, "groups.json", []slack.SlackChannel{}, t)
	marshalAndWriteToZipFile(zipWriter, "dms.json", []slack.SlackChannel{}, t)
	marshalAndWriteToZipFile(zipWriter, "mpims.json", []slack.SlackChannel{}, t)

	_, err := zipWriter.Create("teams//users.json")
	assert.NoError(t, err)

	assert.NoError(t, zipWriter.Close())

	zipReader, err := zip.NewReader(bytes.NewReader(zipData.Bytes()), int64(zipData.Len()))
	assert.NoError(t, err)

	// an empty team name must never be accepted as a valid folder match.
	assert.False(t, gt.GridPreCheck(zipReader))
}
