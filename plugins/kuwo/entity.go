package kuwo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/url"
	"strings"
	"time"

	"github.com/liuran001/MusicBot-Go/bot/platform"
)

const (
	kuwoAlbumURL  = "https://www.kuwo.cn/api/www/album/albumInfo"
	kuwoArtistURL = "https://www.kuwo.cn/api/www/artist/artist"
)

type albumResponse struct {
	Code jsonScalar `json:"code"`
	Data *albumWire `json:"data"`
}

type albumWire struct {
	AlbumID     jsonScalar `json:"albumid"`
	Album       jsonScalar `json:"album"`
	Artist      jsonScalar `json:"artist"`
	ArtistID    jsonScalar `json:"artistid"`
	AllArtistID jsonScalar `json:"allartistid"`
	Pic         jsonScalar `json:"pic"`
	Total       jsonScalar `json:"total"`
	ReleaseDate jsonScalar `json:"releaseDate"`
	AlbumInfo   jsonScalar `json:"albuminfo"`
}

type artistResponse struct {
	Code jsonScalar  `json:"code"`
	Data *artistWire `json:"data"`
}

type artistWire struct {
	ID       jsonScalar `json:"id"`
	Name     jsonScalar `json:"name"`
	Pic300   jsonScalar `json:"pic300"`
	Pic      jsonScalar `json:"pic"`
	Pic120   jsonScalar `json:"pic120"`
	Pic70    jsonScalar `json:"pic70"`
	MusicNum jsonScalar `json:"musicNum"`
}

// cleanUpstreamText strips the HTML entities Kuwo embeds in prose fields
// (notably &nbsp; inside artist and album blurbs). Decoding &nbsp; yields U+00A0,
// which looks like a space but breaks word matching, so it is normalised to a
// plain space rather than left in the text.
func cleanUpstreamText(value string) string {
	unescaped := html.UnescapeString(value)
	return strings.TrimSpace(strings.ReplaceAll(unescaped, "\u00a0", " "))
}

// parseReleaseDate reads Kuwo's "2003-07-31" release dates. Upstream uses
// 0000-00-00 and 1970-01-01 as "unknown", neither of which is a real date worth
// reporting.
func parseReleaseDate(value string) (*time.Time, int) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, 0
	}
	parsed, err := time.Parse("2006-01-02", value)
	if err != nil || parsed.Year() <= 1970 {
		return nil, 0
	}
	return &parsed, parsed.Year()
}

// GetAlbum fetches album metadata. Kuwo returns the album under the same
// numeric id used by album_detail pages.
func (c *Client) GetAlbum(ctx context.Context, albumID string) (*platform.Album, error) {
	albumID = strings.TrimSpace(albumID)
	if !isASCIIUnsignedDecimal(albumID, 20) {
		return nil, platform.NewNotFoundError("kuwo", "album", albumID)
	}
	if c == nil {
		return nil, platform.NewUnavailableError("kuwo", "album", albumID)
	}
	if ctx == nil {
		ctx = context.Background()
	}

	endpoint, err := url.Parse(c.endpoints.album)
	if err != nil {
		return nil, fmt.Errorf("kuwo: parse album URL: %w", err)
	}
	values := endpoint.Query()
	values.Set("albumId", albumID)
	values.Set("pn", "1")
	values.Set("rn", "1")
	values.Set("httpsStatus", "1")
	endpoint.RawQuery = values.Encode()

	body, err := c.signedGet(ctx, endpoint.String(), "https://www.kuwo.cn/album_detail/"+albumID)
	if err != nil {
		return nil, err
	}
	if err := validateUniqueJSONKeys(body); err != nil {
		return nil, errors.Join(
			platform.NewUnavailableError("kuwo", "album", albumID),
			fmt.Errorf("kuwo: invalid album response JSON: %w", err),
		)
	}
	var response albumResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("kuwo: decode album response: %w", err)
	}
	if code, ok := response.Code.Int64(); ok && code != 200 {
		return nil, platform.NewNotFoundError("kuwo", "album", albumID)
	}
	if response.Data == nil {
		return nil, platform.NewNotFoundError("kuwo", "album", albumID)
	}

	wire := response.Data
	title := scalarText(wire.Album)
	if title == "" {
		return nil, platform.NewNotFoundError("kuwo", "album", albumID)
	}
	// Identity check: never hand back an album the caller did not ask for.
	if id := normalizeEntityID(scalarText(wire.AlbumID)); id != "" && id != albumID {
		return nil, platform.NewUnavailableError("kuwo", "album", albumID)
	}

	album := &platform.Album{
		ID:       albumID,
		Platform: "kuwo",
		Title:    title,
		Artists:  splitArtists(scalarText(wire.Artist), scalarText(wire.AllArtistID), scalarText(wire.ArtistID)),
		CoverURL: normalizeCoverURL(scalarText(wire.Pic)),
		URL:      buildAlbumURL(albumID),
	}
	if description := cleanUpstreamText(scalarText(wire.AlbumInfo)); description != "" {
		album.Description = description
	}
	if total, ok := wire.Total.Int64(); ok && total > 0 && total <= int64(^uint(0)>>1) {
		album.TrackCount = int(total)
	}
	if released, year := parseReleaseDate(scalarText(wire.ReleaseDate)); released != nil {
		album.ReleaseDate = released
		album.Year = year
	}
	return album, nil
}

