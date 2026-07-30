package kuwo

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/liuran001/MusicBot-Go/bot/config"
	"github.com/liuran001/MusicBot-Go/bot/platform"
	platformplugins "github.com/liuran001/MusicBot-Go/bot/platform/plugins"
)

const (
	factoryVirtualHome     = "http://kuwo-origin.test.invalid/"
	factoryVirtualPlaylist = "http://kuwo-origin.test.invalid/api/www/playlist/playListInfo"
)

var factoryUUIDV4Pattern = regexp.MustCompile(
	`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`,
)

func TestKuwoFactoryRegistrationAndDefaults(t *testing.T) {
	factory := registeredKuwoFactory(t)
	if contribution, err := factory(nil, nil); err == nil || contribution != nil {
		t.Fatalf("factory(nil) = %#v, %v; want nil contribution and error", contribution, err)
	}

	tests := []struct {
		name        string
		pluginINI   string
		wantTimeout time.Duration
	}{
		{name: "timeout unset", wantTimeout: 20 * time.Second},
		{
			name: "timeout zero",
			pluginINI: `[plugins.kuwo]
timeout = 0
`,
			wantTimeout: 20 * time.Second,
		},
		{
			name: "timeout negative",
			pluginINI: `[plugins.kuwo]
timeout = -7
`,
			wantTimeout: 20 * time.Second,
		},
		{
			name: "timeout positive",
			pluginINI: `[plugins.kuwo]
timeout = 7
`,
			wantTimeout: 7 * time.Second,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := loadKuwoFactoryConfig(t, test.pluginINI)
			contribution, err := factory(cfg, nil)
			if err != nil {
				t.Fatalf("factory() = %v", err)
			}
			if contribution == nil {
				t.Fatal("factory() returned nil contribution")
			}

			providedPlatforms := len(contribution.Platforms)
			if contribution.Platform != nil {
				providedPlatforms++
			}
			if providedPlatforms != 1 {
				t.Fatalf("factory() contributed %d platforms, want exactly 1", providedPlatforms)
			}
			if contribution.Platform == nil || contribution.Platform.Name() != "kuwo" {
				t.Fatalf("factory platform = %#v, want Name() == kuwo", contribution.Platform)
			}

			kuwoPlatform, ok := contribution.Platform.(*KuwoPlatform)
			if !ok || kuwoPlatform.client == nil {
				t.Fatalf("factory platform = %T, want initialized *KuwoPlatform", contribution.Platform)
			}
			kuwoPlatform.client.clientMu.RLock()
			apiClient := kuwoPlatform.client.apiHTTPClient
			mediaClient := kuwoPlatform.client.mediaHTTPClient
			kuwoPlatform.client.clientMu.RUnlock()
			if apiClient == nil || apiClient.Timeout != test.wantTimeout {
				t.Fatalf("API timeout = %v, want %v", clientTimeout(apiClient), test.wantTimeout)
			}
			if mediaClient == nil || mediaClient.Timeout != test.wantTimeout {
				t.Fatalf("media timeout = %v, want %v", clientTimeout(mediaClient), test.wantTimeout)
			}
		})
	}
}

func TestKuwoFactoryPropagatesProxyConfigurationError(t *testing.T) {
	factory := registeredKuwoFactory(t)
	cfg := loadKuwoFactoryConfig(t, `[plugins.kuwo]
api_proxy_enabled = true
api_proxy_type = unsupported-test-proxy
api_proxy_host = 127.0.0.1
api_proxy_port = 1
`)

	contribution, err := factory(cfg, nil)
	if contribution != nil || err == nil {
		t.Fatalf("factory() = %#v, %v; want proxy configuration error", contribution, err)
	}
	if !strings.Contains(err.Error(), "unsupported api proxy type") {
		t.Fatalf("factory error = %q, want unsupported proxy type", err)
	}
}

