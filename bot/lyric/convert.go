package lyric

import "strings"

// Payload carries the raw lyric tracks a platform surfaced. Fields mirror the
// JSON keys consumed by LyricConverterService: the plain LRC, the translation
// and roma side-tracks, and any platform-native word-by-word raw track.
type Payload struct {
	// Lyric is the plain LRC (line-timed) lyric text.
	Lyric string
	// Translation is the translated LRC side-track (zh-Hans).
	Translation string
	// Roma is the romanization LRC side-track.
	Roma string

	// RawYRC / RawQRC / RawLYS hold a platform-native word-by-word raw track,
	// when available. Exactly one is typically set.
	RawYRC string
	RawQRC string
	RawLYS string

	// RawTTML is Apple Music's native word-timed TTML, when available. It is a
	// fully-formed target document and is returned verbatim for format "ttml".
	RawTTML string

	// Metadata used by document-style formats (lqe/ttml/amjson/elrc).
	MusicName  string
	Artist     string
	Album      string
	Source     string // platform name, e.g. "netease"/"tencent"
	NcmMusicID string
	QqMusicID  string
}

// Options controls optional side-track inclusion in formats that support it.
type Options struct {
	// IncludeTranslation merges the translation side-track. When unset for a
	// format, defaultsIncludeTranslation decides per-format.
	IncludeTranslation *bool
	// IncludeRoma merges the romanization side-track.
	IncludeRoma bool
	// RomaFirst emits roma before translation when both are present.
	RomaFirst bool
}

// pickLrcLikeLyric prefers an explicit plain LRC track. Mirrors pickLrcLikeLyric.
func (p Payload) pickLrcLikeLyric() string { return p.Lyric }

// pickTokenLyric returns the first available word-by-word raw track, or the
// plain lyric when it is itself token-shaped. Mirrors pickTokenLyric.
func (p Payload) pickTokenLyric() string {
	for _, t := range []string{p.RawYRC, p.RawQRC, p.RawLYS} {
		if hasTokenTrack(t) {
			return t
		}
	}
	// Apple Music only hands us TTML; derive a token track from its word spans
	// so non-TTML word formats (yrc/qrc/lys/spl/ass/elrc) still work.
	if strings.TrimSpace(p.RawTTML) != "" {
		if tok := ttmlToTokenTrack(p.RawTTML); strings.TrimSpace(tok) != "" {
			return tok
		}
	}
	if looksLikeTokenTrack(p.Lyric) {
		return p.Lyric
	}
	return ""
}

// NormalizeFormat resolves a user-facing format token to its canonical name,
// applying aliases. Empty/"auto" resolve to "lrc". Mirrors normalizeFormat.
func NormalizeFormat(format string) string {
	format = strings.ToLower(strings.TrimSpace(format))
	if format == "" || format == "auto" {
		return "lrc"
	}
	aliases := map[string]string{
		"origin":         "raw",
		"original":       "raw",
		"default":        "lrc",
		"enhancedlrc":    "elrc",
		"lrcx":           "elrc",
		"alrc":           "elrc",
		"applemusicjson": "amjson",
	}
	if mapped, ok := aliases[format]; ok {
		return mapped
	}
	return format
}

// defaultsIncludeTranslation reports whether a format merges the translation
// track by default when the caller did not specify. Mirrors lyric-convert.php.
func defaultsIncludeTranslation(resolved string) bool {
	switch resolved {
	case "spl", "ttml", "amjson", "ass", "lqe":
		return true
	}
	return false
}

// SupportedFormats is the ordered list of formats offered for switching.
var SupportedFormats = []string{
	"lrc", "yrc", "qrc", "lys", "krc", "elrc", "spl", "ass", "lqe", "ttml", "amjson", "srt", "txt", "trans", "roma",
}

