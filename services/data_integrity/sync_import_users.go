package data_integrity

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"

	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/v8/channels/app/imports"
)

type SyncImportUsersFlags struct {
	DryRun     bool
	OutputFile string
}

// Take into account long lines in the jsonl file, for posts that may have many large replies
const bufferSize = 1024 * 1024
const scannerSize = 5 * 1024 * 1024

func SyncImportUsers(reader io.Reader, flags SyncImportUsersFlags, client *model.Client4, logger *logrus.Logger) error {
	scanner := bufio.NewScanner(reader)
	buf := make([]byte, 0, bufferSize)
	scanner.Buffer(buf, scannerSize)

	var out *os.File
	var err error

	var writeLine = func(line string) error {
		if out != nil {
			if _, writeErr := out.WriteString(line + "\n"); writeErr != nil {
				return errors.Wrap(writeErr, "Failed to write to output file")
			}
		}

		return nil
	}

	if !flags.DryRun {
		out, err = os.OpenFile(flags.OutputFile, os.O_CREATE|os.O_WRONLY, 0666)
		if err != nil {
			return errors.Wrap(err, "Error opening output file")
		}
		defer out.Close()
	}

	usersChanged := []string{}
	usernameMappings := map[string]string{}

	ctx := context.Background()

	logger.Info("Starting sync process")
	for scanner.Scan() {
		var lineData imports.LineImportData

		line := scanner.Text()
		err := json.Unmarshal([]byte(line), &lineData)
		if err != nil {
			logger.Warnf("Error unmarshalling line, continuing process anyway: %v", err)
			if writeErr := writeLine(line); writeErr != nil {
				return writeErr
			}
		}

		switch lineData.Type {
		case "user":
			user := lineData.User
			oldUsername := *user.Username
			logger.Debugf("Processing user %s", oldUsername)

			usernameChanged, emailChanged, err := mergeImportFileUser(ctx, user, flags, client, logger)
			if err != nil {
				logger.Errorf("Error checking user %s, keeping import record as-is. %v", *user.Username, err)
				break
			}

			if usernameChanged || emailChanged {
				usersChanged = append(usersChanged, *user.Username)
			}

			if usernameChanged {
				usernameMappings[oldUsername] = *user.Username
			}

			removeDuplicateChannelMemberships(user, flags, logger)
		case "post":
			lineData.Post = processPost(lineData.Post, usernameMappings)
		case "direct_post":
			lineData.DirectPost = processDirectPost(lineData.DirectPost, usernameMappings)
		case "channel":
			lineData.Channel = processChannel(lineData.Channel, usernameMappings)
		case "direct_channel":
			lineData.DirectChannel = processDirectChannel(lineData.DirectChannel, usernameMappings)
		}

		lineMarshaled, err := json.Marshal(lineData)
		if err != nil {
			return errors.Wrap(err, "Error marshaling user")
		}

		if writeErr := writeLine(string(lineMarshaled)); writeErr != nil {
			return writeErr
		}
	}

	if err := scanner.Err(); err != nil {
		return err
	}

	if flags.DryRun {
		logger.Info("Exited after reading users from import file, due to not providing --update-users flag")
	}

	logger.Infof("Number of users with changes: %d", len(usersChanged))
	if len(usersChanged) > 0 {
		logger.Infof("Users changed: %s", strings.Join(usersChanged, ", "))
	}

	logger.Info("Finished sync process")

	return nil
}

func removeDuplicateChannelMemberships(user *imports.UserImportData, flags SyncImportUsersFlags, logger *logrus.Logger) {
	names := map[string]bool{}

	if user.Teams == nil || len(*user.Teams) == 0 {
		return
	}
	teams := *user.Teams

	chansOut := []imports.UserChannelImportData{}

	for _, c := range *teams[0].Channels {
		if names[*c.Name] {
			logger.Warnf("Removing duplicate channel membership: user %s channel %s", *user.Username, *c.Name)
		} else {
			names[*c.Name] = true
			chansOut = append(chansOut, c)
		}
	}

	teams[0].Channels = &chansOut
}

