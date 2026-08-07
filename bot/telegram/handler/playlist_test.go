package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liuran001/MusicBot-Go/bot/platform"
	"github.com/mymmrac/telego"
)

type playlistPageCall struct {
	offset int
	limit  int
}

type exactPlaylistTestPlatform struct {
	*stubPlatform

	mu          sync.Mutex
	total       int
	calls       []playlistPageCall
	fail        error
	nilResponse bool
	totalByPage map[int]int
	idByPage    map[int]string
	badRaw      map[int]bool
}

func newExactPlaylistTestPlatform(total int, badRaw ...int) *exactPlaylistTestPlatform {
	bad := make(map[int]bool, len(badRaw))
	for _, index := range badRaw {
		bad[index] = true
	}
	return &exactPlaylistTestPlatform{
		stubPlatform: newStubPlatform("kuwo"),
		total:        total,
		totalByPage:  make(map[int]int),
		idByPage:     make(map[int]string),
		badRaw:       bad,
	}
}

func (p *exactPlaylistTestPlatform) GetPlaylist(ctx context.Context, playlistID string) (*platform.Playlist, error) {
	offset := platform.PlaylistOffsetFromContext(ctx)
	limit := platform.PlaylistLimitFromContext(ctx)

	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, playlistPageCall{offset: offset, limit: limit})
	if p.fail != nil {
		return nil, p.fail
	}
	if p.nilResponse {
		return nil, nil
	}

	total := p.total
	if override, ok := p.totalByPage[offset]; ok {
		total = override
	}
	responseID := playlistID
	if override, ok := p.idByPage[offset]; ok {
		responseID = override
	}
	end := offset + limit
	if end > p.total {
		end = p.total
	}
	tracks := make([]platform.Track, 0, end-offset)
	for index := offset; index < end; index++ {
		if p.badRaw[index] {
			continue
		}
		tracks = append(tracks, platform.Track{
			ID:       fmt.Sprintf("raw-%d", index),
			Platform: "kuwo",
			Title:    fmt.Sprintf("Raw %d", index),
		})
	}
	return &platform.Playlist{
		ID:         responseID,
		Platform:   "kuwo",
		Title:      "Exact raw playlist",
		TrackCount: total,
		Tracks:     tracks,
	}, nil
}

func (p *exactPlaylistTestPlatform) MatchPlaylistURL(rawURL string) (string, bool) {
	const prefix = "https://www.kuwo.cn/playlist_detail/"
	if !strings.HasPrefix(rawURL, prefix) {
		return "", false
	}
	id := strings.TrimSpace(strings.TrimPrefix(rawURL, prefix))
	return id, id != ""
}

func (p *exactPlaylistTestPlatform) snapshotCalls() []playlistPageCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]playlistPageCall(nil), p.calls...)
}

func trackIDs(tracks []platform.Track) []string {
	ids := make([]string, len(tracks))
	for index := range tracks {
		ids[index] = tracks[index].ID
	}
	return ids
}

type playlistTelegramCall struct {
	method  string
	payload map[string]any
}

type playlistTelegramRecorder struct {
	mu    sync.Mutex
	calls []playlistTelegramCall
}

func newPlaylistTestBot(t *testing.T) (*telego.Bot, *playlistTelegramRecorder) {
	t.Helper()
	recorder := &playlistTelegramRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode Telegram request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		recorder.mu.Lock()
		recorder.calls = append(recorder.calls, playlistTelegramCall{method: method, payload: payload})
		recorder.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		switch method {
		case "sendMessage", "editMessageText":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true,
				"result": map[string]any{
					"message_id": 700,
					"date":       1,
					"chat":       map[string]any{"id": 1001, "type": "private"},
					"text":       payload["text"],
				},
			})
		case "answerCallbackQuery", "deleteMessage":
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

func (r *playlistTelegramRecorder) payloads(method string) []map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	var payloads []map[string]any
	for _, call := range r.calls {
		if call.method == method {
			payloads = append(payloads, call.payload)
		}
	}
	return payloads
}

func payloadJSON(t *testing.T, payload map[string]any) string {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return string(data)
}

