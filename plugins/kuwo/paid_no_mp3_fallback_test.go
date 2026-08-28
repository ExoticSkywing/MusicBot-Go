package kuwo

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/liuran001/MusicBot-Go/bot/platform"
)

// paidTrackDetail is a track kuwo marks as requiring payment.
const paidTrackDetail = `{"data":{"rid":41378936,"duration":213,"isListenFee":true}}`

// TestPaidTrackNeverFallsThroughToMP3 preserves the guarantee that the deleted
// external-resolver tests used to carry: a track kuwo marks as paid must never
// be served as an ordinary MP3, whatever happens to the lossless resolvers. A
// preview or a downgraded stream handed over as if it were the track is worse
// than an honest failure.
func TestPaidTrackNeverFallsThroughToMP3(t *testing.T) {
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
				// The lossless tiers may probe the legacy endpoint, since a paid
				// track can still have a public FLAC. The MP3 tiers must not.
				if quality == platform.QualityHigh || quality == platform.QualityStandard {
					if mobileCalls != 0 {
						t.Errorf("quality %v: probed the mobile MP3 endpoint %d times", quality, mobileCalls)
					}
				}
			}
		})
	}
}

// TestPaidTrackStillServesAVerifiedFLAC is the other side of that guarantee:
// refusing MP3 must not mean refusing everything. When kuwo does hand over a
// fully verified FLAC, a paid track is served -- which is how Jay Chou's
// 告白气球 became downloadable at 24-bit/96kHz.
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
