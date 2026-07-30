package kuwo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/liuran001/MusicBot-Go/bot/platform"
)

// albumPageServer serves paginated albumInfo responses shaped like the live API:
// a window of musicList entries plus the album-wide total, and code -1 with a
// null payload once the requested page runs past the end.
func albumPageServer(t *testing.T, albumID string, total int) (*Client, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.SetCookie(w, &http.Cookie{Name: kuwoSessionCookie, Value: "abcdefghijklmnop", Path: "/"})
			return
		}
		calls.Add(1)
		page, _ := strconv.Atoi(r.URL.Query().Get("pn"))
		limit, _ := strconv.Atoi(r.URL.Query().Get("rn"))
		base := (page - 1) * limit
		if base >= total {
			_, _ = w.Write([]byte(`{"code":-1,"data":null}`))
			return
		}
		count := total - base
		if count > limit {
			count = limit
		}
		items := make([]string, 0, count)
		for i := 0; i < count; i++ {
			index := base + i + 1
			items = append(items, fmt.Sprintf(
				`{"rid":%d,"name":"Track %d","artist":"周杰伦","artistid":336,"album":"叶惠美","albumid":%s}`,
				1000+index, index, albumID))
		}
		_, _ = w.Write([]byte(fmt.Sprintf(
			`{"code":200,"data":{"album":"叶惠美","albumid":%s,"artist":"周杰伦","artistid":336,`+
				`"pic":"https://img2.kuwo.cn/cover.jpg","total":%d,"musicList":[%s]}}`,
			albumID, total, strings.Join(items, ","))))
	}))
	t.Cleanup(server.Close)
	client := newClientWithEndpoints(time.Second, nil, kuwoEndpoints{
		home:  server.URL + "/",
		album: server.URL + "/album",
	})
	return client, &calls
}

func TestCollectionIDRoundTrip(t *testing.T) {
	if got := encodeAlbumCollectionID("1293"); got != "album:1293" {
		t.Fatalf("encodeAlbumCollectionID() = %q", got)
	}
	if got := encodeAlbumCollectionID("  "); got != "" {
		t.Errorf("encodeAlbumCollectionID(blank) = %q, want empty", got)
	}

	tests := []struct{ raw, kind, id string }{
		{"album:1293", "album", "1293"},
		{"1121866085", "playlist", "1121866085"},
		{"", "", ""},
		{"album:", "album", ""},
		// A bare playlist ID must never be mistaken for an album.
		{"album1293", "playlist", "album1293"},
	}
	for _, test := range tests {
		kind, id := parseCollectionID(test.raw)
		if kind != test.kind || id != test.id {
			t.Errorf("parseCollectionID(%q) = (%q, %q), want (%q, %q)", test.raw, kind, id, test.kind, test.id)
		}
	}
}

func TestMatchPlaylistURLEncodesAlbumLinks(t *testing.T) {
	matcher := NewURLMatcher()
	if id, ok := matcher.MatchPlaylistURL("https://www.kuwo.cn/album_detail/1293"); !ok || id != "album:1293" {
		t.Errorf("album link = %q, %t; want album:1293", id, ok)
	}
	// Playlist links must keep their bare IDs so existing IDs stay valid.
	if id, ok := matcher.MatchPlaylistURL("https://www.kuwo.cn/playlist_detail/1121866085"); !ok || id != "1121866085" {
		t.Errorf("playlist link = %q, %t; want the bare id", id, ok)
	}
	for _, rawURL := range []string{
		"https://www.kuwo.cn/album_detail/",
		"https://www.kuwo.cn/album_detail/abc",
		"https://evil.example/album_detail/1293",
	} {
		if id, ok := matcher.MatchPlaylistURL(rawURL); ok {
			t.Errorf("MatchPlaylistURL(%q) = %q, true; want no match", rawURL, id)
		}
	}
}

func TestGetPlaylistExpandsAlbumCollection(t *testing.T) {
	client, _ := albumPageServer(t, "1293", 11)
	collection, err := client.GetPlaylist(context.Background(), "album:1293", 0, 50)
	if err != nil {
		t.Fatalf("GetPlaylist(album) = %v", err)
	}
	if collection.ID != "album:1293" || collection.Platform != "kuwo" || collection.Title != "叶惠美" {
		t.Errorf("collection = %#v", collection)
	}
	if collection.URL != "https://www.kuwo.cn/album_detail/1293" {
		t.Errorf("URL = %q", collection.URL)
	}
	if collection.Creator != "周杰伦" || collection.TrackCount != 11 || len(collection.Tracks) != 11 {
		t.Errorf("creator = %q, total = %d, tracks = %d", collection.Creator, collection.TrackCount, len(collection.Tracks))
	}
	// Tracks must carry the same artist/album links as any other Kuwo track.
	first := collection.Tracks[0]
	if len(first.Artists) != 1 || first.Artists[0].URL != "https://www.kuwo.cn/singer_detail/336" {
		t.Errorf("track artists = %#v", first.Artists)
	}
	if first.Album == nil || first.Album.URL != "https://www.kuwo.cn/album_detail/1293" {
		t.Errorf("track album = %#v", first.Album)
	}
}

