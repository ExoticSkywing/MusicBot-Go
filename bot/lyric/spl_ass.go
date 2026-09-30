package lyric

import "strings"

// --- SPL (Salt Player Lyrics) ---

// lrcToSpl renders line-timed LRC to SPL with optional translation/roma.
// Mirrors lrcToSpl.
func lrcToSpl(lrc, tlyric, roma string, romaFirst bool) string {
	entries := parseLRCEntries(lrc)
	if len(entries) == 0 {
		return ""
	}
	starts, texts := lrcEntryStarts(entries)
	translations := alignSideTrack(tlyric, starts, texts)
	romas := alignSideTrack(roma, starts, texts)
	adjacentMap := parseAdjacentTranslationMap(lrc)

	var out []string
	for i, e := range entries {
		tag := formatSplLineModeTag(e.Time)
		out = append(out, tag+e.Text+formatSplLineModeTag(e.End))
		translation := translations[i]
		if translation == "" && !isCreditLikeLine(e.Text) {
			translation = adjacentMap[e.Tag]
		}
		out = append(out, splSideLines(tag, translation, romas[i], romaFirst)...)
	}
	return strings.Join(out, "\n")
}

// tokenToSpl renders token lines to SPL with per-word "<mm:ss.cc>" timing.
// Mirrors tokenToSpl.
func tokenToSpl(token, tlyric, roma string, romaFirst bool) string {
	lines := parseTokenLines(token)
	if len(lines) == 0 {
		return lrcToSpl(tokenToLRC(token), tlyric, roma, romaFirst)
	}
	starts, texts := tokenLineStarts(lines)
	translations := alignSideTrack(tlyric, starts, texts)
	romas := alignSideTrack(roma, starts, texts)

	var out []string
	for i, line := range lines {
		tag := formatSplTimestamp(starts[i], false)
		out = append(out, tag+splWordLine(line, starts[i], resolveLineEnd(lines, i)))
		out = append(out, splSideLines(tag, translations[i], romas[i], romaFirst)...)
	}
	return strings.Join(out, "\n")
}

// splWordLine renders the body of a word-timed SPL line (everything after the
// leading line tag). The line tag doubles as the first word's start, so a word
// tag is written only where the timeline would otherwise be ambiguous: at a
// word whose start differs from where the previous word ended (a gap), and at
// each word's end. The line closes with a "[mm:ss.cc]" end tag.
//
// Comparisons are made on the rendered centisecond value: two timestamps that
// round to the same tag must not produce a zero-length "<t><t>" pair.
func splWordLine(line tokenLine, startMs, endMs int) string {
	if len(line.Tokens) == 0 {
		return line.Text
	}
	var sb strings.Builder
	cursor := msToRoundedCentis(startMs)
	lastEnd := cursor
	for i, tk := range line.Tokens {
		if s := msToRoundedCentis(tk.Start); s != cursor {
			sb.WriteString(formatSplTimestamp(tk.Start, true))
		}
		sb.WriteString(tk.Text)
		cursor = msToRoundedCentis(tk.End)
		lastEnd = cursor
		isFinalBoundary := i == len(line.Tokens)-1 && msToRoundedCentis(endMs) <= cursor
		sb.WriteString(formatSplTimestamp(tk.End, !isFinalBoundary))
	}
	body := sb.String()
	if end := msToRoundedCentis(endMs); end > lastEnd {
		body += formatSplTimestamp(endMs, false)
	}
	return body
}

func splSideLines(tag, translation, romaji string, romaFirst bool) []string {
	translationLine := ""
	if translation != "" {
		translationLine = tag + translation
	}
	romaLine := ""
	if romaji != "" && romaji != translation {
		romaLine = tag + romaji
	}
	return buildOrderedOutputLines(translationLine, romaLine, romaFirst)
}

// parseAdjacentTranslationMap maps a line's tag to the following untimed line,
// used as a translation fallback. Mirrors parseAdjacentTranslationMapFromLyric.
func parseAdjacentTranslationMap(lrc string) map[string]string {
	m := map[string]string{}
	rows := splitLines(lrc)
	for i := 0; i < len(rows); i++ {
		times, _, ok := parseLRCRow(rows[i])
		if !ok {
			continue
		}
		j := i + 1
		for j < len(rows) && strings.TrimSpace(rows[j]) == "" {
			j++
		}
		if j >= len(rows) {
			continue
		}
		next := strings.TrimSpace(rows[j])
		if _, _, timed := parseLRCRow(next); timed || lrcHeaderTagRe.MatchString(next) {
			continue
		}
		for _, ms := range times {
			m[formatLRCTagFromMs(ms, 2)] = next
		}
	}
	return m
}

// --- ASS (Advanced SubStation Alpha) ---

func lrcToAss(lrc, tlyric, roma string, romaFirst bool) string {
	entries := parseLRCEntries(lrc)
	if len(entries) == 0 {
		return ""
	}
	starts, texts := lrcEntryStarts(entries)
	translations := alignSideTrack(tlyric, starts, texts)
	romas := alignSideTrack(roma, starts, texts)

	var dialogues []string
	for i, e := range entries {
		start := e.Time
		end := e.End
		dialogues = append(dialogues, assDialogue(start, end, "Default", "v1", escapeAssText(e.Text)))
		dialogues = append(dialogues, assSideDialogues(start, end, translations[i], romas[i], romaFirst)...)
	}
	return buildAssDocument(dialogues)
}

