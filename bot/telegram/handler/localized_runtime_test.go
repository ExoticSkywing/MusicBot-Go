package handler

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/liuran001/MusicBot-Go/bot/id3"
	"go.senan.xyz/taglib"
)

// This fixture uses only codecs enabled in the production image, so the test
// binary can exercise cached retagging there without installing full FFmpeg.
func TestPrepareLocalizedAudioWithMinimalFFmpeg(t *testing.T) {
	for _, name := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Skip(name + " required")
		}
	}
	sourcePath := filepath.Join(t.TempDir(), "source.flac")
	cmd := exec.Command("ffmpeg", "-v", "error", "-f", "s16le", "-ar", "44100", "-ac", "1", "-i", "pipe:0", "-c:a", "flac", sourcePath)
	cmd.Stdin = bytes.NewReader(make([]byte, 44100*2))
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create FLAC fixture: %v: %s", err, output)
	}
	if err := taglib.WriteTags(sourcePath, map[string][]string{taglib.Title: {"Original Title"}, taglib.Lyrics: {"Original lyrics"}}, taglib.Clear); err != nil {
		t.Fatal(err)
	}
	cover := localizedAudioCover(t)
	if err := taglib.WriteImage(sourcePath, cover); err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	bot, getFileCalls, downloadCalls := newLocalizedAudioFileBot(t, payload, false)
	source := localizedAudioSource(len(payload))
	source.FileExt = "flac"
	source.CoverFileID = "standalone-cover"
	source.CoverURL = "https://img.example/cover.jpg"
	target := *source
	target.SongName, target.SongArtists, target.SongAlbum = "中文歌名", "中文歌手", "中文专辑"
	target.MetadataLanguage = "zh"
	h := &MusicHandler{Repo: newLocalizedAudioTestRepo(), ID3Service: id3.NewID3Service(nil), CacheDir: t.TempDir()}
	path, cleanup, err := h.prepareLocalizedAudio(zhCtx(), bot, source, &target)
	if err != nil {
		t.Fatalf("retag with production FFmpeg: %v", err)
	}
	if cleanup == nil {
		t.Fatal("missing retag cleanup")
	}
	defer cleanup()
	tags, err := taglib.ReadTags(path)
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{taglib.Title: target.SongName, taglib.Artist: target.SongArtists, taglib.Album: target.SongAlbum, taglib.Lyrics: "Original lyrics"} {
		if got := firstLocalizedAudioTag(tags[key]); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	artwork, err := taglib.ReadImage(path)
	if err != nil || !bytes.Equal(artwork, cover) || target.CoverFileID != source.CoverFileID || target.CoverURL != source.CoverURL {
		t.Fatalf("retag changed embedded or standalone cover: %v", err)
	}
	before, err := audioPayloadHash(context.Background(), sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	after, err := audioPayloadHash(context.Background(), path)
	if err != nil || before != after {
		t.Fatalf("audio payload changed: %q -> %q, %v", before, after, err)
	}
	if target.AudioLanguage != "zh" || target.FileID != "" || source.FileID != "cached-file" || getFileCalls.Load() != 1 || downloadCalls.Load() != 1 {
		t.Fatal("retag did not preserve source and retrieve exactly one cached audio file")
	}
	cleanup()
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatalf("temporary retag directory remains: %v", err)
	}
}
