package joox

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/liuran001/MusicBot-Go/plugins/musiclib/internal/model"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func testResponse(req *http.Request, body string) *http.Response {
	return testStatusResponse(req, http.StatusOK, body)
}

func testStatusResponse(req *http.Request, status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}

func TestParseNormalizesIDAndMapsSelectedStream(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != "cache.api.joox.com" || req.URL.Path != "/page/single" {
			t.Fatalf("unexpected request URL: %s", req.URL)
		}
		if got := req.URL.Query().Get("id"); got != "song+token=" {
			t.Fatalf("id = %q, want song+token=", got)
		}
		body := `{"single":{"id":"song+token=","name":"Song","album_id":"album-token","album_name":"Album","artist_list":[{"name":"Singer"}],"images":[{"width":300,"url":"https://img/cover.jpg"}],"play_duration":248,"vip_flag":1,"is_playable":true,"node_is_preview":false,"error_code":0,"status_code":0,"play_url_list":["https://cdn/song.mp3?a=1&amp;b=2"]}}`
		return testResponse(req, body), nil
	})}

	song, err := New(context.Background(), "cookie=value", client).Parse("https://www.joox.com/hk/single/song%2Btoken%3D")
	if err != nil {
		t.Fatal(err)
	}
	if song.ID != "song+token=" || song.URL != "https://cdn/song.mp3?a=1&b=2" || song.Duration != 248 {
		t.Fatalf("unexpected song identity: %#v", song)
	}
	if song.Name != "Song" || song.Artist != "Singer" || song.AlbumID != "album-token" || !song.IsVIP {
		t.Fatalf("unexpected metadata: %#v", song)
	}
	if song.Ext != "mp3" {
		t.Fatalf("unexpected stream extension: %q", song.Ext)
	}
}

func TestLegacyFallbackProvidesMetadataButNeverUnverifiedMedia(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == "cache.api.joox.com" || req.URL.Host == "www.joox.com" {
			return testStatusResponse(req, http.StatusNotFound, ""), nil
		}
		body := `MusicInfoCallback({"msong":"Song","minterval":100,"mp3Url":"https://cdn/song.mp3","kbps_map":{"320":"999999","128":"123456"}})`
		return testResponse(req, body), nil
	})}

	song, err := New(context.Background(), "", client).Parse("https://www.joox.com/sg/single/song-token-123")
	if err != nil {
		t.Fatal(err)
	}
	if song.Name != "Song" || song.Duration != 100 {
		t.Fatalf("unexpected legacy metadata: %#v", song)
	}
	if song.URL != "" || song.Ext != "" || song.Bitrate != 0 || song.Size != 0 {
		t.Fatalf("unverified legacy stream exposed: %#v", song)
	}
}

func TestPreviewMetadataWorksButFullAudioIsRejected(t *testing.T) {
	const previewURL = "https://cdn/preview.mp3"
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch {
		case req.URL.Host == "cache.api.joox.com" && req.URL.Path == "/page/single":
			body := `{"single":{"id":"preview-song","name":"Preview Song","album_id":"album","album_name":"Album","artist_list":[{"name":"Singer"}],"images":[{"width":300,"url":"https://img/cover.jpg"}],"play_duration":33,"vip_flag":1,"is_playable":false,"node_is_preview":true,"error_code":9009002,"status_code":0,"refrain_url":"` + previewURL + `","play_url_list":["` + previewURL + `"]}}`
			return testResponse(req, body), nil
		case req.URL.Host == "api.joox.com" && req.URL.Path == "/web-fcgi-bin/web_get_songinfo":
			return testStatusResponse(req, http.StatusNotFound, ""), nil
		default:
			t.Fatalf("unexpected request URL: %s", req.URL)
			return nil, nil
		}
	})}

	j := New(context.Background(), "", client)
	song, err := j.Parse("preview-song")
	if err != nil {
		t.Fatal(err)
	}
	if song.Name != "Preview Song" || song.Artist != "Singer" || song.Album != "Album" || !song.IsVIP {
		t.Fatalf("unexpected metadata: %#v", song)
	}
	if song.Duration != 0 || song.URL != "" {
		t.Fatalf("preview leaked as full-track metadata: %#v", song)
	}
	if _, err := j.GetDownloadURL(song); !errors.Is(err, ErrFullAudioUnavailable) {
		t.Fatalf("download error = %v, want ErrFullAudioUnavailable", err)
	}
}

