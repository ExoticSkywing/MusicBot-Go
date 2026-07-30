package kuwo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/liuran001/MusicBot-Go/bot/platform"
)

const (
	kuwoDirectHiResResolveURL = "https://kw-api.cenguigui.cn/"
	maxDirectHiResBody        = 64 << 10
	directHiResBitrate        = int64(4000)
)

type directHiResResponse struct {
	Code jsonScalar       `json:"code"`
	Msg  jsonScalar       `json:"msg"`
	Data *directHiResData `json:"data"`
}

type directHiResData struct {
	RID      jsonScalar       `json:"rid"`
	Bitrate  jsonScalar       `json:"bitrate"`
	Duration jsonScalar       `json:"duration"`
	Size     jsonScalar       `json:"size"`
	URL      jsonScalar       `json:"url"`
	Level    directHiResLevel `json:"level"`
}

type directHiResLevel struct {
	Requested jsonScalar           `json:"requested"`
	Actual    jsonScalar           `json:"actual"`
	EKey      jsonScalar           `json:"ekey"`
	Quality   []directHiResQuality `json:"quality"`
}

type directHiResQuality struct {
	Bitrate jsonScalar `json:"br"`
	Format  jsonScalar `json:"format"`
	Level   jsonScalar `json:"level"`
}

func (c *Client) resolvePlayableHiRes(
	ctx context.Context,
	detail *trackDetail,
) (*platform.DownloadInfo, error) {
	if c == nil || detail == nil || !isASCIIUnsignedDecimal(detail.ID, 20) || detail.Duration <= 0 {
		return nil, terminalUnavailable(errTrackIdentityMismatch)
	}
	if ctx == nil {
		ctx = context.Background()
	}

	endpoint := c.endpoints.qualityResolver
	if endpoint == "" {
		endpoint = kuwoDirectHiResResolveURL
	}
	parsed, err := url.Parse(endpoint)
	if err != nil ||
		parsed.Scheme != "https" ||
		parsed.Host == "" ||
		parsed.User != nil ||
		parsed.Port() != "" ||
		parsed.Fragment != "" {
		return nil, errors.New("kuwo: invalid direct Hi-Res resolver URL")
	}
	query := parsed.Query()
	query.Set("id", detail.ID)
	query.Set("type", "song")
	query.Set("level", "hires")
	query.Set("format", "json")
	parsed.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("kuwo: create direct Hi-Res resolver request: %w", err)
	}
	req.Header.Set("User-Agent", kuwoUserAgent)
	req.Header.Set("Accept", "application/json")
	client := c.sessionlessAPIClient()
	resolverOrigin := parsed.Scheme + "://" + parsed.Host
	client.CheckRedirect = func(redirect *http.Request, via []*http.Request) error {
		redirectQuery := redirect.URL.Query()
		if len(via) >= 3 {
			return errors.New("kuwo: too many direct Hi-Res resolver redirects")
		}
		if redirect.URL.Scheme+"://"+redirect.URL.Host != resolverOrigin ||
			redirect.Method != http.MethodGet ||
			redirectQuery.Get("id") != detail.ID ||
			redirectQuery.Get("type") != "song" ||
			redirectQuery.Get("level") != "hires" ||
			redirectQuery.Get("format") != "json" {
			return errors.New("kuwo: unsafe direct Hi-Res resolver redirect")
		}
		redirect.Header.Set("User-Agent", kuwoUserAgent)
		redirect.Header.Set("Accept", "application/json")
		return nil
	}

	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		return nil, fmt.Errorf("kuwo: request direct Hi-Res resolver: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, platform.NewRateLimitedError("kuwo")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("kuwo: direct Hi-Res resolver returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxDirectHiResBody+1))
	if err != nil {
		return nil, fmt.Errorf("kuwo: read direct Hi-Res resolver response: %w", err)
	}
	if len(body) == 0 {
		return nil, errors.New("kuwo: empty direct Hi-Res resolver response")
	}
	if len(body) > maxDirectHiResBody {
		return nil, errors.New("kuwo: direct Hi-Res resolver response too large")
	}
	if err := validateUniqueJSONKeys(body); err != nil {
		return nil, fmt.Errorf("kuwo: invalid direct Hi-Res resolver JSON: %w", err)
	}

	var result directHiResResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("kuwo: decode direct Hi-Res resolver response: %w", err)
	}
	code, ok := result.Code.Int64()
	if !ok || code != 200 || result.Data == nil {
		return nil, errors.New("kuwo: direct Hi-Res resolver unavailable")
	}
	data := result.Data
	if normalizeRID(scalarText(data.RID)) != detail.ID {
		return nil, terminalUnavailable(errTrackIdentityMismatch)
	}
	bitrate, ok := data.Bitrate.Int64()
	if !ok || bitrate != directHiResBitrate {
		return nil, errors.New("kuwo: direct Hi-Res selector bitrate mismatch")
	}
	durationSeconds, ok := data.Duration.Int64()
	if !ok {
		return nil, terminalUnavailable(errPreviewMedia, errTrackDurationMismatch)
	}
	duration, err := secondsDuration(durationSeconds)
	if err != nil || !durationsMatch(duration, detail.Duration) {
		return nil, terminalUnavailable(errPreviewMedia, errTrackDurationMismatch)
	}
	if strings.ToLower(scalarText(data.Level.Requested)) != "hires" ||
		strings.ToLower(scalarText(data.Level.Actual)) != "hires" {
		return nil, errors.New("kuwo: direct Hi-Res resolver downgraded quality")
	}
	if scalarText(data.Level.EKey) != "" {
		return nil, errors.New("kuwo: encrypted direct Hi-Res response")
	}
	if !containsDirectHiResQuality(data.Level.Quality) {
		return nil, errors.New("kuwo: direct Hi-Res quality list mismatch")
	}
	declaredSize, ok := parseDirectHiResSize(scalarText(data.Size))
	if !ok {
		return nil, errors.New("kuwo: invalid direct Hi-Res resolver size")
	}

	rawURL, err := normalizeMediaURL(scalarText(data.URL), "flac")
	if err != nil {
		if errors.Is(err, errUnsafeMediaURL) {
			return nil, terminalUnavailable(err)
		}
		return nil, err
	}
	probe, err := probeDirectHiResFLAC(
		ctx,
		c.mediaHTTPClient,
		rawURL,
		detail.Duration,
	)
	if err != nil {
		if errors.Is(err, errUnsafeMediaURL) {
			return nil, terminalUnavailable(err)
		}
		return nil, err
	}
	if probe.sampleRate < 96000 ||
		probe.bitsPerSample < 24 ||
		probe.channels != 2 ||
		probe.quality != platform.QualityHiRes {
		return nil, errors.New("kuwo: direct Hi-Res STREAMINFO mismatch")
	}
	if !directHiResSizeMatches(declaredSize, probe.size) {
		return nil, errors.New("kuwo: direct Hi-Res media size mismatch")
	}
	rawSize := probe.size
	trailerProbe, err := c.probeDirectFLACTrailer(
		ctx,
		rawURL,
		rawSize,
	)
	if err != nil {
		if errors.Is(err, errUnsafeMediaURL) {
			return nil, terminalUnavailable(err)
		}
		return nil, err
	}
	info := c.buildDownloadInfo(rawURL, "flac", probe)
	info.Size = trailerProbe.outputSize
	info.Downloader = c.directFLACDownloader(
		rawURL,
		rawSize,
		probe.flacHeader,
		trailerProbe.tail,
	)
	return info, nil
}

