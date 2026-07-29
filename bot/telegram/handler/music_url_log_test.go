package handler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	botpkg "github.com/liuran001/MusicBot-Go/bot"
	"github.com/liuran001/MusicBot-Go/bot/download"
	"github.com/liuran001/MusicBot-Go/bot/platform"
	"github.com/mymmrac/telego"
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

type downloadLogEntry struct {
	msg  string
	args []any
}

type downloadLogCapture struct {
	mu      sync.Mutex
	entries []downloadLogEntry
}

func (l *downloadLogCapture) append(msg string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, downloadLogEntry{msg: msg, args: append([]any(nil), args...)})
}

func (l *downloadLogCapture) Debug(msg string, args ...any)  { l.append(msg, args...) }
func (l *downloadLogCapture) Info(msg string, args ...any)   { l.append(msg, args...) }
func (l *downloadLogCapture) Warn(msg string, args ...any)   { l.append(msg, args...) }
func (l *downloadLogCapture) Error(msg string, args ...any)  { l.append(msg, args...) }
func (l *downloadLogCapture) With(args ...any) botpkg.Logger { return l }

func (l *downloadLogCapture) rendered(msg string) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, entry := range l.entries {
		if entry.msg == msg {
			return fmt.Sprint(entry.args...)
		}
	}
	return ""
}

type downloadLogPlatform struct {
	*stubPlatform
	trackErr error
	info     *platform.DownloadInfo
}

func (p *downloadLogPlatform) GetTrack(context.Context, string) (*platform.Track, error) {
	if p.trackErr != nil {
		return nil, p.trackErr
	}
	return p.stubPlatform.GetTrack(context.Background(), "41378936")
}

func (p *downloadLogPlatform) GetDownloadInfo(context.Context, string, platform.Quality) (*platform.DownloadInfo, error) {
	if p.info == nil {
		return nil, platform.ErrUnavailable
	}
	copy := *p.info
	return &copy, nil
}

func newDownloadLogBot(t *testing.T) (*telego.Bot, func()) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":1,"date":1,"chat":{"id":1001,"type":"private"},"text":"status"}}`)
	}))
	b, err := telego.NewBot("123456:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghi", telego.WithAPIServer(server.URL))
	if err != nil {
		server.Close()
		t.Fatalf("NewBot() = %v", err)
	}
	return b, server.Close
}

func assertLogURLRedacted(t *testing.T, rendered string) {
	t.Helper()
	if rendered == "" {
		t.Fatal("expected log entry was not recorded")
	}
	for _, secret := range []string{"https://", "signed/path", "token=secret"} {
		if strings.Contains(rendered, secret) {
			t.Fatalf("log leaked %q: %q", secret, rendered)
		}
	}
}

func TestProcessMusicSendFailedRedactsDownloadErrorAtCallSite(t *testing.T) {
	mediaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://kw-er.kuwo.cn/signed/path.flac?token=secret", http.StatusFound)
	}))
	defer mediaServer.Close()
	b, closeBot := newDownloadLogBot(t)
	defer closeBot()

	info := &platform.DownloadInfo{
		URL:     mediaServer.URL + "/start",
		Format:  "flac",
		Quality: platform.QualityLossless,
		Headers: map[string]string{"User-Agent": "policy-agent"},
		ValidateURL: func(raw string) error {
			if strings.Contains(raw, "kw-er.kuwo.cn") {
				return &url.Error{Op: "Get", URL: raw, Err: errors.New("blocked redirect")}
			}
			return nil
		},
	}
	manager := newStubManager()
	manager.Register(&downloadLogPlatform{stubPlatform: newStubPlatform("kuwo"), info: info})
	logger := &downloadLogCapture{}
	h := &MusicHandler{
		Repo:            newStubRepo(),
		PlatformManager: manager,
		DownloadService: download.NewDownloadService(download.DownloadServiceOptions{
			Timeout:         time.Second,
			MaxRetries:      1,
			EnableMultipart: false,
		}),
		Logger:         logger,
		CacheDir:       t.TempDir(),
		DefaultQuality: "lossless",
		ProcessTimeout: time.Second,
	}
	message := &telego.Message{
		MessageID: 1,
		Text:      "download",
		From:      &telego.User{ID: 7},
		Chat:      telego.Chat{ID: 1001, Type: "private"},
	}
	err := h.processMusic(withForceNonSilent(zhCtx()), b, message, "kuwo", "41378936", "lossless")
	if err == nil || !strings.Contains(err.Error(), "token=secret") {
		t.Fatalf("processMusic() error = %v; caller must retain original error", err)
	}
	assertLogURLRedacted(t, logger.rendered("failed to send music"))
}

func TestRunInlineMediaFlowRedactsPrepareErrorAtCallSite(t *testing.T) {
	b, closeBot := newDownloadLogBot(t)
	defer closeBot()
	sourceErr := &url.Error{
		Op:  "Get",
		URL: "https://kw-er.kuwo.cn/signed/path.flac?token=secret",
		Err: errors.New("transport failed"),
	}
	manager := newStubManager()
	manager.Register(&downloadLogPlatform{stubPlatform: newStubPlatform("kuwo"), trackErr: sourceErr})
	logger := &downloadLogCapture{}
	h := &MusicHandler{
		PlatformManager: manager,
		Logger:          logger,
		ProcessTimeout:  time.Second,
	}

	runInlineMediaFlow(
		withDownloadWorkAdmission(zhCtx()),
		b,
		inlineMediaFlowDeps{Music: h},
		"inline-log-redaction",
		7,
		"tester",
		"kuwo",
		"41378936",
		"lossless",
		0,
		false,
	)
	assertLogURLRedacted(t, logger.rendered("failed to prepare inline song"))
}
