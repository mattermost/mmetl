package slack

import (
	"archive/zip"
	"errors"
	"fmt"
	"strings"
)

func (t *Transformer) CheckForRequiredFile(zipReader *zip.Reader, fileName string) error {
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
			err := fmt.Errorf("failed to find required file %s in the correct location, but might have found it in a subdirectory", fileName)
			t.Logger.Error(err)
			return err
		}
		err := fmt.Errorf("failed to find required file %s in the correct location", fileName)
		t.Logger.Error(err)
		return err
	}

	return nil
}

func (t *Transformer) Precheck(zipReader *zip.Reader) error {
	requiredFiles := []string{
		"channels.json",
		"integration_logs.json",
	}

	var errs []error
	for _, fileName := range requiredFiles {
		if err := t.CheckForRequiredFile(zipReader, fileName); err != nil {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}
