package kuwo

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/liuran001/MusicBot-Go/bot/platform"
)

func TestEncodeKuwoQueryKnownVectors(t *testing.T) {
	tests := []struct {
		name      string
		plaintext string
		want      string
	}{
		{
			name:      "empty still emits the mandatory zero block",
			plaintext: "",
			want:      "amNIiFPkHdE=",
		},
		{
			name: "production playable FLAC request",
			plaintext: "user=0&corp=kuwo&source=kwplayer_ar_5.1.0.0_B_jiakong_vh.apk&" +
				"p2p=1&type=convert_url2&sig=0&format=flac&rid=41378936",
			want: "3HxQnWXTNdQ6RbicYxLOyHAu64fVpKoBz43BshH4RFaGBPBi+8dZdGuvz4Hu9TAf" +
				"A75CH9prKR/wLP/IiYIvJoWxgCvU/gETNIqiGvqcuuscRcbgESVmpm7oNjCqzuWEy03cbXspfYklrL1vogqWKh92DoNQT7mV",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := encodeKuwoQuery(tt.plaintext); got != tt.want {
				t.Fatalf("encodeKuwoQuery() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseLegacyPlayResponse(t *testing.T) {
	body := []byte(
		"format=flac\r\n" +
			"bitrate=2000\r\n" +
			"rid=41378936\r\n" +
			"duration=213\r\n" +
			"type=0\r\n" +
			"url=https://kw-er.kuwo.cn/song.flac?opaque=a=b\r\n",
	)
	got, err := parseLegacyPlayResponse(body)
	if err != nil {
		t.Fatalf("parseLegacyPlayResponse() error = %v", err)
	}
	if got.url != "https://kw-er.kuwo.cn/song.flac?opaque=a=b" ||
		got.format != "flac" ||
		got.bitrate != "2000" ||
		got.rid != "41378936" ||
		got.duration != "213" ||
		got.mediaType != "0" {
		t.Fatalf("parseLegacyPlayResponse() = %#v", got)
	}
}

func TestParseLegacyPlayResponseRejectsAmbiguousOrHostileBodies(t *testing.T) {
	overlong := "url=https://kw-er.kuwo.cn/" + strings.Repeat("a", maxLegacyPlayLineBytes) + ".flac\n"
	tests := []struct {
		name string
		body string
	}{
		{name: "empty", body: ""},
		{name: "missing URL", body: "format=flac\nbitrate=2000\n"},
		{name: "duplicate critical field", body: "url=https://kw-er.kuwo.cn/a.flac\nurl=https://kw-er.kuwo.cn/b.flac\n"},
		{name: "invalid line", body: "url=https://kw-er.kuwo.cn/a.flac\nnot-a-pair\n"},
		{name: "NUL", body: "url=https://kw-er.kuwo.cn/a.flac\x00\n"},
		{name: "control character", body: "url=https://kw-er.kuwo.cn/a.flac\x01\n"},
		{name: "overlong line", body: overlong},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := parseLegacyPlayResponse([]byte(tt.body)); err == nil {
				t.Fatal("parseLegacyPlayResponse() succeeded")
			}
		})
	}
	if _, err := parseLegacyPlayResponse(make([]byte, maxLegacyPlayBodyBytes+1)); err == nil {
		t.Fatal("parseLegacyPlayResponse(oversized body) succeeded")
	}
}

func TestResolvePlayableLosslessUsesDirect2000ContractAndCleansTrailer(t *testing.T) {
	const expectedQuery = "3HxQnWXTNdQ6RbicYxLOyHAu64fVpKoBz43BshH4RFaGBPBi+8dZdGuvz4Hu9TAf" +
		"A75CH9prKR/wLP/IiYIvJoWxgCvU/gETNIqiGvqcuuscRcbgESVmpm7oNjCqzuWEy03cbXspfYklrL1vogqWKh92DoNQT7mV"
	cleartext := makeTestFLAC(t, 64<<10, 44100, 16, 2, 213*time.Second)
	raw := append(append([]byte(nil), cleartext...), knownDirectFLACTrailer...)
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Host {
		case "mobi.kuwo.cn":
			if req.URL.Query().Get("f") != "kuwo" || req.URL.Query().Get("q") != expectedQuery {
				t.Fatalf("legacy query = %v", req.URL.Query())
			}
			if req.Header.Get("User-Agent") != mediaUserAgent {
				t.Fatalf("legacy User-Agent = %q", req.Header.Get("User-Agent"))
			}
			return response(http.StatusOK, nil, []byte(
				"format=flac\r\n"+
					"bitrate=2000\r\n"+
					"rid=41378936\r\n"+
					"duration=213\r\n"+
					"type=0\r\n"+
					"url=http://kw-er.kuwo.cn/playable.flac\r\n",
			)), nil
		case "kw-er.kuwo.cn":
			if req.URL.Scheme != "https" {
				t.Fatalf("media scheme = %q", req.URL.Scheme)
			}
			switch req.Header.Get("Range") {
			case "bytes=0-41":
				return response(
					http.StatusPartialContent,
					map[string]string{
						"Content-Range": "bytes 0-41/" + strconv.FormatInt(int64(len(raw)), 10),
					},
					raw[:42],
				), nil
			case "bytes=" + strconv.FormatInt(int64(len(cleartext)), 10) + "-" +
				strconv.FormatInt(int64(len(raw)-1), 10):
				tailResponse := response(
					http.StatusPartialContent,
					map[string]string{
						"Content-Range": "bytes " +
							strconv.FormatInt(int64(len(cleartext)), 10) + "-" +
							strconv.FormatInt(int64(len(raw)-1), 10) + "/" +
							strconv.FormatInt(int64(len(raw)), 10),
					},
					knownDirectFLACTrailer,
				)
				tailResponse.ContentLength = int64(len(knownDirectFLACTrailer))
				return tailResponse, nil
			case "":
				fullResponse := response(http.StatusOK, nil, raw)
				fullResponse.ContentLength = int64(len(raw))
				return fullResponse, nil
			default:
				t.Fatalf("unexpected media Range %q", req.Header.Get("Range"))
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

	info, err := client.resolvePlayableLossless(context.Background(), &trackDetail{
		Track: platform.Track{ID: "41378936", Duration: 213 * time.Second},
	})
	if err != nil {
		t.Fatalf("resolvePlayableLossless() error = %v", err)
	}
	if info.URL != "https://kw-er.kuwo.cn/playable.flac" ||
		info.Format != "flac" ||
		info.Size != int64(len(cleartext)) ||
		info.Bitrate != averageBitrateKbps(int64(len(cleartext)), 213*time.Second) ||
		info.Quality != platform.QualityLossless ||
		info.Downloader == nil {
		t.Fatalf("resolvePlayableLossless() = %#v", info)
	}
	if info.ValidateURL == nil ||
		info.ExpiresAt == nil ||
		!info.ExpiresAt.Equal(now.Add(10*time.Minute)) {
		t.Fatalf("download policy = %#v", info)
	}
	destination := filepath.Join(t.TempDir(), "lossless.flac")
	written, err := info.Downloader(
		context.Background(),
		info,
		destination,
		nil,
	)
	if err != nil {
		t.Fatalf("direct lossless download = %v", err)
	}
	downloaded, err := os.ReadFile(destination)
	if err != nil {
		t.Fatalf("read direct lossless = %v", err)
	}
	if written != int64(len(cleartext)) || !bytes.Equal(downloaded, cleartext) {
		t.Fatalf(
			"direct lossless output written=%d size=%d, want %d clean bytes",
			written,
			len(downloaded),
			len(cleartext),
		)
	}
}

func TestResolvePlayableLosslessRejectsResponseIdentityAndPreviewSignals(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		terminal bool
	}{
		{
			name:     "RID mismatch",
			body:     "url=https://kw-er.kuwo.cn/a.flac\nformat=flac\nrid=999\nduration=213\ntype=0\n",
			terminal: true,
		},
		{
			name:     "missing RID",
			body:     "url=https://kw-er.kuwo.cn/a.flac\nformat=flac\nbitrate=2000\nduration=213\ntype=0\n",
			terminal: true,
		},
		{
			name: "format mismatch",
			body: "url=https://kw-er.kuwo.cn/a.flac\nformat=mp3\nrid=41378936\nduration=213\ntype=0\n",
		},
		{
			name: "missing format",
			body: "url=https://kw-er.kuwo.cn/a.flac\nbitrate=2000\nrid=41378936\nduration=213\ntype=0\n",
		},
		{
			name: "missing bitrate",
			body: "url=https://kw-er.kuwo.cn/a.flac\nformat=flac\nrid=41378936\nduration=213\ntype=0\n",
		},
		{
			name: "wrong lossless selector",
			body: "url=https://kw-er.kuwo.cn/a.flac\nformat=flac\nbitrate=4000\nrid=41378936\nduration=213\ntype=0\n",
		},
		{
			name:     "preview bitrate",
			body:     "url=https://kw-er.kuwo.cn/a.flac\nformat=flac\nbitrate=1\nrid=41378936\nduration=213\ntype=0\n",
			terminal: true,
		},
		{
			name:     "duration mismatch",
			body:     "url=https://kw-er.kuwo.cn/a.flac\nformat=flac\nrid=41378936\nduration=30\ntype=0\n",
			terminal: true,
		},
		{
			name:     "preview type",
			body:     "url=https://kw-er.kuwo.cn/a.flac\nformat=flac\nrid=41378936\nduration=213\ntype=1\n",
			terminal: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewClient(time.Second, nil)
			transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.Host != "mobi.kuwo.cn" {
					t.Fatalf("unexpected media probe after invalid contract: %s", req.URL)
				}
				return response(http.StatusOK, nil, []byte(tt.body)), nil
			})
			client.apiHTTPClient.Transport = transport
			client.mediaHTTPClient.Transport = transport
			_, err := client.resolvePlayableLossless(context.Background(), &trackDetail{
				Track: platform.Track{ID: "41378936", Duration: 213 * time.Second},
			})
			if err == nil {
				t.Fatal("resolvePlayableLossless() succeeded")
			}
			if tt.terminal && !errors.Is(err, platform.ErrUnavailable) {
				t.Fatalf("resolvePlayableLossless() error = %v, want unavailable", err)
			}
			if !tt.terminal && errors.Is(err, platform.ErrUnavailable) {
				t.Fatalf("resolvePlayableLossless() error = %v, want retryable candidate failure", err)
			}
		})
	}
}

