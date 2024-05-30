package slack_grid

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/mattermost/mmetl/services/slack"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
)

type TestStruct struct {
	posts  []Post
	result string
}

func setupGridTransformer(t *testing.T) *GridTransformer {
	gridTransformer := NewGridTransformer(logrus.New())
	testDir := createTestDir(t)
	defer os.RemoveAll(testDir)
	gridTransformer.dirPath = testDir

	return gridTransformer
}

func TestParseGridSlackExportFile(t *testing.T) {
	// Create a new logger for testing
	bt := setupGridTransformer(t)

	// Create a new zip file for testing
	zipData := new(bytes.Buffer)
	zipWriter := zip.NewWriter(zipData)

	dms := []slack.SlackChannel{
		{Id: "dm1", Members: []string{"UDDG9RMC7", "U01GLJL697B"}},
		{Id: "dm2", Members: []string{"UDDG9RMC7", "U01GLJL697B"}},
		{Id: "dm3", Members: []string{"UDDG9RMC7", "U01GLJL697B"}},
		{Id: "dm4", Members: []string{"UDDG9RMC7", "U01GLJL697B"}},
		{Id: "dm5", Members: []string{"UDDG9RMC7", "U01GLJL697B"}},
	}

	channels := []slack.SlackChannel{
		{Id: "channel1", Members: []string{"UDDG9RMC7", "U01GLJL697B"}},
		{Id: "channel2", Members: []string{"UDDG9RMC7", "U01GLJL697B"}},
		{Id: "channel3", Members: []string{"UDDG9RMC7", "U01GLJL697B"}},
		{Id: "channel4", Members: []string{"UDDG9RMC7", "U01GLJL697B"}},
		{Id: "channel5", Members: []string{"UDDG9RMC7", "U01GLJL697B"}},
	}

	mpims := []slack.SlackChannel{
		{Id: "mpim1", Members: []string{"UDDG9RMC7", "U01GLJL697B"}},
		{Id: "mpim2", Members: []string{"UDDG9RMC7", "U01GLJL697B"}},
		{Id: "mpim3", Members: []string{"UDDG9RMC7", "U01GLJL697B"}},
		{Id: "mpim4", Members: []string{"UDDG9RMC7", "U01GLJL697B"}},
		{Id: "mpim5", Members: []string{"UDDG9RMC7", "U01GLJL697B"}},
	}

	marshalAndWriteToZipFile(zipWriter, "mpims.json", mpims, t)
	marshalAndWriteToZipFile(zipWriter, "dms.json", dms, t)
	marshalAndWriteToZipFile(zipWriter, "channels.json", channels, t)
	marshalAndWriteToZipFile(zipWriter, "groups.json", channels, t)

	// Close the zip writer
	err := zipWriter.Close()
	assert.NoError(t, err)

	// Create a new zip reader
	zipReader, err := zip.NewReader(bytes.NewReader(zipData.Bytes()), int64(zipData.Len()))
	assert.NoError(t, err)

	// we do not need a team name here.

	bt.Teams = map[string]string{
		"team1": "team1",
	}

	valid := bt.GridPreCheck(zipReader)
	if !valid {
		t.Fatal("file is not valid")
	}

	// Call the ParseGridSlackExportFile function
	slackExport, err := bt.ParseGridSlackExportFile(zipReader)

	// Check the returned error
	assert.NoError(t, err)

	// Check the returned GridSlackExport
	// For example, you can check if the number of channels matches the expected number
	assert.Equal(t, 5, len(slackExport.Private))
	assert.Equal(t, 5, len(slackExport.DMs))
	assert.Equal(t, 5, len(slackExport.GMs))
	assert.Equal(t, 5, len(slackExport.Public))
}