func probeDirectHiResFLAC(
	ctx context.Context,
	baseClient *http.Client,
	rawURL string,
	expectedDuration time.Duration,
) (mediaProbe, error) {
	if err := validateMediaURL(rawURL, "flac"); err != nil {
		return mediaProbe{}, err
	}
	if baseClient == nil {
		return mediaProbe{}, errors.New("kuwo: media client unavailable")
	}
	client := *baseClient
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if err := validateMediaURL(req.URL.String(), "flac"); err != nil {
			return err
		}
		applyMediaHeaders(req)
		req.Header.Set("Accept-Encoding", "identity")
		if len(via) >= 10 {
			return errors.New("kuwo: too many direct Hi-Res media redirects")
		}
		return nil
	}
	data, total, err := readMediaRange(ctx, &client, rawURL, 0, 41, 0)
	if err != nil {
		return mediaProbe{}, err
	}
	probe, err := parseFLACProbe(data, total, expectedDuration)
	if err != nil {
		return mediaProbe{}, err
	}
	return probe, nil
}

func containsDirectHiResQuality(items []directHiResQuality) bool {
	for _, item := range items {
		bitrate, ok := item.Bitrate.Int64()
		if ok &&
			bitrate == directHiResBitrate &&
			strings.ToLower(scalarText(item.Format)) == "flac" &&
			strings.ToLower(scalarText(item.Level)) == "hires" {
			return true
		}
	}
	return false
}

func parseDirectHiResSize(label string) (int64, bool) {
	label = strings.TrimSpace(label)
	if len(label) < 3 || len(label) > 64 || !strings.EqualFold(label[len(label)-2:], "mb") {
		return 0, false
	}
	number := strings.TrimSpace(label[:len(label)-2])
	wholeText, fractionText, hasFraction := strings.Cut(number, ".")
	if wholeText == "" ||
		(hasFraction && (fractionText == "" || len(fractionText) > 3)) ||
		strings.Contains(fractionText, ".") ||
		!isDirectHiResDecimal(wholeText) ||
		(hasFraction && !isDirectHiResDecimal(fractionText)) {
		return 0, false
	}
	whole, err := strconv.ParseInt(wholeText, 10, 64)
	if err != nil || whole < 0 || whole > maxMediaSize/(1<<20) {
		return 0, false
	}
	scale := int64(1)
	fraction := int64(0)
	if hasFraction {
		for range fractionText {
			scale *= 10
		}
		fraction, err = strconv.ParseInt(fractionText, 10, 64)
		if err != nil {
			return 0, false
		}
	}
	units := whole*scale + fraction
	if units <= 0 || units > maxMediaSize*scale/(1<<20) {
		return 0, false
	}
	size := (units*(1<<20) + scale/2) / scale
	return size, size > 0 && size <= maxMediaSize
}

func directHiResSizeMatches(declared, actual int64) bool {
	if declared <= 0 || actual <= 0 {
		return false
	}
	difference := declared - actual
	if difference < 0 {
		difference = -difference
	}
	tolerance := int64(128 << 10)
	if proportional := actual / 100; proportional > tolerance {
		tolerance = proportional
	}
	return difference <= tolerance
}

func isDirectHiResDecimal(value string) bool {
	if value == "" {
		return false
	}
	for index := 0; index < len(value); index++ {
		if value[index] < '0' || value[index] > '9' {
			return false
		}
	}
	return true
}
