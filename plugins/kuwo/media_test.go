package kuwo

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/liuran001/MusicBot-Go/bot/platform"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func response(status int, headers map[string]string, body []byte) *http.Response {
	h := make(http.Header, len(headers))
	for key, value := range headers {
		h.Set(key, value)
	}
	return &http.Response{
		StatusCode: status,
		Header:     h,
		Body:       io.NopCloser(bytes.NewReader(body)),
	}
}

func candidateNames(items []mobileQuality) []string {
	names := make([]string, len(items))
	for i, item := range items {
		names[i] = item.br
	}
	return names
}

func TestMobileQualityCandidates(t *testing.T) {
	tests := []struct {
		quality platform.Quality
		want    []string
	}{
		{platform.QualityStandard, []string{"128kmp3"}},
		{platform.QualityHigh, []string{"320kmp3", "128kmp3"}},
		{platform.QualityLossless, []string{"320kmp3", "128kmp3"}},
		{platform.QualityHiRes, []string{"320kmp3", "128kmp3"}},
	}
	for _, tt := range tests {
		got := mobileQualityCandidates(tt.quality)
		if gotNames := candidateNames(got); !slices.Equal(tt.want, gotNames) {
			t.Fatalf("quality %v = %v, want %v", tt.quality, gotNames, tt.want)
		}
	}
}

func TestLosslessResolverPlanUsesSeparateDirectFLACStreams(t *testing.T) {
	cases := []struct {
		name    string
		quality platform.Quality
		want    []losslessResolver
	}{
		{
			name:    "lossless uses the direct 2000 FLAC stream",
			quality: platform.QualityLossless,
			want:    []losslessResolver{resolvePlayableFLAC},
		},
		{
			name:    "hires uses direct 4000 then direct 2000 fallback",
			quality: platform.QualityHiRes,
			want:    []losslessResolver{resolvePlayableHiRes, resolvePlayableFLAC},
		},
		{
			name:    "high has no lossless resolver",
			quality: platform.QualityHigh,
			want:    nil,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := losslessResolverPlan(testCase.quality)
			if !slices.Equal(got, testCase.want) {
				t.Fatalf("plan = %v, want %v", got, testCase.want)
			}
		})
	}
}

func TestMobileResolverUsesAPIClientAndDownloadProbeClient(t *testing.T) {
	var apiCalls atomic.Int32
	var probeCalls atomic.Int32
	apiTransport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != "mobi.kuwo.cn" {
			t.Fatalf("API client requested unexpected host %q", req.URL.Host)
		}
		apiCalls.Add(1)
		return response(http.StatusOK, nil, []byte(
			`{"code":200,"data":{"rid":41378936,"url":"https://er-sycdn.kuwo.cn/signed.mp3","format":"mp3","bitrate":320,"duration":213,"type":"0"}}`,
		)), nil
	})
	probeTransport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != "er-sycdn.kuwo.cn" {
			t.Fatalf("download probe client requested unexpected host %q", req.URL.Host)
		}
		probeCalls.Add(1)
		return mp3ProbeTransport(t, 8525534, nil).Transport.RoundTrip(req)
	})
	client := NewClient(time.Second, nil)
	client.apiHTTPClient.Transport = apiTransport
	client.mediaHTTPClient.Transport = probeTransport

	info, err := client.resolveMobileDownload(
		context.Background(),
		&trackDetail{Track: platform.Track{ID: "41378936", Duration: 213 * time.Second}},
		mobileQuality{br: "320kmp3", format: "mp3", bitrate: 320, quality: platform.QualityHigh},
	)
	if err != nil {
		t.Fatalf("resolve mobile: %v", err)
	}
	if info == nil || info.Quality != platform.QualityHigh {
		t.Fatalf("info = %#v", info)
	}
	if apiCalls.Load() != 1 || probeCalls.Load() != 2 {
		t.Fatalf("api calls=%d probe calls=%d", apiCalls.Load(), probeCalls.Load())
	}
}

func TestValidateMediaURL(t *testing.T) {
	valid := []struct {
		raw    string
		format string
	}{
		{"https://kw-er.kuwo.cn/path/signed.flac", "flac"},
		{
			"https://kw-er.kuwo.cn/path/signed.flac?" +
				"bitrate$2000&format$flac&source$kwplayer_ar_5.1.0&type$convert_url_with_sign&" +
				"user$359307055300426&loginUid$",
			"flac",
		},
		{"https://er-sycdn.kuwo.cn/path/signed.mp3", "mp3"},
	}
	for _, tt := range valid {
		if err := validateMediaURL(tt.raw, tt.format); err != nil {
			t.Errorf("validateMediaURL(%q, %q) = %v", tt.raw, tt.format, err)
		}
	}
	invalid := []string{
		"http://kw-er.kuwo.cn/a.flac",
		"https://user@kw-er.kuwo.cn/a.flac",
		"https://kw-er.kuwo.cn:443/a.flac",
		"https://kw-er.kuwo.cn/a.flac?",
		"https://kw-er.kuwo.cn/a.flac?token=secret",
		"https://kw-er.kuwo.cn/a.flac#secret",
		"https://kw-er.kuwo.cn.evil.test/a.flac",
		"https://foo.bar.kuwo.cn/a.flac",
		"https://er-sycdn.kuwo.cn/a.mp3.exe",
	}
	for _, raw := range invalid {
		if err := validateMediaURL(raw, "flac"); err == nil {
			t.Errorf("validateMediaURL(%q) succeeded", raw)
		}
	}
}

func TestValidateMediaURLAcceptsOnlyExactMobilePseudoQuery(t *testing.T) {
	const base = "https://kw-er.kuwo.cn/path/signed.flac?"
	const valid = "bitrate$2000&format$flac&source$kwplayer_ar_5.1.0&type$convert_url_with_sign&" +
		"user$359307055300426&loginUid$"

	if err := validateMediaURL(base+valid, "flac"); err != nil {
		t.Fatalf("validateMediaURL(valid mobile query) = %v", err)
	}
	for _, query := range []string{
		"bitrate$2000&format$flac&source$kwplayer&type$convert_url_with_sign&user$1",
		valid + "&token$secret",
		"bitrate$2000&bitrate$2000&format$flac&source$kwplayer&type$convert_url_with_sign&user$1&loginUid$",
		"bitrate$320&format$flac&source$kwplayer&type$convert_url_with_sign&user$1&loginUid$",
		"bitrate$2000&format$mp3&source$kwplayer&type$convert_url_with_sign&user$1&loginUid$",
		"bitrate$2000&format$flac&source$kwplayer%2Fbad&type$convert_url_with_sign&user$1&loginUid$",
		"bitrate$2000&format$flac&source$kwplayer&type$convert_url_with_sign&user$not-a-number&loginUid$",
	} {
		if err := validateMediaURL(base+query, "flac"); !errors.Is(err, errUnsafeMediaURL) {
			t.Errorf("validateMediaURL(query shape) = %v, want errUnsafeMediaURL", err)
		}
	}
}

func TestMediaURLRejectsTrailingEmptyFragmentWithoutMisclassifyingEncodedPath(t *testing.T) {
	for _, raw := range []string{
		"https://kw-er.kuwo.cn/signed.flac#",
		"https://er-sycdn.kuwo.cn/signed.mp3#",
	} {
		t.Run(raw, func(t *testing.T) {
			if _, err := normalizeSafeMediaURL(raw); !errors.Is(err, errUnsafeMediaURL) {
				t.Fatalf("normalizeSafeMediaURL(%q) error = %v, want errUnsafeMediaURL", raw, err)
			}
			format := strings.TrimSuffix(path.Ext(strings.TrimSuffix(raw, "#")), "#")
			format = strings.TrimPrefix(format, ".")
			if err := validateMediaURL(raw, format); !errors.Is(err, errUnsafeMediaURL) {
				t.Fatalf("validateMediaURL(%q) error = %v, want errUnsafeMediaURL", raw, err)
			}
		})
	}

	const encoded = "https://kw-er.kuwo.cn/path/signed%23.flac"
	if normalized, err := normalizeSafeMediaURL(encoded); err != nil || normalized != encoded {
		t.Fatalf("normalizeSafeMediaURL(%q) = (%q, %v)", encoded, normalized, err)
	}
	if err := validateMediaURL(encoded, "flac"); err != nil {
		t.Fatalf("validateMediaURL(%q) = %v", encoded, err)
	}
	if err := validateMediaURL("https://kw-er.kuwo.cn/path/signed.flac%23", "flac"); err == nil || errors.Is(err, errUnsafeMediaURL) {
		t.Fatalf("encoded suffix mismatch error = %v, want ordinary suffix error", err)
	}
}