func privatePlaylistCallback(messageID, page int, requesterID int64) *telego.Update {
	return &telego.Update{CallbackQuery: &telego.CallbackQuery{
		ID:   fmt.Sprintf("query-%d", page),
		From: telego.User{ID: requesterID},
		Message: &telego.Message{
			MessageID: messageID,
			Date:      1,
			Chat:      telego.Chat{ID: 1001, Type: "private"},
		},
		Data: fmt.Sprintf("playlist %d page %d %d", messageID, page, requesterID),
	}}
}

func TestDetectCollectionType(t *testing.T) {
	tests := []struct {
		name       string
		rawID      string
		url        string
		expectedTy string
	}{
		{name: "album by prefixed id", rawID: "album:3411281", expectedTy: collectionTypeAlbum},
		{name: "album by url", rawID: "3411281", url: "https://music.163.com/album?id=3411281", expectedTy: collectionTypeAlbum},
		{name: "playlist default", rawID: "19723756", url: "https://music.163.com/playlist?id=19723756", expectedTy: collectionTypePlaylist},
		{name: "explicit type", rawID: collectionTypeAlbum, expectedTy: collectionTypeAlbum},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detectCollectionType(tt.rawID, tt.url)
			if got != tt.expectedTy {
				t.Fatalf("detectCollectionType()=%q, want=%q", got, tt.expectedTy)
			}
		})
	}
}

func TestFormatExpandableQuote(t *testing.T) {
	quote := formatExpandableQuote(zhCtx(), "第一行\n第二行")
	if !strings.HasPrefix(quote, ">简介\n") {
		t.Fatalf("quote should start with intro line, got %q", quote)
	}
	if !strings.Contains(quote, ">第一行\n>第二行||") {
		t.Fatalf("quote should contain expandable marker, got %q", quote)
	}
}

func TestFormatPlaylistInfoUsesCollectionLabelAndQuote(t *testing.T) {
	info := formatPlaylistInfo(zhCtx(), &platform.Playlist{
		Title:       "测试专辑",
		Description: "这是第一行\n这是第二行",
		TrackCount:  12,
		URL:         "https://music.163.com/album?id=3411281",
	}, "专辑")

	if !strings.Contains(info, "专辑: [测试专辑](https://music.163.com/album?id=3411281)") {
		t.Fatalf("expected album label in playlist info, got %q", info)
	}
	if !strings.Contains(info, ">简介") {
		t.Fatalf("expected quote intro in playlist info, got %q", info)
	}
	if !strings.Contains(info, "||") {
		t.Fatalf("expected expandable marker in playlist info, got %q", info)
	}
}

func TestShouldLazyLoadCollectionIncludesSoda(t *testing.T) {
	if !shouldLazyLoadCollection("soda") {
		t.Fatal("shouldLazyLoadCollection() should lazy load soda")
	}
	if shouldLazyLoadCollection("bilibili") {
		t.Fatal("shouldLazyLoadCollection() should not lazy load bilibili")
	}
}

func TestShouldLazyLoadCollectionIncludesKuwo(t *testing.T) {
	if !shouldLazyLoadCollection("kuwo") {
		t.Fatal("shouldLazyLoadCollection() should lazy load kuwo")
	}
	if !shouldUseExactCollectionPage(" kuwo ") {
		t.Fatal("shouldUseExactCollectionPage() should select trimmed kuwo")
	}
	for _, name := range []string{"qqmusic", "netease", "soda", "bilibili"} {
		if shouldUseExactCollectionPage(name) {
			t.Fatalf("shouldUseExactCollectionPage(%q) = true, want false", name)
		}
	}
}

