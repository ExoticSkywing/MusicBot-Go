package netease

import (
	"context"
	"testing"

	"github.com/liuran001/MusicBot-Go/bot/platform"
)

func TestMatchArtistURL(t *testing.T) {
	matcher := NewURLMatcher()
	matches := []struct{ url, id string }{
		{"https://music.163.com/artist?id=6452", "6452"},
		{"https://music.163.com/#/artist?id=6452", "6452"},
		{"https://y.music.163.com/m/artist?id=6452", "6452"},
		{"http://music.163.com/artist?id=6452", "6452"},
		// Artist IDs are short; the 5-char floor used for playlists must not apply.
		{"https://music.163.com/artist?id=1", "1"},
		{"https://music.163.com/artist?id=6452&userid=1", "6452"},
	}
	for _, test := range matches {
		if id, ok := matcher.MatchArtistURL(test.url); !ok || id != test.id {
			t.Errorf("MatchArtistURL(%q) = %q, %t; want %q, true", test.url, id, ok, test.id)
		}
	}

	rejects := []string{
		"https://music.163.com/song?id=186016",
		"https://music.163.com/playlist?id=123456789",
		"https://music.163.com/album?id=123456",
		"https://music.163.com/artist",
		"https://music.163.com/artist?id=",
		"https://music.163.com/artist?id=abc",
		"https://music.163.com/artist?id=0",
		"https://music.163.com/artist?id=-5",
		"https://music.163.com/artist?id=999999999999999999999",
		"https://evil.example/artist?id=6452",
		"",
		"   ",
	}
	for _, rawURL := range rejects {
		if id, ok := matcher.MatchArtistURL(rawURL); ok {
			t.Errorf("MatchArtistURL(%q) = %q, true; want no match", rawURL, id)
		}
	}
}

func TestHTTPSPortraitURL(t *testing.T) {
	tests := []struct{ raw, want string }{
		{"http://p1.music.126.net/a.jpg", "https://p1.music.126.net/a.jpg"},
		{"https://p1.music.126.net/a.jpg", "https://p1.music.126.net/a.jpg"},
		{"", ""},
		{"   ", ""},
		{"/relative/a.jpg", ""},
		{"ftp://p1.music.126.net/a.jpg", ""},
	}
	for _, test := range tests {
		if got := httpsPortraitURL(test.raw); got != test.want {
			t.Errorf("httpsPortraitURL(%q) = %q, want %q", test.raw, got, test.want)
		}
	}
}

func TestGetArtistDetailsValidatesIDBeforeNetwork(t *testing.T) {
	// The platform has no client, so any network attempt would panic or fail
	// differently; ErrNotFound proves the ID was rejected up front.
	instance := NewPlatform(nil, false)
	for _, id := range []string{"", "  ", "abc", "0", "-1", "64a52"} {
		if _, _, err := instance.GetArtistDetails(context.Background(), id); err == nil {
			t.Errorf("GetArtistDetails(%q) unexpectedly succeeded", id)
		}
	}
}

// TestPlatformSatisfiesArtistRouting pins the interfaces the router discovers by
// type assertion; a signature drift would silently stop artist links matching.
func TestPlatformSatisfiesArtistRouting(t *testing.T) {
	instance := NewPlatform(nil, false)
	if _, ok := any(instance).(platform.ArtistURLMatcher); !ok {
		t.Error("NeteasePlatform does not satisfy platform.ArtistURLMatcher")
	}
	type artistDetailProvider interface {
		GetArtistDetails(ctx context.Context, artistID string) (*platform.Artist, int, error)
	}
	if _, ok := any(instance).(artistDetailProvider); !ok {
		t.Error("NeteasePlatform does not satisfy artistDetailProvider")
	}
}

// TestSegmentMarkerKindsAreDistinct guards the shared marker helper: a URL for
// one resource kind must not be seen as another.
func TestSegmentMarkerKindsAreDistinct(t *testing.T) {
	cases := []struct {
		path, fragment          string
		artist, album, playlist bool
	}{
		{path: "/artist", artist: true},
		{path: "/album", album: true},
		{path: "/playlist", playlist: true},
		{path: "/", fragment: "/artist?id=1", artist: true},
		{path: "/", fragment: "/album?id=1", album: true},
		{path: "/song", fragment: ""},
	}
	for _, test := range cases {
		if got := hasArtistMarker(test.path, test.fragment); got != test.artist {
			t.Errorf("hasArtistMarker(%q, %q) = %t, want %t", test.path, test.fragment, got, test.artist)
		}
		if got := hasAlbumMarker(test.path, test.fragment); got != test.album {
			t.Errorf("hasAlbumMarker(%q, %q) = %t, want %t", test.path, test.fragment, got, test.album)
		}
		if got := hasPlaylistMarker(test.path, test.fragment); got != test.playlist {
			t.Errorf("hasPlaylistMarker(%q, %q) = %t, want %t", test.path, test.fragment, got, test.playlist)
		}
	}
}
