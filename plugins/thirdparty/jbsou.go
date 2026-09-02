package thirdparty

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/liuran001/MusicBot-Go/bot/platform"
)

const (
	defaultJBSouBaseURL = "https://www.jbsou.cn/"
	jbsouUserAgent      = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/151.0.0.0 Safari/537.36"
	maxJBSouBodyBytes   = 1 << 20
	jbsouProbeBytes     = 1024
)

type jbsouProvider struct {
	baseURL         *url.URL
	httpClient      *http.Client
	mediaURLAllowed func(*url.URL) bool
}

type jbsouResponse struct {
	Data  []jbsouTrack `json:"data"`
	Code  int          `json:"code"`
	Error string       `json:"error"`
}

type jbsouTrack struct {
	SongID string `json:"songid"`
	URL    string `json:"url"`
}

type jbsouMedia struct {
	URL  *url.URL
	Size int64
}

func newJBSouProvider(baseURL string, timeout time.Duration, client *http.Client, mediaURLAllowed func(*url.URL) bool) (*jbsouProvider, error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("jbsou: invalid base URL")
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	if client == nil {
		client = &http.Client{Timeout: timeout}
	} else {
		clone := *client
		client = &clone
		if client.Timeout <= 0 {
			client.Timeout = timeout
		}
	}
	if client.Jar == nil {
		jar, jarErr := cookiejar.New(nil)
		if jarErr != nil {
			return nil, fmt.Errorf("jbsou: create cookie jar: %w", jarErr)
		}
		client.Jar = jar
	}
	if mediaURLAllowed == nil {
		mediaURLAllowed = isQQMusicMediaURL
	}
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return fmt.Errorf("jbsou: too many redirects")
		}
		if sameOrigin(req.URL, parsed) || mediaURLAllowed(req.URL) {
			return nil
		}
		return fmt.Errorf("jbsou: redirect target is not an allowed QQ Music CDN")
	}
	return &jbsouProvider{baseURL: parsed, httpClient: client, mediaURLAllowed: mediaURLAllowed}, nil
}

func (p *jbsouProvider) Name() string { return "jbsou" }

func (p *jbsouProvider) Resolve(ctx context.Context, platformName, trackID string, _ platform.Quality) (*platform.DownloadInfo, error) {
	if p == nil || p.baseURL == nil || p.httpClient == nil {
		return nil, fmt.Errorf("jbsou: provider unavailable")
	}
	if strings.ToLower(strings.TrimSpace(platformName)) != "qqmusic" {
		return nil, fmt.Errorf("jbsou: unsupported platform %q", platformName)
	}
	trackID = strings.TrimSpace(trackID)
	if trackID == "" {
		return nil, fmt.Errorf("jbsou: empty QQ songmid")
	}

	if err := p.establishSession(ctx); err != nil {
		return nil, err
	}
	track, err := p.lookupQQTrack(ctx, trackID)
	if err != nil {
		return nil, err
	}
	mediaURL, err := p.resolveMediaURL(ctx, track.URL)
	if err != nil {
		return nil, err
	}
	format, bitrate, quality, err := classifyQQMusicMedia(mediaURL.URL)
	if err != nil {
		return nil, err
	}

	finalURL := mediaURL.URL.String()
	return &platform.DownloadInfo{
		URL:     finalURL,
		Headers: map[string]string{"User-Agent": jbsouUserAgent},
		Size:    mediaURL.Size,
		Format:  format,
		Bitrate: bitrate,
		Quality: quality,
		ValidateURL: func(rawURL string) error {
			parsed, parseErr := url.Parse(rawURL)
			if parseErr != nil || !p.mediaURLAllowed(parsed) {
				return fmt.Errorf("jbsou: download URL is not an allowed QQ Music CDN")
			}
			return nil
		},
	}, nil
}

func (p *jbsouProvider) establishSession(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL.String(), nil)
	if err != nil {
		return fmt.Errorf("jbsou: create session request: %w", err)
	}
	req.Header.Set("User-Agent", jbsouUserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("jbsou: establish session: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("jbsou: establish session returned HTTP %d", resp.StatusCode)
	}
	return nil
}