func TestKuwoFactoryDirectControlFailsWithoutProxy(t *testing.T) {
	var proxyCalls atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		proxyCalls.Add(1)
		http.Error(w, "unexpected proxy request", http.StatusBadGateway)
	}))
	defer proxy.Close()

	errDirectDialSentinel := errors.New("direct dial blocked by test")
	var directDialMu sync.Mutex
	var directDialNetwork string
	var directDialAddress string
	directTransport := &http.Transport{
		Proxy: nil,
		DialContext: func(
			_ context.Context,
			network, address string,
		) (net.Conn, error) {
			directDialMu.Lock()
			directDialNetwork = network
			directDialAddress = address
			directDialMu.Unlock()
			return nil, errDirectDialSentinel
		},
	}
	defer directTransport.CloseIdleConnections()

	client := newClientWithEndpoints(time.Second, nil, kuwoEndpoints{
		home:     factoryVirtualHome,
		playlist: factoryVirtualPlaylist,
	})
	client.clientMu.Lock()
	client.apiHTTPClient.Transport = directTransport
	client.clientMu.Unlock()

	playlist, err := client.GetPlaylist(context.Background(), "123", 0, 1)
	if playlist != nil || !errors.Is(err, errDirectDialSentinel) {
		t.Fatalf("GetPlaylist() = %#v, %v; want direct dial sentinel", playlist, err)
	}
	directDialMu.Lock()
	gotNetwork := directDialNetwork
	gotAddress := directDialAddress
	directDialMu.Unlock()
	if gotNetwork != "tcp" || gotAddress != "kuwo-origin.test.invalid:80" {
		t.Fatalf("direct dial = %q %q, want tcp kuwo-origin.test.invalid:80", gotNetwork, gotAddress)
	}
	if got := proxyCalls.Load(); got != 0 {
		t.Fatalf("direct control made %d proxy requests, want 0", got)
	}
}

func TestKuwoFactoryRoutesSignedPlaylistThroughAPIProxy(t *testing.T) {
	const (
		playlistID   = "123"
		trackID      = "456"
		sessionValue = "abcdefghijklmnop"
	)

	var proxyCalls atomic.Int32
	var requestsMu sync.Mutex
	var requests []factoryProxyRequest
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		call := proxyCalls.Add(1)
		requestsMu.Lock()
		requests = append(requests, snapshotFactoryProxyRequest(request))
		requestsMu.Unlock()

		switch call {
		case 1:
			http.SetCookie(w, &http.Cookie{
				Name:  kuwoSessionCookie,
				Value: sessionValue,
				Path:  "/",
			})
			w.WriteHeader(http.StatusOK)
		case 2:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(fmt.Sprintf(
				`{"code":200,"data":{"id":%q,"name":"Factory playlist","total":1,"musicList":[{"rid":%q,"name":"Factory track","artist":"Factory artist","duration":"180"}]}}`,
				playlistID,
				trackID,
			)))
		default:
			http.Error(w, "unexpected extra request", http.StatusBadGateway)
		}
	}))
	defer proxy.Close()

	proxyURL, err := url.Parse(proxy.URL)
	if err != nil {
		t.Fatalf("parse proxy URL: %v", err)
	}
	proxyHost, proxyPortText, err := net.SplitHostPort(proxyURL.Host)
	if err != nil {
		t.Fatalf("split proxy address: %v", err)
	}
	proxyPort, err := strconv.Atoi(proxyPortText)
	if err != nil {
		t.Fatalf("parse proxy port: %v", err)
	}

	cfg := loadKuwoFactoryConfig(t, fmt.Sprintf(`[plugins.kuwo]
timeout = 2
api_proxy_enabled = true
api_proxy_type = http
api_proxy_host = %s
api_proxy_port = %d
`, proxyHost, proxyPort))
	factory := registeredKuwoFactory(t)
	contribution, err := factory(cfg, nil)
	if err != nil {
		t.Fatalf("factory() = %v", err)
	}
	kuwoPlatform, ok := contribution.Platform.(*KuwoPlatform)
	if !ok || kuwoPlatform.client == nil {
		t.Fatalf("factory platform = %T, want initialized *KuwoPlatform", contribution.Platform)
	}
	kuwoPlatform.client.endpoints.home = factoryVirtualHome
	kuwoPlatform.client.endpoints.playlist = factoryVirtualPlaylist

	ctx := platform.WithPlaylistLimit(context.Background(), 1)
	playlist, err := contribution.Platform.GetPlaylist(ctx, playlistID)
	if err != nil {
		t.Fatalf("GetPlaylist() through factory proxy = %v", err)
	}
	if playlist == nil || playlist.ID != playlistID || playlist.Platform != "kuwo" ||
		playlist.TrackCount != 1 || len(playlist.Tracks) != 1 ||
		playlist.Tracks[0].ID != trackID {
		t.Fatalf("playlist = %#v, want one exact factory fixture track", playlist)
	}

	if got := proxyCalls.Load(); got != 2 {
		t.Fatalf("proxy calls = %d, want exactly 2", got)
	}
	requestsMu.Lock()
	gotRequests := append([]factoryProxyRequest(nil), requests...)
	requestsMu.Unlock()
	if len(gotRequests) != 2 {
		t.Fatalf("captured proxy requests = %d, want 2", len(gotRequests))
	}
	assertFactoryAbsoluteProxyRequest(t, gotRequests[0], factoryVirtualHome)

	signedRequest := gotRequests[1]
	if signedRequest.method != http.MethodGet ||
		!signedRequest.urlIsAbs ||
		signedRequest.urlScheme != "http" ||
		signedRequest.urlHost != "kuwo-origin.test.invalid" ||
		signedRequest.host != "kuwo-origin.test.invalid" ||
		signedRequest.urlPath != "/api/www/playlist/playListInfo" ||
		!strings.HasPrefix(signedRequest.requestURI, factoryVirtualPlaylist+"?") {
		t.Fatalf("signed proxy request did not use the expected absolute-form virtual origin")
	}

	query, err := url.ParseQuery(signedRequest.rawQuery)
	if err != nil {
		t.Fatalf("parse signed query: %v", err)
	}
	for key, want := range map[string]string{
		"pid":         playlistID,
		"pn":          "1",
		"rn":          "1",
		"httpsStatus": "1",
	} {
		values := query[key]
		if len(values) != 1 || values[0] != want {
			t.Fatalf("query %s = %v, want exactly [%s]", key, values, want)
		}
	}
	reqIDValues := query["reqId"]
	if len(reqIDValues) != 1 || !factoryUUIDV4Pattern.MatchString(reqIDValues[0]) {
		t.Fatal("reqId is not exactly one UUID v4 value")
	}
	if len(query) != 5 {
		t.Fatalf("signed query has %d keys, want exactly pid/pn/rn/httpsStatus/reqId", len(query))
	}
	wantQuery := url.Values{
		"httpsStatus": {"1"},
		"pid":         {playlistID},
		"pn":          {"1"},
		"reqId":       {reqIDValues[0]},
		"rn":          {"1"},
	}.Encode()
	if signedRequest.requestURI != factoryVirtualPlaylist+"?"+wantQuery {
		t.Fatal("signed proxy RequestURI did not contain the exact absolute-form query")
	}

	if got := signedRequest.header.Get("Cookie"); got != kuwoSessionCookie+"="+sessionValue {
		t.Fatal("signed request did not carry the exact homepage session cookie")
	}
	if got := signedRequest.header.Get("Referer"); got != kuwoHomeURL {
		t.Fatalf("Referer = %q, want %q", got, kuwoHomeURL)
	}
	if got := signedRequest.header.Get("User-Agent"); got != kuwoUserAgent {
		t.Fatal("signed request did not carry the Kuwo User-Agent")
	}
	if !validFactorySecret(signedRequest.header.Get("Secret"), sessionValue) {
		t.Fatal("signed request Secret was missing or did not derive from the session cookie")
	}
}

