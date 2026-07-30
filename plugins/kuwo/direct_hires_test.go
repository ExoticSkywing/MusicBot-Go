package kuwo

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/liuran001/MusicBot-Go/bot/platform"
)

func TestResolvePlayableHiResBuildsVerifiedDirectFLAC(t *testing.T) {
	const (
		trackID   = "7149583"
		totalSize = 1 << 20
	)
	fullStream := makeTestFLAC(t, totalSize, 96000, 24, 2, time.Second)
	clear(fullStream[len(fullStream)-len(knownDirectFLACTrailer):])
	streamInfo := fullStream[:42]
	var resolverRequests atomic.Int32
	var mediaRequests atomic.Int32
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Host {
		case "resolver.example":
			resolverRequests.Add(1)
			query := req.URL.Query()
			if got := query.Get("id"); got != trackID {
				t.Fatalf("resolver id = %q, want %q", got, trackID)
			}
			if got := query.Get("type"); got != "song" {
				t.Fatalf("resolver type = %q, want song", got)
			}
			if got := query.Get("level"); got != "hires" {
				t.Fatalf("resolver level = %q, want hires", got)
			}
			if got := query.Get("format"); got != "json" {
				t.Fatalf("resolver format = %q, want json", got)
			}
			body := fmt.Sprintf(
				`{"code":200,"msg":"ok","data":{"rid":%q,"bitrate":4000,"duration":1,"size":"1.00 MB","url":"https://kw-lw.kuwo.cn/audio/test.flac","level":{"requested":"hires","actual":"hires","ekey":"","quality":[{"quality":"Hi-Res FLAC","br":"4000","format":"flac","size":"1.00Mb","level":"hires"}]}}}`,
				trackID,
			)
			return response(http.StatusOK, map[string]string{"Content-Type": "application/json"}, []byte(body)), nil
		case "kw-lw.kuwo.cn":
			mediaRequests.Add(1)
			switch got := req.Header.Get("Range"); got {
			case "bytes=0-41":
				return response(
					http.StatusPartialContent,
					map[string]string{
						"Content-Range": fmt.Sprintf("bytes 0-41/%d", totalSize),
						"Content-Type":  "audio/flac",
					},
					streamInfo,
				), nil
			case "bytes=1048561-1048575":
				tail := fullStream[len(fullStream)-len(knownDirectFLACTrailer):]
				tailResponse := response(
					http.StatusPartialContent,
					map[string]string{
						"Content-Range": fmt.Sprintf(
							"bytes 1048561-1048575/%d",
							totalSize,
						),
						"Content-Type": "audio/flac",
					},
					tail,
				)
				tailResponse.ContentLength = int64(len(tail))
				return tailResponse, nil
			case "":
				fullResponse := response(
					http.StatusOK,
					map[string]string{"Content-Type": "audio/flac"},
					fullStream,
				)
				fullResponse.ContentLength = totalSize
				return fullResponse, nil
			default:
				t.Fatalf("unexpected probe range %q", got)
				return nil, nil
			}
		default:
			return nil, fmt.Errorf("unexpected host %q", req.URL.Host)
		}
	})
	client := newClientWithEndpoints(time.Second, nil, kuwoEndpoints{
		qualityResolver: "https://resolver.example/api",
	})
	client.apiHTTPClient.Transport = transport
	client.mediaHTTPClient.Transport = transport
	client.downloadHTTPClient = &http.Client{Transport: transport}

	info, err := client.resolvePlayableHiRes(context.Background(), &trackDetail{
		Track: platform.Track{ID: trackID, Duration: time.Second},
	})
	if err != nil {
		t.Fatalf("resolve direct Hi-Res: %v", err)
	}
	if got := resolverRequests.Load(); got != 1 {
		t.Fatalf("resolver requests = %d, want 1", got)
	}
	if got := mediaRequests.Load(); got != 2 {
		t.Fatalf("media requests = %d, want 2", got)
	}
	if info.URL != "https://kw-lw.kuwo.cn/audio/test.flac" ||
		info.Format != "flac" ||
		info.Size != totalSize ||
		info.Quality != platform.QualityHiRes ||
		info.Downloader == nil ||
		info.ValidateURL == nil {
		t.Fatalf("unexpected direct Hi-Res info: %+v", info)
	}
	written, err := info.Downloader(
		context.Background(),
		info,
		filepath.Join(t.TempDir(), "direct-hires.flac"),
		nil,
	)
	if err != nil {
		t.Fatalf("download direct Hi-Res: %v", err)
	}
	if written != totalSize {
		t.Fatalf("downloaded bytes = %d, want %d", written, totalSize)
	}
	if got := mediaRequests.Load(); got != 3 {
		t.Fatalf("media requests after download = %d, want 3", got)
	}
}

