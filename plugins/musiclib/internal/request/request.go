// Package request supplies cancellable, per-platform HTTP requests for the
// music-lib ports. It deliberately has no mutable global HTTP client.
package request

import (
	"context"
	"fmt"
	"io"
	"net/http"
)

type RequestOption func(*http.Request)

func WithHeader(key, value string) RequestOption {
	return func(req *http.Request) { req.Header.Set(key, value) }
}

// Get limits API/HTML responses, uses the caller's proxy and propagates cancellation.
func Get(ctx context.Context, client *http.Client, rawURL string, opts ...RequestOption) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	for _, opt := range opts {
		opt(req)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("upstream HTTP status %d", resp.StatusCode)
	}
	const maxResponseSize = 16 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxResponseSize {
		return nil, fmt.Errorf("upstream response exceeds %d bytes", maxResponseSize)
	}
	return body, nil
}