func TestParseFallsBackToOfficialPageNextData(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == "cache.api.joox.com" {
			return testStatusResponse(req, http.StatusNotFound, ""), nil
		}
		if req.URL.Host == "www.joox.com" && req.URL.Path == "/hk/single/page-song" {
			body := `<html><script id="__NEXT_DATA__" type="application/json">{"props":{"pageProps":{"passingArgumentsData":{"id":"page-song","name":"Page Song","album_id":"album","album_name":"Page Album","artist_list":[{"name":"Page Singer"}],"images":[{"width":300,"url":"https://img/page.jpg"}],"play_duration":30,"is_playable":false,"node_is_preview":true,"error_code":9009002,"status_code":0}}}}</script></html>`
			return testResponse(req, body), nil
		}
		t.Fatalf("unexpected request URL: %s", req.URL)
		return nil, nil
	})}

	song, err := New(context.Background(), "", client).Parse("https://www.joox.com/hk/single/page-song")
	if err != nil {
		t.Fatal(err)
	}
	if song.ID != "page-song" || song.Name != "Page Song" || song.Artist != "Page Singer" || song.AlbumID != "album" || song.URL != "" {
		t.Fatalf("unexpected page song: %#v", song)
	}
}

func TestLyricsUseCurrentPageAPI(t *testing.T) {
	want := "[00:01.00]line"
	encoded := base64.StdEncoding.EncodeToString([]byte(want))
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != "cache.api.joox.com" || req.URL.Path != "/page/single" {
			t.Fatalf("unexpected request URL: %s", req.URL)
		}
		body := `{"single":{"id":"lyric-song","name":"Song","lrc_exist":1,"lrc_content":"` + encoded + `","is_playable":false,"node_is_preview":true,"error_code":9009002,"status_code":0}}`
		return testResponse(req, body), nil
	})}

	got, err := New(context.Background(), "", client).GetLyrics(&model.Song{Source: "joox", ID: "lyric-song"})
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("lyrics = %q, want %q", got, want)
	}
}

func TestFullStreamRequiresExplicitFullPlaybackDecision(t *testing.T) {
	for _, body := range []string{
		`{"play_url_list":["https://cdn/full.mp3"]}`,
		`{"is_playable":true,"error_code":0,"status_code":0,"play_url_list":["https://cdn/full.mp3"]}`,
		`{"is_playable":true,"node_is_preview":false,"status_code":0,"play_url_list":["https://cdn/full.mp3"]}`,
		`{"is_playable":true,"node_is_preview":false,"error_code":0,"status_code":0,"refrain_url":"https://cdn/preview.mp3","play_url_list":["https://cdn/preview.mp3"]}`,
	} {
		var single jooxPageSingle
		if err := json.Unmarshal([]byte(body), &single); err != nil {
			t.Fatal(err)
		}
		if got := fullStreamURL(&single); got != "" {
			t.Fatalf("ambiguous/preview stream accepted: %s", got)
		}
	}
}

func TestGetPlaylistSongsFallsBackToPageData(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == "cache.api.joox.com" {
			return testResponse(req, `{}`), nil
		}
		if req.URL.Host == "www.joox.com" && req.URL.Path == "/id/playlist/PL+1==" {
			body := `<html><script id="__NEXT_DATA__" type="application/json">{"props":{"pageProps":{"allPlaylistTracks":{"tracks":{"totalCount":1,"items":[{"id":"song+1==","name":"Track","album_id":"album+1==","album_name":"Album","artist_list":[{"id":"artist","name":"Singer"}],"play_duration":123,"images":[{"width":300,"url":"https://img/track.jpg"}]}]}},"playlistDetailList":{"id":"PL+1==","name":"Mix","creator":"Owner","description":"Desc"}}}}</script></html>`
			return testResponse(req, body), nil
		}
		t.Fatalf("unexpected request URL: %s", req.URL)
		return nil, nil
	})}

	songs, err := New(context.Background(), "cookie=value", client).GetPlaylistSongs("PL+1==")
	if err != nil {
		t.Fatal(err)
	}
	if len(songs) != 1 || songs[0].ID != "song+1==" || songs[0].AlbumID != "album+1==" || songs[0].Duration != 123 {
		t.Fatalf("unexpected songs: %#v", songs)
	}
}

func TestSearchHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if err := req.Context().Err(); err != nil {
			return nil, err
		}
		return testResponse(req, `{"section_list":[]}`), nil
	})}

	_, err := New(ctx, "", client).Search("hello")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestEmptyCookieDoesNotInjectUpstreamSession(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if cookie := req.Header.Get("Cookie"); cookie != "" {
			t.Fatalf("unexpected Cookie header: %q", cookie)
		}
		if forwarded := req.Header.Get("X-Forwarded-For"); forwarded != "" {
			t.Fatalf("unexpected X-Forwarded-For header: %q", forwarded)
		}
		return testResponse(req, `{"section_list":[]}`), nil
	})}

	if _, err := New(context.Background(), "", client).Search("hello"); err != nil {
		t.Fatal(err)
	}
}
