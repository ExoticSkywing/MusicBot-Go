package qqmusic

import (
	"context"
	"testing"
	"time"

	"github.com/liuran001/MusicBot-Go/bot/platform"
)

func TestMatchArtistURL(t *testing.T) {
	matcher := NewURLMatcher()
	const mid = "0025NhlN2yWrP4"
	matches := []struct{ url, id string }{
		{"https://y.qq.com/n/ryqq_v2/singer/" + mid, mid},
		{"https://y.qq.com/n/ryqq/singer/" + mid, mid},
		{"https://y.qq.com/n/yqq/singer/" + mid + ".html", mid},
		{"https://y.qq.com/singer/" + mid, mid},
		{"https://y.qq.com/n3/other/pages/details/singer.html?singermid=" + mid, mid},
	}
	for _, test := range matches {
		if id, ok := matcher.MatchArtistURL(test.url); !ok || id != test.id {
			t.Errorf("MatchArtistURL(%q) = %q, %t; want %q, true", test.url, id, ok, test.id)
		}
	}

	rejects := []string{
		"https://y.qq.com/n/ryqq_v2/songDetail/003OUlho2HcRHC",
		"https://y.qq.com/n/ryqq_v2/albumDetail/002fRO0N4FftrX",
		"https://y.qq.com/n/ryqq_v2/playlist/123456",
		"https://y.qq.com/n/ryqq_v2/singer/",
		// Path traversal must not survive as an ID.
		"https://y.qq.com/n/ryqq_v2/singer/../../etc",
		"https://y.qq.com/n/ryqq_v2/singer/has-dash",
		"https://evil.example/n/ryqq_v2/singer/" + mid,
		"",
		"   ",
	}
	for _, rawURL := range rejects {
		if id, ok := matcher.MatchArtistURL(rawURL); ok {
			t.Errorf("MatchArtistURL(%q) = %q, true; want no match", rawURL, id)
		}
	}
}

func TestBuildSingerAvatarURL(t *testing.T) {
	tests := []struct{ pmid, mid, want string }{
		{
			pmid: "0025NhlN2yWrP4_11", mid: "0025NhlN2yWrP4",
			want: "https://y.qq.com/music/photo_new/T001R300x300M0000025NhlN2yWrP4_11.jpg",
		},
		{
			// Falls back to the plain mid when no photo mid is supplied.
			pmid: "", mid: "0025NhlN2yWrP4",
			want: "https://y.qq.com/music/photo_new/T001R300x300M0000025NhlN2yWrP4.jpg",
		},
		{pmid: "", mid: "", want: ""},
		{pmid: "  ", mid: "  ", want: ""},
		{pmid: "../../evil", mid: "", want: ""},
		{pmid: "has space", mid: "", want: ""},
	}
	for _, test := range tests {
		if got := buildSingerAvatarURL(test.pmid, test.mid); got != test.want {
			t.Errorf("buildSingerAvatarURL(%q, %q) = %q, want %q", test.pmid, test.mid, got, test.want)
		}
	}
}

func TestGetSingerDetailValidatesMidBeforeNetwork(t *testing.T) {
	// A nil client cannot perform a request, so ErrNotFound proves the mid was
	// rejected before any network call.
	var client *Client
	for _, id := range []string{"", "   ", "../etc", "has-dash", "has space", "a/b"} {
		if _, _, err := client.GetSingerDetail(context.Background(), id); err == nil {
			t.Errorf("GetSingerDetail(%q) unexpectedly succeeded", id)
		}
	}
}

// TestPlatformSatisfiesArtistRouting pins the interfaces the router discovers by
// type assertion; a signature drift would silently stop artist links matching.
func TestPlatformSatisfiesArtistRouting(t *testing.T) {
	instance := NewPlatform(NewClient("", time.Second, nil, false, 0, nil))
	if _, ok := any(instance).(platform.ArtistURLMatcher); !ok {
		t.Error("QQMusicPlatform does not satisfy platform.ArtistURLMatcher")
	}
	type artistDetailProvider interface {
		GetArtistDetails(ctx context.Context, artistID string) (*platform.Artist, int, error)
	}
	if _, ok := any(instance).(artistDetailProvider); !ok {
		t.Error("QQMusicPlatform does not satisfy artistDetailProvider")
	}
}
