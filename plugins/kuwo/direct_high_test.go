package kuwo

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/liuran001/MusicBot-Go/bot/platform"
)

func TestGetDownloadInfoHighFallsBackAfterExternalResolverInternalTimeout(t *testing.T) {
	const (
		trackID   = "41378936"
		totalSize = int64(8_525_534)
	)
	var resolverCalls atomic.Int32
	var mobileCalls atomic.Int32
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
			return response(
				http.StatusOK,
				nil,
				[]byte(`{"data":{"rid":41378936,"duration":213,"isListenFee":false}}`),
			), nil
		case "resolver.example":
			resolverCalls.Add(1)
			return nil, &url.Error{
				Op:  http.MethodGet,
				URL: "https://resolver.example/api",
				Err: context.DeadlineExceeded,
			}
		case "mobi.kuwo.cn":
			mobileCalls.Add(1)
			return response(
				http.StatusOK,
				nil,
				[]byte(
					`{"code":200,"data":{"rid":41378936,`+
						`"url":"https://kw-er.kuwo.cn/audio/mobile.mp3",`+
						`"format":"mp3","bitrate":320,"duration":213,"type":0}}`,
				),
			), nil
		case "kw-er.kuwo.cn":
			return mp3ProbeTransport(t, totalSize, nil).Transport.RoundTrip(req)
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
	ctx := context.Background()

	info, err := client.GetDownloadInfo(ctx, trackID, platform.QualityHigh)
	if err != nil {
		t.Fatalf("GetDownloadInfo() = %v", err)
	}
	if ctx.Err() != nil {
		t.Fatalf("caller context unexpectedly ended: %v", ctx.Err())
	}
	if info == nil ||
		info.Format != "mp3" ||
		info.Quality != platform.QualityHigh ||
		info.Bitrate < 256 ||
		info.Bitrate > 384 {
		t.Fatalf("GetDownloadInfo() = %#v", info)
	}
	if resolverCalls.Load() != 1 || mobileCalls.Load() != 1 {
		t.Fatalf(
			"resolver/mobile calls=%d/%d, want 1/1",
			resolverCalls.Load(),
			mobileCalls.Load(),
		)
	}
}

func TestGetDownloadInfoHighFallsBackAfterExternalMediaInternalTimeout(t *testing.T) {
	const (
		trackID   = "41378936"
		totalSize = int64(8_525_534)
	)
	var externalMediaCalls atomic.Int32
	var mobileCalls atomic.Int32
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
			return response(
				http.StatusOK,
				nil,
				[]byte(`{"data":{"rid":41378936,"duration":213,"isListenFee":false}}`),
			), nil
		case "resolver.example":
			return response(
				http.StatusOK,
				nil,
				[]byte(externalHighResponse(trackID, "exhigh", "", "8.13 MB")),
			), nil
		case "er-sycdn.kuwo.cn":
			externalMediaCalls.Add(1)
			return nil, &url.Error{
				Op:  http.MethodGet,
				URL: "https://er-sycdn.kuwo.cn/audio/test.mp3",
				Err: context.DeadlineExceeded,
			}
		case "mobi.kuwo.cn":
			mobileCalls.Add(1)
			return response(
				http.StatusOK,
				nil,
				[]byte(
					`{"code":200,"data":{"rid":41378936,`+
						`"url":"https://kw-er.kuwo.cn/audio/mobile.mp3",`+
						`"format":"mp3","bitrate":320,"duration":213,"type":0}}`,
				),
			), nil
		case "kw-er.kuwo.cn":
			return mp3ProbeTransport(t, totalSize, nil).Transport.RoundTrip(req)
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
	ctx := context.Background()

	info, err := client.GetDownloadInfo(ctx, trackID, platform.QualityHigh)
	if err != nil {
		t.Fatalf("GetDownloadInfo() = %v", err)
	}
	if ctx.Err() != nil {
		t.Fatalf("caller context unexpectedly ended: %v", ctx.Err())
	}
	if info == nil ||
		info.Format != "mp3" ||
		info.Quality != platform.QualityHigh ||
		info.Bitrate < 256 ||
		info.Bitrate > 384 {
		t.Fatalf("GetDownloadInfo() = %#v", info)
	}
	if externalMediaCalls.Load() != 1 || mobileCalls.Load() != 1 {
		t.Fatalf(
			"external media/mobile calls=%d/%d, want 1/1",
			externalMediaCalls.Load(),
			mobileCalls.Load(),
		)
	}
}

