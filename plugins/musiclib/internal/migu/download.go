// Source adapted from github.com/guohuiyuan/music-lib (migu/download.go on main).
// Licensed under GNU AGPL v3.0; see the bundled upstream license.

package migu

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/liuran001/MusicBot-Go/plugins/musiclib/internal/model"
)

const (
	miguListenURL      = "https://c.musicapp.migu.cn/MIGUM2.0/v2.1/content/listen-url"
	miguAndroidUA      = "Android_migu/7.41.13 okhttp/3.12.13"
	miguAndroidChannel = "0146832"
	miguAndroidVersion = "7.41.13"
)

var miguToneExtensions = map[string]string{
	"LQ":   "mp3",
	"PQ":   "mp3",
	"HQ":   "mp3",
	"SQ":   "flac",
	"ZQ":   "flac",
	"ZQ24": "flac",
	"ZQ32": "wav",
}

var miguTonePaths = map[string]string{
	"LQ":   "全曲试听/Mp3_64_22_16",
	"PQ":   "标清高清/MP3_128_16_Stero",
	"HQ":   "标清高清/MP3_320_16_Stero",
	"SQ":   "歌曲下载/flac",
	"ZQ":   "歌曲下载/flac_24bit",
	"ZQ24": "歌曲下载/flac_24bit",
	"ZQ32": "歌曲下载/wav_32bit",
}

// miguTonePreference orders download candidates from highest to lowest
// fidelity. Encrypted 3D renditions (Z3D/I3D/3D60) are excluded: the delivery
// pipeline consumes plain audio URLs and cannot decrypt them.
var miguTonePreference = []string{"ZQ32", "ZQ24", "ZQ", "SQ", "HQ", "PQ", "LQ"}

type miguDownloadCandidate struct {
	url    string
	format string
	ext    string
}

// GetDownloadURL 获取下载链接
func (m *Migu) GetDownloadURL(s *model.Song) (string, error) {
	if s == nil {
		return "", errors.New("song is nil")
	}
	if s.Source != "migu" {
		return "", errors.New("source mismatch")
	}
	if s.URL != "" {
		if model.ExplicitPreviewURL(s.URL) {
			return "", fmt.Errorf("%w: migu media URL is marked as an audition", model.ErrPreviewOnly)
		}
		return s.URL, nil
	}

	contentID, resourceType, formatType := miguSongParts(s)
	if contentID == "" {
		return "", errors.New("invalid id structure and missing extra data")
	}
	if resourceType == "" {
		resourceType = "2"
	}

	targetFormat := normalizeMiguTone(formatType)
	if targetFormat == "" {
		targetFormat = normalizeMiguTone(s.Ext)
	}
	if targetFormat == "" {
		targetFormat = "PQ"
	}

	var copyrightID, songID string
	albumID := strings.TrimSpace(s.AlbumID)
	if s.Extra != nil {
		copyrightID = strings.TrimSpace(s.Extra["copyright_id"])
		songID = strings.TrimSpace(s.Extra["song_id"])
		if extraAlbumID := strings.TrimSpace(s.Extra["album_id"]); extraAlbumID != "" {
			albumID = extraAlbumID
		}
	}

	// The endpoint reports the rendition the session may actually play: tones
	// above the entitlement come back with an empty URL and a dialog hint.
	requestTones := []string{"ZQ32", targetFormat, "PQ"}
	responses := make([]*miguListenResponse, 0, len(requestTones))
	seenTones := make(map[string]struct{}, len(requestTones))
	var fetchErr error
	for _, tone := range requestTones {
		if _, ok := seenTones[tone]; ok {
			continue
		}
		seenTones[tone] = struct{}{}
		resp, err := m.fetchListenInfo(contentID, copyrightID, songID, albumID, resourceType, tone)
		if err == nil {
			responses = append(responses, resp)
		} else if fetchErr == nil {
			fetchErr = err
		}
	}

	candidates := buildMiguDownloadCandidates(responses)
	auditionSkipped := false
	for _, candidate := range candidates {
		if model.ExplicitPreviewURL(candidate.url) {
			auditionSkipped = true
			continue
		}
		if !m.downloadInfoValid(candidate) {
			continue
		}
		s.Ext = candidate.ext
		s.Bitrate = miguToneBitrate(candidate.format)
		return candidate.url, nil
	}

	if len(candidates) == 0 {
		for _, resp := range responses {
			if message := miguListenResponseMessage(resp); message != "empty download url" {
				return "", errors.New(message)
			}
		}
		if fetchErr != nil {
			return "", fetchErr
		}
		return "", errors.New("migu returned no download quality")
	}
	if auditionSkipped {
		return "", fmt.Errorf("%w: migu returned only audition media", model.ErrPreviewOnly)
	}
	if fetchErr != nil {
		return "", fmt.Errorf("migu returned no accessible download quality: %w", fetchErr)
	}
	return "", errors.New("migu returned no accessible download quality")
}