func TestKuwoLazyCollectionUsesExactRawPages(t *testing.T) {
	plat := newExactPlaylistTestPlatform(69, 4, 58)
	h := &PlaylistHandler{PageSize: 8}

	initial, err := h.fetchInitialPlaylist(context.Background(), plat, "2952464073", true)
	if err != nil {
		t.Fatalf("fetchInitialPlaylist() error = %v", err)
	}
	if got, want := plat.snapshotCalls(), []playlistPageCall{{offset: 0, limit: 8}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("initial calls = %#v, want %#v", got, want)
	}
	if got, want := trackIDs(initial.Tracks), []string{"raw-0", "raw-1", "raw-2", "raw-3", "raw-5", "raw-6", "raw-7"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("initial track IDs = %v, want %v", got, want)
	}

	state := &playlistState{
		playlist:    *initial,
		platform:    "kuwo",
		totalTracks: 69,
		lazy:        true,
		cacheOffset: 0,
		currentPage: 1,
	}
	pageTracks, pageOffset, err := h.getCachedPage(context.Background(), plat, state, 8)
	if err != nil {
		t.Fatalf("getCachedPage(page 8) error = %v", err)
	}
	if pageOffset != 56 || state.cacheOffset != 56 {
		t.Fatalf("page 8 offsets = returned %d cached %d, want 56/56", pageOffset, state.cacheOffset)
	}
	if got, want := trackIDs(pageTracks), []string{"raw-56", "raw-57", "raw-59", "raw-60", "raw-61", "raw-62", "raw-63"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("page 8 track IDs = %v, want %v", got, want)
	}
	if got, want := plat.snapshotCalls(), []playlistPageCall{{0, 8}, {56, 8}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("page 8 calls = %#v, want %#v", got, want)
	}

	reused, reusedOffset, err := h.getCachedPage(context.Background(), plat, state, 8)
	if err != nil {
		t.Fatalf("getCachedPage(reused page 8) error = %v", err)
	}
	if reusedOffset != 56 || !reflect.DeepEqual(trackIDs(reused), trackIDs(pageTracks)) {
		t.Fatalf("reused page = offset %d tracks %v, want offset 56 tracks %v", reusedOffset, trackIDs(reused), trackIDs(pageTracks))
	}
	if got := len(plat.snapshotCalls()); got != 2 {
		t.Fatalf("reused exact page call count = %d, want 2", got)
	}

	lastTracks, lastOffset, err := h.getCachedPage(context.Background(), plat, state, 9)
	if err != nil {
		t.Fatalf("getCachedPage(page 9) error = %v", err)
	}
	if lastOffset != 64 || state.cacheOffset != 64 {
		t.Fatalf("page 9 offsets = returned %d cached %d, want 64/64", lastOffset, state.cacheOffset)
	}
	if got, want := trackIDs(lastTracks), []string{"raw-64", "raw-65", "raw-66", "raw-67", "raw-68"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("page 9 track IDs = %v, want %v", got, want)
	}
	if got, want := plat.snapshotCalls(), []playlistPageCall{{0, 8}, {56, 8}, {64, 8}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("page 9 calls = %#v, want %#v", got, want)
	}
}

func TestKuwoLazyCollectionRefreshFailureIsAtomic(t *testing.T) {
	sourceErr := errors.New("page unavailable")
	plat := newExactPlaylistTestPlatform(69)
	plat.fail = sourceErr
	h := &PlaylistHandler{PageSize: 8}
	originalTracks := []platform.Track{{ID: "old", Platform: "kuwo", Title: "Old"}}
	state := &playlistState{
		playlist: platform.Playlist{
			ID:         "2952464073",
			Platform:   "kuwo",
			TrackCount: 69,
			Tracks:     append([]platform.Track(nil), originalTracks...),
		},
		platform:    "kuwo",
		totalTracks: 69,
		lazy:        true,
		cacheOffset: 0,
		currentPage: 1,
	}

	tracks, offset, err := h.getCachedPage(context.Background(), plat, state, 8)
	if !errors.Is(err, sourceErr) {
		t.Fatalf("getCachedPage() error = %v, want source error", err)
	}
	if tracks != nil || offset != 56 {
		t.Fatalf("failed page result = tracks %v offset %d, want nil/56", tracks, offset)
	}
	if state.cacheOffset != 0 || state.totalTracks != 69 || state.currentPage != 1 || state.playlist.TrackCount != 69 ||
		!reflect.DeepEqual(state.playlist.Tracks, originalTracks) {
		t.Fatalf("state mutated on failure: %#v", state)
	}
}

