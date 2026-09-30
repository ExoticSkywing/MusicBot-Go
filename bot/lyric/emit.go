package lyric

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// tokenToLRC flattens token lines to line-timed LRC. Mirrors tokenToLrc.
func tokenToLRC(token string) string {
	lines := parseTokenLines(token)
	if len(lines) == 0 {
		return ""
	}
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, formatLRCTagFromMs(line.Start, 2)+line.Text)
	}
	return strings.Join(out, "\n")
}

// tokenToYRC re-emits token lines as netease yrc. Mirrors tokenToYrc.
func tokenToYRC(token string) string {
	lines := parseTokenLines(token)
	if len(lines) == 0 {
		return ""
	}
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		prefix := "[" + itoa(line.Start) + "," + itoa(max0(line.End-line.Start)) + "]"
		var sb strings.Builder
		for _, tk := range line.Tokens {
			dur := max0(tk.End - tk.Start)
			sb.WriteString("(" + itoa(tk.Start) + "," + itoa(dur) + ",0)" + tk.Text)
		}
		content := sb.String()
		if content == "" {
			content = line.Text
		}
		out = append(out, prefix+content)
	}
	return strings.Join(out, "\n")
}

// tokenToQRC re-emits token lines as QQ qrc. Mirrors tokenToQrc.
func tokenToQRC(token string) string {
	lines := parseTokenLines(token)
	if len(lines) == 0 {
		return ""
	}
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		prefix := "[" + itoa(line.Start) + "," + itoa(max0(line.End-line.Start)) + "]"
		var sb strings.Builder
		for _, tk := range line.Tokens {
			dur := max0(tk.End - tk.Start)
			sb.WriteString(tk.Text + "(" + itoa(tk.Start) + "," + itoa(dur) + ")")
		}
		content := sb.String()
		if content == "" {
			content = line.Text
		}
		out = append(out, prefix+content)
	}
	return strings.Join(out, "\n")
}

// tokenToLys re-emits token lines as Lyricify Syllable, with a line-role prefix.
// Mirrors tokenToLys (text(start,dur) segments).
func tokenToLys(token, linePrefix string, stripLeadingPreface bool) string {
	lines := parseTokenLines(token)
	if len(lines) == 0 {
		return ""
	}
	out := make([]string, 0, len(lines))
	started := false
	for _, line := range lines {
		var sb strings.Builder
		for _, tk := range line.Tokens {
			dur := max0(tk.End - tk.Start)
			sb.WriteString(tk.Text + "(" + itoa(tk.Start) + "," + itoa(dur) + ")")
		}
		content := sb.String()
		if content == "" {
			content = line.Text
		}
		if stripLeadingPreface && !started {
			if isLqePrefaceLikeLine(strings.TrimSpace(line.Text)) {
				continue
			}
			started = true
		}
		out = append(out, linePrefix+content)
	}
	return strings.Join(out, "\n")
}

// tokenToLysDocument wraps tokenToLys with [ti]/[ar]/[by] headers. Mirrors
// tokenToLysDocument.
func tokenToLysDocument(token string, p Payload, lyric, tlyric, roma string) string {
	body := tokenToLys(token, "[4]", true)
	if strings.TrimSpace(body) == "" {
		return ""
	}
	meta := extractLRCMetadata(lyric, tlyric, roma)
	musicName := firstNonEmpty(p.MusicName, meta["ti"])
	artists := firstNonEmpty(p.Artist, meta["ar"])
	by := meta["by"]

	var out []string
	if musicName != "" {
		out = append(out, "[ti:"+musicName+"]")
	}
	if artists != "" {
		out = append(out, "[ar:"+artists+"]")
	}
	out = append(out, "[by:"+by+"]", "", body)
	return strings.Join(out, "\n")
}

// tokenToElrc re-emits token lines as enhanced LRC (per-word "<mm:ss.fff>" tags).
// Mirrors tokenToElrc.
func tokenToElrc(token string, p Payload, lyric, tlyric, roma string) string {
	lines := parseTokenLines(token)
	if len(lines) == 0 {
		return ""
	}
	meta := extractLRCMetadata(lyric, tlyric, roma)
	musicName := firstNonEmpty(p.MusicName, meta["ti"])
	artists := firstNonEmpty(p.Artist, meta["ar"])
	by := meta["by"]

	var out []string
	if musicName != "" {
		out = append(out, "[ti:"+musicName+"]")
	}
	if artists != "" {
		out = append(out, "[ar:"+artists+"]")
	}
	out = append(out, "[by:"+by+"]")

	started := false
	for _, line := range lines {
		plain := strings.TrimSpace(line.Text)
		if !started && isLqePrefaceLikeLine(plain) {
			continue
		}
		started = true
		lineText := formatLRCTagFromMs(line.Start, 3)
		if len(line.Tokens) == 0 {
			lineText += formatElrcWordTag(line.Start) + plain
		} else {
			for i, tk := range line.Tokens {
				lineText += formatElrcWordTag(tk.Start) + tk.Text
				if i+1 == len(line.Tokens) || tk.End < line.Tokens[i+1].Start {
					lineText += formatElrcWordTag(tk.End)
				}
			}
		}
		out = append(out, lineText)
	}
	return strings.Join(out, "\n")
}

