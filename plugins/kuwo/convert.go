package kuwo

import (
	"encoding/json"
	"net/url"
	"strings"
	"time"

	"github.com/liuran001/MusicBot-Go/bot/platform"
)

type trackWire struct {
	RID         jsonScalar      `json:"rid"`
	MusicRID    jsonScalar      `json:"MUSICRID"`
	ID          jsonScalar      `json:"id"`
	Name        jsonScalar      `json:"name"`
	SongName    jsonScalar      `json:"SONGNAME"`
	Artist      jsonScalar      `json:"artist"`
	Album       jsonScalar      `json:"album"`
	Duration    jsonScalar      `json:"duration"`
	Cover       jsonScalar      `json:"pic"`
	CoverShort  jsonScalar      `json:"web_albumpic_short"`
	IsListenFee jsonScalar      `json:"isListenFee"`
	IsTry       jsonScalar      `json:"isTry"`
	PayInfo     json.RawMessage `json:"payInfo"`
}

type trackDetail struct {
	platform.Track
}

// trackAccess preserves the upstream availability signals without deciding
// whether a track is downloadable; that policy belongs to the download layer.
type trackAccess struct {
	isListenFee bool
	isTrial     bool
	payInfo     json.RawMessage
}

func normalizeRID(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= len("MUSIC_") && strings.EqualFold(value[:len("MUSIC_")], "MUSIC_") {
		value = value[len("MUSIC_"):]
	}
	if !kuwoIDPattern.MatchString(value) {
		return ""
	}
	return value
}

func scalarText(value jsonScalar) string {
	text, _ := value.String()
	return strings.TrimSpace(text)
}

func scalarFlag(value jsonScalar) bool {
	if flag, ok := value.Bool(); ok {
		return flag
	}
	integer, ok := value.Int64()
	return ok && integer != 0
}

func parseDuration(value jsonScalar) time.Duration {
	text := scalarText(value)
	if text == "" {
		return 0
	}
	if parsed, err := time.ParseDuration(text + "s"); err == nil {
		return parsed
	}
	parts := strings.Split(text, ":")
	if len(parts) != 2 && len(parts) != 3 {
		return 0
	}
	seconds := 0
	for _, part := range parts {
		if len(part) == 0 {
			return 0
		}
		value := 0
		for _, char := range part {
			if char < '0' || char > '9' {
				return 0
			}
			value = value*10 + int(char-'0')
		}
		seconds = seconds*60 + value
	}
	return time.Duration(seconds) * time.Second
}

func splitArtists(value string) []platform.Artist {
	parts := strings.FieldsFunc(value, func(char rune) bool {
		switch char {
		case '&', '/', '、', ',', ';', '，', '；':
			return true
		default:
			return false
		}
	})
	artists := make([]platform.Artist, 0, len(parts))
	for _, part := range parts {
		if name := strings.TrimSpace(part); name != "" {
			artists = append(artists, platform.Artist{Platform: "kuwo", Name: name})
		}
	}
	return artists
}

func normalizeCoverURL(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	parsed, err := url.Parse(value)
	if err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") {
		return value
	}
	return "https://img1.kuwo.cn/star/albumcover/" + strings.TrimLeft(value, "/")
}

func convertTrack(wire trackWire) (trackDetail, trackAccess, bool) {
	id := normalizeRID(scalarText(wire.RID))
	if id == "" {
		id = normalizeRID(scalarText(wire.MusicRID))
	}
	if id == "" {
		id = normalizeRID(scalarText(wire.ID))
	}
	if id == "" {
		return trackDetail{}, trackAccess{}, false
	}
	artists := splitArtists(scalarText(wire.Artist))
	albumName := scalarText(wire.Album)
	var album *platform.Album
	if albumName != "" {
		album = &platform.Album{Platform: "kuwo", Title: albumName, Artists: artists}
	}
	title := scalarText(wire.Name)
	if title == "" {
		title = scalarText(wire.SongName)
	}
	cover := scalarText(wire.Cover)
	if cover == "" {
		cover = normalizeCoverURL(scalarText(wire.CoverShort))
	}
	track := platform.Track{
		ID:       id,
		Platform: "kuwo",
		Title:    title,
		Artists:  artists,
		Album:    album,
		Duration: parseDuration(wire.Duration),
		CoverURL: cover,
		URL:      "https://www.kuwo.cn/play_detail/" + id,
	}
	return trackDetail{Track: track}, trackAccess{
		isListenFee: scalarFlag(wire.IsListenFee),
		isTrial:     scalarFlag(wire.IsTry),
		payInfo:     append(json.RawMessage(nil), wire.PayInfo...),
	}, true
}
