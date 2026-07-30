package download

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
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

var redirectPolicyHeaders = map[string]string{
	"Authorization":    "Bearer top-secret",
	"Cookie":           "session=top-secret",
	"X-Download-Token": "top-secret",
	"User-Agent":       "policy-agent",
	"Referer":          "https://www.kuwo.cn/",
}

func localhostTestURL(t *testing.T, rawURL string) string {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	parsed.Host = net.JoinHostPort("localhost", parsed.Port())
	return parsed.String()
}

func TestSameDownloadOrigin(t *testing.T) {
	parse := func(raw string) *url.URL {
		parsed, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		return parsed
	}
	for _, tt := range []struct {
		name        string
		left, right string
		want        bool
	}{
		{name: "host case", left: "http://EXAMPLE.com/a", right: "http://example.COM/b", want: true},
		{name: "HTTP default port", left: "http://example.com/a", right: "http://example.com:80/b", want: true},
		{name: "HTTPS default port", left: "https://example.com/a", right: "https://example.com:443/b", want: true},
		{name: "IPv6 default port", left: "http://[::1]/a", right: "http://[::1]:80/b", want: true},
		{name: "scheme changed", left: "http://example.com/a", right: "https://example.com/a"},
		{name: "host changed", left: "https://example.com/a", right: "https://cdn.example.com/a"},
		{name: "port changed", left: "https://example.com/a", right: "https://example.com:444/a"},
		{name: "IPv6 port changed", left: "http://[::1]/a", right: "http://[::1]:8080/a"},
		{name: "unknown scheme", left: "ftp://example.com/a", right: "ftp://example.com/b"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := sameDownloadOrigin(parse(tt.left), parse(tt.right)); got != tt.want {
				t.Fatalf("sameDownloadOrigin(%q, %q) = %v, want %v", tt.left, tt.right, got, tt.want)
			}
		})
	}
	if sameDownloadOrigin(nil, parse("https://example.com")) {
		t.Fatal("nil origin accepted")
	}

	origin := &http.Request{URL: parse("https://example.com/start")}
	crossOrigin := &http.Request{URL: parse("https://cdn.example.com/step")}
	backAtOrigin := &http.Request{URL: parse("https://example.com/final")}
	if redirectChainSameOrigin(backAtOrigin, []*http.Request{origin, crossOrigin}) {
		t.Fatal("A -> B -> A redirect chain restored original policy headers")
	}
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

func TestDownloadMultipartIntegrityConflictNeverFallsBack(t *testing.T) {
	const expectedSize = int64(9)
	for _, tt := range []struct {
		name       string
		headSize   int64
		rangeTotal int64
	}{
		{name: "HEAD total conflicts with trusted size", headSize: expectedSize + 1},
		{name: "Content-Range total conflicts with trusted size", headSize: expectedSize, rangeTotal: expectedSize + 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var headHits atomic.Int32
			var rangeHits atomic.Int32
			var plainHits atomic.Int32
			var candidateHits atomic.Int32

			candidate := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				candidateHits.Add(1)
				_, _ = io.WriteString(w, "123456789")
			}))
			defer candidate.Close()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodHead:
					headHits.Add(1)
					w.Header().Set("Accept-Ranges", "bytes")
					w.Header().Set("Content-Length", strconv.FormatInt(tt.headSize, 10))
				case r.Header.Get("Range") != "":
					rangeHits.Add(1)
					w.Header().Set(
						"Content-Range",
						fmt.Sprintf("bytes 0-%d/%d", expectedSize-1, tt.rangeTotal),
					)
					w.Header().Set("Content-Length", strconv.FormatInt(expectedSize, 10))
					w.WriteHeader(http.StatusPartialContent)
					_, _ = io.WriteString(w, "123456789")
				default:
					plainHits.Add(1)
					w.Header().Set("Content-Length", strconv.FormatInt(expectedSize, 10))
					_, _ = io.WriteString(w, "123456789")
				}
			}))
			defer server.Close()

			dest := filepath.Join(t.TempDir(), "audio.bin")
			written, err := newPolicyTestService(true).Download(context.Background(), &platform.DownloadInfo{
				URL:           server.URL,
				CandidateURLs: []string{candidate.URL},
				Size:          expectedSize,
			}, dest, nil)
			if !errors.Is(err, errDownloadIntegrity) {
				t.Fatalf("Download() = (%d, %v), want errDownloadIntegrity", written, err)
			}
			if headHits.Load() != 1 {
				t.Fatalf("HEAD hits = %d, want 1", headHits.Load())
			}
			if tt.rangeTotal > 0 && rangeHits.Load() != 1 {
				t.Fatalf("Range hits = %d, want 1", rangeHits.Load())
			}
			if plainHits.Load() != 0 {
				t.Fatalf("plain GET fallback hits = %d, want 0", plainHits.Load())
			}
			if candidateHits.Load() != 0 {
				t.Fatalf("candidate fallback hits = %d, want 0", candidateHits.Load())
			}
			if _, statErr := os.Stat(dest); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("integrity-conflicted destination still exists: %v", statErr)
			}
		})
	}
}

