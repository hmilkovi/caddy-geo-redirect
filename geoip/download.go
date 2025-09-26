package geoip

import (
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// downloadGeoDB fetches geo ip database and saves it on given filepath
func downloadGeoDB(uri string, filepath string) error {
	client := &http.Client{
		Timeout: 30 * time.Second,
	}

	resp, err := client.Get(uri)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("bad status: %s", resp.Status)
	}

	var reader io.ReadCloser
	if strings.Contains(resp.Header.Get("Content-Encoding"), "gzip") {
		reader, err = gzip.NewReader(resp.Body)
		if err != nil {
			return err
		}
		defer reader.Close()
	} else {
		reader = resp.Body
	}

	out, err := os.Create(filepath)
	if err != nil {
		return fmt.Errorf("failed to create or truncate file: %w", err)
	}
	defer out.Close()

	_, err = io.Copy(out, reader)
	if err != nil {
		return fmt.Errorf("failed to write to file: %w", err)
	}

	return nil
}
