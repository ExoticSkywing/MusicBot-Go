package applemusic

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/liuran001/MusicBot-Go/bot/platform"
)

// enhancedHLSVariant is one selectable stream from the enhancedHls master
// playlist (e.g. ALAC lossless, Dolby Atmos, or an AAC tier).
type enhancedHLSVariant struct {
	Codecs    string // e.g. "alac", "ec-3", "mp4a.40.2"
	AudioCh   string // AUDIO group id, e.g. "audio-alac-stereo-44100-24"
	Channels  string // CHANNELS attr, e.g. "2" or "16/JOC"
	Bandwidth int    // BANDWIDTH attr
	AvgBW     int    // AVERAGE-BANDWIDTH attr
	URI       string // media playlist URI (relative to master)

	// Parsed from the matching EXT-X-MEDIA entry, with the AUDIO group id used
	// as a compatibility fallback for older manifests.
	SampleRate int // e.g. 44100, 48000, 96000, 192000
	BitDepth   int // e.g. 16 or 24
}

// kind classifies a variant into a coarse audio family.
func (v enhancedHLSVariant) isALAC() bool {
	return strings.EqualFold(strings.TrimSpace(v.Codecs), "alac")
}
func (v enhancedHLSVariant) isDolbyAudio() bool {
	codec := strings.ToLower(strings.TrimSpace(v.Codecs))
	return codec == "ec-3" || codec == "ec+3" || strings.Contains(codec, "joc")
}
func (v enhancedHLSVariant) isAtmos() bool {
	codec := strings.ToLower(strings.TrimSpace(v.Codecs))
	channels := strings.ToLower(strings.TrimSpace(v.Channels))
	group := strings.ToLower(strings.TrimSpace(v.AudioCh))
	if !v.isDolbyAudio() {
		return false
	}
	return codec == "ec+3" || strings.Contains(codec, "joc") ||
		strings.Contains(channels, "joc") || strings.Contains(group, "atmos") ||
		strings.Contains(group, "joc")
}
func (v enhancedHLSVariant) isAAC() bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(v.Codecs)), "mp4a.40")
}

var (
	reStreamInf = regexp.MustCompile(`^#EXT-X-STREAM-INF:(.*)$`)
)

type enhancedHLSAudioGroup struct {
	SampleRate int
	BitDepth   int
	Channels   string
}

// parseEnhancedHLSMaster parses the enhancedHls master playlist and returns all
// audio stream variants, sorted by average bandwidth descending (best first).
func parseEnhancedHLSMaster(content string) ([]enhancedHLSVariant, error) {
	var variants []enhancedHLSVariant
	lines := strings.Split(content, "\n")
	audioGroups := parseEnhancedHLSAudioGroups(lines)
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		m := reStreamInf.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		attrs := parseHLSAttributeList(m[1])
		v := enhancedHLSVariant{
			Codecs:  attrs["CODECS"],
			AudioCh: attrs["AUDIO"],
		}
		v.Bandwidth, _ = strconv.Atoi(attrs["BANDWIDTH"])
		v.AvgBW, _ = strconv.Atoi(attrs["AVERAGE-BANDWIDTH"])
		// The URI is on the next non-comment, non-empty line.
		for j := i + 1; j < len(lines); j++ {
			u := strings.TrimSpace(lines[j])
			if u == "" || strings.HasPrefix(u, "#") {
				continue
			}
			v.URI = u
			i = j
			break
		}
		v.SampleRate, v.BitDepth = parseALACGroupDetails(v.AudioCh)
		v.Channels = parseAudioGroupChannels(v.AudioCh)
		if group, ok := audioGroups[v.AudioCh]; ok {
			if group.SampleRate > 0 {
				v.SampleRate = group.SampleRate
			}
			if group.BitDepth > 0 {
				v.BitDepth = group.BitDepth
			}
			if group.Channels != "" {
				v.Channels = group.Channels
			}
		}
		if v.URI != "" {
			variants = append(variants, v)
		}
	}
	if len(variants) == 0 {
		return nil, fmt.Errorf("no stream variants in enhancedHls master")
	}
	sort.SliceStable(variants, func(i, j int) bool {
		return variants[i].AvgBW > variants[j].AvgBW
	})
	return variants, nil
}