func TestDownloadMultipartPolicyRangeCannotOverrideInternalRange(t *testing.T) {
	const payload = "123456789"
	var rangeHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.Header().Set("Accept-Ranges", "bytes")
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
			return
		}
		if got := r.Header.Get("Range"); got != "bytes=0-8" {
			t.Fatalf("Range = %q, want downloader-owned bytes=0-8", got)
		}
		rangeHits.Add(1)
		w.Header().Set("Content-Range", "bytes 0-8/9")
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = io.WriteString(w, payload)
	}))
	defer server.Close()

	dest := filepath.Join(t.TempDir(), "audio.bin")
	written, err := newPolicyTestService(true).Download(context.Background(), &platform.DownloadInfo{
		URL:     server.URL,
		Headers: map[string]string{"range": "bytes=0-0"},
		Size:    int64(len(payload)),
	}, dest, nil)
	if err != nil || written != int64(len(payload)) {
		t.Fatalf("Download() = (%d, %v)", written, err)
	}
	if rangeHits.Load() != 1 {
		t.Fatalf("Range hits = %d, want 1", rangeHits.Load())
	}
}

func TestDownloadMultipartPolicyRangeCannotOverrideInternalRangeOnRedirect(t *testing.T) {
	const payload = "123456789"

	for _, tt := range []struct {
		name      string
		crossHost bool
		validator bool
	}{
		{name: "same origin"},
		{name: "same origin with validator", validator: true},
		{name: "cross origin", crossHost: true},
		{name: "cross origin with validator", crossHost: true, validator: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var (
				rangeMu   sync.Mutex
				finalGets []string
				target    *httptest.Server
			)
			target = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !tt.crossHost && r.URL.Path == "/start" {
					http.Redirect(w, r, "/final", http.StatusTemporaryRedirect)
					return
				}
				if r.URL.Path != "/final" {
					http.NotFound(w, r)
					return
				}
				if r.Method == http.MethodHead {
					w.Header().Set("Accept-Ranges", "bytes")
					w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
					return
				}

				gotRange := r.Header.Get("Range")
				rangeMu.Lock()
				finalGets = append(finalGets, gotRange)
				rangeMu.Unlock()

				var start, end int
				if _, err := fmt.Sscanf(gotRange, "bytes=%d-%d", &start, &end); err != nil ||
					start < 0 || end < start || end >= len(payload) {
					http.Error(w, "invalid range", http.StatusBadRequest)
					return
				}
				w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(payload)))
				w.Header().Set("Content-Length", strconv.Itoa(end-start+1))
				w.WriteHeader(http.StatusPartialContent)
				_, _ = io.WriteString(w, payload[start:end+1])
			}))
			defer target.Close()

			startURL := target.URL + "/start"
			if tt.crossHost {
				source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					http.Redirect(w, r, target.URL+"/final", http.StatusTemporaryRedirect)
				}))
				defer source.Close()
				startURL = source.URL + "/start"
			}

			info := &platform.DownloadInfo{
				URL:     startURL,
				Headers: map[string]string{"range": "bytes=0-0"},
				Size:    int64(len(payload)),
			}
			if tt.validator {
				info.ValidateURL = func(string) error { return nil }
			}

			dest := filepath.Join(t.TempDir(), "audio.bin")
			written, err := newPolicyTestService(true).Download(context.Background(), info, dest, nil)
			if err != nil || written != int64(len(payload)) {
				t.Fatalf("Download() = (%d, %v)", written, err)
			}

			rangeMu.Lock()
			gotFinalGets := append([]string(nil), finalGets...)
			rangeMu.Unlock()
			if len(gotFinalGets) != 1 || gotFinalGets[0] != "bytes=0-8" {
				t.Fatalf("final GET Range values = %q, want [bytes=0-8]", gotFinalGets)
			}
		})
	}
}