func TestResolvePlayableHiResRejectsInvalidResolverContractBeforeMedia(t *testing.T) {
	cases := map[string]string{
		"identity mismatch":     `{"rid":"999","bitrate":4000,"duration":1,"size":"1.00 MB","url":"https://kw-lw.kuwo.cn/a.flac","level":{"requested":"hires","actual":"hires","ekey":"","quality":[{"br":"4000","format":"flac","level":"hires"}]}}`,
		"selector downgrade":    `{"rid":"7149583","bitrate":2000,"duration":1,"size":"1.00 MB","url":"https://kw-lw.kuwo.cn/a.flac","level":{"requested":"hires","actual":"hires","ekey":"","quality":[{"br":"4000","format":"flac","level":"hires"}]}}`,
		"requested downgrade":   `{"rid":"7149583","bitrate":4000,"duration":1,"size":"1.00 MB","url":"https://kw-lw.kuwo.cn/a.flac","level":{"requested":"lossless","actual":"hires","ekey":"","quality":[{"br":"4000","format":"flac","level":"hires"}]}}`,
		"actual downgrade":      `{"rid":"7149583","bitrate":4000,"duration":1,"size":"1.00 MB","url":"https://kw-lw.kuwo.cn/a.flac","level":{"requested":"hires","actual":"lossless","ekey":"","quality":[{"br":"4000","format":"flac","level":"hires"}]}}`,
		"encrypted response":    `{"rid":"7149583","bitrate":4000,"duration":1,"size":"1.00 MB","url":"https://kw-lw.kuwo.cn/a.flac","level":{"requested":"hires","actual":"hires","ekey":"secret","quality":[{"br":"4000","format":"flac","level":"hires"}]}}`,
		"missing quality entry": `{"rid":"7149583","bitrate":4000,"duration":1,"size":"1.00 MB","url":"https://kw-lw.kuwo.cn/a.flac","level":{"requested":"hires","actual":"hires","ekey":"","quality":[{"br":"2000","format":"flac","level":"lossless"}]}}`,
		"wrong quality format":  `{"rid":"7149583","bitrate":4000,"duration":1,"size":"1.00 MB","url":"https://kw-lw.kuwo.cn/a.flac","level":{"requested":"hires","actual":"hires","ekey":"","quality":[{"br":"4000","format":"mflac","level":"hires"}]}}`,
		"invalid size":          `{"rid":"7149583","bitrate":4000,"duration":1,"size":"unknown","url":"https://kw-lw.kuwo.cn/a.flac","level":{"requested":"hires","actual":"hires","ekey":"","quality":[{"br":"4000","format":"flac","level":"hires"}]}}`,
		"unsafe media suffix":   `{"rid":"7149583","bitrate":4000,"duration":1,"size":"1.00 MB","url":"https://kw-lw.kuwo.cn/a.mflac","level":{"requested":"hires","actual":"hires","ekey":"","quality":[{"br":"4000","format":"flac","level":"hires"}]}}`,
		"duration mismatch":     `{"rid":"7149583","bitrate":4000,"duration":11,"size":"1.00 MB","url":"https://kw-lw.kuwo.cn/a.flac","level":{"requested":"hires","actual":"hires","ekey":"","quality":[{"br":"4000","format":"flac","level":"hires"}]}}`,
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			var mediaRequests atomic.Int32
			transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.Host != "resolver.example" {
					mediaRequests.Add(1)
					return nil, fmt.Errorf("unexpected media request")
				}
				if got := req.URL.Query().Get("level"); got != "hires" {
					t.Fatalf("resolver level = %q, want hires", got)
				}
				return response(
					http.StatusOK,
					map[string]string{"Content-Type": "application/json"},
					[]byte(`{"code":200,"msg":"ok","data":`+data+`}`),
				), nil
			})
			client := newClientWithEndpoints(time.Second, nil, kuwoEndpoints{
				qualityResolver: "https://resolver.example/api",
			})
			client.apiHTTPClient.Transport = transport
			client.mediaHTTPClient.Transport = transport

			_, err := client.resolvePlayableHiRes(context.Background(), &trackDetail{
				Track: platform.Track{ID: "7149583", Duration: time.Second},
			})
			if err == nil {
				t.Fatal("resolve direct Hi-Res unexpectedly succeeded")
			}
			if got := mediaRequests.Load(); got != 0 {
				t.Fatalf("media requests = %d, want 0", got)
			}
		})
	}
}

