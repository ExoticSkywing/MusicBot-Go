package kuwo

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/liuran001/MusicBot-Go/bot"
	"github.com/liuran001/MusicBot-Go/bot/httpproxy"
	"github.com/liuran001/MusicBot-Go/bot/platform"
)

const (
	kuwoHomeURL      = "https://www.kuwo.cn/"
	kuwoSearchURL    = "https://www.kuwo.cn/search/searchMusicBykeyWord"
	kuwoDetailURL    = "https://www.kuwo.cn/api/www/music/musicInfo"
	kuwoUserAgent    = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
	maxJSONBodyBytes = 4 << 20
)

type kuwoEndpoints struct {
	home   string
	search string
	detail string
}

type Client struct {
	clientMu        sync.RWMutex
	apiHTTPClient   *http.Client
	mediaHTTPClient *http.Client
	logger          bot.Logger
	endpoints       kuwoEndpoints
	now             func() time.Time

	sessionMu         sync.Mutex
	sessionExpires    time.Time
	sessionRefreshing bool
	sessionReady      chan struct{}
}

func NewClient(timeout time.Duration, logger bot.Logger) *Client {
	return newClientWithEndpoints(timeout, logger, kuwoEndpoints{home: kuwoHomeURL, search: kuwoSearchURL, detail: kuwoDetailURL})
}

// newClientWithEndpoints keeps endpoint and transport injection private to the
// package, while allowing deterministic httptest coverage of the request contract.
func newClientWithEndpoints(timeout time.Duration, logger bot.Logger, endpoints kuwoEndpoints) *Client {
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	jar, _ := cookiejar.New(nil)
	return &Client{
		apiHTTPClient:   &http.Client{Timeout: timeout, Jar: jar},
		mediaHTTPClient: &http.Client{Timeout: timeout},
		logger:          logger,
		endpoints:       endpoints,
		now:             time.Now,
	}
}

func (c *Client) httpClient() *http.Client {
	if c == nil {
		return nil
	}
	c.clientMu.RLock()
	defer c.clientMu.RUnlock()
	return c.apiHTTPClient
}

func (c *Client) SetAPIProxy(cfg httpproxy.Config) error {
	if c == nil {
		return nil
	}
	c.clientMu.Lock()
	defer c.clientMu.Unlock()
	timeout := 20 * time.Second
	if c.apiHTTPClient != nil && c.apiHTTPClient.Timeout > 0 {
		timeout = c.apiHTTPClient.Timeout
	}
	client, err := httpproxy.NewHTTPClient(cfg, timeout)
	if err != nil {
		return err
	}
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	if c.apiHTTPClient != nil && c.apiHTTPClient.Jar != nil {
		client.Jar = c.apiHTTPClient.Jar
	} else {
		client.Jar, _ = cookiejar.New(nil)
	}
	c.apiHTTPClient = client
	return nil
}

func (c *Client) Search(ctx context.Context, query string, limit int) ([]platform.Track, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return []platform.Track{}, nil
	}
	if limit <= 0 {
		limit = 10
	} else if limit > 50 {
		limit = 50
	}
	endpoint, err := url.Parse(c.endpoints.search)
	if err != nil {
		return nil, fmt.Errorf("kuwo: parse search URL: %w", err)
	}
	values := endpoint.Query()
	for key, value := range map[string]string{
		"vipver": "1", "client": "kt", "ft": "music", "cluster": "0", "strategy": "2012", "encoding": "utf8", "rformat": "json", "mobi": "1", "issubtitle": "1", "show_copyright_off": "1", "pn": "0",
	} {
		values.Set(key, value)
	}
	values.Set("rn", fmt.Sprintf("%d", limit))
	values.Set("all", query)
	endpoint.RawQuery = values.Encode()
	body, err := c.signedGet(ctx, endpoint.String(), "https://www.kuwo.cn/search/list?key="+url.QueryEscape(query))
	if err != nil {
		return nil, err
	}
	var response struct {
		Data struct {
			List []trackWire `json:"list"`
		} `json:"data"`
		AbsList []trackWire `json:"abslist"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("kuwo: decode search response: %w", err)
	}
	items := response.Data.List
	if len(items) == 0 {
		items = response.AbsList
	}
	tracks := make([]platform.Track, 0, len(items))
	for _, item := range items {
		track, _, ok := convertTrack(item)
		if ok {
			tracks = append(tracks, track.Track)
		}
	}
	if len(tracks) > limit {
		tracks = tracks[:limit]
	}
	return tracks, nil
}

func (c *Client) GetTrack(ctx context.Context, trackID string) (*platform.Track, error) {
	detail, _, err := c.getTrackDetail(ctx, trackID)
	if err != nil {
		return nil, err
	}
	return &detail.Track, nil
}

func (c *Client) getTrackDetail(ctx context.Context, trackID string) (*trackDetail, trackAccess, error) {
	trackID = normalizeRID(trackID)
	if trackID == "" {
		return nil, trackAccess{}, platform.NewNotFoundError("kuwo", "track", trackID)
	}
	endpoint, err := url.Parse(c.endpoints.detail)
	if err != nil {
		return nil, trackAccess{}, fmt.Errorf("kuwo: parse detail URL: %w", err)
	}
	values := endpoint.Query()
	values.Set("mid", trackID)
	values.Set("httpsStatus", "1")
	endpoint.RawQuery = values.Encode()
	body, err := c.signedGet(ctx, endpoint.String(), kuwoHomeURL)
	if err != nil {
		return nil, trackAccess{}, err
	}
	var response struct {
		Data trackWire `json:"data"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, trackAccess{}, fmt.Errorf("kuwo: decode track response: %w", err)
	}
	detail, access, ok := convertTrack(response.Data)
	if !ok {
		return nil, access, platform.NewNotFoundError("kuwo", "track", trackID)
	}
	if detail.ID != trackID {
		return nil, access, platform.NewUnavailableError("kuwo", "track", trackID)
	}
	return &detail, access, nil
}