func TestDownloadConcurrentMultipartIntegrityConflictNeverFallsBack(t *testing.T) {
	const (
		totalSize = 2 * 1024 * 1024
		partSize  = 1024 * 1024
	)
	payload := bytesOfSize(totalSize)

	for _, tt := range []struct {
		name         string
		maxChunkSize int64
	}{
		{name: "optional multipart"},
		{name: "required bounded chunks", maxChunkSize: partSize},
	} {
		t.Run(tt.name, func(t *testing.T) {
			service := newPolicyTestService(true)
			allPrimaryRangesStarted := make(chan struct{})
			var (
				startedOnce   sync.Once
				rangeStarts   atomic.Int32
				plainHits     atomic.Int32
				candidateHits atomic.Int32
			)

			service.client.Transport = downloadRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.Host == "candidate.invalid" {
					candidateHits.Add(1)
					rangeHeader := req.Header.Get("Range")
					var start, end int
					if _, err := fmt.Sscanf(rangeHeader, "bytes=%d-%d", &start, &end); err != nil {
						return nil, fmt.Errorf("candidate range %q: %w", rangeHeader, err)
					}
					body := payload[start : end+1]
					return &http.Response{
						StatusCode: http.StatusPartialContent,
						Header: http.Header{
							"Content-Range": []string{fmt.Sprintf("bytes %d-%d/%d", start, end, totalSize)},
						},
						Body:          io.NopCloser(bytes.NewReader(body)),
						ContentLength: int64(len(body)),
						Request:       req,
					}, nil
				}

				if req.Method == http.MethodHead {
					return &http.Response{
						StatusCode: http.StatusOK,
						Header: http.Header{
							"Accept-Ranges": []string{"bytes"},
						},
						Body:          http.NoBody,
						ContentLength: totalSize,
						Request:       req,
					}, nil
				}

				rangeHeader := req.Header.Get("Range")
				if rangeHeader == "" {
					plainHits.Add(1)
					return &http.Response{
						StatusCode:    http.StatusOK,
						Header:        make(http.Header),
						Body:          io.NopCloser(bytes.NewReader(payload)),
						ContentLength: totalSize,
						Request:       req,
					}, nil
				}

				if rangeStarts.Add(1) == 2 {
					startedOnce.Do(func() { close(allPrimaryRangesStarted) })
				}
				switch rangeHeader {
				case "bytes=0-1048575":
					<-allPrimaryRangesStarted
					return &http.Response{
						StatusCode:    http.StatusOK,
						Header:        make(http.Header),
						Body:          http.NoBody,
						ContentLength: 0,
						Request:       req,
					}, nil
				case "bytes=1048576-2097151":
					<-req.Context().Done()
					return &http.Response{
						StatusCode: http.StatusPartialContent,
						Header: http.Header{
							"Content-Range": []string{"bytes 1048576-2097151/2097153"},
						},
						Body:          http.NoBody,
						ContentLength: partSize,
						Request:       req,
					}, nil
				default:
					return nil, fmt.Errorf("unexpected primary range %q", rangeHeader)
				}
			})

			info := &platform.DownloadInfo{
				URL:           "https://primary.invalid/audio",
				CandidateURLs: []string{"https://candidate.invalid/audio"},
				Size:          totalSize,
				MaxChunkSize:  tt.maxChunkSize,
			}
			dest := filepath.Join(t.TempDir(), "audio.bin")
			written, err := service.Download(context.Background(), info, dest, nil)
			if !errors.Is(err, errDownloadIntegrity) {
				t.Fatalf("Download() = (%d, %v), want errDownloadIntegrity", written, err)
			}
			if rangeStarts.Load() != 2 {
				t.Fatalf("primary Range requests = %d, want 2", rangeStarts.Load())
			}
			if plainHits.Load() != 0 {
				t.Fatalf("plain GET fallback hits = %d, want 0", plainHits.Load())
			}
			if candidateHits.Load() != 0 {
				t.Fatalf("candidate fallback hits = %d, want 0", candidateHits.Load())
			}
			if _, statErr := os.Stat(dest); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("integrity-conflicted destination still exists: %v", statErr)
			}
		})
	}
}

