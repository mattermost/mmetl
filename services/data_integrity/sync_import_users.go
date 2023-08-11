package data_integrity

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"strings"

	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"

	"github.com/mattermost/mattermost-server/v6/app/imports"
	"github.com/mattermost/mattermost-server/v6/model"
)

type SyncImportUsersFlags struct {
	UpdateUsers bool
	OutputFile  string
}

func SyncImportUsers(reader io.Reader, flags SyncImportUsersFlags, client *model.Client4, logger *logrus.Logger) error {
	scanner := bufio.NewScanner(reader)

	var out *os.File
	var err error

	var write = func(line string) error {
		if out != nil {
			if _, writeErr := out.WriteString(line + "\n"); writeErr != nil {
				return errors.Wrap(writeErr, "Failed to write to output file")
			}
		}

		return nil
	}

	if flags.UpdateUsers {
		out, err = os.OpenFile(flags.OutputFile, os.O_CREATE|os.O_WRONLY, 0666)
		if err != nil {
			return errors.Wrap(err, "Error opening output file")
		}
		defer out.Close()
	}

	foundUser := false

	logger.Info("Starting sync process")
	for scanner.Scan() {
		var lineData imports.LineImportData

		line := scanner.Text()
		err := json.Unmarshal([]byte(line), &lineData)
		if err != nil {
			logger.Warnf("Error unmarshalling line, continuing process anyway: %v", err)
			if writeErr := write(line + "\n"); writeErr != nil {
				return writeErr
			}
		}

		if lineData.Type != "user" {
			if foundUser && !flags.UpdateUsers {
				break
			}

			if writeErr := write(line + "\n"); writeErr != nil {
				return writeErr
			}
			continue
		}

		foundUser = true

		user := lineData.User
		logger.Debugf("Processing user %s", *user.Username)

		err = mergeImportFileUser(user, flags, client, logger)
		if err != nil {
			logger.Errorf("Error checking user %s, keeping import record as-is. %v", *user.Username, err)
			continue
		}

		userOut, err := json.Marshal(user)
		if err != nil {
			return errors.Wrap(err, "Error marshaling user")
		}

		if writeErr := write(string(userOut) + "\n"); writeErr != nil {
			return writeErr
		}
	}

	if err := scanner.Err(); err != nil {
		return err
	}

	if !flags.UpdateUsers {
		logger.Info("Exited after reading users from import file, due to not providing --update-users flag")
	}

	logger.Info("Finished sync process")

	return nil
}

func mergeImportFileUser(user *imports.UserImportData, flags SyncImportUsersFlags, client *model.Client4, logger *logrus.Logger) error {
	usernameExists := false
	emailExists := false

	emailFromImport := strings.ToLower(*user.Email)
	usernameFromImport := strings.ToLower(*user.Username)

	existingUserByUsername, resp, err := client.GetUserByUsername(usernameFromImport, "")
	if err != nil {
		if resp.StatusCode != 404 {
			return err
		}

		logger.Debugf("Username %s does not exist in database", usernameFromImport)
	} else {
		usernameExists = true
		logger.Debugf("Username %s exists in database", usernameFromImport)
	}

	existingUserByEmail, resp, err := client.GetUserByEmail(emailFromImport, "")
	if err != nil {
		if resp.StatusCode != 404 {
			return err
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

	recordChanged := false
	if usernameExists && existingUserByUsername.Email != emailFromImport {
		if flags.UpdateUsers {
			logger.Infof("Updating email for user %s from %s to %s", usernameFromImport, emailFromImport, existingUserByUsername.Email)
			user.Email = &existingUserByUsername.Email
			recordChanged = true
		} else {
			logger.Infof("Use the `--update-users` flag to update the import file's user record for user %s", usernameFromImport)
		}
	}

	if emailExists && existingUserByEmail.Username != usernameFromImport {
		if flags.UpdateUsers {
			logger.Infof("Updating username for user %s from %s to %s", emailFromImport, usernameFromImport, existingUserByEmail.Username)
			user.Username = &existingUserByEmail.Username
			recordChanged = true
		} else {
			logger.Infof("Use the `--update-users` flag to update the import file's user record for user %s", usernameFromImport)
		}
	}

	if !recordChanged {
		logger.Debugf("Record not changed for user %s", usernameFromImport)
	}

	return nil

}
