package migu

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

func TestConvertNeverMarksVIP(t *testing.T) {
	item := MiguSongItem{
		ContentID: "123", SongName: "Song",
		RateFormats: []miguRateFormat{{FormatType: "PQ", ResourceType: "2", Size: "3200000", FileType: "mp3"}},
	}
	song := (&Migu{ctx: context.Background()}).convertItemToSong(item)
	if song == nil || song.IsVIP || song.Extra["preview_only"] != "" {
		t.Fatalf("song carries VIP/preview markers: %#v", song)
	}
}

func listenURLResponse(req *http.Request, mediaURL, formatType string) *http.Response {
	body := `{"code":"000000","info":"操作成功","data":{"url":"` + mediaURL + `","formatType":"` + formatType + `"}}`
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: req}
}

func audioProbeResponse(req *http.Request) *http.Response {
	return &http.Response{StatusCode: http.StatusPartialContent, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("\xff\xfbprobe")), Request: req}
}

func TestDownloadSelectsHighestProbedTone(t *testing.T) {
	const pqURL = "https://cdn.example/x/标清高清/MP3_128_16_Stero/song.mp3?Key=k"
	cases := []struct {
		name        string
		probePath   string
		wantPath    string
		wantBitrate int
	}{
		{"HQ on CDN", "/MP3_320_16_Stero/", "/MP3_320_16_Stero/", 320},
		{"128 only", "/MP3_128_16_Stero/", "/MP3_128_16_Stero/", 128},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				switch {
				case strings.HasSuffix(req.URL.Path, "/listen-url"):
					return listenURLResponse(req, pqURL, "PQ"), nil
				case strings.Contains(req.URL.Path, tc.probePath):
					if req.Header.Get("Range") != "bytes=0-63" {
						t.Errorf("probe requested more than the magic prefix: %s", req.Header.Get("Range"))
					}
					return audioProbeResponse(req), nil
				default:
					resp := audioProbeResponse(req)
					resp.StatusCode = http.StatusNotFound
					return resp, nil
				}
			})}
			song := &model.Song{Source: "migu", ID: "123|2|PQ", Ext: "mp3", Extra: map[string]string{"content_id": "123", "resource_type": "2", "format_type": "PQ"}}
			got, err := (&Migu{ctx: context.Background(), client: client}).GetDownloadURL(song)
			if err != nil || !strings.Contains(got, tc.wantPath) || song.Ext != "mp3" || song.Bitrate != tc.wantBitrate {
				t.Fatalf("url=%s ext=%s bitrate=%d err=%v", got, song.Ext, song.Bitrate, err)
			}
		})
	}
}

func TestDownloadUpgradesToLosslessWhenProbed(t *testing.T) {
	const pqURL = "https://cdn.example/x/标清高清/MP3_128_16_Stero/song.mp3?Key=k"
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch {
		case strings.HasSuffix(req.URL.Path, "/listen-url"):
			return listenURLResponse(req, pqURL, "PQ"), nil
		case strings.Contains(req.URL.Path, "歌曲下载/flac"):
			resp := audioProbeResponse(req)
			resp.Body = io.NopCloser(strings.NewReader("fLaCprobe"))
			return resp, nil
		default:
			resp := audioProbeResponse(req)
			resp.StatusCode = http.StatusNotFound
			return resp, nil
		}
	})}
	song := &model.Song{Source: "migu", ID: "123|2|PQ", Ext: "mp3", Extra: map[string]string{"content_id": "123", "resource_type": "2", "format_type": "PQ"}}
	got, err := (&Migu{ctx: context.Background(), client: client}).GetDownloadURL(song)
	if err != nil || !strings.Contains(got, "flac") || song.Ext != "flac" || song.Bitrate != 0 {
		t.Fatalf("url=%s ext=%s bitrate=%d err=%v", got, song.Ext, song.Bitrate, err)
	}
}

func TestDownloadRejectsAuditionMedia(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return listenURLResponse(req, "https://cdn.example/audition/song.mp3", "PQ"), nil
	})}
	song := &model.Song{Source: "migu", ID: "123|2|PQ", Extra: map[string]string{"content_id": "123", "resource_type": "2", "format_type": "PQ"}}
	if _, err := (&Migu{ctx: context.Background(), client: client}).GetDownloadURL(song); !errors.Is(err, model.ErrPreviewOnly) {
		t.Fatalf("audition error = %v, want ErrPreviewOnly", err)
	}
}

func TestDownloadReportsDialogMessage(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := `{"code":"000000","info":"操作成功","data":{"url":"","dialogInfo":{"text":"会员专属歌曲，请先登录"}}}`
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
	})}
	song := &model.Song{Source: "migu", ID: "123|2|PQ", Extra: map[string]string{"content_id": "123", "resource_type": "2", "format_type": "PQ"}}
	_, err := (&Migu{ctx: context.Background(), client: client}).GetDownloadURL(song)
	if err == nil || !strings.Contains(err.Error(), "会员专属歌曲") {
		t.Fatalf("dialog hint not surfaced: %v", err)
	}
}
