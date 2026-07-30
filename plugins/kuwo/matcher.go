package kuwo

import (
	"net/url"
	"regexp"
	"strings"
)

var (
	kuwoIDPattern   = regexp.MustCompile(`^\d{1,20}$`)
	kuwoTextPattern = regexp.MustCompile(`(?i)^\s*(?:kuwo|kw|酷我|酷我音乐)\s*[:：]\s*(\d{1,20})\s*$`)
	kuwoURLInText   = regexp.MustCompile(`(?i)https?://[^\s]+`)
)

// URLMatcher extracts Kuwo track and playlist IDs from supported URLs.
type URLMatcher struct{}

// NewURLMatcher creates a Kuwo URL matcher.
func NewURLMatcher() *URLMatcher { return &URLMatcher{} }

// MatchURL extracts a track ID from a supported Kuwo track URL.
func (m *URLMatcher) MatchURL(rawURL string) (trackID string, matched bool) {
	parsed, ok := parseKuwoURL(rawURL)
	if !ok {
		return "", false
	}

	if id, ok := pathID(parsed.Path, "/play_detail/"); ok {
		return id, true
	}
	if parsed.Path == "/newh5/singles/songinfoandlrc" {
		if id := parsed.Query().Get("musicId"); kuwoIDPattern.MatchString(id) {
			return id, true
		}
	}
	return "", false
}

// MatchPlaylistURL extracts a playlist ID from a supported Kuwo playlist URL.
func (m *URLMatcher) MatchPlaylistURL(rawURL string) (playlistID string, matched bool) {
	parsed, ok := parseKuwoURL(rawURL)
	if !ok {
		return "", false
	}

	for _, prefix := range []string{"/playlist_detail/", "/h5app/playlist/"} {
		if id, ok := pathID(parsed.Path, prefix); ok {
			return id, true
		}
	}
	// Albums reuse the playlist entry point so an album link expands into its
	// tracks; the prefix keeps the two ID spaces apart.
	if id, ok := pathID(parsed.Path, "/album_detail/"); ok {
		return encodeAlbumCollectionID(id), true
	}
	if parsed.Path == "/web/inventory/share" && parsed.Query().Get("type") == "2016" {
		if id := parsed.Query().Get("pid"); kuwoIDPattern.MatchString(id) {
			return id, true
		}
	}
	return "", false
}

// MatchArtistURL extracts an artist ID from a supported Kuwo artist URL.
func (m *URLMatcher) MatchArtistURL(rawURL string) (artistID string, matched bool) {
	parsed, ok := parseKuwoURL(rawURL)
	if !ok {
		return "", false
	}

	for _, prefix := range []string{"/singer_detail/", "/newh5app/singer_detail/"} {
		if id, ok := pathID(parsed.Path, prefix); ok {
			return id, true
		}
	}
	return "", false
}

// TextMatcher extracts Kuwo track IDs from explicit prefixes or embedded URLs.
type TextMatcher struct{}

// NewTextMatcher creates a Kuwo text matcher.
func NewTextMatcher() *TextMatcher { return &TextMatcher{} }

// MatchText extracts a track ID from a recognized Kuwo prefix or embedded URL.
// Bare numeric IDs are deliberately not accepted.
func (m *TextMatcher) MatchText(text string) (trackID string, matched bool) {
	if matches := kuwoTextPattern.FindStringSubmatch(text); len(matches) == 2 {
		return matches[1], true
	}
	if rawURL := kuwoURLInText.FindString(text); rawURL != "" {
		return NewURLMatcher().MatchURL(strings.TrimRight(rawURL, ".,!?)]}>"))
	}
	return "", false
}

func parseKuwoURL(rawURL string) (*url.URL, bool) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, false
	}
	hostname := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	switch hostname {
	case "kuwo.cn", "www.kuwo.cn", "m.kuwo.cn":
		return parsed, true
	default:
		return nil, false
	}
}

func pathID(path, prefix string) (string, bool) {
	if !strings.HasPrefix(path, prefix) {
		return "", false
	}
	id := strings.TrimPrefix(path, prefix)
	if !kuwoIDPattern.MatchString(id) {
		return "", false
	}
	return id, true
}
