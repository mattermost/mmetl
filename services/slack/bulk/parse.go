package slack_bulk

import (
	"archive/zip"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/mattermost/mattermost-server/v6/model"
	slack "github.com/mattermost/mmetl/services/slack"
	"github.com/pkg/errors"

	// create a team mapping json file and pass it in as an arg
	// read the team mapping file into a map

	// DONE // Parse the Slack export file into a set of intermediate files.

	// channels.json
	// dms.json
	// groups.json
	// mpims.json
	// org_users.json

	// to parse channels:
	// go through each channel in the channels.json and find the directory for it at the root level
	// channel.name = a directory at the root level
	// Pull the file names of the files in that dir
	// look through the files for a post file. The post file should return an array of posts.
	// inside of this array we should see a post with the key "team". This ID defines what team the channel belongs to.
	// move that directory into the team directory in "teams/"
	// append the channel to the channels.json file in the team directory
	// continue this process for every channel in the channels.json file.

	// continue this gor mpims. The structure of the posts are the same, so the general flow is the same.
	// the mpims.jsonp[0].name = a file name at the root level.
	// NOT THE ID

	// continue this for groups. The structure of the posts are the same.
	// Groups also use the name as the file name at the root level.

	// continue this for DMs. The structure of the posts are the same.
	// the dms.jsonp[0].id = a dir name at the root level.
	// we need to create a dms.json file at each team level and append to it.

	// when this is all done we should confirm the users.json of each team has the correct people.
	// org_users[number].id = post.user
	// need to look to see if all posts have a user that belongs to it.

	log "github.com/sirupsen/logrus"
)

type BulkSlackExport struct {
	Channels       []slack.SlackChannel
	Groups         []slack.SlackChannel
	DMs            []slack.SlackChannel
	Mpims          []slack.SlackChannel
	DirectChannels []slack.SlackChannel
}

type Post struct {
	Team       string `json:"team"`
	UserTeam   string `json:"user_team"`
	SourceTeam string `json:"source_team"`
}

type BulkTransformer struct {
	slack.Transformer
	Teams   map[string]string
	dirPath string
}

func NewBulkTransformer(logger log.FieldLogger) *BulkTransformer {
	return &BulkTransformer{
		Transformer: slack.Transformer{
			Intermediate: &slack.Intermediate{},
			Logger:       logger,
		},
	}
}

const (
	ChannelsFile       = "channels.json"
	GroupsFile         = "groups.json"
	DirectMessagesFile = "dms.json"
	MultiPartyIMsFile  = "mpims.json"
	UsersFile          = "org_users.json"
)

func (t *BulkTransformer) ParseBulkSlackExportFile(zipReader *zip.Reader) (*BulkSlackExport, error) {
	slackExport := BulkSlackExport{}
	numFiles := len(zipReader.File)

	// only finding the root information here and storing it.
	for i, file := range zipReader.File {
		if file.FileInfo().IsDir() || strings.Contains(file.Name, "/") {
			continue
		}
		err := func(i int, file *zip.File) error {

			t.Logger.Infof("Processing file %d of %d: %s", i+1, numFiles, file.Name)
			reader, err := file.Open()
			if err != nil {
				return err
			}
			defer reader.Close()

			switch file.Name {
			case "channels.json":
				slackExport.Channels, err = t.SlackParseChannels(reader, model.ChannelTypeOpen)
				if err != nil {
					t.Logger.Infof("error parsing channels.json: %w", err)
					return err
				}
			case "groups.json":
				slackExport.Groups, err = t.SlackParseChannels(reader, model.ChannelTypePrivate)
				if err != nil {
					t.Logger.Infof("error parsing groups.json: %w", err)
					return err
				}
			case "dms.json":
				slackExport.DMs, err = t.SlackParseChannels(reader, model.ChannelTypeDirect)
				if err != nil {
					t.Logger.Infof("error parsing dms.json: %w", err)
					return err
				}
			case "mpims.json":
				slackExport.Mpims, err = t.SlackParseChannels(reader, model.ChannelTypeGroup)
				if err != nil {
					t.Logger.Infof("error parsing mpims.json: %w", err)
					return err
				}

			default:
				t.Logger.Infof("Skipping file %s", file.Name)
			}
			return nil
		}(i, file)

		if err != nil {
			return nil, err
		}
	}
	return &slackExport, nil
}

type ChannelsToMove struct {
	SlackChannel slack.SlackChannel
	TeamID       string
	TeamName     string
	Moved        bool
}