func TestNormalizeSafeMediaURLCanonicalizesBareQueryDelimiter(t *testing.T) {
	const raw = "http://kw-er.kuwo.cn/path/signed.flac?"
	const want = "https://kw-er.kuwo.cn/path/signed.flac"

	normalized, err := normalizeSafeMediaURL(raw)
	if err != nil || normalized != want {
		t.Fatalf("normalizeSafeMediaURL(%q) = (%q, %v), want %q", raw, normalized, err, want)
	}
	if err := validateMediaURL(normalized, "flac"); err != nil {
		t.Fatalf("validateMediaURL(canonicalized URL) = %v", err)
	}
	if err := validateMediaURL(raw, "flac"); !errors.Is(err, errUnsafeMediaURL) {
		t.Fatalf("validateMediaURL(raw bare query) = %v, want errUnsafeMediaURL", err)
	}
	if _, err := normalizeSafeMediaURL(raw + "token=secret"); !errors.Is(err, errUnsafeMediaURL) {
		t.Fatalf("normalizeSafeMediaURL(non-empty query) = %v, want errUnsafeMediaURL", err)
	}
}

func validFLACStreamInfo(duration time.Duration) []byte {
	data := make([]byte, 42)
	copy(data, "fLaC")
	data[4] = 0x80
	data[7] = 34
	binary.BigEndian.PutUint16(data[8:10], 4096)
	binary.BigEndian.PutUint16(data[10:12], 4096)
	sampleRate := uint64(44100)
	totalSamples := uint64(duration.Seconds()) * sampleRate
	packed := sampleRate<<44 | 1<<41 | 15<<36 | totalSamples
	binary.BigEndian.PutUint64(data[18:26], packed)
	return data
}

func TestProbeMediaFLACUsesVerifiedStreamInfo(t *testing.T) {
	const total = int64(27383481)
	body := validFLACStreamInfo(213 * time.Second)
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if got := req.Header.Get("Range"); got != "bytes=0-41" {
			t.Fatalf("Range = %q", got)
		}
		if req.Header.Get("User-Agent") != mediaUserAgent || req.Referer() != mediaReferer {
			t.Fatalf("media headers = %#v", req.Header)
		}
		return response(http.StatusPartialContent, map[string]string{
			"Content-Range": "bytes 0-41/27383481",
			"Content-Type":  "audio/x-flac",
		}, body), nil
	})}

	got, err := probeMedia(context.Background(), client, "https://kw-er.kuwo.cn/signed.flac", "flac", 213*time.Second)
	if err != nil {
		t.Fatalf("probeMedia() = %v", err)
	}
	if got.format != "flac" || got.size != total || got.quality != platform.QualityLossless || got.bitrate != 1028 || got.duration != 213*time.Second {
		t.Fatalf("probe = %#v", got)
	}
}

func mp3ProbeTransport(t *testing.T, total int64, mutate func(*http.Request, []byte) []byte) *http.Client {
	t.Helper()
	return &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		var body []byte
		var contentRange string
		switch req.Header.Get("Range") {
		case "bytes=0-15":
			body = []byte{'I', 'D', '3', 4, 0, 0, 0, 0, 0, 6, 1, 2, 3, 4, 5, 6}
			contentRange = "bytes 0-15/" + formatInt64(total)
		case "bytes=16-31":
			body = append([]byte{0xff, 0xfb, 0x90, 0}, make([]byte, 12)...)
			contentRange = "bytes 16-31/" + formatInt64(total)
		default:
			t.Fatalf("unexpected Range %q", req.Header.Get("Range"))
		}
		if mutate != nil {
			body = mutate(req, body)
		}
		return response(http.StatusPartialContent, map[string]string{"Content-Range": contentRange}, body), nil
	})}
}

func TestProbeMediaMP3ParsesID3AndUsesAverageBitrate(t *testing.T) {
	for _, tt := range []struct {
		name    string
		total   int64
		want    int
		quality platform.Quality
	}{
		{"320", 8525534, 320, platform.QualityHigh},
		{"128", 3410341, 128, platform.QualityStandard},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := probeMedia(context.Background(), mp3ProbeTransport(t, tt.total, nil), "https://er-sycdn.kuwo.cn/signed.mp3", "mp3", 213*time.Second)
			if err != nil {
				t.Fatalf("probeMedia() = %v", err)
			}
			if got.bitrate != tt.want || got.quality != tt.quality || got.size != tt.total {
				t.Fatalf("probe = %#v", got)
			}
		})
	}
}

func TestProbeMediaRejectsMalformedAndMismatchedMedia(t *testing.T) {
	t.Run("truncated flac streaminfo", func(t *testing.T) {
		client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return response(http.StatusPartialContent, map[string]string{"Content-Range": "bytes 0-41/1000"}, []byte("fLaC")), nil
		})}
		if _, err := probeMedia(context.Background(), client, "https://kw-er.kuwo.cn/a.flac", "flac", 213*time.Second); err == nil {
			t.Fatal("probeMedia() succeeded")
		}
	})
	t.Run("streaminfo duration mismatch", func(t *testing.T) {
		client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return response(http.StatusPartialContent, map[string]string{"Content-Range": "bytes 0-41/27383481"}, validFLACStreamInfo(11*time.Second)), nil
		})}
		if _, err := probeMedia(context.Background(), client, "https://kw-er.kuwo.cn/a.flac", "flac", 269*time.Second); !errors.Is(err, errPreviewMedia) {
			t.Fatalf("error = %v, want preview media", err)
		}
	})
	t.Run("only ID3", func(t *testing.T) {
		client := mp3ProbeTransport(t, 3410341, func(req *http.Request, body []byte) []byte {
			if req.Header.Get("Range") == "bytes=16-31" {
				return append([]byte("ID3"), make([]byte, 13)...)
			}
			return body
		})
		if _, err := probeMedia(context.Background(), client, "https://er-sycdn.kuwo.cn/a.mp3", "mp3", 213*time.Second); err == nil {
			t.Fatal("probeMedia() succeeded")
		}
	})
	t.Run("body too long", func(t *testing.T) {
		client := mp3ProbeTransport(t, 3410341, func(req *http.Request, body []byte) []byte {
			return append(body, 0)
		})
		if _, err := probeMedia(context.Background(), client, "https://er-sycdn.kuwo.cn/a.mp3", "mp3", 213*time.Second); err == nil {
			t.Fatal("probeMedia() succeeded")
		}
	})
}

func TestProbeMediaRedirectRevalidatesAndReattachesHeaders(t *testing.T) {
	var mu sync.Mutex
	requests := make([]string, 0, 2)
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		mu.Lock()
		requests = append(requests, req.URL.String())
		mu.Unlock()
		if strings.Contains(req.URL.Path, "first") {
			return response(http.StatusFound, map[string]string{"Location": "https://kw-cdn.kuwo.cn/final.flac"}, nil), nil
		}
		if req.Header.Get("User-Agent") != mediaUserAgent || req.Referer() != mediaReferer {
			t.Fatalf("redirect target headers = %#v", req.Header)
		}
		return response(http.StatusPartialContent, map[string]string{"Content-Range": "bytes 0-41/27383481"}, validFLACStreamInfo(213*time.Second)), nil
	})}
	if _, err := probeMedia(context.Background(), client, "https://kw-er.kuwo.cn/first.flac", "flac", 213*time.Second); err != nil {
		t.Fatalf("probeMedia() = %v", err)
	}
	if len(requests) != 2 {
		t.Fatalf("requests = %v", requests)
	}

	hitExternal := false
	client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == "evil.test" {
			hitExternal = true
		}
		return response(http.StatusFound, map[string]string{"Location": "https://evil.test/final.flac"}, nil), nil
	})
	if _, err := probeMedia(context.Background(), client, "https://kw-er.kuwo.cn/first.flac", "flac", 213*time.Second); err == nil {
		t.Fatal("external redirect succeeded")
	}
	if hitExternal {
		t.Fatal("external redirect target was requested")
	}

	requestCount := 0
	client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requestCount++
		next := requestCount
		return response(http.StatusFound, map[string]string{"Location": fmt.Sprintf("https://kw-er.kuwo.cn/%d.flac", next)}, nil), nil
	})
	if _, err := probeMedia(context.Background(), client, "https://kw-er.kuwo.cn/0.flac", "flac", 213*time.Second); err == nil {
		t.Fatal("redirect chain succeeded")
	}
	if requestCount != 10 {
		t.Fatalf("redirect requests = %d, want 10 before the eleventh request is blocked", requestCount)
	}
}

