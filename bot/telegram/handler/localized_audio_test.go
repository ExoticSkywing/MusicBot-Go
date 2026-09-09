package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	botpkg "github.com/liuran001/MusicBot-Go/bot"
	"github.com/liuran001/MusicBot-Go/bot/id3"
	"github.com/mymmrac/telego"
	"go.senan.xyz/taglib"
)

type localizedAudioTestRepo struct {
	*stubSongRepository
	mu       sync.Mutex
	variants map[string]*botpkg.SongInfo
	saved    []*botpkg.SongInfo
}

func newLocalizedAudioTestRepo() *localizedAudioTestRepo {
	return &localizedAudioTestRepo{stubSongRepository: newStubRepo(), variants: make(map[string]*botpkg.SongInfo)}
}

func localizedAudioTestKey(platform, trackID, quality, language string) string {
	return strings.Join([]string{platform, trackID, quality, language}, "\x00")
}

func (r *localizedAudioTestRepo) FindLocalizedAudio(_ context.Context, platform, trackID, quality, language string) (*botpkg.SongInfo, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if song := r.variants[localizedAudioTestKey(platform, trackID, quality, language)]; song != nil {
		copy := *song
		return &copy, nil
	}
	return nil, nil
}

func (r *localizedAudioTestRepo) SaveLocalizedAudio(ctx context.Context, song *botpkg.SongInfo) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	copy := *song
	r.mu.Lock()
	defer r.mu.Unlock()
	r.saved = append(r.saved, &copy)
	r.variants[localizedAudioTestKey(copy.Platform, copy.TrackID, copy.Quality, copy.AudioLanguage)] = &copy
	return nil
}

func (r *localizedAudioTestRepo) DeleteLocalizedAudio(_ context.Context, platform, trackID, quality, language string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.variants, localizedAudioTestKey(platform, trackID, quality, language))
	return nil
}

func TestPrepareLocalizedAudioTransfersRetagsAndPreservesSource(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg required for cached audio verification")
	}
	sourcePath, cover := localizedAudioFixture(t, ffmpeg)
	sourceBytes, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	bot, getFileCalls, downloadCalls := newLocalizedAudioFileBot(t, sourceBytes, false)

	repo := newLocalizedAudioTestRepo()
	handler := &MusicHandler{Repo: repo, ID3Service: id3.NewID3Service(nil), CacheDir: t.TempDir()}
	source := localizedAudioSource(len(sourceBytes))
	wantSource := *source
	target := *source
	target.ID = 99
	target.SongName = "晴天"
	target.SongArtists = "周杰伦"
	target.SongAlbum = "叶惠美"
	target.MetadataLanguage = "zh"

	beforePayload, err := audioPayloadHash(context.Background(), sourcePath)
	if err != nil {
		t.Fatalf("hash source payload: %v", err)
	}
	path, cleanup, err := handler.prepareLocalizedAudio(zhCtx(), bot, source, &target)
	if err != nil {
		t.Fatalf("prepare localized audio: %v", err)
	}
	if cleanup == nil {
		t.Fatal("prepare localized audio returned no cleanup")
	}
	defer cleanup()

	if !reflect.DeepEqual(*source, wantSource) {
		t.Fatalf("source record mutated:\n got: %+v\nwant: %+v", *source, wantSource)
	}
	if target.ID != 0 || target.FileID != "" || target.AudioLanguage != "zh" {
		t.Fatalf("localized target identity = ID %d, FileID %q, AudioLanguage %q", target.ID, target.FileID, target.AudioLanguage)
	}
	if target.MusicSize <= 0 {
		t.Fatalf("localized target size = %d", target.MusicSize)
	}
	if getFileCalls.Load() != 1 || downloadCalls.Load() != 1 {
		t.Fatalf("Telegram transfer calls = getFile %d, download %d", getFileCalls.Load(), downloadCalls.Load())
	}

	tags, err := taglib.ReadTags(path)
	if err != nil {
		t.Fatalf("read localized tags: %v", err)
	}
	for key, want := range map[string]string{
		taglib.Title:       "晴天",
		taglib.Artist:      "周杰伦",
		taglib.Album:       "叶惠美",
		taglib.AlbumArtist: "Original Album Artist",
		taglib.Lyrics:      "[00:00.00]Original lyrics",
	} {
		if got := firstLocalizedAudioTag(tags[key]); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	gotCover, err := taglib.ReadImage(path)
	if err != nil {
		t.Fatalf("read localized cover: %v", err)
	}
	if !bytes.Equal(gotCover, cover) {
		t.Error("localized audio cover changed")
	}
	afterPayload, err := audioPayloadHash(context.Background(), path)
	if err != nil {
		t.Fatalf("hash localized payload: %v", err)
	}
	if afterPayload != beforePayload {
		t.Fatalf("audio payload changed: got %q, want %q", afterPayload, beforePayload)
	}

	originalTags, err := taglib.ReadTags(sourcePath)
	if err != nil {
		t.Fatalf("read original tags: %v", err)
	}
	if got := firstLocalizedAudioTag(originalTags[taglib.Title]); got != "Original Title" {
		t.Fatalf("original file title changed to %q", got)
	}
	repo.mu.Lock()
	if len(repo.saved) != 1 || !reflect.DeepEqual(*repo.saved[0], wantSource) {
		t.Fatalf("preserved source snapshots = %+v, want %+v", repo.saved, wantSource)
	}
	repo.mu.Unlock()

	tempDir := filepath.Dir(path)
	cleanup()
	if _, err := os.Stat(tempDir); !os.IsNotExist(err) {
		t.Fatalf("temporary directory remains after cleanup: %v", err)
	}
}