type factoryProxyRequest struct {
	method     string
	requestURI string
	host       string
	urlIsAbs   bool
	urlScheme  string
	urlHost    string
	urlPath    string
	rawQuery   string
	header     http.Header
}

func snapshotFactoryProxyRequest(request *http.Request) factoryProxyRequest {
	return factoryProxyRequest{
		method:     request.Method,
		requestURI: request.RequestURI,
		host:       request.Host,
		urlIsAbs:   request.URL.IsAbs(),
		urlScheme:  request.URL.Scheme,
		urlHost:    request.URL.Host,
		urlPath:    request.URL.Path,
		rawQuery:   request.URL.RawQuery,
		header:     request.Header.Clone(),
	}
}

func assertFactoryAbsoluteProxyRequest(
	t *testing.T,
	request factoryProxyRequest,
	wantURI string,
) {
	t.Helper()
	if request.method != http.MethodGet ||
		!request.urlIsAbs ||
		request.urlScheme != "http" ||
		request.urlHost != "kuwo-origin.test.invalid" ||
		request.host != "kuwo-origin.test.invalid" ||
		request.urlPath != "/" ||
		request.rawQuery != "" ||
		request.requestURI != wantURI {
		t.Fatal("homepage proxy request did not use the exact absolute-form virtual origin")
	}
}

func validFactorySecret(secret, cookie string) bool {
	prefixBytes := len(cookie) * 2
	if len(secret) != prefixBytes+8 {
		return false
	}
	nonce, err := strconv.ParseUint(secret[prefixBytes:], 16, 32)
	if err != nil || nonce < 10000000 || nonce > 99999999 {
		return false
	}
	return secret == buildSecret(cookie, int(nonce))
}

func registeredKuwoFactory(t *testing.T) platformplugins.Factory {
	t.Helper()
	factory, ok := platformplugins.Get("kuwo")
	if !ok || factory == nil {
		t.Fatal("kuwo factory is not registered")
	}
	return factory
}

func loadKuwoFactoryConfig(t *testing.T, pluginINI string) *config.Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.ini")
	content := "BOT_TOKEN = test-token\n" + pluginINI
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	return cfg
}

func clientTimeout(client *http.Client) time.Duration {
	if client == nil {
		return 0
	}
	return client.Timeout
}
