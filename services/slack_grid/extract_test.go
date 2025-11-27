package slack_grid

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"

	"github.com/sirupsen/logrus"
)

func TestExtractDirectory(t *testing.T) {
	dir := createTestDir(t)

	defer os.RemoveAll(dir)
	// Create a new GridTransformer
	tf := NewGridTransformer(
		logrus.New(),
	)

	tf.dirPath = dir

	// Create a new zip file for testing
	zipFile, err := os.Create(filepath.Join(tf.dirPath, "test.zip"))
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(zipFile.Name())

	zipWriter := zip.NewWriter(zipFile)
	_, err = zipWriter.Create("test.txt")
	if err != nil {
		t.Fatal(err)
	}
	zipWriter.Close()

	// Open the zip file
	reader, err := zip.OpenReader(zipFile.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()

	// Extract the directory
	err = tf.ExtractDirectory(&reader.Reader)
	if err != nil {
		t.Fatal(err)
	}

	// Check that the directory exists
	_, err = os.Stat(filepath.Join(tf.dirPath, "test.txt"))
	if os.IsNotExist(err) {
		t.Fatalf("Extracted file does not exist")
	}
}
