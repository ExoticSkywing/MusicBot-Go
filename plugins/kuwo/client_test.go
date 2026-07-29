package kuwo

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
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