func (t *BulkTransformer) HandleMovingChannels(slackExport *BulkSlackExport) error {

	channelsToMove := []ChannelsToMove{}
	t.Logger.Info("Unzipped slack export path being used: ", t.dirPath)

	itemsInDir, err := t.readDir(t.dirPath)
	t.Logger.Infof("Found %v items in directory.", len(itemsInDir))

	if err != nil {
		t.Logger.Error("Error reading directory:", err)
		return err
	}

	for _, channel := range slackExport.Channels {
		if channelHasBeenMoved(channel, channelsToMove) {
			break
		}

		teamID := ""

		for _, item := range itemsInDir {
			// comparing the file name to the channel name because they'll be the same here
			// but it also needs to be a dir for us to traverse down.
			if strings.HasPrefix(item.Name(), channel.Name) && item.Type().IsDir() {

				teamID, err = t.findTeamIdFromPostDir(item.Name())
				if err != nil {
					t.Logger.Error("error finding channel info in dir: %w", err)
					continue
				}
			}
		}
		moveChannel := ChannelsToMove{
			SlackChannel: channel,
			TeamID:       teamID,
			TeamName:     t.Teams[teamID],
		}
		t.Logger.Infof("Found channel to move.  %v", moveChannel)
		channelsToMove = append(channelsToMove, moveChannel)
	}

	t.Logger.Infof("Moving channels... %v \n", len(channelsToMove))

	for i, channel := range channelsToMove {
		err := t.performChannelMove(channel, channelsToMove, i)
		if err != nil {
			return err
		}
	}
	return nil
}

func (t *BulkTransformer) performChannelMove(channel ChannelsToMove, channelsToMove []ChannelsToMove, i int) error {

	t.Logger.Infof(
		"Moving channel %v to team %v. channel ID: %v, team ID: %v",
		channel.SlackChannel.Name,
		channel.TeamName,
		channel.TeamID,
		channel.SlackChannel.Id,
	)

	currentDir := filepath.Join(t.dirPath, channel.SlackChannel.Name)
	newDir := filepath.Join(t.dirPath, "teams", channel.TeamName, channel.SlackChannel.Name)

	err := moveDirectory(currentDir, newDir)

	if err != nil {
		return errors.Wrapf(err, "error moving channel %v to team %v", channel.SlackChannel.Name, channel.TeamName)
	}

	// append this to the channels file in the team directory
	channelsToMove[i].Moved = true

	err = t.appendChannelToTeamChannelsFile(channel)
	if err != nil {
		return errors.Wrapf(err, "error appending channel %v to team %v", channel.SlackChannel.Name, channel.TeamName)
	}
	return nil
}

func (t *BulkTransformer) appendChannelToTeamChannelsFile(channel ChannelsToMove) error {
	path := filepath.Join(t.dirPath, "teams", channel.TeamName, ChannelsFile)

	// Read the existing channels
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	var channels []slack.SlackChannel
	if len(data) > 0 {
		err = json.Unmarshal(data, &channels)
		if err != nil {
			return err
		}
	}

	// Append the new channel
	channels = append(channels, channel.SlackChannel)

	// Write the updated channels back to the file
	data, err = json.Marshal(channels)
	if err != nil {
		return err
	}

	err = os.WriteFile(path, data, 0644)
	if err != nil {
		return err
	}

	return nil
}

func (t *BulkTransformer) findTeamIdFromPostDir(dirName string) (string, error) {
	// these should be post files
	innerFiles, err := os.ReadDir(filepath.Join(t.dirPath, dirName))
	if err != nil {
		return "", errors.Wrap(err, "error reading directory")
	}
	for _, innerFile := range innerFiles {
		content, err := os.ReadFile(filepath.Join(t.dirPath, dirName, innerFile.Name()))
		if err != nil {
			return "", errors.Wrap(err, "Error reading file")
		}

		teamID, err := t.findTeamIDFromPost(content)

		if err != nil {
			return "", errors.Wrapf(err, "Error reading file. %v", innerFile.Name())
		}

		if teamID != "" {
			return teamID, nil
		}
	}
	return "", nil
}

func (t *BulkTransformer) findTeamIDFromPost(content []byte) (string, error) {
	var posts []Post
	err := json.Unmarshal(content, &posts)
	if err != nil {
		return "", errors.Wrap(err, "error unmarshalling json")
	}

	teamID := ""
	for _, post := range posts {
		if post.Team != "" {
			// post.Team is the ID of the team
			teamID = post.Team
			break
		}
	}
	return teamID, nil
}

func channelHasBeenMoved(channel slack.SlackChannel, channelsToMove []ChannelsToMove) bool {
	for _, ch := range channelsToMove {
		if ch.SlackChannel.Id == channel.Id && ch.Moved {
			return true
		}
	}
	return false
}

func moveDirectory(source string, destination string) error {
	err := os.Rename(source, destination)
	if err != nil {
		return err
	}
	return nil
}
