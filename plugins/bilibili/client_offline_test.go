package bilibili

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	logpkg "github.com/liuran001/MusicBot-Go/bot/logger"
)

// These tests exercise request shape and response parsing against a local
// server. The previous versions called the real Bilibili API and only skipped
// when CI was set, so a plain `go test ./...` went to the network and broke
// whenever a hard-coded video was removed upstream. The live checks now live in
// live_bilibili_test.go behind the `live` build tag.

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// getTestClient builds a client that talks to nobody; used by tests that only
// exercise local state (auto-renew lifecycle).
func getTestClient() *Client {
	logger, _ := logpkg.New("error", "text", false)
	return New(logger, "", "", false, 0, nil)
}

// newOfflineClient points every absolute bilibili URL at the test server while
// leaving the request the caller sees untouched, so code that inspects
// resp.Request.URL (ResolveB23ID) still observes the real hostname.
func newOfflineClient(t *testing.T, server *httptest.Server) *Client {
	t.Helper()
	logger, _ := logpkg.New("error", "text", false)
	client := New(logger, "", "", false, 0, nil)

	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse test server url: %v", err)
	}
	base := http.DefaultTransport
	// Drop both retry layers: the client retries API errors up to maxRetries
	// with a 1-5s backoff, which would make every error-path assertion wait out
	// the backoff instead of checking the error.
	client.maxRetries = 0
	client.httpClient.RetryMax = 0
	client.httpClient.HTTPClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		clone := req.Clone(req.Context())
		rewritten := *clone.URL
		rewritten.Scheme = target.Scheme
		rewritten.Host = target.Host
		clone.URL = &rewritten
		clone.Host = target.Host
		return base.RoundTrip(clone)
	})
	return client
}

func offlineContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestGetAudioSongInfoParsesResponse(t *testing.T) {
	var gotPath, gotQuery, gotReferer string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery, gotReferer = r.URL.Path, r.URL.RawQuery, r.Header.Get("Referer")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"msg":"success","data":{"id":3302094,"uid":7,"uname":"up","author":"作者","title":"标题","cover":"https://cover","intro":"简介","duration":215,"bvid":"BV1GJ411x7h7"}}`))
	}))
	defer server.Close()

	info, err := newOfflineClient(t, server).GetAudioSongInfo(offlineContext(t), 3302094)
	if err != nil {
		t.Fatalf("GetAudioSongInfo: %v", err)
	}
	if gotPath != "/audio/music-service-c/web/song/info" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotQuery != "sid=3302094" {
		t.Fatalf("query = %q, want sid=3302094", gotQuery)
	}
	// Bilibili rejects requests without a site Referer, so losing this header
	// would break every call at runtime while unit tests stayed green.
	if gotReferer != "https://www.bilibili.com/" {
		t.Fatalf("Referer = %q", gotReferer)
	}
	if info.ID != 3302094 || info.Title != "标题" || info.Duration != 215 {
		t.Fatalf("parsed info = %+v", info)
	}
}

// A non-zero code with HTTP 200 is Bilibili's normal way of reporting failure;
// it must surface as an error rather than an empty struct.
func TestGetAudioSongInfoSurfacesAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":-404,"msg":"啥都木有","data":null}`))
	}))
	defer server.Close()

	_, err := newOfflineClient(t, server).GetAudioSongInfo(offlineContext(t), 1)
	if err == nil {
		t.Fatal("expected an error for code=-404")
	}
	if !containsAll(err.Error(), "-404", "啥都木有") {
		t.Fatalf("error should carry the API code and message, got %v", err)
	}
}

