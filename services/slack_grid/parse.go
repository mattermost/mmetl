package slack_grid

import (
	"archive/zip"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/mattermost/mattermost-server/v6/model"
	"github.com/mattermost/mmetl/services/slack"
	"github.com/pkg/errors"

	log "github.com/sirupsen/logrus"
)

type ChannelFiles string

const (
	ChannelFilePublic  ChannelFiles = "channels"
	ChannelFilePrivate ChannelFiles = "groups"
	ChannelFileDM      ChannelFiles = "dms"
	ChannelFileGM      ChannelFiles = "mpims"
)

const (
	ChannelFilePublicWithExt  = string(ChannelFilePublic) + ".json"
	ChannelFilePrivateWithExt = string(ChannelFilePrivate) + ".json"
	ChannelFileDMWithExt      = string(ChannelFileDM) + ".json"
	ChannelFileGMWithExt      = string(ChannelFileGM) + ".json"
)

const ErrFindingTeamID = "Could not find team ID for channel %v, ID: %v"
const ErrFindingTeamName = "Could not find team name for channel %v"

type GridSlackExport struct {
	Public  []slack.SlackChannel
	Private []slack.SlackChannel
	GMs     []slack.SlackChannel
	DMs     []slack.SlackChannel
}

type Post struct {
	*slack.SlackPost
	Team       string `json:"team"`
	UserTeam   string `json:"user_team"`
	SourceTeam string `json:"source_team"`
}

type Channel struct {
	*slack.SlackChannel
	Team string `json:"team"`
}

type GridTransformer struct {
	slack.Transformer
	Teams map[string]string

	// the root path to export slack to for parsing.
	// defaults to ./tmp/slack_grid
	dirPath string
	pwd     string
}

type ChannelsToMove struct {
	SlackChannel slack.SlackChannel
	TeamID       string
	TeamName     string
	Moved        bool

	// Path is the name of the channel or the channel ID for DMs
	Path string
}

func NewGridTransformer(logger log.FieldLogger) *GridTransformer {
	return &GridTransformer{
		Transformer: slack.Transformer{
			Intermediate: &slack.Intermediate{},
			Logger:       logger,
		},
	}
}

func (t *GridTransformer) ParseGridSlackExportFile(zipReader *zip.Reader) (*GridSlackExport, error) {
	slackExport := GridSlackExport{}

	// only finding the root information here and storing it.
	for i, file := range zipReader.File {
		if file.FileInfo().IsDir() || strings.Contains(file.Name, "/") {
			continue
		}
		err := func(i int, file *zip.File) error {
			reader, err := file.Open()
			if err != nil {
				return errors.Wrap(err, "Error opening file for parsing.")
			}
			defer reader.Close()

			switch file.Name {
			case ChannelFilePublicWithExt:
				slackExport.Public, err = t.parseChannel(file, model.ChannelTypeOpen, "error parsing channels.json")
			case ChannelFilePrivateWithExt:
				slackExport.Private, err = t.parseChannel(file, model.ChannelTypePrivate, "error parsing groups.json")
			case ChannelFileDMWithExt:
				slackExport.DMs, err = t.parseChannel(file, model.ChannelTypeDirect, "error parsing dms.json")
			case ChannelFileGMWithExt:
				slackExport.GMs, err = t.parseChannel(file, model.ChannelTypeGroup, "error parsing mpims.json")
			default:
			}

			if err != nil {
				return err
			}
			return nil
		}(i, file)

		if err != nil {
			return nil, err
		}
	}
	return &slackExport, nil
}

// The primary function here that is responsible for transforming the data. It accepts a Slack channel, which can
// be of GM / DM / Private / Public. It then finds the team ID for that channel and moves it to the correct team directory.
// Any channels that do not have a valid mapping in the teams.json file or no team ID found are skipped.

func (t *GridTransformer) HandleMovingChannels(slackChannels []slack.SlackChannel, channelType ChannelFiles) error {
	t.Logger.Infof("Unzipped slack export path being used: %v", t.dirPath)

	itemsInDir, err := t.readDir(t.dirPath)
	t.Logger.Debugf("Found %v items in directory.", len(itemsInDir))

	if err != nil {
		return errors.Wrap(err, "error reading directory")
	}

	totalChannels := len(slackChannels)
	t.Logger.Debugf("Found %v %v. Looking for team IDs. \n", totalChannels, channelType)

	channelsToMove, err := t.getChannelsToMove(slackChannels, itemsInDir, channelType)
	if err != nil {
		return err
	}

	return t.moveChannels(channelsToMove, channelType)
}

// getChannelsToMove returns a slice of ChannelsToMove for the given slackChannels and itemsInDir.
// It uses the channelType to determine the path name for each channel.
func (t *GridTransformer) getChannelsToMove(slackChannels []slack.SlackChannel, itemsInDir []fs.DirEntry, channelType ChannelFiles) ([]ChannelsToMove, error) {
	var channelsToMove []ChannelsToMove

	for _, channel := range slackChannels {
		if channelHasBeenMoved(channel, channelsToMove) {
			continue
		}

		teamID, err := t.findTeamIDForChannel(channel, itemsInDir, channelType)
		if err != nil {
			t.Logger.Errorf("error finding team ID for channel: %v", err.Error())
			continue
		}

		if teamID == "" {
			t.Logger.Errorf(ErrFindingTeamID, channel.Name, channel.Id)
			continue
		}

		moveChannel, err := t.createMoveChannel(channel, teamID, channelType)
		if err != nil {
			t.Logger.Errorf("error creating move channel: %v", err.Error())
			continue
		}

		t.Logger.Debugf("Found channel to move.  %v", moveChannel)
		channelsToMove = append(channelsToMove, moveChannel)
	}

	return channelsToMove, nil
}

