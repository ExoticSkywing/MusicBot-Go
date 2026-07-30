package kuwo

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/liuran001/MusicBot-Go/bot"
	"github.com/liuran001/MusicBot-Go/bot/httpproxy"
	"github.com/liuran001/MusicBot-Go/bot/platform"
)

const (
	kuwoHomeURL         = "https://www.kuwo.cn/"
	kuwoSearchURL       = "https://www.kuwo.cn/search/searchMusicBykeyWord"
	kuwoDetailURL       = "https://www.kuwo.cn/api/www/music/musicInfo"
	kuwoPlaylistURL     = "https://www.kuwo.cn/api/www/playlist/playListInfo"
	kuwoWordLyricURL    = "https://newlyric.kuwo.cn/newlyric.lrc"
	kuwoMobileLyricURL  = "https://m.kuwo.cn/newh5/singles/songinfoandlrc"
	kuwoUserAgent       = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
	maxJSONBodyBytes    = 4 << 20
	maxKuwoPlaylistPage = math.MaxInt32
)

type kuwoEndpoints struct {
	home        string
	search      string
	detail      string
	playlist    string
	mobile      string
	play        string
	wordLyric   string
	mobileLyric string
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
	return newClientWithEndpoints(timeout, logger, kuwoEndpoints{
		home:        kuwoHomeURL,
		search:      kuwoSearchURL,
		detail:      kuwoDetailURL,
		playlist:    kuwoPlaylistURL,
		wordLyric:   kuwoWordLyricURL,
		mobileLyric: kuwoMobileLyricURL,
	})
}

type playlistResponse struct {
	Code jsonScalar    `json:"code"`
	Data *playlistWire `json:"data"`
}

