package kuwo

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/liuran001/MusicBot-Go/bot/platform"
	"golang.org/x/text/encoding/simplifiedchinese"
)

const (
	wordLyricXORKey      = "yeelion"
	maxDecodedLyricBytes = 8 << 20
)

var (
	wordTimingTagPattern = regexp.MustCompile(`<-?\d+,-?\d+(?:,-?\d+)?>`)
	lrcTimestampPattern  = regexp.MustCompile(`^\[(\d+):(\d{1,2})(?:[.:](\d{1,3}))?\]`)
	lrcOffsetPattern     = regexp.MustCompile(`(?i)^\s*\[offset:([+-]?\d+)\]\s*$`)
	lrcAnyOffsetPattern  = regexp.MustCompile(`(?i)^\s*\[offset:.*\]\s*$`)
	lrcMetadataPattern   = regexp.MustCompile(`(?i)^\s*\[(?:kuwo|ver|ti|ar|al|by|re|ve|length|id|hash|tool|au):.*\]\s*$`)
)

func buildWordLyricQuery(trackID string) string {
	plain := []byte("user=12345,web,web,web&requester=localhost&req=1&rid=MUSIC_" + trackID + "&lrcx=1")
	key := []byte(wordLyricXORKey)
	encoded := make([]byte, len(plain))
	for index, value := range plain {
		encoded[index] = value ^ key[index%len(key)]
	}
	return base64.StdEncoding.EncodeToString(encoded)
}

func decodeWordLyrics(body []byte) (string, error) {
	header, compressed, ok := bytes.Cut(body, []byte("\r\n\r\n"))
	if !ok || len(compressed) == 0 {
		return "", errors.New("kuwo: malformed enhanced lyric envelope")
	}
	headerLines := bytes.Split(header, []byte("\r\n"))
	if len(headerLines) == 0 || !strings.EqualFold(strings.TrimSpace(string(headerLines[0])), "tp=content") {
		return "", errors.New("kuwo: enhanced lyric response is not content")
	}

	reader, err := zlib.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return "", fmt.Errorf("kuwo: open enhanced lyric payload: %w", err)
	}
	decompressed, readErr := io.ReadAll(io.LimitReader(reader, maxDecodedLyricBytes+1))
	closeErr := reader.Close()
	if readErr != nil {
		return "", fmt.Errorf("kuwo: decompress enhanced lyric payload: %w", readErr)
	}
	if closeErr != nil {
		return "", fmt.Errorf("kuwo: close enhanced lyric payload: %w", closeErr)
	}
	if len(decompressed) > maxDecodedLyricBytes {
		return "", errors.New("kuwo: enhanced lyric payload too large")
	}

	encrypted, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(decompressed)))
	if err != nil {
		return "", fmt.Errorf("kuwo: decode enhanced lyric base64: %w", err)
	}
	key := []byte(wordLyricXORKey)
	decodedBytes := make([]byte, len(encrypted))
	for index, value := range encrypted {
		decodedBytes[index] = value ^ key[index%len(key)]
	}
	decoded, err := decodeGB18030Strict(decodedBytes)
	if err != nil {
		return "", fmt.Errorf("kuwo: decode enhanced lyric text: %w", err)
	}
	return decoded, nil
}

func decodeGB18030Strict(data []byte) (string, error) {
	decoded, err := simplifiedchinese.GB18030.NewDecoder().Bytes(data)
	if err != nil {
		return "", err
	}
	if !utf8.Valid(decoded) {
		return "", errors.New("invalid UTF-8 emitted by GB18030 decoder")
	}
	text := string(decoded)
	if !strings.ContainsRune(text, utf8.RuneError) {
		return text, nil
	}

	for offset := 0; offset < len(data); {
		length, err := gb18030TokenLength(data[offset:])
		if err != nil {
			return "", err
		}
		token := data[offset : offset+length]
		tokenDecoded, err := simplifiedchinese.GB18030.NewDecoder().Bytes(token)
		if err != nil || !utf8.Valid(tokenDecoded) || utf8.RuneCount(tokenDecoded) != 1 {
			return "", fmt.Errorf("invalid GB18030 token at byte %d", offset)
		}
		r, _ := utf8.DecodeRune(tokenDecoded)
		if r == utf8.RuneError {
			reencoded, encodeErr := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte(string(r)))
			if encodeErr != nil || !bytes.Equal(reencoded, token) {
				return "", fmt.Errorf("invalid GB18030 replacement token at byte %d", offset)
			}
		}
		offset += length
	}
	return text, nil
}