// FileExtension returns the file extension (without dot) for a format.
func FileExtension(format string) string {
	switch NormalizeFormat(format) {
	case "amjson":
		return "json"
	case "ass":
		return "ass"
	case "srt":
		return "srt"
	case "txt", "trans":
		return "txt"
	case "ttml":
		return "ttml"
	case "lqe":
		return "lqe"
	case "lys":
		return "lys"
	case "qrc":
		return "qrc"
	case "yrc":
		return "yrc"
	case "krc":
		return "krc"
	case "elrc":
		return "lrc"
	default:
		return "lrc"
	}
}

// Convert renders the payload into the requested format. It mirrors
// LyricConverterService::convert: token-shaped raw tracks are preferred for
// word-by-word output, with graceful fallback to the plain LRC. For "krc",
// the returned string contains binary file bytes and must be written verbatim.
func Convert(p Payload, format string, opts Options) string {
	lyric := p.pickLrcLikeLyric()
	// pickTokenLyric parses the whole TTML document for Apple Music payloads.
	// Most formats never touch the result -- "ttml" passes RawTTML straight
	// through, and raw/txt/srt/trans/roma ignore it entirely -- while one render
	// calls Convert four or five times over the same payload. Resolve it on
	// first use instead of on every call.
	var (
		tokenLyricResolved bool
		tokenLyricValue    string
	)
	tokenLyric := func() string {
		if !tokenLyricResolved {
			tokenLyricValue = p.pickTokenLyric()
			tokenLyricResolved = true
		}
		return tokenLyricValue
	}
	tlyric := p.Translation
	roma := normalizeRomaLyric(pickRoma(p))
	resolved := NormalizeFormat(format)
	if resolved == "ttml" && strings.TrimSpace(p.RawTTML) != "" {
		return p.RawTTML
	}

	includeTranslation := defaultsIncludeTranslation(resolved)
	if opts.IncludeTranslation != nil {
		includeTranslation = *opts.IncludeTranslation
	}
	includeRoma := opts.IncludeRoma
	romaFirst := opts.RomaFirst

	derivedLRC := strings.TrimSpace(lyric) == "" || looksLikeTokenTrack(lyric)
	if resolved != "raw" && derivedLRC && tokenLyric() != "" {
		lyric = tokenToLRC(tokenLyric())
	}

	switch resolved {
	case "roma":
		return roma
	case "trans":
		return translationOnly(tlyric)
	case "raw":
		// "raw" is always the verbatim platform LRC, never merged.
		return lyric
	case "lrc":
		// Plain LRC merges the translation/roma side-tracks as extra timestamped
		// lines only when explicitly requested (single-track by default).
		tl := ""
		if includeTranslation {
			tl = tlyric
		}
		rm := ""
		if includeRoma {
			rm = roma
		}
		if tl == "" && rm == "" {
			return lyric
		}
		return mergeLrcTracks(lyric, tl, rm, romaFirst)
	case "txt":
		return lrcToTxt(lyric)
	case "srt":
		if derivedLRC && tokenLyric() != "" {
			return tokenToSrt(tokenLyric())
		}
		return lrcToSrt(lyric)
	}

	if resolved == "yrc" {
		if hasTokenTrack(p.RawYRC) {
			return p.RawYRC
		}
		if tokenLyric() != "" {
			return tokenToYRC(tokenLyric())
		}
		return lyric
	}
	if resolved == "qrc" {
		if hasTokenTrack(p.RawQRC) {
			return p.RawQRC
		}
		if tokenLyric() != "" {
			return tokenToQRC(tokenLyric())
		}
		return lyric
	}
	if resolved == "krc" {
		return encodeKRC(tokenToKRC(tokenLyric(), lyric, p, ternary(includeTranslation, tlyric, ""), ternary(includeRoma, roma, "")))
	}
	if resolved == "lys" {
		if tokenLyric() != "" {
			return tokenToLysDocument(tokenLyric(), p, lyric, tlyric, roma)
		}
		return lyric
	}

	if resolved == "elrc" {
		if tokenLyric() != "" {
			if converted := tokenToElrc(tokenLyric(), p, lyric, tlyric, roma); converted != "" {
				return converted
			}
		}
		return lyric
	}

	romaTrack := ""
	if includeRoma {
		romaTrack = roma
	}

	switch resolved {
	case "spl":
		if tokenLyric() != "" {
			if includeTranslation {
				return tokenToSpl(tokenLyric(), tlyric, romaTrack, romaFirst)
			}
			return tokenToSpl(tokenLyric(), "", romaTrack, romaFirst)
		}
		if includeTranslation {
			return lrcToSpl(lyric, tlyric, romaTrack, romaFirst)
		}
		return lrcToSpl(lyric, "", romaTrack, romaFirst)
	case "ass":
		if tokenLyric() != "" {
			if includeTranslation {
				return tokenToAss(tokenLyric(), tlyric, romaTrack, romaFirst)
			}
			return tokenToAss(tokenLyric(), "", romaTrack, romaFirst)
		}
		if includeTranslation {
			return lrcToAss(lyric, tlyric, romaTrack, romaFirst)
		}
		return lrcToAss(lyric, "", romaTrack, romaFirst)
	case "lqe":
		return lrcToLqe(lyric, ternary(includeTranslation, tlyric, ""), romaTrack, p, tokenLyric(), romaFirst)
	case "ttml":
		if tokenLyric() != "" {
			return tokenToTTML(tokenLyric(), ternary(includeTranslation, tlyric, ""), p, romaTrack, true, romaFirst)
		}
		return lrcToTTML(lyric, ternary(includeTranslation, tlyric, ""), p, romaTrack, true, romaFirst)
	case "amjson":
		if tokenLyric() != "" {
			return ttmlToAppleMusicJSON(tokenToTTML(tokenLyric(), ternary(includeTranslation, tlyric, ""), p, romaTrack, false, romaFirst))
		}
		return ttmlToAppleMusicJSON(lrcToTTML(lyric, ternary(includeTranslation, tlyric, ""), p, romaTrack, false, romaFirst))
	}

	return lyric
}

