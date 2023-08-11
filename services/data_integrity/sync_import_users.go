package data_integrity

import (
	"bufio"
	"encoding/json"
	"io"
	"os"

	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"

	"github.com/mattermost/mattermost-server/v6/app/imports"
	"github.com/mattermost/mattermost-server/v6/model"
)

type SyncImportUsersFlags struct {
	UpdateUsernames bool
	UpdateEmails    bool
	OutputFile      string
}

func SyncImportUsers(reader io.Reader, flags SyncImportUsersFlags, client *model.Client4, logger *logrus.Logger) error {
	scanner := bufio.NewScanner(reader)

	out, err := os.Open(flags.OutputFile)
	if err != nil {
		return errors.Wrap(err, "Error opening output file")
	}
	defer out.Close()

	for scanner.Scan() {
		var lineData imports.LineImportData

		line := scanner.Text()
		err := json.Unmarshal([]byte(line), &lineData)
		if err != nil {
			logger.Warnf("Error unmarshalling line, continuing process anyway: %v", err)
			if _, writeErr := out.WriteString(line + "\n"); writeErr != nil {
				return errors.Wrap(writeErr, "Failed to write to output file")
			}
		}

		if lineData.Type != "user" {
			if _, writeErr := out.WriteString(line + "\n"); writeErr != nil {
				return errors.Wrap(writeErr, "Failed to write to output file")
			}
			continue
		}

		user := lineData.User
		err = mergeImportFileUser(user, flags, client, logger)
		if err != nil {
			logger.Errorf("Error checking user %s, keeping import record as-is. %v", *user.Username, err)
			continue
		}

		userOut, err := json.Marshal(user)
		if err != nil {
			return errors.Wrap(err, "Error marshaling user")
		}

		if _, writeErr := out.Write(append(userOut, '\n')); writeErr != nil {
			return errors.Wrap(writeErr, "Failed to write to output file")
		}
	}

	if err := scanner.Err(); err != nil {
		return err
	}

	return nil
}

func mergeImportFileUser(user *imports.UserImportData, flags SyncImportUsersFlags, client *model.Client4, logger *logrus.Logger) error {
	usernameExists := false
	emailExists := false

	existingUserByUsername, resp, err := client.GetUserByUsername(*user.Username, "")
	if err != nil {
		if resp.StatusCode != 404 {
			return err
		}

		logger.Debugf("Username %s does not exist in database", *user.Username)
	} else {
		usernameExists = true
		logger.Debugf("Username %s exists in database", *user.Username)
	}

	existingUserByEmail, resp, err := client.GetUserByEmail(*user.Email, "")
	if err != nil {
		if resp.StatusCode != 404 {
			return err
		}

		logger.Debugf("Email %s does not exist in database", *user.Email)
	} else {
		emailExists = true
		logger.Debugf("Email %s exists in database", *user.Email)
	}

	if usernameExists && existingUserByUsername.Email != *user.Email {
		logger.Warnf("Username %s already exists, but has a different email. DB email: (%s) Import file email (%s)", *user.Username, existingUserByUsername.Email, *user.Email)
	}

	if emailExists && existingUserByEmail.Username != *user.Username {
		logger.Warnf("Email %s already exists, but has a different username. DB username: (%s) Import file username (%s)", *user.Email, existingUserByEmail.Username, *user.Username)
	}

	if usernameExists && existingUserByUsername.Email != *user.Email {
		if flags.UpdateEmails {
			logger.Infof("Updating email for user %s from %s to %s", *user.Username, *user.Email, existingUserByUsername.Email)
			user.Email = &existingUserByUsername.Email
		}
	}

	if emailExists && existingUserByEmail.Username != *user.Username {
		if flags.UpdateUsernames {
			logger.Infof("Updating username for user %s from %s to %s", *user.Email, *user.Username, existingUserByEmail.Username)
			user.Username = &existingUserByEmail.Username
		}
	}

	return nil

}
