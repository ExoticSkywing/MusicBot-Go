package thirdparty

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/liuran001/MusicBot-Go/bot/platform"
)

// Protocol reference: musicdl qq.py/kugou.py _parsewithqqovoapi; see UPSTREAM.md.
type qqovoProvider struct{ http *directSourceHTTP }

func newQQOVOProvider(baseURL string, timeout time.Duration, client *http.Client) (*qqovoProvider, error) {
	h, err := newDirectSourceHTTP(baseURL, timeout, client)
	if err != nil {
		return nil, err
	}
	return &qqovoProvider{http: h}, nil
}

func (*qqovoProvider) Name() string { return "qqovo" }

func qqovoNonce() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6], b[8] = b[6]&0x0f|0x40, b[8]&0x3f|0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func qqovoSignature(u *url.URL, key, timestamp, nonce string) string {
	payload := strings.Join([]string{http.MethodGet, u.Path, u.Query().Encode(), "", timestamp, nonce}, "\n")
	mac := hmac.New(sha256.New, []byte(key))
	_, _ = mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (p *qqovoProvider) headers(req *http.Request) {
	req.Header.Set("User-Agent", strings.Replace(sourceUserAgent, "151.0.0.0", "152.0.0.0", 1))
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Pragma", "no-cache")
	req.Header.Set("Origin", originOf(p.http.base))
	req.Header.Set("Referer", originOf(p.http.base)+"/room/4SVWQK")
}

func (p *qqovoProvider) Resolve(ctx context.Context, platformName, trackID string, _ platform.Quality) (*platform.DownloadInfo, error) {
	kind := map[string]string{"qqmusic": "tencent", "kugou": "kugou"}[platformName]
	if kind == "" {
		return nil, fmt.Errorf("qqovo: unsupported platform")
	}
	id, err := normalizeSourceTrackID(platformName, trackID)
	if err != nil {
		return nil, err
	}
	// Anonymous session and key belong to this Resolve call only. No user
	// cookies, shared mutable session state, persistent tokens or key rotation.
	client := *p.http.api
	client.Jar, err = cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.http.endpoint("/api/session/bootstrap", nil), strings.NewReader(`{"deviceId":"`+qqovoNonce()+`"}`))
	if err != nil {
		return nil, err
	}
	p.headers(req)
	req.Header.Set("Content-Type", "application/json")
	var session struct {
		Key string `json:"apiSignKey"`
	}
	if err := decodeSourceJSON(&client, req, &session); err != nil {
		return nil, err
	}
	if session.Key == "" || len(session.Key) > 4096 {
		return nil, fmt.Errorf("qqovo: anonymous session unavailable")
	}
	query := url.Values{"server": {kind}, "type": {"url"}, "id": {id}, "quality": {"lossless"}}
	req, err = http.NewRequestWithContext(ctx, http.MethodGet, p.http.endpoint("/api/meting", query), nil)
	if err != nil {
		return nil, err
	}
	p.headers(req)
	timestamp, nonce := strconv.FormatInt(time.Now().Unix(), 10), qqovoNonce()
	req.Header.Set("X-OM-Ts", timestamp)
	req.Header.Set("X-OM-Nonce", nonce)
	req.Header.Set("X-OM-Sign", qqovoSignature(req.URL, session.Key, timestamp, nonce))
	var response struct {
		URL string `json:"url"`
	}
	if err := decodeSourceJSON(&client, req, &response); err != nil {
		return nil, err
	}
	if strings.TrimSpace(response.URL) == "" {
		return nil, fmt.Errorf("qqovo: no audio URL")
	}
	return p.http.downloadInfo(ctx, response.URL)
}
