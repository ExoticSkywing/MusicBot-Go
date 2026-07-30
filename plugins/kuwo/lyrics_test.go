package kuwo

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/liuran001/MusicBot-Go/bot/platform"
	"golang.org/x/text/encoding/simplifiedchinese"
)

const fixedWordLyricResponseBase64 = "dHA9Y29udGVudA0KcGF0aD1maXh0dXJlDQpscmN4PTENCg0KeJwVy0ELgjAchvEP5CFFCzp4qNHfUlSc+m56myOLkmwFTfv02e2Bh9/pEhSRbSHdgecR3bL9WMg5pQN/d8zugkakrrwPPTAdpbeOcxo0x/Nc4tO/PKjlxTVNbd2ZWYBrgS2rCKjNo2v81FlcJb1JAtAZZbpcoqBr/3dA4nwTo/yNCkazUg4xLmwY/gCBODGK"

type lyricRoundTripFunc func(*http.Request) (*http.Response, error)

func (f lyricRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func lyricHTTPResponse(req *http.Request, status int, body []byte) *http.Response {
	return &http.Response{
		StatusCode:    status,
		Header:        make(http.Header),
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
		Request:       req,
	}
}

func xorWordLyric(data []byte) []byte {
	key := []byte("yeelion")
	result := make([]byte, len(data))
	for i, value := range data {
		result[i] = value ^ key[i%len(key)]
	}
	return result
}

func encodedWordLyricPayload(t *testing.T, lrc string) []byte {
	t.Helper()
	encoded, err := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte(lrc))
	if err != nil {
		t.Fatal(err)
	}
	return []byte(base64.StdEncoding.EncodeToString(xorWordLyric(encoded)))
}

func wrapWordLyricPayload(t *testing.T, payload []byte) []byte {
	t.Helper()
	var compressed bytes.Buffer
	writer := zlib.NewWriter(&compressed)
	if _, err := writer.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return append([]byte("tp=content\r\npath=test\r\nlrcx=1\r\n\r\n"), compressed.Bytes()...)
}

func encodedWordLyricResponse(t *testing.T, lrc string) []byte {
	t.Helper()
	return wrapWordLyricPayload(t, encodedWordLyricPayload(t, lrc))
}

func TestBuildWordLyricQueryFixedVectors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		trackID string
		want    string
	}{
		{
			name:    "228908",
			trackID: "228908",
			want:    "DBYAHlReXEpRUEAeCgxVEgAORRgLG0MXCRgaCwoRAB5UAwEaBAkEBhwaXxcAHVReSAsMAVEkOj0wJjpeW1dXSV1DABsMFkRU",
		},
		{
			name:    "41378936",
			trackID: "41378936",
			want:    "DBYAHlReXEpRUEAeCgxVEgAORRgLG0MXCRgaCwoRAB5UAwEaBAkEBhwaXxcAHVReSAsMAVEkOj0wJjpYWFxZQVxWWk8DHBodWF0=",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := buildWordLyricQuery(tt.trackID); got != tt.want {
				t.Fatalf("buildWordLyricQuery(%q) = %q, want %q", tt.trackID, got, tt.want)
			}
		})
	}
}

func TestDecodeWordLyricsFullFixture(t *testing.T) {
	body, err := base64.StdEncoding.DecodeString(fixedWordLyricResponseBase64)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := decodeWordLyrics(body)
	if err != nil {
		t.Fatalf("decodeWordLyrics() = %v", err)
	}
	wantRaw := "[kuwo:104]\r\n[ti:Fixture]\r\n[00:00.000]<0,500>好<500,500>运<1000,500>来\r\n[00:02.000]<2000,1000,0>祝你好运来ḿ"
	if raw != wantRaw {
		t.Fatalf("decoded lyrics = %q, want %q", raw, wantRaw)
	}
	lyrics := parseTimedLyrics(raw)
	if lyrics.Plain != "好运来\n祝你好运来ḿ" {
		t.Fatalf("Plain = %q", lyrics.Plain)
	}
	if len(lyrics.Timestamped) != 2 ||
		lyrics.Timestamped[0].Time != 0 ||
		lyrics.Timestamped[0].Text != "好运来" ||
		lyrics.Timestamped[1].Time != 2*time.Second ||
		lyrics.Timestamped[1].Text != "祝你好运来ḿ" {
		t.Fatalf("Timestamped = %#v", lyrics.Timestamped)
	}
	if lyrics.RawYRC != "" || lyrics.RawQRC != "" || lyrics.RawLYS != "" {
		t.Fatalf("native raw lyrics unexpectedly populated: %#v", lyrics)
	}
}

