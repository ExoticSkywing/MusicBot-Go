package thirdparty

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/liuran001/MusicBot-Go/bot/platform"
)

func TestJBSouResolveQQTrack(t *testing.T) {
	const trackID = "002miT7m27YYe9"
	var homeCalls atomic.Int32
	var lookupCalls atomic.Int32
	var mediaCalls atomic.Int32

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/":
			homeCalls.Add(1)
			http.SetCookie(w, &http.Cookie{Name: "PHPSESSID", Value: "test-session", Path: "/"})
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && r.URL.Path == "/":
			lookupCalls.Add(1)
			cookie, err := r.Cookie("PHPSESSID")
			if err != nil || cookie.Value != "test-session" {
				http.Error(w, "session cookie missing", http.StatusForbidden)
				return
			}
			if err := r.ParseForm(); err != nil || r.Form.Get("input") != trackID || r.Form.Get("filter") != "id" || r.Form.Get("type") != "qq" {
				http.Error(w, "unexpected form", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(jbsouResponse{
				Code: http.StatusOK,
				Data: []jbsouTrack{{SongID: trackID, URL: "/api.php?get=url&type=qq&id=" + trackID}},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api.php":
			http.Redirect(w, r, "/media/M800test.mp3?token=secret", http.StatusFound)
		case r.Method == http.MethodGet && r.URL.Path == "/media/M800test.mp3":
			mediaCalls.Add(1)
			if r.Header.Get("Range") != "bytes=0-1023" {
				http.Error(w, "range missing", http.StatusBadRequest)
				return
			}
			// QQ's CDN sometimes labels valid MP3 files as form data. The
			// resolver verifies the bytes instead of trusting this MIME value.
			w.Header().Set("Content-Type", "application/x-www-form-urlencoded")
			w.Header().Set("Content-Range", "bytes 0-1023/11961155")
			w.Header().Set("Content-Length", "1024")
			w.WriteHeader(http.StatusPartialContent)
			probe := make([]byte, 1024)
			copy(probe, "ID3")
			_, _ = w.Write(probe)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	baseURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	allowTestMedia := func(candidate *url.URL) bool {
		return candidate != nil && candidate.Host == baseURL.Host && strings.HasPrefix(candidate.Path, "/media/")
	}
	provider, err := newJBSouProvider(server.URL+"/", 2*time.Second, server.Client(), allowTestMedia)
	if err != nil {
		t.Fatalf("newJBSouProvider: %v", err)
	}
	info, err := provider.Resolve(t.Context(), "qqmusic", trackID, platform.QualityHiRes)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if info.Format != "mp3" || info.Quality != platform.QualityHigh || info.Bitrate != 320 {
		t.Fatalf("unexpected media classification: %+v", info)
	}
	if info.Size != 11961155 {
		t.Fatalf("size = %d, want 11961155", info.Size)
	}
	if !strings.Contains(info.URL, "/media/M800test.mp3") {
		t.Fatalf("URL = %q, want resolved media URL", info.URL)
	}
	if err := info.ValidateURL(info.URL); err != nil {
		t.Fatalf("ValidateURL: %v", err)
	}
	if homeCalls.Load() != 1 || lookupCalls.Load() != 1 || mediaCalls.Load() != 1 {
		t.Fatalf("calls home=%d lookup=%d media=%d, want 1 each", homeCalls.Load(), lookupCalls.Load(), mediaCalls.Load())
	}
}

func TestJBSouResolveKugouTrack(t *testing.T) {
	const (
		inputTrackID = "055a804b8a3ccc05240d08f8ff1f7de8"
		jbsouTrackID = "055A804B8A3CCC05240D08F8FF1F7DE8"
	)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/":
			http.SetCookie(w, &http.Cookie{Name: "PHPSESSID", Value: "kugou-session", Path: "/"})
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && r.URL.Path == "/":
			cookie, err := r.Cookie("PHPSESSID")
			if err != nil || cookie.Value != "kugou-session" {
				http.Error(w, "session cookie missing", http.StatusForbidden)
				return
			}
			if err := r.ParseForm(); err != nil || r.Form.Get("input") != jbsouTrackID || r.Form.Get("filter") != "id" || r.Form.Get("type") != "kugou" {
				http.Error(w, "unexpected form", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(jbsouResponse{
				Code: http.StatusOK,
				Data: []jbsouTrack{{SongID: jbsouTrackID, URL: "/api.php?get=url&type=kg&id=" + jbsouTrackID}},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api.php":
			http.Redirect(w, r, "/media/full_ap1005_qu320_track.mp3", http.StatusFound)
		case r.Method == http.MethodGet && r.URL.Path == "/media/full_ap1005_qu320_track.mp3":
			w.Header().Set("Content-Range", "bytes 0-1023/8266389")
			w.Header().Set("Content-Length", "1024")
			w.WriteHeader(http.StatusPartialContent)
			probe := make([]byte, 1024)
			copy(probe, "ID3")
			_, _ = w.Write(probe)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	baseURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	allowTestMedia := func(candidate *url.URL) bool {
		return candidate != nil && candidate.Host == baseURL.Host && strings.HasPrefix(candidate.Path, "/media/")
	}
	provider, err := newJBSouProvider(server.URL+"/", 2*time.Second, server.Client(), allowTestMedia)
	if err != nil {
		t.Fatalf("newJBSouProvider: %v", err)
	}
	info, err := provider.Resolve(t.Context(), "kugou", inputTrackID, platform.QualityLossless)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if info.Format != "mp3" || info.Quality != platform.QualityHigh || info.Bitrate != 320 {
		t.Fatalf("unexpected media classification: %+v", info)
	}
	if info.Size != 8266389 {
		t.Fatalf("size = %d, want 8266389", info.Size)
	}
	if err := info.ValidateURL(info.URL); err != nil {
		t.Fatalf("ValidateURL: %v", err)
	}
}

func TestJBSouResolveKuwoTrack(t *testing.T) {
	const trackID = "41378936"
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/":
			http.SetCookie(w, &http.Cookie{Name: "PHPSESSID", Value: "kuwo-session", Path: "/"})
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && r.URL.Path == "/":
			cookie, err := r.Cookie("PHPSESSID")
			if err != nil || cookie.Value != "kuwo-session" {
				http.Error(w, "session cookie missing", http.StatusForbidden)
				return
			}
			if err := r.ParseForm(); err != nil || r.Form.Get("input") != trackID || r.Form.Get("filter") != "id" || r.Form.Get("type") != "kuwo" {
				http.Error(w, "unexpected form", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(jbsouResponse{
				Code: http.StatusOK,
				Data: []jbsouTrack{{SongID: trackID, URL: "/api.php?get=url&type=kw&id=" + trackID}},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api.php":
			http.Redirect(w, r, "/media/M8000009WImX34uQMp.mp3", http.StatusFound)
		case r.Method == http.MethodGet && r.URL.Path == "/media/M8000009WImX34uQMp.mp3":
			w.Header().Set("Content-Type", "audio/mpeg")
			w.Header().Set("Content-Range", "bytes 0-1023/8525534")
			w.Header().Set("Content-Length", "1024")
			w.WriteHeader(http.StatusPartialContent)
			probe := make([]byte, 1024)
			copy(probe, "ID3")
			_, _ = w.Write(probe)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	baseURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	allowTestMedia := func(candidate *url.URL) bool {
		return candidate != nil && candidate.Host == baseURL.Host && strings.HasPrefix(candidate.Path, "/media/")
	}
	provider, err := newJBSouProvider(server.URL+"/", 2*time.Second, server.Client(), allowTestMedia)
	if err != nil {
		t.Fatalf("newJBSouProvider: %v", err)
	}
	info, err := provider.Resolve(t.Context(), "kuwo", trackID, platform.QualityLossless)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if info.Format != "mp3" || info.Quality != platform.QualityHigh || info.Bitrate != 320 {
		t.Fatalf("unexpected media classification: %+v", info)
	}
	if info.Size != 8525534 {
		t.Fatalf("size = %d, want 8525534", info.Size)
	}
	if err := info.ValidateURL(info.URL); err != nil {
		t.Fatalf("ValidateURL: %v", err)
	}
}

func TestLooksLikeAudio(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want bool
	}{
		{name: "mp3 id3", data: []byte("ID3\x03\x00"), want: true},
		{name: "mp3 frame", data: []byte{0xff, 0xfb, 0x90, 0x64}, want: true},
		{name: "flac", data: []byte("fLaCdata"), want: true},
		{name: "ogg", data: []byte("OggSdata"), want: true},
		{name: "m4a", data: []byte{0, 0, 0, 0x18, 'f', 't', 'y', 'p', 'M', '4', 'A', ' '}, want: true},
		{name: "html", data: []byte("<html>forbidden</html>"), want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := looksLikeAudio(test.data); got != test.want {
				t.Fatalf("looksLikeAudio() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestJBSouRequiresExactSongID(t *testing.T) {
	const trackID = "wanted"
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			http.SetCookie(w, &http.Cookie{Name: "PHPSESSID", Value: "test", Path: "/"})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jbsouResponse{
			Code: http.StatusOK,
			Data: []jbsouTrack{{SongID: "different", URL: "/api.php?get=url"}},
		})
	}))
	defer server.Close()

	provider, err := newJBSouProvider(server.URL+"/", time.Second, server.Client(), func(*url.URL) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Resolve(t.Context(), "qqmusic", trackID, platform.QualityHigh); err == nil {
		t.Fatal("expected an exact-songmid mismatch error")
	}
}

func TestQQMusicMediaURLAllowlist(t *testing.T) {
	tests := []struct {
		raw  string
		want bool
	}{
		{raw: "https://isure6.stream.qqmusic.qq.com/file.mp3", want: true},
		{raw: "https://aqqmusic.tc.qq.com/file.mp3", want: true},
		{raw: "http://isure6.stream.qqmusic.qq.com/file.mp3", want: false},
		{raw: "https://qqmusic.qq.com.evil.example/file.mp3", want: false},
		{raw: "https://example.com/file.mp3", want: false},
	}
	for _, test := range tests {
		parsed, err := url.Parse(test.raw)
		if err != nil {
			t.Fatal(err)
		}
		if got := isQQMusicMediaURL(parsed); got != test.want {
			t.Fatalf("isQQMusicMediaURL(%q) = %v, want %v", test.raw, got, test.want)
		}
	}
}

func TestKugouMediaURLAllowlist(t *testing.T) {
	tests := []struct {
		raw  string
		want bool
	}{
		{raw: "https://fsandroid.kugou.com/path/track.mp3", want: true},
		{raw: "https://trackercdn.kugou.com/path/track.flac", want: true},
		{raw: "http://fsandroid.kugou.com/path/track.mp3", want: false},
		{raw: "https://kugou.com.evil.example/path/track.mp3", want: false},
		{raw: "https://example.com/path/track.mp3", want: false},
	}
	for _, test := range tests {
		parsed, err := url.Parse(test.raw)
		if err != nil {
			t.Fatal(err)
		}
		if got := isKugouMediaURL(parsed); got != test.want {
			t.Fatalf("isKugouMediaURL(%q) = %v, want %v", test.raw, got, test.want)
		}
	}
}

func TestKuwoMediaURLAllowlist(t *testing.T) {
	tests := []struct {
		raw  string
		want bool
	}{
		{raw: "https://kw-er.kuwo.cn/path/M800.mp3", want: true},
		{raw: "https://er-sycdn.kuwo.cn/path/M800.mp3", want: true},
		{raw: "http://kw-er.kuwo.cn/path/M800.mp3", want: false},
		{raw: "https://kw-er.kuwo.cn.evil.example/path/M800.mp3", want: false},
		{raw: "https://evil.kuwo.cn/path/M800.mp3", want: false},
	}
	for _, test := range tests {
		parsed, err := url.Parse(test.raw)
		if err != nil {
			t.Fatal(err)
		}
		if got := isKuwoMediaURL(parsed); got != test.want {
			t.Fatalf("isKuwoMediaURL(%q) = %v, want %v", test.raw, got, test.want)
		}
	}
}

func TestNormalizeKugouTrackID(t *testing.T) {
	if got := normalizeKugouTrackID("055a804b8a3ccc05240d08f8ff1f7de8"); got != "055A804B8A3CCC05240D08F8FF1F7DE8" {
		t.Fatalf("normalizeKugouTrackID() = %q", got)
	}
	if got := normalizeKugouTrackID("not-a-hash"); got != "" {
		t.Fatalf("invalid hash normalized to %q", got)
	}
}
