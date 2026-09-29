package lyric

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"encoding/json"
	"math"
	"regexp"
	"strings"
)

// krcLineRe matches a KRC lyric line "[lineStartMs,lineDurMs]rest".
var krcLineRe = regexp.MustCompile(`^\[(\d+),(\d+)\](.*)$`)

// krcWordRe matches a KRC word tag "<relStartMs,durMs,flag>".
var krcWordRe = regexp.MustCompile(`<(\d+),(\d+),(\d+)>`)

// krcLangTagRe matches the "[language:BASE64]" header carrying translation/roma.
var krcLangTagRe = regexp.MustCompile(`^\[language:(.*)\]$`)

// KRCResult holds the tracks extracted from a decrypted KRC document.
type KRCResult struct {
	// RawQRC is the KRC body re-encoded as a QRC-style token track (absolute
	// word timings), suitable for the converter's word-by-word pipeline.
	RawQRC string
	// Lyric is the line-timed LRC derived from the KRC body.
	Lyric string
	// Translation is the LRC translation track, if the KRC embedded one.
	Translation string
	// Roma is the LRC romanization track, if the KRC embedded one.
	Roma string
}

// ParseKRC converts decrypted KRC text into canonical tracks. KRC word tags are
// RELATIVE to their line start; this resolves them to absolute milliseconds and
// re-emits QRC-style tokens. Embedded "[language:...]" translation (type 1) and
// romanization (type 0) tracks are decoded and aligned by line index.
func ParseKRC(krc string) KRCResult {
	rows := splitLines(krc)

	var transContent [][]string // type 1: one string per line
	var romaContent [][]string  // type 0: per-word strings per line
	for _, row := range rows {
		line := strings.TrimSpace(row)
		m := krcLangTagRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		t, r := decodeKRCLanguages(m[1])
		if t != nil {
			transContent = t
		}
		if r != nil {
			romaContent = r
		}
	}

	var qrcLines []string
	var lrcLines []string
	var transLines []string
	var romaLines []string
	lineIdx := 0

	for _, row := range rows {
		line := strings.TrimSpace(row)
		if line == "" {
			continue
		}
		m := krcLineRe.FindStringSubmatch(line)
		if m == nil {
			continue // header tag ([ti:]/[ar:]/[language:]/...)
		}
		lineStart := mustAtoi(m[1])
		lineDur := mustAtoi(m[2])
		content := m[3]

		locs := krcWordRe.FindAllStringSubmatchIndex(content, -1)
		var qrcBody strings.Builder
		var plain strings.Builder
		if len(locs) == 0 {
			text := strings.TrimSpace(content)
			qrcBody.WriteString(text)
			plain.WriteString(text)
		} else {
			prev := 0
			// Text for word i is between tag i's end and tag i+1's start.
			for i, loc := range locs {
				relStart := mustAtoi(content[loc[2]:loc[3]])
				dur := mustAtoi(content[loc[4]:loc[5]])
				textStart := loc[1]
				textEnd := len(content)
				if i+1 < len(locs) {
					textEnd = locs[i+1][0]
				}
				_ = prev
				text := content[textStart:textEnd]
				abs := lineStart + relStart
				if text != "" {
					qrcBody.WriteString(text + "(" + itoa(abs) + "," + itoa(dur) + ")")
					plain.WriteString(text)
				}
			}
		}

		qrcLine := "[" + itoa(lineStart) + "," + itoa(lineDur) + "]" + qrcBody.String()
		qrcLines = append(qrcLines, qrcLine)
		lrcLines = append(lrcLines, formatLRCTagFromMs(lineStart, 2)+strings.TrimSpace(plain.String()))

		if lineIdx < len(transContent) {
			if t := joinKRCWords(transContent[lineIdx]); t != "" {
				transLines = append(transLines, formatLRCTagFromMs(lineStart, 2)+t)
			}
		}
		if lineIdx < len(romaContent) {
			if r := joinKRCWords(romaContent[lineIdx]); r != "" {
				romaLines = append(romaLines, formatLRCTagFromMs(lineStart, 2)+r)
			}
		}
		lineIdx++
	}

	return KRCResult{
		RawQRC:      strings.Join(qrcLines, "\n"),
		Lyric:       strings.Join(lrcLines, "\n"),
		Translation: strings.Join(transLines, "\n"),
		Roma:        strings.Join(romaLines, "\n"),
	}
}

