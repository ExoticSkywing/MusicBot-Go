package kuwo

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/liuran001/MusicBot-Go/bot/platform"
)

func TestResolvePlayableExternalLosslessBuildsVerifiedDirectFLAC(t *testing.T) {
	for _, testCase := range []struct {
		name          string
		bitsPerSample int
		wantQuality   platform.Quality
	}{
		{
			name:          "16 bit reports lossless",
			bitsPerSample: 16,
			wantQuality:   platform.QualityLossless,
		},
		{
			name:          "24 bit reports hires",
			bitsPerSample: 24,
			wantQuality:   platform.QualityHiRes,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			const (
				trackID   = "165721002"
				totalSize = 1 << 20
			)
			fullStream := makeTestFLAC(
				t,
				totalSize,
				48000,
				testCase.bitsPerSample,
				2,
				time.Second,
			)
			var resolverRequests atomic.Int32
			var mediaRequests atomic.Int32
			transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if strings.EqualFold(req.URL.Query().Get("level"), "jymaster") ||
					strings.EqualFold(req.URL.Query().Get("level"), "master") {
					t.Fatal("external lossless resolver requested a master stream")
				}
				switch req.URL.Host {
				case "resolver.example":
					resolverRequests.Add(1)
					query := req.URL.Query()
					if query.Get("id") != trackID ||
						query.Get("type") != "song" ||
						query.Get("level") != "lossless" ||
						query.Get("format") != "json" {
						t.Fatalf("unexpected resolver query: %s", query.Encode())
					}
					return response(
						http.StatusOK,
						map[string]string{"Content-Type": "application/json"},
						[]byte(externalLosslessResponse(
							trackID,
							"lossless",
							"",
							"1.00 MB",
						)),
					), nil
				case "kw-lw.kuwo.cn":
					mediaRequests.Add(1)
					switch req.Header.Get("Range") {
					case "bytes=0-41":
						return response(
							http.StatusPartialContent,
							map[string]string{
								"Content-Range": fmt.Sprintf(
									"bytes 0-41/%d",
									totalSize,
								),
								"Content-Type": "audio/flac",
							},
							fullStream[:42],
						), nil
					case "":
						return directFLACResponse(fullStream), nil
					default:
						return directFLACTestTailResponse(t, req, fullStream), nil
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

			info, err := client.resolvePlayableExternalLossless(
				context.Background(),
				&trackDetail{
					Track: platform.Track{ID: trackID, Duration: time.Second},
				},
			)
			if err != nil {
				t.Fatalf("resolve external lossless: %v", err)
			}
			if info == nil ||
				info.URL != "https://kw-lw.kuwo.cn/audio/test.flac" ||
				info.Format != "flac" ||
				info.Size != totalSize ||
				info.Quality != testCase.wantQuality ||
				info.Downloader == nil ||
				info.ValidateURL == nil {
				t.Fatalf("unexpected external lossless info: %+v", info)
			}
			if resolverRequests.Load() != 1 || mediaRequests.Load() != 2 {
				t.Fatalf(
					"resolver requests=%d media requests=%d, want 1 and 2",
					resolverRequests.Load(),
					mediaRequests.Load(),
				)
			}

			written, err := info.Downloader(
				context.Background(),
				info,
				filepath.Join(t.TempDir(), "external-lossless.flac"),
				nil,
			)
			if err != nil {
				t.Fatalf("download external lossless: %v", err)
			}
			if written != totalSize {
				t.Fatalf("downloaded bytes = %d, want %d", written, totalSize)
			}
			if mediaRequests.Load() != 3 {
				t.Fatalf(
					"media requests after download = %d, want 3",
					mediaRequests.Load(),
				)
			}
		})
	}
}

func TestResolvePlayableExternalLosslessRejectsUnsupportedSTREAMINFO(t *testing.T) {
	for _, testCase := range []struct {
		name          string
		sampleRate    int
		bitsPerSample int
		channels      int
	}{
		{name: "96 kHz 24 bit", sampleRate: 96000, bitsPerSample: 24, channels: 2},
		{name: "48 kHz 32 bit", sampleRate: 48000, bitsPerSample: 32, channels: 2},
		{name: "48 kHz multichannel", sampleRate: 48000, bitsPerSample: 24, channels: 6},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			const totalSize = 1 << 20
			header := makeTestFLAC(
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
					return response(
						http.StatusOK,
						nil,
						[]byte(externalLosslessResponse(
							"165721002",
							"lossless",
							"",
							"1.00 MB",
						)),
					), nil
				case "kw-lw.kuwo.cn":
					return response(
						http.StatusPartialContent,
						map[string]string{
							"Content-Range": "bytes 0-41/1048576",
						},
						header,
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

			info, err := client.resolvePlayableExternalLossless(
				context.Background(),
				&trackDetail{
					Track: platform.Track{
						ID:       "165721002",
						Duration: time.Second,
					},
				},
			)
			if err == nil || info != nil {
				t.Fatalf("unsupported STREAMINFO resolved info=%+v err=%v", info, err)
			}
		})
	}
}

func TestResolvePlayableExternalLosslessValidatesResolverContract(t *testing.T) {
	for _, testCase := range []struct {
		name             string
		rid              string
		actual           string
		ekey             string
		size             string
		wantIdentity     bool
		wantTerminal     bool
		wantMediaRequest bool
	}{
		{
			name:         "downgraded quality",
			rid:          `"165721002"`,
			actual:       "standard",
			size:         "1.00 MB",
			wantTerminal: false,
		},
		{
			name:         "wrong RID is optional candidate failure",
			rid:          `"41378936"`,
			actual:       "lossless",
			size:         "1.00 MB",
			wantIdentity: true,
		},
		{
			name:         "missing RID is terminal",
			actual:       "lossless",
			size:         "1.00 MB",
			wantIdentity: true,
			wantTerminal: true,
		},
		{
			name:         "encrypted response",
			rid:          `"165721002"`,
			actual:       "lossless",
			ekey:         "encrypted",
			size:         "1.00 MB",
			wantTerminal: false,
		},
		{
			name:             "declared size mismatch",
			rid:              `"165721002"`,
			actual:           "lossless",
			size:             "2.00 MB",
			wantMediaRequest: true,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			var mediaRequests atomic.Int32
			transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
				switch req.URL.Host {
				case "resolver.example":
					ridField := ""
					if testCase.rid != "" {
						ridField = `"rid":` + testCase.rid + `,`
					}
					body := fmt.Sprintf(
						`{"code":200,"data":{%s"bitrate":2000,"duration":1,`+
							`"size":%q,"url":"https://kw-lw.kuwo.cn/audio/test.flac",`+
							`"level":{"requested":"lossless","actual":%q,"ekey":%q,`+
							`"quality":[{"br":"2000","format":"flac","level":"lossless"}]}}}`,
						ridField,
						testCase.size,
						testCase.actual,
						testCase.ekey,
					)
					return response(http.StatusOK, nil, []byte(body)), nil
				case "kw-lw.kuwo.cn":
					mediaRequests.Add(1)
					header := makeTestFLAC(t, 42, 48000, 24, 2, time.Second)
					return response(
						http.StatusPartialContent,
						map[string]string{
							"Content-Range": "bytes 0-41/1048576",
						},
						header,
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

			info, err := client.resolvePlayableExternalLossless(
				context.Background(),
				&trackDetail{
					Track: platform.Track{
						ID:       "165721002",
						Duration: time.Second,
					},
				},
			)
			if err == nil || info != nil {
				t.Fatalf("invalid contract resolved info=%+v err=%v", info, err)
			}
			if errors.Is(err, errTrackIdentityMismatch) != testCase.wantIdentity {
				t.Fatalf(
					"identity mismatch classification = %v, want %v: %v",
					errors.Is(err, errTrackIdentityMismatch),
					testCase.wantIdentity,
					err,
				)
			}
			if errors.Is(err, platform.ErrUnavailable) != testCase.wantTerminal {
				t.Fatalf(
					"terminal classification = %v, want %v: %v",
					errors.Is(err, platform.ErrUnavailable),
					testCase.wantTerminal,
					err,
				)
			}
			if (mediaRequests.Load() > 0) != testCase.wantMediaRequest {
				t.Fatalf(
					"media requests = %d, want request=%v",
					mediaRequests.Load(),
					testCase.wantMediaRequest,
				)
			}
		})
	}
}

func TestLosslessOfficialEmptyFallsBackToExternalWithoutMP3(t *testing.T) {
	const (
		trackID   = "41378936"
		totalSize = 1 << 20
	)
	fullStream := makeTestFLAC(t, totalSize, 44100, 16, 2, time.Second)
	var officialCalls atomic.Int32
	var externalCalls atomic.Int32
	var mp3Calls atomic.Int32
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
				`{"data":{"rid":41378936,"duration":1,"isListenFee":false}}`,
			)), nil
		case "mobi.kuwo.cn":
			if req.URL.Query().Get("q") == "" {
				mp3Calls.Add(1)
				t.Fatal("external lossless success fell through to MP3")
			}
			officialCalls.Add(1)
			return response(http.StatusOK, nil, nil), nil
		case "resolver.example":
			externalCalls.Add(1)
			if got := req.URL.Query().Get("level"); got != "lossless" {
				t.Fatalf("resolver level = %q, want lossless", got)
			}
			return response(
				http.StatusOK,
				nil,
				[]byte(externalLosslessResponse(
					trackID,
					"lossless",
					"",
					"1.00 MB",
				)),
			), nil
		case "kw-lw.kuwo.cn":
			switch req.Header.Get("Range") {
			case "bytes=0-41":
				return response(
					http.StatusPartialContent,
					map[string]string{
						"Content-Range": fmt.Sprintf(
							"bytes 0-41/%d",
							totalSize,
						),
					},
					fullStream[:42],
				), nil
			default:
				return directFLACTestTailResponse(t, req, fullStream), nil
			}
		default:
			return nil, fmt.Errorf("unexpected host %q", req.URL.Host)
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
		platform.QualityLossless,
	)
	if err != nil {
		t.Fatalf("GetDownloadInfo() = %v", err)
	}
	if info == nil || info.Quality != platform.QualityLossless {
		t.Fatalf("GetDownloadInfo() = %#v", info)
	}
	if officialCalls.Load() != 1 ||
		externalCalls.Load() != 1 ||
		mp3Calls.Load() != 0 {
		t.Fatalf(
			"official=%d external=%d mp3=%d, want 1 1 0",
			officialCalls.Load(),
			externalCalls.Load(),
			mp3Calls.Load(),
		)
	}
}

