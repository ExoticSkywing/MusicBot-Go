package kuwo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/liuran001/MusicBot-Go/bot/platform"
)

func TestSearchRequestContractAndLimits(t *testing.T) {
	var gotQuery url.Values
	var gotReferer string
	searchCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.SetCookie(w, &http.Cookie{Name: kuwoSessionCookie, Value: "abcdefghijklmnop", Path: "/"})
			return
		}
		searchCalls++
		gotQuery = r.URL.Query()
		gotReferer = r.Referer()
		_, _ = w.Write([]byte(`{"data":{"list":[]}}`))
	}))
	defer server.Close()
	client := newClientWithEndpoints(time.Second, nil, kuwoEndpoints{home: server.URL + "/", search: server.URL + "/search", detail: server.URL + "/detail"})

	if tracks, err := client.Search(context.Background(), " hello world ", 99); err != nil || len(tracks) != 0 {
		t.Fatalf("Search() = %#v, %v", tracks, err)
	}
	for key, want := range map[string]string{"vipver": "1", "client": "kt", "ft": "music", "cluster": "0", "strategy": "2012", "encoding": "utf8", "rformat": "json", "mobi": "1", "issubtitle": "1", "show_copyright_off": "1", "pn": "0", "rn": "50", "all": "hello world"} {
		if got := gotQuery.Get(key); got != want {
			t.Errorf("query %s = %q, want %q", key, got, want)
		}
	}
	if gotReferer != "https://www.kuwo.cn/search/list?key=hello+world" {
		t.Errorf("Referer = %q", gotReferer)
	}
	if !uuidV4Pattern.MatchString(gotQuery.Get("reqId")) {
		t.Errorf("reqId = %q, want UUID v4 query parameter", gotQuery.Get("reqId"))
	}

	if _, err := client.Search(context.Background(), " ", 10); err != nil {
		t.Fatalf("blank Search() = %v", err)
	}
	if searchCalls != 1 {
		t.Fatalf("blank Search() made %d API requests, want 1 total", searchCalls)
	}
	if _, err := client.Search(context.Background(), "second", 0); err != nil {
		t.Fatalf("zero limit Search() = %v", err)
	}
	if got := gotQuery.Get("rn"); got != "10" {
		t.Errorf("zero limit rn = %q, want 10", got)
	}
}

func TestSearchMapsRateLimitAndRejectsOversizedJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.SetCookie(w, &http.Cookie{Name: kuwoSessionCookie, Value: "abcdefghijklmnop", Path: "/"})
			return
		}
		switch r.URL.Query().Get("all") {
		case "limited":
			w.WriteHeader(http.StatusTooManyRequests)
		case "large":
			_, _ = w.Write([]byte(strings.Repeat("x", 4<<20+1)))
		}
	}))
	defer server.Close()
	client := newClientWithEndpoints(time.Second, nil, kuwoEndpoints{home: server.URL + "/", search: server.URL + "/search", detail: server.URL + "/detail"})
	if _, err := client.Search(context.Background(), "limited", 1); !errors.Is(err, platform.ErrRateLimited) {
		t.Fatalf("limited Search() error = %v, want rate limited", err)
	}
	if _, err := client.Search(context.Background(), "large", 1); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("large Search() error = %v, want size error", err)
	}
}

func TestSearchConvertsScalarDriftFixtures(t *testing.T) {
	fixtures := []string{
		`{"data":{"list":[{"rid":"MUSIC_41378936","name":"Song","artist":"Alice & Bob","album":"Album","duration":"234","pic":"cover"}]}}`,
		`{"data":{"list":[{"rid":41378936,"name":"Song","artist":"Alice","album":"Album","duration":234,"pic":"cover","isListenFee":false,"unknown":true}]}}`,
		`{"data":{"list":[{"rid":null,"name":true,"artist":null,"duration":null},{"rid":"MUSIC_41378936","name":"Song","artist":"Alice","duration":true}]}}`,
		`{"abslist":[{"MUSICRID":"MUSIC_41378936","NAME":"Search title","SONGNAME":"Song","ARTIST":"Alice","ALBUM":"Album","DURATION":"234","web_albumpic_short":"cover.jpg","payInfo":{"play":"1"}}]}`,
	}
	for _, fixture := range fixtures {
		t.Run(fixture[:20], func(t *testing.T) {
			server := kuwoFixtureServer(t, fixture)
			defer server.Close()
			client := newClientWithEndpoints(time.Second, nil, kuwoEndpoints{home: server.URL + "/", search: server.URL + "/search", detail: server.URL + "/detail"})
			tracks, err := client.Search(context.Background(), "song", 10)
			if err != nil {
				t.Fatalf("Search() = %v", err)
			}
			if len(tracks) != 1 || tracks[0].ID != "41378936" || tracks[0].Platform != "kuwo" || tracks[0].URL != "https://www.kuwo.cn/play_detail/41378936" {
				t.Fatalf("tracks = %#v", tracks)
			}
			if tracks[0].Duration != 234*time.Second && !strings.Contains(fixture, `"duration":true`) {
				t.Errorf("Duration = %s, want 234s", tracks[0].Duration)
			}
			if strings.Contains(fixture, `"abslist"`) {
				track := tracks[0]
				if track.Title != "Search title" || len(track.Artists) != 1 || track.Artists[0].Name != "Alice" || track.Album == nil || track.Album.Title != "Album" || track.CoverURL != "https://img1.kuwo.cn/star/albumcover/cover.jpg" {
					t.Fatalf("abslist track = %#v, want all live fields converted", track)
				}
			}
		})
	}
}

