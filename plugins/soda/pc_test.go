package soda

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/liuran001/MusicBot-Go/bot/platform"
)

func TestClientGetLunaJSONUsesUnsignedAndroidContract(t *testing.T) {
	const cookie = "sessionid=search-session"
	client := &Client{
		cookie: cookie,
		httpClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.Method != http.MethodGet {
				t.Fatalf("request method = %q, want GET", req.Method)
			}
			if req.URL.Scheme != "https" || req.URL.Host != "api.qishui.com" || req.URL.Path != "/luna/search/track" {
				t.Fatalf("request URL = %q", req.URL.String())
			}
			query := req.URL.Query()
			for key, want := range map[string]string{
				"aid":             "386088",
				"os":              "android",
				"device_platform": "android",
				"count":           "20",
				"q":               "search term",
				"cursor":          "37",
			} {
				if got := query.Get(key); got != want {
					t.Fatalf("query %s = %q, want %q", key, got, want)
				}
			}
			const wantUserAgent = "com.luna.music/100198030 (Linux; U; Android 15; zh_CN_#Hans; ABR-AL80; Build/V417IR;tt-ok/3.12.13.19)"
			if got := req.Header.Get("User-Agent"); got != wantUserAgent {
				t.Fatalf("User-Agent = %q, want %q", got, wantUserAgent)
			}
			if got := req.Header.Get("Cookie"); got != cookie {
				t.Fatalf("Cookie = %q, want %q", got, cookie)
			}
			if got := req.Header.Get("Content-Type"); got != "application/json; charset=UTF-8" {
				t.Fatalf("Content-Type = %q", got)
			}
			for _, header := range []string{"X-Gorgon", "X-Khronos"} {
				if got := req.Header.Get(header); got != "" {
					t.Fatalf("unsigned request unexpectedly set %s=%q", header, got)
				}
			}
			return sodaHTTPResponse(req, `{"status_code":0,"result_groups":[]}`), nil
		})},
	}

	body, err := client.getLunaJSON(context.Background(), "/luna/search/track", url.Values{
		"q":      {"search term"},
		"cursor": {"37"},
	})
	if err != nil {
		t.Fatalf("getLunaJSON() error = %v", err)
	}
	if string(body) != `{"status_code":0,"result_groups":[]}` {
		t.Fatalf("getLunaJSON() body = %q", body)
	}
}

func TestClientGetLunaJSONRetriesForbiddenCookieAnonymously(t *testing.T) {
	const (
		cookie      = "sessionid=expired-session"
		successBody = `{"status_code":0,"result_groups":[{"data":[]}]}`
	)
	requestCount := 0
	firstURL := ""
	client := &Client{
		cookie: cookie,
		httpClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			requestCount++
			switch requestCount {
			case 1:
				firstURL = req.URL.String()
				if got := req.Header.Get("Cookie"); got != cookie {
					t.Fatalf("first request Cookie = %q, want %q", got, cookie)
				}
				return sodaHTTPResponse(req, `{"status_code":1000006,"status_msg":"ERR_REQUEST_FORBIDDEN"}`), nil
			case 2:
				if got := req.URL.String(); got != firstURL {
					t.Fatalf("anonymous retry URL = %q, want unchanged %q", got, firstURL)
				}
				if got := req.Header.Get("Cookie"); got != "" {
					t.Fatalf("anonymous retry Cookie = %q, want empty", got)
				}
				return sodaHTTPResponse(req, successBody), nil
			default:
				t.Fatalf("unexpected request %d: %s", requestCount, req.URL)
				return nil, nil
			}
		})},
	}

	body, err := client.getLunaJSON(context.Background(), "/luna/search/track", url.Values{
		"q":      {"retry me"},
		"cursor": {"0"},
	})
	if err != nil {
		t.Fatalf("getLunaJSON() error = %v", err)
	}
	if string(body) != successBody {
		t.Fatalf("getLunaJSON() body = %q", body)
	}
	if requestCount != 2 {
		t.Fatalf("request count = %d, want 2", requestCount)
	}
	if got := client.Cookie(); got != cookie {
		t.Fatalf("original client Cookie = %q, want unchanged %q", got, cookie)
	}
}

