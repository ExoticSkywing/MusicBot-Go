package download

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/liuran001/MusicBot-Go/bot/platform"
)

func newPolicyTestService(multipart bool) *DownloadService {
	return NewDownloadService(DownloadServiceOptions{
		Timeout:              2 * time.Second,
		MaxRetries:           1,
		EnableMultipart:      multipart,
		MultipartConcurrency: 2,
		MultipartMinSize:     1,
	})
}

type downloadRoundTripFunc func(*http.Request) (*http.Response, error)

func (f downloadRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestDownloadSinglePreservesKnownSizeAndRejectsAnyMismatch(t *testing.T) {
	const expectedSize = int64(9)
	for _, tt := range []struct {
		name          string
		contentLength int64
		body          string
		wantErr       bool
	}{
		{name: "declared short", contentLength: 5, body: "short", wantErr: true},
		{name: "unknown short", contentLength: -1, body: "short", wantErr: true},
		{name: "unknown long", contentLength: -1, body: "0123456789", wantErr: true},
		{name: "exact", contentLength: expectedSize, body: "123456789"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var validatorCalls atomic.Int32
			service := newPolicyTestService(false)
			service.client.Transport = downloadRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.Header.Get("X-Media-Policy") != "verified" {
					t.Fatalf("policy header = %q", req.Header.Get("X-Media-Policy"))
				}
				return &http.Response{
					StatusCode:    http.StatusOK,
					Header:        http.Header{"Content-Type": []string{"audio/flac"}},
					Body:          io.NopCloser(strings.NewReader(tt.body)),
					ContentLength: tt.contentLength,
					Request:       req,
				}, nil
			})
			info := &platform.DownloadInfo{
				URL:     "https://kw-er.kuwo.cn/verified.flac",
				Headers: map[string]string{"X-Media-Policy": "verified"},
				Size:    expectedSize,
				Format:  "flac",
				ValidateURL: func(string) error {
					validatorCalls.Add(1)
					return nil
				},
			}
			dest := filepath.Join(t.TempDir(), "audio.flac")
			written, err := service.Download(context.Background(), info, dest, nil)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Download() succeeded with %d bytes", written)
				}
				if _, statErr := os.Stat(dest); !errors.Is(statErr, os.ErrNotExist) {
					t.Fatalf("partial destination still exists: %v", statErr)
				}
			} else {
				if err != nil || written != expectedSize {
					t.Fatalf("Download() = (%d, %v), want (%d, nil)", written, err, expectedSize)
				}
			}
			if info.Size != expectedSize {
				t.Fatalf("known size overwritten to %d, want %d", info.Size, expectedSize)
			}
			if validatorCalls.Load() == 0 {
				t.Fatal("URL validator was not called")
			}
		})
	}
}

func TestDownloadMultipartPreservesKnownSizeAndRejectsLongerSource(t *testing.T) {
	const expectedSize = int64(9)
	payload := []byte("0123456789")
	for _, tt := range []struct {
		name           string
		headStatus     int
		headSize       int
		supportsRange  bool
		rangeTotal     int
		extraRangeByte bool
	}{
		{name: "HEAD longer without ranges", headStatus: http.StatusOK, headSize: len(payload)},
		{name: "HEAD longer with ranges", headStatus: http.StatusOK, headSize: len(payload), supportsRange: true},
		{name: "written longer without ranges", headStatus: http.StatusOK, headSize: int(expectedSize)},
		{name: "range total longer", headStatus: http.StatusOK, headSize: int(expectedSize), supportsRange: true, rangeTotal: len(payload)},
		{name: "range body longer", headStatus: http.StatusOK, headSize: int(expectedSize), supportsRange: true, rangeTotal: int(expectedSize), extraRangeByte: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodHead {
					if tt.supportsRange {
						w.Header().Set("Accept-Ranges", "bytes")
					}
					if tt.headSize > 0 {
						w.Header().Set("Content-Length", fmt.Sprint(tt.headSize))
					}
					w.WriteHeader(tt.headStatus)
					return
				}
				if rangeHeader := r.Header.Get("Range"); rangeHeader != "" {
					var start, end int
					if _, err := fmt.Sscanf(rangeHeader, "bytes=%d-%d", &start, &end); err != nil {
						t.Fatalf("Range = %q", rangeHeader)
					}
					total := len(payload)
					if tt.rangeTotal > 0 {
						total = tt.rangeTotal
					}
					w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, total))
					w.WriteHeader(http.StatusPartialContent)
					_, _ = w.Write(payload[start : end+1])
					if tt.extraRangeByte {
						_, _ = w.Write(payload[end+1 : end+2])
					}
					return
				}
				w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
				_, _ = w.Write(payload)
			}))
			defer server.Close()

			info := &platform.DownloadInfo{
				URL:     server.URL,
				Headers: map[string]string{"X-Media-Policy": "verified"},
				Size:    expectedSize,
			}
			dest := filepath.Join(t.TempDir(), "audio.bin")
			written, err := newPolicyTestService(true).Download(context.Background(), info, dest, nil)
			if err == nil {
				t.Fatalf("Download() succeeded with %d bytes", written)
			}
			if _, statErr := os.Stat(dest); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("mismatched destination still exists: %v", statErr)
			}
			if info.Size != expectedSize {
				t.Fatalf("known size overwritten to %d, want %d", info.Size, expectedSize)
			}
		})
	}
}