func TestHiResFallsBackToExternalLosslessAfterFirstTwoResolversFail(t *testing.T) {
	const (
		trackID   = "165721002"
		totalSize = 1 << 20
	)
	fullStream := makeTestFLAC(t, totalSize, 48000, 24, 2, time.Second)
	var resolverOrder []string
	var mp3Calls atomic.Int32
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
				`{"data":{"rid":165721002,"duration":1,"isListenFee":false}}`,
			)), nil
		case "resolver.example":
			level := req.URL.Query().Get("level")
			resolverOrder = append(resolverOrder, level)
			switch level {
			case "hires":
				return response(http.StatusOK, nil, []byte(
					`{"code":500,"msg":"unavailable"}`,
				)), nil
			case "lossless":
				return response(
					http.StatusOK,
					nil,
					[]byte(externalLosslessResponse(
						trackID,
						"lossless",
						"",
						"1.00 MB",
					)),
				), nil
			default:
				return nil, fmt.Errorf("unexpected resolver level %q", level)
			}
		case "mobi.kuwo.cn":
			if req.URL.Query().Get("q") == "" {
				mp3Calls.Add(1)
				t.Fatal("external lossless success fell through to MP3")
			}
			resolverOrder = append(resolverOrder, "official")
			return response(http.StatusOK, nil, nil), nil
		case "kw-lw.kuwo.cn":
			switch req.Header.Get("Range") {
			case "bytes=0-41":
				return response(
					http.StatusPartialContent,
					map[string]string{
						"Content-Range": fmt.Sprintf(
							"bytes 0-41/%d",
							totalSize,
						),
					},
					fullStream[:42],
				), nil
			default:
				return directFLACTestTailResponse(t, req, fullStream), nil
			}
		default:
			return nil, fmt.Errorf("unexpected host %q", req.URL.Host)
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
	if info == nil || info.Quality != platform.QualityHiRes {
		t.Fatalf("GetDownloadInfo() = %#v", info)
	}
	wantOrder := []string{"hires", "official", "lossless"}
	if len(resolverOrder) != len(wantOrder) {
		t.Fatalf("resolver order = %v, want %v", resolverOrder, wantOrder)
	}
	for index := range wantOrder {
		if resolverOrder[index] != wantOrder[index] {
			t.Fatalf("resolver order = %v, want %v", resolverOrder, wantOrder)
		}
	}
	if mp3Calls.Load() != 0 {
		t.Fatalf("MP3 calls = %d, want 0", mp3Calls.Load())
	}
}

func externalLosslessResponse(
	rid string,
	actual string,
	ekey string,
	size string,
) string {
	return fmt.Sprintf(
		`{"code":200,"msg":"ok","data":{"rid":%q,"bitrate":2000,"duration":1,`+
			`"size":%q,"url":"https://kw-lw.kuwo.cn/audio/test.flac",`+
			`"level":{"requested":"lossless","actual":%q,"ekey":%q,`+
			`"quality":[{"br":"2000","format":"flac","level":"lossless"}]}}}`,
		rid,
		size,
		actual,
		ekey,
	)
}