func TestClientGetLunaJSONDoesNotRetryOtherFailures(t *testing.T) {
	tests := []struct {
		name   string
		cookie string
		body   string
	}{
		{
			name:   "different top-level status",
			cookie: "sessionid=valid-session",
			body:   `{"status_code":1000007,"status_msg":"OTHER_ERROR"}`,
		},
		{
			name:   "forbidden without cookie",
			cookie: "",
			body:   `{"status_code":1000006,"status_msg":"ERR_REQUEST_FORBIDDEN"}`,
		},
		{
			name:   "forbidden nested status only",
			cookie: "sessionid=valid-session",
			body:   `{"status_code":0,"status_info":{"code":1000006}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requestCount := 0
			client := &Client{
				cookie: tt.cookie,
				httpClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					requestCount++
					return sodaHTTPResponse(req, tt.body), nil
				})},
			}

			body, err := client.getLunaJSON(context.Background(), "/luna/search/track", url.Values{"q": {"no retry"}})
			if err == nil {
				t.Fatalf("getLunaJSON() = %q, want error", body)
			}
			if requestCount != 1 {
				t.Fatalf("request count = %d, want 1", requestCount)
			}
			if got := client.Cookie(); got != tt.cookie {
				t.Fatalf("original client Cookie = %q, want unchanged %q", got, tt.cookie)
			}
		})
	}
}

func TestClientGetPCPlaylistJSONUsesWindowsContract(t *testing.T) {
	const cookie = "sessionid=playlist-session"
	client := &Client{
		cookie: cookie,
		httpClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.Method != http.MethodGet {
				t.Fatalf("request method = %q, want GET", req.Method)
			}
			if req.URL.Scheme != "https" || req.URL.Host != "api.qishui.com" || req.URL.Path != "/luna/pc/playlist/detail" {
				t.Fatalf("request URL = %q", req.URL.String())
			}
			query := req.URL.Query()
			for key, want := range map[string]string{
				"aid":             "386088",
				"device_platform": "windows",
				"playlist_id":     "playlist-1",
				"cursor":          "40",
				"count":           "15",
			} {
				if got := query.Get(key); got != want {
					t.Fatalf("query %s = %q, want %q", key, got, want)
				}
			}
			if _, present := query["cnt"]; present {
				t.Fatalf("legacy cnt parameter present: %q", query.Get("cnt"))
			}
			for key, want := range map[string]string{
				"User-Agent":               "LunaPC/3.3.0(359450208)",
				"Cookie":                   cookie,
				"x-luna-background-type":   "foreground",
				"x-luna-is-background-req": "0",
				"x-luna-is-local-user":     "1",
			} {
				if got := req.Header.Get(key); got != want {
					t.Fatalf("header %s = %q, want %q", key, got, want)
				}
			}
			return sodaHTTPResponse(req, `{"status_code":0,"playlist":{"id":"playlist-1"}}`), nil
		})},
	}

	body, err := client.getPCPlaylistJSON(context.Background(), url.Values{
		"playlist_id": {"playlist-1"},
		"cursor":      {"40"},
		"count":       {"15"},
		"cnt":         {"legacy-value"},
	})
	if err != nil {
		t.Fatalf("getPCPlaylistJSON() error = %v", err)
	}
	if len(body) == 0 {
		t.Fatal("getPCPlaylistJSON() returned empty body")
	}
}

func TestClientFetchTrackWebUsesH5Contract(t *testing.T) {
	const (
		trackID       = "7620326800652224539"
		cookie        = "sessionid=test-session; sid_guard=test-guard"
		playerInfoURL = "https://media.example.com/player?video_id=abc%2Fdef&sign=foo%2Bbar%3D&expires=1788840000"
		topLevelLyric = "[00:01.00]top-level lyric"
		nestedLyric   = "[00:02.00]nested fallback lyric"
		responseBody  = `{"status_code":0,"status_info":{"code":0},"track_player":{"url_player_info":"` + playerInfoURL + `"},"seo_track":{"track":{"id":"` + trackID + `","name":"H5 Track","duration":198000},"lyric":{"content":"` + nestedLyric + `"}},"lyric":{"content":"` + topLevelLyric + `"}}`
	)

	client := &Client{
		cookie: cookie,
		httpClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.Method != http.MethodGet {
				t.Fatalf("request method = %q, want GET", req.Method)
			}
			if req.URL.Scheme != "https" || req.URL.Host != "beta-luna.douyin.com" || req.URL.Path != "/luna/h5/seo_track" {
				t.Fatalf("request URL = %q", req.URL.String())
			}
			if req.URL.RawQuery != "device_platform=web&track_id="+trackID {
				t.Fatalf("request query = %q", req.URL.RawQuery)
			}
			if got := req.Header.Get("User-Agent"); got != sodaUserAgent {
				t.Fatalf("User-Agent = %q, want %q", got, sodaUserAgent)
			}
			if got := req.Header.Get("Cookie"); got != cookie {
				t.Fatalf("Cookie = %q, want %q", got, cookie)
			}
			if got := req.Header.Get("Accept"); got != "application/json, text/plain, */*" {
				t.Fatalf("Accept = %q", got)
			}
			return sodaHTTPResponse(req, responseBody), nil
		})},
	}

	got, err := client.fetchTrackWeb(context.Background(), trackID)
	if err != nil {
		t.Fatalf("fetchTrackWeb() error = %v", err)
	}
	if got == nil {
		t.Fatal("fetchTrackWeb() returned nil")
	}
	if got.Track.ID != trackID || got.Track.Name != "H5 Track" || got.Track.Duration != 198000 {
		t.Fatalf("normalized Track = %+v", got.Track)
	}
	if got.TrackInfo.ID != trackID || got.TrackInfo.Name != "H5 Track" || got.TrackInfo.Duration != 198000 {
		t.Fatalf("normalized TrackInfo = %+v", got.TrackInfo)
	}
	if got.Lyric.Content != topLevelLyric {
		t.Fatalf("normalized lyric = %q, want top-level lyric %q", got.Lyric.Content, topLevelLyric)
	}
	if got.TrackPlayer.URLPlayerInfo != playerInfoURL {
		t.Fatalf("url_player_info = %q, want exact signed URL %q", got.TrackPlayer.URLPlayerInfo, playerInfoURL)
	}
}

func TestClientFetchTrackWebUsesNestedLyricAsFallback(t *testing.T) {
	const responseBody = `{"status_code":0,"status_info":{"code":0},"seo_track":{"track":{"id":"track-1","name":"Track"},"lyric":{"content":"[00:03.00]fallback lyric"}},"lyric":{"content":"   "}}`
	client := sodaClientReturning(responseBody)

	got, err := client.fetchTrackWeb(context.Background(), "track-1")
	if err != nil {
		t.Fatalf("fetchTrackWeb() error = %v", err)
	}
	if got.Lyric.Content != "[00:03.00]fallback lyric" {
		t.Fatalf("normalized lyric = %q", got.Lyric.Content)
	}
}

func TestClientFetchTrackWebRejectsInvalidPayloads(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "empty payload", body: " \n\t"},
		{name: "top-level status code", body: `{"status_code":1001,"status_info":{"code":0},"seo_track":{"track":{"id":"track-1"}}}`},
		{name: "nested status info code", body: `{"status_code":0,"status_info":{"code":2002},"seo_track":{"track":{"id":"track-1"}}}`},
		{name: "missing track", body: `{"status_code":0,"status_info":{"code":0},"seo_track":{}}`},
		{name: "missing track id", body: `{"status_code":0,"status_info":{"code":0},"seo_track":{"track":{"name":"Track without ID"}}}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := sodaClientReturning(tt.body)
			got, err := client.fetchTrackWeb(context.Background(), "track-1")
			if err == nil {
				t.Fatalf("fetchTrackWeb() = %+v, want error", got)
			}
			if got != nil {
				t.Fatalf("fetchTrackWeb() response = %+v, want nil on error", got)
			}
		})
	}
}