func formatElrcWordTag(ms int) string {
	tag := formatLRCTagFromMs(ms, 3)
	return "<" + tag[1:len(tag)-1] + ">"
}

// --- simple text formats ---

func lrcToTxt(lrc string) string {
	var out []string
	for _, line := range splitLines(lrc) {
		line = strings.TrimSpace(line)
		if lrcHeaderTagRe.MatchString(line) {
			continue
		}
		clean := strings.TrimSpace(lrcStripAllTags(line))
		if clean != "" {
			out = append(out, clean)
		}
	}
	return strings.Join(out, "\n")
}

// lrcBareTagRe matches "[mm:ss.xx]" line tags and "<mm:ss.xx>" enhanced-LRC
// word tags.
var lrcBareTagRe = regexp.MustCompile(`\[[0-9:.]+\]|<[0-9]{1,3}:[0-9]{2}(?:[.:][0-9]{1,3})?>`)

func lrcStripAllTags(line string) string {
	return lrcBareTagRe.ReplaceAllString(line, "")
}

func lrcToSrt(lrc string) string {
	entries := parseLRCEntries(lrc)
	if len(entries) == 0 {
		return ""
	}
	var out []string
	for i, e := range entries {
		start := e.Time
		end := e.End
		out = append(out, itoa(i+1))
		out = append(out, secondsToSRTTime(start)+" --> "+secondsToSRTTime(end))
		out = append(out, e.Text)
		out = append(out, "")
	}
	return strings.Join(out, "\n")
}

// Preserve explicit line ends and millisecond precision when no plain LRC
// was supplied. Flattening through LRC would discard both.
func tokenToSrt(token string) string {
	lines := parseTokenLines(token)
	sort.SliceStable(lines, func(i, j int) bool { return lines[i].Start < lines[j].Start })
	var out []string
	for i, line := range lines {
		start := float64(resolveLineStartFromTokens(line)) / 1000
		end := float64(resolveLineEnd(lines, i)) / 1000
		out = append(out, itoa(i+1), secondsToSRTTime(start)+" --> "+secondsToSRTTime(end), line.Text, "")
	}
	return strings.Join(out, "\n")
}

