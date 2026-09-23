package douyin

import (
	"net/url"
	"regexp"
	"strings"
)

var (
	// www.douyin.com/music/<id>、www.iesdouyin.com/share/music/<id>、m.douyin.com/share/music/<id>
	douyinMusicPathPattern = regexp.MustCompile(`^(?:share/)?music/(\d+)$`)
	// 只取 ASCII URL 字符，App 分享文案里链接后常紧跟中文标点和文字。
	douyinURLPattern = regexp.MustCompile(`https?://[A-Za-z0-9\-._~:/?#\[\]@!$&'()*+,;=%]+`)
)

type URLMatcher struct{}

func NewURLMatcher() *URLMatcher { return &URLMatcher{} }

func (m *URLMatcher) MatchURL(rawURL string) (string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || !isDouyinHost(parsed.Hostname()) {
		return "", false
	}
	match := douyinMusicPathPattern.FindStringSubmatch(strings.Trim(parsed.Path, "/"))
	if len(match) != 2 || !isDouyinMusicID(match[1]) {
		return "", false
	}
	return match[1], true
}

type TextMatcher struct{}

func NewTextMatcher() *TextMatcher { return &TextMatcher{} }

// MatchText 只识别带前缀的 ID（douyin:/dy:/抖音:）和原声链接；裸数字留给关键词搜索。
func (m *TextMatcher) MatchText(text string) (string, bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", false
	}
	if id, ok := parseDouyinPrefix(text); ok {
		return id, true
	}
	if urlStr := extractURL(text); urlStr != "" {
		return NewURLMatcher().MatchURL(urlStr)
	}
	return "", false
}

func parseDouyinPrefix(text string) (string, bool) {
	text = strings.ReplaceAll(text, "：", ":")
	parts := strings.SplitN(text, ":", 2)
	if len(parts) != 2 {
		return "", false
	}
	switch strings.ToLower(strings.TrimSpace(parts[0])) {
	case "douyin", "dy", "抖音":
	default:
		return "", false
	}
	id := strings.TrimSpace(parts[1])
	return id, isDouyinMusicID(id)
}

func extractURL(text string) string {
	match := douyinURLPattern.FindString(text)
	return strings.TrimSpace(strings.TrimRight(match, ".,!?)]}>"))
}

func isDouyinHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	for _, domain := range []string{"douyin.com", "iesdouyin.com"} {
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return true
		}
	}
	return false
}

func isDouyinMusicID(value string) bool {
	if len(value) < 8 || len(value) > 20 {
		return false
	}
	for _, ch := range value {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return true
}
