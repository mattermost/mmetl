package slack_grid

import (
	"archive/zip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/pkg/errors"
)

func (t *BulkTransformer) GetWorkingDir() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", errors.Wrap(err, "error getting current working directory")
	}
	return dir, nil
}

func (t *BulkTransformer) readDir(dest string) ([]fs.DirEntry, error) {
	files, err := os.ReadDir(dest)
	if err != nil {
		return nil, errors.Wrap(err, fmt.Sprintf("error reading directory %v", dest))
	}
	return files, nil
}

func (t *BulkTransformer) dirHasContent(dest string) (bool, error) {

	err := os.MkdirAll(t.dirPath, os.ModePerm)
	if err != nil {
		return false, errors.Wrap(err, "error creating directory")
	}

	entries, err := os.ReadDir(dest)
	if err != nil {
		return false, errors.Wrap(err, "error reading directory")
	}

	if len(entries) > 0 {
		t.Logger.Errorf("directory %s is not empty. Using existing data.", dest)
		return true, nil
	}
	return false, nil
}

func (t *BulkTransformer) ExtractDirectory(zipReader *zip.Reader) error {
	fmt.Println("Extracting files...")
	pwd, err := t.GetWorkingDir()

	if err != nil {
		return errors.Errorf("error getting current working directory: %v", err)
	}
	t.dirPath = filepath.Join(pwd, "tmp", "slack_export")
	t.Logger.Infof("Extracting to %s", t.dirPath)

	yes, err := t.dirHasContent(t.dirPath)
	if err != nil {
		return errors.Errorf("error seeing if directory has content already. %v", err)
	}

	if yes {
		t.Logger.Infof("content exists in the directory %s. Skipping extraction.", t.dirPath)
		return nil
	}

	totalFiles := len(zipReader.File)

	for i, f := range zipReader.File {
		fpath := filepath.Join(t.dirPath, f.Name)

		if f.FileInfo().IsDir() {
			// Make Folder
			err := os.MkdirAll(fpath, os.ModePerm)
			if err != nil {
				return errors.Wrap(err, "error creating directory")
			}
			continue
		}

		// Make File
		if err := os.MkdirAll(filepath.Dir(fpath), os.ModePerm); err != nil {
			return errors.Wrap(err, "error creating directory")
		}

		outFile, err := os.OpenFile(fpath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
		if err != nil {
			return errors.Wrap(err, "error creating files")
		}

		rc, err := f.Open()
		if err != nil {
			outFile.Close()
			return errors.Wrap(err, "error opening files")
		}

		_, err = io.Copy(outFile, rc)

		// Close the file without defer to close before next iteration of loop
		outFile.Close()
		rc.Close()
		if i%1000 == 0 || i == totalFiles-1 {
			fmt.Printf("Extracting file %d of %d \n", i+1, totalFiles)
		}
		if err != nil {
			return errors.Wrap(err, "error copying files")
		}
	}
	fmt.Print("Finished extracting files \n")

	return nil
}