func TestDownloadURLValidatorChecksInitialAndRedirectTargets(t *testing.T) {
	var finalHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/start":
			http.Redirect(w, r, "/final", http.StatusFound)
		case "/final":
			finalHits.Add(1)
			_, _ = w.Write([]byte("should not be reached"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	var checked []string
	info := &platform.DownloadInfo{
		URL: server.URL + "/start",
		ValidateURL: func(rawURL string) error {
			checked = append(checked, rawURL)
			if strings.HasSuffix(rawURL, "/final") {
				return errors.New("blocked redirect")
			}
			return nil
		},
	}
	dest := filepath.Join(t.TempDir(), "audio.bin")
	if _, err := newPolicyTestService(false).Download(context.Background(), info, dest, nil); err == nil || !strings.Contains(err.Error(), "blocked redirect") {
		t.Fatalf("Download() error = %v", err)
	}
	if finalHits.Load() != 0 {
		t.Fatalf("redirect target hits = %d", finalHits.Load())
	}
	if len(checked) < 2 || checked[0] != server.URL+"/start" || checked[1] != server.URL+"/final" {
		t.Fatalf("checked URLs = %v", checked)
	}

	initialHits := atomic.Int32{}
	info.URL = server.URL + "/never"
	info.ValidateURL = func(string) error {
		initialHits.Add(1)
		return errors.New("blocked initial")
	}
	if _, err := newPolicyTestService(false).Download(context.Background(), info, dest, nil); err == nil || !strings.Contains(err.Error(), "blocked initial") {
		t.Fatalf("initial Download() error = %v", err)
	}
	if initialHits.Load() != 1 {
		t.Fatalf("initial validator calls = %d", initialHits.Load())
	}
}

func TestDownloadURLValidatorStopsBeforeEleventhRequest(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current := hits.Add(1)
		http.Redirect(w, r, fmt.Sprintf("/%d", current), http.StatusFound)
	}))
	defer server.Close()
	info := &platform.DownloadInfo{
		URL:         server.URL + "/0",
		ValidateURL: func(string) error { return nil },
	}
	dest := filepath.Join(t.TempDir(), "audio.bin")
	if _, err := newPolicyTestService(false).Download(context.Background(), info, dest, nil); err == nil {
		t.Fatal("Download() succeeded")
	}
	if hits.Load() != 10 {
		t.Fatalf("requests = %d, want 10 before the eleventh request is blocked", hits.Load())
	}
}

