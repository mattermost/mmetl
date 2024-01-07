package slack_bulk

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"testing"

	"github.com/mattermost/mmetl/services/slack"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
)

func marshalAndWriteToFile(zipWriter *zip.Writer, filename string, data interface{}, t *testing.T) {
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

func TestParseBulkSlackExportFile(t *testing.T) {
	// Create a new logger for testing
	logger := logrus.New()

	// Create a new BulkTransformer
	transformer := NewBulkTransformer(logger)

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

	marshalAndWriteToFile(zipWriter, "mpims.json", mpims, t)
	marshalAndWriteToFile(zipWriter, "dms.json", dms, t)
	marshalAndWriteToFile(zipWriter, "channels.json", channels, t)
	marshalAndWriteToFile(zipWriter, "groups.json", channels, t)

	// Close the zip writer
	err := zipWriter.Close()
	assert.NoError(t, err)

	// Create a new zip reader
	zipReader, err := zip.NewReader(bytes.NewReader(zipData.Bytes()), int64(zipData.Len()))
	assert.NoError(t, err)

	// we do not need a team name here.
	slackTransformer := NewBulkTransformer(logger)

	valid := slackTransformer.GridPreCheck(zipReader)
	if !valid {
		t.Fatal(err)
	}

	// Call the ParseBulkSlackExportFile function
	slackExport, err := transformer.ParseBulkSlackExportFile(zipReader)

	// Check the returned error
	assert.NoError(t, err)

	// Check the returned BulkSlackExport
	// For example, you can check if the number of channels matches the expected number
	assert.Equal(t, 5, len(slackExport.Private))
	assert.Equal(t, 5, len(slackExport.DMs))
	assert.Equal(t, 5, len(slackExport.GMs))
	assert.Equal(t, 5, len(slackExport.Public))
}
