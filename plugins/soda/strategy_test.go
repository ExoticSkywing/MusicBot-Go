package soda

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/liuran001/MusicBot-Go/bot/platform"
)

func TestNormalizeSodaAPIStrategy(t *testing.T) {
	tests := map[string]sodaAPIStrategy{
		"":           sodaAPIStrategyLegacy,
		"legacy":     sodaAPIStrategyLegacy,
		"signer":     sodaAPIStrategyLegacy,
		"pc_signed":  sodaAPIStrategyLegacy,
		"upstream":   sodaAPIStrategyUpstream,
		"h5":         sodaAPIStrategyUpstream,
		"auto":       sodaAPIStrategyAuto,
		" UPSTREAM ": sodaAPIStrategyUpstream,
	}
	for input, want := range tests {
		t.Run(input, func(t *testing.T) {
			got, err := normalizeSodaAPIStrategy(input)
			if err != nil {
				t.Fatalf("normalizeSodaAPIStrategy(%q) error = %v", input, err)
			}
			if got != want {
				t.Fatalf("normalizeSodaAPIStrategy(%q) = %q, want %q", input, got, want)
			}
		})
	}
	if _, err := normalizeSodaAPIStrategy("unknown"); err == nil {
		t.Fatal("normalizeSodaAPIStrategy(unknown) returned nil error")
	}
}

func TestSodaStrategyHighlights(t *testing.T) {
	client := NewClient("", 0, nil)
	if got := client.StrategyHighlights(); len(got) != 1 || got[0] != "API 方案：BDMS signer / PC" {
		t.Fatalf("legacy highlights = %#v", got)
	}
	if err := client.SetAPIStrategy("auto"); err != nil {
		t.Fatalf("SetAPIStrategy(auto) error = %v", err)
	}
	if got := client.StrategyHighlights(); len(got) != 2 || got[0] != "API 方案：自动回退" {
		t.Fatalf("auto highlights = %#v", got)
	}
	if got := client.StrategyHighlights(); got[1] != "调用顺序：上游公共搜索 / H5 → BDMS signer / PC" {
		t.Fatalf("auto call order = %#v", got)
	}
	if err := client.SetAPIStrategy("upstream"); err != nil {
		t.Fatalf("SetAPIStrategy(upstream) error = %v", err)
	}
	if got := client.StrategyHighlights(); len(got) != 1 || got[0] != "API 方案：上游公共搜索 / H5" {
		t.Fatalf("upstream highlights = %#v", got)
	}
}

func TestClientUpstreamStrategyUsesPublicSearch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/luna/search/track" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"status_code":0,"result_groups":[{"data":[{"entity":{"track":{"id":"upstream-1","name":"Upstream Track","duration":180000}}}]}]}`))
	}))
	defer server.Close()

	client := newSodaTestClient(server.URL)
	if err := client.SetAPIStrategy("upstream"); err != nil {
		t.Fatalf("SetAPIStrategy() error = %v", err)
	}
	tracks, err := client.Search(t.Context(), "test", 3)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(tracks) != 1 || tracks[0].ID != "upstream-1" || tracks[0].Title != "Upstream Track" {
		t.Fatalf("Search() tracks = %#v", tracks)
	}
}