func TestKuwoLazyCollectionRejectsTrackCountDriftAtomically(t *testing.T) {
	for _, driftTotal := range []int{68, 70} {
		t.Run(fmt.Sprintf("total_%d", driftTotal), func(t *testing.T) {
			plat := newExactPlaylistTestPlatform(69)
			plat.totalByPage[56] = driftTotal
			h := &PlaylistHandler{PageSize: 8}
			originalTracks := []platform.Track{{ID: "old", Platform: "kuwo", Title: "Old"}}
			state := &playlistState{
				playlist: platform.Playlist{
					ID:         "2952464073",
					Platform:   "kuwo",
					TrackCount: 69,
					Tracks:     append([]platform.Track(nil), originalTracks...),
				},
				platform:    "kuwo",
				totalTracks: 69,
				lazy:        true,
				cacheOffset: 0,
				currentPage: 1,
			}

			tracks, offset, err := h.getCachedPage(context.Background(), plat, state, 8)
			if !errors.Is(err, platform.ErrUnavailable) {
				t.Fatalf("getCachedPage() error = %v, want ErrUnavailable", err)
			}
			if tracks != nil || offset != 56 {
				t.Fatalf("drift result = tracks %v offset %d, want nil/56", tracks, offset)
			}
			if state.cacheOffset != 0 || state.totalTracks != 69 || state.currentPage != 1 || state.playlist.TrackCount != 69 ||
				!reflect.DeepEqual(state.playlist.Tracks, originalTracks) {
				t.Fatalf("state mutated on total drift: %#v", state)
			}
		})
	}
}

func TestKuwoLazyCollectionRejectsInvalidRefreshAtomically(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*exactPlaylistTestPlatform)
	}{
		{
			name: "nil playlist",
			configure: func(plat *exactPlaylistTestPlatform) {
				plat.nilResponse = true
			},
		},
		{
			name: "playlist ID mismatch",
			configure: func(plat *exactPlaylistTestPlatform) {
				plat.idByPage[56] = "different-playlist"
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plat := newExactPlaylistTestPlatform(69)
			tt.configure(plat)
			h := &PlaylistHandler{PageSize: 8}
			originalTracks := []platform.Track{{ID: "old", Platform: "kuwo", Title: "Old"}}
			state := &playlistState{
				playlist: platform.Playlist{
					ID:         "2952464073",
					Platform:   "kuwo",
					TrackCount: 69,
					Tracks:     append([]platform.Track(nil), originalTracks...),
				},
				platform:    "kuwo",
				totalTracks: 69,
				lazy:        true,
				cacheOffset: 0,
				currentPage: 1,
			}

			tracks, offset, err := h.getCachedPage(context.Background(), plat, state, 8)
			if !errors.Is(err, platform.ErrUnavailable) {
				t.Fatalf("getCachedPage() error = %v, want ErrUnavailable", err)
			}
			if tracks != nil || offset != 56 {
				t.Fatalf("invalid refresh result = tracks %v offset %d, want nil/56", tracks, offset)
			}
			if state.cacheOffset != 0 || state.totalTracks != 69 || state.currentPage != 1 ||
				state.playlist.TrackCount != 69 || !reflect.DeepEqual(state.playlist.Tracks, originalTracks) {
				t.Fatalf("state mutated on invalid refresh: %#v", state)
			}
		})
	}
}

func TestExistingLazyPlatformsKeepChunkRequests(t *testing.T) {
	for _, name := range []string{"qqmusic", "netease", "soda"} {
		t.Run(name, func(t *testing.T) {
			plat := newExactPlaylistTestPlatform(69)
			plat.stubPlatform.name = name
			h := &PlaylistHandler{PageSize: 8}
			if _, err := h.fetchInitialPlaylist(context.Background(), plat, "playlist-id", true); err != nil {
				t.Fatalf("fetchInitialPlaylist() error = %v", err)
			}
			if got, want := plat.snapshotCalls(), []playlistPageCall{{offset: 0, limit: playlistFetchChunkSize}}; !reflect.DeepEqual(got, want) {
				t.Fatalf("calls = %#v, want %#v", got, want)
			}
		})
	}
}

