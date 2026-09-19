package youtubemusic

import (
	"context"
	"errors"
	"io"
	"net"
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

const unavailablePlayerJSON = `{"playabilityStatus":{"status":"UNPLAYABLE","reason":"This video is not available"},"videoDetails":{"videoId":"UQ8cXH7qbVU","title":"Test"}}`

func TestPlayerAlternateIPFamily(t *testing.T) {
	for _, tc := range []struct {
		name, ip string
		prefer6  bool
	}{
		{"ipv6 to ipv4", "2001:db8::1", true},
		{"ipv4 to ipv6", "192.0.2.1", false},
		{"actual dial fallback", "192.0.2.1", true},
		{"default selected ipv6", "2001:db8::1", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := NewClient("SID=test-cookie", time.Second, nil)
			c.SetPreferIPv6(tc.prefer6)
			c.visitorData = "TEST_VISITOR"
			var calls int
			var primaryBody string
			var primaryHeaders http.Header
			c.httpClient = &http.Client{Transport: youtubeMusicRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				searchTestTrace(req, tc.ip)
				body, _ := io.ReadAll(req.Body)
				primaryBody, primaryHeaders = string(body), req.Header.Clone()
				return youtubeMusicTestResponse(req, 200, unavailablePlayerJSON), nil
			})}
			primary := c.httpClient
			alternate := func(network string) *http.Client {
				return &http.Client{Transport: youtubeMusicRoundTripFunc(func(req *http.Request) (*http.Response, error) {
					calls++
					wantNetwork := "tcp4"
					if net.ParseIP(tc.ip).To4() != nil {
						wantNetwork = "tcp6"
					}
					body, _ := io.ReadAll(req.Body)
					if network != wantNetwork || string(body) != primaryBody || !reflect.DeepEqual(req.Header, primaryHeaders) {
						t.Error("retry changed request identity or chose the wrong family")
					}
					return youtubeMusicTestResponse(req, 200, playableYouTubeMusicResponse("alternate")), nil
				})}
			}
			c.searchIPv4Client, c.searchIPv6Client = alternate("tcp4"), alternate("tcp6")
			info, err := c.GetDownloadInfo(t.Context(), "UQ8cXH7qbVU", platform.QualityHigh)
			if err != nil || info == nil {
				t.Fatalf("download info failed: %v", err)
			}
			if calls != 2 || !strings.Contains(info.URL, "id=alternate") || info.Headers["User-Agent"] != visionOSUserAgent || info.MaxChunkSize != googleVideoMaxChunk {
				t.Fatalf("unexpected calls=%d or altered download identity", calls)
			}
			if c.httpClient != primary || c.visitorData != "TEST_VISITOR" || c.preferIPv6 != tc.prefer6 {
				t.Fatal("retry mutated shared client or visitor state")
			}
		})
	}
}

func TestPlayerIPFallbackBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, body, ip      string
		status              int
		proxy, cancel, used bool
	}{
		{"playable", playableYouTubeMusicResponse("primary"), "2001:db8::1", 200, false, false, false},
		{"explicit proxy", unavailablePlayerJSON, "2001:db8::1", 200, true, false, false},
		{"unknown connection", unavailablePlayerJSON, "", 200, false, false, false},
		{"cancelled", unavailablePlayerJSON, "2001:db8::1", 200, false, true, false},
		{"retry already used", unavailablePlayerJSON, "2001:db8::1", 200, false, false, true},
		{"http rate limit", `{}`, "2001:db8::1", 429, false, false, false},
		{"server failure", `{}`, "2001:db8::1", 503, false, false, false},
		{"malformed", `broken`, "2001:db8::1", 200, false, false, false},
		{"bot check", `{"playabilityStatus":{"status":"LOGIN_REQUIRED","reason":"Sign in to confirm you're not a bot"}}`, "2001:db8::1", 200, false, false, false},
		{"country restriction", `{"playabilityStatus":{"status":"UNPLAYABLE","reason":"This video is not available in your country"}}`, "2001:db8::1", 200, false, false, false},
		{"private video", `{"playabilityStatus":{"status":"LOGIN_REQUIRED","reason":"This video is private"}}`, "2001:db8::1", 200, false, false, false},
		{"cipher-only stream", `{"playabilityStatus":{"status":"OK"}}`, "2001:db8::1", 200, false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := NewClient("", time.Second, nil)
			c.apiProxyEnabled = tc.proxy
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			c.httpClient = &http.Client{Transport: youtubeMusicRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				searchTestTrace(req, tc.ip)
				if tc.cancel {
					cancel()
				}
				return youtubeMusicTestResponse(req, tc.status, tc.body), nil
			})}
			unexpected := &http.Client{Transport: youtubeMusicRoundTripFunc(func(*http.Request) (*http.Response, error) {
				t.Error("unexpected alternate IP request")
				return nil, errors.New("unexpected retry")
			})}
			c.searchIPv4Client, c.searchIPv6Client = unexpected, unexpected
			used := tc.used
			_, err := c.playerOnceWithIPFallback(ctx, "UQ8cXH7qbVU", "visitor", playerClientProfiles()[0], &used)
			if used != tc.used {
				t.Error("retry budget consumed without a permitted retry")
			}
			if tc.status == 429 && !errors.Is(err, platform.ErrRateLimited) {
				t.Fatalf("lost 429 error: %v", err)
			}
			if tc.cancel && !errors.Is(err, context.Canceled) {
				t.Fatalf("lost cancellation: %v", err)
			}
		})
	}
}