func (c *Client) signedGet(ctx context.Context, endpoint, referer string) ([]byte, error) {
	for attempt := 0; attempt < 2; attempt++ {
		if err := c.ensureSessionForURL(ctx, endpoint); err != nil {
			return nil, err
		}
		cookie := c.sessionCookie(endpoint)
		if !validSessionCookie(cookie) {
			return nil, fmt.Errorf("kuwo: missing valid session cookie")
		}
		nonce, err := randomNonce()
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, fmt.Errorf("kuwo: create signed request: %w", err)
		}
		req.Header.Set("Secret", buildSecret(cookie, nonce))
		req.Header.Set("Referer", referer)
		req.Header.Set("User-Agent", kuwoUserAgent)
		reqID, err := uuidV4()
		if err != nil {
			return nil, err
		}
		requestURL, err := url.Parse(endpoint)
		if err != nil {
			return nil, fmt.Errorf("kuwo: parse signed request URL: %w", err)
		}
		query := requestURL.Query()
		query.Set("reqId", reqID)
		requestURL.RawQuery = query.Encode()
		req.URL = requestURL
		resp, err := c.httpClient().Do(req)
		if err != nil {
			return nil, fmt.Errorf("kuwo: request API: %w", err)
		}
		body, readErr := readLimited(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		if resp.StatusCode == http.StatusTooManyRequests {
			return nil, platform.NewRateLimitedError("kuwo")
		}
		if invalidSessionResponse(resp.StatusCode, body) {
			if attempt == 0 {
				c.invalidateSession(endpoint)
				continue
			}
			return nil, fmt.Errorf("kuwo: API session invalid after refresh (HTTP %d)", resp.StatusCode)
		}
		if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
			return nil, fmt.Errorf("kuwo: API returned HTTP %d", resp.StatusCode)
		}
		return body, nil
	}
	return nil, fmt.Errorf("kuwo: session refresh retry exhausted")
}

func readLimited(body io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, maxJSONBodyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("kuwo: read response: %w", err)
	}
	if len(data) > maxJSONBodyBytes {
		return nil, fmt.Errorf("kuwo: response too large")
	}
	return data, nil
}

func invalidSessionResponse(status int, body []byte) bool {
	return status == http.StatusUnauthorized || status == http.StatusForbidden || bytes.Contains(bytes.ToLower(body), []byte("the request is illegal!"))
}

func uuidV4() (string, error) {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", fmt.Errorf("kuwo: generate request ID: %w", err)
	}
	data[6] = data[6]&0x0f | 0x40
	data[8] = data[8]&0x3f | 0x80
	encoded := make([]byte, 36)
	hex.Encode(encoded[0:8], data[0:4])
	encoded[8] = '-'
	hex.Encode(encoded[9:13], data[4:6])
	encoded[13] = '-'
	hex.Encode(encoded[14:18], data[6:8])
	encoded[18] = '-'
	hex.Encode(encoded[19:23], data[8:10])
	encoded[23] = '-'
	hex.Encode(encoded[24:36], data[10:16])
	return string(encoded), nil
}
