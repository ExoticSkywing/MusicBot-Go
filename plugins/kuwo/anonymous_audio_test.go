package kuwo

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/liuran001/MusicBot-Go/bot/platform"
)

func TestAnonymousFLACTiersAndFallbacks(t *testing.T) {
	type rendition struct {
		bitrate int
		rate    int
		depth   int
		format  string
	}
	lossless := rendition{2000, 48000, 24, "flac"}
	hiRes := rendition{4000, 96000, 24, "flac"}
	for _, tt := range []struct {
		name      string
		requested platform.Quality
		responses map[string]rendition
		want      platform.Quality
		calls     []string
	}{
		{
			name:      "lossless selects 2000 and keeps 24-bit 48k lossless",
			requested: platform.QualityLossless,
			responses: map[string]rendition{"2000kflac": lossless, "4000kflac": hiRes},
			want:      platform.QualityLossless, calls: []string{"2000kflac"},
		},
		{
			name:      "hires selects independent 4000 rendition",
			requested: platform.QualityHiRes,
			responses: map[string]rendition{"2000kflac": lossless, "4000kflac": hiRes},
			want:      platform.QualityHiRes, calls: []string{"4000kflac"},
		},
		{
			name:      "hires unavailable falls back to ordinary lossless",
			requested: platform.QualityHiRes,
			responses: map[string]rendition{"2000kflac": lossless},
			want:      platform.QualityLossless, calls: []string{"4000kflac", "2000kflac"},
		},
		{
			name:      "server downgrades 4000 to 2000",
			requested: platform.QualityHiRes,
			responses: map[string]rendition{"4000kflac": lossless, "2000kflac": lossless},
			want:      platform.QualityLossless, calls: []string{"4000kflac", "2000kflac"},
		},
		{
			name:      "server upgrades 2000 to 4000",
			requested: platform.QualityLossless,
			responses: map[string]rendition{"2000kflac": hiRes},
			want:      platform.QualityHiRes, calls: []string{"2000kflac"},
		},
		{
			name:      "4000 cannot label a 16-bit file hires",
			requested: platform.QualityHiRes,
			responses: map[string]rendition{"4000kflac": {4000, 44100, 16, "flac"}, "2000kflac": lossless},
			want:      platform.QualityLossless, calls: []string{"4000kflac", "2000kflac"},
		},
		{
			name:      "4000 returned MP3 falls back to FLAC",
			requested: platform.QualityHiRes,
			responses: map[string]rendition{"4000kflac": {320, 44100, 16, "mp3"}, "2000kflac": lossless},
			want:      platform.QualityLossless, calls: []string{"4000kflac", "2000kflac"},
		},
		{
			name:      "4000 returned mflac falls back to FLAC",
			requested: platform.QualityHiRes,
			responses: map[string]rendition{"4000kflac": {4000, 96000, 24, "mflac"}, "2000kflac": lossless},
			want:      platform.QualityLossless, calls: []string{"4000kflac", "2000kflac"},
		},
		{
			name:      "legacy remains a final FLAC fallback",
			requested: platform.QualityHiRes,
			responses: map[string]rendition{"legacy": hiRes},
			want:      platform.QualityHiRes, calls: []string{"4000kflac", "2000kflac", "legacy"},
		},
		{
			name:      "verified downgrade survives unavailable later endpoints",
			requested: platform.QualityHiRes,
			responses: map[string]rendition{"4000kflac": lossless},
			want:      platform.QualityLossless, calls: []string{"4000kflac", "2000kflac", "legacy"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			const clearSize = 128 << 10
			streams := make(map[string][]byte)
			for name, item := range tt.responses {
				if item.format == "flac" {
					cleartext := makeTestFLAC(t, clearSize, item.rate, item.depth, 2, 213*time.Second)
					streams["/"+name+".flac"] = append(cleartext, knownDirectFLACTrailer...)
				}
			}
			var calls []string
			transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
				switch req.URL.Host {
				case "www.kuwo.cn":
					if req.URL.Path == "/" {
						return response(http.StatusOK, map[string]string{"Set-Cookie": kuwoSessionCookie + "=abcdefghijklmnop; Path=/"}, nil), nil
					}
					return response(http.StatusOK, nil, []byte(paidTrackDetail)), nil
				case "mobi.kuwo.cn":
					if req.Header.Get("Cookie") != "" {
						t.Fatal("anonymous media API leaked cookie")
					}
					name := req.URL.Query().Get("br")
					if req.URL.Query().Get("f") == "kuwo" {
						name = "legacy"
					} else if !strings.HasPrefix(req.URL.Query().Get("user"), "C_APK_guanwang_") {
						t.Fatal("wrong anonymous identity")
					}
					calls = append(calls, name)
					item, ok := tt.responses[name]
					if !ok {
						return response(http.StatusBadGateway, nil, nil), nil
					}
					mediaURL := fmt.Sprintf("https://kw-er.kuwo.cn/%s.%s?bitrate$%d&format$%s&source$&type$convert_url_with_sign&user$C_APK_guanwang_123abc&loginUid$", name, item.format, item.bitrate, item.format)
					if name == "legacy" {
						return response(http.StatusOK, nil, []byte(fmt.Sprintf("url=%s\nformat=%s\nbitrate=%d\nrid=41378936\nduration=213\ntype=0\n", mediaURL, item.format, item.bitrate))), nil
					}
					return response(http.StatusOK, nil, []byte(fmt.Sprintf(`{"code":200,"data":{"rid":"41378936","url":%q,"format":%q,"bitrate":%d,"duration":213,"type":0}}`, mediaURL, item.format, item.bitrate))), nil
				case "kw-er.kuwo.cn":
					stream, ok := streams[req.URL.Path]
					if !ok {
						t.Fatalf("unexpected media probe %s", req.URL.Path)
					}
					if req.Header.Get("Range") == "bytes=0-41" {
						return response(http.StatusPartialContent, map[string]string{"Content-Range": fmt.Sprintf("bytes 0-41/%d", len(stream))}, stream[:42]), nil
					}
					return directFLACTestTailResponse(t, req, stream), nil
				default:
					t.Fatalf("unexpected host %s", req.URL.Host)
					return nil, nil
				}
			})
			client := NewClient(time.Second, nil)
			client.apiHTTPClient.Transport = transport
			client.mediaHTTPClient.Transport = transport
			client.downloadHTTPClient = &http.Client{Transport: transport}
			info, err := client.GetDownloadInfo(context.Background(), "41378936", tt.requested)
			if err != nil {
				t.Fatal(err)
			}
			if info.Quality != tt.want || info.Format != "flac" || info.Size != clearSize || info.Downloader == nil {
				t.Fatalf("quality=%v want=%v size=%d downloader=%t", info.Quality, tt.want, info.Size, info.Downloader != nil)
			}
			if !slices.Equal(calls, tt.calls) {
				t.Fatalf("requests=%v want=%v", calls, tt.calls)
			}
		})
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
