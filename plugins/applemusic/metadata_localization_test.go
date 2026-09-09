package applemusic

import (
	"context"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/liuran001/MusicBot-Go/bot/i18n"
	"github.com/liuran001/MusicBot-Go/bot/platform"
)

type localizationTransport func(*http.Request) (*http.Response, error)

func (f localizationTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func metadataContext(lang string) context.Context {
	return i18n.WithLocalizer(context.Background(), i18n.For(lang))
}
func localizationPlatform(t *testing.T, body string, status int, inspect func(*http.Request)) *AppleMusicPlatform {
	t.Helper()
	c := NewClient("private-user-token", "tr", "en-GB", time.Second, nil)
	c.developerToken = "public-developer-token"
	c.httpClient.Transport = localizationTransport(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Cookie") != "" || r.Header.Get("media-user-token") != "" {
			t.Error("account credentials sent on anonymous metadata request")
		}
		if r.Header.Get("Authorization") != "Bearer public-developer-token" {
			t.Error("developer token missing")
		}
		if inspect != nil {
			inspect(r)
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})
	return NewPlatform(c)
}
func TestLocalizedMetadataPreservesPlaybackIdentity(t *testing.T) {
	calls := 0
	p := localizationPlatform(t, `{"data":[{"id":"cn-id","attributes":{"name":"晴天","artistName":"周杰伦","albumName":"叶惠美","isrc":"same"}}]}`, 200, func(r *http.Request) {
		calls++
		if r.URL.Path != "/v1/catalog/cn/songs" || r.URL.Query().Get("l") != "zh-Hans-CN" || r.URL.Query().Get("filter[equivalents]") != "tr-id" {
			t.Errorf("unexpected URL: %s", r.URL)
		}
	})
	yes := true
	source := &platform.Track{ID: "tr-id", Platform: "applemusic", Title: "Sunny Day", ISRC: "same", URL: "tr-link", CoverURL: "cover", Duration: time.Minute, AtmosAvailable: &yes, LyricsAvailable: &yes, Artists: []platform.Artist{{ID: "artist-id", Name: "Jay Chou", URL: "artist-link"}}, Album: &platform.Album{ID: "album-id", Title: "Yeh, Hwei-Mei", URL: "album-link"}}
	got, err := p.LocalizeTrack(metadataContext("zh"), source)
	if err != nil {
		t.Fatal(err)
	}
	want := *source
	want.Title = "晴天"
	want.MetadataLanguage = "zh"
	want.Artists = []platform.Artist{{ID: "artist-id", Name: "周杰伦", URL: "artist-link"}}
	album := *source.Album
	album.Title = "叶惠美"
	want.Album = &album
	if !reflect.DeepEqual(*got, want) {
		t.Fatalf("unexpected localized track: %+v", got)
	}
	if source.Title != "Sunny Day" || source.Artists[0].Name != "Jay Chou" || source.Album.Title != "Yeh, Hwei-Mei" {
		t.Fatal("source mutated")
	}
	if p.client.storefront != "tr" || p.client.language != "en-GB" {
		t.Fatal("account settings mutated")
	}
	again, err := p.LocalizeTrack(metadataContext("zh"), got)
	if err != nil || again != got || calls != 1 {
		t.Fatal("same language refetched or replaced")
	}
}
func TestLocalizedMetadataLocaleAndFallback(t *testing.T) {
	for _, tc := range []struct{ lang, sf, tag string }{{"en", "us", "en-US"}, {"ja", "jp", "ja"}, {"ru", "ru", "ru"}} {
		t.Run(tc.lang, func(t *testing.T) {
			p := localizationPlatform(t, `{"data":[]}`, 200, func(r *http.Request) {
				if r.URL.Path != "/v1/catalog/"+tc.sf+"/songs" || r.URL.Query().Get("l") != tc.tag {
					t.Fatal(r.URL)
				}
			})
			source := &platform.Track{ID: "id", Title: "Original"}
			got, err := p.LocalizeTrack(metadataContext(tc.lang), source)
			if err == nil || got != source || got.MetadataLanguage != "" {
				t.Fatal("missing equivalent changed source")
			}
		})
	}
	for _, body := range []string{`{"data":[{"id":"wrong","attributes":{"name":"Other","isrc":"other"}}]}`, `{"data":[{"id":"empty","attributes":{"isrc":"same"}}]}`} {
		p := localizationPlatform(t, body, 200, nil)
		source := &platform.Track{ID: "id", ISRC: "same", Title: "Original"}
		if got, err := p.LocalizeTrack(metadataContext("zh"), source); err == nil || got != source {
			t.Fatal("invalid candidate accepted")
		}
	}
	p := localizationPlatform(t, `{}`, 503, nil)
	source := &platform.Track{ID: "id", Title: "Original"}
	if got, err := p.LocalizeTrack(metadataContext("zh"), source); err == nil || got != source {
		t.Fatal("HTTP failure replaced source")
	}
}
func TestLocalizedMetadataKeepsExistingNamesAndArtistIdentity(t *testing.T) {
	source := &platform.Track{Title: "原中文名", Artists: []platform.Artist{{ID: "a", Name: "A"}, {ID: "b", Name: "B"}}, Album: &platform.Album{Title: "原中文专辑"}}
	var resource appleMusicResource
	resource.Attributes.Name = "新的中文名"
	resource.Attributes.AlbumName = "新专辑"
	// Exercise the artist helper directly with reversed catalog ordering.
	artists := append([]platform.Artist(nil), source.Artists...)
	got := overlayLocalizedArtistNames(artists, []platform.Artist{{ID: "b", Name: "乙"}, {ID: "a", Name: "甲"}}, "", "zh")
	if got[0].Name != "甲" || got[1].Name != "乙" {
		t.Fatal("artist names lost identity")
	}
	result := overlayLocalizedTrackNames(source, resource, "zh")
	if result.Title != source.Title || result.Album.Title != source.Album.Title {
		t.Fatal("existing Chinese names overwritten")
	}
	names := overlayLocalizedArtistNames([]platform.Artist{{Name: "A"}, {Name: "B"}}, []platform.Artist{{Name: "甲"}, {Name: "乙"}}, "", "zh")
	if names[0].Name != "甲" || names[1].Name != "乙" {
		t.Fatal("name-only legacy artists not localized")
	}
	if chooseLocalizedName("Türkçe", "English", "en") != "English" {
		t.Fatal("Latin script mistaken for English")
	}
}

func TestMetadataLanguageDoesNotChangeLyricsRequest(t *testing.T) {
	c := NewClient("account-token", "tr", "en-GB", time.Second, nil)
	c.developerToken = "developer-token"
	c.storefrontDetected = true
	c.httpClient.Transport = localizationTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/v1/catalog/tr/songs/id/lyrics" || r.URL.Query().Get("l") != "en-GB" {
			t.Fatalf("lyrics moved out of account locale: %s", r.URL)
		}
		if r.Header.Get("media-user-token") != "account-token" || !strings.Contains(r.Header.Get("Cookie"), "media-user-token=account-token") {
			t.Fatal("lyrics lost account authentication")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"data":[{"attributes":{"ttml":"original lyrics"}}]}`)), Header: make(http.Header)}, nil
	})
	got, err := c.GetLyricsTTML(metadataContext("zh"), "id")
	if err != nil || got != "original lyrics" {
		t.Fatalf("lyrics changed: %q %v", got, err)
	}
}
