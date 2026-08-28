package kuwo

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/liuran001/MusicBot-Go/bot/platform"
)

const (
	kuwoMobilePlayURL = "https://mobi.kuwo.cn/mobi.s"
	kuwoWebPlayURL    = "https://www.kuwo.cn/api/v1/www/music/playUrl"
	mediaUserAgent    = "okhttp/3.10.0"
	mediaReferer      = "https://www.kuwo.cn/"
	maxMobileBody     = 1 << 20
	maxMediaSize      = int64(2 << 30)
	maxID3TagSize     = int64(16 << 20)
)

var (
	errPaidTrack             = errors.New("kuwo: paid track")
	errPreviewMedia          = errors.New("kuwo: preview media")
	errTrackIdentityMismatch = errors.New("kuwo: track identity mismatch")
	errTrackDurationMismatch = errors.New("kuwo: track duration mismatch")
	errUnsafeMediaURL        = errors.New("kuwo: unsafe media URL")
	errTerminalCandidate     = errors.New("kuwo: terminal media error")
)

type mobileQuality struct {
	br      string
	format  string
	bitrate int
	quality platform.Quality
}

type losslessResolver uint8

const (
	resolvePlayableFLAC losslessResolver = iota + 1
	resolvePlayableHiRes
	resolvePlayableExternalLossless
)

type mediaProbe struct {
	size          int64
	format        string
	bitrate       int
	quality       platform.Quality
	duration      time.Duration
	sampleRate    int
	bitsPerSample int
	channels      int
	totalSamples  uint64
	flacHeader    [42]byte
}

type flacStreamInfo struct {
	minBlockSize  int
	maxBlockSize  int
	minFrameSize  int
	maxFrameSize  int
	sampleRate    int
	channels      int
	bitsPerSample int
	totalSamples  uint64
	duration      time.Duration
}

func mobileQualityCandidates(quality platform.Quality) []mobileQuality {
	standard := mobileQuality{br: "128kmp3", format: "mp3", bitrate: 128, quality: platform.QualityStandard}
	high := mobileQuality{br: "320kmp3", format: "mp3", bitrate: 320, quality: platform.QualityHigh}
	switch quality {
	case platform.QualityStandard:
		return []mobileQuality{standard}
	case platform.QualityHigh:
		return []mobileQuality{high, standard}
	case platform.QualityLossless, platform.QualityHiRes:
		return []mobileQuality{high, standard}
	default:
		return []mobileQuality{standard}
	}
}

func losslessResolverPlan(quality platform.Quality) []losslessResolver {
	switch quality {
	case platform.QualityLossless:
		return []losslessResolver{resolvePlayableFLAC, resolvePlayableExternalLossless}
	case platform.QualityHiRes:
		// The external resolver stays first on purpose: for paid metadata it is
		// the only path allowed to serve Hi-Res, because its result is strictly
		// verified before use. Reordering it behind the mobile endpoint would
		// let a paid track reach that endpoint, which this tier must not do.
		return []losslessResolver{
			resolvePlayableHiRes,
			resolvePlayableFLAC,
			resolvePlayableExternalLossless,
		}
	default:
		return nil
	}
}

func mediaHeaders() map[string]string {
	return map[string]string{
		"User-Agent": mediaUserAgent,
		"Referer":    mediaReferer,
	}
}

func parseSafeMediaURL(rawURL string) (*url.URL, error) {
	// net/url intentionally drops an empty fragment delimiter when serializing,
	// so reject the raw delimiter before parsing or normalizing it away.
	if strings.Contains(rawURL, "#") {
		return nil, errUnsafeMediaURL
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return nil, errUnsafeMediaURL
	}
	if parsed.User != nil || parsed.Port() != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return nil, errUnsafeMediaURL
	}
	if parsed.RawQuery != "" {
		if _, ok := parseMobileMediaPseudoQuery(parsed.RawQuery); !ok {
			return nil, errUnsafeMediaURL
		}
	}
	if parsed.Host != parsed.Hostname() {
		return nil, errUnsafeMediaURL
	}
	host := strings.ToLower(parsed.Hostname())
	labels := strings.Split(host, ".")
	if len(labels) != 3 || labels[1] != "kuwo" || labels[2] != "cn" {
		return nil, errUnsafeMediaURL
	}
	if !strings.HasPrefix(labels[0], "kw-") && !strings.HasSuffix(labels[0], "-sycdn") {
		return nil, errUnsafeMediaURL
	}
	return parsed, nil
}

