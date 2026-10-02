package migu

import (
	"context"
	"crypto/x509"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"

	"github.com/liuran001/MusicBot-Go/plugins/musiclib/internal/model"
)

func wrappedMiguJSON(body string) []byte {
	const key = "Jk8qzuePiJ1qE3mDYhLQ3T73DtDoAhLP"
	result := []byte{0xab, 0xcd, 1, 19}
	for i, b := range []byte(body) {
		result = append(result, b-19+key[i%len(key)])
	}
	return result
}

func TestDecodeMiguEnvelope(t *testing.T) {
	for _, body := range [][]byte{[]byte(`{"code":"000000"}`), wrappedMiguJSON(`{"code":"000000"}`)} {
		decoded, err := decodeMiguResponse(body)
		if err != nil || string(decoded) != `{"code":"000000"}` {
			t.Fatalf("decoded=%q err=%v", decoded, err)
		}
	}
	for _, body := range [][]byte{{0xab, 0xcd}, {0xab, 0xcd, 2, 0, 'x'}, {0xab, 0xcd, 1, 0}} {
		if _, err := decodeMiguResponse(body); err == nil {
			t.Fatal("invalid envelope accepted")
		}
	}
}

func TestAnonymousListenFallbacks(t *testing.T) {
	for _, target := range []string{"/MIGUM3.0/strategy/pc/listen/v1.0", "/strategy/pc/listen/v2.0", "/strategy/listen-url/h5/v2.4"} {
		t.Run(target, func(t *testing.T) {
			var paths []string
			client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				paths = append(paths, req.URL.Path)
				if req.Header.Get("Cookie") != "" || req.Header.Get("pacmtoken") != "" {
					t.Fatal("anonymous request has credentials")
				}
				if req.URL.Query().Get("contentId") != "123" || req.URL.Query().Get("toneFlag") != "PQ" {
					t.Fatalf("bad params: %v", req.URL.Query())
				}
				if req.URL.Path != target {
					return listenURLResponse(req, "", ""), nil
				}
				if strings.Contains(target, "v2.0") && (len(req.Header.Get("deviceId")) != 32 || req.Header.Get("signature") != "1" || !req.URL.Query().Has("scene")) {
					t.Fatal("PC v2 profile missing")
				}
				if strings.Contains(target, "h5") && (req.URL.Query().Get("lowerQualityContentId") != "123" || req.Header.Get("Referer") != "https://y.migu.cn/") {
					t.Fatal("H5 profile missing")
				}
				r := listenURLResponse(req, "https://cdn.example/song.mp3", "PQ")
				body, _ := io.ReadAll(r.Body)
				r.Body = io.NopCloser(strings.NewReader(string(wrappedMiguJSON(string(body)))))
				return r, nil
			})}
			reply, err := New(context.Background(), "", client).fetchListenInfo("123", "cp", "song", "album", "2", "PQ")
			if err != nil || reply.Data.URL != "https://cdn.example/song.mp3" {
				t.Fatalf("reply=%+v err=%v", reply, err)
			}
			if paths[len(paths)-1] != target {
				t.Fatalf("routes=%v", paths)
			}
		})
	}
}

func TestNewListenFallbacksDoNotRunForAccountOrHighTone(t *testing.T) {
	for _, tc := range []struct{ cookie, tone string }{{"pacmtoken=account", "PQ"}, {"", "ZQ"}, {"", "ZQ24"}} {
		var calls int
		client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			if strings.Contains(req.URL.Path, "/strategy/pc/listen/v2") || strings.Contains(req.URL.Path, "/h5/") {
				t.Fatal("new PQ fallback escaped scope")
			}
			return listenURLResponse(req, "", ""), nil
		})}
		_, err := New(context.Background(), tc.cookie, client).fetchListenInfo("123", "", "", "", "2", tc.tone)
		if err != nil || calls != 2 {
			t.Fatalf("calls=%d err=%v", calls, err)
		}
	}
}

func TestHighToneReplyWinsOverH5PQ(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == "cdn.example" {
			r := audioProbeResponse(req)
			if strings.HasSuffix(req.URL.Path, ".flac") {
				r.Body = io.NopCloser(strings.NewReader("fLaCdata"))
			}
			return r, nil
		}
		if req.URL.Query().Get("toneFlag") == "ZQ24" {
			return listenURLResponse(req, "https://cdn.example/high.flac", "ZQ24"), nil
		}
		if strings.Contains(req.URL.Path, "/h5/") {
			return listenURLResponse(req, "https://cdn.example/low.mp3", "PQ"), nil
		}
		return listenURLResponse(req, "", ""), nil
	})}
	song := &model.Song{Source: "migu", ID: "123|2|ZQ"}
	got, err := New(context.Background(), "", client).GetDownloadURL(song)
	if err != nil || got != "https://cdn.example/high.flac" || song.Ext != "flac" {
		t.Fatalf("url=%q song=%+v err=%v", got, song, err)
	}
}

func TestTrialHintRequiresFullCatalogSize(t *testing.T) {
	for _, tc := range []struct {
		name, total, size string
		ok                bool
	}{
		{"complete", "3200000", "3200000", true},
		{"short", "480000", "3200000", false},
		{"unknown catalog", "3200000", "", false},
		{"unknown CDN", "", "3200000", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.Host == "cdn.example" {
					r := audioProbeResponse(req)
					if tc.total != "" {
						r.Header.Set("Content-Range", "bytes 0-63/"+tc.total)
					}
					return r, nil
				}
				if strings.Contains(req.URL.Path, "/h5/") {
					r := listenURLResponse(req, "https://cdn.example/song.mp3", "PQ")
					r.Body = io.NopCloser(strings.NewReader(`{"code":"000000","data":{"url":"https://cdn.example/song.mp3","formatType":"PQ","dialogInfo":{"text":"\u8bd5\u542c"}}}`))
					return r, nil
				}
				return listenURLResponse(req, "", ""), nil
			})}
			song := &model.Song{Source: "migu", ID: "123|2|PQ", Extra: map[string]string{"size_PQ": tc.size}}
			_, err := New(context.Background(), "", client).GetDownloadURL(song)
			if tc.ok && err != nil {
				t.Fatal(err)
			}
			if !tc.ok && !errors.Is(err, model.ErrPreviewOnly) {
				t.Fatalf("want preview rejection: %v", err)
			}
		})
	}
}

