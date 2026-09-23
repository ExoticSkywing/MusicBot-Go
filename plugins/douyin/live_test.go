//go:build live

package douyin

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/liuran001/MusicBot-Go/bot/platform"
)

// Live end-to-end check against real servers. Run:
//
//	go test -tags live -run TestLiveOriginalSound ./plugins/douyin/ -v
//
// Requires network. Not part of default test runs.
func TestLiveOriginalSound(t *testing.T) {
	client := NewClient(nil, 20*time.Second)
	ctx := context.Background()
	const musicID = "7687226746303580947"

	track, err := client.GetTrack(ctx, musicID)
	if err != nil {
		t.Fatalf("GetTrack: %v", err)
	}
	t.Logf("track: %s by %v, %v, cover=%s", track.Title, track.Artists, track.Duration, track.CoverURL)

	info, err := client.FetchDownloadInfo(ctx, musicID, platform.QualityHiRes)
	if err != nil {
		t.Fatalf("FetchDownloadInfo: %v", err)
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, info.URL, nil)
	req.Header.Set("Range", "bytes=0-1023")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("fetch audio: %v", err)
	}
	defer resp.Body.Close()
	head, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusPartialContent || len(head) == 0 {
		t.Fatalf("audio status=%d bytes=%d", resp.StatusCode, len(head))
	}
	t.Logf("audio: %s (%s, %d kbps) content-type=%s", info.URL, info.Format, info.Bitrate, resp.Header.Get("Content-Type"))
}