func gb18030TokenLength(data []byte) (int, error) {
	if len(data) == 0 {
		return 0, io.ErrUnexpectedEOF
	}
	first := data[0]
	if first <= 0x7F || first == 0x80 {
		return 1, nil
	}
	if first < 0x81 || first > 0xFE || len(data) < 2 {
		return 0, errors.New("invalid or truncated GB18030 lead byte")
	}
	second := data[1]
	if second >= 0x30 && second <= 0x39 {
		if len(data) < 4 ||
			data[2] < 0x81 || data[2] > 0xFE ||
			data[3] < 0x30 || data[3] > 0x39 {
			return 0, errors.New("invalid or truncated GB18030 four-byte token")
		}
		return 4, nil
	}
	if (second >= 0x40 && second <= 0x7E) || (second >= 0x80 && second <= 0xFE) {
		return 2, nil
	}
	return 0, errors.New("invalid GB18030 trail byte")
}

func parseTimedLyrics(raw string) *platform.Lyrics {
	normalized := strings.ReplaceAll(raw, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	physicalLines := strings.Split(normalized, "\n")

	var offset time.Duration
	for _, physical := range physicalLines {
		match := lrcOffsetPattern.FindStringSubmatch(physical)
		if len(match) != 2 {
			continue
		}
		milliseconds, err := strconv.ParseInt(match[1], 10, 64)
		if err != nil || milliseconds > maxDurationMilliseconds() || milliseconds < minDurationMilliseconds() {
			continue
		}
		offset = time.Duration(milliseconds) * time.Millisecond
	}

	timestamped := make([]platform.LyricLine, 0, len(physicalLines))
	plainLines := make([]string, 0, len(physicalLines))
	for _, physical := range physicalLines {
		line := strings.TrimSpace(physical)
		if line == "" || lrcMetadataPattern.MatchString(line) || lrcAnyOffsetPattern.MatchString(line) {
			continue
		}

		rest := line
		timestamps := make([]time.Duration, 0, 2)
		for {
			match := lrcTimestampPattern.FindStringSubmatchIndex(rest)
			if match == nil {
				break
			}
			parts := lrcTimestampPattern.FindStringSubmatch(rest)
			if timestamp, ok := parseLRCTimestamp(parts); ok {
				timestamps = append(timestamps, timestamp)
			}
			rest = rest[match[1]:]
		}
		if len(timestamps) == 0 {
			continue
		}

		text := strings.TrimSpace(wordTimingTagPattern.ReplaceAllString(rest, ""))
		if text == "" {
			continue
		}
		finalTimes := make([]time.Duration, 0, len(timestamps))
		for _, timestamp := range timestamps {
			if adjusted, ok := applyLyricOffset(timestamp, offset); ok {
				finalTimes = append(finalTimes, adjusted)
			}
		}
		if len(finalTimes) == 0 {
			continue
		}
		plainLines = append(plainLines, text)
		for _, timestamp := range finalTimes {
			timestamped = append(timestamped, platform.LyricLine{Time: timestamp, Text: text})
		}
	}
	sort.SliceStable(timestamped, func(left, right int) bool {
		return timestamped[left].Time < timestamped[right].Time
	})
	return &platform.Lyrics{
		Plain:       strings.Join(plainLines, "\n"),
		Timestamped: timestamped,
	}
}

func parseLRCTimestamp(parts []string) (time.Duration, bool) {
	if len(parts) != 4 {
		return 0, false
	}
	minutes, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || minutes < 0 {
		return 0, false
	}
	seconds, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || seconds < 0 || seconds >= 60 {
		return 0, false
	}
	fraction := int64(0)
	if parts[3] != "" {
		parsed, err := strconv.ParseInt(parts[3], 10, 64)
		if err != nil {
			return 0, false
		}
		switch len(parts[3]) {
		case 1:
			fraction = parsed * 100
		case 2:
			fraction = parsed * 10
		case 3:
			fraction = parsed
		default:
			return 0, false
		}
	}
	baseMilliseconds := seconds*1000 + fraction
	maxMilliseconds := maxDurationMilliseconds()
	if minutes > (maxMilliseconds-baseMilliseconds)/60000 {
		return 0, false
	}
	milliseconds := minutes*60000 + baseMilliseconds
	return time.Duration(milliseconds) * time.Millisecond, true
}

func applyLyricOffset(timestamp, offset time.Duration) (time.Duration, bool) {
	if offset >= 0 {
		if timestamp > time.Duration(math.MaxInt64)-offset {
			return 0, false
		}
		return timestamp + offset, true
	}
	if timestamp < -offset {
		return 0, true
	}
	return timestamp + offset, true
}

func maxDurationMilliseconds() int64 {
	return math.MaxInt64 / int64(time.Millisecond)
}

func minDurationMilliseconds() int64 {
	return math.MinInt64 / int64(time.Millisecond)
}

type mobileLyricEnvelope struct {
	Status json.RawMessage `json:"status"`
	Data   struct {
		SongInfo struct {
			ID       json.RawMessage `json:"id"`
			MusicRID json.RawMessage `json:"musicrId"`
		} `json:"songinfo"`
		Lyrics []struct {
			Time json.RawMessage `json:"time"`
			Text json.RawMessage `json:"lineLyric"`
		} `json:"lrclist"`
	} `json:"data"`
}

func parseMobileLyrics(body []byte, requestedID string) (*platform.Lyrics, error) {
	requestedID = normalizeRID(requestedID)
	if requestedID == "" {
		return nil, unavailableLyricError(requestedID, errors.New("invalid track identity"))
	}
	var envelope mobileLyricEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, unavailableLyricError(requestedID, fmt.Errorf("decode mobile response: %w", err))
	}
	status, ok := rawScalarInt64(envelope.Status)
	if !ok || status != http.StatusOK {
		return nil, unavailableLyricError(requestedID, errors.New("mobile lyric status is not 200"))
	}
	if err := validateMobileLyricIdentity(requestedID, envelope.Data.SongInfo.ID, envelope.Data.SongInfo.MusicRID); err != nil {
		return nil, unavailableLyricError(requestedID, err)
	}

	lines := make([]platform.LyricLine, 0, len(envelope.Data.Lyrics))
	for _, item := range envelope.Data.Lyrics {
		timestamp, timeOK := parseMobileLyricTime(item.Time)
		text, textOK := rawStringOrNumber(item.Text)
		text = strings.TrimSpace(text)
		if !timeOK || !textOK || text == "" {
			continue
		}
		lines = append(lines, platform.LyricLine{Time: timestamp, Text: text})
	}
	if len(lines) == 0 {
		return nil, unavailableLyricError(requestedID, errors.New("mobile lyric response has no valid lines"))
	}
	sort.SliceStable(lines, func(left, right int) bool {
		return lines[left].Time < lines[right].Time
	})
	plain := make([]string, 0, len(lines))
	for _, line := range lines {
		plain = append(plain, line.Text)
	}
	return &platform.Lyrics{
		Plain:       strings.Join(plain, "\n"),
		Timestamped: lines,
	}, nil
}

