package kugou

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/guohuiyuan/music-lib/model"
	"github.com/liuran001/MusicBot-Go/bot/download"
	"github.com/liuran001/MusicBot-Go/bot/platform"
)

// Opt-in live contract test: no account configuration is loaded or printed.
func TestAnonymousAudioLive(t *testing.T) {
	if os.Getenv("MUSICBOT_ANONYMOUS_LIVE") != "1" {
		t.Skip("set MUSICBOT_ANONYMOUS_LIVE=1 for anonymous download smoke")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	client := NewClient("", nil)
	song, err := client.ResolveDownloadByQuality(ctx, &model.Song{ID: "3AC561D98B6D441D42E6295F29A646EF", Duration: 446}, platform.QualityLossless)
	if err != nil {
		t.Fatal("anonymous mobile resolution failed")
	}
	info := &platform.DownloadInfo{URL: song.URL, Size: song.Size, Format: song.Ext, Bitrate: song.Bitrate, Quality: platform.QualityStandard}
	expected := 446 * time.Second
	service := download.NewDownloadService(download.DownloadServiceOptions{Timeout: 90 * time.Second, MaxRetries: 1, EnableMultipart: true, MultipartConcurrency: 4, MultipartMinSize: 5 << 20})
	target := filepath.Join(t.TempDir(), "anonymous."+info.Format)
	size, err := service.Download(ctx, info, target, nil)
	if err != nil {
		t.Fatal("anonymous download failed")
	}
	duration, err := download.VerifyFullAudio(ctx, target, expected)
	if err != nil {
		t.Fatalf("incomplete anonymous audio: %v", err)
	}
	if err := exec.CommandContext(ctx, "ffmpeg", "-v", "error", "-xerror", "-i", target, "-f", "null", "-").Run(); err != nil {
		t.Fatal("anonymous audio decode failed")
	}
	t.Logf("anonymous audio verified: bytes=%d duration=%.3fs quality=%v", size, duration.Seconds(), info.Quality)
}