func TestResolvePlayableLosslessRejectsHiResStreamOn2000Selector(t *testing.T) {
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Host {
		case "mobi.kuwo.cn":
			return response(http.StatusOK, nil, []byte(
				"format=flac\n"+
					"bitrate=2000\n"+
					"rid=41378936\n"+
					"duration=213\n"+
					"type=0\n"+
					"url=https://kw-lw.kuwo.cn/audio/wrong-tier.flac\n",
			)), nil
		case "kw-lw.kuwo.cn":
			streamInfo := makeTestFLAC(t, 42, 96000, 24, 2, 213*time.Second)
			return response(
				http.StatusPartialContent,
				map[string]string{"Content-Range": "bytes 0-41/78024024"},
				streamInfo,
			), nil
		default:
			t.Fatalf("unexpected request host %q", req.URL.Host)
			return nil, nil
		}
	})
	client := NewClient(time.Second, nil)
	client.apiHTTPClient.Transport = transport
	client.mediaHTTPClient.Transport = transport

	if _, err := client.resolvePlayableLossless(context.Background(), &trackDetail{
		Track: platform.Track{ID: "41378936", Duration: 213 * time.Second},
	}); err == nil || errors.Is(err, platform.ErrUnavailable) {
		t.Fatalf("resolvePlayableLossless() error = %v, want retryable tier mismatch", err)
	}
}