func TestCandidateFormatAndPreviewValidation(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) { return audioProbeResponse(req), nil })}
	m := New(context.Background(), "", client)
	if valid, _ := m.downloadInfoValid(miguDownloadCandidate{url: "https://cdn.example/song.flac", format: "SQ", ext: "flac"}); valid {
		t.Fatal("MP3 was labeled as FLAC")
	}
	if _, ok := newMiguDownloadCandidate("https://cdn.example/song.mp3", "SQ"); ok {
		t.Fatal("MP3 path accepted as SQ")
	}
	for _, u := range []string{"https://cdn.example/wav_3d_60s/song.wav", "https://cdn.example/30s_song.mp3", "https://cdn.example/\u8bd5\u542c/song.mp3"} {
		if !miguPreviewURL(u) {
			t.Fatalf("preview not rejected: %s", u)
		}
	}
}

func TestCanceledListenStopsFallback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) { calls++; cancel(); return nil, context.Canceled })}
	_, err := New(ctx, "", client).fetchListenInfo("123", "", "", "", "2", "PQ")
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

func TestAnonymousNewRoutesDropCookieJar(t *testing.T) {
	jar, _ := cookiejar.New(nil)
	base, _ := url.Parse("https://app.c.nf.migu.cn")
	jar.SetCookies(base, []*http.Cookie{{Name: "pacmtoken", Value: "jar-account", Domain: ".migu.cn", Path: "/"}})
	seen := false
	client := &http.Client{Jar: jar, Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if strings.Contains(req.URL.Path, "/strategy/pc/listen/v2") {
			seen = true
			if req.Header.Get("Cookie") != "" || req.Header.Get("pacmtoken") != "" {
				t.Fatal("anonymous fallback leaked credentials")
			}
			return listenURLResponse(req, "https://cdn.example/song.mp3", "PQ"), nil
		}
		return listenURLResponse(req, "", ""), nil
	})}
	_, err := New(context.Background(), "device=old", client).fetchListenInfo("123", "", "", "", "2", "PQ")
	if err != nil || !seen {
		t.Fatalf("seen=%v err=%v", seen, err)
	}
	if client.Jar == nil {
		t.Fatal("shared client was mutated")
	}
}

func TestCertificateErrorStopsTonesAndProbes(t *testing.T) {
	for _, failProbe := range []bool{false, true} {
		calls := 0
		probeCalls := 0
		client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			if !failProbe {
				return nil, x509.UnknownAuthorityError{}
			}
			if req.URL.Host == "cdn.example" {
				probeCalls++
				return nil, x509.UnknownAuthorityError{}
			}
			return listenURLResponse(req, "https://cdn.example/song.mp3", "PQ"), nil
		})}
		song := &model.Song{Source: "migu", ID: "123|2|PQ"}
		_, err := New(context.Background(), "", client).GetDownloadURL(song)
		var cert x509.UnknownAuthorityError
		if !errors.As(err, &cert) {
			t.Fatalf("certificate error swallowed: %v", err)
		}
		if !failProbe && calls != 1 {
			t.Fatalf("tone fallback continued: %d", calls)
		}
		if failProbe && probeCalls != 1 {
			t.Fatalf("probe fallback continued: %d", probeCalls)
		}
	}
}

func TestCatalogRetainsEachQualitySize(t *testing.T) {
	song := (&Migu{}).convertItemToSong(MiguSongItem{ContentID: "123", RateFormats: []miguRateFormat{
		{FormatType: "PQ", Size: "3200000", ResourceType: "2"},
		{FormatType: "ZQ24", Size: "30000000", ResourceType: "2"},
	}})
	if song == nil || miguCatalogSize(song, "PQ", "ZQ24") != 3200000 || miguCatalogSize(song, "ZQ", "ZQ24") != 30000000 {
		t.Fatalf("missing format sizes: %+v", song)
	}
}

func TestMP3ProbeRejectsADTSAndInvalidMPEGHeaders(t *testing.T) {
	for _, prefix := range [][]byte{
		{0xff, 0xf1, 0x50, 0x80}, {0xff, 0xf9, 0x50, 0x80},
		{0xff, 0xeb, 0x90, 0}, {0xff, 0xfd, 0x90, 0},
		{0xff, 0xfb, 0xf0, 0}, {0xff, 0xfb, 0x9c, 0},
	} {
		client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			r := audioProbeResponse(req)
			r.Body = io.NopCloser(strings.NewReader(string(prefix)))
			return r, nil
		})}
		valid, err := New(context.Background(), "", client).downloadInfoValid(miguDownloadCandidate{url: "https://cdn.example/song.mp3", format: "PQ", ext: "mp3"})
		if err != nil || valid {
			t.Fatalf("invalid MP3 prefix %x accepted: valid=%v err=%v", prefix, valid, err)
		}
	}
	for _, prefix := range [][]byte{{0xff, 0xfb, 0x90, 0}, {0xff, 0xf3, 0x80, 0}, {0xff, 0xe3, 0x80, 0}} {
		if !isMiguMP3Frame(prefix) {
			t.Fatalf("MPEG Layer III rejected: %x", prefix)
		}
	}
}
