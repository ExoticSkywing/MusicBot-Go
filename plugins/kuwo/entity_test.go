package kuwo

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/liuran001/MusicBot-Go/bot/platform"
)

// entityServer serves a fixed album/artist payload behind the signed-session
// handshake the client performs.
func entityServer(t *testing.T, fixture string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.SetCookie(w, &http.Cookie{Name: kuwoSessionCookie, Value: "abcdefghijklmnop", Path: "/"})
			return
		}
		_, _ = w.Write([]byte(fixture))
	}))
}

func entityClient(t *testing.T, fixture string) *Client {
	t.Helper()
	server := entityServer(t, fixture)
	t.Cleanup(server.Close)
	return newClientWithEndpoints(time.Second, nil, kuwoEndpoints{
		home:   server.URL + "/",
		album:  server.URL + "/album",
		artist: server.URL + "/artist",
	})
}

// Live payload shape from /api/www/album/albumInfo.
const albumFixture = `{"code":200,"data":{"album":"叶惠美","albumid":1293,"artist":"周杰伦","artistid":336,` +
	`"pic":"https://img2.kuwo.cn/star/albumcover/300/s3s94/93/211513640.jpg","total":11,` +
	`"releaseDate":"2003-07-31","albuminfo":"Jay&nbsp;Chou 的第四张专辑"}}`

// Live payload shape from /api/www/artist/artist.
const artistFixture = `{"code":200,"data":{"id":336,"name":"周杰伦","musicNum":1698,"albumNum":45,` +
	`"pic300":"https://star.kuwo.cn/star/starheads/300/s4s56/58/291211030.jpg",` +
	`"pic120":"https://star.kuwo.cn/star/starheads/120/s4s56/58/291211030.jpg"}}`

func TestGetAlbumConvertsLivePayload(t *testing.T) {
	album, err := entityClient(t, albumFixture).GetAlbum(context.Background(), "1293")
	if err != nil {
		t.Fatalf("GetAlbum() = %v", err)
	}
	if album.ID != "1293" || album.Platform != "kuwo" || album.Title != "叶惠美" {
		t.Errorf("identity = %#v", album)
	}
	if album.URL != "https://www.kuwo.cn/album_detail/1293" {
		t.Errorf("URL = %q", album.URL)
	}
	if album.TrackCount != 11 || album.Year != 2003 {
		t.Errorf("TrackCount = %d, Year = %d, want 11 and 2003", album.TrackCount, album.Year)
	}
	if album.ReleaseDate == nil || album.ReleaseDate.Format("2006-01-02") != "2003-07-31" {
		t.Errorf("ReleaseDate = %v", album.ReleaseDate)
	}
	if len(album.Artists) != 1 || album.Artists[0].ID != "336" ||
		album.Artists[0].URL != "https://www.kuwo.cn/singer_detail/336" {
		t.Errorf("Artists = %#v", album.Artists)
	}
	// HTML entities in the blurb must be decoded, not passed through raw.
	if strings.Contains(album.Description, "&nbsp;") || !strings.Contains(album.Description, "Jay Chou") {
		t.Errorf("Description = %q, want decoded entities", album.Description)
	}
}

func TestGetArtistConvertsLivePayload(t *testing.T) {
	artist, count, err := entityClient(t, artistFixture).GetArtist(context.Background(), "336")
	if err != nil {
		t.Fatalf("GetArtist() = %v", err)
	}
	if artist.ID != "336" || artist.Platform != "kuwo" || artist.Name != "周杰伦" {
		t.Errorf("identity = %#v", artist)
	}
	if artist.URL != "https://www.kuwo.cn/singer_detail/336" {
		t.Errorf("URL = %q", artist.URL)
	}
	// pic300 is the largest portrait and must win over the smaller variants.
	if !strings.Contains(artist.AvatarURL, "/300/") {
		t.Errorf("AvatarURL = %q, want the 300px portrait", artist.AvatarURL)
	}
	if count != 1698 {
		t.Errorf("track count = %d, want 1698", count)
	}
}

// TestEntityLookupsRejectMismatchedIdentity is the guard against handing back a
// different album or artist than the caller asked for.
func TestEntityLookupsRejectMismatchedIdentity(t *testing.T) {
	if _, err := entityClient(t, albumFixture).GetAlbum(context.Background(), "999"); !errors.Is(err, platform.ErrUnavailable) {
		t.Errorf("GetAlbum(mismatched) = %v, want ErrUnavailable", err)
	}
	if _, _, err := entityClient(t, artistFixture).GetArtist(context.Background(), "999"); !errors.Is(err, platform.ErrUnavailable) {
		t.Errorf("GetArtist(mismatched) = %v, want ErrUnavailable", err)
	}
}

