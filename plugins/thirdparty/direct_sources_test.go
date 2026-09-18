package thirdparty

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/liuran001/MusicBot-Go/bot/platform"
)

func sourceTestResponse(req *http.Request, status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: req}
}

func sourceTestAudio(t *testing.T, req *http.Request, format string) *http.Response {
	t.Helper()
	if req.Header.Get("Range") != "bytes=0-1023" {
		t.Errorf("unexpected range: %q", req.Header.Get("Range"))
	}
	for _, key := range []string{"Cookie", "Authorization", "X-OM-Sign", "X-OM-Ts", "X-OM-Nonce", "Origin", "Referer"} {
		if req.Header.Get(key) != "" {
			t.Errorf("provider credential/header leaked to CDN: %s", key)
		}
	}
	magic := "ID3"
	if format == "flac" {
		magic = "fLaC"
	}
	resp := sourceTestResponse(req, http.StatusPartialContent, magic+strings.Repeat("\x00", 1024-len(magic)))
	resp.Header.Set("Content-Range", "bytes 0-1023/27550905")
	resp.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp.ContentLength = 1024
	return resp
}

func TestLZMHHHRequestsAndActualQuality(t *testing.T) {
	for _, tc := range []struct{ platform, id, kind, media, format string }{
		{"qqmusic", "000gt6Y92Dm4YQ", "qq", "https://isure6.stream.qqmusic.qq.com/M800track.mp3?token=secret", "mp3"},
		{"kugou", "4be1d1af233aa8519099fcdfaa0e205d", "kg", "http://fs.youthandroid2.kugou.com/track.flac?token=secret", "flac"},
	} {
		t.Run(tc.platform, func(t *testing.T) {
			calls := 0
			client := &http.Client{Transport: jbsouRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.URL.Host == "provider.test" {
					if req.Method != http.MethodPost || req.URL.Path != "/api/music/url" {
						t.Errorf("unexpected API request: %s %s", req.Method, req.URL.Path)
					}
					if err := req.ParseForm(); err != nil || req.Form.Get("type") != tc.kind || !strings.EqualFold(req.Form.Get("id"), tc.id) {
						t.Error("incorrect exact-ID request")
					}
					if req.Header.Get("Cookie") != "" || req.Header.Get("Origin") != "https://provider.test" {
						t.Error("unexpected API identity")
					}
					body, _ := json.Marshal(map[string]any{"code": 1, "data": tc.media})
					return sourceTestResponse(req, 200, "\xef\xbb\xbf"+string(body)), nil
				}
				return sourceTestAudio(t, req, tc.format), nil
			})}
			p, err := newLZMHHHProvider("https://provider.test", time.Second, client)
			if err != nil {
				t.Fatal(err)
			}
			info, err := p.Resolve(t.Context(), tc.platform, tc.id, platform.QualityHiRes)
			if err != nil {
				t.Fatal(err)
			}
			wantQuality := platform.QualityLossless
			if tc.format == "mp3" {
				wantQuality = platform.QualityHigh
			}
			if calls != 2 || info.Size != 27550905 || info.Format != tc.format || info.Quality != wantQuality {
				t.Fatalf("calls=%d format=%s quality=%s size=%d", calls, info.Format, info.Quality, info.Size)
			}
			if info.ValidateURL(info.URL) != nil || info.ValidateURL("https://evil.test/music.mp3") == nil {
				t.Fatal("missing download URL policy")
			}
		})
	}
}

func TestNXINXZExactIDAndHTTPS(t *testing.T) {
	client := &http.Client{Transport: jbsouRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == "provider.test" {
			if req.URL.Scheme != "https" || req.Method != "GET" || req.URL.Path != "/kw.php" || req.URL.Query().Get("id") != "166731" || req.URL.Query().Get("level") != "lossless" {
				t.Error("unexpected nxinxz request")
			}
			return sourceTestResponse(req, 200, `{"code":200,"data":{"url":"https://car-er.kuwo.cn/track.flac"}}`), nil
		}
		return sourceTestAudio(t, req, "flac"), nil
	})}
	p, err := newNXINXZProvider("https://provider.test", time.Second, client)
	if err != nil {
		t.Fatal(err)
	}
	info, err := p.Resolve(t.Context(), "kuwo", "166731", platform.QualityHigh)
	if err != nil || info == nil || info.Quality != platform.QualityLossless {
		t.Fatalf("info=%v error=%v", info, err)
	}
}