func validateMediaURL(rawURL, format string) error {
	parsed, err := parseSafeMediaURL(rawURL)
	if err != nil {
		return err
	}
	if parsed.RawQuery != "" {
		queryFormat, ok := parseMobileMediaPseudoQuery(parsed.RawQuery)
		if !ok || queryFormat != strings.ToLower(format) {
			return errUnsafeMediaURL
		}
	}
	extension := strings.ToLower(path.Ext(parsed.EscapedPath()))
	if extension != "."+strings.ToLower(format) || (extension != ".flac" && extension != ".mp3") {
		return errors.New("kuwo: unexpected media suffix")
	}
	return nil
}

func parseMobileMediaPseudoQuery(rawQuery string) (string, bool) {
	segments := strings.Split(rawQuery, "&")
	if len(segments) != 6 {
		return "", false
	}
	values := make(map[string]string, len(segments))
	for _, segment := range segments {
		name, value, found := strings.Cut(segment, "$")
		if !found || name == "" || strings.Contains(value, "$") {
			return "", false
		}
		if _, duplicate := values[name]; duplicate {
			return "", false
		}
		switch name {
		case "bitrate":
			if value != "128" && value != "320" && value != "2000" {
				return "", false
			}
		case "format":
			if value != "mp3" && value != "flac" {
				return "", false
			}
		case "source", "type":
			if !isSafeMediaQueryToken(value, 128) {
				return "", false
			}
		case "user":
			if !isASCIIUnsignedDecimal(value, 32) {
				return "", false
			}
		case "loginUid":
			if value != "" && !isASCIIUnsignedDecimal(value, 32) {
				return "", false
			}
		default:
			return "", false
		}
		values[name] = value
	}
	bitrate, hasBitrate := values["bitrate"]
	queryFormat, hasFormat := values["format"]
	_, hasSource := values["source"]
	_, hasType := values["type"]
	_, hasUser := values["user"]
	_, hasLoginUID := values["loginUid"]
	if !hasBitrate || !hasFormat || !hasSource || !hasType || !hasUser || !hasLoginUID {
		return "", false
	}
	if (queryFormat == "flac" && bitrate != "2000") ||
		(queryFormat == "mp3" && bitrate != "128" && bitrate != "320") {
		return "", false
	}
	return queryFormat, true
}

func isSafeMediaQueryToken(value string, maximumLength int) bool {
	if value == "" || len(value) > maximumLength {
		return false
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			character == '.' ||
			character == '_' ||
			character == '-' {
			continue
		}
		return false
	}
	return true
}

func normalizeSafeMediaURL(rawURL string) (string, error) {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return "", errors.New("kuwo: empty media URL")
	}
	if strings.Contains(trimmed, "#") {
		return "", errUnsafeMediaURL
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", errUnsafeMediaURL
	}
	parsed.Scheme = "https"
	// Some Kuwo CDN nodes append a bare query delimiter to an otherwise
	// opaque media path. It carries no data, so canonicalize only that exact
	// form while continuing to reject every non-empty query below.
	if parsed.ForceQuery && parsed.RawQuery == "" {
		parsed.ForceQuery = false
	}
	normalized := parsed.String()
	if _, err := parseSafeMediaURL(normalized); err != nil {
		return "", err
	}
	return normalized, nil
}