type playlistWire struct {
	ID        jsonScalar  `json:"id"`
	Name      jsonScalar  `json:"name"`
	Desc      jsonScalar  `json:"desc"`
	Info      jsonScalar  `json:"info"`
	Img700    jsonScalar  `json:"img700"`
	Img500    jsonScalar  `json:"img500"`
	Img300    jsonScalar  `json:"img300"`
	Img       jsonScalar  `json:"img"`
	UserName  jsonScalar  `json:"userName"`
	UName     jsonScalar  `json:"uname"`
	Total     jsonScalar  `json:"total"`
	MusicList []trackWire `json:"musicList"`
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

func (c *Client) sessionlessAPIClient() *http.Client {
	if c == nil {
		return &http.Client{Timeout: 20 * time.Second}
	}
	c.clientMu.RLock()
	defer c.clientMu.RUnlock()
	if c.apiHTTPClient == nil {
		return &http.Client{Timeout: 20 * time.Second}
	}
	snapshot := *c.apiHTTPClient
	snapshot.Jar = nil
	return &snapshot
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

func (c *Client) GetPlaylist(
	ctx context.Context,
	playlistID string,
	offset, limit int,
) (*platform.Playlist, error) {
	playlistID = strings.TrimSpace(playlistID)
	if !isASCIIUnsignedDecimal(playlistID, 20) {
		return nil, platform.NewNotFoundError("kuwo", "playlist", playlistID)
	}
	if c == nil {
		return nil, platform.NewUnavailableError("kuwo", "playlist", playlistID)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = 50
	} else if limit > 100 {
		limit = 100
	}
	if offset > math.MaxInt-limit {
		return nil, platform.NewUnavailableError("kuwo", "playlist", playlistID)
	}

	page, skip, ok := playlistPageWindow(uint64(offset), limit)
	if !ok {
		return nil, platform.NewUnavailableError("kuwo", "playlist", playlistID)
	}

	first, total, err := c.fetchPlaylistPage(ctx, playlistID, page, limit)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	firstStart := skip
	if firstStart > len(first.MusicList) {
		firstStart = len(first.MusicList)
	}
	rawWindow := append([]trackWire(nil), first.MusicList[firstStart:]...)

	pageBase := int64(page-1) * int64(limit)
	needSecond := false
	if skip > 0 && int64(total) > pageBase {
		needSecond = int64(total)-pageBase > int64(limit)
	}
	if needSecond {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		second, secondTotal, err := c.fetchPlaylistPage(ctx, playlistID, page+1, limit)
		if err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if secondTotal != total {
			return nil, playlistUnavailable(playlistID, "playlist total changed between pages")
		}
		secondCount := skip
		if secondCount > len(second.MusicList) {
			secondCount = len(second.MusicList)
		}
		rawWindow = append(rawWindow, second.MusicList[:secondCount]...)
	}
	if len(rawWindow) > limit {
		rawWindow = rawWindow[:limit]
	}

	tracks := make([]platform.Track, 0, len(rawWindow))
	for _, item := range rawWindow {
		detail, _, ok := convertTrack(item)
		if ok {
			tracks = append(tracks, detail.Track)
		}
	}

	return &platform.Playlist{
		ID:          playlistID,
		Platform:    "kuwo",
		Title:       scalarText(first.Name),
		Description: firstScalarText(first.Desc, first.Info),
		CoverURL:    firstScalarText(first.Img700, first.Img500, first.Img300, first.Img),
		Creator:     firstScalarText(first.UserName, first.UName),
		TrackCount:  total,
		Tracks:      tracks,
		URL:         "https://www.kuwo.cn/playlist_detail/" + playlistID,
	}, nil
}

func (c *Client) fetchPlaylistPage(
	ctx context.Context,
	playlistID string,
	page, limit int,
) (*playlistWire, int, error) {
	if page < 1 || page > maxKuwoPlaylistPage || limit < 1 || limit > 100 {
		return nil, 0, platform.NewUnavailableError("kuwo", "playlist", playlistID)
	}
	endpoint := kuwoPlaylistURL
	if c != nil && strings.TrimSpace(c.endpoints.playlist) != "" {
		endpoint = c.endpoints.playlist
	}
	requestURL, err := url.Parse(endpoint)
	if err != nil {
		return nil, 0, fmt.Errorf("kuwo: parse playlist URL: %w", err)
	}
	query := requestURL.Query()
	query.Set("pid", playlistID)
	query.Set("pn", strconv.Itoa(page))
	query.Set("rn", strconv.Itoa(limit))
	query.Set("httpsStatus", "1")
	requestURL.RawQuery = query.Encode()

	body, err := c.signedGet(ctx, requestURL.String(), kuwoHomeURL)
	if err != nil {
		return nil, 0, err
	}
	var response playlistResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, 0, fmt.Errorf("kuwo: decode playlist response: %w", err)
	}
	code, ok := playlistResponseCode(response.Code)
	if ok && code == -1 {
		return nil, 0, platform.NewNotFoundError("kuwo", "playlist", playlistID)
	}
	if !ok || code != 200 || response.Data == nil {
		return nil, 0, playlistUnavailable(playlistID, "invalid playlist response")
	}
	responseID, ok := scalarASCIIUnsignedDecimal(response.Data.ID, 20)
	if !ok || responseID != playlistID {
		return nil, 0, playlistUnavailable(playlistID, "playlist identity mismatch")
	}
	total, ok := scalarNonNegativeInt(response.Data.Total)
	if !ok {
		return nil, 0, playlistUnavailable(playlistID, "invalid playlist total")
	}

	pageBase := int64(page-1) * int64(limit)
	remaining := 0
	if int64(total) > pageBase {
		difference := int64(total) - pageBase
		if difference > int64(limit) {
			remaining = limit
		} else {
			remaining = int(difference)
		}
	}
	if len(response.Data.MusicList) != remaining {
		return nil, 0, playlistUnavailable(
			playlistID,
			fmt.Sprintf("playlist page length %d does not match expected %d", len(response.Data.MusicList), remaining),
		)
	}
	return response.Data, total, nil
}

func isASCIIUnsignedDecimal(value string, maxDigits int) bool {
	if len(value) == 0 || len(value) > maxDigits {
		return false
	}
	for index := 0; index < len(value); index++ {
		if value[index] < '0' || value[index] > '9' {
			return false
		}
	}
	return true
}

func playlistPageWindow(offset uint64, limit int) (page, skip int, ok bool) {
	if limit < 1 || limit > 100 {
		return 0, 0, false
	}
	unsignedLimit := uint64(limit)
	pageZero := offset / unsignedLimit
	if pageZero >= uint64(maxKuwoPlaylistPage) {
		return 0, 0, false
	}
	page = int(pageZero) + 1
	skip = int(offset % unsignedLimit)
	if skip > 0 && page == maxKuwoPlaylistPage {
		return 0, 0, false
	}
	return page, skip, true
}

func playlistResponseCode(value jsonScalar) (int64, bool) {
	decoded, ok := value.value()
	if !ok {
		return 0, false
	}
	var text string
	switch decoded := decoded.(type) {
	case string:
		text = decoded
	case json.Number:
		text = decoded.String()
	default:
		return 0, false
	}
	switch text {
	case "-1":
		return -1, true
	case "200":
		return 200, true
	default:
		return 0, false
	}
}

func scalarASCIIUnsignedDecimal(value jsonScalar, maxDigits int) (string, bool) {
	decoded, ok := value.value()
	if !ok {
		return "", false
	}
	var text string
	switch decoded := decoded.(type) {
	case string:
		text = decoded
	case json.Number:
		text = decoded.String()
	default:
		return "", false
	}
	return text, isASCIIUnsignedDecimal(text, maxDigits)
}

func scalarNonNegativeInt(value jsonScalar) (int, bool) {
	text, ok := scalarASCIIUnsignedDecimal(value, 20)
	if !ok {
		return 0, false
	}
	parsed, err := strconv.ParseUint(text, 10, 64)
	if err != nil || parsed > uint64(math.MaxInt) {
		return 0, false
	}
	return int(parsed), true
}

func firstScalarText(values ...jsonScalar) string {
	for _, value := range values {
		if text := scalarText(value); text != "" {
			return text
		}
	}
	return ""
}

func playlistUnavailable(playlistID, reason string) error {
	base := platform.NewUnavailableError("kuwo", "playlist", playlistID)
	if strings.TrimSpace(reason) == "" {
		return base
	}
	return fmt.Errorf("kuwo: %s: %w", reason, base)
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
		snapshot, err := c.signedSessionSnapshot(ctx, endpoint)
		if err != nil {
			return nil, err
		}
		nonce, err := randomNonce()
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, fmt.Errorf("kuwo: create signed request: %w", err)
		}
		req.Header.Set("Secret", buildSecret(snapshot.cookie, nonce))
		req.Header.Set("Referer", referer)
		req.Header.Set("User-Agent", kuwoUserAgent)
		for _, cookie := range snapshot.cookies {
			req.AddCookie(cookie)
		}
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
		// Do must not consult the shared Jar again: the explicitly attached
		// cookie snapshot is the same state used to derive Secret.
		requestClient := *snapshot.client
		requestClient.Jar = nil
		resp, err := requestClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("kuwo: request API: %w", err)
		}
		// The request-scoped client intentionally has no Jar, so preserve normal
		// session rotation for later requests through the captured shared Jar.
		snapshot.jar.SetCookies(requestURL, resp.Cookies())
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

type signedSessionSnapshot struct {
	client  *http.Client
	jar     http.CookieJar
	cookies []*http.Cookie
	cookie  string
}

func (c *Client) signedSessionSnapshot(ctx context.Context, endpoint string) (signedSessionSnapshot, error) {
	for refresh := 0; refresh < 2; refresh++ {
		if err := c.ensureSessionForURL(ctx, endpoint); err != nil {
			return signedSessionSnapshot{}, err
		}
		client, jar, cookies := c.sessionClientCookies(endpoint)
		cookie, filteredCookies := selectedSessionCookie(cookies)
		if cookie != "" {
			return signedSessionSnapshot{client: client, jar: jar, cookies: filteredCookies, cookie: cookie}, nil
		}
		// An invalidation may replace the client/Jar pair after ensureSession
		// returns. Re-enter session establishment instead of signing with an
		// empty replacement jar.
	}
	return signedSessionSnapshot{}, fmt.Errorf("kuwo: missing valid session cookie")
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