func miguToneBitrate(format string) int {
	switch format {
	case "LQ":
		return 64
	case "PQ":
		return 128
	case "HQ":
		return 320
	}
	return 0
}

func miguSongParts(s *model.Song) (contentID, resourceType, formatType string) {
	if s == nil {
		return "", "", ""
	}
	if s.Extra != nil {
		contentID = strings.TrimSpace(s.Extra["content_id"])
		resourceType = strings.TrimSpace(s.Extra["resource_type"])
		formatType = strings.TrimSpace(s.Extra["format_type"])
	}
	if contentID != "" && resourceType != "" && formatType != "" {
		return contentID, resourceType, formatType
	}
	parts := strings.Split(s.ID, "|")
	if len(parts) == 3 {
		return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), strings.TrimSpace(parts[2])
	}
	if contentID == "" && len(parts) == 1 {
		contentID = strings.TrimSpace(s.ID)
	}
	return contentID, resourceType, formatType
}

type miguListenResponse struct {
	Code string `json:"code"`
	Info string `json:"info"`
	Data struct {
		URL        string `json:"url"`
		FormatType string `json:"formatType"`
		DialogInfo struct {
			Text string `json:"text"`
		} `json:"dialogInfo"`
	} `json:"data"`
}

func (m *Migu) fetchListenInfo(contentID, copyrightID, songID, albumID, resourceType, toneFlag string) (*miguListenResponse, error) {
	params := url.Values{}
	params.Set("netType", "01")
	params.Set("resourceType", firstNonEmpty(resourceType, "2"))
	params.Set("contentId", strings.TrimSpace(contentID))
	params.Set("toneFlag", firstNonEmpty(toneFlag, "PQ"))
	if copyrightID != "" {
		params.Set("copyrightId", copyrightID)
	}
	if songID != "" {
		params.Set("songId", songID)
	}
	if albumID != "" {
		params.Set("albumId", albumID)
	}

	req, err := http.NewRequestWithContext(m.ctx, http.MethodGet, miguListenURL+"?"+params.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", miguAndroidUA)
	req.Header.Set("channel", miguAndroidChannel)
	req.Header.Set("version", miguAndroidVersion)
	req.Header.Set("Referer", "https://music.migu.cn/")
	if cookie := strings.TrimSpace(m.cookie); cookie != "" {
		req.Header.Set("Cookie", cookie)
	}

	resp, err := m.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("migu listen endpoint returned status %d", resp.StatusCode)
	}

	var result miguListenResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("migu listen response json parse error: %w", err)
	}
	if result.Code != "" && result.Code != "000000" {
		return nil, fmt.Errorf("migu api error: %s (code %s)", result.Info, result.Code)
	}
	return &result, nil
}

// buildMiguDownloadCandidates expands the served URLs into every preferred
// tone. The CDN stores sibling renditions in mirrored directories, so the
// tone path of a served URL can be rewritten and probed.
func buildMiguDownloadCandidates(responses []*miguListenResponse) []miguDownloadCandidate {
	type directSource struct {
		url    string
		format string
	}
	sources := make([]directSource, 0, len(responses))
	seenSources := make(map[string]struct{}, len(responses))
	for _, resp := range responses {
		if resp == nil {
			continue
		}
		sourceURL := normalizeMiguDownloadURL(resp.Data.URL)
		if sourceURL == "" || isEncryptedMiguDirectURL(sourceURL) {
			continue
		}
		format := firstNonEmpty(normalizeMiguTone(resp.Data.FormatType), "PQ")
		key := format + "\x00" + sourceURL
		if _, ok := seenSources[key]; ok {
			continue
		}
		seenSources[key] = struct{}{}
		sources = append(sources, directSource{url: sourceURL, format: format})
	}

	candidates := make([]miguDownloadCandidate, 0, len(miguTonePreference))
	seen := make(map[string]struct{})
	for _, tone := range miguTonePreference {
		for _, source := range sources {
			candidateURL := source.url
			if tone != source.format {
				rewrittenURL, ok := rewriteMiguToneURL(source.url, tone)
				if !ok {
					continue
				}
				candidateURL = rewrittenURL
			}
			candidate, ok := newMiguDownloadCandidate(candidateURL, tone)
			if !ok {
				continue
			}
			key := candidate.format + "\x00" + candidate.url
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			candidates = append(candidates, candidate)
		}
	}
	return candidates
}