// parseEnhancedHLSAudioGroups indexes technical attributes declared on
// EXT-X-MEDIA. These attributes are authoritative; group-id parsing below is
// retained only for manifests that omit them.
func parseEnhancedHLSAudioGroups(lines []string) map[string]enhancedHLSAudioGroup {
	groups := make(map[string]enhancedHLSAudioGroup)
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if !strings.HasPrefix(line, "#EXT-X-MEDIA:") {
			continue
		}
		attrs := parseHLSAttributeList(strings.TrimPrefix(line, "#EXT-X-MEDIA:"))
		if !strings.EqualFold(attrs["TYPE"], "AUDIO") {
			continue
		}
		groupID := strings.TrimSpace(attrs["GROUP-ID"])
		if groupID == "" {
			continue
		}
		group := groups[groupID]
		if sampleRate, err := strconv.Atoi(attrs["SAMPLE-RATE"]); err == nil && sampleRate > 0 {
			group.SampleRate = sampleRate
		}
		if bitDepth, err := strconv.Atoi(attrs["BIT-DEPTH"]); err == nil && bitDepth > 0 {
			group.BitDepth = bitDepth
		}
		if channels := strings.TrimSpace(attrs["CHANNELS"]); channels != "" {
			group.Channels = channels
		}
		groups[groupID] = group
	}
	return groups
}

// parseHLSAttributeList splits an HLS attribute list without breaking commas
// inside quoted values (notably CODECS). Returned values have surrounding
// quotes removed.
func parseHLSAttributeList(raw string) map[string]string {
	attrs := make(map[string]string)
	start := 0
	inQuotes := false
	add := func(part string) {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			return
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
			value = value[1 : len(value)-1]
		}
		if key != "" {
			attrs[key] = value
		}
	}
	for i := 0; i < len(raw); i++ {
		switch raw[i] {
		case '"':
			inQuotes = !inQuotes
		case ',':
			if !inQuotes {
				add(raw[start:i])
				start = i + 1
			}
		}
	}
	add(raw[start:])
	return attrs
}

// parseALACGroupDetails extracts sample rate and bit depth from an ALAC audio
// group id like "audio-alac-stereo-44100-24" (rate=44100, depth=24). Returns
// zeros if the pattern doesn't match.
func parseALACGroupDetails(group string) (sampleRate, bitDepth int) {
	if !strings.Contains(strings.ToLower(group), "alac") {
		return 0, 0
	}
	parts := strings.Split(group, "-")
	if len(parts) < 2 {
		return 0, 0
	}
	// Last two numeric-looking segments are <sampleRate>-<bitDepth>.
	depth, err1 := strconv.Atoi(parts[len(parts)-1])
	rate, err2 := strconv.Atoi(parts[len(parts)-2])
	if err1 != nil || err2 != nil {
		return 0, 0
	}
	return rate, depth
}

func parseAudioGroupChannels(group string) string {
	for _, part := range strings.Split(strings.ToLower(group), "-") {
		switch part {
		case "mono":
			return "1"
		case "stereo":
			return "2"
		}
	}
	return ""
}

// selectVariantForQuality picks the best stream variant for a requested quality.
// Enhanced tiers are strict so a caller never receives a differently labelled
// format; Standard and High select their matching AAC rendition.
//
// Mapping:
//   - QualityAtmos   -> highest-rate Dolby Atmos (EC-3/JOC), no tier fallback
//   - QualityHiRes   -> highest-rate ALAC above 48kHz, no tier fallback
//   - QualityLossless-> highest-rate ALAC at or below 48kHz, no tier fallback
//   - QualityHigh    -> AAC ~256k
//   - QualityStandard-> AAC ~128k (or lowest AAC)
func selectVariantForQuality(variants []enhancedHLSVariant, quality platform.Quality) (enhancedHLSVariant, bool) {
	switch quality {
	case platform.QualityAtmos:
		return bestAtmos(variants)
	case platform.QualityHiRes:
		return bestALAC(variants, true)
	case platform.QualityLossless:
		return bestALAC(variants, false)
	case platform.QualityHigh:
		return bestAAC(variants, 256000)
	default: // QualityStandard
		return bestAAC(variants, 128000)
	}
}

