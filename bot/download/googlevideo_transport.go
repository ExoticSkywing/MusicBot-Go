package download

import (
	"context"
	"net"
	"net/http"
	"strings"
)

// googleVideoTransport keeps direct downloads on the IP family that minted
// the signed URL. Googlevideo rejects a URL bound to an IPv6 address when the
// downloader's normal dual-stack dialer chooses IPv4 (and vice versa).
// Explicit download proxies and all other CDNs keep their original routing.
type googleVideoTransport struct {
	base, ipv4, ipv6 *http.Transport
}

func newGoogleVideoTransport(base *http.Transport) *googleVideoTransport {
	forFamily := func(network string) *http.Transport {
		transport := base.Clone()
		dial := base.DialContext
		if dial == nil {
			dial = (&net.Dialer{}).DialContext
		}
		transport.DialContext = func(ctx context.Context, _ string, address string) (net.Conn, error) {
			return dial(ctx, network, address)
		}
		return transport
	}
	return &googleVideoTransport{base: base, ipv4: forFamily("tcp4"), ipv6: forFamily("tcp6")}
}

func (t *googleVideoTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	host := strings.ToLower(req.URL.Hostname())
	if req.URL.Scheme != "https" || !strings.HasSuffix(host, ".googlevideo.com") {
		return t.base.RoundTrip(req)
	}
	if t.base.Proxy != nil {
		if proxyURL, err := t.base.Proxy(req); err != nil || proxyURL != nil {
			return t.base.RoundTrip(req)
		}
	}
	ip := net.ParseIP(req.URL.Query().Get("ip"))
	if ip == nil {
		return t.base.RoundTrip(req)
	}
	if ip.To4() != nil {
		return t.ipv4.RoundTrip(req)
	}
	return t.ipv6.RoundTrip(req)
}

func (t *googleVideoTransport) CloseIdleConnections() {
	t.base.CloseIdleConnections()
	t.ipv4.CloseIdleConnections()
	t.ipv6.CloseIdleConnections()
}