func TestGetTrackRejectsMismatchedResponseRID(t *testing.T) {
	server := kuwoFixtureServer(t, `{"data":{"rid":"MUSIC_99999999","name":"Substituted"}}`)
	defer server.Close()
	client := newClientWithEndpoints(time.Second, nil, kuwoEndpoints{home: server.URL + "/", search: server.URL + "/search", detail: server.URL + "/detail"})
	track, err := client.GetTrack(context.Background(), "MUSIC_41378936")
	if track != nil || !errors.Is(err, platform.ErrUnavailable) {
		t.Fatalf("GetTrack() = %#v, %v, want unavailable error for requested RID", track, err)
	}
}

func TestGetTrackConvertsDetailAndPreservesBenignAccess(t *testing.T) {
	fixture := `{"data":{"id":41378936,"name":"Song","artist":"Alice / Bob","album":"Album","duration":"03:54","pic":"cover","isListenFee":"false","payInfo":{"play":"1","download":"0"},"isTry":true,"unknown":{"field":1}}}`
	server := kuwoFixtureServer(t, fixture)
	defer server.Close()
	client := newClientWithEndpoints(time.Second, nil, kuwoEndpoints{home: server.URL + "/", search: server.URL + "/search", detail: server.URL + "/detail"})
	track, access, err := client.getTrackDetail(context.Background(), "MUSIC_41378936")
	if err != nil {
		t.Fatalf("getTrackDetail() = %v", err)
	}
	if track.ID != "41378936" || track.Title != "Song" || len(track.Artists) != 2 || track.Duration != 234*time.Second {
		t.Fatalf("track = %#v", track)
	}
	if access.isListenFee || !access.isTrial || string(access.payInfo) != `{"play":"1","download":"0"}` {
		t.Fatalf("access = %#v", access)
	}
	if got, err := client.GetTrack(context.Background(), "41378936"); err != nil || got.ID != "41378936" {
		t.Fatalf("GetTrack() = %#v, %v", got, err)
	}
}

func TestGetTrackDetailScalarDrift(t *testing.T) {
	fixtures := []string{
		`{"data":{"id":"MUSIC_41378936","name":"Song","artist":"Alice","duration":"234","isListenFee":true,"payInfo":null}}`,
		`{"data":{"id":41378936,"name":"Song","artist":true,"duration":234,"isListenFee":"0","isTry":"true"}}`,
		`{"data":{"id":"MUSIC_41378936","name":"Song","unknown":true}}`,
	}
	for _, fixture := range fixtures {
		server := kuwoFixtureServer(t, fixture)
		client := newClientWithEndpoints(time.Second, nil, kuwoEndpoints{home: server.URL + "/", search: server.URL + "/search", detail: server.URL + "/detail"})
		track, access, err := client.getTrackDetail(context.Background(), "41378936")
		server.Close()
		if err != nil || track == nil || track.ID != "41378936" {
			t.Fatalf("fixture %s: track=%#v access=%#v err=%v", fixture, track, access, err)
		}
	}
}

func TestRejectPreviewAccessUsesOnlyExplicitSignals(t *testing.T) {
	benign := []json.RawMessage{
		json.RawMessage(`{"feeType":1,"pay":"1","hasLossless":true,"unknown":{"nested":1}}`),
		json.RawMessage(`{"cannotOnlinePlay":0,"listen_fragment":"0"}`),
		json.RawMessage(`null`),
	}
	for _, payInfo := range benign {
		if err := validateTrackAccess(trackAccess{isTrial: true, payInfo: payInfo}); err != nil {
			t.Errorf("validateTrackAccess(%s) = %v", payInfo, err)
		}
	}
	for _, access := range []trackAccess{
		{isListenFee: true},
		{payInfo: json.RawMessage(`{"cannotOnlinePlay":"1"}`)},
		{payInfo: json.RawMessage(`{"listen_fragment":true}`)},
	} {
		err := validateTrackAccess(access)
		if !errors.Is(err, platform.ErrUnavailable) || !errors.Is(err, errPaidTrack) {
			t.Errorf("validateTrackAccess(%#v) = %v", access, err)
		}
	}
}

func kuwoFixtureServer(t *testing.T, fixture string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.SetCookie(w, &http.Cookie{Name: kuwoSessionCookie, Value: "abcdefghijklmnop", Path: "/"})
			return
		}
		if !uuidV4Pattern.MatchString(r.URL.Query().Get("reqId")) {
			t.Errorf("request reqId = %q, want UUID v4 query parameter", r.URL.Query().Get("reqId"))
		}
		if r.URL.Path == "/detail" && (r.URL.Query().Get("mid") != "41378936" || r.URL.Query().Get("httpsStatus") != "1") {
			t.Errorf("detail query = %v, want mid and httpsStatus", r.URL.Query())
		}
		_, _ = w.Write([]byte(fixture))
	}))
}

var uuidV4Pattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

type playlistTestServer struct {
	server    *httptest.Server
	homeCalls atomic.Int32
	apiCalls  atomic.Int32
}

type contextDeadlineBody struct {
	ctx  context.Context
	data []byte
	read bool
}

func (body *contextDeadlineBody) Read(buffer []byte) (int, error) {
	if body.read {
		return 0, io.EOF
	}
	<-body.ctx.Done()
	body.read = true
	return copy(buffer, body.data), io.EOF
}

func (*contextDeadlineBody) Close() error {
	return nil
}

