//go:build live

package soda

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/liuran001/MusicBot-Go/bot/platform"
)

// Opt-in, read-only integration check; keep credentials outside the repository:
// SODA_LIVE_COOKIE_FILE=/private/path/cookie go test -tags live ./plugins/soda -run '^TestLiveWebAPI$' -v
func TestLiveWebAPI(t *testing.T) {
	cookieFile := os.Getenv("SODA_LIVE_COOKIE_FILE")
	if cookieFile == "" {
		t.Skip("SODA_LIVE_COOKIE_FILE not set")
	}
	cookie, err := os.ReadFile(cookieFile)
	if err != nil {
		t.Fatal("cannot read cookie file")
	}
	if strings.TrimSpace(string(cookie)) == "" {
		t.Fatal("cookie file is empty")
	}
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("%s required for live audio validation", tool)
		}
	}
	client := NewClient(strings.TrimSpace(string(cookie)), 30*time.Second, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	safeError := func(err error) string {
		return regexp.MustCompile(`https?://[^\s"<>]+`).ReplaceAllString(err.Error(), "[URL redacted]")
	}
	tracks, err := client.Search(ctx, "周杰伦", 3)
	if err != nil {
		t.Fatalf("search: %s", safeError(err))
	}
	if len(tracks) == 0 {
		t.Fatal("search returned no tracks")
	}
	t.Logf("search: %d tracks", len(tracks))
	track, lyric, err := client.GetTrack(ctx, tracks[0].ID)
	if err != nil {
		t.Fatalf("track: %s", safeError(err))
	}
	if track == nil || track.ID != tracks[0].ID || lyric == "" {
		t.Fatal("track metadata or lyrics missing")
	}
	t.Logf("track: %s; metadata duration=%.3fs; lyric bytes=%d", track.Title, track.Duration.Seconds(), len(lyric))
	if tracks[0].CoverURL == "" {
		t.Fatal("search cover missing")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tracks[0].CoverURL, nil)
	if err != nil {
		t.Fatal("invalid cover URL")
	}
	cover, err := client.httpClient.Do(req)
	if err != nil {
		t.Fatalf("cover: %s", safeError(err))
	}
	cover.Body.Close()
	if cover.StatusCode != http.StatusOK {
		t.Fatalf("cover HTTP %d", cover.StatusCode)
	}
	info, err := client.FetchDownloadInfo(ctx, track.ID, platform.QualityHiRes)
	if err != nil {
		t.Fatalf("playback: %s", safeError(err))
	}
	if info.Bitrate <= 0 || info.Bitrate > 10000 {
		t.Fatalf("invalid bitrate in kbps: %d", info.Bitrate)
	}
	dest := filepath.Join(t.TempDir(), "audio.m4a")
	written, err := client.DownloadAndDecrypt(ctx, info, dest, nil)
	if err != nil {
		t.Fatalf("download: %s", safeError(err))
	}
	if info.Format == "flac" {
		dest = strings.TrimSuffix(dest, filepath.Ext(dest)) + ".flac"
	}
	st, err := os.Stat(dest)
	if err != nil || written < 10000 || st.Size() != written {
		t.Fatal("download size validation failed")
	}
	probe, err := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-show_entries", "format=duration:stream=codec_name", "-of", "json", dest).Output()
	if err != nil {
		t.Fatal("downloaded audio ffprobe failed")
	}
	var media struct {
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
		Streams []struct {
			Codec string `json:"codec_name"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(probe, &media); err != nil {
		t.Fatal("invalid ffprobe JSON")
	}
	duration, _ := strconv.ParseFloat(media.Format.Duration, 64)
	if duration < 1 || len(media.Streams) == 0 {
		t.Fatal("downloaded audio has no playable duration")
	}
	if err := exec.CommandContext(ctx, "ffmpeg", "-v", "error", "-xerror", "-i", dest, "-f", "null", "-").Run(); err != nil {
		t.Fatal("downloaded audio failed full decode")
	}
	t.Logf("audio: codec=%s bitrate=%dkbps bytes=%d duration=%.3fs preview=%t; full decode passed", media.Streams[0].Codec, info.Bitrate, written, duration, track.Duration.Seconds()-duration > 5)
	playlists, err := client.SearchPlaylist(ctx, "周杰伦", 2)
	if err != nil {
		t.Fatalf("playlist search: %s", safeError(err))
	}
	if len(playlists) == 0 {
		t.Fatal("playlist search returned no results")
	}
	plctx := platform.WithPlaylistLimit(ctx, 5)
	playlist, err := client.GetPlaylist(plctx, playlists[0].ID)
	if err != nil {
		t.Fatalf("playlist: %s", safeError(err))
	}
	if playlist == nil || len(playlist.Tracks) == 0 || len(playlist.Tracks) > 5 {
		t.Fatal("playlist contents or limit invalid")
	}
	t.Logf("playlist: %d/%d tracks", len(playlist.Tracks), playlist.TrackCount)
}