func TestDecodeGB18030Strict(t *testing.T) {
	t.Run("legal euro and literal replacement rune", func(t *testing.T) {
		got, err := decodeGB18030Strict([]byte{0x80, 0x84, 0x31, 0xA4, 0x37})
		if err != nil || got != "€�" {
			t.Fatalf("decodeGB18030Strict() = (%q, %v)", got, err)
		}
	})

	for _, raw := range [][]byte{
		{0x81},
		{0x81, 0x20},
		{0x81, 0x7F},
		{0xFE, 0xFE},
		{0xFE, 0x30},
		{0xFF},
		{0x81, 0x30, 0x80, 0x30},
		{0x81, 0x30, 0x81, 0x3A},
		{0x84, 0x31, 0xA5, 0x30},
		{0xE3, 0x32, 0x9A, 0x36},
	} {
		raw := append([]byte(nil), raw...)
		t.Run(fmt.Sprintf("%x", raw), func(t *testing.T) {
			if got, err := decodeGB18030Strict(raw); err == nil {
				t.Fatalf("decodeGB18030Strict(%x) = %q, want error", raw, got)
			}
		})
	}
}

func TestDecodeWordLyricsRejectsProtocolCorruption(t *testing.T) {
	validPayload := encodedWordLyricPayload(t, "[00:00.000]line")
	validBody := wrapWordLyricPayload(t, validPayload)
	corruptZlib := append([]byte("tp=content\r\n\r\n"), []byte("not-zlib")...)
	badBase64 := wrapWordLyricPayload(t, []byte("***"))
	badGB := wrapWordLyricPayload(t, []byte(base64.StdEncoding.EncodeToString(xorWordLyric([]byte{0xFE, 0xFE}))))
	for name, body := range map[string][]byte{
		"missing envelope": []byte("tp=content"),
		"wrong envelope":   append([]byte("tp=error\r\n\r\n"), validBody...),
		"corrupt zlib":     corruptZlib,
		"bad base64":       badBase64,
		"bad GB18030":      badGB,
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := decodeWordLyrics(body); err == nil {
				t.Fatalf("decodeWordLyrics() = %q, want error", got)
			}
		})
	}
}

func TestDecodeWordLyricsDecompressedLimit(t *testing.T) {
	base := encodedWordLyricPayload(t, "[00:00.000]line")
	exact := append(append([]byte(nil), base...), bytes.Repeat([]byte(" "), maxDecodedLyricBytes-len(base))...)
	if _, err := decodeWordLyrics(wrapWordLyricPayload(t, exact)); err != nil {
		t.Fatalf("exact limit decode = %v", err)
	}
	tooLarge := append(append([]byte(nil), exact...), ' ')
	if _, err := decodeWordLyrics(wrapWordLyricPayload(t, tooLarge)); err == nil {
		t.Fatal("limit + 1 decode succeeded")
	}
}

func TestParseTimedLyricsOffsetWordTagsAndStableSort(t *testing.T) {
	raw := strings.Join([]string{
		"[ti:Metadata]",
		"[00:01.000][00:02.50]<-1,2>First",
		"[offset:-1500]",
		"[00:02.500]<1,-2,3>Second",
		"[00:02.500]<not,time>",
		"[offset:not-a-number]",
		"[999999999999999999999:00.000]overflow",
		"[-00:01.000]negative",
	}, "\r\n")
	got := parseTimedLyrics(raw)
	if got.Plain != "First\nSecond\n<not,time>" {
		t.Fatalf("Plain = %q", got.Plain)
	}
	want := []platform.LyricLine{
		{Time: 0, Text: "First"},
		{Time: time.Second, Text: "First"},
		{Time: time.Second, Text: "Second"},
		{Time: time.Second, Text: "<not,time>"},
	}
	if fmt.Sprint(got.Timestamped) != fmt.Sprint(want) {
		t.Fatalf("Timestamped = %#v, want %#v", got.Timestamped, want)
	}
}

