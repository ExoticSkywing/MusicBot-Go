package kuwo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/liuran001/MusicBot-Go/bot/platform"
)

const (
	maxLegacyPlayBodyBytes = 64 << 10
	maxLegacyPlayLineBytes = 8 << 10
)

type legacyPlayData struct {
	url       string
	format    string
	bitrate   string
	rid       string
	duration  string
	mediaType string
}

func parseLegacyPlayResponse(body []byte) (legacyPlayData, error) {
	if len(body) == 0 {
		return legacyPlayData{}, errors.New("kuwo: empty legacy play response")
	}
	if len(body) > maxLegacyPlayBodyBytes {
		return legacyPlayData{}, errors.New("kuwo: legacy play response too large")
	}
	for _, character := range body {
		if (character < 0x20 && character != '\r' && character != '\n') || character == 0x7f {
			return legacyPlayData{}, errors.New("kuwo: invalid control character in legacy play response")
		}
	}

	values := make(map[string]string)
	for _, rawLine := range strings.Split(string(body), "\n") {
		if len(rawLine) > maxLegacyPlayLineBytes {
			return legacyPlayData{}, errors.New("kuwo: legacy play response line too large")
		}
		line := strings.TrimSuffix(rawLine, "\r")
		if line == "" {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		if !found || !isLegacyPlayKey(key) {
			return legacyPlayData{}, errors.New("kuwo: invalid legacy play response line")
		}
		if _, duplicate := values[key]; duplicate {
			return legacyPlayData{}, errors.New("kuwo: duplicate legacy play response field")
		}
		values[key] = value
	}

	data := legacyPlayData{
		url:       values["url"],
		format:    values["format"],
		bitrate:   values["bitrate"],
		rid:       values["rid"],
		duration:  values["duration"],
		mediaType: values["type"],
	}
	if data.url == "" {
		return legacyPlayData{}, errors.New("kuwo: legacy play response missing URL")
	}
	return data, nil
}

func isLegacyPlayKey(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		if (character >= 'a' && character <= 'z') ||
			(character >= '0' && character <= '9') ||
			character == '_' {
			continue
		}
		return false
	}
	return true
}

// legacyLosslessProfileFor maps a bitrate the legacy play endpoint declared onto
// the resolver profile that validates it. Only kuwo's two FLAC tiers qualify.
func legacyLosslessProfileFor(bitrate int64) (directQualityResolverProfile, bool) {
	switch bitrate {
	case directLosslessBitrate:
		return directQualityResolverProfile{
			level:   directLosslessSelectorLevel,
			bitrate: directLosslessBitrate,
			format:  "flac",
		}, true
	case directHiResBitrate:
		return directQualityResolverProfile{
			level:   directHiResSelectorLevel,
			bitrate: directHiResBitrate,
			format:  "flac",
		}, true
	default:
		return directQualityResolverProfile{}, false
	}
}