func newMiguDownloadCandidate(rawURL, format string) (miguDownloadCandidate, bool) {
	rawURL = normalizeMiguDownloadURL(rawURL)
	if rawURL == "" {
		return miguDownloadCandidate{}, false
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return miguDownloadCandidate{}, false
	}
	ext := strings.TrimPrefix(strings.ToLower(path.Ext(parsed.Path)), ".")
	if ext == "" {
		ext = miguToneExtensions[format]
	}
	if ext == "" {
		ext = "mp3"
	}
	if format == "" {
		format = formatFromMiguExt(ext)
	}
	return miguDownloadCandidate{url: rawURL, format: format, ext: ext}, true
}

func rewriteMiguToneURL(rawURL, targetFormat string) (string, bool) {
	targetPath := miguTonePaths[targetFormat]
	targetExt := miguToneExtensions[targetFormat]
	if targetPath == "" || targetExt == "" {
		return "", false
	}
	parsed, err := url.Parse(normalizeMiguDownloadURL(rawURL))
	if err != nil {
		return "", false
	}
	sourcePath := detectMiguTonePath(parsed.Path)
	if sourcePath == "" || sourcePath == targetPath {
		return "", false
	}
	newPath := strings.Replace(parsed.Path, sourcePath, targetPath, 1)
	if oldExt := path.Ext(newPath); oldExt != "" {
		newPath = strings.TrimSuffix(newPath, oldExt)
	}
	newPath += "." + targetExt
	parsed.Path = newPath
	parsed.RawPath = ""
	return parsed.String(), true
}

func detectMiguTonePath(decodedPath string) string {
	for _, format := range []string{"ZQ24", "ZQ", "ZQ32", "SQ", "HQ", "LQ", "PQ"} {
		if tonePath := miguTonePaths[format]; tonePath != "" && strings.Contains(decodedPath, tonePath) {
			return tonePath
		}
	}
	return ""
}

func normalizeMiguDownloadURL(rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	if strings.HasPrefix(rawURL, "//") {
		return "https:" + rawURL
	}
	if strings.HasPrefix(rawURL, "ftp://218.200.160.122:21/") {
		return "https://freetyst.nf.migu.cn/" + strings.TrimPrefix(rawURL, "ftp://218.200.160.122:21/")
	}
	return rawURL
}

func isEncryptedMiguDirectURL(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	unescapedPath, err := url.PathUnescape(parsed.EscapedPath())
	if err != nil {
		unescapedPath = parsed.Path
	}
	return strings.Contains(unescapedPath, "wav_3d/") && !strings.Contains(unescapedPath, "wav_3d_60s/")
}

func miguListenResponseMessage(resp *miguListenResponse) string {
	if resp == nil {
		return "empty download url"
	}
	if message := strings.TrimSpace(resp.Data.DialogInfo.Text); message != "" {
		return message
	}
	if message := strings.TrimSpace(resp.Info); message != "" {
		return message
	}
	return "empty download url"
}

func normalizeMiguTone(value string) string {
	value = strings.ToUpper(strings.TrimSpace(strings.TrimPrefix(value, ".")))
	if _, ok := miguToneExtensions[value]; ok {
		return value
	}
	return ""
}

func formatFromMiguExt(ext string) string {
	switch strings.ToLower(strings.TrimSpace(ext)) {
	case "flac":
		return "SQ"
	case "wav":
		return "ZQ32"
	default:
		return "PQ"
	}
}

// downloadInfoValid probes the first bytes of a candidate. The CDN only
// serves renditions the session may fetch, so the audio magic decides.
func (m *Migu) downloadInfoValid(candidate miguDownloadCandidate) bool {
	req, err := http.NewRequestWithContext(m.ctx, http.MethodGet, candidate.url, nil)
	if err != nil {
		return false
	}
	req.Header.Set("User-Agent", miguAndroidUA)
	req.Header.Set("Referer", "https://music.migu.cn/")
	req.Header.Set("Range", "bytes=0-63")
	if cookie := strings.TrimSpace(m.cookie); cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return false
	}
	prefix, err := io.ReadAll(io.LimitReader(resp.Body, 64))
	if err != nil {
		return false
	}
	return isMiguAudioMagic(prefix)
}

func isMiguAudioMagic(data []byte) bool {
	if len(data) < 4 {
		return false
	}
	header := data[:4]
	return bytes.Equal(header, []byte("fLaC")) ||
		(bytes.Equal(header, []byte("RIFF")) && len(data) >= 12 && bytes.Equal(data[8:12], []byte("WAVE"))) ||
		bytes.Equal(header, []byte("OggS")) ||
		bytes.Equal(data[:3], []byte("ID3")) ||
		(len(data) >= 12 && bytes.Equal(data[4:8], []byte("ftyp"))) ||
		(data[0] == 0xFF && data[1]&0xE0 == 0xE0)
}
