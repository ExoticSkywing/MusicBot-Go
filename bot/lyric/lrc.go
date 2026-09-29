package lyric

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
)

// lrcEntry is one parsed LRC line: absolute time in seconds, its text, and a
// normalized "[mm:ss.cc]" tag. End is the next later timestamp in the track --
// blank timed lines included, since LRC marks where a line stops with one -- or
// Time+3s for the final line.
type lrcEntry struct {
	Time float64
	End  float64
	Text string
	Tag  string
}

var (
	// lrcTimeTagRe matches one leading "[mm:ss]", "[mm:ss.fff]" or "[mm:ss:fff]"
	// tag. A line may carry several ("[00:10.00][01:20.00]chorus").
	lrcTimeTagRe = regexp.MustCompile(`^\[(\d{1,3}):(\d{1,2})(?:[.:](\d{1,3}))?\]`)
	// lrcMetaRe matches "[ti:...]"/"[ar:...]"/"[by:...]" metadata lines.
	lrcMetaRe = regexp.MustCompile(`(?i)^\[(ti|ar|by)\s*:\s*(.*?)\]$`)
	// lrcHeaderTagRe matches any "[key:value]" header line ([ti:], [offset:],
	// [length:], [#:]...). Time tags never start with a letter or '#'.
	lrcHeaderTagRe = regexp.MustCompile(`^\[[A-Za-z#][^\]]*:[^\]]*\]$`)
	// lrcAnyTagRe matches any inline "[..:...]" timestamp (for stripping).
	lrcAnyTagRe = regexp.MustCompile(`\[[0-9]{1,3}:[0-9]{2}(?:[.:][0-9]{1,3})?\]`)
)

// parseLRCRow splits a row into the millisecond times of its leading time tags
// and the text after them. ok is false when the row has no leading time tag.
func parseLRCRow(row string) (times []int, text string, ok bool) {
	rest := strings.TrimSpace(row)
	for {
		m := lrcTimeTagRe.FindStringSubmatch(rest)
		if m == nil {
			break
		}
		times = append(times, (mustAtoi(m[1])*60+mustAtoi(m[2]))*1000+parseLRCFractionToMs(m[3]))
		rest = rest[len(m[0]):]
	}
	if len(times) == 0 {
		return nil, "", false
	}
	return times, strings.TrimSpace(rest), true
}

// parseLRCEntries parses an LRC track into time-sorted entries, dropping empty
// lines. A row with several time tags yields one entry per tag. Mirrors
// LyricConverterService::parseLrcEntries.
func parseLRCEntries(lrc string) []lrcEntry {
	rows := splitLines(lrc)
	entries := make([]lrcEntry, 0, len(rows))
	var stamps []float64 // every timestamp, blank lines included
	for _, row := range rows {
		times, text, ok := parseLRCRow(row)
		if !ok {
			continue
		}
		for _, ms := range times {
			sec := float64(ms) / 1000.0
			stamps = append(stamps, sec)
			if text == "" {
				continue
			}
			entries = append(entries, lrcEntry{Time: sec, Text: text, Tag: formatLRCTagFromMs(ms, 2)})
		}
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Time < entries[j].Time })
	sort.Float64s(stamps)
	for i := range entries {
		entries[i].End = entries[i].Time + 3.0
		// The first stamp strictly after this line; lines sharing a timestamp
		// (bilingual LRC) do not end each other.
		if k := sort.SearchFloat64s(stamps, entries[i].Time+0.0005); k < len(stamps) {
			entries[i].End = stamps[k]
		}
	}
	return entries
}

// lrcEntryStarts returns each entry's start in ms and its text, the keys
// alignSideTrack matches translation/roma lines against.
func lrcEntryStarts(entries []lrcEntry) (starts []int, texts []string) {
	starts = make([]int, len(entries))
	texts = make([]string, len(entries))
	for i, e := range entries {
		starts[i] = int(math.Round(e.Time * 1000))
		texts[i] = e.Text
	}
	return starts, texts
}

// sideEntry is one timed translation/roma line.
type sideEntry struct {
	Ms   int
	Text string
}

const (
	// sideExactToleranceMs is how far apart two timestamps may be and still
	// count as "the same": one track rounding to centiseconds while another
	// truncates (QQ's QRC line at 1547ms vs its translation's [00:01.54]), or
	// one keeping milliseconds, differs by under 10ms.
	sideExactToleranceMs = 10
	// sideFuzzyToleranceMs bounds how far a side-track line may sit from the
	// main line it is attached to when timestamps do not agree.
	sideFuzzyToleranceMs = 500
)

// parseSideEntries parses a translation/roma LRC track into time-sorted
// entries, skipping empty lines and NetEase/QQ's "//" placeholders.
func parseSideEntries(track string) []sideEntry {
	var entries []sideEntry
	for _, row := range splitLines(track) {
		times, text, ok := parseLRCRow(row)
		if !ok || text == "" || text == "//" {
			continue
		}
		for _, ms := range times {
			entries = append(entries, sideEntry{Ms: ms, Text: text})
		}
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Ms < entries[j].Ms })
	return entries
}