func TestKuwoLazyCollectionReusesSameExactPage(t *testing.T) {
	badPage := make([]int, 8)
	for index := range badPage {
		badPage[index] = 56 + index
	}
	plat := newExactPlaylistTestPlatform(69, badPage...)
	h := &PlaylistHandler{PageSize: 8}
	state := &playlistState{
		playlist: platform.Playlist{
			ID:         "2952464073",
			Platform:   "kuwo",
			TrackCount: 69,
			Tracks:     []platform.Track{{ID: "old", Platform: "kuwo"}},
		},
		platform:    "kuwo",
		totalTracks: 69,
		lazy:        true,
		cacheOffset: 0,
	}

	tracks, offset, err := h.getCachedPage(context.Background(), plat, state, 8)
	if err != nil {
		t.Fatalf("getCachedPage(page 8) error = %v", err)
	}
	if len(tracks) != 0 || offset != 56 || state.cacheOffset != 56 || len(state.playlist.Tracks) != 0 {
		t.Fatalf("empty exact page = tracks %v offset %d state %#v", tracks, offset, state)
	}
	if got := len(plat.snapshotCalls()); got != 1 {
		t.Fatalf("first empty page call count = %d, want 1", got)
	}

	tracks, offset, err = h.getCachedPage(context.Background(), plat, state, 8)
	if err != nil {
		t.Fatalf("getCachedPage(reused empty page 8) error = %v", err)
	}
	if len(tracks) != 0 || offset != 56 {
		t.Fatalf("reused empty page = tracks %v offset %d, want empty/56", tracks, offset)
	}
	if got := len(plat.snapshotCalls()); got != 1 {
		t.Fatalf("reused empty page call count = %d, want 1", got)
	}
}

func TestKuwoInitialEmptyFilteredPageStillBuildsNavigation(t *testing.T) {
	badInitial := make([]int, 8)
	for index := range badInitial {
		badInitial[index] = index
	}
	plat := newExactPlaylistTestPlatform(69, badInitial...)
	manager := newStubManager()
	manager.Register(plat)
	playlistURL := "https://www.kuwo.cn/playlist_detail/2952464073"
	manager.AddURLRule(playlistURL, "kuwo", "2952464073")
	bot, recorder := newPlaylistTestBot(t)
	h := &PlaylistHandler{
		PlatformManager: manager,
		PageSize:        8,
	}

	handled := h.TryHandle(zhCtx(), bot, &telego.Update{Message: &telego.Message{
		MessageID: 12,
		From:      &telego.User{ID: 42},
		Date:      1,
		Chat:      telego.Chat{ID: 1001, Type: "private"},
		Text:      playlistURL,
	}})
	if !handled {
		t.Fatal("TryHandle() = false, want true")
	}
	edits := recorder.payloads("editMessageText")
	if len(edits) != 1 {
		t.Fatalf("editMessageText calls = %d, want 1", len(edits))
	}
	editJSON := payloadJSON(t, edits[0])
	if !strings.Contains(editJSON, tr(zhCtx(), "pl_page_of", map[string]any{"Page": 1, "Total": 9})) {
		t.Fatalf("initial empty exact page missing page indicator: %s", editJSON)
	}
	if !strings.Contains(editJSON, tr(zhCtx(), "pl_no_results")) {
		t.Fatalf("initial empty exact page missing local empty hint: %s", editJSON)
	}
	for _, callbackData := range []string{"playlist 700 close 42", "playlist 700 page 2 42"} {
		if !strings.Contains(editJSON, callbackData) {
			t.Fatalf("initial empty exact page missing callback %q: %s", callbackData, editJSON)
		}
	}
	state, ok := h.getPlaylistState(700)
	if !ok {
		t.Fatal("initial empty exact page did not store state")
	}
	if state.totalTracks != 69 || state.currentPage != 1 || state.cacheOffset != 0 || len(state.playlist.Tracks) != 0 {
		t.Fatalf("initial empty exact state = %#v", state)
	}

	callback := &PlaylistCallbackHandler{Playlist: h}
	callback.Handle(zhCtx(), bot, privatePlaylistCallback(700, 2, 42))
	if got, want := plat.snapshotCalls(), []playlistPageCall{{0, 8}, {8, 8}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("initial/next exact calls = %#v, want %#v", got, want)
	}
}