func TestParseMobileLyricsIdentityRowsAndRounding(t *testing.T) {
	body := []byte(`{
		"status":"200",
		"data":{
			"songinfo":{"id":41378936,"musicrId":null,"unknown":true},
			"lrclist":[
				{"time":"93.479996","lineLyric":"rounded"},
				{"time":null,"lineLyric":"skip"},
				{"time":"NaN","lineLyric":"skip"},
				{"time":"Inf","lineLyric":"skip"},
				{"time":"1e100","lineLyric":"skip"},
				{"time":-1,"lineLyric":"skip"},
				{"time":2,"lineLyric":null},
				{"lineLyric":"missing time"},
				{"time":3,"lineLyric":true},
				{"time":{"bad":true},"lineLyric":"composite time"},
				{"time":1,"lineLyric":123},
				{"time":"1.0","lineLyric":"tie"}
			]
		},
		"unknown":{"nested":true}
	}`)
	got, err := parseMobileLyrics(body, "41378936")
	if err != nil {
		t.Fatalf("parseMobileLyrics() = %v", err)
	}
	want := []platform.LyricLine{
		{Time: time.Second, Text: "123"},
		{Time: time.Second, Text: "tie"},
		{Time: 93480 * time.Millisecond, Text: "rounded"},
	}
	if fmt.Sprint(got.Timestamped) != fmt.Sprint(want) || got.Plain != "123\ntie\nrounded" {
		t.Fatalf("lyrics = %#v, want lines %#v", got, want)
	}
}