func normalizeMediaURL(rawURL, format string) (string, error) {
	normalized, err := normalizeSafeMediaURL(rawURL)
	if err != nil {
		return "", err
	}
	if err := validateMediaURL(normalized, format); err != nil {
		return "", err
	}
	return normalized, nil
}

func probeMedia(ctx context.Context, baseClient *http.Client, rawURL, format string, expectedDuration time.Duration) (mediaProbe, error) {
	if err := validateMediaURL(rawURL, format); err != nil {
		return mediaProbe{}, err
	}
	if baseClient == nil {
		return mediaProbe{}, errors.New("kuwo: media client unavailable")
	}
	client := *baseClient
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if err := validateMediaURL(req.URL.String(), format); err != nil {
			return err
		}
		applyMediaHeaders(req)
		if len(via) >= 10 {
			return errors.New("kuwo: too many media redirects")
		}
		return nil
	}
	switch format {
	case "flac":
		return probeFLAC(ctx, &client, rawURL, expectedDuration)
	case "mp3":
		return probeMP3(ctx, &client, rawURL, expectedDuration)
	default:
		return mediaProbe{}, errors.New("kuwo: unsupported media format")
	}
}

func applyMediaHeaders(req *http.Request) {
	req.Header.Set("User-Agent", mediaUserAgent)
	req.Header.Set("Referer", mediaReferer)
}

func readMediaRange(ctx context.Context, client *http.Client, rawURL string, start, end, expectedTotal int64) ([]byte, int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("kuwo: create media probe: %w", err)
	}
	applyMediaHeaders(req)
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("kuwo: probe media: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, 0, platform.NewRateLimitedError("kuwo")
	}
	if resp.StatusCode != http.StatusPartialContent {
		return nil, 0, fmt.Errorf("kuwo: media range returned HTTP %d", resp.StatusCode)
	}
	prefix := fmt.Sprintf("bytes %d-%d/", start, end)
	contentRange := resp.Header.Get("Content-Range")
	if !strings.HasPrefix(contentRange, prefix) {
		return nil, 0, errors.New("kuwo: invalid media content range")
	}
	totalText := strings.TrimPrefix(contentRange, prefix)
	total, err := strconv.ParseInt(totalText, 10, 64)
	if err != nil || total < end+1 || total > maxMediaSize || (expectedTotal > 0 && total != expectedTotal) {
		return nil, 0, errors.New("kuwo: invalid media total size")
	}
	want := end - start + 1
	body, err := io.ReadAll(io.LimitReader(resp.Body, want+1))
	if err != nil {
		return nil, 0, fmt.Errorf("kuwo: read media probe: %w", err)
	}
	if int64(len(body)) != want {
		return nil, 0, errors.New("kuwo: incomplete media probe")
	}
	return body, total, nil
}

func probeFLAC(ctx context.Context, client *http.Client, rawURL string, expectedDuration time.Duration) (mediaProbe, error) {
	data, total, err := readMediaRange(ctx, client, rawURL, 0, 41, 0)
	if err != nil {
		return mediaProbe{}, err
	}
	return parseFLACProbe(data, total, expectedDuration)
}

func parseFLACProbe(data []byte, total int64, expectedDuration time.Duration) (mediaProbe, error) {
	streamInfo, err := parseFLACStreamInfo(data)
	if err != nil {
		return mediaProbe{}, err
	}
	if !durationsMatch(streamInfo.duration, expectedDuration) {
		return mediaProbe{}, terminalUnavailable(errPreviewMedia, errTrackDurationMismatch)
	}
	quality := platform.QualityLossless
	if streamInfo.sampleRate > 48000 || streamInfo.bitsPerSample > 16 {
		quality = platform.QualityHiRes
	}
	probe := mediaProbe{
		size:          total,
		format:        "flac",
		bitrate:       averageBitrateKbps(total, streamInfo.duration),
		quality:       quality,
		duration:      streamInfo.duration,
		sampleRate:    streamInfo.sampleRate,
		bitsPerSample: streamInfo.bitsPerSample,
		channels:      streamInfo.channels,
		totalSamples:  streamInfo.totalSamples,
	}
	copy(probe.flacHeader[:], data[:len(probe.flacHeader)])
	return probe, nil
}

