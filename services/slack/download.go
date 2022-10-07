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

var ErrOverlapNotEqual = errors.New("the downloaded file doesn't match the one on disk")

func downloadInto(name, url string, size int64) error {
	file, err := os.OpenFile(name, os.O_RDWR|os.O_CREATE, 0660)
	if err != nil {
		return err
	}
	defer file.Close()

	return resumeDownload(file, size, url)
}

func resumeDownload(existing *os.File, size int64, downloadURL string) error {
	existingSize, overlap, err := calculateSize(existing, size)
	if err != nil || existingSize == size {
		return err
	}

	start := existingSize - overlap
	req, err := createRequest(downloadURL, start)
	if err != nil {
		return err
	}

	if start != 0 {
		log.Printf("Resuming download from %s\n", humanSize(start))
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusPartialContent:
		// do nothing, everything is fine
	case http.StatusOK:
		// server doesn't support Range
		overlap = 0
		if err = existing.Truncate(0); err != nil {
			return err
		}
	default:
		return fmt.Errorf("download failed with status %q", resp.Status)
	}

	if overlap != 0 {
		err = checkOverlap(existing, resp.Body, overlap)
		if err != nil {
			return err
		}
	}

	_, err = existing.Seek(0, io.SeekEnd)
	if err != nil {
		return err
	}

	_, err = io.Copy(existing, resp.Body)
	return err
}

func checkOverlap(existing io.ReadSeeker, download io.Reader, overlap int64) error {
	bufW := make([]byte, overlap)
	bufL := make([]byte, overlap)

	_, err := io.ReadFull(download, bufW)
	if err != nil {
		return err
	}

	_, err = existing.Seek(-overlap, io.SeekEnd)
	if err != nil {
		return err
	}

	_, err = io.ReadFull(existing, bufL)
	if err != nil {
		return err
	}

	if !bytes.Equal(bufW, bufL) {
		return ErrOverlapNotEqual
	}

	return nil
}

func calculateSize(existing *os.File, size int64) (existingSize, overlap int64, err error) {
	info, err := existing.Stat()
	if err != nil {
		return 0, 0, err
	}

	existingSize = info.Size()
	if existingSize == size {
		return existingSize, 0, nil
	}
	if existingSize > size {
		err = existing.Truncate(0)
		if err != nil {
			return 0, 0, err
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
		return nil, err
	}

	req.Header.Set("User-Agent", "mmetl/1.0")
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-", start))

	return req, nil
}