package kuwo

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/liuran001/MusicBot-Go/bot/download"
	"github.com/liuran001/MusicBot-Go/bot/platform"
)

// TestKuwoFourQualitiesLive exercises the public resolver and full downloads
// without loading account configuration. Opt in explicitly; CDN URLs stay private.
func TestKuwoFourQualitiesLive(t *testing.T) {
	if os.Getenv("MUSICBOT_KUWO_QUALITY_LIVE") != "1" {
		t.Skip("set MUSICBOT_KUWO_QUALITY_LIVE=1 to verify all four tiers online")
	}
	const trackID = "246052537"
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	client := NewClient(30*time.Second, nil)
	detail, _, err := client.getTrackDetail(ctx, trackID)
	if err != nil {
		t.Fatalf("track detail: %s", redactKuwoE2EError(err))
	}
	t.Logf("track=%s title=%s expected_duration=%.3fs", trackID, detail.Title, detail.Duration.Seconds())
	baseTransport := client.apiHTTPClient.Transport
	if baseTransport == nil {
		baseTransport = http.DefaultTransport
	}
	var requests []string
	client.apiHTTPClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == "mobi.kuwo.cn" {
			br := req.URL.Query().Get("br")
			if br == "" {
				br = "legacy"
			}
			requests = append(requests, br)
		}
		return baseTransport.RoundTrip(req)
	})
	service := download.NewDownloadService(download.DownloadServiceOptions{
		Timeout: 3 * time.Minute, MaxRetries: 1,
		EnableMultipart: true, MultipartConcurrency: 4, MultipartMinSize: 5 << 20,
	})
	for _, tier := range []struct {
		quality platform.Quality
		br      string
		codec   string
		rate    string
		depth   string
	}{
		{platform.QualityStandard, "128kmp3", "mp3", "48000", ""},
		{platform.QualityHigh, "320kmp3", "mp3", "48000", ""},
		{platform.QualityLossless, "2000kflac", "flac", "48000", "24"},
		{platform.QualityHiRes, "4000kflac", "flac", "96000", "24"},
	} {
		t.Run(tier.quality.String(), func(t *testing.T) {
			requests = nil
			info, err := client.GetDownloadInfo(ctx, trackID, tier.quality)
			if err != nil {
				t.Fatalf("resolve requests=%v: %s", requests, redactKuwoE2EError(err))
			}
			t.Logf("requested=%s actual=%s format=%s bytes=%d bitrate_kbps=%d requests=%v",
				tier.quality, info.Quality, info.Format, info.Size, info.Bitrate, requests)
			if info.Quality != tier.quality || !slices.Equal(requests, []string{tier.br}) {
				t.Fatal("fixture did not resolve the independently requested tier")
			}
			target := filepath.Join(t.TempDir(), tier.quality.String()+"."+info.Format)
			size, err := service.Download(ctx, info, target, nil)
			if err != nil {
				t.Fatalf("download: %s", redactKuwoE2EError(err))
			}
			if size != info.Size {
				t.Fatalf("download size=%d expected=%d", size, info.Size)
			}
			duration, err := download.VerifyFullAudio(ctx, target, detail.Duration)
			if err != nil {
				t.Fatalf("verify full audio: %s", redactKuwoE2EError(err))
			}
			output, err := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-select_streams", "a:0",
				"-show_entries", "stream=codec_name,sample_rate,channels,bits_per_raw_sample,bit_rate", "-of", "json", target).Output()
			if err != nil {
				t.Fatalf("ffprobe: %v", err)
			}
			var audio struct {
				Streams []struct {
					Codec    string `json:"codec_name"`
					Rate     string `json:"sample_rate"`
					Channels int    `json:"channels"`
					Depth    string `json:"bits_per_raw_sample"`
					Bitrate  string `json:"bit_rate"`
				} `json:"streams"`
			}
			if err := json.Unmarshal(output, &audio); err != nil || len(audio.Streams) != 1 {
				t.Fatal("invalid ffprobe audio stream result")
			}
			stream := audio.Streams[0]
			t.Logf("downloaded=%d duration=%.6fs codec=%s sample_rate=%s bit_depth=%s channels=%d bitrate_bps=%s",
				size, duration.Seconds(), stream.Codec, stream.Rate, stream.Depth, stream.Channels, stream.Bitrate)
			if stream.Codec != tier.codec || stream.Channels != 2 ||
				(tier.depth != "" && (stream.Depth != tier.depth || stream.Rate != tier.rate)) {
				t.Fatal("downloaded audio parameters do not match the fixture tier")
			}
			if (tier.quality == platform.QualityStandard && stream.Bitrate != "128000") ||
				(tier.quality == platform.QualityHigh && stream.Bitrate != "320000") {
				t.Fatal("downloaded MP3 bitrate does not match the requested tier")
			}
			if err := exec.CommandContext(ctx, "ffmpeg", "-v", "error", "-xerror", "-i", target, "-f", "null", "-").Run(); err != nil {
				t.Fatalf("full ffmpeg decode: %v", err)
			}
			t.Log("full ffmpeg decode passed")
		})
	}
}
