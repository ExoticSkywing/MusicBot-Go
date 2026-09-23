//go:build live

package handler

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
	"time"

	botpkg "github.com/liuran001/MusicBot-Go/bot"
	"github.com/liuran001/MusicBot-Go/bot/download"
	"github.com/liuran001/MusicBot-Go/bot/id3"
	logpkg "github.com/liuran001/MusicBot-Go/bot/logger"
	"github.com/liuran001/MusicBot-Go/bot/platform"
	"github.com/liuran001/MusicBot-Go/plugins/douyin"
)

// Live end-to-end check of a Douyin original sound through the same prepare
// path production uses: CDN download, cover, tag embedding and full-audio
// verification. Run:
//
//	go test -tags live -run TestLiveDouyinPrepare ./bot/telegram/handler/ -v
//
// Requires network and ffprobe. Not part of default test runs.
func TestLiveDouyinPrepare(t *testing.T) {
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe required")
	}
	ctx := context.Background()
	plat := douyin.NewPlatform(douyin.NewClient(nil, 20*time.Second))
	defer plat.Close()

	trackID, ok := plat.MatchText("@小鱼儿嘤嘤创作的原声 https://www.douyin.com/music/7687226746303580947")
	if !ok {
		t.Fatal("share text not matched")
	}
	track, err := plat.GetTrack(ctx, trackID)
	if err != nil {
		t.Fatalf("GetTrack: %v", err)
	}
	info, err := plat.GetDownloadInfo(ctx, trackID, platform.QualityHiRes)
	if err != nil {
		t.Fatalf("GetDownloadInfo: %v", err)
	}

	h := &MusicHandler{
		CacheDir: t.TempDir(),
		DownloadService: download.NewDownloadService(download.DownloadServiceOptions{
			Timeout:              60 * time.Second,
			EnableMultipart:      true,
			MultipartConcurrency: 4,
			MultipartMinSize:     256 * 1024,
		}),
		ID3Service: id3.NewID3Service(nil),
	}
	if log, err := logpkg.New("debug", "text", false); err == nil {
		h.Logger = log
	}
	song := &botpkg.SongInfo{}
	fillSongInfoFromTrack(song, track, plat.Name(), trackID, nil)

	audioPath, _, cleanup, err := h.downloadAndPrepareFromPlatform(ctx, plat, track, trackID, info, nil, nil, nil, song, nil)
	defer func() { _ = cleanupFiles(cleanup...) }()
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if !song.AudioValidated {
		t.Fatal("audio not validated")
	}
	t.Logf("prepared %s: %ds, %d bps, %d bytes, cover %d bytes", audioPath, song.Duration, song.BitRate, song.MusicSize, song.EmbPicSize)

	out, err := exec.Command("ffprobe", "-v", "error", "-show_entries",
		"format=format_name,duration:format_tags=title,artist:stream=codec_name,codec_type,bit_rate,sample_rate",
		"-of", "json", audioPath).Output()
	if err != nil {
		t.Fatalf("ffprobe: %v", err)
	}
	var probe struct {
		Streams []struct {
			CodecName  string `json:"codec_name"`
			CodecType  string `json:"codec_type"`
			BitRate    string `json:"bit_rate"`
			SampleRate string `json:"sample_rate"`
		} `json:"streams"`
		Format struct {
			FormatName string            `json:"format_name"`
			Duration   string            `json:"duration"`
			Tags       map[string]string `json:"tags"`
		} `json:"format"`
	}
	if err := json.Unmarshal(out, &probe); err != nil {
		t.Fatalf("decode ffprobe: %v", err)
	}
	t.Logf("ffprobe: %s", strings.TrimSpace(string(out)))

	var hasAudio, hasCover bool
	for _, stream := range probe.Streams {
		switch {
		case stream.CodecType == "audio" && stream.CodecName == "mp3":
			hasAudio = true
		case stream.CodecType == "video":
			hasCover = true // ID3 APIC 封面在 ffprobe 里表现为 attached_pic 视频流
		}
	}
	if !hasAudio || !hasCover {
		t.Fatalf("streams = %+v, want mp3 audio with embedded cover", probe.Streams)
	}
	if probe.Format.Tags["title"] != track.Title || probe.Format.Tags["artist"] != "小鱼儿嘤嘤" {
		t.Fatalf("tags = %v", probe.Format.Tags)
	}
}
