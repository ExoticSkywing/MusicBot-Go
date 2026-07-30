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
	ArtistID    jsonScalar      `json:"artistid"`
	AllArtistID jsonScalar      `json:"allartistid"`
	Album       jsonScalar      `json:"album"`
	AlbumID     jsonScalar      `json:"albumid"`
	Duration    jsonScalar      `json:"duration"`
	Cover       jsonScalar      `json:"pic"`
	CoverShort  jsonScalar      `json:"web_albumpic_short"`
	IsListenFee json.RawMessage `json:"isListenFee"`
	IsTry       jsonScalar      `json:"isTry"`
	PayInfo     json.RawMessage `json:"payInfo"`
}

type trackDetail struct {
	platform.Track
}

// trackAccess preserves the upstream availability signals without deciding
// whether a track is downloadable; that policy belongs to the download layer.
type trackAccess struct {
	listenFee json.RawMessage
	isTrial   bool
	payInfo   json.RawMessage
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

// normalizeEntityID validates an artist or album identifier. Kuwo reports a
// missing album as 0, which is not a linkable entity.
func normalizeEntityID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || !kuwoIDPattern.MatchString(value) {
		return ""
	}
	if strings.Trim(value, "0") == "" {
		return ""
	}
	return value
}

func buildArtistURL(id string) string {
	if id == "" {
		return ""
	}
	return "https://www.kuwo.cn/singer_detail/" + id
}

func buildAlbumURL(id string) string {
	if id == "" {
		return ""
	}
	return "https://www.kuwo.cn/album_detail/" + id
}

func splitFields(value string) []string {
	return strings.FieldsFunc(value, func(char rune) bool {
		switch char {
		case '&', '/', '、', ',', ';', '，', '；':
			return true
		default:
			return false
		}
	})
}

// splitArtists pairs each artist name with its upstream identifier so the
// caption can link every artist. Search results carry allartistid, which is
// ordered and delimited exactly like the artist string; detail and playlist
// responses only carry the lead artist's artistid. When the two lists disagree
// in length the pairing is ambiguous, so only the lead artist — the one
// artistid always refers to — gets a link rather than risking a name pointing
// at the wrong artist's page.
func splitArtists(value, allArtistIDs, leadArtistID string) []platform.Artist {
	names := splitFields(value)
	ids := splitFields(allArtistIDs)
	paired := len(ids) == len(names)
	lead := normalizeEntityID(leadArtistID)

	artists := make([]platform.Artist, 0, len(names))
	for index, part := range names {
		name := strings.TrimSpace(part)
		if name == "" {
			continue
		}
		artist := platform.Artist{Platform: "kuwo", Name: name}
		id := ""
		switch {
		case paired:
			id = normalizeEntityID(ids[index])
		case index == 0:
			id = lead
		}
		if id != "" {
			artist.ID = id
			artist.URL = buildArtistURL(id)
		}
		artists = append(artists, artist)
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
	artists := splitArtists(scalarText(wire.Artist), scalarText(wire.AllArtistID), scalarText(wire.ArtistID))
	title := scalarText(wire.Name)
	if title == "" {
		title = scalarText(wire.SongName)
	}
	cover := scalarText(wire.Cover)
	if cover == "" {
		cover = normalizeCoverURL(scalarText(wire.CoverShort))
	}
	albumName := scalarText(wire.Album)
	var album *platform.Album
	if albumName != "" {
		albumID := normalizeEntityID(scalarText(wire.AlbumID))
		album = &platform.Album{
			ID:       albumID,
			Platform: "kuwo",
			Title:    albumName,
			Artists:  artists,
			CoverURL: cover,
			URL:      buildAlbumURL(albumID),
		}
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
		listenFee: append(json.RawMessage(nil), wire.IsListenFee...),
		isTrial:   scalarFlag(wire.IsTry),
		payInfo:   append(json.RawMessage(nil), wire.PayInfo...),
	}, true
}