func TestResolveDownloadReturnsVerifiedQuality(t *testing.T) {
	cleartext := makeTestFLAC(t, 64<<10, 44100, 16, 2, 213*time.Second)
	raw := append(append([]byte(nil), cleartext...), knownDirectFLACTrailer...)
	legacyCalls := 0
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Host {
		case "www.kuwo.cn":
			if req.URL.Path == "/" {
				return response(http.StatusOK, map[string]string{"Set-Cookie": kuwoSessionCookie + "=abcdefghijklmnop; Path=/"}, nil), nil
			}
			return response(http.StatusOK, nil, []byte(`{"data":{"rid":41378936,"name":"Song","duration":213,"isListenFee":false,"payInfo":{"cannotOnlinePlay":0,"listen_fragment":0}}}`)), nil
		case "kw-api.cenguigui.cn":
			t.Fatal("lossless route requested the Hi-Res/master resolver")
			return nil, nil
		case "mobi.kuwo.cn":
			if req.URL.Query().Get("q") == "" || req.URL.Query().Get("br") != "" {
				t.Fatal("lossless route fell through to an MP3 mobile candidate")
			}
			legacyCalls++
			return response(http.StatusOK, nil, []byte(
				"format=flac\n"+
					"bitrate=2000\n"+
					"rid=41378936\n"+
					"duration=213\n"+
					"type=0\n"+
					"url=https://kw-er.kuwo.cn/audio/lossless.flac\n",
			)), nil
		case "kw-er.kuwo.cn":
			switch req.Header.Get("Range") {
			case "bytes=0-41":
				return response(
					http.StatusPartialContent,
					map[string]string{
						"Content-Range": fmt.Sprintf("bytes 0-41/%d", len(raw)),
					},
					raw[:42],
				), nil
			case fmt.Sprintf("bytes=%d-%d", len(cleartext), len(raw)-1):
				tailResponse := response(
					http.StatusPartialContent,
					map[string]string{
						"Content-Range": fmt.Sprintf(
							"bytes %d-%d/%d",
							len(cleartext),
							len(raw)-1,
							len(raw),
						),
					},
					knownDirectFLACTrailer,
				)
				tailResponse.ContentLength = int64(len(knownDirectFLACTrailer))
				return tailResponse, nil
			default:
				t.Fatalf("unexpected direct lossless Range %q", req.Header.Get("Range"))
				return nil, nil
			}
		default:
			t.Fatalf("unexpected request %s", req.URL)
			return nil, nil
		}
	})
	client := NewClient(time.Second, nil)
	client.apiHTTPClient.Transport = transport
	client.mediaHTTPClient.Transport = transport
	client.downloadHTTPClient = &http.Client{Transport: transport}
	now := time.Unix(1700000000, 0)
	client.now = func() time.Time { return now }

	info, err := client.GetDownloadInfo(context.Background(), "41378936", platform.QualityLossless)
	if err != nil {
		t.Fatalf("GetDownloadInfo() = %v", err)
	}
	if info.URL != "https://kw-er.kuwo.cn/audio/lossless.flac" || info.Format != "flac" ||
		info.Size != int64(len(cleartext)) || info.Quality != platform.QualityLossless ||
		info.Downloader == nil {
		t.Fatalf("info = %#v", info)
	}
	if legacyCalls != 1 {
		t.Fatalf("legacy direct calls = %d, want 1", legacyCalls)
	}
	if info.ValidateURL == nil || info.ExpiresAt == nil || !info.ExpiresAt.Equal(now.Add(10*time.Minute)) {
		t.Fatalf("policy/expiry = %#v", info)
	}
	if info.Headers["User-Agent"] != mediaUserAgent || info.Headers["Referer"] != mediaReferer {
		t.Fatalf("headers = %#v", info.Headers)
	}
}

func TestResolveDownloadRejectsFalse320AndFallsBack(t *testing.T) {
	var mobileCalls []string
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Host {
		case "www.kuwo.cn":
			if req.URL.Path == "/" {
				return response(http.StatusOK, map[string]string{"Set-Cookie": kuwoSessionCookie + "=abcdefghijklmnop; Path=/"}, nil), nil
			}
			return response(http.StatusOK, nil, []byte(`{"data":{"rid":41378936,"name":"Song","duration":213,"isListenFee":false}}`)), nil
		case "mobi.kuwo.cn":
			br := req.URL.Query().Get("br")
			mobileCalls = append(mobileCalls, br)
			bitrate := 320
			if br == "128kmp3" {
				bitrate = 128
			}
			return response(http.StatusOK, nil, []byte(fmt.Sprintf(`{"code":200,"data":{"rid":41378936,"url":"http://er-sycdn.kuwo.cn/signed.mp3","format":"mp3","bitrate":%d,"duration":213,"type":"0"}}`, bitrate))), nil
		case "er-sycdn.kuwo.cn":
			return mp3ProbeTransport(t, 3410341, nil).Transport.RoundTrip(req)
		default:
			t.Fatalf("unexpected request %s", req.URL)
			return nil, nil
		}
	})
	client := NewClient(time.Second, nil)
	client.apiHTTPClient.Transport = transport
	client.mediaHTTPClient.Transport = transport
	info, err := client.GetDownloadInfo(context.Background(), "41378936", platform.QualityHigh)
	if err != nil {
		t.Fatalf("GetDownloadInfo() = %v", err)
	}
	if info.Quality != platform.QualityStandard || info.Bitrate != 128 {
		t.Fatalf("info = %#v", info)
	}
	if !slices.Equal(mobileCalls, []string{"320kmp3", "128kmp3"}) {
		t.Fatalf("mobile calls = %v", mobileCalls)
	}
}

