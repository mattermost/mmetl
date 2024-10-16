package slack

import (
	"archive/zip"
	"fmt"
	"strings"
)

func (t *Transformer) checkForRequiredFile(zipReader *zip.Reader, fileName string) bool {
	found := false
	foundInSubdirectory := false

	for _, file := range zipReader.File {
		if file.Name == fileName {
			found = true
		} else if strings.HasSuffix(file.Name, "/"+fileName) {
			foundInSubdirectory = true
		}
	}

	if !found {
		if foundInSubdirectory {
			t.Logger.Errorf("Failed to find required file %s in the correct location, but might have found it in a subdirectory.", fileName)
		} else {
			t.Logger.Errorf("Failed to find required file %s in the correct location.", fileName)
		}

		return false
	}

	return true
}

func (t *Transformer) Precheck(zipReader *zip.Reader) bool {
	requiredFiles := []string{
		"channels.json",
		"integration_logs.json",
	}

	valid := true

	for _, fileName := range requiredFiles {
		fileExists := t.checkForRequiredFile(zipReader, fileName)

		valid = valid && fileExists
	}

	return valid
}

func CheckAuthService(authService string) error {
	if !(authService == "" || authService == "gitlab" || authService == "ldap" ||
		authService == "saml" || authService == "google" || authService == "office365") {
		return fmt.Errorf("Auth serivece must be one of gitlab, ldap, saml, google, office365")
	}
	return nil
}
