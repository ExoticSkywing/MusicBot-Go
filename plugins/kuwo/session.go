package kuwo

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"time"
)

const (
	secretMultiplier = int64(9253)
	secretIncrement  = int64(23)
	secretModulus    = int64(2147483647)
	secretSeed       = int64(59910100)
	sessionTTL       = 30 * time.Minute

	kuwoSessionCookie = "Hm_Iuvt_cdb524f42f23cer9b268564v7y735ewrq2324"
)

func buildSecret(cookie string, nonce int) string {
	state := secretSeed
	encoded := make([]byte, len(cookie))
	for i := range cookie {
		state = (state*secretMultiplier + secretIncrement) % secretModulus
		encoded[i] = cookie[i] ^ byte(state*255/secretModulus)
	}
	return fmt.Sprintf("%x%08x", encoded, nonce)
}

func randomNonce() (int, error) {
	value, err := rand.Int(rand.Reader, big.NewInt(90000000))
	if err != nil {
		return 0, fmt.Errorf("kuwo: generate nonce: %w", err)
	}
	return int(value.Int64()) + 10000000, nil
}

func validSessionCookie(value string) bool {
	if len(value) < 16 || len(value) > 128 {
		return false
	}
	for _, char := range value {
		if !(char >= 'a' && char <= 'z') && !(char >= 'A' && char <= 'Z') && !(char >= '0' && char <= '9') {
			return false
		}
	}
	return true
}

func (c *Client) ensureSession(ctx context.Context) error {
	if c == nil {
		return fmt.Errorf("kuwo: nil client")
	}
	c.sessionMu.Lock()
	defer c.sessionMu.Unlock()
	if cookie := c.sessionCookie(); validSessionCookie(cookie) && c.now().Before(c.sessionExpires) {
		return nil
	}

	homeURL, err := url.Parse(c.endpoints.home)
	if err != nil {
		return fmt.Errorf("kuwo: parse homepage URL: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, homeURL.String(), nil)
	if err != nil {
		return fmt.Errorf("kuwo: create homepage request: %w", err)
	}
	req.Header.Set("User-Agent", kuwoUserAgent)
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("kuwo: request homepage: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("kuwo: homepage returned HTTP %d", resp.StatusCode)
	}
	if cookie := c.sessionCookie(); !validSessionCookie(cookie) {
		return fmt.Errorf("kuwo: homepage did not return a valid session cookie")
	}
	c.sessionExpires = c.now().Add(sessionTTL)
	return nil
}

func (c *Client) sessionCookie() string {
	if c == nil || c.httpClient() == nil || c.httpClient().Jar == nil {
		return ""
	}
	homeURL, err := url.Parse(c.endpoints.home)
	if err != nil {
		return ""
	}
	for _, cookie := range c.httpClient().Jar.Cookies(homeURL) {
		if cookie.Name == kuwoSessionCookie {
			return cookie.Value
		}
	}
	return ""
}

func (c *Client) invalidateSession() {
	if c == nil {
		return
	}
	c.sessionMu.Lock()
	defer c.sessionMu.Unlock()
	c.invalidateSessionLocked()
}

func (c *Client) invalidateSessionLocked() {
	c.sessionExpires = time.Time{}
	client := c.httpClient()
	if client == nil || client.Jar == nil {
		return
	}
	homeURL, err := url.Parse(c.endpoints.home)
	if err != nil {
		return
	}
	client.Jar.SetCookies(homeURL, []*http.Cookie{{Name: kuwoSessionCookie, Value: "", Path: "/", MaxAge: -1}})
}
