package soda

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const defaultSodaSignerTimeout = 5 * time.Second

type sodaRequestSigner interface {
	Sign(ctx context.Context, rawURL string, headers http.Header) (string, http.Header, error)
}

type sodaBDMSSigner struct {
	endpoint   string
	token      string
	httpClient *http.Client
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
	if err := json.Unmarshal(body, &result); err != nil {
		return "", nil, fmt.Errorf("decode signer response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		message := strings.TrimSpace(result.Error)
		if message == "" {
			message = http.StatusText(resp.StatusCode)
		}
		return "", nil, fmt.Errorf("signer returned HTTP %d: %s", resp.StatusCode, message)
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