// Looks in the root directory for the channel directory name.
// If it finds it, calls findTeamIDFromChannelDir to get the team ID.
func (t *GridTransformer) findTeamIDForChannel(channel slack.SlackChannel, itemsInDir []fs.DirEntry, channelType ChannelFiles) (string, error) {
	channelDirName := getChannelDirName(channel, channelType)
	for _, item := range itemsInDir {
		if strings.HasPrefix(item.Name(), channelDirName) && item.Type().IsDir() {
			return t.findTeamIdFromChannelDir(item.Name())
		}
	}
	return "", errors.Errorf(ErrFindingTeamID, channel.Name, channel.Id)
}

func (t *GridTransformer) createMoveChannel(channel slack.SlackChannel, teamID string, channelType ChannelFiles) (ChannelsToMove, error) {
	teamName := t.Teams[teamID]
	if len(teamName) == 0 {
		return ChannelsToMove{}, errors.Errorf(ErrFindingTeamName, channel.Name)
	}

	channelDirName := getChannelDirName(channel, channelType)

	return ChannelsToMove{
		SlackChannel: channel,
		TeamID:       teamID,
		TeamName:     teamName,
		Path:         channelDirName,
	}, nil
}

func (t *GridTransformer) parseChannel(file *zip.File, channelType model.ChannelType, errorMessage string) ([]slack.SlackChannel, error) {
	reader, err := file.Open()
	if err != nil {
		return nil, errors.Wrap(err, "Error opening file for parsing.")
	}
	defer reader.Close()

	channels, err := t.SlackParseChannels(reader, channelType)
	if err != nil {
		return nil, errors.Wrap(err, errorMessage)
	}
	return channels, nil
}

func (t *GridTransformer) moveChannels(channelsToMove []ChannelsToMove, channelType ChannelFiles) error {
	totalChannels := len(channelsToMove)
	t.Logger.Infof("Moving %v channels. \n", totalChannels)

	for i, channel := range channelsToMove {
		if totalChannels > 100 && i%100 == 0 {
			t.Logger.Infof("Performing %v of %v %v moves \n", i+100, totalChannels, channelType)
		}

		err := t.performChannelMove(channelType, channel, channelsToMove, i)
		if err != nil {
			return err
		}
	}

	t.Logger.Infof("Moved %v %v \n", totalChannels, channelType)
	return nil
}

// DMs use the channelID as their channel path in the root dir.
// Every other channel type so far uses the channel name as the channel path in the root dir.
func getChannelDirName(channel slack.SlackChannel, channelType ChannelFiles) string {
	if channelType == ChannelFileDM {
		return channel.Id
	}
	return channel.Name
}

func (t *GridTransformer) performChannelMove(channelType ChannelFiles, channel ChannelsToMove, channelsToMove []ChannelsToMove, channelIndex int) error {

	if len(channel.TeamName) == 0 {
		return errors.Errorf(ErrFindingTeamName, channel.Path)
	}

	t.Logger.Debugf(
		"Moving channel %v to team %v. channel ID: %v, team ID: %v",
		channel.Path,
		channel.TeamName,
		channel.TeamID,
		channel.SlackChannel.Id,
	)

	currentDir := filepath.Join(t.dirPath, channel.Path)
	newDir := filepath.Join(t.dirPath, "teams", channel.TeamName, channel.Path)

	err := moveDirectory(currentDir, newDir)

	if err != nil {
		return errors.Wrapf(err, "error moving channel %v to team %v", channel.Path, channel.TeamName)
	}

	// append this to the channels file in the team directory
	channelsToMove[channelIndex].Moved = true

	err = t.appendChannelToTeamChannelsFile(channelType, channel)
	if err != nil {
		return errors.Wrapf(err, "error appending channel %v to team %v", channel.Path, channel.TeamName)
	}
	return nil
}

// this finds the correct json file in the directory and appends the channel information to it.
func (t *GridTransformer) appendChannelToTeamChannelsFile(channelType ChannelFiles, channel ChannelsToMove) error {

	path := filepath.Join(t.dirPath, "teams", channel.TeamName, string(channelType)+".json")

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

// Loops over the entire directory provided to find the post[number].Team value.
// This returns the FIRST value it finds.
func (t *GridTransformer) findTeamIdFromChannelDir(channelDirName string) (string, error) {
	// these should be post files
	postFiles, err := os.ReadDir(filepath.Join(t.dirPath, channelDirName))
	if err != nil {
		return "", errors.Wrap(err, "error reading directory")
	}

	channelPath := filepath.Join(t.dirPath, channelDirName)
	for _, postFile := range postFiles {
		posts, err := os.ReadFile(filepath.Join(channelPath, postFile.Name()))
		if err != nil {
			return "", errors.Wrap(err, "Error reading file")
		}

		teamID, err := t.findTeamIDFromPostArray(posts)

		if err != nil {
			return "", err
		}

		if teamID != "" {
			return teamID, nil
		}
	}
	return "", errors.New("No team ID found")
}

// Simply looks through the post file and finds a Post[number].Team value. If it exists, it returns it.
func (t *GridTransformer) findTeamIDFromPostArray(content []byte) (string, error) {
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

// Simple check to see if the channel has already been moved based on it's channelID and moved status.
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
