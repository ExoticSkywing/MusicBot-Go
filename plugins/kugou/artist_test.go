package kugou

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/liuran001/MusicBot-Go/bot/platform"
)

func TestBuildSingerAvatarURL(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "fills the size placeholder and upgrades to https",
			raw:  "http://singerimg.kugou.com/uploadpic/softhead/{size}/20260324/x.jpg",
			want: "https://singerimg.kugou.com/uploadpic/softhead/400/20260324/x.jpg",
		},
		{
			name: "already https is preserved",
			raw:  "https://singerimg.kugou.com/uploadpic/softhead/400/a.jpg",
			want: "https://singerimg.kugou.com/uploadpic/softhead/400/a.jpg",
		},
		{name: "empty", raw: "", want: ""},
		{name: "blank", raw: "   ", want: ""},
		// An unresolved placeholder would render as a broken image.
		{name: "unknown placeholder is rejected", raw: "http://x.kugou.com/{foo}/a.jpg", want: ""},
		{name: "relative path is rejected", raw: "uploadpic/softhead/{size}/a.jpg", want: ""},
		{name: "non-http scheme is rejected", raw: "ftp://x.kugou.com/a.jpg", want: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := buildSingerAvatarURL(test.raw); got != test.want {
				t.Errorf("buildSingerAvatarURL(%q) = %q, want %q", test.raw, got, test.want)
			}
		})
	}
}

// singerFixture mirrors the live /api/v5/singer/info payload.
const singerFixture = `{"status":1,"errcode":0,"data":{"singerid":3520,"singername":"周杰伦",` +
	`"imgurl":"http://singerimg.kugou.com/uploadpic/softhead/{size}/20260324/x.jpg","songcount":1761,"albumcount":49}}`

func decodeSingerFixture(t *testing.T, payload string) *kugouSingerInfo {
	t.Helper()
	var info kugouSingerInfo
	if err := json.Unmarshal([]byte(payload), &info); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return &info
}

func TestConvertSingerInfo(t *testing.T) {
	artist, count, err := convertSingerInfo(decodeSingerFixture(t, singerFixture), "3520")
	if err != nil {
		t.Fatalf("convertSingerInfo() = %v", err)
	}
	if artist.ID != "3520" || artist.Platform != "kugou" || artist.Name != "周杰伦" {
		t.Errorf("artist = %#v", artist)
	}
	if artist.URL != "https://www.kugou.com/singer/3520.html" {
		t.Errorf("URL = %q", artist.URL)
	}
	if artist.AvatarURL != "https://singerimg.kugou.com/uploadpic/softhead/400/20260324/x.jpg" {
		t.Errorf("AvatarURL = %q", artist.AvatarURL)
	}
	if count != 1761 {
		t.Errorf("track count = %d, want 1761", count)
	}
}

func TestConvertSingerInfoRejectsBadPayloads(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		id      string
		want    error
	}{
		{"nil-safe missing status", `{"status":0,"data":{"singername":"X"}}`, "3520", platform.ErrNotFound},
		{"empty name", `{"status":1,"data":{"singerid":3520,"singername":"  "}}`, "3520", platform.ErrNotFound},
		// The identity guard is what stops one artist being shown under another's ID.
		{"identity mismatch", singerFixture, "9999", platform.ErrUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := convertSingerInfo(decodeSingerFixture(t, test.payload), test.id)
			if !errors.Is(err, test.want) {
				t.Errorf("convertSingerInfo() = %v, want %v", err, test.want)
			}
		})
	}
	if _, _, err := convertSingerInfo(nil, "3520"); !errors.Is(err, platform.ErrNotFound) {
		t.Errorf("convertSingerInfo(nil) = %v, want ErrNotFound", err)
	}
}

func TestGetSingerInfoValidatesIDBeforeNetwork(t *testing.T) {
	// A nil client would fail on any network call, so reaching ErrNotFound
	// proves the ID was rejected before a request was attempted.
	var client *Client
	for _, id := range []string{"", "  ", "abc", "35a20", "-1", "3.5"} {
		if _, _, err := client.GetSingerInfo(t.Context(), id); !errors.Is(err, platform.ErrNotFound) {
			t.Errorf("GetSingerInfo(%q) = %v, want ErrNotFound", id, err)
		}
	}
}

func TestMatchArtistURL(t *testing.T) {
	matcher := NewURLMatcher()
	matches := []struct{ url, id string }{
		{"https://www.kugou.com/singer/3520.html", "3520"},
		{"https://www.kugou.com/yy/singer/home/3520.html", "3520"},
		{"https://www.kugou.com/singer/info/3520/", "3520"},
		{"https://www.kugou.com/singer/info/3520", "3520"},
		{"http://m.kugou.com/singer/3520", "3520"},
	}
	for _, test := range matches {
		if id, ok := matcher.MatchArtistURL(test.url); !ok || id != test.id {
			t.Errorf("MatchArtistURL(%q) = %q, %t; want %q, true", test.url, id, ok, test.id)
		}
	}

	rejects := []string{
		"https://www.kugou.com/song/abc.html",
		"https://www.kugou.com/album/123.html",
		"https://www.kugou.com/singer/",
		"https://www.kugou.com/singer/abc.html",
		"https://evil.example/singer/3520.html",
		"",
		"   ",
	}
	for _, rawURL := range rejects {
		if id, ok := matcher.MatchArtistURL(rawURL); ok {
			t.Errorf("MatchArtistURL(%q) = %q, true; want no match", rawURL, id)
		}
	}
}

// TestPlatformSatisfiesArtistRouting pins the interfaces the router discovers by
// type assertion; a signature drift would silently stop artist links matching.
func TestPlatformSatisfiesArtistRouting(t *testing.T) {
	instance := NewPlatform(NewClient("", nil))
	if _, ok := any(instance).(platform.ArtistURLMatcher); !ok {
		t.Error("KugouPlatform does not satisfy platform.ArtistURLMatcher")
	}
	type artistDetailProvider interface {
		GetArtistDetails(ctx context.Context, artistID string) (*platform.Artist, int, error)
	}
	if _, ok := any(instance).(artistDetailProvider); !ok {
		t.Error("KugouPlatform does not satisfy artistDetailProvider")
	}
}