func TestChannelHasBeenMoved(t *testing.T) {

	channels := []ChannelsToMove{
		{SlackChannel: slack.SlackChannel{Id: "channel1"}, Moved: true},
		{SlackChannel: slack.SlackChannel{Id: "channel2"}, Moved: false},
	}
	t.Run("channel has been moved", func(t *testing.T) {
		channel := slack.SlackChannel{Id: "channel1"}
		assert.True(t, channelHasBeenMoved(channel, channels))
	})

	t.Run("channel has not been moved", func(t *testing.T) {
		channel := slack.SlackChannel{Id: "channel2"}
		assert.False(t, channelHasBeenMoved(channel, channels))
	})

	t.Run("channel does not exist in channelsToMoved", func(t *testing.T) {
		channel := slack.SlackChannel{Id: "channel3"}
		assert.False(t, channelHasBeenMoved(channel, channels))
	})
}

func TestFindTeamIDFromPostArray(t *testing.T) {
	bt := setupGridTransformer(t)
	tests := []TestStruct{
		{posts: []Post{{Team: "team1"}, {}}, result: "team1"},
		{posts: []Post{{}, {}, {Team: "team1"}}, result: "team1"},
		{posts: []Post{{}, {}, {}}, result: ""},
		{posts: []Post{{Team: "team1"}, {Team: "team2"}}, result: "team1"},
		{posts: []Post{{Team: ""}, {Team: "team1"}}, result: "team1"},
	}

	t.Run("finds the team name in a post array", func(t *testing.T) {
		for _, test := range tests {
			teamID, err := bt.findTeamIDFromPostArray(marshalJson(test.posts, t))
			assert.NoError(t, err)
			assert.Equal(t, test.result, teamID)
		}
	})
}

func TestFindTeamIdFromChannelDir(t *testing.T) {
	bt := setupGridTransformer(t)

	postsWithTwoTeams := [][]Post{
		{{}, {}, {Team: ""}},
		{{}, {}, {Team: "team1"}},
		{{}, {}, {Team: "team2"}},
	}

	postsWithoutTeam := [][]Post{
		{{}, {}, {Team: ""}},
		{{}, {}, {Team: ""}},
		{{}, {}, {Team: ""}},
	}

	t.Run("finds the team name in a post directory", func(t *testing.T) {

		dir := createDirAndWriteFiles(postsWithTwoTeams, t)
		defer os.RemoveAll(dir)
		bt.dirPath = dir
		teamID, err := bt.findTeamIdFromChannelDir("")
		assert.NoError(t, err)
		assert.Equal(t, "team1", teamID)
	})

	t.Run("directory does not exist", func(t *testing.T) {

		teamID, err := bt.findTeamIdFromChannelDir("badPath")
		assert.ErrorContains(t, err, "error reading directory")
		assert.Equal(t, "", teamID)
	})

	// TODO - Need to figure out why this test is only reading the posts.json file and seems to be ignoring the
	// unreadable.json file.
	// t.Run("bad file in directory", func(t *testing.T) {
	// 	dir := createTestDir(t)
	// 	defer os.RemoveAll(dir)
	// 	bulkTransformer.dirPath = dir

	// 	// writing the bad file first so it's read and handled.
	// 	os.WriteFile(filepath.Join(dir, "/unreadable.json"), marshalJson(postsWithTwoTeams[2], t), 0644)
	// 	os.WriteFile(filepath.Join(dir, "/posts.json"), marshalJson(postsWithTwoTeams[1], t), 0644)

	// 	teamID, err := bulkTransformer.findTeamIdFromChannelDir("")
	// 	assert.ErrorContains(t, err, "Error reading file")
	// 	assert.Equal(t, "", teamID)
	// })

	t.Run("finds no team name in a post directory", func(t *testing.T) {
		dir := createDirAndWriteFiles(postsWithoutTeam, t)
		defer os.RemoveAll(dir)
		bt.dirPath = dir

		teamID, err := bt.findTeamIdFromChannelDir("")
		assert.ErrorContains(t, err, "No team ID found")
		assert.Equal(t, "", teamID)
	})
}