func TestResolvePlayableHiResRejectsFalseHiResSTREAMINFO(t *testing.T) {
	cases := []struct {
		name          string
		sampleRate    int
		bitsPerSample int
		channels      int
	}{
		{name: "ordinary lossless", sampleRate: 44100, bitsPerSample: 16, channels: 2},
		{name: "not 24 bit", sampleRate: 96000, bitsPerSample: 16, channels: 2},
		{name: "not 96 kHz", sampleRate: 48000, bitsPerSample: 24, channels: 2},
		{name: "not stereo", sampleRate: 96000, bitsPerSample: 24, channels: 6},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			streamInfo := makeTestFLAC(
				t,
				42,
				testCase.sampleRate,
				testCase.bitsPerSample,
				testCase.channels,
				time.Second,
			)
			transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
				switch req.URL.Host {
				case "resolver.example":
					if strings.EqualFold(req.URL.Query().Get("level"), "jymaster") {
						t.Fatal("direct Hi-Res resolver requested jymaster")
					}
					body := `{"code":200,"msg":"ok","data":{"rid":"7149583","bitrate":4000,"duration":1,"size":"1.00 MB","url":"https://kw-lw.kuwo.cn/a.flac","level":{"requested":"hires","actual":"hires","ekey":"","quality":[{"br":"4000","format":"flac","level":"hires"}]}}}`
					return response(http.StatusOK, nil, []byte(body)), nil
				case "kw-lw.kuwo.cn":
					return response(
						http.StatusPartialContent,
						map[string]string{"Content-Range": "bytes 0-41/1048576"},
						streamInfo,
					), nil
				default:
					return nil, fmt.Errorf("unexpected host %q", req.URL.Host)
				}
			})
			client := newClientWithEndpoints(time.Second, nil, kuwoEndpoints{
				qualityResolver: "https://resolver.example/api",
			})
			client.apiHTTPClient.Transport = transport
			client.mediaHTTPClient.Transport = transport

			info, err := client.resolvePlayableHiRes(context.Background(), &trackDetail{
				Track: platform.Track{ID: "7149583", Duration: time.Second},
			})
			if err == nil || info != nil {
				t.Fatalf("false Hi-Res resolved info=%+v err=%v", info, err)
			}
		})
	}
}

func TestParseDirectHiResSizeRejectsMultiplicationOverflow(t *testing.T) {
	// Without checking the whole-MiB bound before multiplying by 1000, this
	// value wraps whole*scale to 8 and could be mistaken for a tiny valid file.
	if size, ok := parseDirectHiResSize("2066035336255469781.000 MB"); ok {
		t.Fatalf("overflowing size parsed as %d bytes", size)
	}
}
