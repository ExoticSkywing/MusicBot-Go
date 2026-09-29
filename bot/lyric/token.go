// Package lyric ports meting-api-new's LyricConverterService to Go. It parses
// the various platform word-by-word lyric formats (netease yrc, QQ qrc, kugou
// krc, Apple TTML) into a common token model and re-emits them to any of the
// supported target formats (lrc/yrc/qrc/lys/krc/elrc/spl/ass/lqe/ttml/amjson/
// srt/txt).
package lyric

import (
	"regexp"
	"strconv"
	"strings"
)

// tokenWord is a single timed word/syllable. Start/End are absolute
// milliseconds from the start of the track.
type tokenWord struct {
	Start int
	End   int
	Text  string
}

// tokenLine is a single lyric line with its word-level timing. Start/End are
// absolute milliseconds. Text is the concatenation of all word texts.
type tokenLine struct {
	Start  int
	End    int
	Text   string
	Tokens []tokenWord
}

var (
	// lineHeadRe matches the leading "[lineStart,lineDur]" tag shared by
	// yrc/qrc/krc line formats.
	lineHeadRe = regexp.MustCompile(`^\[(\d+),(\d+)\](.*)$`)
	// lysLineHeadRe matches a Lyricify Syllable line "[property]text(start,dur)…",
	// which carries no line timing of its own.
	lysLineHeadRe = regexp.MustCompile(`^\[(\d)\](.*\(\d+,\d+\).*)$`)
	// wordTagRe matches a "(start,dur)" or "(start,dur,flag)" word tag.
	wordTagRe = regexp.MustCompile(`\((\d+),(\d+)(?:,(\d+))?\)`)
	// yrcLeadRe matches content that opens with a yrc word tag. Checking for a
	// bare "(" is not enough: qrc/lys lines routinely open with a parenthesised
	// background word such as "(Oh (1000,500)".
	yrcLeadRe = regexp.MustCompile(`^\(\d+,\d+(?:,\d+)?\)`)
)

// looksLikeTokenTrack reports whether any line of s is a yrc/qrc/lys token line.
// It scans line by line: header tags ([ti:]/[ar:]…) usually precede the body.
func looksLikeTokenTrack(s string) bool {
	for _, row := range splitLines(s) {
		row = strings.TrimSpace(row)
		if lineHeadRe.MatchString(row) || lysLineHeadRe.MatchString(row) {
			return true
		}
	}
	return false
}

// parseTokenLines parses yrc/qrc/lys token text into canonical token lines.
//
// It mirrors LyricConverterService::parseTokenLines. Three on-wire shapes are
// supported:
//   - yrc:  [lineStart,lineDur](wStart,wDur,flag)text(wStart,wDur,flag)text
//   - qrc:  [lineStart,lineDur]text(wStart,wDur)text(wStart,wDur)
//   - lys:  [property]text(wStart,wDur)text(wStart,wDur)
//
// Word timestamps are absolute milliseconds in all shapes. LYS lines have no
// line timing, so theirs is derived from the first and last word.
func parseTokenLines(token string) []tokenLine {
	rows := splitLines(token)
	out := make([]tokenLine, 0, len(rows))
	for _, row := range rows {
		row = strings.TrimSpace(row)
		if row == "" {
			continue
		}
		var lineStart, lineEnd int
		var content string
		var tokens []tokenWord
		if m := lineHeadRe.FindStringSubmatch(row); m != nil {
			lineStart = mustAtoi(m[1])
			lineEnd = lineStart + mustAtoi(m[2])
			content = m[3]
			if yrcLeadRe.MatchString(content) {
				tokens = parseYRCWords(content)
			} else {
				tokens = parseQRCWords(content)
			}
		} else if m := lysLineHeadRe.FindStringSubmatch(row); m != nil {
			content = m[2]
			tokens = parseQRCWords(content)
			if len(tokens) == 0 {
				continue
			}
			lineStart = tokens[0].Start
			for _, tk := range tokens {
				lineEnd = max(lineEnd, tk.End)
			}
		} else {
			continue
		}

		if len(tokens) == 0 {
			text := strings.TrimSpace(wordTagRe.ReplaceAllString(content, ""))
			if text != "" {
				tokens = []tokenWord{{Start: lineStart, End: lineEnd, Text: text}}
			}
		}

		var sb strings.Builder
		for _, tk := range tokens {
			sb.WriteString(tk.Text)
		}
		lineText := sb.String()
		if lineText == "" {
			continue
		}
		out = append(out, tokenLine{Start: lineStart, End: lineEnd, Text: lineText, Tokens: tokens})
	}
	return out
}