func (c *Client) resolvePlayableLossless(ctx context.Context, detail *trackDetail) (*platform.DownloadInfo, error) {
	if c == nil || c.mediaHTTPClient == nil || detail == nil {
		return nil, platform.NewUnavailableError("kuwo", "media", "")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if !isASCIIUnsignedDecimal(detail.ID, 20) || detail.Duration <= 0 {
		return nil, terminalUnavailable(errTrackIdentityMismatch)
	}

	endpoint := c.endpoints.legacy
	if endpoint == "" {
		endpoint = kuwoMobilePlayURL
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("kuwo: parse legacy play URL: %w", err)
	}
	plaintext := "user=0&corp=kuwo&source=kwplayer_ar_5.1.0.0_B_jiakong_vh.apk&" +
		"p2p=1&type=convert_url2&sig=0&format=flac&rid=" + detail.ID
	query := parsed.Query()
	query.Set("f", "kuwo")
	query.Set("q", encodeKuwoQuery(plaintext))
	parsed.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("kuwo: create legacy play request: %w", err)
	}
	req.Header.Set("User-Agent", mediaUserAgent)
	resp, err := c.sessionlessAPIClient().Do(req)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		return nil, fmt.Errorf("kuwo: request legacy play API: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, platform.NewRateLimitedError("kuwo")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("kuwo: legacy play API returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxLegacyPlayBodyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("kuwo: read legacy play response: %w", err)
	}
	data, err := parseLegacyPlayResponse(body)
	if err != nil {
		return nil, err
	}
	if normalizeRID(data.rid) != detail.ID {
		return nil, terminalUnavailable(errTrackIdentityMismatch)
	}
	if data.mediaType != "" && data.mediaType != "0" {
		return nil, terminalUnavailable(errPreviewMedia)
	}
	if data.duration != "" {
		seconds, parseErr := strconv.ParseInt(data.duration, 10, 64)
		if parseErr != nil || seconds <= 0 {
			return nil, terminalUnavailable(errPreviewMedia, errTrackDurationMismatch)
		}
		duration, durationErr := secondsDuration(seconds)
		if durationErr != nil || !durationsMatch(duration, detail.Duration) {
			return nil, terminalUnavailable(errPreviewMedia, errTrackDurationMismatch)
		}
	}
	bitrate, parseErr := strconv.ParseInt(data.bitrate, 10, 64)
	if parseErr != nil || bitrate <= 0 || bitrate > 100000 {
		return nil, errors.New("kuwo: invalid legacy play bitrate")
	}
	if bitrate <= 1 {
		return nil, terminalUnavailable(errPreviewMedia)
	}
	// Kuwo answers this endpoint with whichever FLAC tier it is willing to
	// serve, which for some tracks is the 4000 Hi-Res tier rather than the 2000
	// lossless one. Accepting only 2000 rejected those outright, so a track kuwo
	// would happily serve at 24-bit/96kHz failed with a bitrate mismatch and,
	// once every resolver had failed, surfaced as "paid track".
	selector, ok := legacyLosslessProfileFor(bitrate)
	if !ok {
		return nil, errors.New("kuwo: legacy lossless selector bitrate mismatch")
	}
	if !strings.EqualFold(data.format, "flac") {
		return nil, errors.New("kuwo: legacy play format mismatch")
	}

	rawURL, err := normalizeMediaURL(data.url, "flac")
	if err != nil {
		if errors.Is(err, errUnsafeMediaURL) {
			return nil, terminalUnavailable(err)
		}
		return nil, err
	}
	probe, err := probeMedia(ctx, c.mediaHTTPClient, rawURL, "flac", detail.Duration)
	if err != nil {
		if errors.Is(err, errUnsafeMediaURL) {
			return nil, terminalUnavailable(err)
		}
		return nil, err
	}
	// Verify the stream against the tier kuwo actually declared, reusing the
	// same profile rules the external resolver path applies. The checks stay as
	// strict as before for a given tier; only the set of accepted tiers grew.
	if !selector.acceptsProbe(probe) {
		return nil, errors.New("kuwo: legacy lossless STREAMINFO mismatch")
	}
	rawSize := probe.size
	trailerProbe, err := c.probeDirectFLACTrailer(
		ctx,
		rawURL,
		rawSize,
		probe.flacHeader,
	)
	if err != nil {
		if errors.Is(err, errUnsafeMediaURL) {
			return nil, terminalUnavailable(err)
		}
		return nil, err
	}
	probe.size = trailerProbe.outputSize
	probe.bitrate = averageBitrateKbps(trailerProbe.outputSize, probe.duration)
	info := c.buildDownloadInfo(rawURL, "flac", probe)
	info.Downloader = c.directFLACDownloader(
		rawURL,
		rawSize,
		probe.flacHeader,
		trailerProbe,
	)
	return info, nil
}

func secondsDuration(seconds int64) (time.Duration, error) {
	if seconds <= 0 || seconds > math.MaxInt64/int64(time.Second) {
		return 0, errors.New("kuwo: invalid duration")
	}
	return time.Duration(seconds) * time.Second, nil
}