func TestAppendChannelToChannelsToMove(t *testing.T) {
	bt := setupGridTransformer(t)

	teamName := "team1"

	teamExistingChannels := marshalJson([]slack.SlackChannel{{Id: "0"}}, t)
	writeToFileInTestDir(filepath.Join(bt.dirPath, "teams", teamName), string(ChannelFilePublic)+".json", teamExistingChannels, t)

	channelsToMerge := []ChannelsToMove{
		{SlackChannel: slack.SlackChannel{Id: "1"}, TeamName: teamName},
		{SlackChannel: slack.SlackChannel{Id: "2"}, TeamName: teamName},
	}
	for _, channel := range channelsToMerge {
		err := bt.appendChannelToTeamChannelsFile(ChannelFilePublic, channel)
		if err != nil {
			t.Fatalf("error appending channel to team channels file %v", err)
		}
	}

	teamUpdatedChannels := readChannelsFile(filepath.Join(bt.dirPath, "teams", teamName, string(ChannelFilePublic)+".json"), t)

	for i, channel := range teamUpdatedChannels {
		if strconv.Itoa(i) != channel.Id {
			t.Fatalf("channel id %v does not match expected value %v", channel.Id, i)
		}
	}
}

func TestHandleMovingChannels(t *testing.T) {

	bt := setupGridTransformer(t)

	bt.Teams = map[string]string{
		"team1": "team1",
	}

	teamPath := filepath.Join(bt.dirPath, "teams", "team1")
	channelPath := filepath.Join(bt.dirPath, "channel1")

	// storing the channels.json file in the team directory to be used.
	writeToFileInTestDir(teamPath, "channels.json",
		marshalJson([]slack.SlackChannel{}, t),
		t,
	)

	// creating a channel with a single post in it that we can move.
	writeToFileInTestDir(channelPath, "posts.json",
		marshalJson([]Post{{Team: "team1"}}, t),
		t,
	)

	slackChannel := []slack.SlackChannel{
		{Id: "channel1", Name: "channel1"},
	}
	err := bt.HandleMovingChannels(slackChannel, ChannelFilePublic)

	if err != nil {
		t.Fatalf("error moving channel %v", err)
	}

	channels := readChannelsFile(teamPath+"/channels.json", t)

	if len(channels) != len(slackChannel) {
		t.Fatal("channel was not moved. Channel IDs in team path are: ", channels)
	}
}

func readChannelsFile(path string, t *testing.T) []slack.SlackChannel {
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("error reading file %v", path)
	}

	var channels []slack.SlackChannel
	if len(data) > 0 {
		err = json.Unmarshal(data, &channels)
		if err != nil {
			t.Fatalf("error reading file %v", path)
		}
	}
	return channels
}

func createDirAndWriteFiles(data [][]Post, t *testing.T) string {
	dir := createTestDir(t)

	for i, posts := range data {
		postArray := marshalJson(posts, t)
		fileName := "/post_" + strconv.Itoa(i) + ".json"
		writeToFileInTestDir(dir, fileName, postArray, t)
	}
	return dir
}

func writeToFileInTestDir(dir string, filename string, data []byte, t *testing.T) {
	// Create the directory if it does not exist
	err := os.MkdirAll(dir, 0755)
	if err != nil {
		t.Fatalf("error creating the directory %v", dir)
	}

	filePath := filepath.Join(dir, filename)
	err = os.WriteFile(filePath, data, 0644)
	if err != nil {
		t.Fatal(
			errors.Wrap(err, fmt.Sprintf("error writing the file %v to the test directory", filename)),
		)
	}
}

func marshalJson(data interface{}, t *testing.T) []byte {
	jsonData, err := json.Marshal(data)
	if err != nil {
		t.Fatal(errors.Wrap(err, "error mashalling json"))
	}
	return jsonData
}

func createTestDir(t *testing.T) string {
	dir, err := os.MkdirTemp("", "mmetl_test_*")

	if err != nil {
		t.Fatal(errors.Wrap(err, "error creating test directory"))
	}

	return dir
}

func marshalAndWriteToZipFile(zipWriter *zip.Writer, filename string, data interface{}, t *testing.T) {
	// Create a new file in the zip file
	fileWriter, err := zipWriter.Create(filename)
	if err != nil {
		t.Fatal(err)
	}

	// Marshal the data to a JSON string
	jsonData, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}

	// Write the data to the file
	_, err = fileWriter.Write(jsonData)
	if err != nil {
		t.Fatal(err)
	}
}
