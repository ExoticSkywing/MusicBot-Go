package lyric

import (
	"encoding/json"
	"regexp"
	"strings"
)

// xmlEscape escapes text for XML attribute/content (ENT_QUOTES | ENT_XML1).
func xmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
	s = strings.ReplaceAll(s, "'", "&#39;")
	return s
}

// lrcToTTML renders line-timed LRC to AMLL-flavored TTML. Mirrors lrcToTtml.
func lrcToTTML(lrc, tlyric string, p Payload, roma string, inlineTracks, romaFirst bool) string {
	entries := parseLRCEntries(lrc)
	if len(entries) == 0 {
		return buildTTMLDocument(nil, 0, 0, p, "", "Line")
	}
	starts, texts := lrcEntryStarts(entries)
	translations := alignSideTrack(tlyric, starts, texts)
	romas := alignSideTrack(roma, starts, texts)
	sides := newTTMLSideTracks(romaFirst)

	var lines []string
	duration := 0.0
	for i, e := range entries {
		lineKey := "L" + itoa(i+1)
		line := "<p begin=\"" + secondsToTTMLTime(e.Time) + "\" end=\"" + secondsToTTMLTime(e.End) + "\" itunes:key=\"" + lineKey + "\" ttm:agent=\"v1\">" + xmlEscape(e.Text)
		line += sides.add(lineKey, translations[i], romas[i], inlineTracks)
		line += "</p>"
		lines = append(lines, "      "+line)
		duration = maxFloat(duration, e.End)
	}
	itunesMetadata := ""
	if !inlineTracks {
		itunesMetadata = sides.metadata()
	}
	return buildTTMLDocument(lines, duration, entries[0].Time, p, itunesMetadata, "Line")
}

// tokenToTTML renders token lines to word-timed TTML. Mirrors tokenToTtml,
// including the background-line folding for short echo lines.
func tokenToTTML(token, tlyric string, p Payload, roma string, inlineTracks, romaFirst bool) string {
	lines := parseTokenLines(token)
	if len(lines) == 0 {
		return lrcToTTML(tokenToLRC(token), tlyric, p, roma, inlineTracks, romaFirst)
	}
	starts, texts := tokenLineStarts(lines)
	translations := alignSideTrack(tlyric, starts, texts)
	romas := alignSideTrack(roma, starts, texts)
	sides := newTTMLSideTracks(romaFirst)
	skip := map[int]bool{}

	var pLines []string
	durationMs := 0
	for idx := 0; idx < len(lines); idx++ {
		if skip[idx] {
			continue
		}
		line := lines[idx]
		lineStartMs := starts[idx]
		lineEndMs := resolveLineEnd(lines, idx)

		var spans []string
		var bgInner []string
		bgStartMs, bgEndMs := -1, -1
		flushBg := func() {
			if len(bgInner) == 0 {
				return
			}
			start := bgStartMs
			if start < 0 {
				start = lineStartMs
			}
			end := max(bgEndMs, start)
			spans = append(spans, "<span ttm:role=\"x-bg\" begin=\""+ttmlMs(start)+"\" end=\""+ttmlMs(end)+"\">"+strings.Join(bgInner, "")+"</span>")
			bgInner = nil
			bgStartMs, bgEndMs = -1, -1
		}

		// A parenthesised run -- "(oh", "my", "god)" -- is one background
		// phrase, so the state carries across tokens until the closing paren.
		inBg := false
		for _, tk := range line.Tokens {
			trimmed := strings.TrimSpace(tk.Text)
			isBg := inBg || bgOpenRe.MatchString(trimmed)
			inBg = isBg && !bgCloseRe.MatchString(trimmed)
			tokenSpan := ttmlWordSpan(tk)
			if isBg {
				bgInner = append(bgInner, tokenSpan)
				if tk.Start >= 0 && (bgStartMs < 0 || tk.Start < bgStartMs) {
					bgStartMs = tk.Start
				}
				if tk.End > 0 && tk.End > bgEndMs {
					bgEndMs = tk.End
				}
				continue
			}
			flushBg()
			spans = append(spans, tokenSpan)
		}
		flushBg()

		lineKey := "L" + itoa(idx+1)
		mainContent := strings.Join(spans, "")
		if mainContent == "" {
			mainContent = "<span>" + xmlEscape(line.Text) + "</span>"
		}
		mainContent += sides.add(lineKey, translations[idx], romas[idx], inlineTracks)

		// Fold a short overlapping echo line into x-bg.
		if idx+1 < len(lines) && translations[idx+1] == "" && romas[idx+1] == "" && shouldAttachAsBackgroundLine(line, lines[idx+1]) {
			nextLine := lines[idx+1]
			var inner []string
			for _, ntk := range nextLine.Tokens {
				inner = append(inner, ttmlWordSpan(ntk))
			}
			if len(inner) > 0 {
				bgStart := nextLine.Start
				bgEnd := max(resolveLineEnd(lines, idx+1), bgStart)
				mainContent += "<span ttm:role=\"x-bg\" begin=\"" + ttmlMs(bgStart) + "\" end=\"" + ttmlMs(bgEnd) + "\">" + strings.Join(inner, "") + "</span>"
				lineEndMs = max(lineEndMs, bgEnd)
				skip[idx+1] = true
			}
		}

		pLines = append(pLines, "      <p begin=\""+ttmlMs(lineStartMs)+"\" end=\""+ttmlMs(lineEndMs)+"\" itunes:key=\""+lineKey+"\" ttm:agent=\"v1\">"+mainContent+"</p>")
		durationMs = max(durationMs, lineEndMs)
	}

	itunesMetadata := ""
	if !inlineTracks {
		itunesMetadata = sides.metadata()
	}
	firstStart := starts[0]
	for _, start := range starts {
		firstStart = min(firstStart, start)
	}
	return buildTTMLDocument(pLines, float64(durationMs)/1000.0, float64(firstStart)/1000.0, p, itunesMetadata, "Word")
}