func rawScalarInt64(raw json.RawMessage) (int64, bool) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return 0, false
	}
	var scalar jsonScalar
	if err := json.Unmarshal(raw, &scalar); err != nil {
		return 0, false
	}
	return scalar.Int64()
}

func validateMobileLyricIdentity(requested string, values ...json.RawMessage) error {
	found := false
	for _, raw := range values {
		trimmed := bytes.TrimSpace(raw)
		if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
			continue
		}
		var scalar jsonScalar
		if err := json.Unmarshal(trimmed, &scalar); err != nil {
			return errors.New("invalid mobile lyric identity")
		}
		text, ok := scalar.String()
		if !ok {
			return errors.New("invalid mobile lyric identity")
		}
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		found = true
		normalized := normalizeRID(text)
		if normalized == "" || normalized != requested {
			return errors.New("mobile lyric identity mismatch")
		}
	}
	if !found {
		return errors.New("mobile lyric identity missing")
	}
	return nil
}

func rawStringOrNumber(raw json.RawMessage) (string, bool) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return "", false
	}
	var scalar jsonScalar
	if err := json.Unmarshal(raw, &scalar); err != nil {
		return "", false
	}
	value, ok := scalar.value()
	if !ok {
		return "", false
	}
	switch value := value.(type) {
	case string:
		return value, true
	case json.Number:
		return value.String(), true
	default:
		return "", false
	}
}