// GetArtist fetches artist metadata along with the artist's track count, which
// upstream reports as musicNum.
func (c *Client) GetArtist(ctx context.Context, artistID string) (*platform.Artist, int, error) {
	artistID = strings.TrimSpace(artistID)
	if !isASCIIUnsignedDecimal(artistID, 20) {
		return nil, 0, platform.NewNotFoundError("kuwo", "artist", artistID)
	}
	if c == nil {
		return nil, 0, platform.NewUnavailableError("kuwo", "artist", artistID)
	}
	if ctx == nil {
		ctx = context.Background()
	}

	endpoint, err := url.Parse(c.endpoints.artist)
	if err != nil {
		return nil, 0, fmt.Errorf("kuwo: parse artist URL: %w", err)
	}
	values := endpoint.Query()
	values.Set("artistid", artistID)
	values.Set("httpsStatus", "1")
	endpoint.RawQuery = values.Encode()

	body, err := c.signedGet(ctx, endpoint.String(), buildArtistURL(artistID))
	if err != nil {
		return nil, 0, err
	}
	if err := validateUniqueJSONKeys(body); err != nil {
		return nil, 0, errors.Join(
			platform.NewUnavailableError("kuwo", "artist", artistID),
			fmt.Errorf("kuwo: invalid artist response JSON: %w", err),
		)
	}
	var response artistResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, 0, fmt.Errorf("kuwo: decode artist response: %w", err)
	}
	if code, ok := response.Code.Int64(); ok && code != 200 {
		return nil, 0, platform.NewNotFoundError("kuwo", "artist", artistID)
	}
	if response.Data == nil {
		return nil, 0, platform.NewNotFoundError("kuwo", "artist", artistID)
	}

	wire := response.Data
	name := cleanUpstreamText(scalarText(wire.Name))
	if name == "" {
		return nil, 0, platform.NewNotFoundError("kuwo", "artist", artistID)
	}
	// Identity check mirrors GetAlbum: a mismatched id means the response is
	// about somebody else.
	if id := normalizeEntityID(scalarText(wire.ID)); id != "" && id != artistID {
		return nil, 0, platform.NewUnavailableError("kuwo", "artist", artistID)
	}

	artist := &platform.Artist{
		ID:        artistID,
		Platform:  "kuwo",
		Name:      name,
		AvatarURL: firstNonEmptyPortrait(wire.Pic300, wire.Pic, wire.Pic120, wire.Pic70),
		URL:       buildArtistURL(artistID),
	}
	trackCount := 0
	if num, ok := wire.MusicNum.Int64(); ok && num > 0 && num <= int64(^uint(0)>>1) {
		trackCount = int(num)
	}
	return artist, trackCount, nil
}

// firstNonEmptyPortrait picks the largest available portrait, falling back
// through the smaller variants Kuwo ships. Only absolute URLs are accepted:
// portraits live under starheads paths, so normalizeCoverURL's album-cover
// prefix for relative values would build a broken link.
func firstNonEmptyPortrait(candidates ...jsonScalar) string {
	for _, candidate := range candidates {
		value := scalarText(candidate)
		if value == "" {
			continue
		}
		if parsed, err := url.Parse(value); err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") {
			return value
		}
	}
	return ""
}
