package musiclib

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liuran001/MusicBot-Go/bot/platform"
	"github.com/liuran001/MusicBot-Go/plugins/musiclib/internal/model"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func jsonResponse(req *http.Request, body string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: req}
}

const miguSongJSON = `{"contentId":"123","copyrightId":"600123","name":"Song","singers":[{"name":"Artist"}],"duration":200,"rateFormats":[{"formatType":"PQ","resourceType":"2","size":"3200000","fileType":"mp3"}]}`

func TestSearchAdapters(t *testing.T) {
	cases := []struct{ name, body, id string }{
		{"migu", `{"songResultData":{"result":[` + miguSongJSON + `]}}`, "123"},
		{"qianqian", `{"data":{"typeTrack":[{"TSID":"T123","title":"Song","artist":[{"name":"Artist"}],"duration":200}]}}`, "T123"},
		{"fivesing", `{"list":[{"songId":123,"songName":"<em>Song</em>","singer":"Artist","songSize":8000000,"typeEname":"yc"}]}`, "yc_123"},
		{"jamendo", `[{"id":123,"name":"Song","artist":{"name":"Artist"},"duration":200,"download":{"mp3":"https://cdn.example/song.mp3"}}]`, "123"},
		{"joox", `{"section_list":[{"item_list":[{"song":[{"song_info":{"id":"abc+def123==","name":"Song","artist_list":[{"name":"Artist"}],"play_duration":200}}]}]}]}`, "abc+def123=="},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.Header.Get("Cookie") != "session=test" {
					t.Error("per-platform cookie was not passed")
				}
				return jsonResponse(req, tc.body), nil
			})}
			p := NewPlatform(tc.name, "session=test", client, time.Second)
			tracks, err := p.Search(context.Background(), "Song & Artist", 1)
			if err != nil || len(tracks) != 1 {
				t.Fatalf("Search = %#v, %v", tracks, err)
			}
			track := tracks[0]
			if track.ID != tc.id || track.Title != "Song" || track.Platform != tc.name || track.Duration != 200*time.Second || len(track.Artists) != 1 || track.Artists[0].Name != "Artist" {
				t.Fatalf("incorrect conversion: %#v", track)
			}
			id, ok := p.MatchURL(track.URL)
			if !ok || id != track.ID {
				t.Fatalf("search result cannot round-trip through link: %s -> %s", track.URL, id)
			}
		})
	}
}

func TestMiguDownloadPreservesClientAndMetadataOnlyLookup(t *testing.T) {
	downloadCalls := 0
	client := &http.Client{Timeout: time.Second, Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch {
		case strings.HasSuffix(req.URL.Path, "/resourceinfo.do"):
			if req.URL.Query().Get("resourceId") != "123" {
				t.Fatalf("wrong content ID: %s", req.URL.Query().Get("resourceId"))
			}
			return jsonResponse(req, `{"resource":[`+miguSongJSON+`]}`), nil
		case strings.HasSuffix(req.URL.Path, "/listenSong.do"):
			downloadCalls++
			if req.URL.Query().Get("contentId") != "123" || req.Header.Get("Cookie") != "session=test" {
				t.Fatal("download lost ID or credential")
			}
			resp := jsonResponse(req, "")
			resp.StatusCode = http.StatusFound
			resp.Header.Set("Location", "https://cdn.example/song.mp3")
			return resp, nil
		default:
			t.Fatalf("download resolver followed media redirect: %s", req.URL.Host)
			return nil, errors.New("unexpected request")
		}
	})}
	p := NewPlatform("migu", "session=test", client, time.Second)
	track, err := p.GetTrack(context.Background(), "123")
	if err != nil || track.Title != "Song" || downloadCalls != 0 {
		t.Fatalf("metadata lookup fetched audio: track=%v err=%v calls=%d", track, err, downloadCalls)
	}
	info, err := p.GetDownloadInfo(context.Background(), "123", platform.QualityHiRes)
	if err != nil || info.URL != "https://cdn.example/song.mp3" || info.Format != "mp3" || info.Quality != platform.QualityStandard || downloadCalls != 1 {
		t.Fatalf("download = %#v, %v; calls=%d", info, err, downloadCalls)
	}
	if client.CheckRedirect != nil || client.Timeout != time.Second {
		t.Fatal("shared client mutated")
	}
}

