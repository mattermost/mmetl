package slack_bulk

import (
	"archive/zip"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
)

func (t *BulkTransformer) GetWorkingDir() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		t.Logger.Error("Error getting current directory:", err)
		return "", err
	}
	return dir, nil
}

func (t *BulkTransformer) readDir(dest string) ([]fs.DirEntry, error) {

	files, err := os.ReadDir(dest)
	if err != nil {
		t.Logger.Error("Error reading directory:", err)
		return nil, err
	}
	return files, nil
}

func (t *BulkTransformer) dirHasContent(dest string) (bool, error) {

	err := os.MkdirAll(t.dirPath, os.ModePerm)
	if err != nil {
		log.Fatal(err)
		return false, err
	}

	entries, err := os.ReadDir(dest)
	if err != nil {
		return false, err
	}

	if len(entries) > 0 {
		t.Logger.Errorf("directory %s is not empty. Using existing data.", dest)
		return true, nil
	}
	return false, nil
}

func (t *BulkTransformer) ExtractDirectory(zipReader *zip.Reader) error {
	pwd, err := t.GetWorkingDir()

	if err != nil {
		t.Logger.Errorf("Error getting current directory: %v", err)
		return err
	}
	t.dirPath = filepath.Join(pwd, "tmp", "slack_export")
	t.Logger.Infof("Extracting to %s", t.dirPath)

	yes, err := t.dirHasContent(t.dirPath)
	if err != nil {
		return err
	}

	if yes {
		return nil
	}

	for _, f := range zipReader.File {
		fpath := filepath.Join(t.dirPath, f.Name)

		if f.FileInfo().IsDir() {
			// Make Folder
			err := os.MkdirAll(fpath, os.ModePerm)
			if err != nil {
				t.Logger.Errorf("Error creating directory: %v", err)
				return err
			}
			continue
		}

		// Make File
		if err := os.MkdirAll(filepath.Dir(fpath), os.ModePerm); err != nil {
			t.Logger.Errorf("Error creating directory: %v", err)
			return err
		}

		outFile, err := os.OpenFile(fpath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
		if err != nil {
			t.Logger.Errorf("Error creating file: %v", err)
			return err
		}

		rc, err := f.Open()
		if err != nil {
			t.Logger.Errorf("Error opening file: %v", err)
			return err
		}

		_, err = io.Copy(outFile, rc)

		// Close the file without defer to close before next iteration of loop
		outFile.Close()
		rc.Close()

		if err != nil {
			t.Logger.Errorf("Error copying file: %v", err)
			return err
		}
	}

	// Return the full path of the directory
	return nil
}
