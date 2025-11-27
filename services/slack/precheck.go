package slack

import (
	"archive/zip"
	"strings"
)

func (t *Transformer) CheckForRequiredFile(zipReader *zip.Reader, fileName string) bool {
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
			// Accept files found in subdirectories (e.g., teams/team1/channels.json for multi-workspace exports)
			t.Logger.Debugf("Found required file %s in a subdirectory (multi-workspace export)", fileName)
			return true
		}
		t.Logger.Errorf("Failed to find required file %s", fileName)
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
		fileExists := t.CheckForRequiredFile(zipReader, fileName)

		valid = valid && fileExists
	}

	return valid
}