func joinKRCWords(words []string) string {
	return strings.TrimSpace(strings.Join(words, ""))
}

// decodeKRCLanguages decodes the base64 JSON of a "[language:...]" tag, returning
// the translation track (type 1, one line per entry) and roma track (type 0,
// per-word entries). Either may be nil if absent.
func decodeKRCLanguages(b64 string) (translation, roma [][]string) {
	b64 = strings.TrimSpace(b64)
	if b64 == "" {
		return nil, nil
	}
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, nil
	}
	var payload struct {
		Content []struct {
			Type         int        `json:"type"`
			LyricContent [][]string `json:"lyricContent"`
		} `json:"content"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, nil
	}
	for _, c := range payload.Content {
		switch c.Type {
		case 1:
			translation = c.LyricContent
		case 0:
			roma = c.LyricContent
		}
	}
	return translation, roma
}

// tokenToKRC emits decoded KRC text. Word offsets are relative to the line,
// unlike QRC/YRC. Line-only input is represented by a single timed segment.
func tokenToKRC(token, lrc string, p Payload, translation, roma string) string {
	lines := parseTokenLines(token)
	if len(lines) == 0 {
		for _, e := range parseLRCEntries(lrc) {
			s, end := int(math.Round(e.Time*1000)), int(math.Round(e.End*1000))
			lines = append(lines, tokenLine{Start: s, End: end, Text: e.Text, Tokens: []tokenWord{{Start: s, End: end, Text: e.Text}}})
		}
	}
	if len(lines) == 0 {
		return ""
	}
	var out []string
	meta := extractLRCMetadata(lrc)
	for _, field := range []struct{ key, value string }{
		{"ti", firstNonEmpty(p.MusicName, meta["ti"])},
		{"ar", firstNonEmpty(p.Artist, meta["ar"])},
		{"al", p.Album},
	} {
		if field.value != "" {
			out = append(out, "["+field.key+":"+field.value+"]")
		}
	}
	type language struct {
		Language     int        `json:"language"`
		Type         int        `json:"type"`
		LyricContent [][]string `json:"lyricContent"`
	}
	var languages []language
	starts, texts := tokenLineStarts(lines)
	for _, track := range []struct {
		text string
		kind int
	}{{translation, 1}, {roma, 0}} {
		if strings.TrimSpace(track.text) == "" {
			continue
		}
		content := make([][]string, len(lines))
		for i, text := range alignSideTrack(track.text, starts, texts) {
			content[i] = []string{text}
		}
		languages = append(languages, language{Type: track.kind, LyricContent: content})
	}
	if len(languages) > 0 {
		data, _ := json.Marshal(struct {
			Content []language `json:"content"`
		}{languages})
		out = append(out, "[language:"+base64.StdEncoding.EncodeToString(data)+"]")
	}
	for i, line := range lines {
		start := line.Start
		for _, w := range line.Tokens {
			start = min(start, w.Start)
		}
		var body strings.Builder
		body.WriteString("[" + itoa(start) + "," + itoa(max0(resolveLineEnd(lines, i)-start)) + "]")
		for _, w := range line.Tokens {
			body.WriteString("<" + itoa(w.Start-start) + "," + itoa(max0(w.End-w.Start)) + ",0>" + w.Text)
		}
		out = append(out, body.String())
	}
	return strings.Join(out, "\n")
}

// Convert returns strings for all formats; KRC is the exception containing
// binary file bytes. Callers must write it verbatim, without UTF-8 conversion.
func encodeKRC(text string) string {
	if text == "" {
		return ""
	}
	var compressed bytes.Buffer
	w := zlib.NewWriter(&compressed)
	_, _ = w.Write([]byte(text)) // bytes.Buffer writes cannot fail.
	_ = w.Close()
	out := append([]byte("krc1"), compressed.Bytes()...)
	for i := 4; i < len(out); i++ {
		out[i] ^= kugouKRCKey[(i-4)%len(kugouKRCKey)]
	}
	return string(out)
}