func TestResolvePlayableExternalHighPreservesCallerCancellation(t *testing.T) {
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return nil, context.Canceled
	})
	client := newClientWithEndpoints(time.Second, nil, kuwoEndpoints{
		qualityResolver: "https://resolver.example/api",
	})
	client.apiHTTPClient.Transport = transport
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	info, err := client.resolvePlayableExternalHigh(
		ctx,
		&trackDetail{
			Track: platform.Track{
				ID:       "41378936",
				Duration: 213 * time.Second,
			},
		},
	)
	if info != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("resolve result=(%+v, %v), want caller cancellation", info, err)
	}
}

func TestGetDownloadInfoDoesNotContactExternalHighBeforeAccessOrForStandard(t *testing.T) {
	for _, testCase := range []struct {
		name          string
		quality       platform.Quality
		detail        string
		wantError     error
		wantInfo      bool
		wantMediaCall bool
	}{
		{
			name:      "paid High stops before resolver",
			quality:   platform.QualityHigh,
			detail:    `{"data":{"rid":41378936,"duration":213,"isListenFee":true}}`,
			wantError: platform.ErrUnavailable,
		},
		{
			name:          "Standard never contacts external resolver",
			quality:       platform.QualityStandard,
			detail:        `{"data":{"rid":41378936,"duration":213,"isListenFee":false}}`,
			wantInfo:      true,
			wantMediaCall: true,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			var resolverCalls atomic.Int32
			var mobileCalls atomic.Int32
			var mediaCalls atomic.Int32
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
					return response(http.StatusOK, nil, []byte(testCase.detail)), nil
				case "resolver.example":
					resolverCalls.Add(1)
					return nil, errors.New("external resolver must not be contacted")
				case "mobi.kuwo.cn":
					mobileCalls.Add(1)
					return response(
						http.StatusOK,
						nil,
						[]byte(
							`{"code":200,"data":{"rid":41378936,`+
								`"url":"https://kw-er.kuwo.cn/audio/standard.mp3",`+
								`"format":"mp3","bitrate":128,"duration":213,"type":0}}`,
						),
					), nil
				case "kw-er.kuwo.cn":
					mediaCalls.Add(1)
					return mp3ProbeTransport(t, 3_410_341, nil).Transport.RoundTrip(req)
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

			info, err := client.GetDownloadInfo(
				context.Background(),
				"41378936",
				testCase.quality,
			)
			if !errors.Is(err, testCase.wantError) {
				t.Fatalf("GetDownloadInfo() error=%v, want %v", err, testCase.wantError)
			}
			if (info != nil) != testCase.wantInfo {
				t.Fatalf("GetDownloadInfo() info=%+v, want info=%v", info, testCase.wantInfo)
			}
			if resolverCalls.Load() != 0 {
				t.Fatalf("external resolver calls=%d, want 0", resolverCalls.Load())
			}
			if testCase.wantInfo &&
				(mobileCalls.Load() != 1 ||
					(mediaCalls.Load() > 0) != testCase.wantMediaCall) {
				t.Fatalf(
					"mobile/media calls=%d/%d",
					mobileCalls.Load(),
					mediaCalls.Load(),
				)
			}
		})
	}
}