func TestPlaylistPagePreservesImplicitQualityIntent(t *testing.T) {
	h := &PlaylistHandler{PageSize: 8}
	_, keyboard := h.buildPlaylistPage(zhCtx(), []platform.Track{{ID: "123", Title: "Song"}}, 1, 0, "applemusic", "auto-lossless", 42, 700, 1)
	if keyboard == nil || len(keyboard.InlineKeyboard) == 0 || len(keyboard.InlineKeyboard[0]) == 0 {
		t.Fatal("missing playlist result callback")
	}
	parsed := parseMusicCallbackDataV2(strings.Fields(keyboard.InlineKeyboard[0][0].CallbackData))
	if !parsed.ok || parsed.qualityOverride != "auto-lossless" {
		t.Fatalf("parsed callback = %+v, want implicit lossless quality", parsed)
	}
}

func TestKuwoCallbackEmptyFilteredPageStillBuildsNavigation(t *testing.T) {
	badPage := make([]int, 8)
	for index := range badPage {
		badPage[index] = 56 + index
	}
	plat := newExactPlaylistTestPlatform(69, badPage...)
	manager := newStubManager()
	manager.Register(plat)
	bot, recorder := newPlaylistTestBot(t)
	h := &PlaylistHandler{
		PlatformManager: manager,
		PageSize:        8,
	}
	h.storePlaylistState(700, &playlistState{
		playlist: platform.Playlist{
			ID:         "2952464073",
			Platform:   "kuwo",
			TrackCount: 69,
			Tracks:     []platform.Track{{ID: "raw-48", Platform: "kuwo", Title: "Raw 48"}},
		},
		platform:    "kuwo",
		collection:  collectionTypePlaylist,
		quality:     "lossless",
		requesterID: 42,
		currentPage: 7,
		updatedAt:   time.Now(),
		totalTracks: 69,
		lazy:        true,
		cacheOffset: 48,
	})

	callback := &PlaylistCallbackHandler{Playlist: h}
	callback.Handle(zhCtx(), bot, privatePlaylistCallback(700, 8, 42))
	edits := recorder.payloads("editMessageText")
	if len(edits) != 1 {
		t.Fatalf("editMessageText calls = %d, want 1", len(edits))
	}
	editJSON := payloadJSON(t, edits[0])
	if !strings.Contains(editJSON, tr(zhCtx(), "pl_page_of", map[string]any{"Page": 8, "Total": 9})) ||
		!strings.Contains(editJSON, tr(zhCtx(), "pl_no_results")) {
		t.Fatalf("callback empty exact page missing page/empty text: %s", editJSON)
	}
	for _, callbackData := range []string{"playlist 700 page 7 42", "playlist 700 page 9 42", "playlist 700 home 42"} {
		if !strings.Contains(editJSON, callbackData) {
			t.Fatalf("callback empty exact page missing callback %q: %s", callbackData, editJSON)
		}
	}
	state, ok := h.getPlaylistState(700)
	if !ok {
		t.Fatal("callback empty exact page lost state")
	}
	if state.currentPage != 8 || state.cacheOffset != 56 || len(state.playlist.Tracks) != 0 ||
		state.totalTracks != 69 || state.playlist.TrackCount != 69 {
		t.Fatalf("callback empty exact state = %#v", state)
	}
	if got, want := plat.snapshotCalls(), []playlistPageCall{{56, 8}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("callback empty exact calls = %#v, want %#v", got, want)
	}
}