func TestMiguRejectsNonMediaResponse(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusForbidden, http.StatusFound} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if strings.HasSuffix(req.URL.Path, "/resourceinfo.do") {
					return jsonResponse(req, `{"resource":[`+miguSongJSON+`]}`), nil
				}
				resp := jsonResponse(req, `{"error":"not playable"}`)
				resp.StatusCode = status
				return resp, nil
			})}
			info, err := NewPlatform("migu", "", client, time.Second).GetDownloadInfo(context.Background(), "123", platform.QualityHigh)
			if err == nil || info != nil {
				t.Fatalf("non-media response became a download: %#v, %v", info, err)
			}
		})
	}
}

func TestMiguCDNFallbackDoesNotReuseLosslessBitrate(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if strings.HasSuffix(req.URL.Path, "/resourceinfo.do") {
			song := strings.Replace(miguSongJSON, `"fileType":"mp3"`, `"fileType":"flac"`, 1)
			return jsonResponse(req, `{"resource":[`+song+`]}`), nil
		}
		resp := jsonResponse(req, "")
		resp.StatusCode = http.StatusFound
		resp.Header.Set("Location", "https://cdn.example/fallback.mp3")
		return resp, nil
	})}
	info, err := NewPlatform("migu", "", client, time.Second).GetDownloadInfo(context.Background(), "123", platform.QualityLossless)
	if err != nil || info.Format != "mp3" || info.Bitrate != 0 || info.Quality != platform.QualityStandard {
		t.Fatalf("lossless metadata leaked into MP3 fallback: %#v, %v", info, err)
	}
}

func TestQianqianRejectsPreviewAndReportsSelectedQuality(t *testing.T) {
	for _, full := range []bool{false, true} {
		t.Run(map[bool]string{false: "preview_only", true: "full_track"}[full], func(t *testing.T) {
			client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.Path == "/v1/song/info" {
					return jsonResponse(req, `{"data":[{"title":"Song","duration":200}]}`), nil
				}
				if req.URL.Path == "/v1/song/tracklink" && req.URL.Query().Get("rate") == "128" && full {
					return jsonResponse(req, `{"data":{"path":"https://cdn.example/song.mp3","format":"mp3"}}`), nil
				}
				return jsonResponse(req, `{"data":{"trail_audio_info":{"path":"https://cdn.example/preview.mp3"}}}`), nil
			})}
			p := NewPlatform("qianqian", "", client, time.Second)
			info, err := p.GetDownloadInfo(context.Background(), "T123", platform.QualityLossless)
			if !full {
				if err == nil || info != nil || !errors.Is(err, platform.ErrIncompleteAudio) {
					t.Fatalf("preview exposed as complete song: %#v, %v", info, err)
				}
				return
			}
			if err != nil || info.Bitrate != 128 || info.Quality != platform.QualityStandard {
				t.Fatalf("wrong selected quality: %#v, %v", info, err)
			}
		})
	}
}

func TestQianqianStringPreviewDurationDoesNotDiscardFullTrack(t *testing.T) {
	for _, full := range []bool{false, true} {
		t.Run(fmt.Sprint(full), func(t *testing.T) {
			client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.Path == "/v1/song/info" {
					return jsonResponse(req, `{"data":[{"title":"深夜书店","duration":260}]}`), nil
				}
				path := ""
				if full {
					path = `"path":"https://cdn.example/full.flac","format":"flac","duration":260,`
				}
				return jsonResponse(req, `{"data":{`+path+`"trail_audio_info":{"path":"https://cdn.example/preview.mp3","duration":"30","start_time":"0"}}}`), nil
			})}
			info, err := NewPlatform("qianqian", "", client, time.Second).GetDownloadInfo(context.Background(), "T10038972257", platform.QualityLossless)
			if full {
				if err != nil || info == nil || info.Format != "flac" || info.URL != "https://cdn.example/full.flac" {
					t.Fatalf("full audio discarded: %#v, %v", info, err)
				}
			} else if info != nil || !errors.Is(err, platform.ErrIncompleteAudio) {
				t.Fatalf("preview not rejected: %#v, %v", info, err)
			}
		})
	}
}