func parseFLACStreamInfo(data []byte) (flacStreamInfo, error) {
	if len(data) < 42 {
		return flacStreamInfo{}, errors.New("kuwo: incomplete FLAC STREAMINFO")
	}
	if !bytes.Equal(data[:4], []byte("fLaC")) {
		return flacStreamInfo{}, errors.New("kuwo: invalid FLAC signature")
	}
	if data[4]&0x7f != 0 || int(data[5])<<16|int(data[6])<<8|int(data[7]) != 34 {
		return flacStreamInfo{}, errors.New("kuwo: invalid FLAC STREAMINFO")
	}
	minBlock := binary.BigEndian.Uint16(data[8:10])
	maxBlock := binary.BigEndian.Uint16(data[10:12])
	if minBlock < 16 || maxBlock < 16 || minBlock > maxBlock {
		return flacStreamInfo{}, errors.New("kuwo: invalid FLAC block size")
	}
	minFrame := int(data[12])<<16 | int(data[13])<<8 | int(data[14])
	maxFrame := int(data[15])<<16 | int(data[16])<<8 | int(data[17])
	if minFrame > 0 && maxFrame > 0 && minFrame > maxFrame {
		return flacStreamInfo{}, errors.New("kuwo: invalid FLAC frame size")
	}
	packed := binary.BigEndian.Uint64(data[18:26])
	sampleRate := int((packed >> 44) & 0xfffff)
	channels := int((packed>>41)&7) + 1
	bitsPerSample := int((packed>>36)&31) + 1
	totalSamples := packed & 0xfffffffff
	if sampleRate == 0 || totalSamples == 0 || channels < 1 || channels > 8 || bitsPerSample < 4 || bitsPerSample > 32 {
		return flacStreamInfo{}, errors.New("kuwo: invalid FLAC audio parameters")
	}
	duration := time.Duration(float64(totalSamples) / float64(sampleRate) * float64(time.Second))
	return flacStreamInfo{
		minBlockSize:  int(minBlock),
		maxBlockSize:  int(maxBlock),
		minFrameSize:  minFrame,
		maxFrameSize:  maxFrame,
		sampleRate:    sampleRate,
		bitsPerSample: bitsPerSample,
		channels:      channels,
		totalSamples:  totalSamples,
		duration:      duration,
	}, nil
}

func probeMP3(ctx context.Context, client *http.Client, rawURL string, expectedDuration time.Duration) (mediaProbe, error) {
	data, total, err := readMediaRange(ctx, client, rawURL, 0, 15, 0)
	if err != nil {
		return mediaProbe{}, err
	}
	frame := data
	if bytes.HasPrefix(data, []byte("ID3")) {
		for _, value := range data[6:10] {
			if value&0x80 != 0 {
				return mediaProbe{}, errors.New("kuwo: invalid ID3 syncsafe size")
			}
		}
		tagSize := int64(data[6])<<21 | int64(data[7])<<14 | int64(data[8])<<7 | int64(data[9])
		if tagSize > maxID3TagSize {
			return mediaProbe{}, errors.New("kuwo: ID3 tag too large")
		}
		footerSize := int64(0)
		if data[5]&0x10 != 0 {
			footerSize = 10
		}
		offset := int64(10) + tagSize + footerSize
		if offset+15 >= total {
			return mediaProbe{}, errors.New("kuwo: ID3 tag exceeds media")
		}
		frame, _, err = readMediaRange(ctx, client, rawURL, offset, offset+15, total)
		if err != nil {
			return mediaProbe{}, err
		}
	}
	if !validMPEGHeader(frame) {
		return mediaProbe{}, errors.New("kuwo: invalid MPEG frame header")
	}
	bitrate := averageBitrateKbps(total, expectedDuration)
	quality := platform.QualityStandard
	switch {
	case bitrate >= 256 && bitrate <= 384:
		quality = platform.QualityHigh
	case bitrate >= 102 && bitrate <= 154:
		quality = platform.QualityStandard
	default:
		return mediaProbe{}, errors.New("kuwo: unexpected MP3 average bitrate")
	}
	return mediaProbe{size: total, format: "mp3", bitrate: bitrate, quality: quality, duration: expectedDuration}, nil
}