// alignSideTrack attaches side-track lines to main lines, returning one text
// per main line ("" when none). starts are the main lines' start times in ms,
// texts their plain text.
//
// Lines whose timestamps agree (within sideExactToleranceMs) pair first. Each
// remaining side line then goes to the main line nearest it, provided that line
// is still free, within sideFuzzyToleranceMs, and not a credit line -- so a line
// with no translation never borrows its neighbour's, and no side line is used
// twice.
func alignSideTrack(track string, starts []int, texts []string) []string {
	out := make([]string, len(starts))
	side := parseSideEntries(track)
	if len(side) == 0 || len(starts) == 0 {
		return out
	}
	used := make([]bool, len(side))
	type candidate struct{ main, side, distance int }
	var exact []candidate
	for i, ms := range starts {
		for j, e := range side {
			if diff := absInt(e.Ms - ms); diff <= sideExactToleranceMs {
				exact = append(exact, candidate{i, j, diff})
			}
		}
	}
	// Reserve closer matches first, including exact equality: an earlier
	// line 5ms away must not consume a later line's exact translation.
	sort.SliceStable(exact, func(i, j int) bool { return exact[i].distance < exact[j].distance })
	for _, c := range exact {
		if out[c.main] == "" && !used[c.side] {
			out[c.main] = side[c.side].Text
			used[c.side] = true
		}
	}
	for j, e := range side {
		if used[j] {
			continue
		}
		best := -1
		for i, ms := range starts {
			if best < 0 || absInt(ms-e.Ms) < absInt(starts[best]-e.Ms) {
				best = i
			}
		}
		if best < 0 || absInt(starts[best]-e.Ms) > sideFuzzyToleranceMs || out[best] != "" || isCreditLikeLine(texts[best]) {
			continue
		}
		out[best] = e.Text
		used[j] = true
	}
	return out
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// --- time formatting helpers ---

// parseLRCFractionToMs interprets a fractional string as milliseconds,
// right-padding to 3 digits ("4" -> 400, "43" -> 430). Mirrors
// parseLrcFractionToMilliseconds.
func parseLRCFractionToMs(fraction string) int {
	if fraction == "" {
		fraction = "0"
	}
	padded := (fraction + "000")[:3]
	return mustAtoi(padded)
}

// msToRoundedCentis converts milliseconds to centiseconds with +5 rounding.
func msToRoundedCentis(ms int) int {
	if ms < 0 {
		ms = 0
	}
	return (ms + 5) / 10
}

// formatLRCTagFromParts builds a centisecond-precision "[mm:ss.cc]" tag.
func formatLRCTagFromParts(minutes, seconds, milliseconds int) string {
	totalMs := (minutes*60+seconds)*1000 + milliseconds
	if totalMs < 0 {
		totalMs = 0
	}
	centis := msToRoundedCentis(totalMs)
	min := centis / 6000
	sec := (centis % 6000) / 100
	cs := centis % 100
	return fmt.Sprintf("[%02d:%02d.%02d]", min, sec, cs)
}

// formatLRCTagFromMs builds an LRC tag from absolute milliseconds. precision>=3
// yields millisecond "[mm:ss.fff]"; otherwise centisecond "[mm:ss.cc]".
func formatLRCTagFromMs(ms, precision int) string {
	if ms < 0 {
		ms = 0
	}
	if precision >= 3 {
		secAll := ms / 1000
		return fmt.Sprintf("[%02d:%02d.%03d]", secAll/60, secAll%60, ms%1000)
	}
	return formatLRCTagFromParts(0, 0, ms)
}

// secondsToSRTTime formats seconds as "HH:MM:SS,mmm".
func secondsToSRTTime(seconds float64) string {
	ms := int(math.Round(seconds * 1000))
	h := ms / 3600000
	ms -= h * 3600000
	m := ms / 60000
	ms -= m * 60000
	s := ms / 1000
	ms -= s * 1000
	return fmt.Sprintf("%02d:%02d:%02d,%03d", h, m, s, ms)
}

// secondsToTTMLTime formats seconds as TTML clock-time, dropping the hour field
// when zero ("mm:ss.fff" or "HH:MM:SS.fff").
func secondsToTTMLTime(seconds float64) string {
	if seconds < 0 {
		seconds = 0
	}
	ms := int(math.Round(seconds * 1000))
	totalSeconds := ms / 1000
	hours := totalSeconds / 3600
	remain := totalSeconds - hours*3600
	minutes := remain / 60
	secs := remain % 60
	millis := ms % 1000
	if hours > 0 {
		return fmt.Sprintf("%02d:%02d:%02d.%03d", hours, minutes, secs, millis)
	}
	return fmt.Sprintf("%02d:%02d.%03d", minutes, secs, millis)
}

// secondsToASSTime formats seconds as "H:MM:SS.cc".
func secondsToASSTime(seconds float64) string {
	centis := int(math.Round(seconds * 100))
	h := centis / 360000
	centis -= h * 360000
	m := centis / 6000
	centis -= m * 6000
	s := centis / 100
	centis -= s * 100
	return fmt.Sprintf("%d:%02d:%02d.%02d", h, m, s, centis)
}

// formatSplLineModeTag formats seconds as a line-mode SPL tag "[mm:ss.cc]".
func formatSplLineModeTag(seconds float64) string {
	ms := int(math.Round(seconds * 1000))
	centis := msToRoundedCentis(ms)
	return fmt.Sprintf("[%02d:%02d.%02d]", centis/6000, (centis%6000)/100, centis%100)
}

// formatSplTimestamp formats ms as an SPL word "<mm:ss.cc>" or line "[mm:ss.cc]" tag.
func formatSplTimestamp(ms int, word bool) string {
	if ms < 0 {
		ms = 0
	}
	centis := msToRoundedCentis(ms)
	min := centis / 6000
	sec := (centis % 6000) / 100
	cs := centis % 100
	if word {
		return fmt.Sprintf("<%02d:%02d.%02d>", min, sec, cs)
	}
	return fmt.Sprintf("[%02d:%02d.%02d]", min, sec, cs)
}