func TestGetAudioStreamUrlParsesCdns(t *testing.T) {
	var gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"msg":"success","data":{"sid":3302094,"type":0,"size":3456789,"title":"标题","cdns":["https://cdn1/a.m4a","https://cdn2/a.m4a"]}}`))
	}))
	defer server.Close()

	stream, err := newOfflineClient(t, server).GetAudioStreamUrl(offlineContext(t), 3302094, 0)
	if err != nil {
		t.Fatalf("GetAudioStreamUrl: %v", err)
	}
	for _, want := range []string{"songid=3302094", "quality=0", "privilege=2", "platform=pc"} {
		if !containsAll(gotQuery, want) {
			t.Fatalf("query %q missing %q", gotQuery, want)
		}
	}
	if len(stream.Cdns) != 2 || stream.Cdns[0] != "https://cdn1/a.m4a" || stream.Size != 3456789 {
		t.Fatalf("parsed stream = %+v", stream)
	}
}

func TestGetVideoInfoUsesBvidAndAidParams(t *testing.T) {
	cases := []struct {
		id        string
		wantQuery string
	}{
		{id: "BV1GJ411x7h7", wantQuery: "bvid=BV1GJ411x7h7"},
		// "av" ids are passed through as a numeric aid, not as the raw string.
		{id: "av170001", wantQuery: "aid=170001"},
	}
	for _, tc := range cases {
		var gotPath, gotQuery string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":0,"message":"0","data":{"bvid":"BV1GJ411x7h7","aid":170001,"cid":137649199,"title":"视频标题"}}`))
		}))

		info, err := newOfflineClient(t, server).GetVideoInfo(offlineContext(t), tc.id)
		server.Close()
		if err != nil {
			t.Fatalf("GetVideoInfo(%s): %v", tc.id, err)
		}
		if gotPath != "/x/web-interface/view" {
			t.Fatalf("path = %q", gotPath)
		}
		if gotQuery != tc.wantQuery {
			t.Fatalf("query for %s = %q, want %q", tc.id, gotQuery, tc.wantQuery)
		}
		if info.Cid != 137649199 || info.Title != "视频标题" {
			t.Fatalf("parsed info = %+v", info)
		}
	}
}

func TestGetVideoPlayUrlReturnsDashAudio(t *testing.T) {
	var gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"message":"0","data":{"dash":{"audio":[
			{"id":30216,"baseUrl":"https://cdn/low.m4s","bandwidth":67000,"mimeType":"audio/mp4","codecs":"mp4a.40.5","backupUrl":["https://backup/low.m4s"]},
			{"id":30280,"baseUrl":"https://cdn/high.m4s","bandwidth":320000,"mimeType":"audio/mp4","codecs":"mp4a.40.2"}]}}}`))
	}))
	defer server.Close()

	streams, err := newOfflineClient(t, server).GetVideoPlayUrl(offlineContext(t), "BV1GJ411x7h7", 137649199)
	if err != nil {
		t.Fatalf("GetVideoPlayUrl: %v", err)
	}
	// fnval=16 is what makes the API return DASH at all; without it there is no
	// separate audio track to pick.
	for _, want := range []string{"bvid=BV1GJ411x7h7", "cid=137649199", "fnval=16"} {
		if !containsAll(gotQuery, want) {
			t.Fatalf("query %q missing %q", gotQuery, want)
		}
	}
	if len(streams) != 2 {
		t.Fatalf("got %d audio streams, want 2", len(streams))
	}
	if streams[0].Bandwidth != 67000 || streams[1].BaseURL != "https://cdn/high.m4s" {
		t.Fatalf("parsed streams = %+v", streams)
	}
	if urls := streams[0].CandidateURLs(); len(urls) != 2 || urls[1] != "https://backup/low.m4s" {
		t.Fatalf("CandidateURLs = %v, want primary then backup", urls)
	}
}

func TestGetVideoPlayUrlRejectsEmptyDash(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"message":"0","data":{"dash":{"audio":[]}}}`))
	}))
	defer server.Close()

	if _, err := newOfflineClient(t, server).GetVideoPlayUrl(offlineContext(t), "BV1GJ411x7h7", 1); err == nil {
		t.Fatal("expected an error when the DASH payload carries no audio")
	}
}

func TestResolveB23IDFollowsRedirectToVideo(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ysjTEMn" {
			http.Redirect(w, r, "https://www.bilibili.com/video/BV1GJ411x7h7", http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	id, err := newOfflineClient(t, server).ResolveB23ID(offlineContext(t), "ysjTEMn")
	if err != nil {
		t.Fatalf("ResolveB23ID: %v", err)
	}
	if id != "BV1GJ411x7h7" {
		t.Fatalf("resolved id = %q, want BV1GJ411x7h7", id)
	}
}

// A shortlink that lands somewhere other than a playable track must be an
// error, not a bogus track id handed to the download pipeline.
func TestResolveB23IDRejectsNonTrackTarget(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ysjTEMn" {
			http.Redirect(w, r, "https://www.bilibili.com/", http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	if _, err := newOfflineClient(t, server).ResolveB23ID(offlineContext(t), "ysjTEMn"); err == nil {
		t.Fatal("expected an error when the shortlink does not resolve to a track")
	}
}

func containsAll(haystack string, needles ...string) bool {
	for _, needle := range needles {
		found := false
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