func validMPEGHeader(data []byte) bool {
	if len(data) < 4 {
		return false
	}
	header := binary.BigEndian.Uint32(data[:4])
	if header>>21 != 0x7ff {
		return false
	}
	version := (header >> 19) & 3
	layer := (header >> 17) & 3
	bitrateIndex := (header >> 12) & 15
	sampleRate := (header >> 10) & 3
	emphasis := header & 3
	return version != 1 && layer != 0 && bitrateIndex != 0 && bitrateIndex != 15 && sampleRate != 3 && emphasis != 2
}

func averageBitrateKbps(size int64, duration time.Duration) int {
	if size <= 0 || duration <= 0 {
		return 0
	}
	return int(math.Round(float64(size) * 8 / duration.Seconds() / 1000))
}

func durationsMatch(actual, expected time.Duration) bool {
	if actual <= 0 || expected <= 0 {
		return false
	}
	tolerance := 5 * time.Second
	if proportional := expected / 20; proportional > tolerance {
		tolerance = proportional
	}
	difference := actual - expected
	if difference < 0 {
		difference = -difference
	}
	return difference <= tolerance
}

func terminalUnavailable(reasons ...error) error {
	items := []error{platform.ErrUnavailable, errTerminalCandidate}
	items = append(items, reasons...)
	return errors.Join(items...)
}

func isTerminalMediaError(err error) bool {
	return errors.Is(err, errTerminalCandidate) ||
		errors.Is(err, platform.ErrRateLimited) ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded)
}

type mobilePlayResponse struct {
	Code jsonScalar     `json:"code"`
	Data mobilePlayData `json:"data"`
}

// rawJSONValue deliberately preserves composite and null values. The shared
// jsonScalar rejects composites during decoding, which is the right default for
// most Kuwo response models. Candidate media responses need field-level
// classification instead: malformed identity values fail closed, while a valid
// identity for a different track may discard only that candidate.
type rawJSONValue struct {
	raw json.RawMessage
}

func (v *rawJSONValue) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	v.raw = append(v.raw[:0], trimmed...)
	return nil
}

func (v rawJSONValue) scalar() jsonScalar {
	return jsonScalar{raw: v.raw}
}

// canonicalMediaTypeZero accepts only the numeric token 0 or a JSON string
// whose value is exactly "0". In particular, numeric/string spellings such as
// -0, 0.0, "+0", "00", and "-0" are not canonical mobile media types.
func (v rawJSONValue) canonicalMediaTypeZero() bool {
	raw := bytes.TrimSpace(v.raw)
	if bytes.Equal(raw, []byte("0")) {
		return true
	}
	var text string
	return json.Unmarshal(raw, &text) == nil && text == "0"
}

type mobilePlayData struct {
	RID      rawJSONValue `json:"rid"`
	URL      rawJSONValue `json:"url"`
	Format   rawJSONValue `json:"format"`
	Bitrate  rawJSONValue `json:"bitrate"`
	Duration rawJSONValue `json:"duration"`
	Type     rawJSONValue `json:"type"`
}

