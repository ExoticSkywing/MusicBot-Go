package handler

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	botpkg "github.com/liuran001/MusicBot-Go/bot"
	"github.com/liuran001/MusicBot-Go/bot/download"
	"github.com/liuran001/MusicBot-Go/bot/id3"
	"github.com/liuran001/MusicBot-Go/bot/platform"
)

// One second of real PCM audio; lifecycle tests should exercise the same media
// validation as production rather than treating arbitrary bytes as a song.
func preparedAudioWAV(t *testing.T) []byte {
	t.Helper()
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe required for prepared audio tests")
	}
	const dataSize = 8000 * 2
	data := make([]byte, 44+dataSize)
	copy(data, "RIFF")
	binary.LittleEndian.PutUint32(data[4:], uint32(len(data)-8))
	copy(data[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(data[16:], 16)
	binary.LittleEndian.PutUint16(data[20:], 1)
	binary.LittleEndian.PutUint16(data[22:], 1)
	binary.LittleEndian.PutUint32(data[24:], 8000)
	binary.LittleEndian.PutUint32(data[28:], 16000)
	binary.LittleEndian.PutUint16(data[32:], 2)
	binary.LittleEndian.PutUint16(data[34:], 16)
	copy(data[36:], "data")
	binary.LittleEndian.PutUint32(data[40:], dataSize)
	return data
}

var completeAudioPlatforms = []string{
	"netease", "qqmusic", "kugou", "kuwo", "soda", "bilibili",
	"applemusic", "spotify", "youtubemusic",
	"migu", "qianqian", "fivesing", "jamendo", "joox",
}

func TestPrepareRejectsPreviewForEveryPlatform(t *testing.T) {
	payload := preparedAudioWAV(t)
	for _, name := range completeAudioPlatforms {
		t.Run(name, func(t *testing.T) {
			cacheDir := t.TempDir()
			h := &MusicHandler{CacheDir: cacheDir, DownloadService: download.NewDownloadService(download.DownloadServiceOptions{})}
			info := &platform.DownloadInfo{URL: "test://audio", Format: "wav", Downloader: func(_ context.Context, _ *platform.DownloadInfo, path string, _ func(int64, int64)) (int64, error) {
				return int64(len(payload)), os.WriteFile(path, payload, 0600)
			}}
			track := &platform.Track{ID: "1", Title: "Preview", Duration: time.Minute}
			song := &botpkg.SongInfo{}
			path, _, _, err := h.downloadAndPrepareFromPlatform(context.Background(), newStubPlatform(name), track, track.ID, info, nil, nil, nil, song, nil)
			if !errors.Is(err, platform.ErrIncompleteAudio) || path != "" || song.AudioValidated {
				t.Fatalf("preview escaped: path=%q validated=%t err=%v", path, song.AudioValidated, err)
			}
			files, err := os.ReadDir(cacheDir)
			if err != nil || len(files) != 0 {
				t.Fatalf("rejected audio not cleaned: %v, %v", files, err)
			}
		})
	}
}

func TestPrepareRejectsUnknownCatalogDurationBeforeDownload(t *testing.T) {
	h := &MusicHandler{CacheDir: t.TempDir(), DownloadService: download.NewDownloadService(download.DownloadServiceOptions{})}
	info := &platform.DownloadInfo{URL: "test://audio", Downloader: func(context.Context, *platform.DownloadInfo, string, func(int64, int64)) (int64, error) {
		t.Fatal("download attempted without a full catalog duration")
		return 0, nil
	}}
	_, _, _, err := h.downloadAndPrepareFromPlatform(context.Background(), nil, &platform.Track{Title: "Unknown"}, "1", info, nil, nil, nil, &botpkg.SongInfo{}, nil)
	if !errors.Is(err, platform.ErrIncompleteAudio) {
		t.Fatalf("got %v", err)
	}
}

func TestUnverifiedAudioCacheCannotBeReused(t *testing.T) {
	for _, name := range completeAudioPlatforms {
		cached := &botpkg.SongInfo{Platform: name, FileID: "legacy", Quality: "high", Duration: 180, QualityVerified: true}
		if isReusableCachedSong(cached, name, "high") {
			t.Fatalf("%s reused unverified file", name)
		}
		cached.AudioValidated = true
		if !isReusableCachedSong(cached, name, "high") {
			t.Fatalf("%s rejected validated file", name)
		}
	}
	ctx := context.Background()
	repo := newStubRepo()
	repo.platformSongs["netease:1:high"] = &botpkg.SongInfo{Platform: "netease", TrackID: "1", Quality: "high", FileID: "old-preview"}
	h := &MusicHandler{Repo: repo, DefaultQuality: "high"}
	if cached, _, err := h.findInlineCachedSong(ctx, 1, 0, false, "netease", "1", "high"); err != nil || cached != nil {
		t.Fatalf("inline reused unverified file: %v, %v", cached, err)
	}
	_, sent, err := h.trySendCachedTrack(ctx, nil, nil, nil, "netease", "1", "high", true,
		func(platformName, id, quality string) (*botpkg.SongInfo, error) {
			return repo.FindByPlatformTrackID(ctx, platformName, id, quality)
		}, nil)
	if err != nil || sent {
		t.Fatalf("direct path reused unverified file: sent=%t err=%v", sent, err)
	}
}

func TestSendMusicDirectRejectsUnverifiedAudio(t *testing.T) {
	h := &MusicHandler{}
	for _, fileID := range []string{"", "old-preview"} {
		result := h.sendMusicDirect(context.Background(), nil, nil, &botpkg.SongInfo{FileID: fileID}, "", "", false)
		if !errors.Is(result.err, platform.ErrIncompleteAudio) {
			t.Fatalf("unverified audio reached delivery: %v", result.err)
		}
	}
}

type audioValidationTagProvider func() (*id3.TagData, error)

func (f audioValidationTagProvider) GetTagData(context.Context, *platform.Track, *platform.DownloadInfo) (*id3.TagData, error) {
	return f()
}

func TestPrepareValidatesFinalFileAfterTagProcessing(t *testing.T) {
	payload := preparedAudioWAV(t)
	cacheDir := t.TempDir()
	providerCalled := false
	h := &MusicHandler{
		CacheDir:        cacheDir,
		DownloadService: download.NewDownloadService(download.DownloadServiceOptions{}),
		ID3Service:      id3.NewID3Service(nil),
		TagProviders: map[string]id3.ID3TagProvider{"test": audioValidationTagProvider(func() (*id3.TagData, error) {
			providerCalled = true
			paths, err := filepath.Glob(filepath.Join(cacheDir, "*", "*.wav"))
			if err != nil || len(paths) != 1 {
				t.Fatalf("final media path: %v, %v", paths, err)
			}
			// Model a metadata step that damages an otherwise complete file.
			if err := os.WriteFile(paths[0], []byte("corrupted during tagging"), 0600); err != nil {
				t.Fatal(err)
			}
			return &id3.TagData{Title: "Song"}, nil
		})},
	}
	info := &platform.DownloadInfo{URL: "test://audio", Format: "wav", Downloader: func(_ context.Context, _ *platform.DownloadInfo, path string, _ func(int64, int64)) (int64, error) {
		return int64(len(payload)), os.WriteFile(path, payload, 0600)
	}}
	song := &botpkg.SongInfo{}
	_, _, _, err := h.downloadAndPrepareFromPlatform(context.Background(), newStubPlatform("test"), &platform.Track{Title: "Song", Duration: time.Second}, "1", info, nil, nil, nil, song, nil)
	if !providerCalled || !errors.Is(err, platform.ErrIncompleteAudio) || song.AudioValidated {
		t.Fatalf("final file escaped verification: tags=%t validated=%t err=%v", providerCalled, song.AudioValidated, err)
	}
}