func TestResolveDownloadUntrustedMobileHostIsTerminal(t *testing.T) {
	unsafeURLs := []struct {
		name string
		url  string
	}{
		{name: "plain", url: "https://evil.test/signed.mp3"},
		{name: "query", url: "https://evil.test/signed.mp3?token=x"},
		{name: "userinfo", url: "https://user@evil.test/signed.mp3"},
		{name: "explicit port", url: "https://evil.test:443/signed.mp3"},
		{name: "fragment", url: "https://evil.test/signed.mp3#fragment"},
		{name: "http scheme", url: "http://evil.test/signed.mp3"},
	}
	sources := []struct {
		name       string
		response   bool
		initialURL string
	}{
		{name: "response URL", response: true},
		{name: "probe redirect", initialURL: "https://er-sycdn.kuwo.cn/redirect.mp3"},
	}

	for _, source := range sources {
		for _, unsafeURL := range unsafeURLs {
			t.Run(source.name+"/"+unsafeURL.name, func(t *testing.T) {
				firstURL := source.initialURL
				redirectURL := unsafeURL.url
				if source.response {
					firstURL = unsafeURL.url
					redirectURL = ""
				}

				var mobileCalls atomic.Int32
				var webCalls atomic.Int32
				var evilHits atomic.Int32
				transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
					switch req.URL.Hostname() {
					case "www.kuwo.cn":
						if req.URL.Path == "/" {
							return response(http.StatusOK, map[string]string{"Set-Cookie": kuwoSessionCookie + "=abcdefghijklmnop; Path=/"}, nil), nil
						}
						if strings.Contains(req.URL.Path, "playUrl") {
							webCalls.Add(1)
							return response(http.StatusOK, nil, []byte(`{"code":200,"data":{"url":"https://er-sycdn.kuwo.cn/web.mp3"}}`)), nil
						}
						return response(http.StatusOK, nil, []byte(`{"data":{"rid":41378936,"duration":213,"isListenFee":false}}`)), nil
					case "mobi.kuwo.cn":
						call := mobileCalls.Add(1)
						if call == 1 {
							return response(http.StatusOK, nil, []byte(fmt.Sprintf(
								`{"code":200,"data":{"rid":41378936,"url":%q,"format":"mp3","bitrate":320,"duration":213,"type":0}}`,
								firstURL,
							))), nil
						}
						return response(http.StatusOK, nil, []byte(
							`{"code":200,"data":{"rid":41378936,"url":"https://er-sycdn.kuwo.cn/fallback.mp3","format":"mp3","bitrate":128,"duration":213,"type":0}}`,
						)), nil
					case "er-sycdn.kuwo.cn":
						if redirectURL != "" && req.URL.Path == "/redirect.mp3" {
							return response(http.StatusFound, map[string]string{"Location": redirectURL}, nil), nil
						}
						return mp3ProbeTransport(t, 3410341, nil).Transport.RoundTrip(req)
					case "evil.test":
						evilHits.Add(1)
						return response(http.StatusOK, nil, nil), nil
					default:
						t.Fatalf("unexpected request %s", req.URL)
						return nil, nil
					}
				})
				client := NewClient(time.Second, nil)
				client.apiHTTPClient.Transport = transport
				client.mediaHTTPClient.Transport = transport

				_, err := client.GetDownloadInfo(context.Background(), "41378936", platform.QualityHigh)
				if !errors.Is(err, platform.ErrUnavailable) {
					t.Fatalf("error = %v, want ErrUnavailable", err)
				}
				if !errors.Is(err, errUnsafeMediaURL) {
					t.Fatalf("error = %v, want unsafe media URL classification", err)
				}
				if mobileCalls.Load() != 1 || webCalls.Load() != 0 {
					t.Fatalf("mobile calls = %d, web calls = %d; unsafe URL must be terminal", mobileCalls.Load(), webCalls.Load())
				}
				if evilHits.Load() != 0 {
					t.Fatalf("unsafe target was requested %d times", evilHits.Load())
				}
			})
		}
	}
}

func TestResolveDownloadTrailingEmptyFragmentIsTerminal(t *testing.T) {
	for _, tt := range []struct {
		name       string
		quality    platform.Quality
		format     string
		bitrate    int
		mediaTotal int64
	}{
		{name: "mp3", quality: platform.QualityStandard, format: "mp3", bitrate: 128, mediaTotal: 3410341},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var mobileCalls atomic.Int32
			var mediaCalls atomic.Int32
			var webCalls atomic.Int32
			transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
				switch req.URL.Hostname() {
				case "www.kuwo.cn":
					switch {
					case req.URL.Path == "/":
						return response(http.StatusOK, map[string]string{"Set-Cookie": kuwoSessionCookie + "=abcdefghijklmnop; Path=/"}, nil), nil
					case strings.Contains(req.URL.Path, "musicInfo"):
						return response(http.StatusOK, nil, []byte(`{"data":{"rid":41378936,"duration":213,"isListenFee":false}}`)), nil
					case strings.Contains(req.URL.Path, "playUrl"):
						webCalls.Add(1)
						return response(http.StatusOK, nil, []byte(`{"code":200,"data":{"url":"https://er-sycdn.kuwo.cn/web.mp3"}}`)), nil
					}
				case "mobi.kuwo.cn":
					mobileCalls.Add(1)
					if req.URL.Query().Get("f") == "kuwo" {
						return response(http.StatusOK, nil, []byte(
							"format=flac\r\n"+
								"bitrate=2000\r\n"+
								"rid=41378936\r\n"+
								"duration=213\r\n"+
								"type=0\r\n"+
								"url=https://kw-er.kuwo.cn/signed.flac#\r\n",
						)), nil
					}
					body := fmt.Sprintf(
						`{"code":200,"data":{"rid":41378936,"url":"https://kw-er.kuwo.cn/signed.%s#","format":%q,"bitrate":%d,"duration":213,"type":0}}`,
						tt.format,
						tt.format,
						tt.bitrate,
					)
					return response(http.StatusOK, nil, []byte(body)), nil
				case "kw-er.kuwo.cn":
					mediaCalls.Add(1)
					if tt.format == "flac" {
						return response(
							http.StatusPartialContent,
							map[string]string{"Content-Range": fmt.Sprintf("bytes 0-41/%d", tt.mediaTotal)},
							validFLACStreamInfo(213*time.Second),
						), nil
					}
					return mp3ProbeTransport(t, tt.mediaTotal, nil).Transport.RoundTrip(req)
				}
				t.Fatalf("unexpected request %s", req.URL)
				return nil, nil
			})
			client := NewClient(time.Second, nil)
			client.apiHTTPClient.Transport = transport
			client.mediaHTTPClient.Transport = transport

			_, err := client.GetDownloadInfo(context.Background(), "41378936", tt.quality)
			if !errors.Is(err, platform.ErrUnavailable) || !errors.Is(err, errUnsafeMediaURL) {
				t.Fatalf("error = %v, want ErrUnavailable + errUnsafeMediaURL", err)
			}
			if got := mobileCalls.Load(); got != 1 {
				t.Fatalf("mobile calls = %d, want 1", got)
			}
			if got := mediaCalls.Load(); got != 0 {
				t.Fatalf("media probe calls = %d, want 0", got)
			}
			if got := webCalls.Load(); got != 0 {
				t.Fatalf("web calls = %d, want 0", got)
			}
		})
	}
}

func TestResolveDownloadUnsafeURLPrecedesQualityMetadataFallback(t *testing.T) {
	for _, tt := range []struct {
		name     string
		metadata string
	}{
		{name: "format mismatch", metadata: `"format":"flac","bitrate":320`},
		{name: "bitrate mismatch", metadata: `"format":"mp3","bitrate":128`},
		{name: "format and bitrate composites", metadata: `"format":{},"bitrate":[]`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var mobileCalls atomic.Int32
			var mediaCalls atomic.Int32
			var webCalls atomic.Int32
			transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
				switch req.URL.Hostname() {
				case "www.kuwo.cn":
					switch {
					case req.URL.Path == "/":
						return response(http.StatusOK, map[string]string{"Set-Cookie": kuwoSessionCookie + "=abcdefghijklmnop; Path=/"}, nil), nil
					case strings.Contains(req.URL.Path, "musicInfo"):
						return response(http.StatusOK, nil, []byte(`{"data":{"rid":41378936,"duration":213,"isListenFee":false}}`)), nil
					case strings.Contains(req.URL.Path, "playUrl"):
						webCalls.Add(1)
						return response(http.StatusOK, nil, []byte(`{"code":200,"data":{"url":"https://er-sycdn.kuwo.cn/web.mp3"}}`)), nil
					}
				case "mobi.kuwo.cn":
					mobileCalls.Add(1)
					body := `{"code":200,"data":{"rid":41378936,"url":"https://evil.test/signed.mp3?token=x",` +
						tt.metadata + `,"duration":213,"type":0}}`
					return response(http.StatusOK, nil, []byte(body)), nil
				case "evil.test", "er-sycdn.kuwo.cn":
					mediaCalls.Add(1)
					return mp3ProbeTransport(t, 3410341, nil).Transport.RoundTrip(req)
				}
				t.Fatalf("unexpected request %s", req.URL)
				return nil, nil
			})
			client := NewClient(time.Second, nil)
			client.apiHTTPClient.Transport = transport
			client.mediaHTTPClient.Transport = transport

			_, err := client.GetDownloadInfo(context.Background(), "41378936", platform.QualityHigh)
			if !errors.Is(err, platform.ErrUnavailable) {
				t.Fatalf("error = %v, want ErrUnavailable", err)
			}
			if !errors.Is(err, errUnsafeMediaURL) {
				t.Fatalf("error = %v, want unsafe media URL classification", err)
			}
			if got := mobileCalls.Load(); got != 1 {
				t.Fatalf("mobile calls = %d, want 1", got)
			}
			if got := mediaCalls.Load(); got != 0 {
				t.Fatalf("media probe calls = %d, want 0", got)
			}
			if got := webCalls.Load(); got != 0 {
				t.Fatalf("web calls = %d, want 0", got)
			}
		})
	}
}

