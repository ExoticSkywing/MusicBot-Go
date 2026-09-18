package youtubemusic

import (
	"context"
	"net"
	"net/http"
	"net/http/httptrace"
	"sync/atomic"
	"time"

	"github.com/liuran001/MusicBot-Go/bot/platform"
)

// Separate, reusable clients keep a search retry from changing the shared
// player/lyrics transport or racing with another request. No global routing,
// environment variables or platform configuration are changed on success.
func newYouTubeMusicFamilyHTTPClient(timeout time.Duration, network string) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	dialer := &net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}
	transport.DialContext = func(ctx context.Context, _ string, address string) (net.Conn, error) {
		return dialer.DialContext(ctx, network, address)
	}
	return &http.Client{Timeout: timeout, Transport: transport}
}

// Only direct searches may use a second direct route. Respect both the plugin
// proxy and the primary transport's proxy (including HTTP(S)_PROXY/NO_PROXY).
func (c *Client) directSearchFallbackAllowed() bool {
	if c.apiProxyEnabled {
		return false
	}
	transport := c.httpClient.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	if direct, ok := transport.(*http.Transport); ok && direct.Proxy != nil {
		req, _ := http.NewRequest(http.MethodPost, innerTubeBaseMusic+"/search", nil)
		proxyURL, err := direct.Proxy(req)
		return err == nil && proxyURL == nil
	}
	return true
}

func (c *Client) searchWithIPFallback(ctx context.Context, query string, limit int) ([]platform.Track, error) {
	if c == nil || c.httpClient == nil {
		return nil, platform.ErrUnavailable
	}
	// Capture the actual connected family: prefer_ipv6 may have fallen back at
	// dial time, and the default dual-stack dialer can choose either family.
	var family atomic.Int32
	traceCtx := httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		GotConn: func(info httptrace.GotConnInfo) {
			host, _, err := net.SplitHostPort(info.Conn.RemoteAddr().String())
			if err != nil {
				return
			}
			ip := net.ParseIP(host)
			if ip == nil {
				return
			}
			if ip.To4() != nil {
				family.Store(4)
			} else {
				family.Store(6)
			}
		},
	})
	tracks, err := c.searchOnce(traceCtx, c.httpClient, query, limit)
	if err != nil || len(tracks) > 0 || !c.directSearchFallbackAllowed() {
		return tracks, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var alternate *http.Client
	var from, to string
	switch family.Load() {
	case 4:
		alternate, from, to = c.searchIPv6Client, "ipv4", "ipv6"
	case 6:
		alternate, from, to = c.searchIPv4Client, "ipv6", "ipv4"
	}
	if alternate == nil {
		return tracks, nil
	}
	if c.logger != nil {
		c.logger.Info("youtubemusic: empty search; trying alternate IP family", "from", from, "to", to)
	}
	// Exactly one retry; a true no-match stays empty and a failed retry remains
	// an error rather than being misreported as a successful empty search.
	tracks, err = c.searchOnce(ctx, alternate, query, limit)
	if c.logger != nil {
		if err != nil {
			c.logger.Warn("youtubemusic: alternate search failed", "family", to, "error", err)
		} else {
			c.logger.Info("youtubemusic: alternate search completed", "family", to, "results", len(tracks))
		}
	}
	return tracks, err
}
