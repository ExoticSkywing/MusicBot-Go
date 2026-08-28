package kuwo

import (
	"context"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/liuran001/MusicBot-Go/bot/platform"
)

var _ platform.Platform = (*KuwoPlatform)(nil)
var _ platform.URLMatcher = (*KuwoPlatform)(nil)
var _ platform.PlaylistURLMatcher = (*KuwoPlatform)(nil)
var _ platform.TextMatcher = (*KuwoPlatform)(nil)
var _ platform.MetadataProvider = (*KuwoPlatform)(nil)

func TestPlatformMetadataAndCapabilities(t *testing.T) {
	instance := NewPlatform(NewClient(time.Second, nil))
	if got := instance.Name(); got != "kuwo" {
		t.Fatalf("Name() = %q, want kuwo", got)
	}
	if !instance.SupportsDownload() || !instance.SupportsSearch() || !instance.SupportsLyrics() || instance.SupportsRecognition() {
		t.Fatalf("support flags = download:%t search:%t lyrics:%t recognition:%t",
			instance.SupportsDownload(),
			instance.SupportsSearch(),
			instance.SupportsLyrics(),
			instance.SupportsRecognition(),
		)
	}
	wantCapabilities := platform.Capabilities{
		Download:    true,
		Search:      true,
		Lyrics:      true,
		Recognition: false,
		HiRes:       true,
	}
	if got := instance.Capabilities(); got != wantCapabilities {
		t.Fatalf("Capabilities() = %#v, want %#v", got, wantCapabilities)
	}
	wantMeta := platform.Meta{
		Name:          "kuwo",
		DisplayName:   "酷我音乐",
		Emoji:         "🎧",
		Aliases:       []string{"kuwo", "kw", "酷我", "酷我音乐"},
		AllowGroupURL: true,
		GroupURLHosts: []string{"kuwo.cn", "www.kuwo.cn", "m.kuwo.cn"},
	}
	if got := instance.Metadata(); !reflect.DeepEqual(got, wantMeta) {
		t.Fatalf("Metadata() = %#v, want %#v", got, wantMeta)
	}
}

func TestPlatformDelegatesMatchers(t *testing.T) {
	instance := NewPlatform(NewClient(time.Second, nil))
	if id, ok := instance.MatchURL("https://www.kuwo.cn/play_detail/41378936"); !ok || id != "41378936" {
		t.Fatalf("MatchURL() = %q, %t", id, ok)
	}
	if id, ok := instance.MatchPlaylistURL("https://www.kuwo.cn/playlist_detail/2952464073"); !ok || id != "2952464073" {
		t.Fatalf("MatchPlaylistURL() = %q, %t", id, ok)
	}
	if id, ok := instance.MatchText("酷我：41378936"); !ok || id != "41378936" {
		t.Fatalf("MatchText() = %q, %t", id, ok)
	}
}

func TestPlatformGetPlaylistUsesContextPagination(t *testing.T) {
	tests := []struct {
		name      string
		withCtx   func(context.Context) context.Context
		wantPage  string
		wantLimit string
		total     string
		tracks    []string
		wantIDs   []string
	}{
		{
			name: "offset and limit",
			withCtx: func(ctx context.Context) context.Context {
				return platform.WithPlaylistLimit(platform.WithPlaylistOffset(ctx, 3), 2)
			},
			wantPage:  "2",
			wantLimit: "2",
			total:     "4",
			tracks:    []string{playlistTrackFixture("3"), playlistTrackFixture("4")},
			wantIDs:   []string{"4"},
		},
		{
			name: "offset only uses default limit",
			withCtx: func(ctx context.Context) context.Context {
				return platform.WithPlaylistOffset(ctx, 3)
			},
			wantPage:  "1",
			wantLimit: "50",
			total:     "4",
			tracks: []string{
				playlistTrackFixture("1"),
				playlistTrackFixture("2"),
				playlistTrackFixture("3"),
				playlistTrackFixture("4"),
			},
			wantIDs: []string{"4"},
		},
		{
			name: "limit only",
			withCtx: func(ctx context.Context) context.Context {
				return platform.WithPlaylistLimit(ctx, 2)
			},
			wantPage:  "1",
			wantLimit: "2",
			total:     "2",
			tracks:    []string{playlistTrackFixture("1"), playlistTrackFixture("2")},
			wantIDs:   []string{"1", "2"},
		},
		{
			name:      "no options",
			withCtx:   func(ctx context.Context) context.Context { return ctx },
			wantPage:  "1",
			wantLimit: "50",
			total:     "0",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, fixture := newPlaylistTestServer(t, func(_ int, request *http.Request) (int, string) {
				if request.URL.Query().Get("pn") != test.wantPage || request.URL.Query().Get("rn") != test.wantLimit {
					t.Errorf("query = %v, want pn=%s rn=%s", request.URL.Query(), test.wantPage, test.wantLimit)
				}
				return http.StatusOK, playlistFixture("123", test.total, test.tracks...)
			})
			defer fixture.Close()
			got, err := NewPlatform(client).GetPlaylist(test.withCtx(context.Background()), "123")
			if err != nil || got == nil {
				t.Fatalf("GetPlaylist() = %#v, %v", got, err)
			}
			gotIDs := make([]string, len(got.Tracks))
			for index := range got.Tracks {
				gotIDs[index] = got.Tracks[index].ID
			}
			if strings.Join(gotIDs, ",") != strings.Join(test.wantIDs, ",") {
				t.Fatalf("track IDs = %v, want %v", gotIDs, test.wantIDs)
			}
		})
	}
}

