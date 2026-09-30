package kuwo

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/liuran001/MusicBot-Go/bot/platform"
)

func TestAnonymousFLACFallbackPreservesActualHiResAndTrailerPolicy(t *testing.T) {
	for _, rate := range []int{44100, 96000} {
		bitrate := 2000
		if rate == 96000 {
			bitrate = 4000
		}
		const rawSize = 1 << 20
		cleartext := makeTestFLAC(t, rawSize-len(knownDirectFLACTrailer), rate, 24, 2, 213*time.Second)
		stream := append(append([]byte(nil), cleartext...), knownDirectFLACTrailer...)
		legacy, anonymous := 0, 0
		transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
			switch {
			case req.URL.Path == "/":
				return response(http.StatusOK, map[string]string{"Set-Cookie": kuwoSessionCookie + "=abcdefghijklmnop; Path=/"}, nil), nil
			case req.URL.Host == "mobi.kuwo.cn":
				if req.Header.Get("Cookie") != "" {
					t.Fatal("anonymous media API leaked cookie")
				}
				if req.URL.Query().Get("f") == "kuwo" {
					legacy++
					return response(http.StatusBadGateway, nil, nil), nil
				}
				anonymous++
				if req.URL.Query().Get("br") != "2000kflac" || !strings.HasPrefix(req.URL.Query().Get("user"), "C_APK_guanwang_") {
					t.Fatal("wrong anonymous FLAC request")
				}
				return response(http.StatusOK, nil, []byte(fmt.Sprintf(`{"code":200,"data":{"rid":"41378936","url":"https://kw-er.kuwo.cn/a.flac?bitrate$%d&format$flac&source$&type$convert_url_with_sign&user$C_APK_guanwang_123abc&loginUid$","format":"flac","bitrate":%d,"duration":213,"type":0}}`, bitrate, bitrate))), nil
			case req.URL.Host == "kw-er.kuwo.cn":
				if req.Header.Get("Range") == "bytes=0-41" {
					return response(http.StatusPartialContent, map[string]string{"Content-Range": fmt.Sprintf("bytes 0-41/%d", rawSize)}, stream[:42]), nil
				}
				return directFLACTestTailResponse(t, req, stream), nil
			default:
				return response(http.StatusOK, nil, []byte(paidTrackDetail)), nil
			}
		})
		client := NewClient(time.Second, nil)
		client.apiHTTPClient.Transport = transport
		client.mediaHTTPClient.Transport = transport
		client.downloadHTTPClient = &http.Client{Transport: transport}
		info, err := client.GetDownloadInfo(context.Background(), "41378936", platform.QualityHiRes)
		if err != nil {
			t.Fatal(err)
		}
		if info.Quality != platform.QualityHiRes || info.Format != "flac" || info.Size != int64(len(cleartext)) || info.Downloader == nil || anonymous != 1 || legacy != 1 {
			t.Fatalf("quality=%v size=%d downloader=%t legacy=%d anonymous=%d", info.Quality, info.Size, info.Downloader != nil, legacy, anonymous)
		}
	}

}

func TestAnonymousMediaPseudoQuery(t *testing.T) {
	base := "https://kw-er.kuwo.cn/a.flac?bitrate$2000&format$flac&source$&type$convert_url_with_sign&user$C_APK_guanwang_123abc&loginUid$"
	if err := validateMediaURL(base, "flac"); err != nil {
		t.Fatal(err)
	}
	for _, replacement := range []string{"C_APK_guanwang_", "C_APK_guanwang_abc%2Fdef", "C_APK_guanwang_abc$def", "C_APK_guanwang_" + strings.Repeat("a", 65), "C_APK_guanwangX_123"} {
		if err := validateMediaURL(strings.Replace(base, "C_APK_guanwang_123abc", replacement, 1), "flac"); err == nil {
			t.Fatalf("accepted invalid anonymous user %q", replacement)
		}
	}
	for _, raw := range []string{base + "&user$123", strings.Replace(base, "source$&", "source$bad%2Fsource&", 1), strings.Replace(base, "type$convert_url_with_sign", "type$", 1)} {
		if err := validateMediaURL(raw, "flac"); err == nil {
			t.Fatal("accepted malformed pseudo-query")
		}
	}
}
