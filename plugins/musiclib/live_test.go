package musiclib

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/liuran001/MusicBot-Go/bot/download"
	"github.com/liuran001/MusicBot-Go/bot/platform"
	"github.com/liuran001/MusicBot-Go/plugins/musiclib/internal/joox"
)

// Opt-in anonymous smoke test against public APIs. It never logs signed media
// URLs or credentials and does not send Telegram messages.
func TestLivePlatforms(t *testing.T) {
	if os.Getenv("MUSICLIB_LIVE") != "1" {
		t.Skip("set MUSICLIB_LIVE=1 to probe public platform APIs")
	}
	for _, tc := range []struct{ name, query string }{
		{"migu", "成都"}, {"qianqian", "庄心妍"}, {"fivesing", "成都"}, {"jamendo", "love"}, {"joox", "love"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			requestTimeout := 45 * time.Second
			if tc.name == "jamendo" {
				requestTimeout = 3 * time.Minute
			}
			p := NewPlatform(tc.name, "", nil, requestTimeout)
			defer p.Close()
			ctx, cancel := context.WithTimeout(context.Background(), requestTimeout+2*time.Minute)
			defer cancel()
			tracks, err := p.Search(ctx, tc.query, 3)
			if err != nil || len(tracks) == 0 {
				t.Fatalf("search results=%d error=%v", len(tracks), err)
			}
			t.Logf("search: %d results, first track %q (%s)", len(tracks), tracks[0].Title, tracks[0].ID)
			if tc.name == "joox" {
				track, err := p.GetTrack(ctx, tracks[0].ID)
				if err != nil || track.Title == "" {
					t.Fatalf("JOOX page metadata unavailable: %v", err)
				}
				lyrics, err := p.GetLyrics(ctx, track.ID)
				if err != nil || strings.TrimSpace(lyrics.Plain) == "" {
					t.Fatalf("JOOX page lyrics unavailable: %v", err)
				}
				t.Log("official page metadata and lyrics available")
			}
			var failures []string
			for _, candidate := range tracks {
				track, err := p.GetTrack(ctx, candidate.ID)
				if err != nil {
					failures = append(failures, fmt.Sprintf("%s: catalog metadata failed", candidate.ID))
					continue
				}
				info, err := p.GetDownloadInfo(ctx, track.ID, platform.QualityHigh)
				if err != nil {
					if tc.name == "joox" && (errors.Is(err, joox.ErrFullAudioUnavailable) || errors.Is(err, platform.ErrIncompleteAudio)) {
						t.Log("official page provides no full audio; preview correctly rejected (full download unverified)")
						return
					}
					failures = append(failures, fmt.Sprintf("%s: media resolution failed", track.ID))
					continue
				}
				if track.Duration <= 0 {
					failures = append(failures, fmt.Sprintf("%s: catalog duration missing", track.ID))
					continue
				}
				mediaPath := filepath.Join(t.TempDir(), "media."+info.Format)
				req, err := http.NewRequestWithContext(ctx, http.MethodGet, info.URL, nil)
				if err != nil {
					t.Fatal("invalid media URL")
				}
				for key, value := range info.Headers {
					req.Header.Set(key, value)
				}
				resp, err := p.client.Do(req)
				if err != nil {
					failures = append(failures, track.ID+": media request failed")
					continue
				}
				if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
					resp.Body.Close()
					failures = append(failures, fmt.Sprintf("%s: media status=%d", track.ID, resp.StatusCode))
					continue
				}
				file, createErr := os.Create(mediaPath)
				if createErr != nil {
					resp.Body.Close()
					t.Fatal(createErr)
				}
				const maxLiveMediaSize = int64(256 << 20)
				written, copyErr := io.Copy(file, io.LimitReader(resp.Body, maxLiveMediaSize+1))
				closeErr := file.Close()
				resp.Body.Close()
				if copyErr != nil || closeErr != nil || written <= 0 || written > maxLiveMediaSize {
					failures = append(failures, fmt.Sprintf("%s: full media download failed after %d bytes", track.ID, written))
					continue
				}
				prefix := make([]byte, 512)
				probeFile, openErr := os.Open(mediaPath)
				if openErr != nil {
					t.Fatal(openErr)
				}
				prefixN, readErr := probeFile.Read(prefix)
				probeFile.Close()
				contentType := http.DetectContentType(prefix[:prefixN])
				if readErr != nil && !errors.Is(readErr, io.EOF) || prefixN < 128 || strings.HasPrefix(contentType, "text/") || strings.Contains(contentType, "json") {
					failures = append(failures, fmt.Sprintf("%s: invalid media type=%s bytes=%d", track.ID, contentType, written))
					continue
				}
				actualDuration, verifyErr := download.VerifyFullAudio(ctx, mediaPath, track.Duration)
				if verifyErr != nil {
					failures = append(failures, fmt.Sprintf("%s: catalog=%s media=%s verification=%v", track.ID, track.Duration, actualDuration, verifyErr))
					continue
				}
				t.Logf("full media: %s format=%s bitrate=%d bytes=%d catalog=%s packets=%s", track.ID, info.Format, info.Bitrate, written, track.Duration, actualDuration)
				return
			}
			t.Fatalf("no playable anonymous sample: %s", strings.Join(failures, "; "))
		})
	}
}