func (p *jbsouProvider) lookupQQTrack(ctx context.Context, trackID string) (*jbsouTrack, error) {
	form := url.Values{
		"input":  {trackID},
		"filter": {"id"},
		"type":   {"qq"},
		"page":   {"1"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL.String(), strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("jbsou: create lookup request: %w", err)
	}
	req.Header.Set("User-Agent", jbsouUserAgent)
	req.Header.Set("Accept", "application/json, text/javascript, */*; q=0.01")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
	req.Header.Set("Origin", originOf(p.baseURL))
	req.Header.Set("Referer", p.baseURL.String())
	req.Header.Set("X-Requested-With", "XMLHttpRequest")

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("jbsou: lookup request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("jbsou: lookup returned HTTP %d", resp.StatusCode)
	}
	var payload jbsouResponse
	decoder := json.NewDecoder(io.LimitReader(resp.Body, maxJBSouBodyBytes))
	if err := decoder.Decode(&payload); err != nil {
		return nil, fmt.Errorf("jbsou: decode lookup response: %w", err)
	}
	if payload.Code != http.StatusOK {
		return nil, fmt.Errorf("jbsou: lookup response code %d", payload.Code)
	}
	for i := range payload.Data {
		item := &payload.Data[i]
		if strings.TrimSpace(item.SongID) == trackID && strings.TrimSpace(item.URL) != "" {
			return item, nil
		}
	}
	return nil, fmt.Errorf("jbsou: exact QQ songmid match not found")
}

func (p *jbsouProvider) resolveMediaURL(ctx context.Context, raw string) (*jbsouMedia, error) {
	reference, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("jbsou: invalid media endpoint")
	}
	endpoint := p.baseURL.ResolveReference(reference)
	if !sameOrigin(endpoint, p.baseURL) {
		return nil, fmt.Errorf("jbsou: media endpoint is outside the provider origin")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("jbsou: create media request: %w", err)
	}
	req.Header.Set("User-Agent", jbsouUserAgent)
	req.Header.Set("Referer", p.baseURL.String())
	req.Header.Set("Range", fmt.Sprintf("bytes=0-%d", jbsouProbeBytes-1))
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("jbsou: resolve media URL: %w", err)
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		resp.Body.Close()
		return nil, fmt.Errorf("jbsou: media URL returned HTTP %d", resp.StatusCode)
	}
	if resp.Request == nil || resp.Request.URL == nil || !p.mediaURLAllowed(resp.Request.URL) {
		resp.Body.Close()
		return nil, fmt.Errorf("jbsou: media URL is not an allowed QQ Music CDN")
	}
	probe, readErr := io.ReadAll(io.LimitReader(resp.Body, jbsouProbeBytes))
	resp.Body.Close()
	if readErr != nil {
		return nil, fmt.Errorf("jbsou: read media probe: %w", readErr)
	}
	if !looksLikeAudio(probe) {
		return nil, fmt.Errorf("jbsou: media URL did not return recognizable audio data")
	}
	size := responseTotalSize(resp)
	if size <= 0 {
		return nil, fmt.Errorf("jbsou: QQ Music CDN did not provide a file size")
	}
	return &jbsouMedia{URL: resp.Request.URL, Size: size}, nil
}

func responseTotalSize(resp *http.Response) int64 {
	if resp == nil {
		return 0
	}
	contentRange := strings.TrimSpace(resp.Header.Get("Content-Range"))
	if slash := strings.LastIndex(contentRange, "/"); slash >= 0 && slash+1 < len(contentRange) {
		if total, err := strconv.ParseInt(strings.TrimSpace(contentRange[slash+1:]), 10, 64); err == nil && total > 0 {
			return total
		}
	}
	return resp.ContentLength
}

func looksLikeAudio(data []byte) bool {
	if len(data) >= 4 {
		if string(data[:4]) == "fLaC" || string(data[:4]) == "OggS" || string(data[:4]) == "RIFF" {
			return true
		}
	}
	if len(data) >= 3 && string(data[:3]) == "ID3" {
		return true
	}
	if len(data) >= 2 && data[0] == 0xff && data[1]&0xe0 == 0xe0 {
		return true
	}
	return len(data) >= 12 && string(data[4:8]) == "ftyp"
}

func classifyQQMusicMedia(mediaURL *url.URL) (string, int, platform.Quality, error) {
	if mediaURL == nil {
		return "", 0, platform.QualityStandard, fmt.Errorf("jbsou: empty media URL")
	}
	format := strings.ToLower(strings.TrimPrefix(path.Ext(mediaURL.Path), "."))
	allowedFormats := map[string]bool{"mp3": true, "flac": true, "m4a": true, "aac": true, "ogg": true}
	if !allowedFormats[format] {
		return "", 0, platform.QualityStandard, fmt.Errorf("jbsou: unsupported audio format %q", format)
	}
	filename := strings.ToUpper(path.Base(mediaURL.Path))
	prefix := filename
	if len(prefix) > 4 {
		prefix = prefix[:4]
	}
	switch prefix {
	case "RS01", "AI00", "Q000", "Q001":
		return format, platform.QualityHiRes.Bitrate(), platform.QualityHiRes, nil
	case "F000":
		return format, platform.QualityLossless.Bitrate(), platform.QualityLossless, nil
	case "M800", "O801", "O800":
		return format, platform.QualityHigh.Bitrate(), platform.QualityHigh, nil
	case "C600":
		return format, 192, platform.QualityStandard, nil
	case "M500":
		return format, platform.QualityStandard.Bitrate(), platform.QualityStandard, nil
	case "C400":
		return format, 96, platform.QualityStandard, nil
	}
	if format == "flac" {
		return format, platform.QualityLossless.Bitrate(), platform.QualityLossless, nil
	}
	return format, platform.QualityStandard.Bitrate(), platform.QualityStandard, nil
}

func isQQMusicMediaURL(candidate *url.URL) bool {
	if candidate == nil || !strings.EqualFold(candidate.Scheme, "https") {
		return false
	}
	host := strings.ToLower(candidate.Hostname())
	return host == "aqqmusic.tc.qq.com" || strings.HasSuffix(host, ".qqmusic.qq.com")
}

func sameOrigin(left, right *url.URL) bool {
	if left == nil || right == nil {
		return false
	}
	return strings.EqualFold(left.Scheme, right.Scheme) && strings.EqualFold(left.Host, right.Host)
}

func originOf(value *url.URL) string {
	if value == nil {
		return ""
	}
	return value.Scheme + "://" + value.Host
}
