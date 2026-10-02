package migu

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/liuran001/MusicBot-Go/bot/download"
)

// Only the earlier endpoints and sibling renditions are blocked locally.
// Catalog, selected fallback and full media requests use the public services.
func TestLiveAnonymousFallbacks(t *testing.T) {
	if os.Getenv("MIGU_FALLBACK_LIVE") != "1" {
		t.Skip("set MIGU_FALLBACK_LIVE=1 to download anonymous PC v2/H5 samples")
	}
	for _, tc := range []struct{ name, id, endpoint string }{
		{"pc_v2", "600929000002562618", "/strategy/pc/listen/v2.0"},
		{"h5", "600902000006889366", "/strategy/listen-url/h5/v2.4"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			transport := http.DefaultTransport.(*http.Transport).Clone()
			defer transport.CloseIdleConnections()
			var selectedPath string
			var fallbackCalls int
			client := &http.Client{Timeout: 45 * time.Second, Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.Header.Get("Cookie") != "" || req.Header.Get("pacmtoken") != "" {
					t.Fatal("live anonymous request contains account credentials")
				}
				listen := strings.Contains(req.URL.Path, "/listen/") || strings.Contains(req.URL.Path, "/listen-url")
				if listen && req.URL.Path != tc.endpoint {
					return listenURLResponse(req, "", ""), nil
				}
				if selectedPath != "" && !listen && req.URL.Path != selectedPath {
					return &http.Response{StatusCode: http.StatusNotFound, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
				}
				resp, err := transport.RoundTrip(req)
				if err != nil || !listen {
					return resp, err
				}
				fallbackCalls++
				body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
				resp.Body.Close()
				if err != nil {
					return nil, err
				}
				resp.Body = io.NopCloser(bytes.NewReader(body))
				decoded, err := decodeMiguResponse(body)
				var reply miguListenResponse
				if err == nil && json.Unmarshal(decoded, &reply) == nil {
					if parsed, err := url.Parse(normalizeMiguDownloadURL(reply.Data.URL)); err == nil {
						selectedPath = parsed.Path
					}
				}
				return resp, nil
			})}
			api := New(ctx, "", client)
			song, err := api.fetchSongDetail(tc.id)
			if err != nil || song == nil || song.Duration <= 0 {
				t.Fatal("live catalog metadata unavailable")
			}
			media, err := api.GetDownloadURL(song)
			if err != nil || media == "" {
				t.Fatalf("live fallback failed: %T", err)
			}
			if fallbackCalls != 1 || song.Ext != "mp3" || song.Bitrate != 128 {
				t.Fatalf("fallback calls=%d format=%s bitrate=%d", fallbackCalls, song.Ext, song.Bitrate)
			}
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, media, nil)
			if err != nil {
				t.Fatal("invalid media URL")
			}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal("live media download failed")
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("media status=%d", resp.StatusCode)
			}
			filePath := filepath.Join(t.TempDir(), "sample.mp3")
			file, err := os.Create(filePath)
			if err != nil {
				t.Fatal(err)
			}
			written, copyErr := io.Copy(file, io.LimitReader(resp.Body, (64<<20)+1))
			closeErr := file.Close()
			if copyErr != nil || closeErr != nil || written <= 0 || written > 64<<20 {
				t.Fatal("incomplete media download")
			}
			actual, err := download.VerifyFullAudio(ctx, filePath, time.Duration(song.Duration)*time.Second)
			if err != nil {
				t.Fatalf("full audio verification: %v", err)
			}
			if err := exec.CommandContext(ctx, "ffmpeg", "-v", "error", "-xerror", "-i", filePath, "-f", "null", "-").Run(); err != nil {
				t.Fatal("full audio decode failed")
			}
			t.Logf("id=%s bytes=%d format=%s bitrate=%d catalog=%ds packets=%s; full decode passed", tc.id, written, song.Ext, song.Bitrate, song.Duration, actual)
		})
	}
}
