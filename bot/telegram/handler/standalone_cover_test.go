package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	botpkg "github.com/liuran001/MusicBot-Go/bot"
	"github.com/liuran001/MusicBot-Go/bot/platform"
	"github.com/mymmrac/telego"
)

type standaloneCoverCallRecorder struct {
	mu      sync.Mutex
	methods []string
	payload map[string]map[string]any
}

func (r *standaloneCoverCallRecorder) record(method string, payload map[string]any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.methods = append(r.methods, method)
	if r.payload == nil {
		r.payload = make(map[string]map[string]any)
	}
	r.payload[method] = payload
}

func (r *standaloneCoverCallRecorder) snapshot() ([]string, map[string]map[string]any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	methods := append([]string(nil), r.methods...)
	payload := make(map[string]map[string]any, len(r.payload))
	for key, value := range r.payload {
		payload[key] = value
	}
	return methods, payload
}

func newStandaloneCoverTestBot(t *testing.T, failAudio bool) (*telego.Bot, *standaloneCoverCallRecorder) {
	t.Helper()
	recorder := &standaloneCoverCallRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		payload := make(map[string]any)
		if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
			if err := r.ParseMultipartForm(2 << 20); err != nil {
				t.Errorf("parse multipart %s: %v", method, err)
			}
			for key, values := range r.MultipartForm.Value {
				if len(values) > 0 {
					payload[key] = values[0]
				}
			}
			if files := r.MultipartForm.File["photo"]; len(files) > 0 {
				payload["photo_upload"] = files[0].Filename
			}
		} else if r.Body != nil {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("read %s body: %v", method, err)
			} else if len(body) > 0 {
				if err := json.Unmarshal(body, &payload); err != nil {
					t.Errorf("decode %s body: %v body=%s", method, err, body)
				}
			}
		}
		recorder.record(method, payload)

		w.Header().Set("Content-Type", "application/json")
		switch method {
		case "sendPhoto":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true,
				"result": map[string]any{
					"message_id": 21,
					"date":       1,
					"chat":       map[string]any{"id": 1001, "type": "private"},
					"photo": []map[string]any{
						{"file_id": "cover-small", "file_unique_id": "cover-u1", "width": 90, "height": 90},
						{"file_id": "cover-large", "file_unique_id": "cover-u2", "width": 1000, "height": 1000},
					},
				},
			})
		case "sendAudio":
			if failAudio {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error_code": 400, "description": "Bad Request: audio failed"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true,
				"result": map[string]any{
					"message_id": 22,
					"date":       1,
					"chat":       map[string]any{"id": 1001, "type": "private"},
					"audio":      map[string]any{"file_id": "audio-new", "file_unique_id": "audio-u", "duration": 180},
				},
			})
		case "deleteMessage":
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": true})
		default:
			t.Errorf("unexpected Telegram method %q", method)
			http.Error(w, "unexpected method", http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	bot, err := telego.NewBot("123456:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghi", telego.WithAPIServer(server.URL))
	if err != nil {
		t.Fatalf("NewBot() error = %v", err)
	}
	return bot, recorder
}

func standaloneCoverTestMessage() *telego.Message {
	return &telego.Message{
		MessageID: 10,
		From:      &telego.User{ID: 42},
		Chat:      telego.Chat{ID: 1001, Type: "private"},
	}
}

func standaloneCoverTestSong() *botpkg.SongInfo {
	return &botpkg.SongInfo{
		Platform:    "spotify",
		TrackID:     "track-1",
		Quality:     "high",
		SongName:    "Song",
		SongArtists: "Artist",
		FileID:      "audio-cached",
		CoverFileID: "cover-cached",
		CoverURL:    "https://img.example/cover.jpg",
		Duration:    180,
	}
}