func TestResolvePlayableExternalHighValidatesResolverContract(t *testing.T) {
	const (
		trackID   = "41378936"
		totalSize = int64(8_525_534)
	)
	valid := externalHighResponse(trackID, "exhigh", "", "8.13 MB")
	for _, testCase := range []struct {
		name             string
		body             string
		wantIdentity     bool
		wantTerminal     bool
		wantMediaRequest bool
	}{
		{
			name:         "wrong RID is optional candidate failure",
			body:         strings.Replace(valid, `"rid":"41378936"`, `"rid":"165721002"`, 1),
			wantIdentity: true,
		},
		{
			name:         "missing RID is terminal",
			body:         strings.Replace(valid, `"rid":"41378936",`, "", 1),
			wantIdentity: true,
			wantTerminal: true,
		},
		{
			name:         "duration mismatch is terminal",
			body:         strings.Replace(valid, `"duration":213`, `"duration":21`, 1),
			wantTerminal: true,
		},
		{
			name: "selector bitrate mismatch",
			body: strings.Replace(valid, `"bitrate":320`, `"bitrate":128`, 1),
		},
		{
			name: "requested level mismatch",
			body: strings.Replace(valid, `"requested":"exhigh"`, `"requested":"standard"`, 1),
		},
		{
			name: "actual level mismatch",
			body: strings.Replace(valid, `"actual":"exhigh"`, `"actual":"standard"`, 1),
		},
		{
			name: "encrypted response",
			body: strings.Replace(valid, `"ekey":""`, `"ekey":"encrypted"`, 1),
		},
		{
			name: "missing exact quality entry",
			body: strings.Replace(valid, `"br":"320"`, `"br":"128"`, 1),
		},
		{
			name: "wrong quality format",
			body: strings.Replace(valid, `"format":"mp3"`, `"format":"aac"`, 1),
		},
		{
			name: "wrong quality level",
			body: strings.Replace(
				valid,
				`"level":"exhigh"}]`,
				`"level":"standard"}]`,
				1,
			),
		},
		{
			name: "invalid declared size",
			body: strings.Replace(valid, `"size":"8.13 MB"`, `"size":"unknown"`, 1),
		},
		{
			name:         "unsafe media URL is terminal",
			body:         strings.Replace(valid, "er-sycdn.kuwo.cn", "evil.test", 1),
			wantTerminal: true,
		},
		{
			name:             "declared size mismatch",
			body:             strings.Replace(valid, `"size":"8.13 MB"`, `"size":"16.00 MB"`, 1),
			wantMediaRequest: true,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			var mediaRequests atomic.Int32
			transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
				switch req.URL.Host {
				case "resolver.example":
					return response(http.StatusOK, nil, []byte(testCase.body)), nil
				case "er-sycdn.kuwo.cn":
					mediaRequests.Add(1)
					return mp3ProbeTransport(t, totalSize, nil).Transport.RoundTrip(req)
				default:
					return nil, fmt.Errorf("unexpected host %q", req.URL.Host)
				}
			})
			client := newClientWithEndpoints(time.Second, nil, kuwoEndpoints{
				qualityResolver: "https://resolver.example/api",
			})
			client.apiHTTPClient.Transport = transport
			client.mediaHTTPClient.Transport = transport

			info, err := client.resolvePlayableExternalHigh(
				context.Background(),
				&trackDetail{
					Track: platform.Track{ID: trackID, Duration: 213 * time.Second},
				},
			)
			if err == nil || info != nil {
				t.Fatalf("invalid contract resolved info=%+v err=%v", info, err)
			}
			if errors.Is(err, errTrackIdentityMismatch) != testCase.wantIdentity {
				t.Fatalf(
					"identity mismatch classification=%v, want %v: %v",
					errors.Is(err, errTrackIdentityMismatch),
					testCase.wantIdentity,
					err,
				)
			}
			if errors.Is(err, platform.ErrUnavailable) != testCase.wantTerminal {
				t.Fatalf(
					"terminal classification=%v, want %v: %v",
					errors.Is(err, platform.ErrUnavailable),
					testCase.wantTerminal,
					err,
				)
			}
			if (mediaRequests.Load() > 0) != testCase.wantMediaRequest {
				t.Fatalf(
					"media requests=%d, want request=%v",
					mediaRequests.Load(),
					testCase.wantMediaRequest,
				)
			}
		})
	}
}