func TestClientFetchDownloadInfoUsesSignedPlayerURLOnceAndReturnsKbps(t *testing.T) {
	const (
		trackID         = "track-hires"
		playerInfoURL   = "https://media.example.com/player?video_id=hires%2Ftrack&sign=signed%2Bvalue%3D&expires=1788840000"
		downloadURL     = "https://download.example.com/audio.m4a"
		playerInfoBody  = `{"Result":{"Data":{"PlayInfoList":[{"MainPlayUrl":"` + downloadURL + `","Size":1048576,"Bitrate":999000,"Format":"m4a","Quality":"higher","Duration":120000}]}}}`
		trackDetailBody = `{"status_code":0,"seo_track":{"track":{"id":"` + trackID + `"}},"track_player":{"url_player_info":"` + playerInfoURL + `"}}`
	)
	playerInfoRequests := 0
	client := &Client{httpClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch {
		case req.URL.Host == "beta-luna.douyin.com" && req.URL.Path == "/luna/h5/seo_track":
			return sodaHTTPResponse(req, trackDetailBody), nil
		case req.URL.String() == playerInfoURL:
			if req.Header.Get("Cookie") != "" {
				t.Fatal("account cookie leaked to player host")
			}
			playerInfoRequests++
			return sodaHTTPResponse(req, playerInfoBody), nil
		default:
			t.Fatalf("unexpected request URL: %s", req.URL.String())
			return nil, nil
		}
	})}}

	client.cookie = "sessionid=test-cookie"
	client.apiStrategy = sodaAPIStrategyUpstream
	info, err := client.FetchDownloadInfo(context.Background(), trackID, platform.QualityHiRes)
	if err != nil {
		t.Fatalf("FetchDownloadInfo() error = %v", err)
	}
	if info == nil {
		t.Fatal("FetchDownloadInfo() returned nil")
	}
	if playerInfoRequests != 1 {
		t.Fatalf("signed player URL request count = %d, want 1", playerInfoRequests)
	}
	if info.URL != downloadURL {
		t.Fatalf("download URL = %q, want %q", info.URL, downloadURL)
	}
	if info.Bitrate != 999 {
		t.Fatalf("bitrate = %d, want 999 kbps", info.Bitrate)
	}
}