func ttmlMs(ms int) string { return secondsToTTMLTime(float64(ms) / 1000.0) }

// ttmlWordSpan renders one timed word. Leading/trailing whitespace goes outside
// the span, as Apple Music writes it ("<span>Hello</span> <span>world</span>"):
// the inter-word space is then a text node every TTML reader keeps, rather than
// span content some readers trim.
func ttmlWordSpan(tk tokenWord) string {
	core := strings.TrimSpace(tk.Text)
	if core == "" {
		return xmlEscape(tk.Text)
	}
	lead := tk.Text[:strings.Index(tk.Text, core)]
	trail := tk.Text[len(lead)+len(core):]
	if tk.End <= tk.Start || tk.Start < 0 {
		return xmlEscape(tk.Text)
	}
	return lead + "<span begin=\"" + ttmlMs(tk.Start) + "\" end=\"" + ttmlMs(tk.End) + "\">" + xmlEscape(core) + "</span>" + trail
}

// ttmlSideTracks collects per-line translation/roma text, either inlined as
// x-translation/x-roman spans or gathered for the iTunesMetadata head block.
type ttmlSideTracks struct {
	romaFirst    bool
	keys         []string
	translations map[string]string
	romas        map[string]string
}

func newTTMLSideTracks(romaFirst bool) *ttmlSideTracks {
	return &ttmlSideTracks{romaFirst: romaFirst, translations: map[string]string{}, romas: map[string]string{}}
}

// add records a line's side texts and returns the inline spans to append to
// its <p> (empty unless inline).
func (s *ttmlSideTracks) add(lineKey, translation, romaji string, inline bool) string {
	if translation == "" && romaji == "" {
		return ""
	}
	s.keys = append(s.keys, lineKey)
	if translation != "" {
		s.translations[lineKey] = translation
	}
	if romaji != "" {
		s.romas[lineKey] = romaji
	}
	if !inline {
		return ""
	}
	translationSpan := ""
	if translation != "" {
		translationSpan = "<span ttm:role=\"x-translation\" xml:lang=\"zh-Hans\">" + xmlEscape(translation) + "</span>"
	}
	romaSpan := ""
	if romaji != "" {
		romaSpan = "<span ttm:role=\"x-roman\" xml:lang=\"ja-Latn\">" + xmlEscape(romaji) + "</span>"
	}
	return strings.Join(buildOrderedOutputLines(translationSpan, romaSpan, s.romaFirst), "")
}

func (s *ttmlSideTracks) metadata() string {
	return buildITunesMetadataLocalizations(s.keys, s.translations, s.romas, s.romaFirst)
}

var bgOpenRe = regexp.MustCompile(`^[（(]`)
var bgCloseRe = regexp.MustCompile(`[)）]$`)

var comparablePunctRe = regexp.MustCompile(`[\s\p{P}\p{S}]+`)

func normalizeComparableText(text string) string {
	text = strings.ToLower(strings.TrimSpace(text))
	if text == "" {
		return ""
	}
	return comparablePunctRe.ReplaceAllString(text, "")
}

func shouldAttachAsBackgroundLine(mainLine, nextLine tokenLine) bool {
	mainStart, mainEnd := mainLine.Start, mainLine.End
	nextStart, nextEnd := nextLine.Start, nextLine.End
	if nextStart <= 0 || nextEnd <= nextStart || mainEnd <= mainStart {
		return false
	}
	if nextStart < mainStart || nextStart >= mainEnd {
		return false
	}
	if nextEnd-nextStart > 1400 {
		return false
	}
	if len(nextLine.Tokens) > 6 {
		return false
	}
	mainText := normalizeComparableText(mainLine.Text)
	nextText := normalizeComparableText(nextLine.Text)
	if mainText == "" || nextText == "" {
		return false
	}
	if strings.Contains(mainText, nextText) {
		return true
	}
	return strings.HasSuffix(mainText, nextText)
}

