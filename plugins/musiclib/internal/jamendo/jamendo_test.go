package jamendo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
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

func TestSearchMapsMetadataAndBestStream(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/api/search" || req.URL.Query().Get("type") != "track" {
			t.Fatalf("unexpected request URL: %s", req.URL)
		}
		call := req.Header.Get("x-jam-call")
		if !strings.HasPrefix(call, "$") || !strings.HasSuffix(call, "~") || req.Header.Get("x-jam-version") != XJamVersion {
			t.Fatalf("invalid Jamendo headers: call=%q version=%q", call, req.Header.Get("x-jam-version"))
		}
		body := `[{"id":11,"name":"Track","duration":135,"artistId":2,"albumId":3,"artist":{"id":2,"name":"Artist"},"album":{"id":3,"name":"Album"},"cover":{"big":{"size300":"https://img/cover.jpg"}},"download":{"flac":"https://cdn/track.flac","mp3":"https://cdn/track.mp3"}}]`
		return testResponse(req, body), nil
	})}

	songs, err := New(context.Background(), "", client).Search("track")
	if err != nil {
		t.Fatal(err)
	}
	if len(songs) != 1 {
		t.Fatalf("len(songs) = %d, want 1", len(songs))
	}
	song := songs[0]
	if song.ID != "11" || song.Duration != 135 || song.AlbumID != "3" || song.URL != "https://cdn/track.flac" || song.Ext != "flac" {
		t.Fatalf("unexpected song: %#v", song)
	}
}

func TestSearchRejectsUnavailableAndExplicitPreviewTracks(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := `[{"id":1,"name":"Unavailable","duration":10,"status":{"available":false},"download":{"mp3":"https://cdn/full.mp3"}},{"id":2,"name":"Preview","duration":10,"status":{"available":true},"download":{"mp3":"https://cdn/preview/clip.mp3"}}]`
		return testResponse(req, body), nil
	})}
	songs, err := New(context.Background(), "", client).Search("track")
	if err != nil {
		t.Fatal(err)
	}
	if len(songs) != 0 {
		t.Fatalf("unavailable/preview tracks exposed: %#v", songs)
	}
}

func TestParsePlaylistMapsCollection(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/api/playlists":
			return testResponse(req, `[{"id":7,"name":"Mix","user_name":"Owner","image":"https://img/mix.jpg","description":"Desc","tracks":[{"position":1,"id":11},{"position":2,"id":12}]}]`), nil
		case "/api/tracks":
			id := req.URL.Query().Get("id")
			body := fmt.Sprintf(`[{"id":%s,"name":"Track %s","duration":60,"artist":{"id":2,"name":"Artist"},"album":{"id":3,"name":"Album"},"stream":{"mp3":"https://cdn/%s.mp3"}}]`, id, id, id)
			return testResponse(req, body), nil
		default:
			t.Fatalf("unexpected request URL: %s", req.URL)
			return nil, nil
		}
	})}

	playlist, songs, err := New(context.Background(), "", client).ParsePlaylist("https://www.jamendo.com/playlist/7")
	if err != nil {
		t.Fatal(err)
	}
	if playlist.ID != "7" || playlist.Name != "Mix" || playlist.Creator != "Owner" || playlist.TrackCount != 2 {
		t.Fatalf("unexpected playlist: %#v", playlist)
	}
	if len(songs) != 2 || songs[0].ID != "11" || songs[1].ID != "12" {
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
		return testResponse(req, `[]`), nil
	})}

	_, err := New(ctx, "", client).Search("hello")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}
