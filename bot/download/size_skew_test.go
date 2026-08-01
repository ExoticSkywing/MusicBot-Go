package download

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/liuran001/MusicBot-Go/bot/platform"
)

// sizeSkewServer serves a body of actualSize bytes with full Range support.
// Platforms sometimes report a stale/truncated size in their metadata (QQ
// music understates some FLAC files by 15 bytes), so tests pin the behaviour
// when the served body and the declared size disagree.
func sizeSkewServer(t *testing.T, actualSize int) *httptest.Server {
	t.Helper()
	data := make([]byte, actualSize)
	for i := range data {
		data[i] = byte(i % 256)
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Accept-Ranges", "bytes")
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
			w.WriteHeader(http.StatusOK)
			return
		}
		rangeHeader := r.Header.Get("Range")
		if rangeHeader == "" {
			w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(data)
			return
		}
		var start, end int
		if _, err := fmt.Sscanf(rangeHeader, "bytes=%d-%d", &start, &end); err != nil {
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		if start < 0 || end >= len(data) || start > end {
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(data)))
		w.Header().Set("Content-Length", fmt.Sprintf("%d", end-start+1))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(data[start : end+1])
	}))
}

func TestMultipartDownload_ServerLargerThanDeclaredSize(t *testing.T) {
	const actualSize = 4 * 1024 * 1024
	const declaredSize = actualSize - 15 // platform metadata understates the file

	server := sizeSkewServer(t, actualSize)
	defer server.Close()

	downloader := NewMultipartDownloader(&http.Client{Timeout: 30 * time.Second}, 30*time.Second, MultipartDownloadOptions{
		Concurrency: 4,
		MinSize:     1024,
	})

	destPath := filepath.Join(t.TempDir(), "out.bin")
	written, err := downloader.Download(context.Background(), server.URL, &platform.DownloadInfo{
		URL:            server.URL,
		Size:           declaredSize,
		SizeIsAdvisory: true,
	}, destPath, nil)
	if err != nil {
		t.Fatalf("download should tolerate an understated advisory size, got error: %v", err)
	}
	if written != actualSize {
		t.Errorf("expected the served size %d to win over the declared %d, got %d", actualSize, declaredSize, written)
	}
	stat, err := os.Stat(destPath)
	if err != nil {
		t.Fatalf("stat destination: %v", err)
	}
	if stat.Size() != actualSize {
		t.Errorf("file size = %d, want %d", stat.Size(), actualSize)
	}
}

func TestMultipartDownload_ServerSmallerThanDeclaredSize(t *testing.T) {
	const actualSize = 4 * 1024 * 1024
	const declaredSize = actualSize + 4096 // a genuinely truncated download

	server := sizeSkewServer(t, actualSize)
	defer server.Close()

	downloader := NewMultipartDownloader(&http.Client{Timeout: 30 * time.Second}, 30*time.Second, MultipartDownloadOptions{
		Concurrency: 4,
		MinSize:     1024,
	})

	destPath := filepath.Join(t.TempDir(), "out.bin")
	_, err := downloader.Download(context.Background(), server.URL, &platform.DownloadInfo{
		URL:            server.URL,
		Size:           declaredSize,
		SizeIsAdvisory: true,
	}, destPath, nil)
	if err == nil {
		t.Fatal("a short download must stay an integrity failure")
	}
	if !errors.Is(err, errDownloadIntegrity) {
		t.Errorf("expected errDownloadIntegrity, got %v", err)
	}
}

func TestDownloadOnce_ServerLargerThanDeclaredSize(t *testing.T) {
	const actualSize = 64 * 1024
	const declaredSize = actualSize - 15

	server := sizeSkewServer(t, actualSize)
	defer server.Close()

	svc := NewDownloadService(DownloadServiceOptions{Timeout: 30 * time.Second, MaxRetries: 1})
	destPath := filepath.Join(t.TempDir(), "out.bin")

	written, err := svc.downloadOnce(context.Background(), server.URL, &platform.DownloadInfo{
		URL:            server.URL,
		Size:           declaredSize,
		SizeIsAdvisory: true,
	}, destPath, nil)
	if err != nil {
		t.Fatalf("single-thread download should tolerate an understated advisory size, got: %v", err)
	}
	if written != actualSize {
		t.Errorf("written = %d, want %d", written, actualSize)
	}
}

func TestDownloadOnce_ServerSmallerThanDeclaredSize(t *testing.T) {
	const actualSize = 64 * 1024
	const declaredSize = actualSize + 4096

	server := sizeSkewServer(t, actualSize)
	defer server.Close()

	svc := NewDownloadService(DownloadServiceOptions{Timeout: 30 * time.Second, MaxRetries: 1})
	destPath := filepath.Join(t.TempDir(), "out.bin")

	_, err := svc.downloadOnce(context.Background(), server.URL, &platform.DownloadInfo{
		URL:            server.URL,
		Size:           declaredSize,
		SizeIsAdvisory: true,
	}, destPath, nil)
	if err == nil {
		t.Fatal("a short download must stay an integrity failure")
	}
	if !errors.Is(err, errDownloadIntegrity) {
		t.Errorf("expected errDownloadIntegrity, got %v", err)
	}
}