// parseYRCWords parses the yrc shape "(start,dur,flag)text...". The text for a
// tag is everything between the end of that tag and the start of the next tag
// (or end of string). This replaces the PHP lookahead regex, which RE2 lacks.
func parseYRCWords(content string) []tokenWord {
	locs := wordTagRe.FindAllStringSubmatchIndex(content, -1)
	if len(locs) == 0 {
		return nil
	}
	tokens := make([]tokenWord, 0, len(locs))
	for i, loc := range locs {
		start := mustAtoi(content[loc[2]:loc[3]])
		dur := mustAtoi(content[loc[4]:loc[5]])
		textStart := loc[1]
		textEnd := len(content)
		if i+1 < len(locs) {
			textEnd = locs[i+1][0]
		}
		text := content[textStart:textEnd]
		if text == "" {
			continue
		}
		tokens = append(tokens, tokenWord{Start: start, End: start + dur, Text: text})
	}
	return tokens
}

// parseQRCWords parses the qrc/lys shape "text(start,dur)...". The text for a
// tag is everything between the previous tag (or start of string) and the tag.
func parseQRCWords(content string) []tokenWord {
	locs := wordTagRe.FindAllStringSubmatchIndex(content, -1)
	if len(locs) == 0 {
		return nil
	}
	tokens := make([]tokenWord, 0, len(locs))
	prev := 0
	for _, loc := range locs {
		text := content[prev:loc[0]]
		start := mustAtoi(content[loc[2]:loc[3]])
		dur := mustAtoi(content[loc[4]:loc[5]])
		prev = loc[1]
		if text == "" {
			continue
		}
		tokens = append(tokens, tokenWord{Start: start, End: start + dur, Text: text})
	}
	// Untimed trailing punctuation/text belongs to the final syllable.
	if len(tokens) > 0 && prev < len(content) {
		tokens[len(tokens)-1].Text += content[prev:]
	}
	return tokens
}

// Keep the declared line start, including a lead-in before the first word.
// Expand it only when a word actually starts earlier than its line header.
func resolveLineStartFromTokens(line tokenLine) int {
	start := line.Start
	for _, tk := range line.Tokens {
		if tk.Start >= 0 && tk.End >= tk.Start {
			start = min(start, tk.Start)
		}
	}
	return start
}

// resolveLineEnd returns when a line stops being sung: the later of its
// declared end and its last word end. Only a line with no usable timing falls
// back to the next line's start, then to a 3s default. The next line's start is
// not used otherwise: overlapping lines (duets, background echoes) start before
// the current one ends, which would cut it short or even end it before it begins.
func resolveLineEnd(lines []tokenLine, idx int) int {
	line := lines[idx]
	start := resolveLineStartFromTokens(line)
	end := line.End
	for _, tk := range line.Tokens {
		end = max(end, tk.End)
	}
	if end > start {
		return end
	}
	if idx+1 < len(lines) {
		if next := resolveLineStartFromTokens(lines[idx+1]); next > start {
			return next
		}
	}
	return start + 3000
}

// tokenLineStarts returns each line's resolved start and plain text, the keys
// alignSideTrack matches translation/roma lines against.
func tokenLineStarts(lines []tokenLine) (starts []int, texts []string) {
	starts = make([]int, len(lines))
	texts = make([]string, len(lines))
	for i, line := range lines {
		starts[i] = resolveLineStartFromTokens(line)
		texts[i] = strings.TrimSpace(line.Text)
	}
	return starts, texts
}

func splitLines(s string) []string {
	s = strings.TrimPrefix(s, "\ufeff")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.Split(s, "\n")
}

func mustAtoi(s string) int {
	v, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return v
}
