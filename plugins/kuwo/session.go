package kuwo

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"net/http"
	"net/http/cookiejar"
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
	return c.ensureSessionForURL(ctx, c.endpoints.home)
}

func (c *Client) ensureSessionForURL(ctx context.Context, signedURL string) error {
	if c == nil {
		return fmt.Errorf("kuwo: nil client")
	}
	for {
		c.sessionMu.Lock()
		if cookie := c.sessionCookie(signedURL); validSessionCookie(cookie) && c.now().Before(c.sessionExpires) {
			c.sessionMu.Unlock()
			return nil
		}
		if c.sessionRefreshing {
			ready := c.sessionReady
			c.sessionMu.Unlock()
			select {
			case <-ready:
				continue
			case <-ctx.Done():
				return fmt.Errorf("kuwo: wait for session refresh: %w", ctx.Err())
			}
		}
		ready := make(chan struct{})
		c.sessionRefreshing = true
		c.sessionReady = ready
		c.sessionMu.Unlock()

		err := c.refreshSession(ctx)
		c.sessionMu.Lock()
		if err == nil {
			c.sessionExpires = c.now().Add(sessionTTL)
		}
		c.sessionRefreshing = false
		c.sessionReady = nil
		close(ready)
		c.sessionMu.Unlock()
		return err
	}
}

func (c *Client) refreshSession(ctx context.Context) error {
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
	if cookie := c.sessionCookie(homeURL.String()); !validSessionCookie(cookie) {
		return fmt.Errorf("kuwo: homepage did not return a valid session cookie")
	}
	return nil
}

func (c *Client) sessionCookie(requestURL string) string {
	_, cookie := c.sessionClientAndCookie(requestURL)
	return cookie
}

func (c *Client) sessionClientAndCookie(requestURL string) (*http.Client, string) {
	if c == nil {
		return nil, ""
	}
	endpoint, err := url.Parse(requestURL)
	if err != nil {
		return nil, ""
	}
	c.clientMu.RLock()
	defer c.clientMu.RUnlock()
	client := c.apiHTTPClient
	if client == nil || client.Jar == nil {
		return client, ""
	}
	for _, cookie := range client.Jar.Cookies(endpoint) {
		if cookie.Name == kuwoSessionCookie {
			return client, cookie.Value
		}
	}
	return client, ""
}

func (c *Client) invalidateSession(requestURL string) {
	if c == nil {
		return
	}
	c.sessionMu.Lock()
	defer c.sessionMu.Unlock()
	c.invalidateSessionLocked(requestURL)
}

func (c *Client) invalidateSessionLocked(_ string) {
	c.sessionExpires = time.Time{}
	c.clientMu.Lock()
	defer c.clientMu.Unlock()
	if c.apiHTTPClient == nil {
		return
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return
	}
	// The API client owns this anonymous session jar. Replacing the client
	// pointer avoids guessing cookie paths and leaves in-flight requests on
	// their immutable client/jar pair.
	client := *c.apiHTTPClient
	client.Jar = jar
	c.apiHTTPClient = &client
}