// TestGetAlbumPlaylistPaginationWindows checks the offset window, including the
// case where a window straddles two upstream pages.
func TestGetAlbumPlaylistPaginationWindows(t *testing.T) {
	tests := []struct {
		name        string
		offset      int
		limit       int
		wantTracks  int
		wantFirst   string
		wantAPICall int32
	}{
		{name: "aligned first page", offset: 0, limit: 5, wantTracks: 5, wantFirst: "Track 1", wantAPICall: 1},
		{name: "aligned second page", offset: 5, limit: 5, wantTracks: 5, wantFirst: "Track 6", wantAPICall: 1},
		{name: "straddles two pages", offset: 3, limit: 5, wantTracks: 5, wantFirst: "Track 4", wantAPICall: 2},
		{name: "tail shorter than limit", offset: 8, limit: 5, wantTracks: 3, wantFirst: "Track 9", wantAPICall: 2},
		{name: "negative offset clamps", offset: -10, limit: 5, wantTracks: 5, wantFirst: "Track 1", wantAPICall: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, calls := albumPageServer(t, "1293", 11)
			collection, err := client.GetAlbumPlaylist(context.Background(), "1293", test.offset, test.limit)
			if err != nil {
				t.Fatalf("GetAlbumPlaylist() = %v", err)
			}
			if len(collection.Tracks) != test.wantTracks {
				t.Fatalf("tracks = %d, want %d", len(collection.Tracks), test.wantTracks)
			}
			if collection.Tracks[0].Title != test.wantFirst {
				t.Errorf("first track = %q, want %q", collection.Tracks[0].Title, test.wantFirst)
			}
			if got := calls.Load(); got != test.wantAPICall {
				t.Errorf("API calls = %d, want %d", got, test.wantAPICall)
			}
			if collection.TrackCount != 11 {
				t.Errorf("TrackCount = %d, want the album-wide total 11", collection.TrackCount)
			}
		})
	}
}

func TestGetAlbumPlaylistRejectsBadIDsAndPages(t *testing.T) {
	client, _ := albumPageServer(t, "1293", 11)
	for _, id := range []string{"", " ", "abc", "12a", "-1", "999999999999999999999"} {
		if _, err := client.GetAlbumPlaylist(context.Background(), id, 0, 10); !errors.Is(err, platform.ErrNotFound) {
			t.Errorf("GetAlbumPlaylist(%q) = %v, want ErrNotFound", id, err)
		}
	}
	// Past the end upstream answers code -1, which is a missing page.
	if _, err := client.GetAlbumPlaylist(context.Background(), "1293", 500, 50); !errors.Is(err, platform.ErrNotFound) {
		t.Errorf("out-of-range offset = %v, want ErrNotFound", err)
	}
}

// TestGetAlbumPlaylistRejectsIdentityAndShapeDrift is the guard against serving
// a different album's tracks, or a page whose length contradicts the total.
func TestGetAlbumPlaylistRejectsIdentityAndShapeDrift(t *testing.T) {
	fixtures := []struct {
		name    string
		payload string
	}{
		{"identity mismatch", `{"code":200,"data":{"album":"Other","albumid":999,"total":1,"musicList":[{"rid":1,"name":"X"}]}}`},
		{"page shorter than total promises", `{"code":200,"data":{"album":"A","albumid":1293,"total":10,"musicList":[{"rid":1,"name":"X"}]}}`},
		{"missing total", `{"code":200,"data":{"album":"A","albumid":1293,"musicList":[]}}`},
		{"duplicate JSON keys", `{"code":200,"code":200,"data":{"album":"A","albumid":1293,"total":0,"musicList":[]}}`},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			client := entityClient(t, fixture.payload)
			if _, err := client.GetAlbumPlaylist(context.Background(), "1293", 0, 10); !errors.Is(err, platform.ErrUnavailable) {
				t.Errorf("GetAlbumPlaylist() = %v, want ErrUnavailable", err)
			}
		})
	}
}

// TestAlbumCollectionSurvivesJSONRoundTrip pins that the prefixed ID stays
// intact through the serialisation the bot layer performs on collections.
func TestAlbumCollectionSurvivesJSONRoundTrip(t *testing.T) {
	client, _ := albumPageServer(t, "1293", 3)
	collection, err := client.GetPlaylist(context.Background(), "album:1293", 0, 50)
	if err != nil {
		t.Fatalf("GetPlaylist() = %v", err)
	}
	encoded, err := json.Marshal(collection)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded platform.Playlist
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.ID != "album:1293" {
		t.Errorf("round-tripped ID = %q, want album:1293", decoded.ID)
	}
	if kind, id := parseCollectionID(decoded.ID); kind != "album" || id != "1293" {
		t.Errorf("re-parsed = (%q, %q), want (album, 1293)", kind, id)
	}
}