func pickRoma(p Payload) string { return p.Roma }

// HasWordTiming reports whether the payload actually carries word-by-word
// ("逐词") timing, as opposed to only line-level lyrics. It is used to decide
// whether a "逐词"-labeled format truly renders word-by-word for this song or
// silently falls back to line-level output.
//
// Platform-native raw tracks (yrc/qrc/lys) count whenever they parse to at
// least one timed token. Apple Music TTML counts only when it contains primary
// word spans — a line-level TTML document does not. A token-shaped plain LRC is
// also honored for the rare platforms that inline word tags there.
func HasWordTiming(p Payload) bool {
	for _, t := range []string{p.RawYRC, p.RawQRC, p.RawLYS} {
		if hasTimedWords(t) {
			return true
		}
	}
	if strings.TrimSpace(p.RawTTML) != "" && ttmlHasWordSpans(p.RawTTML) {
		return true
	}
	if looksLikeTokenTrack(p.Lyric) {
		return hasTimedWords(p.Lyric)
	}
	return false
}

// A line header alone can be parsed for fallback rendering, but it must not
// be advertised as word timing: parseTokenLines synthesizes a token for it.
func hasTimedWords(track string) bool {
	for _, row := range splitLines(track) {
		if !wordTagRe.MatchString(row) {
			continue
		}
		for _, line := range parseTokenLines(row) {
			for _, word := range line.Tokens {
				if word.End > word.Start && strings.TrimSpace(word.Text) != "" {
					return true
				}
			}
		}
	}
	return false
}

// hasTokenTrack reports whether a yrc/qrc/lys token track parses to at least one
// line carrying word tokens.
func hasTokenTrack(token string) bool {
	if strings.TrimSpace(token) == "" {
		return false
	}
	for _, line := range parseTokenLines(token) {
		if len(line.Tokens) > 0 {
			return true
		}
	}
	return false
}

func ternary(cond bool, a, b string) string {
	if cond {
		return a
	}
	return b
}