func TestPrepareLocalizedAudioTransferFailureLeavesRecordsUntouched(t *testing.T) {
	bot, _, downloadCalls := newLocalizedAudioFileBot(t, []byte("not returned"), true)
	repo := newLocalizedAudioTestRepo()
	cacheDir := t.TempDir()
	handler := &MusicHandler{Repo: repo, ID3Service: id3.NewID3Service(nil), CacheDir: cacheDir}
	source := localizedAudioSource(123)
	wantSource := *source
	target := *source
	target.SongName = "晴天"
	target.MetadataLanguage = "zh"
	wantTarget := target

	_, cleanup, err := handler.prepareLocalizedAudio(zhCtx(), bot, source, &target)
	if err == nil {
		t.Fatal("transfer failure unexpectedly succeeded")
	}
	if cleanup != nil {
		cleanup()
	}
	if !reflect.DeepEqual(*source, wantSource) || !reflect.DeepEqual(target, wantTarget) {
		t.Fatalf("failed transfer mutated records: source=%+v target=%+v", *source, target)
	}
	if downloadCalls.Load() == 0 {
		t.Fatal("test did not reach Telegram file transfer")
	}
	entries, readErr := os.ReadDir(cacheDir)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("failed transfer left temporary entries: %v", entries)
	}
}

func TestFindLanguageAudioUsesExactVariant(t *testing.T) {
	repo := newLocalizedAudioTestRepo()
	primary := localizedAudioSource(100)
	repo.platformSongs["applemusic:track-1:high"] = primary
	variant := *primary
	variant.FileID = "zh-file"
	variant.MetadataLanguage = "zh"
	variant.AudioLanguage = "zh"
	repo.variants[localizedAudioTestKey("applemusic", "track-1", "high", "zh")] = &variant

	got, err := findLanguageAudio(zhCtx(), repo, "applemusic", "track-1", "high")
	if err != nil {
		t.Fatalf("find exact language audio: %v", err)
	}
	if got == nil || got.FileID != "zh-file" || got.AudioLanguage != "zh" {
		t.Fatalf("exact language audio = %+v", got)
	}
}