func TestParseMobileLyricsRejectsEnvelopeAndIdentityErrors(t *testing.T) {
	for _, tt := range []struct {
		name string
		body string
	}{
		{name: "bad status", body: `{"status":301,"data":{"songinfo":{"id":"41378936"},"lrclist":[{"time":0,"lineLyric":"x"}]}}`},
		{name: "null status", body: `{"status":null,"data":{"songinfo":{"id":"41378936"},"lrclist":[{"time":0,"lineLyric":"x"}]}}`},
		{name: "missing status", body: `{"data":{"songinfo":{"id":"41378936"},"lrclist":[{"time":0,"lineLyric":"x"}]}}`},
		{name: "missing identity", body: `{"status":200,"data":{"songinfo":{},"lrclist":[{"time":0,"lineLyric":"x"}]}}`},
		{name: "all null identity", body: `{"status":200,"data":{"songinfo":{"id":null,"musicrId":null},"lrclist":[{"time":0,"lineLyric":"x"}]}}`},
		{name: "one mismatch", body: `{"status":200,"data":{"songinfo":{"id":"41378936","musicrId":"MUSIC_228908"},"lrclist":[{"time":0,"lineLyric":"x"}]}}`},
		{name: "one malformed", body: `{"status":200,"data":{"songinfo":{"id":"41378936","musicrId":true},"lrclist":[{"time":0,"lineLyric":"x"}]}}`},
		{name: "one composite", body: `{"status":200,"data":{"songinfo":{"id":"41378936","musicrId":{"bad":true}},"lrclist":[{"time":0,"lineLyric":"x"}]}}`},
		{name: "all rows invalid", body: `{"status":200,"data":{"songinfo":{"musicrId":"MUSIC_41378936"},"lrclist":[{"time":null,"lineLyric":"x"}]}}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got, err := parseMobileLyrics([]byte(tt.body), "41378936"); !errors.Is(err, platform.ErrUnavailable) {
				t.Fatalf("parseMobileLyrics() = (%#v, %v), want ErrUnavailable", got, err)
			}
		})
	}
}

func TestSessionlessAPIClientSnapshot(t *testing.T) {
	transport := http.DefaultTransport
	redirect := func(*http.Request, []*http.Request) error { return nil }
	client := newClientWithEndpoints(time.Second, nil, kuwoEndpoints{})
	client.apiHTTPClient.Transport = transport
	client.apiHTTPClient.Timeout = 7 * time.Second
	client.apiHTTPClient.CheckRedirect = redirect
	sharedJar := client.apiHTTPClient.Jar

	snapshot := client.sessionlessAPIClient()
	if snapshot == client.apiHTTPClient {
		t.Fatal("sessionless snapshot reused shared client pointer")
	}
	if snapshot.Transport != transport ||
		snapshot.Timeout != 7*time.Second ||
		snapshot.CheckRedirect == nil ||
		snapshot.Jar != nil {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	if client.apiHTTPClient.Jar != sharedJar || sharedJar == nil {
		t.Fatal("shared Jar was mutated")
	}
}

func newLyricsTestClient(t *testing.T, transport http.RoundTripper) *Client {
	t.Helper()
	client := newClientWithEndpoints(time.Second, nil, kuwoEndpoints{
		wordLyric:   "https://word.test/newlyric.lrc",
		mobileLyric: "https://mobile.test/songinfoandlrc",
	})
	client.apiHTTPClient.Transport = transport
	for _, rawURL := range []string{client.endpoints.wordLyric, client.endpoints.mobileLyric} {
		parsed, err := url.Parse(rawURL)
		if err != nil {
			t.Fatal(err)
		}
		client.apiHTTPClient.Jar.SetCookies(parsed, []*http.Cookie{{
			Name:  "session",
			Value: "must-not-leak",
		}})
	}
	return client
}

func validMobileLyricBody() []byte {
	return []byte(`{
		"status":200,
		"data":{
			"songinfo":{"id":"41378936","musicrId":"MUSIC_41378936"},
			"lrclist":[
				{"time":"0.5","lineLyric":"第一句"},
				{"time":"2.0","lineLyric":"第二句"}
			]
		}
	}`)
}

func TestGetLyricsUsesOpaqueWordQueryWithoutSession(t *testing.T) {
	wordBody, err := base64.StdEncoding.DecodeString(fixedWordLyricResponseBase64)
	if err != nil {
		t.Fatal(err)
	}
	var mobileCalls atomic.Int32
	client := newLyricsTestClient(t, lyricRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Header.Get("Cookie") != "" || req.Header.Get("Secret") != "" {
			t.Fatalf("session headers leaked: %#v", req.Header)
		}
		switch req.URL.Host {
		case "word.test":
			wantQuery := "DBYAHlReXEpRUEAeCgxVEgAORRgLG0MXCRgaCwoRAB5UAwEaBAkEBhwaXxcAHVReSAsMAVEkOj0wJjpYWFxZQVxWWk8DHBodWF0="
			if req.URL.RawQuery != wantQuery {
				t.Fatalf("RawQuery = %q, want opaque %q", req.URL.RawQuery, wantQuery)
			}
			if strings.Contains(req.URL.RawQuery, "%3D") {
				t.Fatalf("RawQuery was URL encoded: %q", req.URL.RawQuery)
			}
			return lyricHTTPResponse(req, http.StatusOK, wordBody), nil
		case "mobile.test":
			mobileCalls.Add(1)
			return lyricHTTPResponse(req, http.StatusOK, validMobileLyricBody()), nil
		default:
			return nil, fmt.Errorf("unexpected host %q", req.URL.Host)
		}
	}))

	got, err := client.GetLyrics(context.Background(), "MUSIC_41378936")
	if err != nil {
		t.Fatalf("GetLyrics() = %v", err)
	}
	if got.Plain != "好运来\n祝你好运来ḿ" || len(got.Timestamped) != 2 {
		t.Fatalf("GetLyrics() = %#v", got)
	}
	if mobileCalls.Load() != 0 {
		t.Fatalf("mobile fallback calls = %d", mobileCalls.Load())
	}
	if client.apiHTTPClient.Jar == nil {
		t.Fatal("shared API Jar was mutated")
	}
}

func TestGetLyricsFallsBackToIdentityCheckedMobile(t *testing.T) {
	for _, tt := range []struct {
		name       string
		wordStatus int
		wordBody   []byte
	}{
		{name: "enhanced HTTP failure", wordStatus: http.StatusInternalServerError},
		{name: "enhanced corrupt response", wordStatus: http.StatusOK, wordBody: []byte("bad")},
		{name: "enhanced no timed lines", wordStatus: http.StatusOK, wordBody: encodedWordLyricResponse(t, "[ti:only metadata]")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var mobileCalls atomic.Int32
			client := newLyricsTestClient(t, lyricRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.Header.Get("Cookie") != "" || req.Header.Get("Secret") != "" {
					t.Fatalf("session headers leaked: %#v", req.Header)
				}
				switch req.URL.Host {
				case "word.test":
					return lyricHTTPResponse(req, tt.wordStatus, tt.wordBody), nil
				case "mobile.test":
					mobileCalls.Add(1)
					if req.URL.RawQuery != "musicId=41378936" {
						t.Fatalf("mobile RawQuery = %q", req.URL.RawQuery)
					}
					if len(req.URL.Query()) != 1 || req.URL.Query().Get("httpsStatus") != "" {
						t.Fatalf("mobile query = %#v", req.URL.Query())
					}
					return lyricHTTPResponse(req, http.StatusOK, validMobileLyricBody()), nil
				default:
					return nil, fmt.Errorf("unexpected host %q", req.URL.Host)
				}
			}))

			got, err := client.GetLyrics(context.Background(), "41378936")
			if err != nil {
				t.Fatalf("GetLyrics() = %v", err)
			}
			if got.Plain != "第一句\n第二句" ||
				len(got.Timestamped) != 2 ||
				got.Timestamped[0].Time != 500*time.Millisecond ||
				got.Timestamped[1].Time != 2*time.Second {
				t.Fatalf("mobile lyrics = %#v", got)
			}
			if mobileCalls.Load() != 1 {
				t.Fatalf("mobile calls = %d", mobileCalls.Load())
			}
		})
	}
}

type lyricTrackingBody struct {
	reads  *atomic.Int32
	closes *atomic.Int32
	cancel context.CancelFunc
	err    error
}

func (b *lyricTrackingBody) Read([]byte) (int, error) {
	b.reads.Add(1)
	if b.cancel != nil {
		b.cancel()
	}
	if b.err != nil {
		return 0, b.err
	}
	return 0, io.EOF
}

func (b *lyricTrackingBody) Close() error {
	b.closes.Add(1)
	return nil
}

func TestGetLyricsTerminalFailuresNeverFallback(t *testing.T) {
	t.Run("429 classified before oversized body read", func(t *testing.T) {
		var reads, closes, mobileCalls atomic.Int32
		client := newLyricsTestClient(t, lyricRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.Host == "mobile.test" {
				mobileCalls.Add(1)
			}
			return &http.Response{
				StatusCode: http.StatusTooManyRequests,
				Header:     make(http.Header),
				Body:       &lyricTrackingBody{reads: &reads, closes: &closes},
				Request:    req,
			}, nil
		}))
		got, err := client.GetLyrics(context.Background(), "41378936")
		if got != nil || !errors.Is(err, platform.ErrRateLimited) {
			t.Fatalf("GetLyrics() = (%#v, %v), want ErrRateLimited", got, err)
		}
		if reads.Load() != 0 || closes.Load() != 1 || mobileCalls.Load() != 0 {
			t.Fatalf("reads/closes/mobile = %d/%d/%d", reads.Load(), closes.Load(), mobileCalls.Load())
		}
	})

	t.Run("cancel during enhanced read", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		var reads, closes, mobileCalls atomic.Int32
		client := newLyricsTestClient(t, lyricRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.Host == "mobile.test" {
				mobileCalls.Add(1)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body: &lyricTrackingBody{
					reads:  &reads,
					closes: &closes,
					cancel: cancel,
					err:    errors.New("ordinary read failure"),
				},
				Request: req,
			}, nil
		}))
		got, err := client.GetLyrics(ctx, "41378936")
		if got != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("GetLyrics() = (%#v, %v), want context.Canceled", got, err)
		}
		if reads.Load() != 1 || closes.Load() != 1 || mobileCalls.Load() != 0 {
			t.Fatalf("reads/closes/mobile = %d/%d/%d", reads.Load(), closes.Load(), mobileCalls.Load())
		}
	})

	t.Run("expired deadline", func(t *testing.T) {
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()
		var mobileCalls atomic.Int32
		client := newLyricsTestClient(t, lyricRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.Host == "mobile.test" {
				mobileCalls.Add(1)
			}
			return nil, errors.New("transport should not run")
		}))
		if got, err := client.GetLyrics(ctx, "41378936"); got != nil || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("GetLyrics() = (%#v, %v)", got, err)
		}
		if mobileCalls.Load() != 0 {
			t.Fatalf("mobile calls = %d", mobileCalls.Load())
		}
	})
}

func TestGetLyricsCancellationWinsSuccessfulResponse(t *testing.T) {
	wordBody, err := base64.StdEncoding.DecodeString(fixedWordLyricResponseBase64)
	if err != nil {
		t.Fatal(err)
	}
	for _, cancelAtMobile := range []bool{false, true} {
		t.Run(fmt.Sprintf("mobile=%v", cancelAtMobile), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			var mobileCalls atomic.Int32
			client := newLyricsTestClient(t, lyricRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.Host == "word.test" {
					if cancelAtMobile {
						return lyricHTTPResponse(req, http.StatusInternalServerError, nil), nil
					}
					cancel()
					return lyricHTTPResponse(req, http.StatusOK, wordBody), nil
				}
				mobileCalls.Add(1)
				cancel()
				return lyricHTTPResponse(req, http.StatusOK, validMobileLyricBody()), nil
			}))

			got, err := client.GetLyrics(ctx, "41378936")
			if got != nil || !errors.Is(err, context.Canceled) {
				t.Fatalf("GetLyrics() = (%#v, %v), want context.Canceled", got, err)
			}
			wantMobile := int32(0)
			if cancelAtMobile {
				wantMobile = 1
			}
			if mobileCalls.Load() != wantMobile {
				t.Fatalf("mobile calls = %d, want %d", mobileCalls.Load(), wantMobile)
			}
		})
	}
}

func padWordLyricResponse(t *testing.T, body []byte, size int) []byte {
	t.Helper()
	_, payload, ok := bytes.Cut(body, []byte("\r\n\r\n"))
	if !ok {
		t.Fatal("test word lyric envelope missing separator")
	}
	prefix := []byte("tp=content\r\npadding=")
	suffix := []byte("\r\n\r\n")
	padding := size - len(prefix) - len(suffix) - len(payload)
	if padding < 0 {
		t.Fatalf("target size %d too small", size)
	}
	result := make([]byte, 0, size)
	result = append(result, prefix...)
	result = append(result, bytes.Repeat([]byte("x"), padding)...)
	result = append(result, suffix...)
	result = append(result, payload...)
	return result
}

func paddedMobileLyricBody(t *testing.T, size int) []byte {
	t.Helper()
	prefix := []byte(`{"status":200,"data":{"songinfo":{"id":"41378936"},"lrclist":[{"time":0,"lineLyric":"line"}]},"padding":"`)
	suffix := []byte(`"}`)
	padding := size - len(prefix) - len(suffix)
	if padding < 0 {
		t.Fatalf("target size %d too small", size)
	}
	result := make([]byte, 0, size)
	result = append(result, prefix...)
	result = append(result, bytes.Repeat([]byte("x"), padding)...)
	result = append(result, suffix...)
	return result
}

func TestGetLyricsRawResponseLimits(t *testing.T) {
	validWord := encodedWordLyricResponse(t, "[00:00.000]word")
	for _, tt := range []struct {
		name       string
		wordBody   []byte
		mobileBody []byte
		wantErr    bool
		wantMobile int32
		wantPlain  string
		wordStatus int
	}{
		{
			name:       "enhanced exactly 4 MiB",
			wordBody:   padWordLyricResponse(t, validWord, maxJSONBodyBytes),
			wordStatus: http.StatusOK,
			wantPlain:  "word",
		},
		{
			name:       "enhanced 4 MiB plus one falls back",
			wordBody:   padWordLyricResponse(t, validWord, maxJSONBodyBytes+1),
			wordStatus: http.StatusOK,
			mobileBody: validMobileLyricBody(),
			wantMobile: 1,
			wantPlain:  "第一句\n第二句",
		},
		{
			name:       "mobile exactly 4 MiB",
			wordStatus: http.StatusInternalServerError,
			mobileBody: paddedMobileLyricBody(t, maxJSONBodyBytes),
			wantMobile: 1,
			wantPlain:  "line",
		},
		{
			name:       "mobile 4 MiB plus one",
			wordStatus: http.StatusInternalServerError,
			mobileBody: paddedMobileLyricBody(t, maxJSONBodyBytes+1),
			wantMobile: 1,
			wantErr:    true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var mobileCalls atomic.Int32
			client := newLyricsTestClient(t, lyricRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.Host == "word.test" {
					return lyricHTTPResponse(req, tt.wordStatus, tt.wordBody), nil
				}
				mobileCalls.Add(1)
				return lyricHTTPResponse(req, http.StatusOK, tt.mobileBody), nil
			}))
			got, err := client.GetLyrics(context.Background(), "41378936")
			if tt.wantErr {
				if got != nil || err == nil {
					t.Fatalf("GetLyrics() = (%#v, %v), want error", got, err)
				}
			} else if err != nil || got == nil || got.Plain != tt.wantPlain {
				t.Fatalf("GetLyrics() = (%#v, %v), want Plain %q", got, err, tt.wantPlain)
			}
			if mobileCalls.Load() != tt.wantMobile {
				t.Fatalf("mobile calls = %d, want %d", mobileCalls.Load(), tt.wantMobile)
			}
		})
	}
}
