package fivesing

import (
	"context"
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
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}

func TestSearchMapsMetadata(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != "search.5sing.kugou.com" || req.URL.Path != "/home/json" {
			t.Fatalf("unexpected request URL: %s", req.URL)
		}
		if got := req.URL.Query().Get("type"); got != "0" {
			t.Fatalf("type = %q, want 0", got)
		}
		return testResponse(req, `{"list":[{"songId":123,"songName":"<em class=\"keyword\">Hello &amp; Bye</em>","singer":"Singer &amp; One","songSize":1280000,"typeEname":"yc"}]}`), nil
	})}

	songs, err := New(context.Background(), "cookie=value", client).Search("hello")
	if err != nil {
		t.Fatal(err)
	}
	if len(songs) != 1 {
		t.Fatalf("len(songs) = %d, want 1", len(songs))
	}
	song := songs[0]
	if song.ID != "123|yc" || song.Name != "Hello & Bye" || song.Artist != "Singer & One" {
		t.Fatalf("unexpected song identity: %#v", song)
	}
	if song.Size != 1280000 || song.Duration != 32 {
		t.Fatalf("unexpected size/duration: size=%d duration=%d", song.Size, song.Duration)
	}
	if song.Extra["songid"] != "123" || song.Extra["songtype"] != "yc" {
		t.Fatalf("unexpected extra: %#v", song.Extra)
	}
}

func TestRemoveEmTagsAcceptsBareAndAttributedTags(t *testing.T) {
	for _, input := range []string{"<em>Song</em>", `<em class="keyword">Song</em>`} {
		if got := removeEmTags(input); got != "Song" {
			t.Fatalf("removeEmTags(%q) = %q, want Song", input, got)
		}
	}
}

func TestParseFetchesMetadataWithoutResolvingAudio(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/song/newget" {
			t.Fatalf("metadata lookup requested media endpoint: %s", req.URL)
		}
		return testResponse(req, `{"data":{"SN":"Song","SS":8000000,"user":{"NN":"Artist","I":"https://img/cover.jpg"}}}`), nil
	})}

	song, err := New(context.Background(), "", client).Parse("https://5sing.kugou.com/yc/123.html")
	if err != nil {
		t.Fatal(err)
	}
	if song.ID != "123|yc" || song.Name != "Song" || song.URL != "" || song.Size != 8000000 || song.Duration != 200 {
		t.Fatalf("unexpected metadata-only result: %#v", song)
	}
}

func TestDownloadRejectsExplicitPreviewURL(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return testResponse(req, `{"code":1000,"data":{"squrl":"https://cdn.example/preview/song.mp3"}}`), nil
	})}
	song := &model.Song{Source: "fivesing", ID: "123|yc", Extra: map[string]string{"songid": "123", "songtype": "yc"}}
	if _, err := New(context.Background(), "", client).GetDownloadURL(song); !errors.Is(err, model.ErrPreviewOnly) {
		t.Fatalf("preview error = %v, want ErrPreviewOnly", err)
	}
}

func TestParsePlaylistMapsMetadataAndSongs(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Host {
		case "mobileapi.5sing.kugou.com":
			return testResponse(req, `{"data":{"T":"Set","C":"Description","P":"https://img/cover.jpg","H":12,"E":1,"user":{"ID":99,"NN":"Owner"}}}`), nil
		case "5sing.kugou.com":
			body := `<html><li class="p_rel"><a href="http://5sing.kugou.com/yc/123.html">Tune &amp; More</a><a class="s_soner" href="#">Singer &amp; One</a></li></html>`
			return testResponse(req, body), nil
		default:
			t.Fatalf("unexpected request URL: %s", req.URL)
			return nil, nil
		}
	})}

	playlist, songs, err := New(context.Background(), "", client).ParsePlaylist("http://5sing.kugou.com/99/dj/55.html")
	if err != nil {
		t.Fatal(err)
	}
	if playlist.ID != "55" || playlist.Name != "Set" || playlist.Creator != "Owner" || playlist.TrackCount != 1 {
		t.Fatalf("unexpected playlist: %#v", playlist)
	}
	if len(songs) != 1 || songs[0].ID != "123|yc" || songs[0].Name != "Tune & More" || songs[0].Artist != "Singer & One" {
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
		return testResponse(req, `{"list":[]}`), nil
	})}

	_, err := New(ctx, "", client).Search("hello")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}