// buildTTMLDocument wraps rendered <p> lines in the TTML envelope. timing is
// the itunes:timing value: "Word" for word-timed lines, "Line" for line-timed.
func buildTTMLDocument(pLines []string, durationSeconds, divBeginSeconds float64, p Payload, itunesMetadata, timing string) string {
	ncmMusicID := p.NcmMusicID
	qqMusicID := p.QqMusicID

	meta := []string{"      <ttm:agent type=\"person\" xml:id=\"v1\"/>"}
	if itunesMetadata != "" {
		meta = append(meta, itunesMetadata)
	}
	meta = append(meta,
		"      <amll:meta key=\"musicName\" value=\""+xmlEscape(p.MusicName)+"\"/>",
		"      <amll:meta key=\"artists\" value=\""+xmlEscape(p.Artist)+"\"/>",
		"      <amll:meta key=\"album\" value=\""+xmlEscape(p.Album)+"\"/>",
	)
	if ncmMusicID != "" {
		meta = append(meta, "      <amll:meta key=\"ncmMusicId\" value=\""+xmlEscape(ncmMusicID)+"\"/>")
	}
	if qqMusicID != "" {
		meta = append(meta, "      <amll:meta key=\"qqMusicId\" value=\""+xmlEscape(qqMusicID)+"\"/>")
	}

	dur := secondsToTTMLTime(maxFloat(0, durationSeconds))
	divBegin := secondsToTTMLTime(maxFloat(0, divBeginSeconds))
	return "<tt xmlns=\"http://www.w3.org/ns/ttml\" xmlns:amll=\"http://www.example.com/ns/amll\" xmlns:itunes=\"http://music.apple.com/lyric-ttml-internal\" xmlns:ttm=\"http://www.w3.org/ns/ttml#metadata\" itunes:timing=\"" + timing + "\">\n" +
		"  <head>\n" +
		"    <metadata>\n" +
		strings.Join(meta, "\n") +
		"\n    </metadata>\n" +
		"  </head>\n" +
		"  <body dur=\"" + dur + "\">\n" +
		"    <div begin=\"" + divBegin + "\" end=\"" + dur + "\">\n" +
		strings.Join(pLines, "\n") +
		"\n    </div>\n" +
		"  </body>\n" +
		"</tt>"
}

func buildITunesMetadataLocalizations(orderedKeys []string, translationsByKey, romasByKey map[string]string, romaFirst bool) string {
	hasTranslation := len(translationsByKey) > 0
	hasRoma := len(romasByKey) > 0
	if !hasTranslation && !hasRoma {
		return ""
	}

	out := []string{"      <iTunesMetadata xmlns=\"http://music.apple.com/lyric-ttml-internal\">"}

	translationBlock := ""
	if hasTranslation {
		lines := []string{"        <translations>", "          <translation type=\"subtitle\" xml:lang=\"zh-Hans\">"}
		for _, key := range orderedKeys {
			if text, ok := translationsByKey[key]; ok {
				lines = append(lines, "            <text for=\""+xmlEscape(key)+"\">"+xmlEscape(text)+"</text>")
			}
		}
		lines = append(lines, "          </translation>", "        </translations>")
		translationBlock = strings.Join(lines, "\n")
	}

	romaBlock := ""
	if hasRoma {
		lines := []string{"        <transliterations>", "          <transliteration xml:lang=\"ja-Latn\">"}
		for _, key := range orderedKeys {
			if text, ok := romasByKey[key]; ok {
				lines = append(lines, "            <text for=\""+xmlEscape(key)+"\">"+xmlEscape(text)+"</text>")
			}
		}
		lines = append(lines, "          </transliteration>", "        </transliterations>")
		romaBlock = strings.Join(lines, "\n")
	}

	for _, block := range buildOrderedOutputLines(translationBlock, romaBlock, romaFirst) {
		if block != "" {
			out = append(out, block)
		}
	}
	out = append(out, "      </iTunesMetadata>")
	return strings.Join(out, "\n")
}

var xmlDeclRe = regexp.MustCompile(`(?s)^\s*<\?xml[^>]*\?>\s*`)

// xmlGapRe matches the indentation between tags: whitespace that contains a
// line break. A plain space between two word spans is an inter-word space and
// must survive compaction.
var xmlGapRe = regexp.MustCompile(`>[ \t]*[\r\n]\s*<`)

func compactTTMLForAmjson(ttml string) string {
	ttml = xmlDeclRe.ReplaceAllString(ttml, "")
	ttml = xmlGapRe.ReplaceAllString(ttml, "><")
	return strings.TrimSpace(ttml)
}

func ttmlToAppleMusicJSON(ttml string) string {
	compact := compactTTMLForAmjson(ttml)
	doc := map[string]interface{}{
		"data": []interface{}{
			map[string]interface{}{
				"id":   "",
				"type": "syllable-lyrics",
				"attributes": map[string]interface{}{
					"playParams": map[string]interface{}{
						"catalogId":   "",
						"displayType": 3,
						"id":          "AP_",
						"kind":        "lyric",
					},
					"ttmlLocalizations": compact,
				},
			},
		},
	}
	var sb strings.Builder
	enc := json.NewEncoder(&sb)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(doc); err != nil {
		return ""
	}
	return strings.TrimRight(sb.String(), "\n")
}