func TestQQOVOSessionSignatureAndConcurrentIsolation(t *testing.T) {
	var bootstrapCalls, playerCalls, mediaCalls atomic.Int32
	client := &http.Client{Transport: jbsouRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/api/session/bootstrap":
			bootstrapCalls.Add(1)
			var data struct {
				DeviceID string `json:"deviceId"`
			}
			if err := json.NewDecoder(req.Body).Decode(&data); err != nil || len(data.DeviceID) != 36 || req.Header.Get("Cookie") != "" {
				t.Error("invalid or shared anonymous session")
			}
			body, _ := json.Marshal(map[string]string{"apiSignKey": "key-" + data.DeviceID})
			resp := sourceTestResponse(req, 200, string(body))
			resp.Header.Set("Set-Cookie", "sid="+data.DeviceID+"; Path=/; Secure; HttpOnly")
			return resp, nil
		case "/api/meting":
			playerCalls.Add(1)
			cookie, err := req.Cookie("sid")
			if err != nil {
				return nil, fmt.Errorf("missing session cookie")
			}
			query := req.URL.Query()
			if (query.Get("server") != "tencent" && query.Get("server") != "kugou") || query.Get("quality") != "lossless" || query.Get("type") != "url" {
				t.Error("unexpected signed query")
			}
			payload := "GET\n/api/meting\n" + query.Encode() + "\n\n" + req.Header.Get("X-OM-Ts") + "\n" + req.Header.Get("X-OM-Nonce")
			mac := hmac.New(sha256.New, []byte("key-"+cookie.Value))
			_, _ = mac.Write([]byte(payload))
			want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
			if req.Header.Get("X-OM-Sign") != want || len(req.Header.Get("X-OM-Nonce")) != 36 {
				t.Error("wrong session key or canonical signature")
			}
			return sourceTestResponse(req, 200, `{"url":"https://aqqmusic.tc.qq.com/F000track.flac?token=secret"}`), nil
		default:
			mediaCalls.Add(1)
			return sourceTestAudio(t, req, "flac"), nil
		}
	})}
	p, err := newQQOVOProvider("https://provider.test", time.Second, client)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			platformName, id := "qqmusic", "000gt6Y92Dm4YQ"
			if i%2 == 0 {
				platformName, id = "kugou", "4BE1D1AF233AA8519099FCDFAA0E205D"
			}
			info, err := p.Resolve(t.Context(), platformName, id, platform.QualityHigh)
			if err != nil || info == nil || info.Format != "flac" {
				t.Errorf("resolve failed: %v", err)
			}
		}()
	}
	wg.Wait()
	if bootstrapCalls.Load() != 12 || playerCalls.Load() != 12 || mediaCalls.Load() != 12 {
		t.Fatalf("unexpected calls: %d %d %d", bootstrapCalls.Load(), playerCalls.Load(), mediaCalls.Load())
	}
}

func TestDirectSourceMediaPolicy(t *testing.T) {
	for _, tc := range []struct {
		raw     string
		allowed bool
	}{
		{"https://aqqmusic.tc.qq.com/F000a.flac", true},
		{"https://isure6.stream.qqmusic.qq.com/M800a.mp3", true},
		{"https://kw-er.kuwo.cn/a.flac", true},
		{"https://car-er.kuwo.cn/a.flac", true},
		{"https://fsandroid.kugou.com/a.flac", true},
		{"http://fs.youthandroid2.kugou.com/a.flac", true},
		{"http://fs.youthandroid2.kugou.com:80/a.flac", true},
		{"http://fsandroid.kugou.com/a.flac", false},
		{"http://car-er.kuwo.cn/a.flac", false},
		{"https://aqqmusic.tc.qq.com.evil.test/a.mp3", false},
		{"http://fs.youthandroid2.kugou.com.evil.test/a.flac", false},
		{"https://user:pass@aqqmusic.tc.qq.com/a.mp3", false},
		{"https://aqqmusic.tc.qq.com:8443/a.mp3", false},
		{"http://fs.youthandroid2.kugou.com:8080/a.flac", false},
		{"https://127.0.0.1/a.mp3", false},
		{"file:///etc/passwd", false},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			u, _ := url.Parse(tc.raw)
			if got := isDirectSourceMediaURL(u); got != tc.allowed {
				t.Fatalf("allowed=%v want=%v", got, tc.allowed)
			}
		})
	}
	// The new HTTP exception must not loosen the existing JBSou policy.
	u, _ := url.Parse("http://fs.youthandroid2.kugou.com/a.flac")
	if isJBSouMediaURL(u) {
		t.Fatal("JBSou policy changed")
	}
}