func TestResolveDownloadPreviewBitratePrecedesSafeSuffixMismatchFallback(t *testing.T) {
	var mobileCalls atomic.Int32
	var mediaCalls atomic.Int32
	var webCalls atomic.Int32
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Hostname() {
		case "www.kuwo.cn":
			switch {
			case req.URL.Path == "/":
				return response(http.StatusOK, map[string]string{"Set-Cookie": kuwoSessionCookie + "=abcdefghijklmnop; Path=/"}, nil), nil
			case strings.Contains(req.URL.Path, "musicInfo"):
				return response(http.StatusOK, nil, []byte(`{"data":{"rid":41378936,"duration":213,"isListenFee":false}}`)), nil
			case strings.Contains(req.URL.Path, "playUrl"):
				webCalls.Add(1)
				return response(http.StatusOK, nil, []byte(`{"code":200,"data":{"url":"https://er-sycdn.kuwo.cn/web.mp3"}}`)), nil
			}
		case "mobi.kuwo.cn":
			call := mobileCalls.Add(1)
			if call == 1 {
				return response(http.StatusOK, nil, []byte(
					`{"code":200,"data":{"rid":41378936,"url":"https://er-sycdn.kuwo.cn/first.flac","format":"mp3","bitrate":1,"duration":213,"type":0}}`,
				)), nil
			}
			return response(http.StatusOK, nil, []byte(
				`{"code":200,"data":{"rid":41378936,"url":"https://er-sycdn.kuwo.cn/fallback.mp3","format":"mp3","bitrate":128,"duration":213,"type":0}}`,
			)), nil
		case "er-sycdn.kuwo.cn":
			mediaCalls.Add(1)
			return mp3ProbeTransport(t, 3410341, nil).Transport.RoundTrip(req)
		}
		t.Fatalf("unexpected request %s", req.URL)
		return nil, nil
	})
	client := NewClient(time.Second, nil)
	client.apiHTTPClient.Transport = transport
	client.mediaHTTPClient.Transport = transport

	_, err := client.GetDownloadInfo(context.Background(), "41378936", platform.QualityHigh)
	if !errors.Is(err, platform.ErrUnavailable) {
		t.Fatalf("error = %v, want ErrUnavailable", err)
	}
	if !errors.Is(err, errPreviewMedia) {
		t.Fatalf("error = %v, want preview media classification", err)
	}
	if got := mobileCalls.Load(); got != 1 {
		t.Fatalf("mobile calls = %d, want 1", got)
	}
	if got := mediaCalls.Load(); got != 0 {
		t.Fatalf("media probe calls = %d, want 0", got)
	}
	if got := webCalls.Load(); got != 0 {
		t.Fatalf("web calls = %d, want 0", got)
	}
}

func TestResolveDownloadOverflowingMobileDurationIsTerminal(t *testing.T) {
	for _, tt := range []struct {
		name     string
		duration string
	}{
		{name: "number", duration: `36028797018964181`},
		{name: "string", duration: `"36028797018964181"`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var mobileCalls atomic.Int32
			var mediaCalls atomic.Int32
			var webCalls atomic.Int32
			transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
				switch req.URL.Hostname() {
				case "www.kuwo.cn":
					switch {
					case req.URL.Path == "/":
						return response(http.StatusOK, map[string]string{"Set-Cookie": kuwoSessionCookie + "=abcdefghijklmnop; Path=/"}, nil), nil
					case strings.Contains(req.URL.Path, "musicInfo"):
						return response(http.StatusOK, nil, []byte(`{"data":{"rid":41378936,"duration":213,"isListenFee":false}}`)), nil
					case strings.Contains(req.URL.Path, "playUrl"):
						webCalls.Add(1)
						return response(http.StatusOK, nil, []byte(`{"code":200,"data":{"url":"https://er-sycdn.kuwo.cn/web.mp3"}}`)), nil
					}
				case "mobi.kuwo.cn":
					mobileCalls.Add(1)
					body := fmt.Sprintf(
						`{"code":200,"data":{"rid":41378936,"url":"https://er-sycdn.kuwo.cn/mobile.mp3","format":"mp3","bitrate":320,"duration":%s,"type":0}}`,
						tt.duration,
					)
					return response(http.StatusOK, nil, []byte(body)), nil
				case "er-sycdn.kuwo.cn":
					mediaCalls.Add(1)
					return mp3ProbeTransport(t, 8525534, nil).Transport.RoundTrip(req)
				}
				t.Fatalf("unexpected request %s", req.URL)
				return nil, nil
			})
			client := NewClient(time.Second, nil)
			client.apiHTTPClient.Transport = transport
			client.mediaHTTPClient.Transport = transport

			_, err := client.GetDownloadInfo(context.Background(), "41378936", platform.QualityHigh)
			if !errors.Is(err, platform.ErrUnavailable) {
				t.Fatalf("error = %v, want ErrUnavailable", err)
			}
			if !errors.Is(err, errTrackDurationMismatch) {
				t.Fatalf("error = %v, want track duration mismatch", err)
			}
			if got := mobileCalls.Load(); got != 1 {
				t.Fatalf("mobile calls = %d, want 1", got)
			}
			if got := mediaCalls.Load(); got != 0 {
				t.Fatalf("media probe calls = %d, want 0", got)
			}
			if got := webCalls.Load(); got != 0 {
				t.Fatalf("web calls = %d, want 0", got)
			}
		})
	}
}

func TestRejectPreviewAndAccessSignalsAreTerminal(t *testing.T) {
	fixtures := []struct {
		name   string
		detail string
		want   error
	}{
		{"listen fee", `{"data":{"rid":41378936,"duration":213,"isListenFee":true}}`, errPaidTrack},
		{"listen fee null", `{"data":{"rid":41378936,"duration":213,"isListenFee":null}}`, errPaidTrack},
		{"listen fee malformed string", `{"data":{"rid":41378936,"duration":213,"isListenFee":"garbage"}}`, errPaidTrack},
		{"listen fee fractional", `{"data":{"rid":41378936,"duration":213,"isListenFee":0.5}}`, errPaidTrack},
		{"listen fee composite", `{"data":{"rid":41378936,"duration":213,"isListenFee":{}}}`, errPaidTrack},
		{"cannot play", `{"data":{"rid":41378936,"duration":213,"isListenFee":false,"payInfo":{"cannotOnlinePlay":1}}}`, errPaidTrack},
		{"fragment", `{"data":{"rid":41378936,"duration":213,"isListenFee":false,"payInfo":{"listen_fragment":"true"}}}`, errPaidTrack},
		{"malformed cannot play", `{"data":{"rid":41378936,"duration":213,"isListenFee":false,"payInfo":{"cannotOnlinePlay":{"unexpected":1}}}}`, errPaidTrack},
		{"malformed fragment", `{"data":{"rid":41378936,"duration":213,"isListenFee":false,"payInfo":{"listen_fragment":[]}}}`, errPaidTrack},
		{"null known flag", `{"data":{"rid":41378936,"duration":213,"isListenFee":false,"payInfo":{"cannotOnlinePlay":null}}}`, errPaidTrack},
		{"malformed pay info", `{"data":{"rid":41378936,"duration":213,"isListenFee":false,"payInfo":[]}}`, errPaidTrack},
		{"duplicate listen fee", `{"data":{"rid":41378936,"duration":213,"isListenFee":true,"isListenFee":false}}`, platform.ErrUnavailable},
		{"duplicate pay flag", `{"data":{"rid":41378936,"duration":213,"isListenFee":false,"payInfo":{"cannotOnlinePlay":1,"cannotOnlinePlay":0}}}`, platform.ErrUnavailable},
	}
	for _, tt := range fixtures {
		t.Run(tt.name, func(t *testing.T) {
			mobileCalls := 0
			webCalls := 0
			transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.Path == "/" {
					return response(http.StatusOK, map[string]string{"Set-Cookie": kuwoSessionCookie + "=abcdefghijklmnop; Path=/"}, nil), nil
				}
				if req.URL.Host == "mobi.kuwo.cn" {
					mobileCalls++
				}
				if req.URL.Host == "www.kuwo.cn" && strings.Contains(req.URL.Path, "playUrl") {
					webCalls++
				}
				return response(http.StatusOK, nil, []byte(tt.detail)), nil
			})
			client := NewClient(time.Second, nil)
			client.apiHTTPClient.Transport = transport
			client.mediaHTTPClient.Transport = transport
			_, err := client.GetDownloadInfo(context.Background(), "41378936", platform.QualityHigh)
			if !errors.Is(err, platform.ErrUnavailable) || !errors.Is(err, tt.want) {
				t.Fatalf("error = %v, want unavailable and %v", err, tt.want)
			}
			if mobileCalls != 0 {
				t.Fatalf("mobile calls = %d", mobileCalls)
			}
			if webCalls != 0 {
				t.Fatalf("web calls = %d", webCalls)
			}
		})
	}
}