func TestKuwoCallbackFailureKeepsRenderedAndCachedPage(t *testing.T) {
	sourceErr := errors.New("page fetch failed")
	tests := []struct {
		name      string
		configure func(*exactPlaylistTestPlatform)
	}{
		{
			name: "request error",
			configure: func(plat *exactPlaylistTestPlatform) {
				plat.fail = sourceErr
			},
		},
		{
			name: "total decreases",
			configure: func(plat *exactPlaylistTestPlatform) {
				plat.totalByPage[56] = 68
			},
		},
		{
			name: "total increases",
			configure: func(plat *exactPlaylistTestPlatform) {
				plat.totalByPage[56] = 70
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plat := newExactPlaylistTestPlatform(69)
			tt.configure(plat)
			manager := newStubManager()
			manager.Register(plat)
			bot, recorder := newPlaylistTestBot(t)
			h := &PlaylistHandler{PlatformManager: manager, PageSize: 8}
			originalTracks := []platform.Track{{ID: "raw-48", Platform: "kuwo", Title: "Raw 48"}}
			h.storePlaylistState(700, &playlistState{
				playlist: platform.Playlist{
					ID:         "2952464073",
					Platform:   "kuwo",
					TrackCount: 69,
					Tracks:     append([]platform.Track(nil), originalTracks...),
				},
				platform:    "kuwo",
				collection:  collectionTypePlaylist,
				quality:     "lossless",
				requesterID: 42,
				currentPage: 7,
				updatedAt:   time.Now(),
				totalTracks: 69,
				lazy:        true,
				cacheOffset: 48,
			})

			callback := &PlaylistCallbackHandler{Playlist: h}
			callback.Handle(zhCtx(), bot, privatePlaylistCallback(700, 8, 42))
			if edits := recorder.payloads("editMessageText"); len(edits) != 0 {
				t.Fatalf("failed callback edited message: %#v", edits)
			}
			answers := recorder.payloads("answerCallbackQuery")
			if len(answers) != 1 || answers[0]["text"] != tr(zhCtx(), "pl_load_failed", map[string]any{"Label": collectionTypeLabel(zhCtx(), collectionTypePlaylist)}) {
				t.Fatalf("failed callback answers = %#v", answers)
			}
			state, ok := h.getPlaylistState(700)
			if !ok {
				t.Fatal("failed callback lost state")
			}
			if state.currentPage != 7 || state.cacheOffset != 48 || state.totalTracks != 69 ||
				state.playlist.TrackCount != 69 || !reflect.DeepEqual(state.playlist.Tracks, originalTracks) {
				t.Fatalf("failed callback mutated state: %#v", state)
			}
		})
	}
}

func TestKuwoZeroTrackPlaylistKeepsWholePlaylistEmptyBehavior(t *testing.T) {
	plat := newExactPlaylistTestPlatform(0)
	manager := newStubManager()
	manager.Register(plat)
	playlistURL := "https://www.kuwo.cn/playlist_detail/0"
	manager.AddURLRule(playlistURL, "kuwo", "0")
	bot, recorder := newPlaylistTestBot(t)
	h := &PlaylistHandler{PlatformManager: manager, PageSize: 8}

	if !h.TryHandle(zhCtx(), bot, &telego.Update{Message: &telego.Message{
		MessageID: 12,
		From:      &telego.User{ID: 42},
		Date:      1,
		Chat:      telego.Chat{ID: 1001, Type: "private"},
		Text:      playlistURL,
	}}) {
		t.Fatal("TryHandle() = false, want true")
	}
	edits := recorder.payloads("editMessageText")
	if len(edits) != 1 || edits[0]["text"] != tr(zhCtx(), "playlist_empty") {
		t.Fatalf("zero-track edit payloads = %#v", edits)
	}
	if _, ok := h.getPlaylistState(700); ok {
		t.Fatal("zero-track playlist unexpectedly stored state")
	}
	if strings.Contains(payloadJSON(t, edits[0]), "reply_markup") {
		t.Fatalf("zero-track playlist unexpectedly rendered keyboard: %#v", edits[0])
	}
}

func TestNonKuwoEmptyPageKeepsNoResultsBehavior(t *testing.T) {
	h := &PlaylistHandler{PageSize: 8}
	text, keyboard := h.buildPlaylistPage(zhCtx(), nil, 69, 0, "soda", "", 42, 700, 1)
	if text != tr(zhCtx(), "no_results") || keyboard != nil {
		t.Fatalf("non-Kuwo empty page = text %q keyboard %#v", text, keyboard)
	}
}
