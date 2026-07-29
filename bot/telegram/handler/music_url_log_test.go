package handler

import (
	"errors"
	"net/url"
	"strings"
	"testing"
)

func TestDownloadURLForLogRedactsPathQueryAndFragment(t *testing.T) {
	for _, tt := range []struct {
		raw  string
		want string
	}{
		{"https://kw-er.kuwo.cn/token/song.flac?sign=secret#fragment", "https://kw-er.kuwo.cn/[redacted]"},
		{"http://er-sycdn.kuwo.cn:8080/private.mp3", "http://er-sycdn.kuwo.cn:8080/[redacted]"},
		{"not a URL", "[redacted]"},
		{"https://host.test/%gh", "[redacted]"},
		{"/relative/path", "[redacted]"},
		{"javascript://host/secret", "[redacted]"},
	} {
		if got := downloadURLForLog(tt.raw); got != tt.want {
			t.Errorf("downloadURLForLog(%q) = %q, want %q", tt.raw, got, tt.want)
		}
	}
}

func TestDownloadErrorForLogRedactsAllURLs(t *testing.T) {
	nested := &url.Error{
		Op:  "Get",
		URL: "https://kw-er.kuwo.cn/signed/path.flac?token=secret",
		Err: errors.New("redirect from http://er-sycdn.kuwo.cn/other.mp3?signature=also-secret to https://evil.test/final"),
	}
	got := downloadErrorForLog(nested)
	if strings.Contains(got, "http://") || strings.Contains(got, "https://") ||
		strings.Contains(got, "signed") || strings.Contains(got, "signature") {
		t.Fatalf("downloadErrorForLog() leaked URL: %q", got)
	}
	if count := strings.Count(got, "[redacted-url]"); count < 3 {
		t.Fatalf("downloadErrorForLog() = %q, redactions = %d", got, count)
	}

	plain := downloadErrorForLog(errors.New("first https://a.test/path then http://b.test/token"))
	if plain != "first [redacted-url] then [redacted-url]" {
		t.Fatalf("plain error = %q", plain)
	}
	if got := downloadErrorForLog(nil); got != "" {
		t.Fatalf("nil error = %q", got)
	}
}