func mergeImportFileUser(ctx context.Context, user *imports.UserImportData, flags SyncImportUsersFlags, client *model.Client4, logger *logrus.Logger) (usernameChanged, emailChanged bool, err error) {
	usernameExists := false
	emailExists := false

	emailFromImport := strings.ToLower(*user.Email)
	usernameFromImport := strings.ToLower(*user.Username)

	existingUserByUsername, resp, err := client.GetUserByUsername(ctx, usernameFromImport, "")
	if err != nil {
		if resp == nil {
			return false, false, errors.Wrap(err, "error fetching user by username")
		}

		if resp.StatusCode != 404 {
			return false, false, err
		}

		logger.Debugf("Username %s does not exist in database", usernameFromImport)
	} else {
		usernameExists = true
		logger.Debugf("Username %s exists in database", usernameFromImport)
	}

	existingUserByEmail, resp, err := client.GetUserByEmail(ctx, emailFromImport, "")
	if err != nil {
		if resp == nil {
			return false, false, errors.Wrap(err, "error fetching user by email")
		}

		if resp.StatusCode != 404 {
			return false, false, err
		}

		logger.Debugf("Email %s does not exist in database", emailFromImport)
	} else {
		emailExists = true
		logger.Debugf("Email %s exists in database", emailFromImport)
	}

	if usernameExists && existingUserByUsername.Email != emailFromImport {
		logger.Warnf("Username %s already exists, but has a different email. DB email: (%s) Import file email (%s)", usernameFromImport, existingUserByUsername.Email, emailFromImport)
	}

	if emailExists && existingUserByEmail.Username != usernameFromImport {
		logger.Warnf("Email %s already exists, but has a different username. DB username: (%s) Import file username (%s)", emailFromImport, existingUserByEmail.Username, usernameFromImport)
	}

	avoidUpdatingEmail := false
	avoidUpdatingUsername := false
	if usernameExists && emailExists && existingUserByUsername.Id != existingUserByEmail.Id {
		logger.Warnf("Found duplicate user %s in database. Import file username (%s). DB username 1 (%s). DB username 2 (%s). Import file email (%s). DB email 1 (%s). DB email 2 (%s)",
			usernameFromImport, usernameFromImport, existingUserByUsername.Username, existingUserByEmail.Username, emailFromImport, existingUserByUsername.Email, existingUserByEmail.Email)

		usernameUserActive := existingUserByUsername.DeleteAt == 0
		emailUserActive := existingUserByEmail.DeleteAt == 0
		if usernameUserActive && !emailUserActive {
			avoidUpdatingEmail = false
			avoidUpdatingUsername = true
			logger.Infof("Duplicate user with email (%s) is marked as inactive in the database. Updating email to (%s) from active user with username (%s)", existingUserByEmail.Email, existingUserByUsername.Email, existingUserByUsername.Username)
		} else if !usernameUserActive && emailUserActive {
			avoidUpdatingEmail = true
			avoidUpdatingUsername = false
			logger.Infof("Duplicate user with username (%s) is marked as inactive in the database. Updating username to (%s) from active user with email (%s)", existingUserByUsername.Username, existingUserByEmail.Username, existingUserByEmail.Email)
		} else if usernameUserActive && emailUserActive {
			avoidUpdatingEmail = true
			avoidUpdatingUsername = false
			logger.Warnf("Duplicate user with username (%s) has two users in database marked as active. Updating new user's username from (%s) to (%s)", usernameFromImport, usernameFromImport, existingUserByEmail.Username)
		} else {
			avoidUpdatingEmail = true
			avoidUpdatingUsername = false
			logger.Warnf("Duplicate user with username (%s) has two users in database marked as inactive. Updating new user's username from (%s) to (%s)", usernameFromImport, usernameFromImport, existingUserByEmail.Username)
		}
	}

	emailChanged = false
	if !avoidUpdatingEmail && usernameExists && existingUserByUsername.Email != emailFromImport {
		emailChanged = true
		user.Email = &existingUserByUsername.Email
		if !flags.DryRun {
			logger.Infof("Updating email for user %s from %s to %s", usernameFromImport, emailFromImport, existingUserByUsername.Email)
		} else {
			logger.Infof("Use the `--update-users` flag to update the import file's user record for user %s", usernameFromImport)
		}
	}

	usernameChanged = false
	if !avoidUpdatingUsername && emailExists && existingUserByEmail.Username != usernameFromImport {
		usernameChanged = true
		user.Username = &existingUserByEmail.Username
		if !flags.DryRun {
			logger.Infof("Updating username for user %s from %s to %s", emailFromImport, usernameFromImport, existingUserByEmail.Username)
		} else {
			logger.Infof("Use the `--update-users` flag to update the import file's user record for user %s", usernameFromImport)
		}
	}

	if !emailChanged && !usernameChanged {
		logger.Debugf("Record not changed for user %s", usernameFromImport)
	}

	return usernameChanged, emailChanged, nil

}