func TestDownloadHeadersSurviveSingleAndMultipartRedirects(t *testing.T) {
	payload := bytesOfSize(256 * 1024)
	for _, multipart := range []bool{false, true} {
		t.Run(fmt.Sprintf("multipart=%v", multipart), func(t *testing.T) {
			var missingHeaders atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/start" {
					http.Redirect(w, r, "/media", http.StatusTemporaryRedirect)
					return
				}
				if r.Header.Get("User-Agent") != "policy-agent" || r.Referer() != "https://www.kuwo.cn/" {
					missingHeaders.Add(1)
				}
				if r.Method == http.MethodHead {
					w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
					if multipart {
						w.Header().Set("Accept-Ranges", "bytes")
					}
					return
				}
				if value := r.Header.Get("Range"); value != "" {
					var start, end int
					if _, err := fmt.Sscanf(value, "bytes=%d-%d", &start, &end); err != nil {
						t.Errorf("Range = %q", value)
						http.Error(w, "bad range", http.StatusBadRequest)
						return
					}
					w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(payload)))
					w.WriteHeader(http.StatusPartialContent)
					_, _ = w.Write(payload[start : end+1])
					return
				}
				_, _ = w.Write(payload)
			}))
			defer server.Close()
			info := &platform.DownloadInfo{
				URL:     server.URL + "/start",
				Headers: map[string]string{"User-Agent": "policy-agent", "Referer": "https://www.kuwo.cn/"},
				ValidateURL: func(rawURL string) error {
					if !strings.HasPrefix(rawURL, server.URL+"/") {
						return errors.New("outside media origin")
					}
					return nil
				},
			}
			dest := filepath.Join(t.TempDir(), "audio.bin")
			written, err := newPolicyTestService(multipart).Download(context.Background(), info, dest, nil)
			if err != nil {
				t.Fatalf("Download() = %v", err)
			}
			got, err := os.ReadFile(dest)
			if err != nil {
				t.Fatal(err)
			}
			if written != int64(len(payload)) || string(got) != string(payload) {
				t.Fatalf("download bytes = %d/%d", written, len(got))
			}
			if missingHeaders.Load() != 0 {
				t.Fatalf("requests missing headers = %d", missingHeaders.Load())
			}
		})
	}
}

func TestDownloadConstrainedInflightSeparatesHeaders(t *testing.T) {
	var hits atomic.Int32
	bothStarted := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 2 {
			close(bothStarted)
		}
		select {
		case <-bothStarted:
		case <-time.After(500 * time.Millisecond):
		}
		_, _ = io.WriteString(w, r.Header.Get("X-Download-Policy"))
	}))
	defer server.Close()
	service := newPolicyTestService(false)
	results := make([]string, 2)
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i, policy := range []string{"alpha", "beta"} {
		wg.Add(1)
		go func(i int, policy string) {
			defer wg.Done()
			dest := filepath.Join(t.TempDir(), policy+".bin")
			_, errs[i] = service.Download(context.Background(), &platform.DownloadInfo{
				URL:     server.URL,
				Headers: map[string]string{"X-Download-Policy": policy},
			}, dest, nil)
			if data, err := os.ReadFile(dest); err == nil {
				results[i] = string(data)
			}
		}(i, policy)
	}
	wg.Wait()
	if errs[0] != nil || errs[1] != nil {
		t.Fatalf("errors = %v", errs)
	}
	if hits.Load() != 2 || results[0] != "alpha" || results[1] != "beta" {
		t.Fatalf("hits=%d results=%v", hits.Load(), results)
	}
}

func TestDownloadConstrainedInflightSeparatesValidators(t *testing.T) {
	var hits atomic.Int32
	var validatorCalls [2]atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, "audio")
	}))
	defer server.Close()
	service := newPolicyTestService(false)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range 2 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			dest := filepath.Join(t.TempDir(), fmt.Sprintf("%d.bin", i))
			_, errs[i] = service.Download(context.Background(), &platform.DownloadInfo{
				URL: server.URL,
				ValidateURL: func(string) error {
					validatorCalls[i].Add(1)
					return nil
				},
			}, dest, nil)
		}(i)
	}
	wg.Wait()
	if errs[0] != nil || errs[1] != nil {
		t.Fatalf("errors = %v", errs)
	}
	if hits.Load() != 2 || validatorCalls[0].Load() == 0 || validatorCalls[1].Load() == 0 {
		t.Fatalf("hits=%d validators=%d,%d", hits.Load(), validatorCalls[0].Load(), validatorCalls[1].Load())
	}
}

func TestDownloadConstrainedInflightPreservesUnconstrainedDedup(t *testing.T) {
	var hits atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			close(started)
		}
		<-release
		_, _ = io.WriteString(w, "audio")
	}))
	defer server.Close()
	service := newPolicyTestService(false)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range 2 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			dest := filepath.Join(t.TempDir(), fmt.Sprintf("%d.bin", i))
			_, errs[i] = service.Download(context.Background(), &platform.DownloadInfo{URL: server.URL}, dest, nil)
		}(i)
	}
	<-started
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	if errs[0] != nil || errs[1] != nil || hits.Load() != 1 {
		t.Fatalf("errors=%v hits=%d", errs, hits.Load())
	}
}

func bytesOfSize(size int) []byte {
	data := make([]byte, size)
	for i := range data {
		data[i] = byte(i % 251)
	}
	return data
}
