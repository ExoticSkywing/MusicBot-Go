package kuwo

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/liuran001/MusicBot-Go/bot/platform"
)

// paidTrackDetail is a track kuwo marks as requiring payment.
const paidTrackDetail = `{"data":{"rid":41378936,"duration":213,"isListenFee":true}}`

func TestPaidCatalogMP3RequiresFullVerifiedAudio(t *testing.T) {
	for _, quality := range []platform.Quality{platform.QualityStandard, platform.QualityHigh} {
		for _, source := range []struct {
			name      string
			mediaType int
			duration  int
			wantErr   error
		}{
			{"full", 0, 213, nil},
			{"preview", 1, 213, errPreviewMedia},
			{"short", 0, 30, errTrackDurationMismatch},
		} {
			t.Run(quality.String()+"/"+source.name, func(t *testing.T) {
				candidate := mobileQualityCandidates(quality)[0]
				var mobileCalls, probes int
				transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
					switch req.URL.Host {
					case "www.kuwo.cn":
						if req.URL.Path == "/" {
							return response(http.StatusOK, map[string]string{"Set-Cookie": kuwoSessionCookie + "=abcdefghijklmnop; Path=/"}, nil), nil
						}
						if strings.Contains(req.URL.Path, "playUrl") {
							t.Fatal("paid catalog must not use unchecked web fallback")
						}
						return response(http.StatusOK, nil, []byte(`{"data":{"rid":41378936,"duration":213,"isListenFee":true,"payInfo":{"listen_fragment":"1","cannotOnlinePlay":0}}}`)), nil
					case "mobi.kuwo.cn":
						mobileCalls++
						if req.URL.Query().Get("br") != candidate.br {
							t.Fatal("unexpected requested tier")
						}
						return response(http.StatusOK, nil, []byte(fmt.Sprintf(`{"code":200,"data":{"rid":41378936,"url":"https://kw-er.kuwo.cn/a.mp3","format":"mp3","bitrate":%d,"duration":%d,"type":%d}}`, candidate.bitrate, source.duration, source.mediaType))), nil
					case "kw-er.kuwo.cn":
						probes++
						return mp3ProbeTransport(t, int64(candidate.bitrate)*1000*213/8, nil).Transport.RoundTrip(req)
					default:
						t.Fatalf("unexpected host %s", req.URL.Host)
						return nil, nil
					}
				})
				client := NewClient(time.Second, nil)
				client.apiHTTPClient.Transport = transport
				client.mediaHTTPClient.Transport = transport
				info, err := client.GetDownloadInfo(context.Background(), "41378936", quality)
				if mobileCalls != 1 {
					t.Fatalf("mobile calls=%d, want 1", mobileCalls)
				}
				if source.wantErr != nil {
					if !errors.Is(err, source.wantErr) || info != nil || probes != 0 {
						t.Fatalf("unverified source: err=%v info=%v probes=%d", err, info, probes)
					}
				} else if err != nil || info.Quality != quality || info.Bitrate != candidate.bitrate || probes == 0 {
					t.Fatalf("full source: err=%v info=%v probes=%d", err, info, probes)
				}
			})
		}
	}
}

// Catalog flags allow a verified mobile rendition, never an unchecked web MP3.
func TestPaidTrackRejectsUnverifiedAudio(t *testing.T) {
	for _, tt := range []struct {
		name         string
		legacyStatus int
		legacyBody   string
	}{
		{"legacy endpoint refuses", http.StatusBadGateway, ""},
		{"legacy endpoint answers empty", http.StatusOK, ""},
		{"legacy endpoint offers a preview", http.StatusOK,
			"url=https://kw-er.kuwo.cn/a.flac\nformat=flac\nbitrate=1\nrid=41378936\nduration=213\ntype=0\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var mobileCalls, webCalls int
			transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.Path == "/" {
					return response(http.StatusOK, map[string]string{
						"Set-Cookie": kuwoSessionCookie + "=abcdefghijklmnop; Path=/",
					}, nil), nil
				}
				switch {
				case req.URL.Host == "mobi.kuwo.cn":
					mobileCalls++
					return response(tt.legacyStatus, nil, []byte(tt.legacyBody)), nil
				case req.URL.Host == "www.kuwo.cn" && strings.Contains(req.URL.Path, "playUrl"):
					webCalls++
					return response(http.StatusOK, nil, nil), nil
				default:
					return response(http.StatusOK, nil, []byte(paidTrackDetail)), nil
				}
			})

			client := newClientWithEndpoints(time.Second, nil, kuwoEndpoints{
				home:   kuwoHomeURL,
				detail: kuwoDetailURL,
			})
			client.apiHTTPClient.Transport = transport
			client.mediaHTTPClient.Transport = transport

			for _, quality := range []platform.Quality{
				platform.QualityHiRes,
				platform.QualityLossless,
				platform.QualityHigh,
				platform.QualityStandard,
			} {
				mobileCalls, webCalls = 0, 0
				info, err := client.GetDownloadInfo(context.Background(), "41378936", quality)
				if err == nil {
					t.Fatalf("quality %v: GetDownloadInfo() returned %q for a paid track", quality, info.Format)
				}
				if webCalls != 0 {
					t.Errorf("quality %v: fell through to the web MP3 endpoint", quality)
				}
				if mobileCalls == 0 {
					t.Errorf("quality %v: catalog flag prevented checking the actual audio", quality)
				}
			}
		})
	}
}

// Catalog fee labels also permit a fully verified FLAC rendition.
func TestPaidTrackStillServesAVerifiedFLAC(t *testing.T) {
	const rawSize = 1 << 20
	cleartext := makeTestFLAC(t, rawSize-len(knownDirectFLACTrailer), 96000, 24, 2, 213*time.Second)
	stream := append(append([]byte(nil), cleartext...), knownDirectFLACTrailer...)

	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch {
		case req.URL.Path == "/":
			return response(http.StatusOK, map[string]string{
				"Set-Cookie": kuwoSessionCookie + "=abcdefghijklmnop; Path=/",
			}, nil), nil
		case req.URL.Host == "mobi.kuwo.cn":
			return response(http.StatusOK, nil, []byte(
				"url=https://kw-er.kuwo.cn/a.flac\nformat=flac\nbitrate=4000\n"+
					"rid=41378936\nduration=213\ntype=0\n",
			)), nil
		case req.URL.Host == "kw-er.kuwo.cn":
			if req.Header.Get("Range") == "bytes=0-41" {
				return response(
					http.StatusPartialContent,
					map[string]string{"Content-Range": "bytes 0-41/1048576"},
					stream[:42],
				), nil
			}
			return directFLACTestTailResponse(t, req, stream), nil
		default:
			return response(http.StatusOK, nil, []byte(paidTrackDetail)), nil
		}
	})

	client := newClientWithEndpoints(time.Second, nil, kuwoEndpoints{
		home:   kuwoHomeURL,
		detail: kuwoDetailURL,
	})
	client.apiHTTPClient.Transport = transport
	client.mediaHTTPClient.Transport = transport
	client.downloadHTTPClient = &http.Client{Transport: transport}

	info, err := client.GetDownloadInfo(context.Background(), "41378936", platform.QualityHiRes)
	if err != nil {
		t.Fatalf("GetDownloadInfo() = %v, want a verified FLAC for a paid track", err)
	}
	if info.Quality != platform.QualityHiRes || info.Format != "flac" {
		t.Fatalf("got q=%v format=%q, want hires/flac", info.Quality, info.Format)
	}
}