func TestDownloadMultipartUnexpectedEOFKeepsIntegritySentinel(t *testing.T) {
	const payload = "123456789"
	var rangeHits, plainHits, candidateHits atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodHead:
			w.Header().Set("Accept-Ranges", "bytes")
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		case r.Header.Get("Range") != "":
			rangeHits.Add(1)
			w.Header().Set("Content-Range", "bytes 0-8/9")
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = io.WriteString(w, "short")
		default:
			plainHits.Add(1)
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
			_, _ = io.WriteString(w, payload)
		}
	}))
	defer server.Close()

	candidate := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		candidateHits.Add(1)
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		_, _ = io.WriteString(w, payload)
	}))
	defer candidate.Close()

	dest := filepath.Join(t.TempDir(), "audio.bin")
	written, err := newPolicyTestService(true).Download(context.Background(), &platform.DownloadInfo{
		URL:           server.URL,
		CandidateURLs: []string{candidate.URL},
		Size:          int64(len(payload)),
	}, dest, nil)
	if !errors.Is(err, errDownloadIntegrity) {
		t.Fatalf("Download() = (%d, %v), want errDownloadIntegrity", written, err)
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("Download() error = %v, want wrapped io.ErrUnexpectedEOF", err)
	}
	if rangeHits.Load() != 1 || plainHits.Load() != 0 || candidateHits.Load() != 0 {
		t.Fatalf("request hits: range=%d plain=%d candidate=%d, want 1/0/0",
			rangeHits.Load(), plainHits.Load(), candidateHits.Load())
	}
	if _, statErr := os.Stat(dest); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("integrity-conflicted destination still exists: %v", statErr)
	}
}

func TestDownloadSingleUnexpectedEOFKeepsIntegritySentinel(t *testing.T) {
	const payload = "123456789"

	for _, multipart := range []bool{false, true} {
		t.Run(fmt.Sprintf("multipart=%v", multipart), func(t *testing.T) {
			var primaryGets, candidateHits atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodHead {
					w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
					return
				}
				primaryGets.Add(1)
				w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
				_, _ = io.WriteString(w, "short")
			}))
			defer server.Close()

			candidate := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				candidateHits.Add(1)
				w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
				_, _ = io.WriteString(w, payload)
			}))
			defer candidate.Close()

			dest := filepath.Join(t.TempDir(), "audio.bin")
			written, err := newPolicyTestService(multipart).Download(context.Background(), &platform.DownloadInfo{
				URL:           server.URL,
				CandidateURLs: []string{candidate.URL},
				Size:          int64(len(payload)),
			}, dest, nil)
			if !errors.Is(err, errDownloadIntegrity) {
				t.Fatalf("Download() = (%d, %v), want errDownloadIntegrity", written, err)
			}
			if !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatalf("Download() error = %v, want wrapped io.ErrUnexpectedEOF", err)
			}
			if primaryGets.Load() != 1 || candidateHits.Load() != 0 {
				t.Fatalf("request hits: primary GET=%d candidate=%d, want 1/0",
					primaryGets.Load(), candidateHits.Load())
			}
			if _, statErr := os.Stat(dest); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("integrity-conflicted destination still exists: %v", statErr)
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

