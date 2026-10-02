package migu

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/liuran001/MusicBot-Go/plugins/musiclib/internal/model"
)

// The envelope protocol is adapted from Domdkw/miguMusic-api-enhanced
// url_v2.ts (MIT, Copyright 2026 Domdkw); see MRC_LICENSE.
func decodeMiguResponse(body []byte) ([]byte, error) {
	if !bytes.HasPrefix(body, []byte{0xab, 0xcd}) {
		return body, nil
	}
	if len(body) < 5 || body[2] != 1 {
		return nil, errors.New("invalid migu response envelope")
	}
	const key = "Jk8qzuePiJ1qE3mDYhLQ3T73DtDoAhLP"
	decoded := make([]byte, len(body)-4)
	for i := range decoded {
		decoded[i] = body[i+4] + body[3] - key[i%len(key)]
	}
	return decoded, nil
}

func miguAccountToken(cookie string) string {
	for _, pair := range strings.Split(cookie, ";") {
		key, value, ok := strings.Cut(strings.TrimSpace(pair), "=")
		if ok && strings.EqualFold(key, "pacmtoken") {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func (m *Migu) fetchListenInfo(contentID, copyrightID, songID, albumID, resourceType, toneFlag string) (*miguListenResponse, error) {
	params := url.Values{"netType": {"01"}, "resourceType": {firstNonEmpty(resourceType, "2")}, "contentId": {strings.TrimSpace(contentID)}, "toneFlag": {firstNonEmpty(toneFlag, "PQ")}}
	for key, value := range map[string]string{"copyrightId": copyrightID, "songId": songID, "albumId": albumID} {
		if value != "" {
			params.Set(key, value)
		}
	}
	routes := []string{miguListenURL, "https://app.c.nf.migu.cn/MIGUM3.0/strategy/pc/listen/v1.0"}
	anonymous := miguAccountToken(m.cookie) == ""
	// The new transports serve PQ. Keep the ZQ/ZQ24 request sequence intact.
	if anonymous && params.Get("toneFlag") == "PQ" {
		routes = append(routes, "https://app.c.nf.migu.cn/strategy/pc/listen/v2.0", "https://c.musicapp.migu.cn/strategy/listen-url/h5/v2.4")
	}
	var firstReply *miguListenResponse
	var firstErr error
	for index, endpoint := range routes {
		if err := m.ctx.Err(); err != nil {
			return nil, err
		}
		resp, err := m.fetchListenRoute(endpoint, params, index)
		if err := m.ctx.Err(); err != nil {
			return nil, err
		}
		if err != nil {
			if miguTerminalError(err) {
				return nil, err
			}
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if firstReply == nil {
			firstReply = resp
		}
		if strings.TrimSpace(resp.Data.URL) != "" {
			return resp, nil
		}
	}
	if firstReply != nil {
		return firstReply, nil
	}
	return nil, firstErr
}

func (m *Migu) fetchListenRoute(endpoint string, params url.Values, route int) (*miguListenResponse, error) {
	// Copy values: PC/H5 additions must not mutate later tone requests.
	query := url.Values{}
	for key, values := range params {
		query[key] = append([]string(nil), values...)
	}
	if route >= 2 {
		query.Set("scene", "")
	}
	if route == 3 {
		query.Set("lowerQualityContentId", query.Get("contentId"))
	}
	req, err := http.NewRequestWithContext(m.ctx, http.MethodGet, endpoint+"?"+query.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	req.Header.Set("Referer", "https://music.migu.cn/")
	req.Header.Set("channel", "014X031")
	if route == 0 {
		req.Header.Set("User-Agent", miguAndroidUA)
		req.Header.Set("channel", miguAndroidChannel)
		req.Header.Set("version", miguAndroidVersion)
	}
	if route == 2 {
		device := make([]byte, 16)
		if _, err := rand.Read(device); err != nil {
			return nil, err
		}
		for key, value := range map[string]string{"subchannel": "014X031", "deviceId": hex.EncodeToString(device), "ua": "Android_migu", "version": "6.8.8", "activityId": "MUSIC-WWW", "birth": "h5page", "signature": "1", "timestamp": strconv.FormatInt(time.Now().UnixMilli(), 10)} {
			req.Header.Set(key, value)
		}
	}
	if route == 3 {
		req.Header.Set("birth", "h5page")
		req.Header.Set("Referer", "https://y.migu.cn/")
	}
	if cookie := strings.TrimSpace(m.cookie); route < 2 && cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	if token := miguAccountToken(m.cookie); route < 2 && token != "" {
		req.Header.Set("pacmtoken", token)
	}
	client := *m.client
	if route >= 2 {
		client.Jar = nil
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("migu listen endpoint returned status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, (4<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(body) > 4<<20 {
		return nil, errors.New("migu listen response exceeds limit")
	}
	body, err = decodeMiguResponse(body)
	if err != nil {
		return nil, err
	}
	var result miguListenResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("migu listen response json parse error: %w", err)
	}
	if result.Code != "" && result.Code != "000000" {
		return nil, fmt.Errorf("migu api error: %s (code %s)", result.Info, result.Code)
	}
	return &result, nil
}

func miguTrialHint(resp *miguListenResponse) bool {
	if resp == nil {
		return false
	}
	text := strings.ToLower(resp.Data.DialogInfo.Text)
	if strings.Contains(text, "\u8bd5\u542c") || strings.Contains(text, "preview") || strings.Contains(text, "audition") {
		return true
	}
	value := strings.Trim(string(resp.Data.IsTrial), "\" ")
	if value == "true" || value == "1" {
		return true
	}
	length, _ := strconv.ParseFloat(strings.Trim(string(resp.Data.AuditionsLength), "\" "), 64)
	return length > 0
}

func miguPreviewURL(raw string) bool {
	if model.ExplicitPreviewURL(raw) {
		return true
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}
	path := strings.ToLower(parsed.Path)
	for _, marker := range []string{"60s", "30s", "audition", "preview", "\u8bd5\u542c"} {
		if strings.Contains(path, marker) {
			return true
		}
	}
	return false
}

func miguCatalogSize(song *model.Song, tone, selectedTone string) int64 {
	tones := []string{tone}
	if tone == "ZQ24" {
		tones = append(tones, "ZQ")
	}
	if tone == "ZQ" {
		tones = append(tones, "ZQ24")
	}
	for _, key := range tones {
		if n, err := strconv.ParseInt(song.Extra["size_"+key], 10, 64); err == nil && n > 0 {
			return n
		}
		if key == selectedTone && song.Size > 0 {
			return song.Size
		}
	}
	return 0
}

func miguTerminalError(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var verify *tls.CertificateVerificationError
	var unknown x509.UnknownAuthorityError
	var invalid x509.CertificateInvalidError
	var hostname x509.HostnameError
	return errors.As(err, &verify) || errors.As(err, &unknown) || errors.As(err, &invalid) || errors.As(err, &hostname)
}
