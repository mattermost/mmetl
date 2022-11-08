package slack

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
)

const defaultOverlap int64 = 512

var ErrOverlapNotEqual = errors.New("download: the downloaded file doesn't match the one on disk")

// downloadInto downloads the contents of a URL into a file. If the file already exists it
// will resume the download. To prevent corrupting the files it downloads a tiny bit of
// overlapping data (512 byte) and compares it to the existing file:
//
//	[-----existing local file-----]
//	                      [-------resumed download-------]
//	                      [overlap]
//
// When the check fails, the function returns an error and doesn't silently re-download
// the whole file. If the server doesn't support resumable downloads, the existing file will
// be truncated and re-downloaded.
func downloadInto(filename, url string, size int64) error {
	file, err := os.OpenFile(filename, os.O_RDWR|os.O_CREATE, 0660)
	if err != nil {
		return fmt.Errorf("download: error opening the destination file: %w", err)
	}
	defer file.Close()

	return resumeDownload(file, size, url)
}

func resumeDownload(existing *os.File, size int64, downloadURL string) error {
	existingSize, overlap, err := calculateSize(existing, size)
	if err != nil {
		return err
	}
	if existingSize == size {
		// the file has already been downloaded
		return nil
	}

	start := existingSize - overlap // calculateSize makes sure this can't be negative
	req, err := createRequest(downloadURL, start)
	if err != nil {
		return err
	}

	if start != 0 {
		log.Printf("Resuming download from %s\n", humanSize(start))
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("download: error during HTTP request: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusPartialContent:
		// do nothing, everything is fine
	case http.StatusOK:
		// server doesn't support Range
		overlap = 0
		if err = existing.Truncate(0); err != nil {
			return fmt.Errorf("download: error emptying file for re-download: %w", err)
		}
	default:
		return fmt.Errorf("download: HTTP request failed with status %q", resp.Status)
	}

	if overlap != 0 {
		err = checkOverlap(existing, resp.Body, overlap)
		if err != nil {
			return err
		}
	}

	_, err = existing.Seek(0, io.SeekEnd)
	if err != nil {
		return fmt.Errorf("download: error seeking to the end of the existing file: %w", err)
	}

	_, err = io.Copy(existing, resp.Body)
	return fmt.Errorf("download: error during download: %w", err)
}

func checkOverlap(existing io.ReadSeeker, download io.Reader, overlap int64) error {
	bufW := make([]byte, overlap)
	bufL := make([]byte, overlap)

	_, err := io.ReadFull(download, bufW)
	if err != nil {
		return fmt.Errorf("download: error downloading the overlapping data: %w", err)
	}

	_, err = existing.Seek(-overlap, io.SeekEnd)
	if err != nil {
		return fmt.Errorf("download: error seeking to the start of the existing overlap: %w", err)
	}

	_, err = io.ReadFull(existing, bufL)
	if err != nil {
		return fmt.Errorf("download: error reading the local overlapping data: %w", err)
	}

	if !bytes.Equal(bufW, bufL) {
		return ErrOverlapNotEqual
	}

	return nil
}

func calculateSize(existing *os.File, size int64) (existingSize, overlap int64, err error) {
	info, err := existing.Stat()
	if err != nil {
		return 0, 0, fmt.Errorf("download: error reading file info: %w", err)
	}

	existingSize = info.Size()
	if existingSize == size {
		return existingSize, 0, nil
	}
	if existingSize > size {
		err = existing.Truncate(0)
		if err != nil {
			return 0, 0, fmt.Errorf("download: error emptying file: %w", err)
		}
		existingSize = 0
	}

	overlap = defaultOverlap
	if overlap > existingSize {
		overlap = existingSize
	}

	return existingSize, overlap, nil
}

func createRequest(url string, start int64) (*http.Request, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("download: error creating HTTP request: %w", err)
	}

	req.Header.Set("User-Agent", "mmetl/1.0")
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-", start))

	return req, nil
}