func TestResolvePlayableExternalHighClassifiesTopLevelIdentity(t *testing.T) {
	for _, testCase := range []struct {
		name         string
		body         string
		wantTerminal bool
	}{
		{name: "code 200 missing data", body: `{"code":200}`, wantTerminal: true},
		{name: "code 200 null data", body: `{"code":200,"data":null}`, wantTerminal: true},
		{name: "business failure remains optional", body: `{"code":407,"data":null}`},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.Host != "resolver.example" {
					return nil, fmt.Errorf("unexpected host %q", req.URL.Host)
				}
				return response(http.StatusOK, nil, []byte(testCase.body)), nil
			})
			client := newClientWithEndpoints(time.Second, nil, kuwoEndpoints{
				qualityResolver: "https://resolver.example/api",
			})
			client.apiHTTPClient.Transport = transport

			info, err := client.resolvePlayableExternalHigh(
				context.Background(),
				&trackDetail{
					Track: platform.Track{
						ID:       "41378936",
						Duration: 213 * time.Second,
					},
				},
			)
			if err == nil || info != nil {
				t.Fatalf("identity response resolved info=%+v err=%v", info, err)
			}
			if errors.Is(err, platform.ErrUnavailable) != testCase.wantTerminal {
				t.Fatalf(
					"terminal classification=%v, want %v: %v",
					errors.Is(err, platform.ErrUnavailable),
					testCase.wantTerminal,
					err,
				)
			}
			if testCase.wantTerminal && !errors.Is(err, errTrackIdentityMismatch) {
				t.Fatalf("error=%v, want identity mismatch", err)
			}
		})
	}
}

func TestGetDownloadInfoHighStopsOnMissingExternalIdentityData(t *testing.T) {
	for _, testCase := range []struct {
		name string
		body string
	}{
		{name: "missing data", body: `{"code":200}`},
		{name: "null data", body: `{"code":200,"data":null}`},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			var resolverCalls atomic.Int32
			var officialCalls atomic.Int32
			var mobileCalls atomic.Int32
			var webCalls atomic.Int32
			transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
				switch req.URL.Host {
				case "www.kuwo.cn":
					switch {
					case req.URL.Path == "/":
						return response(
							http.StatusOK,
							map[string]string{
								"Set-Cookie": kuwoSessionCookie + "=abcdefghijklmnop; Path=/",
							},
							nil,
						), nil
					case strings.Contains(req.URL.Path, "musicInfo"):
						return response(
							http.StatusOK,
							nil,
							[]byte(`{"data":{"rid":41378936,"name":"Song","duration":213,"isListenFee":false}}`),
						), nil
					case strings.Contains(req.URL.Path, "playUrl"):
						webCalls.Add(1)
						return response(http.StatusInternalServerError, nil, nil), nil
					}
				case "resolver.example":
					resolverCalls.Add(1)
					return response(http.StatusOK, nil, []byte(testCase.body)), nil
				case "mobi.kuwo.cn":
					if req.URL.Query().Get("f") == "kuwo" {
						officialCalls.Add(1)
					} else {
						mobileCalls.Add(1)
					}
					return response(http.StatusInternalServerError, nil, nil), nil
				}
				return nil, fmt.Errorf("unexpected request %s", req.URL)
			})
			client := newClientWithEndpoints(time.Second, nil, kuwoEndpoints{
				home:            kuwoHomeURL,
				detail:          kuwoDetailURL,
				qualityResolver: "https://resolver.example/api",
			})
			client.apiHTTPClient.Transport = transport
			client.mediaHTTPClient.Transport = transport

			info, err := client.GetDownloadInfo(
				context.Background(),
				"41378936",
				platform.QualityHigh,
			)
			if info != nil ||
				!errors.Is(err, platform.ErrUnavailable) ||
				!errors.Is(err, errTrackIdentityMismatch) {
				t.Fatalf(
					"GetDownloadInfo()=(%+v, %v), want terminal identity mismatch",
					info,
					err,
				)
			}
			if resolverCalls.Load() != 1 ||
				officialCalls.Load() != 0 ||
				mobileCalls.Load() != 0 ||
				webCalls.Load() != 0 {
				t.Fatalf(
					"resolver/official/mobile/web=%d/%d/%d/%d, want 1/0/0/0",
					resolverCalls.Load(),
					officialCalls.Load(),
					mobileCalls.Load(),
					webCalls.Load(),
				)
			}
		})
	}
}

