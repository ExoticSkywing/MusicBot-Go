package applemusic

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/liuran001/MusicBot-Go/bot/i18n"
	"github.com/liuran001/MusicBot-Go/bot/platform"
	"golang.org/x/sync/errgroup"
)

const (
	metadataLocalizationTimeout     = 8 * time.Second
	metadataLocalizationConcurrency = 4
)

type appleMusicMetadataLocale struct {
	botLanguage     string
	storefront      string
	catalogLanguage string
}

var appleMusicMetadataLocales = map[string]appleMusicMetadataLocale{
	"zh": {botLanguage: "zh", storefront: "cn", catalogLanguage: "zh-Hans-CN"},
	"en": {botLanguage: "en", storefront: "us", catalogLanguage: "en-US"},
	"ja": {botLanguage: "ja", storefront: "jp", catalogLanguage: "ja"},
	"ru": {botLanguage: "ru", storefront: "ru", catalogLanguage: "ru"},
}

// LocalizeTrack resolves the equivalent catalog song for the bot language and
// overlays display names onto a copy of track. Playback IDs, URLs, capabilities,
// artwork and all other source-storefront fields stay unchanged.
func (p *AppleMusicPlatform) LocalizeTrack(ctx context.Context, track *platform.Track) (*platform.Track, error) {
	if track == nil {
		return nil, nil
	}
	locale, ok := appleMusicMetadataLocales[i18n.From(ctx).Lang()]
	if !ok || track.MetadataLanguage == locale.botLanguage {
		return track, nil
	}
	if p == nil || p.client == nil || strings.TrimSpace(track.ID) == "" {
		return track, platform.NewUnavailableError("applemusic", "track", track.ID)
	}
	if ctx == nil {
		ctx = context.Background()
	}

	lookupCtx, cancel := context.WithTimeout(ctx, metadataLocalizationTimeout)
	defer cancel()
	resource, err := p.fetchLocalizedEquivalent(lookupCtx, track, locale)
	if err != nil {
		return track, err
	}
	return overlayLocalizedTrackNames(track, resource, locale.botLanguage), nil
}

func (p *AppleMusicPlatform) fetchLocalizedEquivalent(ctx context.Context, track *platform.Track, locale appleMusicMetadataLocale) (appleMusicResource, error) {
	endpoint, err := url.Parse(fmt.Sprintf("%s/v1/catalog/%s/songs", appleMusicBaseURL, locale.storefront))
	if err != nil {
		return appleMusicResource{}, err
	}
	query := endpoint.Query()
	query.Set("filter[equivalents]", track.ID)
	query.Set("include", "albums,artists")
	query.Set("l", locale.catalogLanguage)
	endpoint.RawQuery = query.Encode()

	body, err := p.client.doAnonymousMetadataRequest(ctx, endpoint.String(), true)
	if err != nil {
		return appleMusicResource{}, err
	}
	var response appleMusicResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return appleMusicResource{}, fmt.Errorf("applemusic: parse localized metadata response: %w", err)
	}
	for _, candidate := range response.Data {
		if strings.TrimSpace(candidate.Attributes.Name) == "" {
			continue
		}
		if track.ISRC != "" && !strings.EqualFold(track.ISRC, candidate.Attributes.ISRC) {
			continue
		}
		return candidate, nil
	}
	return appleMusicResource{}, platform.NewNotFoundError("applemusic", "localized track", track.ID)
}

// doAnonymousMetadataRequest deliberately omits the account media-user-token.
// Localized catalog metadata is public and must not depend on the subscription
// storefront used by downloads and lyrics.
func (c *Client) doAnonymousMetadataRequest(ctx context.Context, requestURL string, retry bool) ([]byte, error) {
	if err := c.ensureDeveloperToken(ctx); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.getDeveloperToken())
	req.Header.Set("Origin", appleMusicOrigin)
	req.Header.Set("User-Agent", appleMusicUA)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return nil, err
	}
	switch resp.StatusCode {
	case http.StatusOK:
		return body, nil
	case http.StatusUnauthorized:
		if retry {
			c.clearDeveloperToken()
			if err := c.ensureDeveloperToken(ctx); err != nil {
				return nil, err
			}
			return c.doAnonymousMetadataRequest(ctx, requestURL, false)
		}
		return nil, &platform.PlatformError{Platform: "applemusic", Resource: "localized metadata", Err: platform.ErrAuthRequired}
	case http.StatusTooManyRequests:
		return nil, platform.NewRateLimitedError("applemusic")
	case http.StatusNotFound:
		return nil, platform.NewNotFoundError("applemusic", "localized metadata", requestURL)
	default:
		return nil, fmt.Errorf("applemusic: localized metadata HTTP %d: %s", resp.StatusCode, string(body[:min(len(body), 200)]))
	}
}

