package download

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"testing"
)

func TestGoogleVideoTransportRouting(t *testing.T) {
	for _, tc := range []struct {
		name, rawURL, wantNetwork string
		proxy                     bool
	}{
		{"ipv4 bound", "https://rr1.googlevideo.com/videoplayback?ip=192.0.2.1", "tcp4", false},
		{"ipv6 bound", "https://rr1.googlevideo.com/videoplayback?ip=2001:db8::1", "tcp6", false},
		{"mapped ipv4", "https://rr1.googlevideo.com/videoplayback?ip=::ffff:192.0.2.1", "tcp4", false},
		{"missing ip", "https://rr1.googlevideo.com/videoplayback", "tcp", false},
		{"invalid ip", "https://rr1.googlevideo.com/videoplayback?ip=example.com", "tcp", false},
		{"other platform", "https://music.example.com/audio?ip=2001:db8::1", "tcp", false},
		{"lookalike host", "https://notgooglevideo.com/audio?ip=2001:db8::1", "tcp", false},
		{"untrusted suffix", "https://googlevideo.com.example.com/audio?ip=2001:db8::1", "tcp", false},
		{"insecure URL", "http://rr1.googlevideo.com/videoplayback?ip=2001:db8::1", "tcp", false},
		{"proxy retained", "https://rr1.googlevideo.com/videoplayback?ip=2001:db8::1", "tcp", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gotNetwork, gotAddress string
			wantErr := errors.New("test dial stopped")
			base := &http.Transport{DialContext: func(_ context.Context, network, address string) (net.Conn, error) {
				gotNetwork, gotAddress = network, address
				return nil, wantErr
			}}
			if tc.proxy {
				proxyURL, _ := url.Parse("http://127.0.0.1:12345")
				base.Proxy = http.ProxyURL(proxyURL)
			}
			transport := newGoogleVideoTransport(base)
			defer transport.CloseIdleConnections()
			req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, tc.rawURL, nil)
			req.Header.Set("Range", "bytes=0-1023")
			req.Header.Set("User-Agent", "player-test")
			_, err := transport.RoundTrip(req)
			if !errors.Is(err, wantErr) || gotNetwork != tc.wantNetwork {
				t.Fatalf("network=%q want=%q err=%v", gotNetwork, tc.wantNetwork, err)
			}
			if tc.proxy && gotAddress != "127.0.0.1:12345" {
				t.Fatalf("bypassed proxy: %s", gotAddress)
			}
			if req.URL.String() != tc.rawURL || req.Header.Get("Range") != "bytes=0-1023" || req.Header.Get("User-Agent") != "player-test" {
				t.Fatal("transport changed signed URL or request identity")
			}
		})
	}
}

func TestGoogleVideoTransportProxyFailureDoesNotDial(t *testing.T) {
	wantErr := errors.New("invalid proxy configuration")
	base := &http.Transport{
		Proxy: func(*http.Request) (*url.URL, error) { return nil, wantErr },
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			t.Error("proxy failure caused direct connection")
			return nil, errors.New("unexpected direct connection")
		},
	}
	transport := newGoogleVideoTransport(base)
	defer transport.CloseIdleConnections()
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://rr1.googlevideo.com/videoplayback?ip=2001:db8::1", nil)
	if _, err := transport.RoundTrip(req); !errors.Is(err, wantErr) {
		t.Fatalf("lost proxy error: %v", err)
	}
}