func translationOnly(tlyric string) string {
	if strings.TrimSpace(tlyric) == "" {
		return ""
	}
	var out []string
	for _, row := range splitLines(tlyric) {
		line := strings.TrimSpace(row)
		if line == "" || line == "//" {
			continue
		}
		if _, text, ok := parseLRCRow(line); !ok || text == "" || text == "//" {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// mergeLrcTracks interleaves translation and/or roma side-tracks under each
// original LRC line. Mirrors LyricConverterService::mergeLrcTracks, but matches
// side lines by time (see alignSideTrack) rather than by the literal tag text,
// so "[00:12.34]" and "[00:12.340]" still pair up. The original line text and
// tag are preserved verbatim; extra lines reuse the original line's tag so
// players keep them in sync. A row carrying several time tags is split into
// one row per tag. Order honors romaFirst.
func mergeLrcTracks(lyric, tlyric, roma string, romaFirst bool) string {
	if strings.TrimSpace(lyric) == "" {
		return lyric
	}
	if strings.TrimSpace(tlyric) == "" && strings.TrimSpace(roma) == "" {
		return lyric
	}

	// item is one output row plus any untimed rows that followed it.
	type item struct {
		ms     int
		timed  bool
		tag    string
		text   string
		extras []string
	}
	var items []item
	multiTag := false
	for _, raw := range splitLines(lyric) {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		row := strings.TrimSpace(raw)
		var tags []string
		var times []int
		for {
			m := lrcTimeTagRe.FindStringSubmatch(row)
			if m == nil {
				break
			}
			tags = append(tags, m[0])
			times = append(times, (mustAtoi(m[1])*60+mustAtoi(m[2]))*1000+parseLRCFractionToMs(m[3]))
			row = row[len(m[0]):]
		}
		if len(tags) == 0 {
			if len(items) > 0 && items[len(items)-1].timed {
				items[len(items)-1].extras = append(items[len(items)-1].extras, raw)
			} else {
				items = append(items, item{text: raw})
			}
			continue
		}
		if len(tags) > 1 {
			multiTag = true
		}
		for k := range tags {
			items = append(items, item{ms: times[k], timed: true, tag: tags[k], text: row})
		}
	}
	if multiTag {
		// Untimed header rows stay on top; timed rows follow in time order.
		sort.SliceStable(items, func(i, j int) bool {
			if items[i].timed != items[j].timed {
				return !items[i].timed
			}
			return items[i].ms < items[j].ms
		})
	}

	var starts []int
	var texts []string
	var idx []int
	for i, it := range items {
		if it.timed && strings.TrimSpace(it.text) != "" {
			starts = append(starts, it.ms)
			texts = append(texts, strings.TrimSpace(it.text))
			idx = append(idx, i)
		}
	}
	transByItem := map[int]string{}
	romaByItem := map[int]string{}
	for k, t := range alignSideTrack(tlyric, starts, texts) {
		transByItem[idx[k]] = t
	}
	for k, r := range alignSideTrack(roma, starts, texts) {
		romaByItem[idx[k]] = r
	}

	var out []string
	for i, it := range items {
		if !it.timed {
			out = append(out, it.text)
			continue
		}
		out = append(out, it.tag+it.text)
		trans := transByItem[i]
		romaji := romaByItem[i]
		transLine := ""
		if trans != "" {
			transLine = it.tag + trans
		}
		romaLine := ""
		if romaji != "" && romaji != trans {
			romaLine = it.tag + romaji
		}
		out = append(out, buildOrderedOutputLines(transLine, romaLine, romaFirst)...)
		out = append(out, it.extras...)
	}
	return strings.Join(out, "\n")
}

func buildOrderedOutputLines(translationLine, romaLine string, romaFirst bool) []string {
	var ordered []string
	if romaFirst {
		ordered = []string{romaLine, translationLine}
	} else {
		ordered = []string{translationLine, romaLine}
	}
	out := make([]string, 0, 2)
	for _, l := range ordered {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

// creditLineRe matches leading credit/metadata lines (作词/作曲/编曲/制作…). Bare
// 词/曲 are included because QQ Music writes credits as "词：…"/"曲：…" rather
// than "作词：…"; anchored at line start + immediate colon, a real lyric almost
// never collides. Mirrors isLqePrefaceLikeLine's bare 词|曲 precedent.
var creditLineRe = regexp.MustCompile(`(?i)^(演唱|歌手|调教|作词|作曲|编曲|制作人|制作|监制|录音|混音|母带|和声|合声|弦乐团|出品人|出品|艺术总监|歌词翻译|词曲|词|曲|Lyricist|Composer|Producer|Arranger|Vocal|Mix|Master|OP|SP)([^:：]{0,20})?[:：]`)

func isCreditLikeLine(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return false
	}
	return creditLineRe.MatchString(text)
}

var (
	lqePrefaceDashRe = regexp.MustCompile(`\s-\s`)
	lqePrefaceWordRe = regexp.MustCompile(`(?i)^(词|曲|作词|作曲|Lyricist|Composer)\s*[:：]`)
)

func isLqePrefaceLikeLine(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return false
	}
	return lqePrefaceDashRe.MatchString(text) || lqePrefaceWordRe.MatchString(text)
}

var multiSpaceRe = regexp.MustCompile(`\s\s+`)

// normalizeRomaLyric strips secondary inline timestamps from roma content.
// Mirrors normalizeRomaLyric.
func normalizeRomaLyric(roma string) string {
	if strings.TrimSpace(roma) == "" {
		return ""
	}
	var out []string
	for _, line := range splitLines(roma) {
		if line == "" {
			out = append(out, line)
			continue
		}
		prefix := ""
		content := strings.TrimSpace(line)
		for {
			m := lrcTimeTagRe.FindString(content)
			if m == "" {
				break
			}
			prefix += m
			content = content[len(m):]
		}
		content = lrcAnyTagRe.ReplaceAllString(content, " ")
		content = strings.TrimSpace(multiSpaceRe.ReplaceAllString(content, " "))
		out = append(out, prefix+content)
	}
	return strings.Join(out, "\n")
}

// extractLRCMetadata pulls [ti]/[ar]/[by] from the given tracks (first wins).
func extractLRCMetadata(tracks ...string) map[string]string {
	meta := map[string]string{}
	for _, track := range tracks {
		if track == "" {
			continue
		}
		for _, row := range splitLines(track) {
			line := strings.TrimSpace(row)
			if line == "" {
				continue
			}
			m := lrcMetaRe.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			key := strings.ToLower(m[1])
			val := strings.TrimSpace(m[2])
			if val != "" {
				if _, ok := meta[key]; !ok {
					meta[key] = val
				}
			}
		}
	}
	return meta
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func itoa(v int) string {
	return strconv.Itoa(v)
}

func max0(v int) int {
	if v < 0 {
		return 0
	}
	return v
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