func TestPaidMetadataAllowsOnlyStrictlyVerifiedDirectHiRes(t *testing.T) {
	const (
		trackID = "7149583"
		rawSize = 1 << 20
	)
	streamInfo := makeTestFLAC(t, 42, 96000, 24, 2, time.Second)
	var resolverCalls atomic.Int32
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Host {
		case "www.kuwo.cn":
			if req.URL.Path == "/" {
				return response(
					http.StatusOK,
					map[string]string{
						"Set-Cookie": kuwoSessionCookie + "=abcdefghijklmnop; Path=/",
					},
					nil,
				), nil
			}
			return response(http.StatusOK, nil, []byte(
				`{"data":{"rid":7149583,"duration":1,"isListenFee":true}}`,
			)), nil
		case "resolver.example":
			resolverCalls.Add(1)
			if req.URL.Query().Get("level") != "hires" {
				t.Fatalf("resolver level = %q, want hires", req.URL.Query().Get("level"))
			}
			return response(http.StatusOK, nil, []byte(
				`{"code":200,"data":{"rid":"7149583","bitrate":4000,"duration":1,`+
					`"size":"1.00 MB","url":"https://kw-lw.kuwo.cn/audio/hires.flac",`+
					`"level":{"requested":"hires","actual":"hires","ekey":"","quality":[`+
					`{"br":"4000","format":"flac","level":"hires"}]}}}`,
			)), nil
		case "kw-lw.kuwo.cn":
			switch req.Header.Get("Range") {
			case "bytes=0-41":
				return response(
					http.StatusPartialContent,
					map[string]string{"Content-Range": "bytes 0-41/1048576"},
					streamInfo,
				), nil
			case "bytes=1048561-1048575":
				tailResponse := response(
					http.StatusPartialContent,
					map[string]string{
						"Content-Range": "bytes 1048561-1048575/1048576",
					},
					make([]byte, len(knownDirectFLACTrailer)),
				)
				tailResponse.ContentLength = int64(len(knownDirectFLACTrailer))
				return tailResponse, nil
			default:
				t.Fatalf("unexpected direct Hi-Res Range %q", req.Header.Get("Range"))
				return nil, nil
			}
		case "mobi.kuwo.cn":
			t.Fatal("paid Hi-Res request fell through to MP3")
			return nil, nil
		default:
			t.Fatalf("unexpected request host %q", req.URL.Host)
			return nil, nil
		}
	})
	client := newClientWithEndpoints(time.Second, nil, kuwoEndpoints{
		home:            kuwoHomeURL,
		detail:          kuwoDetailURL,
		qualityResolver: "https://resolver.example/api",
	})
	client.apiHTTPClient.Transport = transport
	client.mediaHTTPClient.Transport = transport
	client.downloadHTTPClient = &http.Client{Transport: transport}

	info, err := client.GetDownloadInfo(
		context.Background(),
		trackID,
		platform.QualityHiRes,
	)
	if err != nil {
		t.Fatalf("GetDownloadInfo() = %v", err)
	}
	if resolverCalls.Load() != 1 ||
		info == nil ||
		info.Quality != platform.QualityHiRes ||
		info.Downloader == nil {
		t.Fatalf("resolverCalls=%d info=%#v", resolverCalls.Load(), info)
	}
}

func TestHiResFalseLabelFallsBackToDirect2000WithoutMaster(t *testing.T) {
	const (
		trackID = "41378936"
		rawSize = 1 << 20
	)
	falseHiResHeader := makeTestFLAC(t, 42, 44100, 16, 2, 213*time.Second)
	losslessCleartext := makeTestFLAC(
		t,
		rawSize-len(knownDirectFLACTrailer),
		44100,
		16,
		2,
		213*time.Second,
	)
	losslessRaw := append(
		append([]byte(nil), losslessCleartext...),
		knownDirectFLACTrailer...,
	)
	var resolverCalls atomic.Int32
	var legacyCalls atomic.Int32
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if strings.EqualFold(req.URL.Query().Get("level"), "jymaster") ||
			strings.EqualFold(path.Ext(req.URL.Path), ".mflac") {
			t.Fatal("runtime requested a master stream")
		}
		switch req.URL.Host {
		case "www.kuwo.cn":
			if req.URL.Path == "/" {
				return response(
					http.StatusOK,
					map[string]string{
						"Set-Cookie": kuwoSessionCookie + "=abcdefghijklmnop; Path=/",
					},
					nil,
				), nil
			}
			return response(http.StatusOK, nil, []byte(
				`{"data":{"rid":41378936,"duration":213,"isListenFee":false}}`,
			)), nil
		case "resolver.example":
			resolverCalls.Add(1)
			if req.URL.Query().Get("level") != "hires" {
				t.Fatalf("resolver level = %q, want hires", req.URL.Query().Get("level"))
			}
			return response(http.StatusOK, nil, []byte(
				`{"code":200,"data":{"rid":"41378936","bitrate":4000,"duration":213,`+
					`"size":"1.00 MB","url":"https://kw-lw.kuwo.cn/audio/false-hires.flac",`+
					`"level":{"requested":"hires","actual":"hires","ekey":"","quality":[`+
					`{"br":"4000","format":"flac","level":"hires"}]}}}`,
			)), nil
		case "kw-lw.kuwo.cn":
			if req.Header.Get("Range") != "bytes=0-41" {
				t.Fatalf("unexpected false Hi-Res Range %q", req.Header.Get("Range"))
			}
			return response(
				http.StatusPartialContent,
				map[string]string{"Content-Range": "bytes 0-41/1048576"},
				falseHiResHeader,
			), nil
		case "mobi.kuwo.cn":
			if req.URL.Query().Get("q") == "" || req.URL.Query().Get("br") != "" {
				t.Fatal("Hi-Res fallback skipped the direct 2000 resolver")
			}
			legacyCalls.Add(1)
			return response(http.StatusOK, nil, []byte(
				"format=flac\n"+
					"bitrate=2000\n"+
					"rid=41378936\n"+
					"duration=213\n"+
					"type=0\n"+
					"url=https://kw-er.kuwo.cn/audio/lossless.flac\n",
			)), nil
		case "kw-er.kuwo.cn":
			switch req.Header.Get("Range") {
			case "bytes=0-41":
				return response(
					http.StatusPartialContent,
					map[string]string{"Content-Range": "bytes 0-41/1048576"},
					losslessRaw[:42],
				), nil
			case "bytes=1048561-1048575":
				tailResponse := response(
					http.StatusPartialContent,
					map[string]string{
						"Content-Range": "bytes 1048561-1048575/1048576",
					},
					losslessRaw[rawSize-len(knownDirectFLACTrailer):],
				)
				tailResponse.ContentLength = int64(len(knownDirectFLACTrailer))
				return tailResponse, nil
			case "":
				return directFLACResponse(losslessRaw), nil
			default:
				t.Fatalf("unexpected lossless Range %q", req.Header.Get("Range"))
				return nil, nil
			}
		default:
			t.Fatalf("unexpected request host %q", req.URL.Host)
			return nil, nil
		}
	})
	client := newClientWithEndpoints(time.Second, nil, kuwoEndpoints{
		home:            kuwoHomeURL,
		detail:          kuwoDetailURL,
		qualityResolver: "https://resolver.example/api",
	})
	client.apiHTTPClient.Transport = transport
	client.mediaHTTPClient.Transport = transport
	client.downloadHTTPClient = &http.Client{Transport: transport}

	info, err := client.GetDownloadInfo(
		context.Background(),
		trackID,
		platform.QualityHiRes,
	)
	if err != nil {
		t.Fatalf("GetDownloadInfo() = %v", err)
	}
	if resolverCalls.Load() != 1 ||
		legacyCalls.Load() != 1 ||
		info == nil ||
		info.Quality != platform.QualityLossless ||
		info.Downloader == nil {
		t.Fatalf(
			"resolverCalls=%d legacyCalls=%d info=%#v",
			resolverCalls.Load(),
			legacyCalls.Load(),
			info,
		)
	}
	destination := filepath.Join(t.TempDir(), "fallback.flac")
	written, err := info.Downloader(
		context.Background(),
		info,
		destination,
		nil,
	)
	if err != nil {
		t.Fatalf("fallback downloader = %v", err)
	}
	if written != int64(len(losslessCleartext)) {
		t.Fatalf("fallback written = %d, want %d", written, len(losslessCleartext))
	}
	downloaded, err := os.ReadFile(destination)
	if err != nil {
		t.Fatalf("read fallback download: %v", err)
	}
	if !bytes.Equal(downloaded, losslessCleartext) {
		t.Fatal("fallback downloader did not publish the verified 2000k stream")
	}
}