func tokenToAss(token, tlyric, roma string, romaFirst bool) string {
	lines := parseTokenLines(token)
	if len(lines) == 0 {
		return lrcToAss(tokenToLRC(token), tlyric, roma, romaFirst)
	}
	starts, texts := tokenLineStarts(lines)
	translations := alignSideTrack(tlyric, starts, texts)
	romas := alignSideTrack(roma, starts, texts)

	var dialogues []string
	for i, line := range lines {
		startMs := starts[i]
		endMs := resolveLineEnd(lines, i)
		start := float64(startMs) / 1000.0
		end := float64(endMs) / 1000.0
		karaoke := buildAssKaraokeFromTokens(line.Tokens, line.Text, startMs)
		dialogues = append(dialogues, assDialogue(start, end, "Default", "v1", karaoke))
		dialogues = append(dialogues, assSideDialogues(start, end, translations[i], romas[i], romaFirst)...)
	}
	return buildAssDocument(dialogues)
}

func assDialogue(start, end float64, style, name, text string) string {
	return "Dialogue: 0," + secondsToASSTime(start) + "," + secondsToASSTime(end) + "," + style + "," + name + ",0,0,0,," + text
}

// assSideDialogues renders the translation/roma dialogues shown alongside a line.
func assSideDialogues(start, end float64, translation, romaji string, romaFirst bool) []string {
	translationDialogue := ""
	if translation != "" {
		translationDialogue = assDialogue(start, end, "ts", "x-lang:zh-Hans", escapeAssText(translation))
	}
	romaDialogue := ""
	if romaji != "" && romaji != translation {
		romaDialogue = assDialogue(start, end, "roma", "x-lang:ja-Latn", escapeAssText(romaji))
	}
	return buildOrderedOutputLines(translationDialogue, romaDialogue, romaFirst)
}

// buildAssKaraokeFromTokens renders word timing as "{\kNN}" karaoke tags, NN in
// centiseconds counted from the dialogue start. Gaps between words become empty
// "{\kNN}" blocks so later words do not highlight early, and every boundary is
// rounded against the line start rather than per word, so rounding error does
// not accumulate across a long line.
func buildAssKaraokeFromTokens(tokens []tokenWord, fallbackText string, lineStartMs int) string {
	if len(tokens) == 0 {
		return escapeAssText(fallbackText)
	}
	pos := func(ms int) int { return roundDiv(max0(ms-lineStartMs), 10) }
	var sb strings.Builder
	cursor := lineStartMs
	for _, tk := range tokens {
		if tk.Text == "" {
			continue
		}
		s := max(tk.Start, cursor)
		e := max(tk.End, s)
		if gap := pos(s) - pos(cursor); gap > 0 {
			sb.WriteString("{\\k" + itoa(gap) + "}")
		}
		sb.WriteString("{\\k" + itoa(pos(e)-pos(s)) + "}" + escapeAssText(tk.Text))
		cursor = e
	}
	if sb.Len() == 0 {
		return escapeAssText(fallbackText)
	}
	return sb.String()
}

func buildAssDocument(dialogues []string) string {
	return "[Script Info]\n" +
		"ScriptType: v4.00+\n" +
		"PlayResX: 1920\n" +
		"PlayResY: 1080\n" +
		"\n[V4+ Styles]\n" +
		"Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding\n" +
		"Style: Default,Arial,100,&H00FFFFFF,&H003F3F3F,&H00000000,&H00000000,-1,0,0,0,100,100,0,0,1,2,1,2,10,10,10,1\n" +
		"Style: Orig,Arial,100,&H00FFFFFF,&H003F3F3F,&H00000000,&H00000000,-1,0,0,0,100,100,0,0,1,2,1,2,10,10,10,1\n" +
		"Style: ts,Arial,55,&H00D3D3D3,&H000000FF,&H00000000,&H99000000,0,0,0,0,100,100,0,0,1,2,1,2,10,10,50,1\n" +
		"Style: roma,Arial,55,&H00D3D3D3,&H000000FF,&H00000000,&H99000000,0,0,0,0,100,100,0,0,1,2,1,2,10,10,50,1\n" +
		"Style: bg-ts,Arial,45,&H00A0A0A0,&H000000FF,&H00000000,&H99000000,0,0,0,0,100,100,0,0,1,1.5,1,8,10,10,55,1\n" +
		"Style: bg-roma,Arial,45,&H00A0A0A0,&H000000FF,&H00000000,&H99000000,0,0,0,0,100,100,0,0,1,1.5,1,8,10,10,55,1\n" +
		"Style: meta,Arial,40,&H00C0C0C0,&H000000FF,&H00000000,&H99000000,0,0,0,0,100,100,0,0,1,1,0,5,10,10,10,1\n" +
		"\n[Events]\n" +
		"Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n" +
		strings.Join(dialogues, "\n") +
		"\n"
}

// assTextReplacer escapes lyric text for a Dialogue Text field. Commas need no
// escaping -- Text is the last field, so renderers split only the first nine --
// but braces open override blocks and would hide the enclosed lyric, and there
// is no escape for them that VSFilter honours, so they become full-width.
var assTextReplacer = strings.NewReplacer("\r", "", "\n", "\\N", "{", "｛", "}", "｝")

func escapeAssText(text string) string {
	return assTextReplacer.Replace(text)
}

func roundDiv(value, divisor int) int {
	if divisor == 0 {
		return 0
	}
	return (value + divisor/2) / divisor
}
