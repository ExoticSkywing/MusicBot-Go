package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
)

// TestResolveShortURLMemoisesRepeatedCalls covers the router hot path: the same
// message is evaluated by several predicates, each re-resolving the same link.
func TestResolveShortURLMemoisesRepeatedCalls(t *testing.T) {
	var hits atomic.Int64
	finalURL := "https://music.douyin.com/qishui/share/album?album_id=123"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Location", finalURL)
		w.WriteHeader(http.StatusFound)
	}))
	defer server.Close()

	manager := newStubManager()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse server url: %v", err)
	}
	manager.Register(&shortLinkTestPlatform{
		stubPlatform: newStubPlatform("soda"),
		hosts:        []string{parsed.Hostname()},
	})

	shortURLCache.Delete(server.URL)
	shortURLFailureCache.Delete(server.URL)

	for i := range 5 {
		resolved, err := resolveShortURL(context.Background(), manager, server.URL)
		if err != nil {
			t.Fatalf("call %d: resolveShortURL() error = %v", i, err)
		}
		if resolved != finalURL {
			t.Fatalf("call %d: resolveShortURL() = %q, want %q", i, resolved, finalURL)
		}
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("upstream was hit %d times across 5 resolutions, want 1", got)
	}
}

// TestResolveShortURLCachesFailuresBriefly keeps one unreachable host from
// turning every router predicate into another 8s timeout.
func TestResolveShortURLCachesFailuresBriefly(t *testing.T) {
	var hits atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Errorf("response writer is not a Hijacker")
			return
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		// Drop the connection so both client attempts fail.
		_ = conn.Close()
	}))
	defer server.Close()

	manager := newStubManager()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse server url: %v", err)
	}
	manager.Register(&shortLinkTestPlatform{
		stubPlatform: newStubPlatform("soda"),
		hosts:        []string{parsed.Hostname()},
	})

	shortURLCache.Delete(server.URL)
	shortURLFailureCache.Delete(server.URL)

	for range 4 {
		if resolved, _ := resolveShortURL(context.Background(), manager, server.URL); resolved != server.URL {
			t.Fatalf("failed resolution should fall back to the input, got %q", resolved)
		}
	}
	if got := hits.Load(); got > 2 {
		t.Fatalf("upstream was hit %d times across 4 failed resolutions, want <= 2", got)
	}
}
