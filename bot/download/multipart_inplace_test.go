package download

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/liuran001/MusicBot-Go/bot/platform"
)

// rangeServer serves data over Range requests, optionally failing one part so
// the error path can be exercised.
func rangeServer(t *testing.T, data []byte, failPartAtOffset int64) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.Header().Set("Accept-Ranges", "bytes")
			w.Header().Set("Content-Length", strconv.Itoa(len(data)))
			w.WriteHeader(http.StatusOK)
			return
		}
		rangeHeader := r.Header.Get("Range")
		if rangeHeader == "" {
			w.Header().Set("Content-Length", strconv.Itoa(len(data)))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(data)
			return
		}
		var start, end int64
		if _, err := fmt.Sscanf(rangeHeader, "bytes=%d-%d", &start, &end); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if failPartAtOffset >= 0 && start == failPartAtOffset {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(data)))
		w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(data[start : end+1])
	}))
}

func patternedData(n int) []byte {
	data := make([]byte, n)
	for i := range data {
		// A byte pattern with a long period, so a part written at the wrong
		// offset cannot coincidentally match.
		data[i] = byte((i*31 + i/251) % 251)
	}
	return data
}

// TestMultipartWritesPartsInPlace is the core guarantee of writing parts
// straight into the destination: many workers pwrite into one file
// concurrently, and the assembled bytes must match the source exactly. An
// offset mistake shows up here as corruption rather than as a short file.
func TestMultipartWritesPartsInPlace(t *testing.T) {
	data := patternedData(6 * 1024 * 1024)
	server := rangeServer(t, data, -1)
	defer server.Close()

	dest := filepath.Join(t.TempDir(), "track.flac")
	md := NewMultipartDownloader(server.Client(), 30*time.Second, MultipartDownloadOptions{
		Concurrency: 4, MinSize: 1024 * 1024, PartSize: 1024 * 1024,
	})

	written, err := md.Download(context.Background(), server.URL,
		&platform.DownloadInfo{URL: server.URL, Size: int64(len(data))}, dest, nil)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if written != int64(len(data)) {
		t.Fatalf("written = %d, want %d", written, len(data))
	}

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read destination: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("destination content differs from source (got %d bytes, want %d)", len(got), len(data))
	}
}

// TestMultipartLeavesNoPartsDirectory pins the removal of the staging step:
// nothing may be created beside the destination.
func TestMultipartLeavesNoPartsDirectory(t *testing.T) {
	data := patternedData(4 * 1024 * 1024)
	server := rangeServer(t, data, -1)
	defer server.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "track.flac")
	md := NewMultipartDownloader(server.Client(), 30*time.Second, MultipartDownloadOptions{
		Concurrency: 3, MinSize: 1024 * 1024, PartSize: 1024 * 1024,
	})

	if _, err := md.Download(context.Background(), server.URL,
		&platform.DownloadInfo{URL: server.URL, Size: int64(len(data))}, dest, nil); err != nil {
		t.Fatalf("Download: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, entry := range entries {
		if entry.Name() == "track.flac" {
			continue
		}
		t.Errorf("unexpected leftover beside the destination: %q (dir=%v)", entry.Name(), entry.IsDir())
	}
	if strings.Contains(strings.Join(entryNames(entries), " "), ".parts") {
		t.Error("a .parts staging directory was left behind")
	}
}

// TestMultipartFailureLeavesNoSparseCarcass covers the risk introduced by
// preallocating: a failed download must not leave a full-size hole-filled file
// that a caller could mistake for a complete track.
func TestMultipartFailureLeavesNoSparseCarcass(t *testing.T) {
	data := patternedData(6 * 1024 * 1024)
	// Fail the part starting at 2MiB, after the file has been preallocated.
	server := rangeServer(t, data, 2*1024*1024)
	defer server.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "track.flac")
	md := NewMultipartDownloader(server.Client(), 30*time.Second, MultipartDownloadOptions{
		Concurrency: 2, MinSize: 1024 * 1024, PartSize: 1024 * 1024,
	})

	if _, err := md.Download(context.Background(), server.URL,
		&platform.DownloadInfo{URL: server.URL, Size: int64(len(data))}, dest, nil); err == nil {
		t.Fatal("Download succeeded despite a failing part")
	}

	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		info, statErr := os.Stat(dest)
		size := int64(-1)
		if statErr == nil {
			size = info.Size()
		}
		t.Fatalf("failed download left %q behind (size=%d, err=%v)", dest, size, err)
	}
}

func entryNames(entries []os.DirEntry) []string {
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}
