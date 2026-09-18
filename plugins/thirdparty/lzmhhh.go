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

// Protocol reference: musicdl qq.py/kugou.py _parsewithlzmhhhapi; see UPSTREAM.md.
type lzmhhhProvider struct{ http *directSourceHTTP }

func newLZMHHHProvider(baseURL string, timeout time.Duration, client *http.Client) (*lzmhhhProvider, error) {
	h, err := newDirectSourceHTTP(baseURL, timeout, client)
	if err != nil {
		return nil, err
	}
	return &lzmhhhProvider{http: h}, nil
}

func (*lzmhhhProvider) Name() string { return "lzmhhh" }

func (p *lzmhhhProvider) Resolve(ctx context.Context, platformName, trackID string, _ platform.Quality) (*platform.DownloadInfo, error) {
	kind := map[string]string{"qqmusic": "qq", "kugou": "kg"}[platformName]
	if kind == "" {
		return nil, fmt.Errorf("lzmhhh: unsupported platform")
	}
	id, err := normalizeSourceTrackID(platformName, trackID)
	if err != nil {
		return nil, err
	}
	form := url.Values{"id": {id}, "type": {kind}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.http.endpoint("/api/music/url", nil), strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", sourceUserAgent)
	req.Header.Set("Origin", originOf(p.http.base))
	req.Header.Set("Referer", originOf(p.http.base)+"/")
	var response struct {
		Code int    `json:"code"`
		Data string `json:"data"`
	}
	if err := decodeSourceJSON(p.http.api, req, &response); err != nil {
		return nil, err
	}
	if response.Code != 1 || strings.TrimSpace(response.Data) == "" {
		return nil, fmt.Errorf("lzmhhh: no audio URL")
	}
	return p.http.downloadInfo(ctx, response.Data)
}
