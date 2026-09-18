package thirdparty

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/liuran001/MusicBot-Go/bot/platform"
)

// Protocol reference: musicdl kuwo.py _parsewithnxinxzapi. HTTPS is verified
// on this host even though that reference still uses HTTP; see UPSTREAM.md.
type nxinxzProvider struct{ http *directSourceHTTP }

func newNXINXZProvider(baseURL string, timeout time.Duration, client *http.Client) (*nxinxzProvider, error) {
	h, err := newDirectSourceHTTP(baseURL, timeout, client)
	if err != nil {
		return nil, err
	}
	return &nxinxzProvider{http: h}, nil
}

func (*nxinxzProvider) Name() string { return "nxinxz" }

func (p *nxinxzProvider) Resolve(ctx context.Context, platformName, trackID string, _ platform.Quality) (*platform.DownloadInfo, error) {
	if platformName != "kuwo" {
		return nil, fmt.Errorf("nxinxz: unsupported platform")
	}
	id, err := normalizeSourceTrackID(platformName, trackID)
	if err != nil {
		return nil, err
	}
	// One quality per provider attempt; the source chain owns fallback. Never
	// label the returned stream with the requested quality without inspecting it.
	query := url.Values{"id": {id}, "level": {"lossless"}, "type": {"json"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.http.endpoint("/kw.php", query), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", sourceUserAgent)
	var response struct {
		Code int `json:"code"`
		Data struct {
			URL string `json:"url"`
		} `json:"data"`
	}
	if err := decodeSourceJSON(p.http.api, req, &response); err != nil {
		return nil, err
	}
	if response.Code != http.StatusOK || strings.TrimSpace(response.Data.URL) == "" {
		return nil, fmt.Errorf("nxinxz: no audio URL")
	}
	return p.http.downloadInfo(ctx, response.Data.URL)
}