func TestTryMultipartDownload_ServerLargerThanDeclaredSize(t *testing.T) {
	const actualSize = 4 * 1024 * 1024
	const declaredSize = actualSize - 15

	server := sizeSkewServer(t, actualSize)
	defer server.Close()

	svc := NewDownloadService(DownloadServiceOptions{
		Timeout:              30 * time.Second,
		MaxRetries:           1,
		EnableMultipart:      true,
		MultipartConcurrency: 4,
		MultipartMinSize:     1024,
	})
	destPath := filepath.Join(t.TempDir(), "out.bin")

	written, err := svc.tryMultipartDownload(context.Background(), server.URL, &platform.DownloadInfo{
		URL:            server.URL,
		Size:           declaredSize,
		SizeIsAdvisory: true,
	}, destPath, nil)
	if err != nil {
		t.Fatalf("multipart download should tolerate an understated advisory size, got: %v", err)
	}
	if written != actualSize {
		t.Errorf("written = %d, want %d", written, actualSize)
	}
}

// TestMultipartDownload_StrictSizeStillRejectsLongerSource pins the default:
// without SizeIsAdvisory an oversized body is still an integrity failure, which
// is what keeps a CDN from swapping in content the platform never described.
func TestMultipartDownload_StrictSizeStillRejectsLongerSource(t *testing.T) {
	const actualSize = 4 * 1024 * 1024
	const declaredSize = actualSize - 15

	server := sizeSkewServer(t, actualSize)
	defer server.Close()

	downloader := NewMultipartDownloader(&http.Client{Timeout: 30 * time.Second}, 30*time.Second, MultipartDownloadOptions{
		Concurrency: 4,
		MinSize:     1024,
	})

	destPath := filepath.Join(t.TempDir(), "out.bin")
	_, err := downloader.Download(context.Background(), server.URL, &platform.DownloadInfo{
		URL:  server.URL,
		Size: declaredSize, // authoritative by default
	}, destPath, nil)
	if err == nil {
		t.Fatal("an exact declared size must still reject a longer body")
	}
	if !errors.Is(err, errDownloadIntegrity) {
		t.Errorf("expected errDownloadIntegrity, got %v", err)
	}
}

// TestDownload_AdvisorySizeReportsRealByteCount guards the end-to-end contract:
// Download's return value, the file on disk, and info.Size must agree. The
// inflight path hard-links the temp file and reports the declared size, so an
// accepted undercount would otherwise be reported as the file's size.
func TestDownload_AdvisorySizeReportsRealByteCount(t *testing.T) {
	const actualSize = 4 * 1024 * 1024
	const declaredSize = actualSize - 15

	server := sizeSkewServer(t, actualSize)
	defer server.Close()

	svc := NewDownloadService(DownloadServiceOptions{
		Timeout:              30 * time.Second,
		MaxRetries:           1,
		EnableMultipart:      true,
		MultipartConcurrency: 4,
		MultipartMinSize:     1024,
	})
	destPath := filepath.Join(t.TempDir(), "out.bin")

	info := &platform.DownloadInfo{
		URL:            server.URL,
		Size:           declaredSize,
		SizeIsAdvisory: true,
	}
	written, err := svc.Download(context.Background(), info, destPath, nil)
	if err != nil {
		t.Fatalf("Download() error: %v", err)
	}
	stat, err := os.Stat(destPath)
	if err != nil {
		t.Fatalf("stat destination: %v", err)
	}
	if stat.Size() != actualSize {
		t.Fatalf("file on disk = %d bytes, want %d", stat.Size(), actualSize)
	}
	if written != stat.Size() {
		t.Errorf("Download() reported %d bytes but wrote %d", written, stat.Size())
	}
	if info.Size != actualSize {
		t.Errorf("info.Size = %d after download, want the real size %d", info.Size, actualSize)
	}
}

func TestWrapDownloadIntegrityReadError_SatisfiedExpectation(t *testing.T) {
	readErr := errors.New("unexpected EOF")

	// Advisory expectation, read past it: the declared size was simply too
	// small, not a truncated transfer.
	if got := wrapDownloadIntegrityReadError(readErr, 120, 100, "download body", true); errors.Is(got, errDownloadIntegrity) {
		t.Errorf("reading past an advisory size must not be an integrity failure, got %v", got)
	}

	// Exact expectation, read past it: still a violation.
	if got := wrapDownloadIntegrityReadError(readErr, 120, 100, "download body", false); !errors.Is(got, errDownloadIntegrity) {
		t.Errorf("reading past an exact size must stay an integrity failure, got %v", got)
	}

	// Genuinely short read stays an integrity failure either way.
	for _, advisory := range []bool{true, false} {
		got := wrapDownloadIntegrityReadError(readErr, 80, 100, "download body", advisory)
		if !errors.Is(got, errDownloadIntegrity) {
			t.Errorf("short read (advisory=%v) must stay an integrity failure, got %v", advisory, got)
		}
		if !strings.Contains(got.Error(), "80 of 100") {
			t.Errorf("error should report progress, got %v", got)
		}
	}
}