func sodaClientReturning(body string) *Client {
	return &Client{httpClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return sodaHTTPResponse(req, body), nil
	})}}
}

func sodaHTTPResponse(req *http.Request, body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}

func TestSodaCoverUsesUpstreamTemplate(t *testing.T) {
	var track sodaTrack
	if err := json.Unmarshal([]byte(`{"id":"cover-track","album":{"url_cover":{"urls":["https://p3-luna.douyinpic.com/img/"],"uri":"tos-cn/cover","template_prefix":"tplv-b829550vbb"}}}`), &track); err != nil {
		t.Fatal(err)
	}
	const want = "https://p3-luna.douyinpic.com/img/tos-cn/cover~tplv-b829550vbb-resize:960:960.png"
	if got := convertSodaTrack(track).CoverURL; got != want {
		t.Fatalf("cover URL = %q, want %q", got, want)
	}
}

func TestClientGetPlaylistUsesUpstreamCursor(t *testing.T) {
	calls := 0
	client := &Client{httpClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.URL.Path != "/luna/pc/playlist/detail" {
			t.Fatalf("unexpected path %s", req.URL.Path)
		}
		switch calls {
		case 1:
			if req.URL.Query().Get("cursor") != "0" {
				t.Fatal("first cursor must be 0")
			}
			return sodaHTTPResponse(req, `{"playlist":{"id":"p","count_tracks":100},"next_cursor":"opaque-page-2","has_more":true,"media_resources":[{"type":"track","entity":{"track_wrapper":{"track":{"id":"t1"}}}}]}`), nil
		case 2:
			if req.URL.Query().Get("cursor") != "opaque-page-2" {
				t.Fatal("server cursor was not preserved")
			}
			return sodaHTTPResponse(req, `{"playlist":{"id":"p","count_tracks":100},"next_cursor":"unused","has_more":false,"media_resources":[{"type":"track","entity":{"track_wrapper":{"track":{"id":"t2"}}}}]}`), nil
		default:
			t.Fatal("requested page after has_more=false")
			return nil, nil
		}
	})}}
	client.apiStrategy = sodaAPIStrategyUpstream
	playlist, err := client.GetPlaylist(context.Background(), "p")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || len(playlist.Tracks) != 2 {
		t.Fatalf("calls=%d tracks=%d, want 2/2", calls, len(playlist.Tracks))
	}
}

func TestPlayerInfoErrorRedactsSignedURL(t *testing.T) {
	client := &Client{httpClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return nil, errors.New("network unavailable")
	})}}
	_, err := client.fetchPlayInfos(context.Background(), "https://media.example/player?X-Amz-Signature=private-token")
	if err == nil || strings.Contains(err.Error(), "private-token") || strings.Contains(err.Error(), "X-Amz-Signature") {
		t.Fatalf("error did not redact URL: %v", err)
	}
	if !strings.Contains(err.Error(), "network unavailable") {
		t.Fatalf("transport error lost: %v", err)
	}
}

func TestClientSearchDeduplicatesAcrossGroups(t *testing.T) {
	client := sodaClientReturning(`{"result_groups":[{"data":[{"entity":{"track":{"id":"t1"}}}]},{"data":[{"entity":{"track":{"id":"t1"}}},{"entity":{"track":{"id":"t2"}}}]}]}`)
	client.apiStrategy = sodaAPIStrategyUpstream
	tracks, err := client.Search(context.Background(), "keyword", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(tracks) != 2 || tracks[0].ID != "t1" || tracks[1].ID != "t2" {
		t.Fatalf("tracks = %+v, want t1/t2", tracks)
	}
}