func bestALAC(variants []enhancedHLSVariant, hiResOnly bool) (enhancedHLSVariant, bool) {
	var best enhancedHLSVariant
	found := false
	for _, v := range variants {
		if !v.isALAC() {
			continue
		}
		if v.SampleRate <= 0 {
			continue
		}
		if hiResOnly && v.SampleRate <= 48000 {
			continue
		}
		if !hiResOnly && v.SampleRate > 48000 {
			continue
		}
		// Prefer resolution first; AvgBW breaks ties deterministically.
		if !found || v.SampleRate > best.SampleRate ||
			(v.SampleRate == best.SampleRate && v.BitDepth > best.BitDepth) ||
			(v.SampleRate == best.SampleRate && v.BitDepth == best.BitDepth && v.AvgBW > best.AvgBW) {
			best = v
			found = true
		}
	}
	return best, found
}

func bestAtmos(variants []enhancedHLSVariant) (enhancedHLSVariant, bool) {
	for _, v := range variants { // sorted desc -> first declared Atmos stream is highest bitrate
		if v.isAtmos() {
			return v, true
		}
	}
	return enhancedHLSVariant{}, false
}

// bestAAC returns the AAC variant whose bandwidth is closest to (but not far
// above) the target; falls back to the highest available AAC.
func bestAAC(variants []enhancedHLSVariant, targetBW int) (enhancedHLSVariant, bool) {
	var best enhancedHLSVariant
	found := false
	bestDiff := 1 << 30
	for _, v := range variants {
		if !v.isAAC() {
			continue
		}
		diff := v.AvgBW - targetBW
		if diff < 0 {
			diff = -diff
		}
		if !found || diff < bestDiff {
			best = v
			bestDiff = diff
			found = true
		}
	}
	return best, found
}

// enhancedHLSMedia is the parsed media (sub) playlist for one variant: the
// single byte-range mp4 URL and the ordered FairPlay key URI for each segment
// (fragment). The wrapper expects one key per fragment, in order.
type enhancedHLSMedia struct {
	MP4URL  string   // absolute URL of the single mp4 file
	SegKeys []string // FairPlay skd:// key URI per segment, in playlist order
}

var reMediaKey = regexp.MustCompile(`#EXT-X-KEY:[^\n]*URI="(skd://[^"]+)"`)

var reMapURI = regexp.MustCompile(`URI="([^"]+)"`)

// parseEnhancedHLSMedia parses a variant's media playlist. EXT-X-KEY lines set
// the "current" FairPlay key; each subsequent segment (a non-comment line, or
// an EXT-X-MAP init reference) inherits the most recent key. We record the key
// in effect for each fragment so the wrapper handshake can be driven per
// fragment (matching runv2's playlistSegments[i].Key).
func parseEnhancedHLSMedia(mediaURL, content string) (enhancedHLSMedia, error) {
	var m enhancedHLSMedia
	baseURL := mediaURL[:strings.LastIndex(mediaURL, "/")+1]

	currentKey := ""
	mp4Name := ""
	lines := strings.Split(content, "\n")
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#EXT-X-KEY") {
			if km := reMediaKey.FindStringSubmatch(line); km != nil {
				currentKey = km[1]
			}
			continue
		}
		if strings.HasPrefix(line, "#EXT-X-MAP") {
			// init segment; capture the mp4 file name from its URI.
			if im := reMapURI.FindStringSubmatch(line); im != nil {
				mp4Name = im[1]
			}
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		// A media segment line: it references the mp4 file (byte-range mode).
		if mp4Name == "" {
			mp4Name = line
		}
		m.SegKeys = append(m.SegKeys, currentKey)
	}

	if mp4Name == "" {
		return m, fmt.Errorf("no mp4 segment in media playlist")
	}
	if strings.HasPrefix(mp4Name, "http") {
		m.MP4URL = mp4Name
	} else {
		m.MP4URL = baseURL + mp4Name
	}
	if len(m.SegKeys) == 0 {
		return m, fmt.Errorf("no segments in media playlist")
	}
	return m, nil
}
