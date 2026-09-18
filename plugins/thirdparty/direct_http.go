package thirdparty

// Shared transport checks for direct-URL providers. Keep API-specific request
// fields, signing and response structs in each provider's own file.
import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/liuran001/MusicBot-Go/bot/platform"
)

const sourceUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/151.0.0.0 Safari/537.36"

type directSourceHTTP struct {
	base  *url.URL
	api   *http.Client
	media *http.Client
}

func newDirectSourceHTTP(baseURL string, timeout time.Duration, client *http.Client) (*directSourceHTTP, error) {
	base, err := url.Parse(baseURL)
	if err != nil || base.Scheme != "https" || base.Host == "" || base.User != nil {
		return nil, fmt.Errorf("third-party: invalid API origin")
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	api, media := *client, *client
	api.Timeout, media.Timeout = timeout, timeout
	// Never share provider session cookies or signed API headers with a CDN.
	api.Jar, media.Jar = nil, nil
	api.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 || req.URL.User != nil || !sameOrigin(req.URL, base) {
			return fmt.Errorf("third-party: API redirect outside provider origin")
		}
		return nil
	}
	media.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 || !isDirectSourceMediaURL(req.URL) {
			return fmt.Errorf("third-party: media redirect outside allowed music CDNs")
		}
		return nil
	}
	return &directSourceHTTP{base: base, api: &api, media: &media}, nil
}

func (h *directSourceHTTP) endpoint(path string, query url.Values) string {
	u := *h.base
	u.Path, u.RawQuery = path, query.Encode()
	return u.String()
}

// decodeSourceJSON accepts only bounded JSON, optionally preceded by a BOM.
// Do not log response bodies: they can contain session keys or signed URLs.
func decodeSourceJSON(client *http.Client, req *http.Request, out any) error {
	resp, err := client.Do(req)
	if err != nil {
		return sourceRequestError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("third-party: API returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxJBSouBodyBytes+1))
	if err != nil {
		return sourceRequestError(err)
	}
	if len(body) > maxJBSouBodyBytes {
		return fmt.Errorf("third-party: API response exceeds size limit")
	}
	if err := json.Unmarshal(bytes.TrimPrefix(body, []byte{0xef, 0xbb, 0xbf}), out); err != nil {
		return fmt.Errorf("third-party: invalid JSON response")
	}
	return nil
}

func sourceRequestError(err error) error {
	// url.Error includes the complete URL (and potentially a CDN token).
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return urlErr.Err
	}
	return err
}

func normalizeSourceTrackID(platformName, raw string) (string, error) {
	id := strings.TrimSpace(raw)
	if platformName == "kugou" {
		id = normalizeKugouTrackID(id)
	}
	if id == "" || len(id) > 128 {
		return "", fmt.Errorf("third-party: invalid track ID")
	}
	for _, c := range id {
		if c >= '0' && c <= '9' {
			continue
		}
		if platformName != "kuwo" && ((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')) {
			continue
		}
		return "", fmt.Errorf("third-party: invalid track ID")
	}
	return id, nil
}

func isDirectSourceMediaURL(u *url.URL) bool {
	if u == nil || u.User != nil || u.Opaque != "" || u.Fragment != "" {
		return false
	}
	if u.Scheme == "http" {
		// Observed qqovo/lzmhhh Kugou CDN. Its HTTPS certificate does not
		// validate; allow this exact HTTP host, never disable TLS verification.
		return strings.EqualFold(u.Hostname(), "fs.youthandroid2.kugou.com") && (u.Port() == "" || u.Port() == "80")
	}
	if u.Scheme != "https" || (u.Port() != "" && u.Port() != "443") {
		return false
	}
	return isJBSouMediaURL(u) || strings.EqualFold(u.Hostname(), "car-er.kuwo.cn")
}

func (h *directSourceHTTP) downloadInfo(ctx context.Context, rawURL string) (*platform.DownloadInfo, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || !isDirectSourceMediaURL(u) {
		return nil, fmt.Errorf("third-party: audio URL outside allowed music CDNs")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("third-party: invalid audio URL")
	}
	req.Header.Set("User-Agent", sourceUserAgent)
	req.Header.Set("Range", "bytes=0-1023")
	resp, err := h.media.Do(req)
	if err != nil {
		return nil, sourceRequestError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return nil, fmt.Errorf("third-party: media returned HTTP %d", resp.StatusCode)
	}
	if resp.Request == nil || !isDirectSourceMediaURL(resp.Request.URL) {
		return nil, fmt.Errorf("third-party: invalid final CDN URL")
	}
	probe, err := io.ReadAll(io.LimitReader(resp.Body, jbsouProbeBytes))
	if err != nil {
		return nil, sourceRequestError(err)
	}
	if !looksLikeAudio(probe) {
		return nil, fmt.Errorf("third-party: media response is not recognizable audio")
	}
	size := responseTotalSize(resp)
	if size <= 0 {
		return nil, fmt.Errorf("third-party: media size missing")
	}
	u = resp.Request.URL
	classify := classifyKugouMedia
	if isQQMusicMediaURL(u) {
		classify = classifyQQMusicMedia
	} else if isKuwoMediaURL(u) || strings.EqualFold(u.Hostname(), "car-er.kuwo.cn") {
		classify = classifyKuwoMedia
	}
	format, bitrate, quality, err := classify(u)
	if err != nil {
		return nil, fmt.Errorf("third-party: unsupported audio format")
	}
	// A filename alone must not promote another audio format to lossless.
	if (format == "flac") != bytes.HasPrefix(probe, []byte("fLaC")) {
		return nil, fmt.Errorf("third-party: audio format does not match file extension")
	}
	return &platform.DownloadInfo{
		URL: u.String(), Size: size, Format: format, Bitrate: bitrate, Quality: quality,
		Headers: map[string]string{"User-Agent": sourceUserAgent},
		ValidateURL: func(raw string) error {
			parsed, err := url.Parse(raw)
			if err != nil || !isDirectSourceMediaURL(parsed) {
				return fmt.Errorf("third-party: download URL outside allowed music CDNs")
			}
			return nil
		},
	}, nil
}
