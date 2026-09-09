package handler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	botpkg "github.com/liuran001/MusicBot-Go/bot"
	"github.com/liuran001/MusicBot-Go/bot/download"
	"github.com/liuran001/MusicBot-Go/bot/i18n"
	"github.com/mymmrac/telego"
)

type localizedAudioRepository interface {
	FindLocalizedAudio(context.Context, string, string, string, string) (*botpkg.SongInfo, error)
	SaveLocalizedAudio(context.Context, *botpkg.SongInfo) error
	DeleteLocalizedAudio(context.Context, string, string, string, string) error
}

func findLanguageAudio(ctx context.Context, repo botpkg.SongRepository, p, id, q string) (*botpkg.SongInfo, error) {
	if repo == nil {
		return nil, nil
	}
	if storage, ok := repo.(localizedAudioRepository); ok && isAppleMusicPlatform(p) {
		song, err := storage.FindLocalizedAudio(ctx, p, id, q, i18n.From(ctx).Lang())
		if err == nil && song != nil && song.FileID != "" && isReusableCachedSong(song, p, q) {
			return song, nil
		}
	}
	song, primaryErr := repo.FindByPlatformTrackID(ctx, p, id, q)
	if song != nil && (!isAppleMusicPlatform(p) || (song.FileID != "" && isReusableCachedSong(song, p, q))) {
		return song, primaryErr
	}
	if storage, ok := repo.(interface {
		FindCachedAudioSource(context.Context, string, string, string) (*botpkg.SongInfo, error)
	}); ok && isAppleMusicPlatform(p) {
		if fallback, err := storage.FindCachedAudioSource(ctx, p, id, q); err == nil && fallback != nil && fallback.FileID != "" && isReusableCachedSong(fallback, p, q) {
			return fallback, nil
		}
	}
	return song, primaryErr
}

func needsLocalizedAudio(ctx context.Context, song *botpkg.SongInfo) bool {
	return song != nil && isAppleMusicPlatform(song.Platform) && song.MetadataLanguage == i18n.From(ctx).Lang() && song.AudioLanguage != song.MetadataLanguage
}

// A failure is retried on the next user request, not at every cache checkpoint
// within the current request. Other languages and original audio stay intact.
type audioRetagAttemptKey struct{}
type audioRetagAttempts struct {
	sync.Mutex
	files map[string]bool
}

func withAudioRetagAttempts(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, audioRetagAttemptKey{}, &audioRetagAttempts{files: make(map[string]bool)})
}
func beginAudioRetag(ctx context.Context, song *botpkg.SongInfo) bool {
	attempts, _ := ctx.Value(audioRetagAttemptKey{}).(*audioRetagAttempts)
	if attempts == nil {
		return true
	}
	key := song.FileID + ":" + i18n.From(ctx).Lang()
	attempts.Lock()
	defer attempts.Unlock()
	if attempts.files[key] {
		return false
	}
	attempts.files[key] = true
	return true
}

// Limit disk/network work independently from the Apple playback gate. A cached
// retag can also run at cache checkpoints that already hold the download slot.
var cachedAudioRetagSlots = make(chan struct{}, 2)

type audioRetagJob struct{ cancel context.CancelFunc }