func TestPlatformDownloadQualityDelegatesToClient(t *testing.T) {
	apiServer := kuwoFixtureServer(t, `{"data":{"id":"41378936","name":"Song","artist":"Artist","duration":180}}`)
	defer apiServer.Close()
	client := newClientWithEndpoints(time.Second, nil, kuwoEndpoints{
		home:   apiServer.URL + "/",
		detail: apiServer.URL + "/detail",
		mobile: "https://mobile.test/play",
	})
	const totalSize = int64(7_200_000)
	probeClient := mp3ProbeTransport(t, totalSize, nil)
	probeTransport := probeClient.Transport
	apiOriginTransport := apiServer.Client().Transport
	client.apiHTTPClient.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Host == "mobile.test" {
			if request.URL.Query().Get("br") != "320kmp3" || request.URL.Query().Get("format") != "mp3" {
				t.Fatalf("mobile query = %v, want 320kmp3/mp3", request.URL.Query())
			}
			return response(http.StatusOK, nil, []byte(
				`{"code":200,"data":{"rid":"41378936","url":"https://er-sycdn.kuwo.cn/song.mp3","format":"mp3","bitrate":320,"duration":180,"type":0}}`,
			)), nil
		}
		return apiOriginTransport.RoundTrip(request)
	})
	client.mediaHTTPClient = &http.Client{
		Timeout: time.Second,
		Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			return probeTransport.RoundTrip(request)
		}),
	}
	got, err := NewPlatform(client).GetDownloadInfo(context.Background(), "41378936", platform.QualityHigh)
	if err != nil {
		t.Fatalf("GetDownloadInfo() = %v", err)
	}
	if got == nil || got.Format != "mp3" || got.Quality != platform.QualityHigh || got.Bitrate < 256 {
		t.Fatalf("download info = %#v", got)
	}
}

func TestPlatformNilClientAndReceiverAreUnavailable(t *testing.T) {
	instances := []*KuwoPlatform{NewPlatform(nil), nil}
	for _, instance := range instances {
		if got, err := instance.GetDownloadInfo(context.Background(), "1", platform.QualityHigh); got != nil || !errors.Is(err, platform.ErrUnavailable) {
			t.Errorf("GetDownloadInfo() = %#v, %v", got, err)
		}
		if got, err := instance.Search(context.Background(), "query", 1); got != nil || !errors.Is(err, platform.ErrUnavailable) {
			t.Errorf("Search() = %#v, %v", got, err)
		}
		if got, err := instance.GetLyrics(context.Background(), "1"); got != nil || !errors.Is(err, platform.ErrUnavailable) {
			t.Errorf("GetLyrics() = %#v, %v", got, err)
		}
		if got, err := instance.GetTrack(context.Background(), "1"); got != nil || !errors.Is(err, platform.ErrUnavailable) {
			t.Errorf("GetTrack() = %#v, %v", got, err)
		}
		if got, err := instance.GetPlaylist(context.Background(), "1"); got != nil || !errors.Is(err, platform.ErrUnavailable) {
			t.Errorf("GetPlaylist() = %#v, %v", got, err)
		}
		if got, err := instance.GetArtist(context.Background(), "1"); got != nil || !errors.Is(err, platform.ErrUnavailable) {
			t.Errorf("GetArtist() = %#v, %v", got, err)
		}
		if got, count, err := instance.GetArtistDetails(context.Background(), "1"); got != nil || count != 0 || !errors.Is(err, platform.ErrUnavailable) {
			t.Errorf("GetArtistDetails() = %#v, %d, %v", got, count, err)
		}
		if got, err := instance.GetAlbum(context.Background(), "1"); got != nil || !errors.Is(err, platform.ErrUnavailable) {
			t.Errorf("GetAlbum() = %#v, %v", got, err)
		}

		if got, err := instance.RecognizeAudio(context.Background(), strings.NewReader("audio")); got != nil || !errors.Is(err, platform.ErrUnsupported) {
			t.Errorf("RecognizeAudio() = %#v, %v", got, err)
		}
	}
}

func TestPlatformDelegatesSearchTrackAndLyrics(t *testing.T) {
	server := kuwoFixtureServer(t, `{"data":{"list":[]}}`)
	defer server.Close()
	client := newClientWithEndpoints(time.Second, nil, kuwoEndpoints{
		home:        server.URL + "/",
		search:      server.URL + "/search",
		detail:      server.URL + "/detail",
		wordLyric:   server.URL + "/word",
		mobileLyric: server.URL + "/mobile-lyric",
	})
	instance := NewPlatform(client)
	if tracks, err := instance.Search(context.Background(), "query", 1); err != nil || len(tracks) != 0 {
		t.Fatalf("Search() = %#v, %v", tracks, err)
	}
}

var _ io.Reader = strings.NewReader("")