func TestTryLocalizedInlineAudioSingleflightUploadsOnce(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg required for cached audio verification")
	}
	sourcePath, _ := localizedAudioFixture(t, ffmpeg)
	audio, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}

	downloadStarted := make(chan struct{})
	releaseDownload := make(chan struct{})
	abortServer := make(chan struct{})
	var downloadOnce sync.Once
	var getFileCalls, downloadCalls, uploadCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/getFile"):
			getFileCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true,
				"result": map[string]any{
					"file_id":        "cached-file",
					"file_unique_id": "cached-file-unique",
					"file_size":      len(audio),
					"file_path":      "music/source.m4a",
				},
			})
		case strings.Contains(r.URL.Path, "/file/bot"):
			downloadCalls.Add(1)
			downloadOnce.Do(func() { close(downloadStarted) })
			select {
			case <-releaseDownload:
			case <-abortServer:
				return
			}
			_, _ = w.Write(audio)
		case strings.HasSuffix(r.URL.Path, "/sendAudio"):
			uploadCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true,
				"result": map[string]any{
					"message_id": 1,
					"date":       1,
					"chat":       map[string]any{"id": -100, "type": "supergroup"},
					"audio": map[string]any{
						"file_id":        "localized-file-id",
						"file_unique_id": "localized-file-unique",
						"duration":       1,
						"file_size":      len(audio),
					},
				},
			})
		default:
			http.Error(w, "unexpected Telegram path", http.StatusNotFound)
		}
	}))
	t.Cleanup(func() {
		close(abortServer)
		server.Close()
	})
	bot, err := telego.NewBot("123456:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghi", telego.WithAPIServer(server.URL), telego.WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatalf("create Telegram bot: %v", err)
	}

	repo := newLocalizedAudioTestRepo()
	source := localizedAudioSource(len(audio))
	source.SongName = "晴天"
	source.SongArtists = "周杰伦"
	source.SongAlbum = "叶惠美"
	source.MetadataLanguage = "zh"
	repo.platformSongs["applemusic:track-1:high"] = source
	handler := &MusicHandler{
		Repo:               repo,
		ID3Service:         id3.NewID3Service(nil),
		CacheDir:           t.TempDir(),
		InlineUploadChatID: -100,
		BotName:            "test_bot",
	}

	start := make(chan struct{})
	results := make(chan *botpkg.SongInfo, 2)
	errors := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			song, err := handler.tryLocalizedInlineAudio(zhCtx(), bot, "applemusic", "track-1", "high", nil)
			results <- song
			errors <- err
		}()
	}
	close(start)
	select {
	case <-downloadStarted:
	case <-time.After(time.Second):
		t.Fatal("localized download did not start")
	}
	// Give the second caller time to join the in-flight singleflight request.
	time.Sleep(20 * time.Millisecond)
	close(releaseDownload)
	for range 2 {
		if err := <-errors; err != nil {
			t.Fatalf("try localized inline audio: %v", err)
		}
		song := <-results
		if song == nil || song.FileID != "localized-file-id" || song.AudioLanguage != "zh" {
			t.Fatalf("localized inline result = %+v", song)
		}
	}
	if getFileCalls.Load() != 1 || downloadCalls.Load() != 1 || uploadCalls.Load() != 1 {
		t.Fatalf("singleflight calls = getFile %d, download %d, upload %d", getFileCalls.Load(), downloadCalls.Load(), uploadCalls.Load())
	}
}

func TestPrepareLocalizedAudioCanceledBeforeStaging(t *testing.T) {
	cachedAudioRetagSlots <- struct{}{}
	cachedAudioRetagSlots <- struct{}{}
	defer func() {
		<-cachedAudioRetagSlots
		<-cachedAudioRetagSlots
	}()

	repo := newLocalizedAudioTestRepo()
	cacheDir := t.TempDir()
	handler := &MusicHandler{Repo: repo, ID3Service: id3.NewID3Service(nil), CacheDir: cacheDir}
	source := localizedAudioSource(100)
	target := *source
	target.MetadataLanguage = "zh"
	ctx, cancel := context.WithCancel(zhCtx())
	cancel()
	_, cleanup, err := handler.prepareLocalizedAudio(ctx, nil, source, &target)
	if cleanup != nil {
		cleanup()
	}
	if err == nil {
		t.Fatal("canceled staging unexpectedly succeeded")
	}
	entries, readErr := os.ReadDir(cacheDir)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("canceled staging left temporary entries: %v", entries)
	}
}