func overlayLocalizedTrackNames(source *platform.Track, resource appleMusicResource, language string) *platform.Track {
	localized := convertSong(resource)
	result := *source
	result.Artists = append([]platform.Artist(nil), source.Artists...)
	if source.Album != nil {
		album := *source.Album
		album.Artists = append([]platform.Artist(nil), source.Album.Artists...)
		result.Album = &album
	}

	result.Title = chooseLocalizedName(source.Title, localized.Title, language)
	result.Artists = overlayLocalizedArtistNames(result.Artists, localized.Artists, resource.Attributes.ArtistName, language)
	if result.Album != nil {
		albumTitle := resource.Attributes.AlbumName
		if localized.Album != nil && strings.TrimSpace(localized.Album.Title) != "" {
			albumTitle = localized.Album.Title
		}
		result.Album.Title = chooseLocalizedName(result.Album.Title, albumTitle, language)
		if localized.Album != nil {
			result.Album.Artists = overlayLocalizedArtistNames(result.Album.Artists, localized.Album.Artists, resource.Attributes.ArtistName, language)
		}
	}
	result.MetadataLanguage = language
	return &result
}

func overlayLocalizedArtistNames(existing, localized []platform.Artist, displayName, language string) []platform.Artist {
	if len(existing) == 0 {
		return existing
	}
	if len(existing) == 1 && strings.TrimSpace(displayName) != "" {
		existing[0].Name = chooseLocalizedName(existing[0].Name, displayName, language)
		return existing
	}
	nameOnly := len(existing) == len(localized)
	for _, artist := range existing {
		if artist.ID != "" || artist.URL != "" || artist.AvatarURL != "" {
			nameOnly = false
		}
	}
	if nameOnly {
		for i := range existing {
			existing[i].Name = chooseLocalizedName(existing[i].Name, localized[i].Name, language)
		}
		return existing
	}
	for i := range existing {
		// Cross-region artist ordering can differ. Keep names paired with the
		// source artist IDs/links instead of relabeling by array position.
		for _, artist := range localized {
			if existing[i].ID != "" && artist.ID == existing[i].ID {
				existing[i].Name = chooseLocalizedName(existing[i].Name, artist.Name, language)
				break
			}
		}
	}
	return existing
}

func chooseLocalizedName(existing, localized, language string) string {
	if isLikelyLanguageName(existing, language) || strings.TrimSpace(localized) == "" {
		return existing
	}
	return localized
}

func isLikelyLanguageName(value, language string) bool {
	foundHan, foundKana, foundCyrillic := false, false, false
	for _, r := range value {
		switch {
		case unicode.In(r, unicode.Hiragana, unicode.Katakana):
			foundKana = true
		case unicode.Is(unicode.Han, r):
			foundHan = true
		case unicode.Is(unicode.Cyrillic, r):
			foundCyrillic = true
		}
	}
	switch language {
	case "zh":
		return foundHan && !foundKana
	case "ja":
		return foundKana
	case "ru":
		return foundCyrillic
	default:
		return false
	}
}

func (p *AppleMusicPlatform) localizeTrackList(ctx context.Context, tracks []platform.Track) {
	if len(tracks) == 0 {
		return
	}
	lookupCtx, cancel := context.WithTimeout(ctx, metadataLocalizationTimeout)
	defer cancel()
	group, groupCtx := errgroup.WithContext(lookupCtx)
	group.SetLimit(metadataLocalizationConcurrency)
	for i := range tracks {
		if groupCtx.Err() != nil {
			break
		}
		i := i
		group.Go(func() error {
			localized, err := p.LocalizeTrack(groupCtx, &tracks[i])
			if err == nil && localized != nil {
				tracks[i] = *localized
			}
			return nil
		})
	}
	_ = group.Wait()
}
