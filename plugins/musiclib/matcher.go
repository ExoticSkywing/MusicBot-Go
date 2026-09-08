package musiclib

import (
	"net/url"
	"regexp"
	"strings"

	"github.com/liuran001/MusicBot-Go/bot/platform"
)

var platformMetadata = map[string]platform.Meta{
	"migu":     {Name: "migu", DisplayName: "咪咕音乐", Emoji: "🎵", Aliases: []string{"migu", "mg", "咪咕", "咪咕音乐"}, AllowGroupURL: true, GroupURLHosts: []string{"music.migu.cn", "y.migu.cn"}},
	"qianqian": {Name: "qianqian", DisplayName: "千千音乐", Emoji: "🎶", Aliases: []string{"qianqian", "qqian", "千千", "千千音乐"}, AllowGroupURL: true, GroupURLHosts: []string{"music.91q.com"}},
	"fivesing": {Name: "fivesing", DisplayName: "5sing", Emoji: "🎤", Aliases: []string{"fivesing", "5sing", "5s"}, AllowGroupURL: true, GroupURLHosts: []string{"5sing.kugou.com"}},
	"jamendo":  {Name: "jamendo", DisplayName: "Jamendo", Emoji: "🎸", Aliases: []string{"jamendo", "jam"}, AllowGroupURL: true, GroupURLHosts: []string{"jamendo.com", "www.jamendo.com"}},
	"joox":     {Name: "joox", DisplayName: "JOOX", Emoji: "🎧", Aliases: []string{"joox", "jx"}, AllowGroupURL: true, GroupURLHosts: []string{"joox.com", "www.joox.com"}},
}

var (
	numericID        = regexp.MustCompile(`^[0-9]+$`)
	wordID           = regexp.MustCompile(`^[A-Za-z0-9_]+$`)
	jooxID           = regexp.MustCompile(`^[A-Za-z0-9_+=-]+$`)
	fivesingID       = regexp.MustCompile(`^(yc|fc|bz)_([0-9]+)$`)
	fivesingSourceID = regexp.MustCompile(`^([0-9]+)\|(yc|fc|bz)$`)
)

func (p *Platform) Metadata() platform.Meta {
	meta := platformMetadata[p.name]
	meta.Aliases = append([]string(nil), meta.Aliases...)
	meta.GroupURLHosts = append([]string(nil), meta.GroupURLHosts...)
	return meta
}

func (p *Platform) MatchURL(rawURL string) (string, bool) {
	kind, id := p.matchResource(rawURL)
	if kind == "track" && id != "" {
		return id, true
	}
	return "", false
}

func (p *Platform) MatchPlaylistURL(rawURL string) (string, bool) {
	kind, id := p.matchResource(rawURL)
	if kind == "album" && id != "" {
		return platform.EncodeAlbumCollectionID(id), true
	}
	if kind == "playlist" && id != "" {
		return id, true
	}
	return "", false
}

func (p *Platform) matchResource(rawURL string) (string, string) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return "", ""
	}
	allowed := false
	for _, host := range platformMetadata[p.name].GroupURLHosts {
		if strings.EqualFold(u.Hostname(), host) {
			allowed = true
			break
		}
	}
	if !allowed {
		return "", ""
	}
	if p.name == "migu" && (u.Path == "/v5/" || u.Path == "/v3/" || u.Path == "/") && strings.HasPrefix(u.Fragment, "/") {
		fragment, err := url.Parse(u.Fragment)
		if err == nil && fragment.Host == "" && fragment.Scheme == "" && resourceKind(strings.Trim(fragment.Path, "/")) != "" {
			u.Path, u.RawQuery = fragment.Path, fragment.RawQuery
		}
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	kind, id := "", ""
	switch p.name {
	case "migu":
		if len(parts) == 4 && (parts[0] == "v3" || parts[0] == "v5") && parts[1] == "music" {
			kind, id = resourceKind(parts[2]), parts[3]
		} else if len(parts) == 2 {
			kind, id = resourceKind(parts[0]), parts[1]
		}
		if kind == "" {
			kind, id = queryResource(u, map[string]string{"playlistId": "playlist", "musicListId": "playlist", "albumId": "album", "copyrightId": "track", "contentId": "track", "songId": "track"})
		}
		if kind == "" && (u.Path == "/album" || u.Path == "/v3/music/album" || u.Path == "/v5/music/album" || u.Query().Get("resourceType") == "2003") {
			kind, id = "album", u.Query().Get("resourceId")
		}
	case "qianqian":
		if len(parts) == 2 {
			kind, id = resourceKind(parts[0]), parts[1]
		}
		if kind == "" {
			kind, id = queryResource(u, map[string]string{"songlistid": "playlist", "tracklistid": "playlist", "playlistid": "playlist", "albumAssetCode": "album", "albumid": "album", "TSID": "track"})
		}
	case "fivesing":
		if len(parts) == 2 && (parts[0] == "yc" || parts[0] == "fc" || parts[0] == "bz") && strings.HasSuffix(parts[1], ".html") {
			kind, id = "track", parts[0]+"_"+strings.TrimSuffix(parts[1], ".html")
		} else if (len(parts) == 2 && parts[0] == "dj") || (len(parts) == 3 && numericID.MatchString(parts[0]) && parts[1] == "dj") {
			if strings.HasSuffix(parts[len(parts)-1], ".html") {
				kind, id = "playlist", strings.TrimSuffix(parts[len(parts)-1], ".html")
			}
		}
	case "jamendo":
		if len(parts) >= 2 {
			kind, id = resourceKind(parts[0]), parts[1]
		}
	case "joox":
		if len(parts) == 3 {
			kind, id = resourceKind(parts[1]), parts[2]
		}
		if kind == "" {
			// These are base64 IDs: raw '+' is literal, but %20 is an invalid
			// space. Preserve that distinction before query decoding.
			u.RawQuery = strings.ReplaceAll(u.RawQuery, "+", "%2B")
			kind, id = queryResource(u, map[string]string{"playlistid": "playlist", "playlist_id": "playlist", "h_activity_id": "album", "albumid": "album", "songid": "track"})
		}
	}
	if kind == "" || !p.validID(kind, id) {
		return "", ""
	}
	return kind, id
}