func TestPlayerAlternateFailurePreservesPrimaryResponse(t *testing.T) {
	for _, failHTTP := range []bool{false, true} {
		c := NewClient("", time.Second, nil)
		c.httpClient = &http.Client{Transport: youtubeMusicRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			searchTestTrace(req, "2001:db8::1")
			return youtubeMusicTestResponse(req, 200, unavailablePlayerJSON), nil
		})}
		c.searchIPv4Client = &http.Client{Transport: youtubeMusicRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			if failHTTP {
				return nil, errors.New("alternate unavailable")
			}
			return youtubeMusicTestResponse(req, 200, `{"playabilityStatus":{"status":"LOGIN_REQUIRED"}}`), nil
		})}
		used := false
		pr, err := c.playerOnceWithIPFallback(t.Context(), "UQ8cXH7qbVU", "visitor", playerClientProfiles()[0], &used)
		if err != nil || !used || !playerCanRetryIP(pr) || pr.VideoDetails.VideoID != "UQ8cXH7qbVU" {
			t.Fatal("alternate failure discarded the primary response or metadata")
		}
	}
}

func TestPlayerIPRetryBudgetAndConcurrentIsolation(t *testing.T) {
	c := NewClient("", time.Second, nil)
	c.visitorData = "VISITOR"
	var primaryCalls, alternateCalls atomic.Int32
	c.httpClient = &http.Client{Transport: youtubeMusicRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		primaryCalls.Add(1)
		searchTestTrace(req, "2001:db8::1")
		return youtubeMusicTestResponse(req, 200, unavailablePlayerJSON), nil
	})}
	c.searchIPv4Client = &http.Client{Transport: youtubeMusicRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		alternateCalls.Add(1)
		return youtubeMusicTestResponse(req, 200, unavailablePlayerJSON), nil
	})}
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := c.GetDownloadInfo(t.Context(), "UQ8cXH7qbVU", platform.QualityHigh)
			if !errors.Is(err, platform.ErrNotFound) {
				t.Errorf("unexpected final error: %v", err)
			}
		}()
	}
	wg.Wait()
	if primaryCalls.Load() != 24 || alternateCalls.Load() != 12 {
		t.Fatalf("expected 2 original profiles and 1 IP retry per operation, got %d/%d", primaryCalls.Load(), alternateCalls.Load())
	}
}

func TestPlayerFallbackRespectsEndpointProxy(t *testing.T) {
	c := NewClient("", time.Second, nil)
	c.httpClient.Transport = &http.Transport{Proxy: func(req *http.Request) (*url.URL, error) {
		if req.URL.Host == "www.youtube.com" {
			return url.Parse("http://127.0.0.1:8080")
		}
		return nil, nil
	}}
	if !c.directSearchFallbackAllowed() || c.directFallbackAllowed(innerTubeBaseVideo+"/player") {
		t.Fatal("player fallback reused the music search endpoint's proxy decision")
	}
	c.httpClient.Transport = &http.Transport{Proxy: func(*http.Request) (*url.URL, error) { return nil, errors.New("proxy failure") }}
	if c.directFallbackAllowed(innerTubeBaseVideo + "/player") {
		t.Fatal("proxy failure permitted direct fallback")
	}
}

func TestPlayerIPRetryBudgetSurvivesVisitorRefresh(t *testing.T) {
	c := NewClient("", time.Second, nil)
	c.visitorData = "OLD_VISITOR"
	primaryCalls, alternateCalls, refreshCalls := 0, 0, 0
	c.httpClient = &http.Client{Transport: youtubeMusicRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/watch" {
			refreshCalls++
			return youtubeMusicTestResponse(req, 200, `{"visitorData":"NEW_VISITOR"}`), nil
		}
		primaryCalls++
		searchTestTrace(req, "2001:db8::1")
		if req.Header.Get("X-YouTube-Client-Name") == androidVRClientNumber {
			return youtubeMusicTestResponse(req, 200, `{"playabilityStatus":{"status":"LOGIN_REQUIRED"}}`), nil
		}
		return youtubeMusicTestResponse(req, 200, unavailablePlayerJSON), nil
	})}
	c.searchIPv4Client = &http.Client{Transport: youtubeMusicRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		alternateCalls++
		return youtubeMusicTestResponse(req, 200, unavailablePlayerJSON), nil
	})}
	_, _ = c.player(t.Context(), "UQ8cXH7qbVU")
	if primaryCalls != 4 || alternateCalls != 1 || refreshCalls != 1 {
		t.Fatalf("retry multiplied across visitor refresh: primary=%d alternate=%d refresh=%d", primaryCalls, alternateCalls, refreshCalls)
	}
}

func TestPlayerAlternateCancellation(t *testing.T) {
	c := NewClient("", time.Second, nil)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	c.httpClient = &http.Client{Transport: youtubeMusicRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		searchTestTrace(req, "2001:db8::1")
		return youtubeMusicTestResponse(req, 200, unavailablePlayerJSON), nil
	})}
	c.searchIPv4Client = &http.Client{Transport: youtubeMusicRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		cancel()
		return youtubeMusicTestResponse(req, 200, playableYouTubeMusicResponse("cancelled")), nil
	})}
	used := false
	_, err := c.playerOnceWithIPFallback(ctx, "UQ8cXH7qbVU", "visitor", playerClientProfiles()[0], &used)
	if !errors.Is(err, context.Canceled) || !used {
		t.Fatalf("cancellation not propagated: %v", err)
	}
}