func (h *MusicHandler) prepareLocalizedAudio(ctx context.Context, b *telego.Bot, source, target *botpkg.SongInfo) (path string, cleanup func(), err error) {
	if h == nil || h.ID3Service == nil || source == nil || target == nil || !source.AudioValidated || source.FileID == "" || !needsLocalizedAudio(ctx, target) {
		return "", nil, errors.New("localized audio unavailable")
	}
	if !beginAudioRetag(ctx, source) {
		return "", nil, errors.New("cached audio retrieval already attempted")
	}
	transferCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	job := &audioRetagJob{cancel: cancel}
	h.prepareMu.Lock()
	if h.prepareShuttingDown {
		h.prepareMu.Unlock()
		return "", nil, errUploadShuttingDown
	}
	if h.audioRetags == nil {
		h.audioRetags = make(map[*audioRetagJob]struct{})
	}
	h.audioRetags[job] = struct{}{}
	h.prepareWG.Add(1)
	h.prepareMu.Unlock()
	defer func() { h.prepareMu.Lock(); delete(h.audioRetags, job); h.prepareMu.Unlock(); h.prepareWG.Done() }()
	select {
	case cachedAudioRetagSlots <- struct{}{}:
		defer func() { <-cachedAudioRetagSlots }()
	case <-transferCtx.Done():
		return "", nil, transferCtx.Err()
	}
	// Preserve even a legacy, unmarked original before any later primary-row upsert.
	if storage, ok := h.Repo.(localizedAudioRepository); ok {
		if err := storage.SaveLocalizedAudio(transferCtx, source); err != nil {
			return "", nil, errors.New("cannot preserve original audio cache")
		}
	} else {
		return "", nil, errors.New("localized audio storage unavailable")
	}
	dir := h.CacheDir
	if dir == "" {
		dir = os.TempDir()
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", nil, err
	}
	tempDir, err := os.MkdirTemp(dir, strconv.FormatInt(time.Now().UnixMicro(), 10)+"-*.retag")
	if err != nil {
		return "", nil, err
	}
	cleanup = func() { _ = os.RemoveAll(tempDir) }
	defer func() {
		if err != nil {
			cleanup()
		}
	}()
	ext := strings.ToLower(strings.TrimPrefix(source.FileExt, "."))
	switch ext {
	case "m4a", "mp4", "mp3", "flac":
	default:
		return "", cleanup, errors.New("unsupported cached audio format")
	}
	path = filepath.Join(tempDir, sanitizeFileName(target.SongArtists+" - "+target.SongName)+"."+ext)
	fileBot := b
	if h.UploadBot != nil {
		fileBot = h.UploadBot
	}
	if fileBot == nil {
		return "", cleanup, errors.New("file bot unavailable")
	}
	if err = retrieveCachedAudio(transferCtx, fileBot, source.FileID, path); err != nil {
		return "", cleanup, err
	}
	expected := time.Duration(source.Duration) * time.Second
	if _, err = download.VerifyFullAudio(transferCtx, path, expected); err != nil {
		return "", cleanup, err
	}
	before, err := audioPayloadHash(transferCtx, path)
	if err != nil {
		return "", cleanup, err
	}
	if err = h.ID3Service.RewriteLocalizedNames(path, target.SongName, target.SongArtists, target.SongAlbum); err != nil {
		return "", cleanup, err
	}
	after, err := audioPayloadHash(transferCtx, path)
	if err != nil {
		return "", cleanup, err
	}
	if before != after {
		return "", cleanup, errors.New("audio payload changed during tag rewrite")
	}
	if _, err = download.VerifyFullAudio(transferCtx, path, expected); err != nil {
		return "", cleanup, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", cleanup, err
	}
	target.ID = 0
	target.FileID = ""
	target.AudioLanguage = target.MetadataLanguage
	target.MusicSize = int(info.Size())
	return path, cleanup, nil
}

func audioPayloadHash(ctx context.Context, path string) (string, error) {
	hashCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	output, err := exec.CommandContext(hashCtx, "ffmpeg", "-v", "error", "-i", path, "-map", "0:a:0", "-c:a", "copy", "-f", "hash", "-hash", "sha256", "-").Output()
	if err != nil {
		return "", errors.New("cannot verify audio payload")
	}
	value := strings.TrimSpace(string(output))
	if !strings.HasPrefix(value, "SHA256=") {
		return "", errors.New("invalid audio payload hash")
	}
	return value, nil
}

