package slack_bulk

import (
	"archive/zip"

	"github.com/mattermost/mattermost-server/v6/model"
	slack "github.com/mattermost/mmetl/services/slack"

	// create a team mapping json file and pass it in as an arg
	// read the team mapping file into a map

	// Parse the Slack export file into a set of intermediate files.

	// channels.json
	// dms.json
	// groups.json
	// mpims.json
	// org_users.json

	// to parse channels:
	// go through each channel in the channels.json and find the directory for it at the root level
	// channels.json[number].name = a directory at the root level
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

type BulkTransformer struct {
	*slack.Transformer
}

func NewBulkTransformer(logger log.FieldLogger) *BulkTransformer {
	return &BulkTransformer{
		Transformer: &slack.Transformer{
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
					return err
				}
			case "groups.json":
				slackExport.Groups, err = t.SlackParseChannels(reader, model.ChannelTypePrivate)
				if err != nil {
					return err
				}
			case "dms.json":
				slackExport.DMs, err = t.SlackParseChannels(reader, model.ChannelTypeDirect)
				if err != nil {
					return err
				}
			case "mpims.json":
				slackExport.Mpims, err = t.SlackParseChannels(reader, model.ChannelTypeGroup)
				if err != nil {
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