func queryResource(u *url.URL, keys map[string]string) (string, string) {
	kind, id := "", ""
	for key, value := range keys {
		if candidate := u.Query().Get(key); candidate != "" {
			// Ambiguous mixed resource links must not be routed arbitrarily.
			if id != "" && (kind != value || id != candidate) {
				return "", ""
			}
			kind, id = value, candidate
		}
	}
	return kind, id
}

func resourceKind(segment string) string {
	switch segment {
	case "song", "track", "single":
		return "track"
	case "playlist", "songlist", "tracklist":
		return "playlist"
	case "album":
		return "album"
	}
	return ""
}

func (p *Platform) validID(kind, id string) bool {
	if len(id) == 0 || len(id) > 128 {
		return false
	}
	switch p.name {
	case "migu", "jamendo":
		return numericID.MatchString(id)
	case "qianqian":
		return wordID.MatchString(id)
	case "fivesing":
		if kind == "album" {
			return false
		}
		if kind == "track" {
			return fivesingID.MatchString(id)
		}
		return wordID.MatchString(id)
	case "joox":
		return jooxID.MatchString(id)
	}
	return false
}

func (p *Platform) trackURL(id string) string {
	if p.name == "migu" && strings.Contains(id, "|") {
		parts := strings.Split(id, "|")
		if len(parts) == 3 && wordID.MatchString(parts[1]) && wordID.MatchString(parts[2]) {
			id = parts[0]
		}
	}
	if p.name == "fivesing" {
		if parts := fivesingSourceID.FindStringSubmatch(id); parts != nil {
			id = parts[2] + "_" + parts[1]
		}
	}
	if !p.validID("track", id) {
		return ""
	}
	switch p.name {
	case "migu":
		return "https://music.migu.cn/v3/music/song/" + id
	case "qianqian":
		return "https://music.91q.com/song/" + id
	case "fivesing":
		parts := fivesingID.FindStringSubmatch(id)
		return "https://5sing.kugou.com/" + parts[1] + "/" + parts[2] + ".html"
	case "jamendo":
		return "https://www.jamendo.com/track/" + id
	case "joox":
		return "https://www.joox.com/sg/single/" + url.PathEscape(id)
	}
	return ""
}

func (p *Platform) collectionURL(id string, isAlbum bool) string {
	kind := "playlist"
	if isAlbum {
		kind = "album"
	}
	if !p.validID(kind, id) {
		return ""
	}
	switch p.name {
	case "migu":
		return "https://music.migu.cn/v3/music/" + kind + "/" + id
	case "qianqian":
		return "https://music.91q.com/" + kind + "/" + id
	case "fivesing":
		return "https://5sing.kugou.com/dj/" + id + ".html"
	case "jamendo":
		return "https://www.jamendo.com/" + kind + "/" + id
	case "joox":
		return "https://www.joox.com/sg/" + kind + "/" + url.PathEscape(id)
	}
	return ""
}
