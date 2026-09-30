package qqmusic

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/liuran001/MusicBot-Go/bot/platform"
)

// GetVKey preserves authenticated failures; the web fallback is sessionless.
func (c *Client) GetVKey(ctx context.Context, songMid, mediaMid, qualityCode, ext, uin, authst string) (string, error) {
	session := c.Cookie()
	result, err := c.getSignedVKey(ctx, songMid, mediaMid, qualityCode, ext, uin, authst)
	if c.Cookie() != session {
		return "", errors.New("qqmusic: account changed during audio resolution")
	}
	var certificateError *tls.CertificateVerificationError
	var invalidCertificate x509.CertificateInvalidError
	var unknownAuthority x509.UnknownAuthorityError
	var hostnameError x509.HostnameError
	if result != "" || authst != "" || errors.As(err, &certificateError) || errors.As(err, &invalidCertificate) || errors.As(err, &unknownAuthority) || errors.As(err, &hostnameError) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if c.Cookie() != session {
		return "", errors.New("qqmusic: account changed during audio resolution")
	}
	result, err = c.getAnonymousVKey(ctx, songMid, buildVKeyFilenames(songMid, mediaMid, qualityCode, ext))
	if c.Cookie() != session {
		return "", errors.New("qqmusic: account changed during audio resolution")
	}
	return result, err
}

func (c *Client) getAnonymousVKey(ctx context.Context, songMid string, filenames []string) (string, error) {
	mids, types := make([]string, len(filenames)), make([]int, len(filenames))
	for i := range mids {
		mids[i] = songMid
	}
	payload, err := json.Marshal(map[string]any{"req_0": map[string]any{
		"module": "vkey.GetVkeyServer", "method": "CgiGetVkey",
		"param": map[string]any{"guid": randomHex32(), "loginflag": 1,
			"filename": filenames, "songmid": mids, "songtype": types, "uin": "0", "platform": "20"},
	}})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, musicuEndpoint+"?"+url.Values{"data": {string(payload)}}.Encode(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	req.Header.Set("Referer", "https://y.qq.com/")
	client := *c.httpClient
	client.Jar = nil
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("qqmusic: anonymous vkey HTTP %d", resp.StatusCode)
	}
	var response struct {
		Code int `json:"code"`
		Req  *struct {
			Code int `json:"code"`
			Data struct {
				Info []struct {
					Filename string `json:"filename"`
					Purl     string `json:"purl"`
					Vkey     string `json:"vkey"`
				} `json:"midurlinfo"`
			} `json:"data"`
		} `json:"req_0"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&response); err != nil {
		return "", err
	}
	if response.Code == 0 && response.Req != nil && response.Req.Code == 0 {
		for _, filename := range filenames {
			for _, item := range response.Req.Data.Info {
				purl, err := url.Parse(strings.TrimSpace(item.Purl))
				if err == nil && purl.User == nil && item.Filename == filename && path.Base(purl.Path) == filename && item.Vkey != "" &&
					(purl.Scheme == "" || purl.Scheme == "https" || purl.Scheme == "http") {
					return item.Purl, nil
				}
			}
		}
	}
	return "", platform.NewUnavailableError("qqmusic", "track", songMid)
}
