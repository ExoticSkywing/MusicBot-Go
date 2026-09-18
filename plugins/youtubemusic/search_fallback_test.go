package youtubemusic

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/liuran001/MusicBot-Go/bot/httpproxy"
	"github.com/liuran001/MusicBot-Go/bot/platform"
)

const searchTrackJSON = `{"contents":[{"musicResponsiveListItemRenderer":{"navigationEndpoint":{"watchEndpoint":{"videoId":"UQ8cXH7qbVU"}},"flexColumns":[{"musicResponsiveListItemFlexColumnRenderer":{"text":{"runs":[{"text":"Test song"}]}}}]}}]}`

type searchTestConn struct {
	net.Conn
	ip string
}

func (c searchTestConn) RemoteAddr() net.Addr { return &net.TCPAddr{IP: net.ParseIP(c.ip), Port: 443} }

func searchTestTrace(req *http.Request, ip string) {
	if trace := httptrace.ContextClientTrace(req.Context()); trace != nil && trace.GotConn != nil {
		trace.GotConn(httptrace.GotConnInfo{Conn: searchTestConn{ip: ip}})
	}
}

func TestSearchAlternateIPFamily(t *testing.T) {
	for _, tc := range []struct {
		name, ip, primary, alternate string
		preferIPv6                   bool
		wantCalls, wantTracks        int
	}{
		{"ipv6 to ipv4", "2001:db8::1", `{}`, searchTrackJSON, true, 2, 1},
		{"ipv4 to ipv6", "192.0.2.1", `{}`, searchTrackJSON, false, 2, 1},
		{"use actual family after dial fallback", "192.0.2.1", `{}`, searchTrackJSON, true, 2, 1},
		{"default dialer chose ipv6", "2001:db8::1", `{}`, searchTrackJSON, false, 2, 1},
		{"both empty", "2001:db8::1", `{}`, `{}`, true, 2, 0},
		{"primary works", "2001:db8::1", searchTrackJSON, `{}`, true, 1, 1},
		{"unknown route", "", `{}`, searchTrackJSON, true, 1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := NewClient("SID=test-cookie", time.Second, nil)
			client.SetPreferIPv6(tc.preferIPv6)
			var calls int
			var firstBody string
			client.httpClient = &http.Client{Transport: youtubeMusicRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				searchTestTrace(req, tc.ip)
				body, _ := io.ReadAll(req.Body)
				firstBody = string(body)
				return youtubeMusicTestResponse(req, 200, tc.primary), nil
			})}
			primary := client.httpClient
			alternate := func(network string) *http.Client {
				return &http.Client{Transport: youtubeMusicRoundTripFunc(func(req *http.Request) (*http.Response, error) {
					calls++
					wantNetwork := "tcp4"
					if net.ParseIP(tc.ip).To4() != nil {
						wantNetwork = "tcp6"
					}
					if network != wantNetwork {
						t.Fatalf("retry uses %s, want %s", network, wantNetwork)
					}
					body, _ := io.ReadAll(req.Body)
					if string(body) != firstBody || req.Header.Get("Cookie") != "SID=test-cookie" || req.Header.Get("User-Agent") != defaultUserAgent {
						t.Fatal("retry changed request identity/payload")
					}
					return youtubeMusicTestResponse(req, 200, tc.alternate), nil
				})}
			}
			client.searchIPv4Client, client.searchIPv6Client = alternate("tcp4"), alternate("tcp6")
			tracks, err := client.Search(t.Context(), "test", 1)
			if err != nil || len(tracks) != tc.wantTracks || calls != tc.wantCalls {
				t.Fatalf("tracks=%d calls=%d error=%v", len(tracks), calls, err)
			}
			if client.httpClient != primary {
				t.Fatal("search changed shared API/player transport")
			}
		})
	}
}

func TestSearchFallbackBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, body    string
		status        int
		proxy, cancel bool
		wantError     bool
	}{
		{"explicit proxy", `{}`, 200, true, false, false},
		{"rate limited", `{}`, 429, false, false, true},
		{"server error", `{}`, 503, false, false, true},
		{"malformed response", `not json`, 200, false, false, true},
		{"cancelled", `{}`, 200, false, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := NewClient("", time.Second, nil)
			client.apiProxyEnabled = tc.proxy
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			client.httpClient = &http.Client{Transport: youtubeMusicRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				searchTestTrace(req, "2001:db8::1")
				if tc.cancel {
					cancel()
				}
				return youtubeMusicTestResponse(req, tc.status, tc.body), nil
			})}
			unexpected := &http.Client{Transport: youtubeMusicRoundTripFunc(func(*http.Request) (*http.Response, error) {
				t.Error("unexpected direct retry")
				return nil, errors.New("unexpected")
			})}
			client.searchIPv4Client, client.searchIPv6Client = unexpected, unexpected
			_, err := client.Search(ctx, "test", 1)
			if (err != nil) != tc.wantError {
				t.Fatalf("error = %v", err)
			}
			if tc.status == 429 && !errors.Is(err, platform.ErrRateLimited) {
				t.Fatalf("lost rate limit error: %v", err)
			}
		})
	}
}

func TestSearchAlternateFailureAndConcurrency(t *testing.T) {
	client := NewClient("", time.Second, nil)
	var primaryCalls, retryCalls atomic.Int32
	client.httpClient = &http.Client{Transport: youtubeMusicRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		primaryCalls.Add(1)
		searchTestTrace(req, "2001:db8::1")
		return youtubeMusicTestResponse(req, 200, `{}`), nil
	})}
	wantErr := errors.New("alternate network unavailable")
	client.searchIPv4Client = &http.Client{Transport: youtubeMusicRoundTripFunc(func(req *http.Request) (*http.Response, error) { retryCalls.Add(1); return nil, wantErr })}
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := client.Search(t.Context(), "test", 1); !errors.Is(err, wantErr) {
				t.Errorf("retry error not preserved: %v", err)
			}
		}()
	}
	wg.Wait()
	if primaryCalls.Load() != 16 || retryCalls.Load() != 16 {
		t.Fatalf("unexpected request counts: %d/%d", primaryCalls.Load(), retryCalls.Load())
	}
}

func TestSearchFallbackRespectsProxyConfiguration(t *testing.T) {
	client := NewClient("", time.Second, nil)
	if err := client.SetAPIProxy(httpproxy.Config{Enabled: true, Type: "http", Host: "127.0.0.1", Port: 8080}); err != nil {
		t.Fatal(err)
	}
	proxyClient := client.httpClient
	client.SetPreferIPv6(true)
	if client.httpClient != proxyClient || client.directSearchFallbackAllowed() {
		t.Fatal("IPv6 preference bypassed configured proxy")
	}
	if err := client.SetAPIProxy(httpproxy.Config{}); err != nil {
		t.Fatal(err)
	}
	// This is the same transport hook used by ProxyFromEnvironment.
	client.httpClient.Transport = &http.Transport{Proxy: func(*http.Request) (*url.URL, error) { return url.Parse("http://127.0.0.1:8080") }}
	if client.directSearchFallbackAllowed() {
		t.Fatal("environment/transport proxy could be bypassed")
	}
	client.httpClient.Transport = &http.Transport{Proxy: func(*http.Request) (*url.URL, error) { return nil, errors.New("proxy config error") }}
	if client.directSearchFallbackAllowed() {
		t.Fatal("proxy error permits direct fallback")
	}
	client.httpClient.Transport = &http.Transport{}
	if !client.directSearchFallbackAllowed() {
		t.Fatal("direct transport cannot retry")
	}
}