func TestUploadShutdownCancelsLocalizedAudioRetrieval(t *testing.T) {
	requestStarted := make(chan struct{})
	abortServer := make(chan struct{})
	var startedOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/getFile"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true,
				"result": map[string]any{
					"file_id":        "cached-file",
					"file_unique_id": "cached-file-unique",
					"file_size":      100,
					"file_path":      "music/source.m4a",
				},
			})
		case strings.Contains(r.URL.Path, "/file/bot"):
			startedOnce.Do(func() { close(requestStarted) })
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("partial"))
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			select {
			case <-r.Context().Done():
			case <-abortServer:
			}
		default:
			http.Error(w, "unexpected Telegram path", http.StatusNotFound)
		}
	}))
	t.Cleanup(func() {
		close(abortServer)
		server.Close()
	})
	bot, err := telego.NewBot("123456:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghi", telego.WithAPIServer(server.URL), telego.WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatalf("create Telegram bot: %v", err)
	}

	cacheDir := t.TempDir()
	handler := &MusicHandler{Repo: newLocalizedAudioTestRepo(), ID3Service: id3.NewID3Service(nil), CacheDir: cacheDir}
	source := localizedAudioSource(100)
	target := *source
	target.MetadataLanguage = "zh"
	result := make(chan error, 1)
	go func() {
		_, cleanup, err := handler.prepareLocalizedAudio(zhCtx(), bot, source, &target)
		if cleanup != nil {
			cleanup()
		}
		result <- err
	}()

	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("localized audio retrieval did not start")
	}
	handler.prepareMu.Lock()
	activeRetags := len(handler.audioRetags)
	handler.prepareMu.Unlock()
	if activeRetags != 1 {
		t.Fatalf("active localized audio jobs = %d, want 1", activeRetags)
	}
	handler.BeginUploadShutdown()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("shutdown-canceled retrieval unexpectedly succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("localized audio retrieval did not stop on shutdown")
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := handler.ShutdownUploads(waitCtx); err != nil {
		t.Fatalf("wait for localized audio shutdown: %v", err)
	}
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("shutdown left localized audio temporary entries: %v", entries)
	}
}

func localizedAudioSource(size int) *botpkg.SongInfo {
	return &botpkg.SongInfo{
		ID:               7,
		Platform:         "applemusic",
		TrackID:          "track-1",
		Quality:          "high",
		SongName:         "Original Title",
		SongArtists:      "Original Artist",
		SongAlbum:        "Original Album",
		FileExt:          "m4a",
		MusicSize:        size,
		Duration:         1,
		AudioValidated:   true,
		FileID:           "cached-file",
		MetadataLanguage: "en",
		AudioLanguage:    "en",
	}
}

func localizedAudioFixture(t *testing.T, ffmpeg string) (string, []byte) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "source.m4a")
	cmd := exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=1", "-c:a", "aac", "-b:a", "64k", "-y", path)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate audio fixture: %v: %s", err, output)
	}
	tags := map[string][]string{
		taglib.Title:       {"Original Title"},
		taglib.Artist:      {"Original Artist"},
		taglib.Album:       {"Original Album"},
		taglib.AlbumArtist: {"Original Album Artist"},
		taglib.Lyrics:      {"[00:00.00]Original lyrics"},
	}
	if err := taglib.WriteTags(path, tags, taglib.Clear); err != nil {
		t.Fatalf("seed audio tags: %v", err)
	}
	cover := localizedAudioCover(t)
	if err := taglib.WriteImage(path, cover); err != nil {
		t.Fatalf("seed audio cover: %v", err)
	}
	return path, cover
}

func localizedAudioCover(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.NRGBA{R: 0xff, A: 0xff})
	img.Set(1, 1, color.NRGBA{B: 0xff, A: 0xff})
	var output bytes.Buffer
	if err := png.Encode(&output, img); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func newLocalizedAudioFileBot(t *testing.T, audio []byte, failDownload bool) (*telego.Bot, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	getFileCalls := &atomic.Int32{}
	downloadCalls := &atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/getFile"):
			getFileCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true,
				"result": map[string]any{
					"file_id":        "cached-file",
					"file_unique_id": "cached-file-unique",
					"file_size":      len(audio),
					"file_path":      "music/source.m4a",
				},
			})
		case strings.Contains(r.URL.Path, "/file/bot"):
			downloadCalls.Add(1)
			if failDownload {
				http.Error(w, "transfer failed", http.StatusBadGateway)
				return
			}
			w.Header().Set("Content-Length", "")
			_, _ = io.Copy(w, bytes.NewReader(audio))
		default:
			http.Error(w, "unexpected Telegram path", http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	bot, err := telego.NewBot("123456:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghi", telego.WithAPIServer(server.URL), telego.WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatalf("create Telegram bot: %v", err)
	}
	return bot, getFileCalls, downloadCalls
}

func firstLocalizedAudioTag(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}
