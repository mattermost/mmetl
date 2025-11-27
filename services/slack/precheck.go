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
		fileExists := t.CheckForRequiredFile(zipReader, fileName)

		valid = valid && fileExists
	}

	return valid
}