func newPlaylistTestServer(
	t *testing.T,
	responder func(call int, request *http.Request) (int, string),
) (*Client, *playlistTestServer) {
	t.Helper()
	fixture := &playlistTestServer{}
	fixture.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/":
			fixture.homeCalls.Add(1)
			http.SetCookie(w, &http.Cookie{Name: kuwoSessionCookie, Value: "abcdefghijklmnop", Path: "/"})
		case "/playlist":
			call := int(fixture.apiCalls.Add(1))
			status, body := responder(call, request)
			if status == 0 {
				status = http.StatusOK
			}
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		default:
			http.NotFound(w, request)
		}
	}))
	client := newClientWithEndpoints(time.Second, nil, kuwoEndpoints{
		home:     fixture.server.URL + "/",
		playlist: fixture.server.URL + "/playlist",
	})
	return client, fixture
}

func (s *playlistTestServer) Close() {
	if s != nil && s.server != nil {
		s.server.Close()
	}
}

func playlistFixture(playlistID string, total string, tracks ...string) string {
	return fmt.Sprintf(
		`{"code":200,"data":{"id":%q,"name":"Fixture","desc":"Description","img700":"https://img.test/700.jpg","userName":"Creator","total":%s,"musicList":[%s]}}`,
		playlistID,
		total,
		strings.Join(tracks, ","),
	)
}

func playlistTrackFixture(id string) string {
	return fmt.Sprintf(`{"rid":%q,"name":"Track %s","artist":"Artist","duration":"180"}`, id, id)
}

func TestGetPlaylistValidatesStrictPlaylistIDBeforeNetwork(t *testing.T) {
	client, fixture := newPlaylistTestServer(t, func(_ int, _ *http.Request) (int, string) {
		return http.StatusOK, playlistFixture("1", "0")
	})
	defer fixture.Close()

	for _, playlistID := range []string{
		"",
		" \t\r\n ",
		"MUSIC_123",
		"+123",
		"-123",
		"12a3",
		"１２３",
		"١٢٣",
		"123456789012345678901",
	} {
		t.Run(strconv.Quote(playlistID), func(t *testing.T) {
			playlist, err := client.GetPlaylist(context.Background(), playlistID, 0, 50)
			if playlist != nil || !errors.Is(err, platform.ErrNotFound) {
				t.Fatalf("GetPlaylist(%q) = %#v, %v; want ErrNotFound", playlistID, playlist, err)
			}
		})
	}
	if got := fixture.homeCalls.Load(); got != 0 {
		t.Fatalf("invalid IDs made %d homepage requests, want 0", got)
	}
	if got := fixture.apiCalls.Load(); got != 0 {
		t.Fatalf("invalid IDs made %d API requests, want 0", got)
	}
}

func TestGetPlaylistAcceptsTwentyDigitPlaylistIDAsString(t *testing.T) {
	const playlistID = "99999999999999999999"
	client, fixture := newPlaylistTestServer(t, func(_ int, request *http.Request) (int, string) {
		if got := request.URL.Query().Get("pid"); got != playlistID {
			t.Errorf("pid = %q, want %q", got, playlistID)
		}
		return http.StatusOK, playlistFixture(playlistID, "0")
	})
	defer fixture.Close()

	playlist, err := client.GetPlaylist(context.Background(), " \t"+playlistID+"\n ", 0, 50)
	if err != nil {
		t.Fatalf("GetPlaylist() = %v", err)
	}
	if playlist == nil || playlist.ID != playlistID || playlist.TrackCount != 0 {
		t.Fatalf("playlist = %#v", playlist)
	}
	if fixture.homeCalls.Load() != 1 || fixture.apiCalls.Load() != 1 {
		t.Fatalf("calls = home:%d api:%d, want 1/1", fixture.homeCalls.Load(), fixture.apiCalls.Load())
	}
}

func TestGetPlaylistNormalizesOffsetAndLimit(t *testing.T) {
	tests := []struct {
		name        string
		offset      int
		limit       int
		wantPage    string
		wantLimit   string
		wantErr     bool
		wantAPICall int32
	}{
		{name: "negative offset", offset: -99, limit: 1, wantPage: "1", wantLimit: "1", wantAPICall: 1},
		{name: "default zero limit", offset: 0, limit: 0, wantPage: "1", wantLimit: "50", wantAPICall: 1},
		{name: "default negative limit before division", offset: 51, limit: -1, wantPage: "2", wantLimit: "50", wantAPICall: 1},
		{name: "limit 100", offset: 100, limit: 100, wantPage: "2", wantLimit: "100", wantAPICall: 1},
		{name: "cap limit before division", offset: 101, limit: 101, wantPage: "2", wantLimit: "100", wantAPICall: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, fixture := newPlaylistTestServer(t, func(_ int, request *http.Request) (int, string) {
				query := request.URL.Query()
				if query.Get("pn") != test.wantPage || query.Get("rn") != test.wantLimit {
					t.Errorf("query pn/rn = %q/%q, want %q/%q", query.Get("pn"), query.Get("rn"), test.wantPage, test.wantLimit)
				}
				return http.StatusOK, playlistFixture("123", "0")
			})
			defer fixture.Close()
			playlist, err := client.GetPlaylist(context.Background(), "123", test.offset, test.limit)
			if test.wantErr {
				if !errors.Is(err, platform.ErrUnavailable) {
					t.Fatalf("error = %v, want ErrUnavailable", err)
				}
				return
			}
			if err != nil || playlist == nil {
				t.Fatalf("GetPlaylist() = %#v, %v", playlist, err)
			}
			if got := fixture.apiCalls.Load(); got != test.wantAPICall {
				t.Fatalf("API calls = %d, want %d", got, test.wantAPICall)
			}
		})
	}
}

