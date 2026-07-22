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

// TestZipTeamDirectories_NoTeamsDir covers the case where no channels were
// ever moved into a team directory, so "teams/" was never created. Prior to
// the fix, this made ZipTeamDirectories fail with "error reading teams
// directory" instead of completing with nothing to zip.
func TestZipTeamDirectories_NoTeamsDir(t *testing.T) {
	dir := createTestDir(t)
	defer os.RemoveAll(dir)

	tf := NewGridTransformer(logrus.New())
	tf.dirPath = dir
	tf.pwd = dir

	err := tf.ZipTeamDirectories()
	if err != nil {
		t.Fatalf("expected no error when teams directory does not exist, got: %v", err)
	}
}