func (c *Client) GetDownloadInfo(ctx context.Context, trackID string, quality platform.Quality) (*platform.DownloadInfo, error) {
	detail, access, err := c.getTrackDetail(ctx, trackID)
	if err != nil {
		return nil, err
	}
	accessErr := validateTrackAccess(access)
	resolverPlan := losslessResolverPlan(quality)
	if accessErr != nil && len(resolverPlan) == 0 {
		return nil, accessErr
	}
	var lastErr error
	if quality == platform.QualityHigh {
		info, candidateErr := c.resolvePlayableExternalHigh(ctx, detail)
		if candidateErr == nil {
			return info, nil
		}
		if isTerminalMediaError(candidateErr) {
			return nil, candidateErr
		}
		lastErr = candidateErr
	}
	for _, resolver := range resolverPlan {
		var (
			info         *platform.DownloadInfo
			candidateErr error
		)
		switch resolver {
		case resolvePlayableFLAC:
			info, candidateErr = c.resolvePlayableLossless(ctx, detail)
		case resolvePlayableHiRes:
			info, candidateErr = c.resolvePlayableHiRes(ctx, detail)
		case resolvePlayableExternalLossless:
			info, candidateErr = c.resolvePlayableExternalLossless(ctx, detail)
		default:
			continue
		}
		if candidateErr == nil {
			return info, nil
		}
		if isTerminalMediaError(candidateErr) {
			return nil, candidateErr
		}
		lastErr = candidateErr
	}
	// Paid/preview metadata must never fall through to ordinary MP3 candidates.
	// A requested lossless tier may still use a public direct FLAC, but only
	// after its selector, identity, duration, STREAMINFO, size, and URL have all
	// passed the resolver-specific checks above.
	if accessErr != nil {
		// Report why the resolvers actually failed alongside the access flag.
		// Returning accessErr alone made every failure read as "paid track"
		// whatever went wrong: a region block, an unreachable host and a genuine
		// entitlement problem were indistinguishable in the logs.
		if lastErr != nil {
			return nil, fmt.Errorf("%w (lossless resolvers failed: %v)", accessErr, lastErr)
		}
		return nil, accessErr
	}
	for _, candidate := range mobileQualityCandidates(quality) {
		info, candidateErr := c.resolveMobileDownload(ctx, detail, candidate)
		if candidateErr == nil {
			return info, nil
		}
		if isTerminalMediaError(candidateErr) {
			return nil, candidateErr
		}
		lastErr = candidateErr
	}
	info, err := c.resolveWebDownload(ctx, detail)
	if err == nil {
		return info, nil
	}
	if isTerminalMediaError(err) {
		return nil, err
	}
	if lastErr != nil {
		return nil, fmt.Errorf("kuwo: mobile candidates failed: %v; web fallback: %w", lastErr, err)
	}
	return nil, err
}

func validateTrackAccess(access trackAccess) error {
	restricted, valid := parseAccessRestriction(access.listenFee)
	if !valid || restricted {
		return terminalUnavailable(errPaidTrack)
	}
	if len(access.payInfo) == 0 || bytes.Equal(bytes.TrimSpace(access.payInfo), []byte("null")) {
		return nil
	}
	if err := validateUniqueJSONKeys(access.payInfo); err != nil {
		return terminalUnavailable(errPaidTrack)
	}
	var payInfo struct {
		CannotOnlinePlay json.RawMessage `json:"cannotOnlinePlay"`
		ListenFragment   json.RawMessage `json:"listen_fragment"`
	}
	if err := json.Unmarshal(access.payInfo, &payInfo); err != nil {
		return terminalUnavailable(errPaidTrack)
	}
	for _, raw := range []json.RawMessage{
		payInfo.CannotOnlinePlay,
		payInfo.ListenFragment,
	} {
		restricted, valid := parseAccessRestriction(raw)
		if !valid || restricted {
			return terminalUnavailable(errPaidTrack)
		}
	}
	return nil
}

// parseAccessRestriction treats an absent field as unrestricted. Once a known
// restriction field is present, only the explicit boolean/integer states used
// by Kuwo are accepted; null, composites, and type drift fail closed.
func parseAccessRestriction(raw json.RawMessage) (restricted, valid bool) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return false, true
	}
	var value jsonScalar
	if err := value.UnmarshalJSON(raw); err != nil || len(value.raw) == 0 {
		return false, false
	}
	if flag, ok := value.Bool(); ok {
		return flag, true
	}
	number, ok := value.Int64()
	if !ok || (number != 0 && number != 1) {
		return false, false
	}
	return number == 1, true
}