func TestEntityLookupsRejectBadInputAndEnvelopes(t *testing.T) {
	tests := []struct {
		name    string
		id      string
		fixture string
		want    error
	}{
		{"non-numeric album id", "abc", albumFixture, platform.ErrNotFound},
		{"empty album id", "", albumFixture, platform.ErrNotFound},
		{"upstream error code", "1293", `{"code":404,"data":null}`, platform.ErrNotFound},
		{"null data", "1293", `{"code":200,"data":null}`, platform.ErrNotFound},
		{"missing title", "1293", `{"code":200,"data":{"albumid":1293}}`, platform.ErrNotFound},
		{"duplicate JSON keys", "1293", `{"code":200,"code":200,"data":{"album":"X","albumid":1293}}`, platform.ErrUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := entityClient(t, test.fixture).GetAlbum(context.Background(), test.id)
			if !errors.Is(err, test.want) {
				t.Errorf("GetAlbum() = %v, want %v", err, test.want)
			}
		})
	}

	if _, _, err := entityClient(t, artistFixture).GetArtist(context.Background(), "abc"); !errors.Is(err, platform.ErrNotFound) {
		t.Errorf("GetArtist(non-numeric) = %v, want ErrNotFound", err)
	}
	if _, _, err := entityClient(t, `{"code":200,"data":{"id":336}}`).GetArtist(context.Background(), "336"); !errors.Is(err, platform.ErrNotFound) {
		t.Errorf("GetArtist(nameless) = %v, want ErrNotFound", err)
	}
}

// TestGetAlbumTolueratesMissingOptionalFields keeps a sparse-but-valid album
// usable rather than failing the whole lookup.
func TestGetAlbumToleratesMissingOptionalFields(t *testing.T) {
	album, err := entityClient(t, `{"code":200,"data":{"album":"Single","albumid":7,"releaseDate":"0000-00-00","total":0}}`).
		GetAlbum(context.Background(), "7")
	if err != nil {
		t.Fatalf("GetAlbum() = %v", err)
	}
	if album.Title != "Single" || album.URL != "https://www.kuwo.cn/album_detail/7" {
		t.Errorf("album = %#v", album)
	}
	if album.ReleaseDate != nil || album.Year != 0 {
		t.Errorf("placeholder release date leaked: %v / %d", album.ReleaseDate, album.Year)
	}
	if album.TrackCount != 0 {
		t.Errorf("TrackCount = %d, want 0", album.TrackCount)
	}
	if len(album.Artists) != 0 {
		t.Errorf("Artists = %#v, want none", album.Artists)
	}
}

// TestGetArtistRejectsRelativePortraitPaths guards the portrait fallback: the
// album-cover prefix used for relative track covers points at the wrong bucket
// for artist portraits, so a relative value must yield no avatar at all rather
// than a broken link.
func TestGetArtistRejectsRelativePortraitPaths(t *testing.T) {
	artist, _, err := entityClient(t, `{"code":200,"data":{"id":336,"name":"周杰伦","pic300":"120/s4s56/58/291211030.jpg"}}`).
		GetArtist(context.Background(), "336")
	if err != nil {
		t.Fatalf("GetArtist() = %v", err)
	}
	if artist.AvatarURL != "" {
		t.Errorf("AvatarURL = %q, want empty for a relative upstream path", artist.AvatarURL)
	}
}

// TestPlatformSatisfiesArtistRouting pins the interfaces the Telegram router
// discovers by type assertion. A signature drift would not fail compilation —
// the platform would just silently stop matching artist links.
func TestPlatformSatisfiesArtistRouting(t *testing.T) {
	instance := NewPlatform(NewClient(time.Second, nil))

	if _, ok := any(instance).(platform.ArtistURLMatcher); !ok {
		t.Error("KuwoPlatform does not satisfy platform.ArtistURLMatcher; artist links will never route")
	}
	// Mirrors the handler's private artistDetailProvider interface.
	type artistDetailProvider interface {
		GetArtistDetails(ctx context.Context, artistID string) (*platform.Artist, int, error)
	}
	if _, ok := any(instance).(artistDetailProvider); !ok {
		t.Error("KuwoPlatform does not satisfy artistDetailProvider; the artist card will lose its track count")
	}
}

func TestMatchArtistURL(t *testing.T) {
	matcher := NewURLMatcher()
	matches := []struct{ url, id string }{
		{"https://www.kuwo.cn/singer_detail/336", "336"},
		{"http://kuwo.cn/singer_detail/336", "336"},
		{"https://m.kuwo.cn/newh5app/singer_detail/336", "336"},
	}
	for _, test := range matches {
		if id, ok := matcher.MatchArtistURL(test.url); !ok || id != test.id {
			t.Errorf("MatchArtistURL(%q) = %q, %t; want %q, true", test.url, id, ok, test.id)
		}
	}

	rejects := []string{
		"https://www.kuwo.cn/play_detail/228908",
		"https://www.kuwo.cn/playlist_detail/1121866085",
		"https://www.kuwo.cn/album_detail/1293",
		"https://evil.example/singer_detail/336",
		"https://www.kuwo.cn/singer_detail/",
		"https://www.kuwo.cn/singer_detail/abc",
		"https://www.kuwo.cn/singer_detail/336/extra",
		"ftp://www.kuwo.cn/singer_detail/336",
		"",
	}
	for _, rawURL := range rejects {
		if id, ok := matcher.MatchArtistURL(rawURL); ok {
			t.Errorf("MatchArtistURL(%q) = %q, true; want no match", rawURL, id)
		}
	}
}