// Stream downloads to an owned temporary file instead of retaining a lossless
// track in memory. Telegram may return a shared absolute path in local API mode.
func retrieveCachedAudio(ctx context.Context, b *telego.Bot, fileID, destination string) error {
	info, err := b.GetFile(ctx, &telego.GetFileParams{FileID: fileID})
	if err != nil || info == nil || info.FilePath == "" {
		return errors.New("Telegram cached file unavailable")
	}
	const limit int64 = 2 << 30
	if int64(info.FileSize) > limit {
		return errors.New("cached file exceeds download limit")
	}
	copyFile := func(reader io.Reader) error {
		file, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		n, copyErr := io.Copy(file, io.LimitReader(reader, limit+1))
		closeErr := file.Close()
		if copyErr != nil {
			return errors.New("cached file transfer interrupted")
		}
		if closeErr != nil {
			return closeErr
		}
		if n == 0 || n > limit {
			return errors.New("invalid cached file size")
		}
		if info.FileSize > 0 && n != int64(info.FileSize) {
			return errors.New("incomplete cached file transfer")
		}
		return ctx.Err()
	}
	if filepath.IsAbs(info.FilePath) {
		if local, openErr := os.Open(info.FilePath); openErr == nil {
			defer local.Close()
			return copyFile(&contextAudioReader{ctx: ctx, r: local})
		}
	}
	paths := []string{info.FilePath}
	if trimmed := strings.TrimLeft(info.FilePath, "/"); trimmed != info.FilePath {
		paths = append(paths, trimmed)
	}
	if token := b.Token(); token != "" {
		if _, relative, ok := strings.Cut(info.FilePath, token+"/"); ok {
			paths = append(paths, relative)
		}
	}
	client := &http.Client{Timeout: 5 * time.Minute}
	for _, p := range paths {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, b.FileDownloadURL(p), nil)
		if err != nil {
			continue
		}
		response, err := client.Do(request)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			continue
		}
		if response.StatusCode != http.StatusOK {
			response.Body.Close()
			continue
		}
		err = copyFile(response.Body)
		response.Body.Close()
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	// URLs contain the bot token, so never return raw HTTP errors to logs.
	return errors.New("Telegram cached file download failed")
}

type contextAudioReader struct {
	ctx context.Context
	r   io.Reader
}

func (r *contextAudioReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

func (h *MusicHandler) tryLocalizedInlineAudio(ctx context.Context, b *telego.Bot, p, id, q string, progress func(string)) (*botpkg.SongInfo, error) {
	if !isAppleMusicPlatform(p) || h.InlineUploadChatID == 0 {
		return nil, nil
	}
	resultCh := h.localizedAudioGroup.DoChan(metadataRequestKey(ctx, p, "retag:"+id+":"+q), func() (any, error) {
		source, err := findLanguageAudio(ctx, h.Repo, p, id, q)
		if err != nil || source == nil || source.FileID == "" || !isReusableCachedSong(source, p, q) {
			return nil, nil
		}
		song := *source
		localizeCachedSong(ctx, h.PlatformManager, h.Repo, &song)
		if !needsLocalizedAudio(ctx, &song) {
			return &song, nil
		}
		path, cleanup, err := h.prepareLocalizedAudio(ctx, b, source, &song)
		if err != nil {
			return nil, nil
		} // Retrieval/validation failed: use the existing Apple download path.
		defer cleanup()
		if err := h.uploadInlineAudio(ctx, b, &song, path, "", progress); err != nil {
			return nil, err
		}
		return &song, nil
	})
	var result any
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case outcome := <-resultCh:
		if outcome.Err != nil {
			return nil, outcome.Err
		}
		result = outcome.Val
	}
	if result == nil {
		return nil, nil
	}
	song, ok := result.(*botpkg.SongInfo)
	if !ok {
		return nil, fmt.Errorf("invalid localized audio result")
	}
	copy := *song
	return &copy, nil
}

func invalidateLanguageAudio(ctx context.Context, repo botpkg.SongRepository, song *botpkg.SongInfo) {
	if song == nil || repo == nil {
		return
	}
	if storage, ok := repo.(interface {
		DeleteCachedAudioFile(context.Context, string) error
	}); ok && isAppleMusicPlatform(song.Platform) && song.FileID != "" {
		_ = storage.DeleteCachedAudioFile(ctx, song.FileID)
		return
	}
	if storage, ok := repo.(localizedAudioRepository); ok && isAppleMusicPlatform(song.Platform) {
		_ = storage.DeleteLocalizedAudio(ctx, song.Platform, song.TrackID, song.Quality, song.AudioLanguage)
	}
}
