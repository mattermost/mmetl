package commands_test

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"testing"

	"github.com/mattermost/mmetl/commands"
	"github.com/stretchr/testify/require"
)

func TestYourCommandFunction(t *testing.T) {
	defaultChannelsData := `[
		{
			"id": "channel1",
			"name": "general",
			"creator": "user1",
			"members": ["user1", "user2", "user3"],
			"purpose": {"value": "Company wide announcements and work-based matters"},
			"topic": {"value": "Work matters"},
			"type": "O"
		},
		{
			"id": "channel2",
			"name": "random",
			"creator": "user2",
			"members": ["user1", "user2", "user3", "user4"],
			"purpose": {"value": "Non-work related chit-chat"},
			"topic": {"value": "Anything goes!"},
			"type": "O"
		}
	]`

	defaultUsersData := `[
		{
			"id": "user1",
			"name": "JohnDoe",
			"is_bot": false,
			"profile": {
				"real_name": "John Doe",
				"email": "john.doe@example.com",
				"title": "Software Engineer"
			},
			"deleted": false
		},
		{
			"id": "user2",
			"name": "JaneSmith",
			"id_bot": false,
			"profile": {
				"real_name": "Jane Smith",
				"email":  "jane.smith@example.com",
				"title": "Product Manager"
			},
			"deleted": false
		}
	]`

	defaultPostsData := `[
		{
			"user": "user1",
			"text": "Hello, World!",
			"ts": "1577836800.000000",
			"type":      "message",
			"attachments": [
				{
				}
			}
		},
		{
			"user": "user2",
			"text": "Hello, user1!",
			"ts": "1577836801.000000",
			"type": "message",
			"attachments": [
				{
				}
			}
		}
	]`

	for name, tc := range map[string]struct {
		channelsData   string
		usersData      string
		postsData      string
		expectedOutput string
		expectedError  string
	}{
		"valid": {
			channelsData: defaultChannelsData,
			usersData:    defaultUsersData,
			postsData:    defaultPostsData,
			expectedOutput: `{"type":"version","version":1}
{"type":"channel","channel":{"team":"myteam","name":"general","display_name":"general","type":"O","header":"Work matters","purpose":"Company wide announcements and work-based matters"}}
{"type":"channel","channel":{"team":"myteam","name":"random","display_name":"random","type":"O","header":"Anything goes!","purpose":"Non-work related chit-chat"}}
{"type":"user","user":{"username":"JohnDoe","email":"john.doe@example.com","auth_service":null,"nickname":"","first_name":"John","last_name":"Doe","position":"Software Engineer","roles":"system_user","locale":null,"teams":[{"name":"myteam","roles":"team_user","channels":[{"name":"general","roles":"channel_user"},{"name":"random","roles":"channel_user"}]}]}}
{"type":"user","user":{"username":"JaneSmith","email":"jane.smith@example.com","auth_service":null,"nickname":"","first_name":"Jane","last_name":"Smith","position":"Product Manager","roles":"system_user","locale":null,"teams":[{"name":"myteam","roles":"team_user","channels":[{"name":"general","roles":"channel_user"},{"name":"random","roles":"channel_user"}]}]}}
`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			team := "myteam"
			inputFilePath := "test_input.zip"
			outputFilePath := "test_output.txt"
			defer func() {
				os.Remove(inputFilePath)
				os.Remove(outputFilePath)
				os.Remove("transform-slack.log")
			}()

			var err error
			err = createTestZipFile(inputFilePath, tc.channelsData, tc.usersData, tc.postsData)
			require.NoError(t, err)

			args := []string{
				"transform",
				"slack",
				"--team", team,
				"--file", inputFilePath,
				"--output", outputFilePath,
			}

			c := commands.RootCmd
			c.SetArgs(args)
			err = c.Execute()

			if tc.expectedError != "" {
				require.Error(t, err)
				require.Equal(t, tc.expectedError, err.Error())
				return
			}

			require.NoError(t, err)

			_, err = os.Stat(outputFilePath)
			if os.IsNotExist(err) {
				t.Fatalf("output file was not created")
			}

			require.NoError(t, err)

			outputBytes, err := os.ReadFile(outputFilePath)
			require.NoError(t, err, "failed to read output file")

			output := string(outputBytes)

			expectedLines := strings.Split(tc.expectedOutput, "\n")
			actualLines := strings.Split(output, "\n")
			require.Len(t, actualLines, len(expectedLines), "wrong number of lines in tool's output")

			expectedMaps := []map[string]any{}
			actualMaps := []map[string]any{}
			for _, line := range expectedLines {
				if line == "" {
					continue
				}

				expectedMap := map[string]any{}
				err := json.Unmarshal([]byte(line), &expectedMap)
				require.NoError(t, err)

				expectedMaps = append(expectedMaps, expectedMap)

				actualMap := map[string]any{}
				err = json.Unmarshal([]byte(line), &actualMap)
				require.NoError(t, err)

				actualMaps = append(actualMaps, actualMap)
			}

			for _, actual := range actualMaps {
				found := false
				for _, expected := range expectedMaps {
					if reflect.DeepEqual(actual, expected) {
						found = true
					}
				}

				require.True(t, found, "no equal for "+fmt.Sprintf("%v+", actual))
			}
		})
	}
}

func createTestZipFile(inputFilePath, channelsData, usersData, postsData string) error {
	tempDir, err := os.MkdirTemp(os.TempDir(), "")
	defer os.RemoveAll(tempDir)
	if err != nil {
		return err
	}

	err = writeFile(filepath.Join(tempDir, "channels.json"), channelsData)
	if err != nil {
		return err
	}
	err = writeFile(filepath.Join(tempDir, "users.json"), usersData)
	if err != nil {
		return err
	}
	err = writeFile(filepath.Join(tempDir, "posts.json"), postsData)
	if err != nil {
		return err
	}

	err = zipFiles(inputFilePath, tempDir, []string{"channels.json", "users.json", "posts.json"})
	if err != nil {
		return err
	}

	return nil
}

func writeFile(filePath, data string) error {
	file, err := os.Create(filePath)
	if err != nil {
		return err
	}
	defer file.Close()

	_, err = file.WriteString(data)
	if err != nil {
		return err
	}

	return nil
}

func zipFiles(zipFilePath, dir string, files []string) error {
	zipFile, err := os.Create(zipFilePath)
	if err != nil {
		return err
	}
	defer zipFile.Close()

	archive := zip.NewWriter(zipFile)
	defer archive.Close()

	for _, file := range files {
		err = addFileToZip(archive, filepath.Join(dir, file), file)
		if err != nil {
			return err
		}
	}

	return nil
}

func addFileToZip(archive *zip.Writer, filePath, fileName string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return err
	}

	header, err := zip.FileInfoHeader(info)
	if err != nil {
		return err
	}
	header.Name = fileName
	header.Method = zip.Deflate

	writer, err := archive.CreateHeader(header)
	if err != nil {
		return err
	}

	_, err = io.Copy(writer, file)
	if err != nil {
		return err
	}

	return nil
}