func TestDownloadHeadersWithoutValidatorNeverCrossOriginalOrigin(t *testing.T) {
	payload := bytesOfSize(256 * 1024)
	for _, multipart := range []bool{false, true} {
		t.Run(fmt.Sprintf("multipart=%v", multipart), func(t *testing.T) {
			var leakedPolicyValues atomic.Int32
			var targetHits atomic.Int32
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				targetHits.Add(1)
				for key, value := range redirectPolicyHeaders {
					if r.Header.Get(key) == value {
						leakedPolicyValues.Add(1)
					}
				}
				if r.URL.Path == "/step" {
					http.Redirect(w, r, "/final", http.StatusTemporaryRedirect)
					return
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
						http.Error(w, "bad range", http.StatusBadRequest)
						return
					}
					w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(payload)))
					w.WriteHeader(http.StatusPartialContent)
					_, _ = w.Write(payload[start : end+1])
					return
				}
				w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
				_, _ = w.Write(payload)
			}))
			defer target.Close()

			var missingInitialPolicy atomic.Int32
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				for key, value := range redirectPolicyHeaders {
					if r.Header.Get(key) != value {
						missingInitialPolicy.Add(1)
					}
				}
				http.Redirect(w, r, target.URL+"/step", http.StatusTemporaryRedirect)
			}))
			defer origin.Close()

			dest := filepath.Join(t.TempDir(), "audio.bin")
			written, err := newPolicyTestService(multipart).Download(context.Background(), &platform.DownloadInfo{
				URL:     localhostTestURL(t, origin.URL) + "/start",
				Headers: redirectPolicyHeaders,
				Size:    int64(len(payload)),
			}, dest, nil)
			if err != nil {
				t.Fatalf("Download() = %v", err)
			}
			if written != int64(len(payload)) {
				t.Fatalf("written = %d, want %d", written, len(payload))
			}
			if missingInitialPolicy.Load() != 0 {
				t.Fatalf("initial requests missing policy values %d times", missingInitialPolicy.Load())
			}
			if targetHits.Load() < 2 {
				t.Fatalf("cross-origin target hits = %d, want at least two redirect hops", targetHits.Load())
			}
			if leakedPolicyValues.Load() != 0 {
				t.Fatalf("cross-origin target received %d policy header values", leakedPolicyValues.Load())
			}
		})
	}
}

func TestDownloadHeadersWithoutValidatorSurviveSameOriginRedirect(t *testing.T) {
	var missingPolicy atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "/final", http.StatusTemporaryRedirect)
			return
		}
		for key, value := range redirectPolicyHeaders {
			if r.Header.Get(key) != value {
				missingPolicy.Add(1)
			}
		}
		_, _ = io.WriteString(w, "audio")
	}))
	defer server.Close()

	dest := filepath.Join(t.TempDir(), "audio.bin")
	if _, err := newPolicyTestService(false).Download(context.Background(), &platform.DownloadInfo{
		URL:     server.URL + "/start",
		Headers: redirectPolicyHeaders,
	}, dest, nil); err != nil {
		t.Fatalf("Download() = %v", err)
	}
	if missingPolicy.Load() != 0 {
		t.Fatalf("same-origin redirect lost %d policy header values", missingPolicy.Load())
	}
}

func TestDownloadValidatorControlsCrossOriginHeaderReattachment(t *testing.T) {
	t.Run("approved", func(t *testing.T) {
		var missingPolicy atomic.Int32
		target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			for key, value := range redirectPolicyHeaders {
				if r.Header.Get(key) != value {
					missingPolicy.Add(1)
				}
			}
			_, _ = io.WriteString(w, "audio")
		}))
		defer target.Close()
		origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target.URL+"/final", http.StatusTemporaryRedirect)
		}))
		defer origin.Close()

		dest := filepath.Join(t.TempDir(), "audio.bin")
		if _, err := newPolicyTestService(false).Download(context.Background(), &platform.DownloadInfo{
			URL:         localhostTestURL(t, origin.URL) + "/start",
			Headers:     redirectPolicyHeaders,
			ValidateURL: func(string) error { return nil },
		}, dest, nil); err != nil {
			t.Fatalf("Download() = %v", err)
		}
		if missingPolicy.Load() != 0 {
			t.Fatalf("approved cross-origin redirect lost %d policy header values", missingPolicy.Load())
		}
	})

	t.Run("rejected", func(t *testing.T) {
		var targetHits atomic.Int32
		errBlocked := errors.New("blocked cross-origin redirect")
		target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			targetHits.Add(1)
			_, _ = io.WriteString(w, "stolen")
		}))
		defer target.Close()
		origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target.URL+"/final", http.StatusTemporaryRedirect)
		}))
		defer origin.Close()

		dest := filepath.Join(t.TempDir(), "audio.bin")
		_, err := newPolicyTestService(false).Download(context.Background(), &platform.DownloadInfo{
			URL:     localhostTestURL(t, origin.URL) + "/start",
			Headers: redirectPolicyHeaders,
			ValidateURL: func(rawURL string) error {
				if strings.HasPrefix(rawURL, target.URL) {
					return errBlocked
				}
				return nil
			},
		}, dest, nil)
		if !errors.Is(err, errBlocked) {
			t.Fatalf("Download() error = %v, want blocked sentinel", err)
		}
		if targetHits.Load() != 0 {
			t.Fatalf("rejected redirect target hits = %d", targetHits.Load())
		}
	})
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