func TestResolveWebDownloadEmptyURLIsTerminal(t *testing.T) {
	for _, tt := range []struct {
		name string
		body string
	}{
		{name: "missing", body: `{"code":200,"data":{}}`},
		{name: "null", body: `{"code":200,"data":{"url":null}}`},
		{name: "empty", body: `{"code":200,"data":{"url":""}}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var mediaCalls atomic.Int32
			transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
				switch req.URL.Host {
				case "www.kuwo.cn":
					switch {
					case req.URL.Path == "/":
						return response(http.StatusOK, map[string]string{"Set-Cookie": kuwoSessionCookie + "=abcdefghijklmnop; Path=/"}, nil), nil
					case strings.Contains(req.URL.Path, "musicInfo"):
						return response(http.StatusOK, nil, []byte(`{"data":{"rid":41378936,"duration":213,"isListenFee":false}}`)), nil
					case strings.Contains(req.URL.Path, "playUrl"):
						return response(http.StatusOK, nil, []byte(tt.body)), nil
					}
				case "mobi.kuwo.cn":
					return response(http.StatusOK, nil, []byte(`{"code":500}`)), nil
				default:
					mediaCalls.Add(1)
				}
				t.Fatalf("unexpected request %s", req.URL)
				return nil, nil
			})
			client := NewClient(time.Second, nil)
			client.apiHTTPClient.Transport = transport
			client.mediaHTTPClient.Transport = transport

			_, err := client.GetDownloadInfo(context.Background(), "41378936", platform.QualityStandard)
			if !errors.Is(err, platform.ErrUnavailable) {
				t.Fatalf("error = %v, want ErrUnavailable", err)
			}
			if mediaCalls.Load() != 0 {
				t.Fatalf("media probe calls = %d", mediaCalls.Load())
			}
		})
	}
}

func TestResolveDownloadTerminalIdentityAndTypeErrorsDoNotFallback(t *testing.T) {
	for _, fixture := range []string{
		`{"code":200,"data":{"rid":99999999,"url":"http://er-sycdn.kuwo.cn/a.mp3","format":"mp3","duration":213,"type":0}}`,
		`{"code":200,"data":{"rid":41378936,"url":"http://er-sycdn.kuwo.cn/a.mp3","format":"mp3","duration":213}}`,
		`{"code":200,"data":{"rid":41378936,"url":"http://er-sycdn.kuwo.cn/a.mp3","format":"mp3","duration":11,"type":0}}`,
	} {
		t.Run(fixture[:20], func(t *testing.T) {
			webCalls := 0
			transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
				switch req.URL.Host {
				case "www.kuwo.cn":
					if req.URL.Path == "/" {
						return response(http.StatusOK, map[string]string{"Set-Cookie": kuwoSessionCookie + "=abcdefghijklmnop; Path=/"}, nil), nil
					}
					if strings.Contains(req.URL.Path, "playUrl") {
						webCalls++
					}
					return response(http.StatusOK, nil, []byte(`{"data":{"rid":41378936,"duration":213,"isListenFee":false}}`)), nil
				case "mobi.kuwo.cn":
					return response(http.StatusOK, nil, []byte(fixture)), nil
				default:
					return response(http.StatusInternalServerError, nil, nil), nil
				}
			})
			client := NewClient(time.Second, nil)
			client.apiHTTPClient.Transport = transport
			client.mediaHTTPClient.Transport = transport
			_, err := client.GetDownloadInfo(context.Background(), "41378936", platform.QualityStandard)
			if !errors.Is(err, platform.ErrUnavailable) {
				t.Fatalf("error = %v, want unavailable", err)
			}
			if webCalls != 0 {
				t.Fatalf("web calls = %d", webCalls)
			}
		})
	}
}

func TestResolveDownloadMalformedCriticalMobileFieldsAreTerminal(t *testing.T) {
	fixtures := []struct {
		name string
		data string
	}{
		{"rid object", `"rid":{}`},
		{"rid array", `"rid":[]`},
		{"rid null", `"rid":null`},
		{"rid missing", ``},
		{"rid bool", `"rid":true`},
		{"type object", `"type":{}`},
		{"type array", `"type":[]`},
		{"type null", `"type":null`},
		{"type missing", ``},
		{"type bool", `"type":false`},
		{"type double zero", `"type":"00"`},
		{"type plus zero", `"type":"+0"`},
		{"type minus zero string", `"type":"-0"`},
		{"type decimal zero string", `"type":"0.0"`},
		{"type spaced zero", `"type":" 0 "`},
		{"type numeric minus zero", `"type":-0`},
		{"type numeric decimal zero", `"type":0.0`},
		{"duration object", `"duration":{}`},
		{"duration array", `"duration":[]`},
		{"duration null", `"duration":null`},
		{"duration missing", ``},
		{"duration bool", `"duration":true`},
	}

	for _, tt := range fixtures {
		t.Run(tt.name, func(t *testing.T) {
			fields := map[string]string{
				"rid":      `"rid":41378936`,
				"type":     `"type":0`,
				"duration": `"duration":213`,
			}
			switch {
			case strings.HasPrefix(tt.name, "rid "):
				fields["rid"] = tt.data
			case strings.HasPrefix(tt.name, "type "):
				fields["type"] = tt.data
			case strings.HasPrefix(tt.name, "duration "):
				fields["duration"] = tt.data
			default:
				t.Fatalf("unclassified fixture %q", tt.name)
			}
			parts := []string{
				fields["rid"],
				`"url":"https://er-sycdn.kuwo.cn/mobile.mp3"`,
				`"format":"mp3"`,
				`"bitrate":128`,
				fields["duration"],
				fields["type"],
			}
			present := parts[:0]
			for _, part := range parts {
				if part != "" {
					present = append(present, part)
				}
			}
			fixture := `{"code":200,"data":{` + strings.Join(present, ",") + `}}`

			var mobileCalls atomic.Int32
			var webCalls atomic.Int32
			var mediaCalls atomic.Int32
			transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
				switch req.URL.Host {
				case "www.kuwo.cn":
					switch {
					case req.URL.Path == "/":
						return response(http.StatusOK, map[string]string{"Set-Cookie": kuwoSessionCookie + "=abcdefghijklmnop; Path=/"}, nil), nil
					case strings.Contains(req.URL.Path, "musicInfo"):
						return response(http.StatusOK, nil, []byte(`{"data":{"rid":41378936,"duration":213,"isListenFee":false}}`)), nil
					case strings.Contains(req.URL.Path, "playUrl"):
						webCalls.Add(1)
						return response(http.StatusOK, nil, []byte(`{"code":200,"data":{"url":"https://er-sycdn.kuwo.cn/web.mp3"}}`)), nil
					}
				case "mobi.kuwo.cn":
					mobileCalls.Add(1)
					return response(http.StatusOK, nil, []byte(fixture)), nil
				case "er-sycdn.kuwo.cn":
					mediaCalls.Add(1)
					return mp3ProbeTransport(t, 3410341, nil).Transport.RoundTrip(req)
				}
				t.Fatalf("unexpected request %s", req.URL)
				return nil, nil
			})
			client := NewClient(time.Second, nil)
			client.apiHTTPClient.Transport = transport
			client.mediaHTTPClient.Transport = transport

			_, err := client.GetDownloadInfo(context.Background(), "41378936", platform.QualityStandard)
			if !errors.Is(err, platform.ErrUnavailable) {
				t.Fatalf("error = %v, want ErrUnavailable", err)
			}
			if got := mobileCalls.Load(); got != 1 {
				t.Fatalf("mobile calls = %d, want 1", got)
			}
			if got := webCalls.Load(); got != 0 {
				t.Fatalf("web calls = %d, want 0", got)
			}
			if got := mediaCalls.Load(); got != 0 {
				t.Fatalf("media calls = %d, want 0", got)
			}
		})
	}
}

func TestResolveDownloadMalformedQualityMetadataCanDowngrade(t *testing.T) {
	for _, tt := range []struct {
		name          string
		firstURL      string
		firstMetadata string
	}{
		{name: "empty URL", firstURL: `""`, firstMetadata: `"format":"mp3","bitrate":320`},
		{name: "suffix mismatch", firstURL: `"https://er-sycdn.kuwo.cn/first.flac"`, firstMetadata: `"format":"mp3","bitrate":320`},
		{name: "bitrate mismatch", firstMetadata: `"format":"mp3","bitrate":128`},
		{name: "format mismatch", firstMetadata: `"format":"flac","bitrate":320`},
		{name: "bitrate object", firstMetadata: `"format":"mp3","bitrate":{}`},
		{name: "format array", firstMetadata: `"format":[],"bitrate":320`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			firstURL := tt.firstURL
			if firstURL == "" {
				firstURL = `"https://er-sycdn.kuwo.cn/first.mp3"`
			}
			var mobileCalls []string
			var mediaCalls atomic.Int32
			transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
				switch req.URL.Host {
				case "www.kuwo.cn":
					if req.URL.Path == "/" {
						return response(http.StatusOK, map[string]string{"Set-Cookie": kuwoSessionCookie + "=abcdefghijklmnop; Path=/"}, nil), nil
					}
					return response(http.StatusOK, nil, []byte(`{"data":{"rid":41378936,"duration":213,"isListenFee":false}}`)), nil
				case "mobi.kuwo.cn":
					br := req.URL.Query().Get("br")
					mobileCalls = append(mobileCalls, br)
					if br == "320kmp3" {
						body := `{"code":200,"data":{"rid":41378936,"url":` + firstURL + `,` +
							tt.firstMetadata + `,"duration":213,"type":0}}`
						return response(http.StatusOK, nil, []byte(body)), nil
					}
					return response(http.StatusOK, nil, []byte(
						`{"code":200,"data":{"rid":41378936,"url":"https://er-sycdn.kuwo.cn/fallback.mp3","format":"mp3","bitrate":128,"duration":213,"type":"0"}}`,
					)), nil
				case "er-sycdn.kuwo.cn":
					mediaCalls.Add(1)
					return mp3ProbeTransport(t, 3410341, nil).Transport.RoundTrip(req)
				}
				t.Fatalf("unexpected request %s", req.URL)
				return nil, nil
			})
			client := NewClient(time.Second, nil)
			client.apiHTTPClient.Transport = transport
			client.mediaHTTPClient.Transport = transport

			info, err := client.GetDownloadInfo(context.Background(), "41378936", platform.QualityHigh)
			if err != nil {
				t.Fatalf("GetDownloadInfo() = %v", err)
			}
			if info.Quality != platform.QualityStandard || info.Bitrate != 128 {
				t.Fatalf("info = %#v", info)
			}
			if !slices.Equal(mobileCalls, []string{"320kmp3", "128kmp3"}) {
				t.Fatalf("mobile calls = %v", mobileCalls)
			}
			if got := mediaCalls.Load(); got != 2 {
				t.Fatalf("media range calls = %d, want 2 for the verified fallback only", got)
			}
		})
	}
}

func TestResolveDownloadMobileTransportFailureUsesVerifiedWebMP3(t *testing.T) {
	for _, test := range []struct {
		name    string
		size    int64
		quality platform.Quality
		bitrate int
	}{
		{name: "128k", size: 3410341, quality: platform.QualityStandard, bitrate: 128},
		{name: "320k", size: 8525534, quality: platform.QualityHigh, bitrate: 320},
	} {
		t.Run(test.name, func(t *testing.T) {
			var webCalls atomic.Int32
			transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
				switch req.URL.Host {
				case "www.kuwo.cn":
					switch {
					case req.URL.Path == "/":
						return response(http.StatusOK, map[string]string{"Set-Cookie": kuwoSessionCookie + "=abcdefghijklmnop; Path=/"}, nil), nil
					case strings.Contains(req.URL.Path, "musicInfo"):
						return response(http.StatusOK, nil, []byte(`{"data":{"rid":41378936,"duration":213,"isListenFee":false}}`)), nil
					case strings.Contains(req.URL.Path, "playUrl"):
						webCalls.Add(1)
						if req.URL.Query().Get("mid") != "41378936" || req.URL.Query().Get("type") != "music" || req.URL.Query().Get("httpsStatus") != "1" {
							t.Fatalf("web query = %v", req.URL.Query())
						}
						return response(http.StatusOK, nil, []byte(`{"code":200,"data":{"url":"https://er-sycdn.kuwo.cn/web.mp3"}}`)), nil
					}
				case "mobi.kuwo.cn":
					return nil, errors.New("mobile transport down")
				case "kw-api.cenguigui.cn":
					return response(http.StatusOK, nil, []byte(`{"code":404,"data":[]}`)), nil
				case "er-sycdn.kuwo.cn":
					return mp3ProbeTransport(t, test.size, nil).Transport.RoundTrip(req)
				}
				t.Fatalf("unexpected request %s", req.URL)
				return nil, nil
			})
			client := NewClient(time.Second, nil)
			client.apiHTTPClient.Transport = transport
			client.mediaHTTPClient.Transport = transport
			info, err := client.GetDownloadInfo(context.Background(), "41378936", platform.QualityLossless)
			if err != nil {
				t.Fatalf("GetDownloadInfo() = %v", err)
			}
			if webCalls.Load() != 1 || info.Quality != test.quality || info.Bitrate != test.bitrate {
				t.Fatalf("webCalls=%d info=%#v", webCalls.Load(), info)
			}
		})
	}
}

func TestResolveDownloadRateLimitIsTerminal(t *testing.T) {
	var webCalls atomic.Int32
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Host {
		case "www.kuwo.cn":
			if req.URL.Path == "/" {
				return response(http.StatusOK, map[string]string{"Set-Cookie": kuwoSessionCookie + "=abcdefghijklmnop; Path=/"}, nil), nil
			}
			if strings.Contains(req.URL.Path, "playUrl") {
				webCalls.Add(1)
			}
			return response(http.StatusOK, nil, []byte(`{"data":{"rid":41378936,"duration":213,"isListenFee":false}}`)), nil
		case "mobi.kuwo.cn":
			return response(http.StatusTooManyRequests, nil, nil), nil
		case "kw-api.cenguigui.cn":
			return response(http.StatusOK, nil, []byte(`{"code":404,"data":[]}`)), nil
		default:
			t.Fatalf("unexpected request %s", req.URL)
			return nil, nil
		}
	})
	client := NewClient(time.Second, nil)
	client.apiHTTPClient.Transport = transport
	client.mediaHTTPClient.Transport = transport
	_, err := client.GetDownloadInfo(context.Background(), "41378936", platform.QualityLossless)
	if !errors.Is(err, platform.ErrRateLimited) {
		t.Fatalf("error = %v, want rate limited", err)
	}
	if webCalls.Load() != 0 {
		t.Fatalf("web calls = %d", webCalls.Load())
	}
}

func formatInt64(value int64) string {
	return strconv.FormatInt(value, 10)
}