func (c *Client) resolveMobileDownload(ctx context.Context, detail *trackDetail, candidate mobileQuality) (*platform.DownloadInfo, error) {
	endpoint := c.endpoints.mobile
	if endpoint == "" {
		endpoint = kuwoMobilePlayURL
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("kuwo: parse mobile play URL: %w", err)
	}
	query := parsed.Query()
	for key, value := range map[string]string{
		"user": "359307055300426", "source": "kwplayer_ar_5.1.0.0_B_jiakong_vh.apk",
		"type": "convert_url_with_sign", "sig": "0", "network": "WIFI", "f": "web",
		"rid": detail.ID, "br": candidate.br, "format": candidate.format,
	} {
		query.Set(key, value)
	}
	parsed.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("kuwo: create mobile play request: %w", err)
	}
	req.Header.Set("User-Agent", mediaUserAgent)
	resp, err := c.sessionlessAPIClient().Do(req)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		return nil, fmt.Errorf("kuwo: request mobile play API: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, platform.NewRateLimitedError("kuwo")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("kuwo: mobile play API returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxMobileBody+1))
	if err != nil {
		return nil, fmt.Errorf("kuwo: read mobile play response: %w", err)
	}
	if len(body) > maxMobileBody {
		return nil, errors.New("kuwo: mobile play response too large")
	}
	var result mobilePlayResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("kuwo: decode mobile play response: %w", err)
	}
	code, ok := result.Code.Int64()
	if !ok || code != 200 {
		if code == -1 || code == -1001 {
			return nil, terminalUnavailable()
		}
		return nil, errors.New("kuwo: invalid mobile play response")
	}
	return c.downloadInfoFromMobileData(ctx, detail, candidate, result.Data)
}

func (c *Client) downloadInfoFromMobileData(ctx context.Context, detail *trackDetail, candidate mobileQuality, data mobilePlayData) (*platform.DownloadInfo, error) {
	rid := normalizeRID(scalarText(data.RID.scalar()))
	if rid == "" {
		return nil, terminalUnavailable(errTrackIdentityMismatch)
	}
	if rid != detail.ID {
		return nil, errTrackIdentityMismatch
	}
	if !data.Type.canonicalMediaTypeZero() {
		return nil, terminalUnavailable(errPreviewMedia)
	}
	durationSeconds, ok := data.Duration.scalar().Int64()
	if !ok || durationSeconds <= 0 || durationSeconds > math.MaxInt64/int64(time.Second) {
		return nil, terminalUnavailable(errPreviewMedia, errTrackDurationMismatch)
	}
	duration := time.Duration(durationSeconds) * time.Second
	if !durationsMatch(duration, detail.Duration) {
		return nil, terminalUnavailable(errPreviewMedia, errTrackDurationMismatch)
	}
	declaredBitrate, bitrateOK := data.Bitrate.scalar().Int64()
	if bitrateOK && declaredBitrate > 0 && declaredBitrate <= 1 {
		return nil, terminalUnavailable(errPreviewMedia)
	}
	if !bitrateOK || declaredBitrate != int64(candidate.bitrate) {
		return nil, errors.New("kuwo: candidate bitrate mismatch")
	}
	declaredFormat := strings.ToLower(scalarText(data.Format.scalar()))
	if declaredFormat == "" || declaredFormat != candidate.format {
		return nil, errors.New("kuwo: candidate format mismatch")
	}
	rawURLValue := scalarText(data.URL.scalar())
	var rawURL string
	if strings.TrimSpace(rawURLValue) != "" {
		var err error
		rawURL, err = normalizeSafeMediaURL(rawURLValue)
		if err != nil {
			if errors.Is(err, errUnsafeMediaURL) {
				return nil, terminalUnavailable(err)
			}
			return nil, err
		}
	}
	if rawURL == "" {
		var err error
		rawURL, err = normalizeMediaURL(rawURLValue, candidate.format)
		if err != nil {
			if errors.Is(err, errUnsafeMediaURL) {
				return nil, terminalUnavailable(err)
			}
			return nil, err
		}
	} else if err := validateMediaURL(rawURL, candidate.format); err != nil {
		if errors.Is(err, errUnsafeMediaURL) {
			return nil, terminalUnavailable(err)
		}
		return nil, err
	}
	probe, err := probeMedia(ctx, c.mediaHTTPClient, rawURL, candidate.format, detail.Duration)
	if err != nil {
		if errors.Is(err, errUnsafeMediaURL) {
			return nil, terminalUnavailable(err)
		}
		return nil, err
	}
	if candidate.format == "mp3" {
		if candidate.bitrate == 320 && (probe.bitrate < 256 || probe.bitrate > 384) {
			return nil, errors.New("kuwo: 320k candidate failed bitrate verification")
		}
		if candidate.bitrate == 128 && (probe.bitrate < 102 || probe.bitrate > 154) {
			return nil, errors.New("kuwo: 128k candidate failed bitrate verification")
		}
	}
	return c.buildDownloadInfo(rawURL, candidate.format, probe), nil
}