func parseMobileLyricTime(raw json.RawMessage) (time.Duration, bool) {
	text, ok := rawStringOrNumber(raw)
	if !ok {
		return 0, false
	}
	seconds, err := strconv.ParseFloat(strings.TrimSpace(text), 64)
	if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < 0 {
		return 0, false
	}
	maxMilliseconds := maxDurationMilliseconds()
	if seconds > float64(maxMilliseconds)/1000 {
		return 0, false
	}
	roundedMilliseconds := math.Round(seconds * 1000)
	if math.IsNaN(roundedMilliseconds) || math.IsInf(roundedMilliseconds, 0) ||
		roundedMilliseconds < 0 || roundedMilliseconds > float64(maxMilliseconds) {
		return 0, false
	}
	milliseconds := int64(roundedMilliseconds)
	if milliseconds < 0 || milliseconds > maxMilliseconds {
		return 0, false
	}
	return time.Duration(milliseconds) * time.Millisecond, true
}

func (c *Client) GetLyrics(ctx context.Context, trackID string) (*platform.Lyrics, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	trackID = normalizeRID(trackID)
	if trackID == "" {
		return nil, platform.NewNotFoundError("kuwo", "track", trackID)
	}
	lyrics, err := c.getEnhancedLyrics(ctx, trackID)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	if err == nil && lyrics != nil && len(lyrics.Timestamped) > 0 {
		return lyrics, nil
	}
	if isTerminalLyricError(err) {
		return nil, err
	}
	lyrics, err = c.getMobileLyrics(ctx, trackID)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	if err != nil {
		return nil, err
	}
	return lyrics, nil
}

func (c *Client) getEnhancedLyrics(ctx context.Context, trackID string) (*platform.Lyrics, error) {
	endpoint := kuwoWordLyricURL
	if c != nil && strings.TrimSpace(c.endpoints.wordLyric) != "" {
		endpoint = c.endpoints.wordLyric
	}
	requestURL, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("kuwo: parse enhanced lyric URL: %w", err)
	}
	requestURL.RawQuery = buildWordLyricQuery(trackID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("kuwo: create enhanced lyric request: %w", err)
	}
	req.URL.RawQuery = requestURL.RawQuery
	req.Header.Set("User-Agent", kuwoUserAgent)
	resp, err := c.sessionlessAPIClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("kuwo: request enhanced lyrics: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, platform.NewRateLimitedError("kuwo")
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("kuwo: enhanced lyric endpoint returned HTTP %d", resp.StatusCode)
	}
	body, err := readLimited(resp.Body)
	if err != nil {
		return nil, err
	}
	raw, err := decodeWordLyrics(body)
	if err != nil {
		return nil, err
	}
	lyrics := parseTimedLyrics(raw)
	if lyrics == nil || len(lyrics.Timestamped) == 0 {
		return nil, errors.New("kuwo: enhanced lyric response has no valid timed lines")
	}
	return lyrics, nil
}

func (c *Client) getMobileLyrics(ctx context.Context, trackID string) (*platform.Lyrics, error) {
	endpoint := kuwoMobileLyricURL
	if c != nil && strings.TrimSpace(c.endpoints.mobileLyric) != "" {
		endpoint = c.endpoints.mobileLyric
	}
	requestURL, err := url.Parse(endpoint)
	if err != nil {
		return nil, unavailableLyricError(trackID, fmt.Errorf("parse mobile lyric URL: %w", err))
	}
	requestURL.RawQuery = url.Values{"musicId": []string{trackID}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return nil, unavailableLyricError(trackID, fmt.Errorf("create mobile lyric request: %w", err))
	}
	req.Header.Set("User-Agent", kuwoUserAgent)
	resp, err := c.sessionlessAPIClient().Do(req)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		return nil, unavailableLyricError(trackID, fmt.Errorf("request mobile lyrics: %w", err))
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, platform.NewRateLimitedError("kuwo")
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, unavailableLyricError(trackID, fmt.Errorf("mobile lyric endpoint returned HTTP %d", resp.StatusCode))
	}
	body, err := readLimited(resp.Body)
	if err != nil {
		return nil, unavailableLyricError(trackID, err)
	}
	return parseMobileLyrics(body, trackID)
}

func isTerminalLyricError(err error) bool {
	return errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, platform.ErrRateLimited)
}

func unavailableLyricError(trackID string, reason error) error {
	base := platform.NewUnavailableError("kuwo", "lyrics", trackID)
	if reason == nil {
		return base
	}
	return fmt.Errorf("%w: %w", base, reason)
}
