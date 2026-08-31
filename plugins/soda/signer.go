package soda

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	defaultSodaSignerTimeout    = 10 * time.Second
	defaultSodaSignerAttempts   = 2
	defaultSodaSignerRetryDelay = 100 * time.Millisecond
)

type sodaRequestSigner interface {
	Sign(ctx context.Context, rawURL string, headers http.Header) (string, http.Header, error)
}

type sodaBDMSSigner struct {
	endpoint   string
	token      string
	httpClient *http.Client
	attempts   int
	retryDelay time.Duration
}

type sodaSignerHTTPError struct {
	statusCode int
	message    string
}

func (e *sodaSignerHTTPError) Error() string {
	return fmt.Sprintf("signer returned HTTP %d: %s", e.statusCode, e.message)
}

type sodaSignerRequest struct {
	URL             string            `json:"url"`
	Headers         map[string]string `json:"headers,omitempty"`
	AddCommonParams bool              `json:"add_common_params"`
}

type sodaSignerResponse struct {
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Error   string            `json:"error"`
}

func newSodaBDMSSigner(endpoint, token string, timeout time.Duration) (*sodaBDMSSigner, error) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return nil, nil
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("soda: parse BDMS signer URL: %w", err)
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil {
		return nil, fmt.Errorf("soda: BDMS signer URL must be an absolute HTTP(S) URL without credentials")
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/v1/sign"
	if timeout <= 0 {
		timeout = defaultSodaSignerTimeout
	}
	return &sodaBDMSSigner{
		endpoint:   parsed.String(),
		token:      strings.TrimSpace(token),
		httpClient: &http.Client{Timeout: timeout},
		attempts:   defaultSodaSignerAttempts,
		retryDelay: defaultSodaSignerRetryDelay,
	}, nil
}

func (c *Client) SetBDMSSigner(endpoint, token string, timeout time.Duration) error {
	if c == nil {
		return nil
	}
	signer, err := newSodaBDMSSigner(endpoint, token, timeout)
	if err != nil {
		return err
	}
	c.signer = signer
	return nil
}

func (s *sodaBDMSSigner) Sign(ctx context.Context, rawURL string, headers http.Header) (string, http.Header, error) {
	if s == nil || s.httpClient == nil || strings.TrimSpace(s.endpoint) == "" {
		return "", nil, fmt.Errorf("BDMS signer is not configured")
	}
	requestHeaders := make(map[string]string, len(headers))
	for name, values := range headers {
		if strings.EqualFold(name, "Cookie") || strings.EqualFold(name, "Authorization") {
			continue
		}
		if len(values) == 0 || strings.TrimSpace(values[0]) == "" {
			continue
		}
		requestHeaders[name] = values[0]
	}
	payload, err := json.Marshal(sodaSignerRequest{
		URL:             rawURL,
		Headers:         requestHeaders,
		AddCommonParams: true,
	})
	if err != nil {
		return "", nil, fmt.Errorf("encode request: %w", err)
	}
	attempts := s.attempts
	if attempts <= 0 {
		attempts = 1
	}
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		signedURL, signedHeaders, signErr := s.signOnce(ctx, rawURL, payload)
		if signErr == nil {
			return signedURL, signedHeaders, nil
		}
		lastErr = signErr
		if attempt >= attempts || !shouldRetrySodaSigner(ctx, signErr) {
			break
		}
		if err := waitForSodaSignerRetry(ctx, s.retryDelay); err != nil {
			return "", nil, err
		}
	}
	return "", nil, lastErr
}

func (s *sodaBDMSSigner) signOnce(ctx context.Context, rawURL string, payload []byte) (string, http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if s.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.token)
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return "", nil, fmt.Errorf("request signer: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", nil, fmt.Errorf("read signer response: %w", err)
	}
	var result sodaSignerResponse
	if resp.StatusCode != http.StatusOK {
		// Preserve retryable 5xx status errors even if an upstream proxy or a
		// partially failing signer returns a non-JSON error page.
		_ = json.Unmarshal(body, &result)
		message := strings.TrimSpace(result.Error)
		if message == "" {
			message = http.StatusText(resp.StatusCode)
		}
		return "", nil, &sodaSignerHTTPError{statusCode: resp.StatusCode, message: message}
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", nil, fmt.Errorf("decode signer response: %w", err)
	}
	if err := validateSignedSodaURL(rawURL, result.URL); err != nil {
		return "", nil, err
	}
	signedHeaders := make(http.Header, 2)
	for name, value := range result.Headers {
		if !strings.EqualFold(name, "X-Helios") && !strings.EqualFold(name, "X-Medusa") {
			continue
		}
		if strings.TrimSpace(value) != "" {
			signedHeaders.Set(name, value)
		}
	}
	if signedHeaders.Get("X-Helios") == "" || signedHeaders.Get("X-Medusa") == "" {
		return "", nil, fmt.Errorf("signer response is missing X-Helios or X-Medusa")
	}
	return result.URL, signedHeaders, nil
}

func shouldRetrySodaSigner(ctx context.Context, err error) bool {
	if err == nil || ctx == nil || ctx.Err() != nil || errors.Is(err, context.Canceled) {
		return false
	}
	var statusErr *sodaSignerHTTPError
	if errors.As(err, &statusErr) {
		return statusErr.statusCode >= http.StatusInternalServerError
	}
	var networkErr net.Error
	if errors.As(err, &networkErr) {
		return networkErr.Timeout() || networkErr.Temporary()
	}
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}

func waitForSodaSignerRetry(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func validateSignedSodaURL(originalURL, signedURL string) error {
	original, err := url.Parse(originalURL)
	if err != nil {
		return fmt.Errorf("parse original Soda URL: %w", err)
	}
	signed, err := url.Parse(strings.TrimSpace(signedURL))
	if err != nil {
		return fmt.Errorf("parse signed Soda URL: %w", err)
	}
	if signed.Scheme != "https" || !strings.EqualFold(original.Host, signed.Host) || original.Path != signed.Path || signed.User != nil {
		return fmt.Errorf("signer returned a mismatched target URL")
	}
	return nil
}
