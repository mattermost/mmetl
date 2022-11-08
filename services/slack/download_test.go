package slack

import (
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

var mockData []byte

func TestDownload(t *testing.T) {
	// set up the test
	initializeMockData()
	srv, old := mockDefaultHTTPClient()
	defer func() {
		srv.Close()
		http.DefaultClient = old
	}()

	// run the idividual tests
	t.Run("successful download", func(t *testing.T) {
		fileName := filepath.Join(os.TempDir(), "download-test")
		defer os.Remove(fileName)

		require.NoError(t, downloadInto(fileName, srv.URL+"/no_resume", int64(len(mockData))))
		tempFile, _ := os.ReadFile(fileName)
		require.Equal(t, mockData, tempFile)
	})

	t.Run("successful resume, empty file", func(t *testing.T) {
		fileName := filepath.Join(os.TempDir(), "download-test")
		defer os.Remove(fileName)
		require.NoError(t, os.WriteFile(fileName, []byte{}, 0660))

		require.NoError(t, downloadInto(fileName, srv.URL+"/resume", int64(len(mockData))))
		tempFile, _ := os.ReadFile(fileName)
		require.Equal(t, mockData, tempFile)
	})

	t.Run("successful resume, tiny file", func(t *testing.T) {
		fileName := filepath.Join(os.TempDir(), "download-test")
		defer os.Remove(fileName)
		require.NoError(t, os.WriteFile(fileName, mockData[:8], 0660))

		require.NoError(t, downloadInto(fileName, srv.URL+"/resume", int64(len(mockData))))
		tempFile, _ := os.ReadFile(fileName)
		require.Equal(t, mockData, tempFile)
	})

	t.Run("successful resume, half file", func(t *testing.T) {
		fileName := filepath.Join(os.TempDir(), "download-test")
		defer os.Remove(fileName)
		require.NoError(t, os.WriteFile(fileName, mockData[:1024*512], 0660))

		require.NoError(t, downloadInto(fileName, srv.URL+"/resume", int64(len(mockData))))
		tempFile, _ := os.ReadFile(fileName)
		require.Equal(t, mockData, tempFile)
	})

	t.Run("successful resume, full file", func(t *testing.T) {
		fileName := filepath.Join(os.TempDir(), "download-test")
		defer os.Remove(fileName)
		require.NoError(t, os.WriteFile(fileName, mockData, 0660))

		require.NoError(t, downloadInto(fileName, srv.URL+"/resume", int64(len(mockData))))
		tempFile, _ := os.ReadFile(fileName)
		require.Equal(t, mockData, tempFile)
	})

	t.Run("successful re-download, empty file", func(t *testing.T) {
		fileName := filepath.Join(os.TempDir(), "download-test")
		defer os.Remove(fileName)
		require.NoError(t, os.WriteFile(fileName, []byte{}, 0660))

		require.NoError(t, downloadInto(fileName, srv.URL+"/no_resume", int64(len(mockData))))
		tempFile, _ := os.ReadFile(fileName)
		require.Equal(t, mockData, tempFile)
	})

	t.Run("successful re-download, tiny file", func(t *testing.T) {
		fileName := filepath.Join(os.TempDir(), "download-test")
		defer os.Remove(fileName)
		require.NoError(t, os.WriteFile(fileName, mockData[:8], 0660))

		require.NoError(t, downloadInto(fileName, srv.URL+"/no_resume", int64(len(mockData))))
		tempFile, _ := os.ReadFile(fileName)
		require.Equal(t, mockData, tempFile)
	})

	t.Run("successful re-download, half file", func(t *testing.T) {
		fileName := filepath.Join(os.TempDir(), "download-test")
		defer os.Remove(fileName)
		require.NoError(t, os.WriteFile(fileName, mockData[:1024*512], 0660))

		require.NoError(t, downloadInto(fileName, srv.URL+"/no_resume", int64(len(mockData))))
		tempFile, _ := os.ReadFile(fileName)
		require.Equal(t, mockData, tempFile)
	})

	t.Run("successful re-download, full file", func(t *testing.T) {
		fileName := filepath.Join(os.TempDir(), "download-test")
		defer os.Remove(fileName)
		require.NoError(t, os.WriteFile(fileName, mockData, 0660))

		require.NoError(t, downloadInto(fileName, srv.URL+"/no_resume", int64(len(mockData))))
		tempFile, _ := os.ReadFile(fileName)
		require.Equal(t, mockData, tempFile)
	})

	t.Run("unsuccessful resume, tiny file", func(t *testing.T) {
		fileName := filepath.Join(os.TempDir(), "download-test")
		defer os.Remove(fileName)
		require.NoError(t, os.WriteFile(fileName, mockData[:8], 0660))

		require.Error(t, downloadInto(fileName, srv.URL+"/wrong_resume", int64(len(mockData))))
	})

	t.Run("unsuccessful resume, half file", func(t *testing.T) {
		fileName := filepath.Join(os.TempDir(), "download-test")
		defer os.Remove(fileName)
		require.NoError(t, os.WriteFile(fileName, mockData[:1024*512], 0660))

		require.Error(t, downloadInto(fileName, srv.URL+"/wrong_resume", int64(len(mockData))))
	})

	t.Run("successful resume from wrong file with an already downloaded file", func(t *testing.T) {
		fileName := filepath.Join(os.TempDir(), "download-test")
		defer os.Remove(fileName)
		require.NoError(t, os.WriteFile(fileName, mockData, 0660))

		require.NoError(t, downloadInto(fileName, srv.URL+"/wrong_resume", int64(len(mockData))))
		tempFile, _ := os.ReadFile(fileName)
		require.Equal(t, mockData, tempFile)
	})

	t.Run("unknown file", func(t *testing.T) {
		fileName := filepath.Join(os.TempDir(), "download-test")
		defer os.Remove(fileName)
		require.NoError(t, os.WriteFile(fileName, mockData[:1024*512], 0660))

		require.Error(t, downloadInto(fileName, srv.URL+"/wrong_path", int64(len(mockData))))
	})
}

func mockDefaultHTTPClient() (newServer *httptest.Server, oldClient *http.Client) {
	mux := http.NewServeMux()

	mux.HandleFunc("/no_resume", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(mockData)
	})

	mux.HandleFunc("/resume", func(w http.ResponseWriter, r *http.Request) {
		rangeHeader := r.Header.Get("Range")
		if rangeHeader == "" {
			_, _ = w.Write(mockData)
			return
		}

		from, _ := strconv.ParseInt(strings.TrimPrefix(strings.TrimRight(rangeHeader, "-"), "bytes="), 10, 64)

		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(mockData[from:])
	})

	mux.HandleFunc("/wrong_resume", func(w http.ResponseWriter, r *http.Request) {
		wrongData := make([]byte, 1024*1024)
		rand.Read(wrongData) // read different "random" data

		rangeHeader := r.Header.Get("Range")
		if rangeHeader == "" {
			_, _ = w.Write(wrongData)
			return
		}

		from, _ := strconv.ParseInt(strings.TrimPrefix(strings.TrimRight(rangeHeader, "-"), "bytes="), 10, 64)

		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(wrongData[from:])
	})

	newServer = httptest.NewServer(mux)
	oldClient = http.DefaultClient
	http.DefaultClient = newServer.Client()

	return
}

func initializeMockData() {
	mockData = make([]byte, 1024*1024) // 1 MiB of "random" data
	rand.Read(mockData)
}