func TestGetPlaylistPaginationBoundaries(t *testing.T) {
	maxOffset := (int(maxKuwoPlaylistPage) - 1)
	client, fixture := newPlaylistTestServer(t, func(_ int, request *http.Request) (int, string) {
		if got := request.URL.Query().Get("pn"); got != strconv.Itoa(maxKuwoPlaylistPage) {
			t.Fatalf("pn = %q, want %d", got, maxKuwoPlaylistPage)
		}
		return http.StatusOK, playlistFixture("123", "0")
	})
	defer fixture.Close()
	if _, err := client.GetPlaylist(context.Background(), "123", maxOffset, 1); err != nil {
		t.Fatalf("maximum page GetPlaylist() = %v", err)
	}

	before := fixture.apiCalls.Load()
	if playlist, err := client.GetPlaylist(context.Background(), "123", int(maxKuwoPlaylistPage), 1); playlist != nil || !errors.Is(err, platform.ErrUnavailable) {
		t.Fatalf("page max+1 = %#v, %v; want ErrUnavailable", playlist, err)
	}
	if got := fixture.apiCalls.Load(); got != before {
		t.Fatalf("page max+1 made API call: before=%d after=%d", before, got)
	}

	if playlist, err := client.GetPlaylist(context.Background(), "123", math.MaxInt, 100); playlist != nil || !errors.Is(err, platform.ErrUnavailable) {
		t.Fatalf("MaxInt offset = %#v, %v; want ErrUnavailable", playlist, err)
	}
	if got := fixture.apiCalls.Load(); got != before {
		t.Fatalf("MaxInt offset made API call: before=%d after=%d", before, got)
	}
}

func TestGetPlaylistRejectsPotentialSecondPageOverflowBeforeNetwork(t *testing.T) {
	if strconv.IntSize < 64 {
		t.Skip("max Kuwo page plus nonzero skip is not representable on 32-bit")
	}
	offsetValue := (uint64(maxKuwoPlaylistPage)-1)*2 + 1
	offset := int(offsetValue)
	client, fixture := newPlaylistTestServer(t, func(_ int, _ *http.Request) (int, string) {
		return http.StatusOK, playlistFixture("123", "0")
	})
	defer fixture.Close()
	playlist, err := client.GetPlaylist(context.Background(), "123", offset, 2)
	if playlist != nil || !errors.Is(err, platform.ErrUnavailable) {
		t.Fatalf("GetPlaylist() = %#v, %v; want ErrUnavailable", playlist, err)
	}
	if fixture.homeCalls.Load() != 0 || fixture.apiCalls.Load() != 0 {
		t.Fatalf("overflow risk made network calls home=%d api=%d", fixture.homeCalls.Load(), fixture.apiCalls.Load())
	}
}

func TestPlaylistPageWindowRejectsPotentialSecondPageOverflow(t *testing.T) {
	offset := (uint64(maxKuwoPlaylistPage)-1)*2 + 1
	if page, skip, ok := playlistPageWindow(offset, 2); ok {
		t.Fatalf("playlistPageWindow() = page %d skip %d, want rejected", page, skip)
	}
}

func TestGetPlaylistEnforcesRawPageLength(t *testing.T) {
	tests := []struct {
		name      string
		offset    int
		limit     int
		body      string
		wantError bool
		wantCount int
	}{
		{name: "zero missing list", offset: 0, limit: 3, body: `{"code":200,"data":{"id":"123","total":0}}`, wantCount: 0},
		{name: "zero null list", offset: 0, limit: 3, body: `{"code":200,"data":{"id":"123","total":0,"musicList":null}}`, wantCount: 0},
		{name: "page base equals total", offset: 3, limit: 3, body: playlistFixture("123", "3"), wantCount: 0},
		{name: "page base beyond total", offset: 6, limit: 3, body: playlistFixture("123", "2"), wantCount: 0},
		{name: "missing expected rows", offset: 0, limit: 3, body: `{"code":200,"data":{"id":"123","total":2}}`, wantError: true},
		{name: "short full page", offset: 0, limit: 3, body: playlistFixture("123", "3", playlistTrackFixture("1"), playlistTrackFixture("2")), wantError: true},
		{name: "rows beyond zero total", offset: 0, limit: 3, body: playlistFixture("123", "0", playlistTrackFixture("1")), wantError: true},
		{name: "rows beyond last page", offset: 3, limit: 3, body: playlistFixture("123", "4", playlistTrackFixture("4"), playlistTrackFixture("5")), wantError: true},
		{name: "exact partial last page", offset: 3, limit: 3, body: playlistFixture("123", "5", playlistTrackFixture("4"), playlistTrackFixture("5")), wantCount: 2},
		{name: "exact full page", offset: 0, limit: 3, body: playlistFixture("123", "3", playlistTrackFixture("1"), playlistTrackFixture("2"), playlistTrackFixture("3")), wantCount: 3},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, fixture := newPlaylistTestServer(t, func(_ int, _ *http.Request) (int, string) {
				return http.StatusOK, test.body
			})
			defer fixture.Close()
			playlist, err := client.GetPlaylist(context.Background(), "123", test.offset, test.limit)
			if test.wantError {
				if playlist != nil || !errors.Is(err, platform.ErrUnavailable) {
					t.Fatalf("GetPlaylist() = %#v, %v; want ErrUnavailable", playlist, err)
				}
			} else {
				if err != nil || playlist == nil || len(playlist.Tracks) != test.wantCount {
					t.Fatalf("GetPlaylist() = %#v, %v; want %d tracks", playlist, err, test.wantCount)
				}
			}
			if got := fixture.apiCalls.Load(); got != 1 {
				t.Fatalf("API calls = %d, want 1", got)
			}
		})
	}
}