func TestClientAutoStrategyFallsBackToLegacySearch(t *testing.T) {
	paths := make([]string, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/luna/search/track":
			http.Error(w, "retired", http.StatusGone)
		case "/luna/pc/search/track":
			_, _ = w.Write([]byte(`{"status_code":0,"result_groups":[{"data":[{"entity":{"track":{"id":"fallback-1","name":"Fallback Track","duration":180000}}}]}]}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := newSodaTestClient(server.URL)
	if err := client.SetAPIStrategy("auto"); err != nil {
		t.Fatalf("SetAPIStrategy() error = %v", err)
	}
	tracks, err := client.Search(t.Context(), "test", 3)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(tracks) != 1 || tracks[0].ID != "fallback-1" {
		t.Fatalf("Search() tracks = %#v", tracks)
	}
	wantPaths := []string{"/luna/search/track", "/luna/pc/search/track"}
	if !reflect.DeepEqual(paths, wantPaths) {
		t.Fatalf("request paths = %#v, want %#v", paths, wantPaths)
	}
}

func TestClientUpstreamStrategyResolvesFullPlayback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/luna/h5/seo_track":
			_, _ = w.Write([]byte(`{
				"status_code":0,
				"track_player":{"url_player_info":"https://player.example/player","media_id":"full-media"},
				"seo_track":{"track":{"id":"track-1","name":"Full Track","duration":180000,"preview":{"vid":"preview-media"}}}
			}`))
		case "/player":
			if r.Header.Get("Cookie") != "" {
				t.Fatalf("upstream player-info request leaked cookie: %q", r.Header.Get("Cookie"))
			}
			resp := sodaPlayInfoResponse{}
			resp.Result.Data.PlayInfoList = []sodaPlayInfo{{
				MainPlayURL: "https://download.example/full.m4a",
				PlayAuth:    "play-auth",
				Size:        7_200_000,
				Bitrate:     320_000,
				Format:      "m4a",
				Quality:     "highest",
				Duration:    180,
			}}
			_ = json.NewEncoder(w).Encode(resp)
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := newSodaTestClient(server.URL)
	client.cookie = "sessionid=local-only"
	if err := client.SetAPIStrategy("upstream"); err != nil {
		t.Fatalf("SetAPIStrategy() error = %v", err)
	}
	info, err := client.FetchDownloadInfo(t.Context(), "track-1", platform.QualityHigh)
	if err != nil {
		t.Fatalf("FetchDownloadInfo() error = %v", err)
	}
	if info == nil || info.URL != "https://download.example/full.m4a" || info.Bitrate != 320 {
		t.Fatalf("FetchDownloadInfo() = %#v", info)
	}
}

func TestClientUpstreamStrategyRejectsCatalogPreview(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/luna/h5/seo_track" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{
			"status_code":0,
			"track_player":{"url_player_info":"https://player.example/player","media_id":"preview-media"},
			"seo_track":{"track":{"id":"track-1","name":"Preview Track","duration":180000,"preview":{"vid":"preview-media"}}}
		}`))
	}))
	defer server.Close()

	client := newSodaTestClient(server.URL)
	if err := client.SetAPIStrategy("upstream"); err != nil {
		t.Fatalf("SetAPIStrategy() error = %v", err)
	}
	info, err := client.FetchDownloadInfo(t.Context(), "track-1", platform.QualityHigh)
	if info != nil || !errors.Is(err, errSodaIncompleteAudio) || !errors.Is(err, platform.ErrUnavailable) {
		t.Fatalf("FetchDownloadInfo() = %#v, %v", info, err)
	}
}

func TestClientAutoStrategyFallsBackToLegacyForCatalogPreview(t *testing.T) {
	paths := make([]string, 0, 3)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/luna/h5/seo_track":
			_, _ = w.Write([]byte(`{
				"status_code":0,
				"track_player":{"url_player_info":"https://player.example/preview","media_id":"preview-media"},
				"seo_track":{"track":{"id":"track-1","name":"Preview Track","duration":180000,"preview":{"vid":"preview-media"}}}
			}`))
		case "/luna/pc/track_v2":
			_, _ = w.Write([]byte(`{
				"track_info":{"id":"track-1","name":"Full Track","duration":180000},
				"track_player":{"url_player_info":"https://player.example/legacy-player"}
			}`))
		case "/legacy-player":
			resp := sodaPlayInfoResponse{}
			resp.Result.Data.PlayInfoList = []sodaPlayInfo{{
				MainPlayURL: "https://download.example/full.m4a",
				Size:        7_200_000,
				Bitrate:     320,
				Format:      "m4a",
				Quality:     "highest",
				Duration:    180,
			}}
			_ = json.NewEncoder(w).Encode(resp)
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := newSodaTestClient(server.URL)
	if err := client.SetAPIStrategy("auto"); err != nil {
		t.Fatalf("SetAPIStrategy() error = %v", err)
	}
	info, err := client.FetchDownloadInfo(t.Context(), "track-1", platform.QualityHigh)
	if err != nil {
		t.Fatalf("FetchDownloadInfo() error = %v", err)
	}
	if info == nil || info.URL != "https://download.example/full.m4a" {
		t.Fatalf("FetchDownloadInfo() = %#v", info)
	}
	wantPaths := []string{"/luna/h5/seo_track", "/luna/pc/track_v2", "/legacy-player"}
	if !reflect.DeepEqual(paths, wantPaths) {
		t.Fatalf("request paths = %#v, want %#v", paths, wantPaths)
	}
}