func TestSendMusicDirectSendsPureStandaloneCoverBeforeAudio(t *testing.T) {
	bot, recorder := newStandaloneCoverTestBot(t, false)
	coverPath := filepath.Join(t.TempDir(), "cover.jpg")
	if err := os.WriteFile(coverPath, []byte("cover-payload"), 0o644); err != nil {
		t.Fatalf("write cover: %v", err)
	}

	h := &MusicHandler{Repo: newStubRepo(), BotName: "test_bot"}
	song := standaloneCoverTestSong()
	song.CoverFileID = ""
	song.CoverLocalPath = coverPath
	result := h.sendMusicDirect(context.Background(), bot, standaloneCoverTestMessage(), song, "", "", true)
	if result.err != nil {
		t.Fatalf("sendMusicDirect() error = %v", result.err)
	}
	if !result.coverAttempted || result.coverMessage == nil {
		t.Fatalf("standalone cover result = %#v, want sent cover", result)
	}
	if result.coverFileID != "cover-large" {
		t.Fatalf("cover file ID = %q, want largest photo ID", result.coverFileID)
	}

	methods, payloads := recorder.snapshot()
	if got, want := strings.Join(methods, ","), "sendPhoto,sendAudio"; got != want {
		t.Fatalf("Telegram method order = %q, want %q", got, want)
	}
	photo := payloads["sendPhoto"]
	if photo["photo_upload"] == nil {
		t.Fatalf("sendPhoto did not upload prepared cover: %#v", photo)
	}
	for _, forbidden := range []string{"caption", "reply_markup", "reply_parameters"} {
		if value, ok := photo[forbidden]; ok && strings.TrimSpace(value.(string)) != "" {
			t.Fatalf("pure cover unexpectedly contains %s=%v", forbidden, value)
		}
	}
}

func TestSendMusicDirectSkipsStandaloneCoverWhenDisabled(t *testing.T) {
	bot, recorder := newStandaloneCoverTestBot(t, false)
	h := &MusicHandler{Repo: newStubRepo(), BotName: "test_bot"}
	result := h.sendMusicDirect(context.Background(), bot, standaloneCoverTestMessage(), standaloneCoverTestSong(), "", "", false)
	if result.err != nil {
		t.Fatalf("sendMusicDirect() error = %v", result.err)
	}
	if result.coverAttempted || result.coverMessage != nil {
		t.Fatalf("cover was attempted while disabled: %#v", result)
	}
	methods, _ := recorder.snapshot()
	if got, want := strings.Join(methods, ","), "sendAudio"; got != want {
		t.Fatalf("Telegram methods = %q, want %q", got, want)
	}
}

func TestSendMusicDirectPrefersOriginalCoverURLOverPreparedThumbnail(t *testing.T) {
	bot, recorder := newStandaloneCoverTestBot(t, false)
	coverPath := filepath.Join(t.TempDir(), "thumbnail.jpg")
	if err := os.WriteFile(coverPath, []byte("320px-thumbnail"), 0o644); err != nil {
		t.Fatalf("write thumbnail: %v", err)
	}
	song := standaloneCoverTestSong()
	song.CoverFileID = ""
	h := &MusicHandler{Repo: newStubRepo(), BotName: "test_bot"}

	result := h.sendMusicDirect(context.Background(), bot, standaloneCoverTestMessage(), song, "", coverPath, true)
	if result.err != nil {
		t.Fatalf("sendMusicDirect() error = %v", result.err)
	}
	_, payloads := recorder.snapshot()
	photo := payloads["sendPhoto"]
	if got := photo["photo"]; got != song.CoverURL {
		t.Fatalf("standalone photo source = %#v, want original URL %q", got, song.CoverURL)
	}
	if _, uploadedThumbnail := photo["photo_upload"]; uploadedThumbnail {
		t.Fatalf("prepared thumbnail was uploaded instead of the original URL: %#v", photo)
	}
}

func TestSendMusicDirectRollsBackCoverWhenAudioFails(t *testing.T) {
	bot, recorder := newStandaloneCoverTestBot(t, true)
	h := &MusicHandler{Repo: newStubRepo(), BotName: "test_bot"}
	result := h.sendMusicDirect(context.Background(), bot, standaloneCoverTestMessage(), standaloneCoverTestSong(), "", "", true)
	if result.err == nil {
		t.Fatal("sendMusicDirect() error = nil, want audio failure")
	}
	methods, payloads := recorder.snapshot()
	if got, want := strings.Join(methods, ","), "sendPhoto,sendAudio,deleteMessage"; got != want {
		t.Fatalf("Telegram method order = %q, want %q", got, want)
	}
	if got := payloads["deleteMessage"]["message_id"]; got != float64(21) {
		t.Fatalf("rolled back message ID = %#v, want 21", got)
	}
}