func TestGetPlaylistNonAlignedOffsetAtOrBeyondTotal(t *testing.T) {
	tests := []struct {
		name      string
		offset    int
		total     string
		tracks    []string
		wantIDs   []string
		wantTotal int
	}{
		{
			name:      "skip beyond short legitimate page",
			offset:    5,
			total:     "4",
			tracks:    []string{playlistTrackFixture("4")},
			wantTotal: 4,
		},
		{
			name:      "skip equals legitimate page length",
			offset:    5,
			total:     "5",
			tracks:    []string{playlistTrackFixture("4"), playlistTrackFixture("5")},
			wantTotal: 5,
		},
		{
			name:      "tail remains after prefix",
			offset:    4,
			total:     "5",
			tracks:    []string{playlistTrackFixture("4"), playlistTrackFixture("5")},
			wantIDs:   []string{"5"},
			wantTotal: 5,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, fixture := newPlaylistTestServer(t, func(_ int, request *http.Request) (int, string) {
				if request.URL.Query().Get("pn") != "2" || request.URL.Query().Get("rn") != "3" {
					t.Errorf("query = %v, want pn=2 rn=3", request.URL.Query())
				}
				return http.StatusOK, playlistFixture("123", test.total, test.tracks...)
			})
			defer fixture.Close()
			playlist, err := client.GetPlaylist(context.Background(), "123", test.offset, 3)
			if err != nil || playlist == nil {
				t.Fatalf("GetPlaylist() = %#v, %v", playlist, err)
			}
			gotIDs := make([]string, len(playlist.Tracks))
			for index := range playlist.Tracks {
				gotIDs[index] = playlist.Tracks[index].ID
			}
			if strings.Join(gotIDs, ",") != strings.Join(test.wantIDs, ",") {
				t.Fatalf("track IDs = %v, want %v", gotIDs, test.wantIDs)
			}
			if playlist.TrackCount != test.wantTotal || fixture.apiCalls.Load() != 1 {
				t.Fatalf("TrackCount/calls = %d/%d, want %d/1", playlist.TrackCount, fixture.apiCalls.Load(), test.wantTotal)
			}
		})
	}
}

func TestGetPlaylistTotalIntegerBoundaries(t *testing.T) {
	maxTotal := strconv.FormatUint(uint64(math.MaxInt), 10)
	tooLarge := strconv.FormatUint(uint64(math.MaxInt)+1, 10)
	tests := []struct {
		name      string
		total     string
		wantError bool
	}{
		{name: "max int", total: maxTotal},
		{name: "max int plus one", total: tooLarge, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, fixture := newPlaylistTestServer(t, func(_ int, _ *http.Request) (int, string) {
				return http.StatusOK, playlistFixture("123", test.total, playlistTrackFixture("1"))
			})
			defer fixture.Close()
			playlist, err := client.GetPlaylist(context.Background(), "123", 0, 1)
			if test.wantError {
				if playlist != nil || !errors.Is(err, platform.ErrUnavailable) {
					t.Fatalf("GetPlaylist() = %#v, %v; want ErrUnavailable", playlist, err)
				}
			} else if err != nil || playlist == nil || playlist.TrackCount != math.MaxInt {
				t.Fatalf("GetPlaylist() = %#v, %v; want total MaxInt", playlist, err)
			}
			if got := fixture.apiCalls.Load(); got != 1 {
				t.Fatalf("API calls = %d, want 1", got)
			}
		})
	}
}