func TestSourceJSONRejectsFailuresAndRedactsRequestURL(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"HTML", "<html>error</html>", 200},
		{"malformed", "{broken}", 200},
		{"trailing garbage", `{"url":"x"}<script>`, 200},
		{"too large", strings.Repeat(" ", maxJBSouBodyBytes+1), 200},
		{"HTTP error", `{}`, 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &http.Client{Transport: jbsouRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				return sourceTestResponse(req, tc.status, tc.body), nil
			})}
			req, _ := http.NewRequestWithContext(t.Context(), "GET", "https://provider.test/?token=secret", nil)
			var value any
			if err := decodeSourceJSON(client, req, &value); err == nil || strings.Contains(err.Error(), "secret") {
				t.Fatalf("error=%v", err)
			}
		})
	}
	client := &http.Client{Transport: jbsouRoundTripFunc(func(req *http.Request) (*http.Response, error) { return nil, context.Canceled })}
	req, _ := http.NewRequestWithContext(t.Context(), "GET", "https://provider.test/?token=secret", nil)
	var value any
	err := decodeSourceJSON(client, req, &value)
	if !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "secret") {
		t.Fatalf("lost cancellation or leaked URL: %v", err)
	}
}

func TestDirectSourceBlocksRedirectsAndBadMedia(t *testing.T) {
	for _, tc := range []struct {
		name, endpoint, redirect, body string
		status                         int
	}{
		{"API credentials redirect", "https://provider.test/api", "https://aqqmusic.tc.qq.com/leak", "", 302},
		{"media redirect to private address", "https://aqqmusic.tc.qq.com/a.mp3", "http://127.0.0.1/", "", 302},
		{"HTML media", "https://aqqmusic.tc.qq.com/a.mp3", "", "<html>failed</html>", 200},
		{"false lossless", "https://aqqmusic.tc.qq.com/a.flac", "", "ID3-not-flac", 200},
		{"unknown media size", "https://aqqmusic.tc.qq.com/a.mp3", "", "ID3", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			client := &http.Client{Transport: jbsouRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				resp := sourceTestResponse(req, tc.status, tc.body)
				if tc.redirect != "" {
					resp.Header.Set("Location", tc.redirect)
				}
				if tc.name != "unknown media size" {
					resp.ContentLength = 1000
				}
				return resp, nil
			})}
			h, _ := newDirectSourceHTTP("https://provider.test", time.Second, client)
			var err error
			if strings.Contains(tc.name, "API") {
				req, _ := http.NewRequestWithContext(t.Context(), "GET", tc.endpoint, nil)
				req.Header.Set("X-OM-Sign", "secret")
				var v any
				err = decodeSourceJSON(h.api, req, &v)
			} else {
				_, err = h.downloadInfo(t.Context(), tc.endpoint)
			}
			if err == nil || calls != 1 {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestNewSourcesRejectUnsupportedPlatformAndInvalidID(t *testing.T) {
	client := &http.Client{Transport: jbsouRoundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Error("invalid input triggered network request")
		return nil, errors.New("unexpected request")
	})}
	qqovo, _ := newQQOVOProvider("https://provider.test", time.Second, client)
	lzm, _ := newLZMHHHProvider("https://provider.test", time.Second, client)
	nx, _ := newNXINXZProvider("https://provider.test", time.Second, client)
	for _, p := range []provider{qqovo, lzm, nx} {
		for _, platformName := range []string{"qqmusic", "kugou", "kuwo", "spotify"} {
			if _, err := p.Resolve(t.Context(), platformName, "../bad?id=1", platform.QualityHigh); err == nil {
				t.Errorf("%s accepted invalid input", p.Name())
			}
		}
	}
}

func TestNewSourcesRegisteredInConfiguredOrder(t *testing.T) {
	names := []string{"qqovo", "lzmhhh", "nxinxz", "jbsou"}
	chain, err := NewChain(names, time.Second, nil)
	if err != nil || !reflect.DeepEqual(chain.ProviderNames(), names) {
		t.Fatalf("chain=%v error=%v", chain, err)
	}
}