func TestResolveStandaloneCoverEnabledByScope(t *testing.T) {
	repo := newStubRepo()
	ctx := context.Background()
	private := standaloneCoverTestMessage()
	group := &telego.Message{From: &telego.User{ID: 42}, Chat: telego.Chat{ID: -1001, Type: "supergroup"}}

	if !resolveStandaloneCoverEnabled(ctx, repo, private) || !resolveStandaloneCoverEnabled(ctx, repo, group) {
		t.Fatal("standalone cover should default to enabled in all scopes")
	}
	if err := repo.SetPluginSetting(ctx, botpkg.PluginScopeUser, 42, StandaloneCoverPlugin, StandaloneCoverKey, StandaloneCoverOff); err != nil {
		t.Fatalf("disable private cover: %v", err)
	}
	if resolveStandaloneCoverEnabled(ctx, repo, private) {
		t.Fatal("private standalone cover ignored user-level off setting")
	}
	if !resolveStandaloneCoverEnabled(ctx, repo, group) {
		t.Fatal("private setting leaked into group scope")
	}
	if err := repo.SetPluginSetting(ctx, botpkg.PluginScopeGroup, -1001, StandaloneCoverPlugin, StandaloneCoverKey, StandaloneCoverOff); err != nil {
		t.Fatalf("disable group cover: %v", err)
	}
	if resolveStandaloneCoverEnabled(ctx, repo, group) {
		t.Fatal("group standalone cover ignored group-level off setting")
	}
}

func TestStandaloneCoverSettingUsesImageIconAndDefaultsOn(t *testing.T) {
	def := StandaloneCoverSettingDefinition()
	if def.DefaultForScope(botpkg.PluginScopeUser) != StandaloneCoverOn || def.DefaultForScope(botpkg.PluginScopeGroup) != StandaloneCoverOn {
		t.Fatalf("standalone cover defaults = user:%q group:%q, want on/on", def.DefaultUser, def.DefaultGroup)
	}
	if icon := pluginSettingIcon(def); icon != "🖼" {
		t.Fatalf("settings icon = %q, want image icon", icon)
	}
}

func TestSendMusicPersistsStandaloneCoverFileID(t *testing.T) {
	bot, _ := newStandaloneCoverTestBot(t, false)
	repo := newStubRepo()
	workerCtx, cancelWorker := context.WithCancel(context.Background())
	h := &MusicHandler{
		Repo:                  repo,
		BotName:               "test_bot",
		CacheDir:              t.TempDir(),
		EnableStandaloneCover: true,
		UploadWorkerCount:     1,
		UploadQueueSize:       2,
	}
	h.StartWorker(workerCtx)
	t.Cleanup(func() {
		cancelWorker()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = h.ShutdownUploads(shutdownCtx)
	})

	song := standaloneCoverTestSong()
	if err := h.sendMusic(context.Background(), bot, nil, standaloneCoverTestMessage(), song, "", "", nil, nil, song.Platform, song.TrackID); err != nil {
		t.Fatalf("sendMusic() error = %v", err)
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		stored, _ := repo.FindByPlatformTrackID(context.Background(), song.Platform, song.TrackID, song.Quality)
		if stored != nil && stored.CoverFileID == "cover-large" {
			if stored.CoverURL != song.CoverURL {
				t.Fatalf("stored cover URL = %q, want %q", stored.CoverURL, song.CoverURL)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("standalone cover file ID was not persisted by upload completion")
}

type standaloneCoverMetadataPlatform struct {
	*stubPlatform
	coverURL string
}

func (p *standaloneCoverMetadataPlatform) GetTrack(_ context.Context, trackID string) (*platform.Track, error) {
	return &platform.Track{ID: trackID, Title: "Legacy", CoverURL: p.coverURL}, nil
}

func TestRefreshCachedCoverSourceHydratesLegacyRow(t *testing.T) {
	manager := newStubManager()
	manager.Register(&standaloneCoverMetadataPlatform{stubPlatform: newStubPlatform("spotify"), coverURL: "https://img.example/legacy.jpg"})
	h := &MusicHandler{PlatformManager: manager}
	song := &botpkg.SongInfo{Platform: "spotify", TrackID: "legacy-track"}

	h.refreshCachedCoverSource(context.Background(), song)
	if song.CoverURL != "https://img.example/legacy.jpg" {
		t.Fatalf("legacy cover URL = %q", song.CoverURL)
	}
}