func TestGetPlaylistRejectsInvalidEnvelopeAndIdentity(t *testing.T) {
	tests := []struct {
		name         string
		body         string
		wantNotFound bool
	}{
		{name: "numeric code minus one", body: `{"code":-1}`, wantNotFound: true},
		{name: "string code minus one", body: `{"code":"-1"}`, wantNotFound: true},
		{name: "missing code", body: `{"data":{"id":"123","total":0}}`},
		{name: "null code", body: `{"code":null,"data":{"id":"123","total":0}}`},
		{name: "boolean code", body: `{"code":true,"data":{"id":"123","total":0}}`},
		{name: "unexpected code", body: `{"code":201,"data":{"id":"123","total":0}}`},
		{name: "signed success code", body: `{"code":"+200","data":{"id":"123","total":0}}`},
		{name: "zero padded success code", body: `{"code":"0200","data":{"id":"123","total":0}}`},
		{name: "zero padded not found code", body: `{"code":"-01"}`},
		{name: "missing data", body: `{"code":200}`},
		{name: "null data", body: `{"code":"200","data":null}`},
		{name: "missing id", body: `{"code":200,"data":{"total":0}}`},
		{name: "boolean id", body: `{"code":200,"data":{"id":true,"total":0}}`},
		{name: "mismatched id", body: `{"code":200,"data":{"id":"124","total":0}}`},
		{name: "missing total", body: `{"code":200,"data":{"id":"123"}}`},
		{name: "null total", body: `{"code":200,"data":{"id":"123","total":null}}`},
		{name: "boolean total", body: `{"code":200,"data":{"id":"123","total":false}}`},
		{name: "negative total", body: `{"code":200,"data":{"id":"123","total":-1}}`},
		{name: "fractional total", body: `{"code":200,"data":{"id":"123","total":1.5}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, fixture := newPlaylistTestServer(t, func(_ int, _ *http.Request) (int, string) {
				return http.StatusOK, test.body
			})
			defer fixture.Close()
			playlist, err := client.GetPlaylist(context.Background(), "123", 0, 50)
			if playlist != nil {
				t.Fatalf("playlist = %#v, want nil", playlist)
			}
			if test.wantNotFound {
				if !errors.Is(err, platform.ErrNotFound) {
					t.Fatalf("error = %v, want ErrNotFound", err)
				}
			} else if !errors.Is(err, platform.ErrUnavailable) {
				t.Fatalf("error = %v, want ErrUnavailable", err)
			}
		})
	}

	for _, code := range []string{"200", `"200"`} {
		t.Run("successful code "+code, func(t *testing.T) {
			client, fixture := newPlaylistTestServer(t, func(_ int, _ *http.Request) (int, string) {
				return http.StatusOK, fmt.Sprintf(`{"code":%s,"data":{"id":123,"total":"0"}}`, code)
			})
			defer fixture.Close()
			if playlist, err := client.GetPlaylist(context.Background(), "123", 0, 50); err != nil || playlist == nil {
				t.Fatalf("GetPlaylist() = %#v, %v", playlist, err)
			}
		})
	}
}

func TestGetPlaylistNonAlignedOffsetReturnsExactRawWindow(t *testing.T) {
	var requestIDs []string
	var requestMu sync.Mutex
	client, fixture := newPlaylistTestServer(t, func(call int, request *http.Request) (int, string) {
		query := request.URL.Query()
		if len(query) != 5 {
			t.Errorf("query = %v, want exactly pid/pn/rn/httpsStatus/reqId", query)
		}
		if query.Get("pid") != "123" || query.Get("rn") != "2" || query.Get("httpsStatus") != "1" {
			t.Errorf("query = %v", query)
		}
		if request.Header.Get("Cookie") == "" || request.Header.Get("Secret") == "" {
			t.Errorf("signed headers missing: %#v", request.Header)
		}
		if request.Referer() != kuwoHomeURL || request.UserAgent() != kuwoUserAgent {
			t.Errorf("referer/user-agent = %q/%q", request.Referer(), request.UserAgent())
		}
		reqID := query.Get("reqId")
		if !uuidV4Pattern.MatchString(reqID) {
			t.Errorf("reqId = %q", reqID)
		}
		requestMu.Lock()
		requestIDs = append(requestIDs, reqID)
		requestMu.Unlock()
		switch call {
		case 1:
			if query.Get("pn") != "2" {
				t.Errorf("first pn = %q, want 2", query.Get("pn"))
			}
			return http.StatusOK, playlistFixture(
				"123",
				"6",
				`{"rid":"bad","name":"discarded invalid prefix"}`,
				`{"rid":"4","name":"Paid","artist":"Artist","duration":180,"isListenFee":true,"payInfo":{"cannotOnlinePlay":1}}`,
			)
		case 2:
			if query.Get("pn") != "3" {
				t.Errorf("second pn = %q, want 3", query.Get("pn"))
			}
			return http.StatusOK, playlistFixture("123", "6", playlistTrackFixture("5"), playlistTrackFixture("6"))
		default:
			t.Fatalf("unexpected API call %d", call)
			return 0, ""
		}
	})
	defer fixture.Close()

	playlist, err := client.GetPlaylist(context.Background(), "123", 3, 2)
	if err != nil {
		t.Fatalf("GetPlaylist() = %v", err)
	}
	if playlist == nil || len(playlist.Tracks) != 2 ||
		playlist.Tracks[0].ID != "4" || playlist.Tracks[1].ID != "5" {
		t.Fatalf("tracks = %#v, want exact raw window [3,5)", playlist)
	}
	if playlist.Tracks[0].Title != "Paid" {
		t.Fatalf("paid browse track was not retained: %#v", playlist.Tracks[0])
	}
	if fixture.apiCalls.Load() != 2 || len(requestIDs) != 2 || requestIDs[0] == requestIDs[1] {
		t.Fatalf("calls/request IDs = %d/%v, want two distinct", fixture.apiCalls.Load(), requestIDs)
	}
}

func TestGetPlaylistCrossPageConsistencyIsAtomic(t *testing.T) {
	tests := []struct {
		name       string
		secondBody string
		wantError  error
	}{
		{name: "id mismatch", secondBody: playlistFixture("124", "6", playlistTrackFixture("5"), playlistTrackFixture("6")), wantError: platform.ErrUnavailable},
		{name: "total drift", secondBody: playlistFixture("123", "7", playlistTrackFixture("5"), playlistTrackFixture("6")), wantError: platform.ErrUnavailable},
		{name: "short second page", secondBody: playlistFixture("123", "6", playlistTrackFixture("5")), wantError: platform.ErrUnavailable},
		{name: "rate limited second page", wantError: platform.ErrRateLimited},
		{name: "malformed second page", secondBody: `{`, wantError: nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, fixture := newPlaylistTestServer(t, func(call int, _ *http.Request) (int, string) {
				if call == 1 {
					return http.StatusOK, playlistFixture("123", "6", playlistTrackFixture("3"), playlistTrackFixture("4"))
				}
				if test.name == "rate limited second page" {
					return http.StatusTooManyRequests, ""
				}
				return http.StatusOK, test.secondBody
			})
			defer fixture.Close()
			playlist, err := client.GetPlaylist(context.Background(), "123", 3, 2)
			if playlist != nil || err == nil {
				t.Fatalf("GetPlaylist() = %#v, %v; want atomic failure", playlist, err)
			}
			if test.wantError != nil && !errors.Is(err, test.wantError) {
				t.Fatalf("error = %v, want errors.Is(%v)", err, test.wantError)
			}
			if test.name == "malformed second page" &&
				(!strings.Contains(err.Error(), "kuwo: decode playlist response") || errors.Is(err, platform.ErrNotFound)) {
				t.Fatalf("decode error = %v", err)
			}
			if got := fixture.apiCalls.Load(); got != 2 {
				t.Fatalf("API calls = %d, want 2", got)
			}
		})
	}
}

func TestGetPlaylistStopsBeforeSecondPageOnTerminalError(t *testing.T) {
	t.Run("first page rate limit", func(t *testing.T) {
		client, fixture := newPlaylistTestServer(t, func(_ int, _ *http.Request) (int, string) {
			return http.StatusTooManyRequests, ""
		})
		defer fixture.Close()
		if playlist, err := client.GetPlaylist(context.Background(), "123", 3, 2); playlist != nil || !errors.Is(err, platform.ErrRateLimited) {
			t.Fatalf("GetPlaylist() = %#v, %v", playlist, err)
		}
		if fixture.apiCalls.Load() != 1 {
			t.Fatalf("API calls = %d, want 1", fixture.apiCalls.Load())
		}
	})

	t.Run("context canceled after first page", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		client, fixture := newPlaylistTestServer(t, func(call int, _ *http.Request) (int, string) {
			if call == 1 {
				cancel()
				return http.StatusOK, playlistFixture("123", "6", playlistTrackFixture("3"), playlistTrackFixture("4"))
			}
			t.Fatalf("unexpected second page request after cancellation")
			return 0, ""
		})
		defer fixture.Close()
		playlist, err := client.GetPlaylist(ctx, "123", 3, 2)
		if playlist != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("GetPlaylist() = %#v, %v; want context.Canceled", playlist, err)
		}
		if fixture.apiCalls.Load() != 1 {
			t.Fatalf("API calls = %d, want 1", fixture.apiCalls.Load())
		}
	})

	t.Run("deadline expires after first page", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		var apiCalls atomic.Int32
		client := NewClient(time.Second, nil)
		client.endpoints.home = "https://kuwo-deadline.test/"
		client.endpoints.playlist = "https://kuwo-deadline.test/playlist"
		client.apiHTTPClient.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
			switch request.URL.Path {
			case "/":
				return response(http.StatusOK, map[string]string{
					"Set-Cookie": kuwoSessionCookie + "=abcdefghijklmnop; Path=/",
				}, nil), nil
			case "/playlist":
				call := apiCalls.Add(1)
				if call != 1 {
					t.Fatalf("unexpected page request %d after deadline", call)
				}
				payload := []byte(playlistFixture(
					"123",
					"6",
					playlistTrackFixture("3"),
					playlistTrackFixture("4"),
				))
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       &contextDeadlineBody{ctx: ctx, data: payload},
				}, nil
			default:
				t.Fatalf("unexpected request URL %s", request.URL)
				return nil, errors.New("unexpected request")
			}
		})
		playlist, err := client.GetPlaylist(ctx, "123", 3, 2)
		if playlist != nil || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("GetPlaylist() = %#v, %v; want context.DeadlineExceeded", playlist, err)
		}
		if apiCalls.Load() != 1 {
			t.Fatalf("API calls = %d, want 1", apiCalls.Load())
		}
	})
}

func TestGetPlaylistSecondPageTransportErrorPreservesCause(t *testing.T) {
	sentinel := errors.New("second page transport failed")
	var apiCalls atomic.Int32
	client := NewClient(time.Second, nil)
	client.endpoints.home = "https://kuwo-transport.test/"
	client.endpoints.playlist = "https://kuwo-transport.test/playlist"
	client.apiHTTPClient.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/":
			return response(http.StatusOK, map[string]string{
				"Set-Cookie": kuwoSessionCookie + "=abcdefghijklmnop; Path=/",
			}, nil), nil
		case "/playlist":
			call := apiCalls.Add(1)
			if call == 1 {
				return response(http.StatusOK, nil, []byte(playlistFixture(
					"123",
					"6",
					playlistTrackFixture("3"),
					playlistTrackFixture("4"),
				))), nil
			}
			if call == 2 {
				return nil, sentinel
			}
			t.Fatalf("unexpected third page request")
			return nil, errors.New("unexpected request")
		default:
			t.Fatalf("unexpected request URL %s", request.URL)
			return nil, errors.New("unexpected request")
		}
	})
	playlist, err := client.GetPlaylist(context.Background(), "123", 3, 2)
	if playlist != nil || !errors.Is(err, sentinel) {
		t.Fatalf("GetPlaylist() = %#v, %v; want transport sentinel", playlist, err)
	}
	if apiCalls.Load() != 2 {
		t.Fatalf("API calls = %d, want 2", apiCalls.Load())
	}
}

func TestGetPlaylistFieldFallbackAndRawThenConvert(t *testing.T) {
	body := `{"code":200,"data":{
		"id":"123",
		"name":"Playlist",
		"desc":"",
		"info":"Fallback description",
		"img700":"",
		"img500":"https://img.test/500.jpg",
		"img300":"https://img.test/300.jpg",
		"img":"https://img.test/base.jpg",
		"userName":"",
		"uname":"Fallback creator",
		"total":5,
		"unknown":{"nested":true},
		"musicList":[
			{"rid":"1","name":"One","artist":"A","album":"Album","duration":"03:00","pic":"https://img.test/one.jpg","isListenFee":true},
			{"rid":"bad","name":"Bad"},
			{"rid":"2","name":"Two","artist":"B","duration":180},
			{"rid":"2","name":"Two duplicate","artist":"B","duration":"180"},
			{"MUSICRID":"MUSIC_3","SONGNAME":"Three","ARTIST":"C","DURATION":"180","web_albumpic_short":"three.jpg"}
		]
	}}`
	client, fixture := newPlaylistTestServer(t, func(_ int, _ *http.Request) (int, string) {
		return http.StatusOK, body
	})
	defer fixture.Close()
	playlist, err := client.GetPlaylist(context.Background(), "123", 0, 5)
	if err != nil {
		t.Fatalf("GetPlaylist() = %v", err)
	}
	if playlist.Description != "Fallback description" ||
		playlist.CoverURL != "https://img.test/500.jpg" ||
		playlist.Creator != "Fallback creator" ||
		playlist.TrackCount != 5 ||
		playlist.URL != "https://www.kuwo.cn/playlist_detail/123" {
		t.Fatalf("playlist metadata = %#v", playlist)
	}
	if len(playlist.Tracks) != 4 ||
		playlist.Tracks[0].ID != "1" ||
		playlist.Tracks[1].ID != "2" ||
		playlist.Tracks[2].ID != "2" ||
		playlist.Tracks[3].ID != "3" {
		t.Fatalf("tracks = %#v, want invalid filtered, duplicates preserved", playlist.Tracks)
	}
}

func TestGetPlaylistMalformedKnownScalarPreservesDecodeCause(t *testing.T) {
	client, fixture := newPlaylistTestServer(t, func(_ int, _ *http.Request) (int, string) {
		return http.StatusOK, `{"code":200,"data":{"id":"123","name":{"bad":true},"total":0,"musicList":[]}}`
	})
	defer fixture.Close()
	playlist, err := client.GetPlaylist(context.Background(), "123", 0, 50)
	if playlist != nil || err == nil ||
		!strings.Contains(err.Error(), "kuwo: decode playlist response") ||
		!strings.Contains(err.Error(), "scalar cannot be composite") ||
		errors.Is(err, platform.ErrNotFound) {
		t.Fatalf("GetPlaylist() = %#v, %v", playlist, err)
	}
}

func TestGetPlaylistAlignedBoundaryDoesNotFetchExtraPage(t *testing.T) {
	client, fixture := newPlaylistTestServer(t, func(call int, request *http.Request) (int, string) {
		if call != 1 {
			t.Fatalf("unexpected API call %d", call)
		}
		if request.URL.Query().Get("pn") != "2" || request.URL.Query().Get("rn") != "2" {
			t.Fatalf("query = %v, want pn=2 rn=2", request.URL.Query())
		}
		return http.StatusOK, playlistFixture("123", "6", playlistTrackFixture("3"), playlistTrackFixture("4"))
	})
	defer fixture.Close()
	playlist, err := client.GetPlaylist(context.Background(), "123", 2, 2)
	if err != nil || playlist == nil || len(playlist.Tracks) != 2 {
		t.Fatalf("GetPlaylist() = %#v, %v", playlist, err)
	}
	if fixture.apiCalls.Load() != 1 {
		t.Fatalf("API calls = %d, want 1", fixture.apiCalls.Load())
	}
}

func TestGetPlaylistConcurrentWindowsDoNotCross(t *testing.T) {
	client, fixture := newPlaylistTestServer(t, func(_ int, request *http.Request) (int, string) {
		query := request.URL.Query()
		switch query.Get("pid") + "/" + query.Get("pn") + "/" + query.Get("rn") {
		case "111/1/2":
			return http.StatusOK, playlistFixture("111", "2", playlistTrackFixture("11"), playlistTrackFixture("12"))
		case "222/1/2":
			return http.StatusOK, playlistFixture("222", "4", playlistTrackFixture("21"), playlistTrackFixture("22"))
		case "222/2/2":
			return http.StatusOK, playlistFixture("222", "4", playlistTrackFixture("23"), playlistTrackFixture("24"))
		default:
			t.Fatalf("unexpected query %v", query)
			return 0, ""
		}
	})
	defer fixture.Close()

	type result struct {
		playlist *platform.Playlist
		err      error
	}
	aligned := make(chan result, 1)
	nonAligned := make(chan result, 1)
	var group sync.WaitGroup
	group.Add(2)
	go func() {
		defer group.Done()
		playlist, err := client.GetPlaylist(context.Background(), "111", 0, 2)
		aligned <- result{playlist: playlist, err: err}
	}()
	go func() {
		defer group.Done()
		playlist, err := client.GetPlaylist(context.Background(), "222", 1, 2)
		nonAligned <- result{playlist: playlist, err: err}
	}()
	group.Wait()
	close(aligned)
	close(nonAligned)

	first := <-aligned
	second := <-nonAligned
	if first.err != nil || first.playlist == nil ||
		first.playlist.ID != "111" || first.playlist.TrackCount != 2 ||
		len(first.playlist.Tracks) != 2 ||
		first.playlist.Tracks[0].ID != "11" || first.playlist.Tracks[1].ID != "12" {
		t.Fatalf("aligned result = %#v, %v", first.playlist, first.err)
	}
	if second.err != nil || second.playlist == nil ||
		second.playlist.ID != "222" || second.playlist.TrackCount != 4 ||
		len(second.playlist.Tracks) != 2 ||
		second.playlist.Tracks[0].ID != "22" || second.playlist.Tracks[1].ID != "23" {
		t.Fatalf("non-aligned result = %#v, %v", second.playlist, second.err)
	}
	if fixture.apiCalls.Load() != 3 {
		t.Fatalf("API calls = %d, want 3", fixture.apiCalls.Load())
	}
}
