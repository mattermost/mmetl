package testhelper

import "testing"

// WorkDir creates a temporary directory, changes into it for the rest of the
// test, and restores the previous working directory afterwards.
func WorkDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	return dir
}