func TestQianqianRejectsPathWhoseDurationIsIncomplete(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/v1/song/info" {
			return jsonResponse(req, `{"data":[{"title":"Song","duration":200}]}`), nil
		}
		return jsonResponse(req, `{"data":{"path":"https://cdn.example/song.mp3","format":"mp3","duration":30}}`), nil
	})}
	info, err := NewPlatform("qianqian", "", client, time.Second).GetDownloadInfo(context.Background(), "T123", platform.QualityHigh)
	if info != nil || !errors.Is(err, platform.ErrIncompleteAudio) {
		t.Fatalf("short data.path exposed: %#v, %v", info, err)
	}
}

func TestQianqianAlbumPaginationAndLyrics(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/v1/album/info":
			return jsonResponse(req, `{"state":true,"data":{"albumAssetCode":"P123","title":"Album","artist":[{"name":"Artist"}],"trackList":[{"assetId":"T1","title":"First","sort":1},{"assetId":"T2","title":"Second","sort":2},{"assetId":"T3","title":"Third","sort":3}]}}`), nil
		case "/v1/song/info":
			return jsonResponse(req, `{"data":[{"title":"Song","lyric":"https://cdn.example/lyrics.lrc"}]}`), nil
		case "/lyrics.lrc":
			return jsonResponse(req, "[00:01.234]first line\n[00:02.50]second line"), nil
		default:
			t.Fatalf("unexpected request: %s", req.URL.Path)
			return nil, errors.New("unexpected request")
		}
	})}
	p := NewPlatform("qianqian", "", client, time.Second)
	ctx := platform.WithPlaylistLimit(platform.WithPlaylistOffset(context.Background(), 1), 1)
	list, err := p.GetPlaylist(ctx, "album:P123")
	if err != nil || len(list.Tracks) != 1 || list.Tracks[0].ID != "T2" || list.TrackCount != 3 || list.ID != "album:P123" {
		t.Fatalf("wrong album page: %#v, %v", list, err)
	}
	lyrics, err := p.GetLyrics(context.Background(), "T123")
	if err != nil || len(lyrics.Timestamped) != 2 || lyrics.Timestamped[0].Time != 1230*time.Millisecond {
		t.Fatalf("wrong lyrics: %#v, %v", lyrics, err)
	}
}

func TestRequestContextsAreIsolated(t *testing.T) {
	for _, name := range []string{"migu", "qianqian", "fivesing", "jamendo", "joox"} {
		t.Run(name, func(t *testing.T) {
			started := make(chan struct{}, 2)
			client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				started <- struct{}{}
				<-req.Context().Done()
				return nil, req.Context().Err()
			})}
			p := NewPlatform(name, "", client, time.Second)
			ctx1, cancel1 := context.WithCancel(context.Background())
			defer cancel1()
			ctx2, cancel2 := context.WithCancel(context.Background())
			defer cancel2()
			var wg sync.WaitGroup
			for _, ctx := range []context.Context{ctx1, ctx2} {
				wg.Go(func() {
					_, err := p.Search(ctx, "query", 1)
					if !errors.Is(err, context.Canceled) {
						t.Errorf("cancellation not propagated: %v", err)
					}
				})
			}
			<-started
			<-started
			cancel1()
			if ctx2.Err() != nil {
				t.Fatal("second request canceled by first")
			}
			cancel2()
			wg.Wait()
		})
	}
}

func TestUnsupportedCapabilitiesAndInvalidIDs(t *testing.T) {
	p := NewPlatform("jamendo", "", nil, time.Second)
	if p.SupportsLyrics() || p.Capabilities().Lyrics {
		t.Fatal("upstream stub advertised as lyric support")
	}
	if _, err := p.GetLyrics(context.Background(), "123"); !errors.Is(err, platform.ErrUnsupported) {
		t.Fatalf("lyrics error = %v", err)
	}
	if _, err := p.GetTrack(context.Background(), "https://evil.example"); !errors.Is(err, platform.ErrNotFound) {
		t.Fatalf("invalid ID should be rejected without network: %v", err)
	}
	if _, err := NewPlatform("fivesing", "", nil, time.Second).GetAlbum(context.Background(), "123"); !errors.Is(err, platform.ErrUnsupported) {
		t.Fatalf("unsupported album error = %v", err)
	}
	tracks := p.convertSongs([]model.Song{{ID: "1", Name: "Bad", IsInvalid: true}, {ID: "2", Name: "Good", Duration: -1}})
	if len(tracks) != 1 || tracks[0].ID != "2" || tracks[0].Duration != 0 {
		t.Fatalf("invalid track data not filtered: %#v", tracks)
	}
}