func TestResolvePlayableExternalHighRejectsFalse320Media(t *testing.T) {
	const trackID = "41378936"
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Host {
		case "resolver.example":
			return response(
				http.StatusOK,
				nil,
				[]byte(externalHighResponse(trackID, "exhigh", "", "3.25 MB")),
			), nil
		case "er-sycdn.kuwo.cn":
			return mp3ProbeTransport(t, 3_410_341, nil).Transport.RoundTrip(req)
		default:
			return nil, fmt.Errorf("unexpected host %q", req.URL.Host)
		}
	})
	client := newClientWithEndpoints(time.Second, nil, kuwoEndpoints{
		qualityResolver: "https://resolver.example/api",
	})
	client.apiHTTPClient.Transport = transport
	client.mediaHTTPClient.Transport = transport

	info, err := client.resolvePlayableExternalHigh(
		context.Background(),
		&trackDetail{
			Track: platform.Track{ID: trackID, Duration: 213 * time.Second},
		},
	)
	if err == nil || info != nil {
		t.Fatalf("false 320 media resolved info=%+v err=%v", info, err)
	}
}

func TestGetDownloadInfoHighPrefersVerifiedExternalExHigh(t *testing.T) {
	const (
		trackID   = "41378936"
		duration  = 213 * time.Second
		totalSize = int64(8_525_534)
	)
	var resolverCalls atomic.Int32
	var mobileCalls atomic.Int32
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
			return response(
				http.StatusOK,
				nil,
				[]byte(`{"data":{"rid":41378936,"name":"Song","duration":213,"isListenFee":false}}`),
			), nil
		case "resolver.example":
			resolverCalls.Add(1)
			level := req.URL.Query().Get("level")
			if strings.EqualFold(level, "standard") ||
				strings.EqualFold(level, "master") ||
				strings.EqualFold(level, "jymaster") {
				t.Fatalf("High resolver requested forbidden level %q", level)
			}
			if level != "exhigh" {
				t.Fatalf("High resolver level = %q, want exhigh", level)
			}
			return response(
				http.StatusOK,
				map[string]string{"Content-Type": "application/json"},
				[]byte(externalHighResponse(trackID, "exhigh", "", "8.13 MB")),
			), nil
		case "mobi.kuwo.cn":
			mobileCalls.Add(1)
			return nil, fmt.Errorf("external exhigh success fell through to mobile")
		case "er-sycdn.kuwo.cn":
			return mp3ProbeTransport(t, totalSize, nil).Transport.RoundTrip(req)
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

	info, err := client.GetDownloadInfo(
		context.Background(),
		trackID,
		platform.QualityHigh,
	)
	if err != nil {
		t.Fatalf("GetDownloadInfo() = %v", err)
	}
	if info == nil ||
		info.URL != "https://er-sycdn.kuwo.cn/audio/test.mp3" ||
		info.Format != "mp3" ||
		info.Size != totalSize ||
		info.Bitrate < 256 ||
		info.Bitrate > 384 ||
		info.Quality != platform.QualityHigh {
		t.Fatalf("GetDownloadInfo() = %#v", info)
	}
	if resolverCalls.Load() != 1 || mobileCalls.Load() != 0 {
		t.Fatalf(
			"resolver calls=%d mobile calls=%d, want 1 and 0",
			resolverCalls.Load(),
			mobileCalls.Load(),
		)
	}
}

func externalHighResponse(
	rid string,
	actual string,
	ekey string,
	size string,
) string {
	return fmt.Sprintf(
		`{"code":200,"msg":"ok","data":{"rid":%q,"bitrate":320,"duration":213,`+
			`"size":%q,"url":"https://er-sycdn.kuwo.cn/audio/test.mp3",`+
			`"level":{"requested":"exhigh","actual":%q,"ekey":%q,`+
			`"quality":[{"br":"320","format":"mp3","level":"exhigh"}]}}}`,
		rid,
		size,
		actual,
		ekey,
	)
}