func (c *Client) resolveWebDownload(ctx context.Context, detail *trackDetail) (*platform.DownloadInfo, error) {
	endpoint := c.endpoints.play
	if endpoint == "" {
		endpoint = kuwoWebPlayURL
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("kuwo: parse web play URL: %w", err)
	}
	query := parsed.Query()
	query.Set("mid", detail.ID)
	query.Set("type", "music")
	query.Set("httpsStatus", "1")
	parsed.RawQuery = query.Encode()
	body, err := c.signedGet(ctx, parsed.String(), kuwoHomeURL)
	if err != nil {
		return nil, err
	}
	var response struct {
		Code jsonScalar `json:"code"`
		Data struct {
			URL jsonScalar `json:"url"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("kuwo: decode web play response: %w", err)
	}
	code, ok := response.Code.Int64()
	if !ok || code != 200 {
		if code == -1 || code == -1001 {
			return nil, terminalUnavailable()
		}
		return nil, errors.New("kuwo: invalid web play response")
	}
	webURL := strings.TrimSpace(scalarText(response.Data.URL))
	if webURL == "" {
		return nil, terminalUnavailable()
	}
	rawURL, err := normalizeMediaURL(webURL, "mp3")
	if err != nil {
		if errors.Is(err, errUnsafeMediaURL) {
			return nil, terminalUnavailable(err)
		}
		return nil, err
	}
	probe, err := probeMedia(ctx, c.mediaHTTPClient, rawURL, "mp3", detail.Duration)
	if err != nil {
		if errors.Is(err, errUnsafeMediaURL) {
			return nil, terminalUnavailable(err)
		}
		return nil, err
	}
	// probeMP3 already accepts only verified 128k or 320k average-bitrate
	// buckets. The Web endpoint may return either representation for the same
	// track, so preserve the quality established from the actual media.
	return c.buildDownloadInfo(rawURL, "mp3", probe), nil
}

func (c *Client) buildDownloadInfo(rawURL, format string, probe mediaProbe) *platform.DownloadInfo {
	expiresAt := c.now().Add(10 * time.Minute)
	return &platform.DownloadInfo{
		URL:       rawURL,
		Headers:   mediaHeaders(),
		Size:      probe.size,
		Format:    format,
		Bitrate:   probe.bitrate,
		Quality:   probe.quality,
		ExpiresAt: &expiresAt,
		ValidateURL: func(candidateURL string) error {
			return validateMediaURL(candidateURL, format)
		},
	}
}
