package lyric

import (
	"encoding/base64"
	"encoding/xml"
	"io"
	"reflect"
	"strings"
	"testing"
)

func TestMixedTTMLContent(t *testing.T) {
	raw := `<tt xmlns:ttm="http://www.w3.org/ns/ttml#metadata"><body><div><div><p begin="1000ms" end="3s">Say <span><span begin="1.2s" end="1.5s">hello</span> <span begin="1.5s" dur="500ms">world</span></span>!<span ttm:role="x-translation">翻译</span><span ttm:role="x-bg"><span begin="1s" end="2s">echo</span></span></p></div></div></body></tt>`
	p := Payload{RawTTML: raw}
	if got := Convert(p, "txt", Options{}); got != "Say hello world!" {
		t.Fatalf("text = %q", got)
	}
	lines := parseTokenLines(ttmlToTokenTrack(raw))
	if len(lines) != 1 || lines[0].Start != 1000 || lines[0].End != 3000 {
		t.Fatalf("lines = %+v", lines)
	}
	if got := lines[0].Tokens[2]; got.Text != "world" || got.Start != 1500 || got.End != 2000 {
		t.Fatalf("word = %+v", got)
	}
	if !HasWordTiming(p) {
		t.Fatal("nested word spans not recognized")
	}
	if HasWordTiming(Payload{RawTTML: `<tt><body><p begin="1s" end="2s"><span>Hello</span> world</p></body></tt>`}) {
		t.Fatal("untimed span reported as word timing")
	}
	if got := Convert(Payload{RawTTML: `<tt><body><p begin="1s" end="2s"><span>Hello</span> world</p></body></tt>`}, "txt", Options{}); got != "Hello world" {
		t.Fatalf("line text = %q", got)
	}
}

func TestTTMLTimeUnits(t *testing.T) {
	for input, want := range map[string]int{"1500ms": 1500, "1.5s": 1500, "1.5m": 90000, "0.5h": 1800000, "01:02.345": 62345, "01:02:03.456": 3723456, "-2s": 0, "NaNs": 0, "abc": 0} {
		if got := ttmlTimeToMs(input); got != want {
			t.Errorf("%s = %d; want %d", input, got, want)
		}
	}
}

func TestTTMLRoundTripSpacesAndOverlap(t *testing.T) {
	raw := "[1000,3000](1000,500,0)Hello (1500,500,0)world\n[2500,500](2500,500,0)other"
	out := Convert(Payload{RawYRC: raw}, "ttml", Options{})
	d := xml.NewDecoder(strings.NewReader(out))
	for {
		_, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	lines := parseTokenLines(ttmlToTokenTrack(out))
	if len(lines) != 2 || lines[0].End != 4000 || lines[0].Text != "Hello world" {
		t.Fatalf("round trip = %+v", lines)
	}
	compact := compactTTMLForAmjson(out)
	if !strings.Contains(compact, "</span> <span") {
		t.Fatal("compaction removed inter-word space")
	}
}

func TestKRCExportRoundTrip(t *testing.T) {
	yes := true
	p := Payload{RawYRC: sampleYRC, Translation: "[00:01.00]你好世界\n[00:03.00]测试", Roma: "[00:01.00]hello world"}
	file := Convert(p, "krc", Options{IncludeTranslation: &yes, IncludeRoma: true})
	if !strings.HasPrefix(file, "krc1") {
		t.Fatal("missing binary KRC signature")
	}
	text, err := DecodeKRC(base64.StdEncoding.EncodeToString([]byte(file)))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "[1000,2000]<0,500,0>Hello <500,500,0>world") {
		t.Fatalf("wrong relative timing: %q", text)
	}
	result := ParseKRC(text)
	if got := parseTokenLines(result.RawQRC); !reflect.DeepEqual(got, parseTokenLines(sampleYRC)) {
		t.Fatalf("round trip = %+v", got)
	}
	if result.Translation != p.Translation || result.Roma != p.Roma {
		t.Fatalf("side tracks = %+v", result)
	}
	for _, p := range []Payload{{Lyric: "[00:01.00]line\n[00:02.00]"}, {RawYRC: sampleYRC, Translation: "[00:01.00]hidden"}} {
		text, err := DecodeKRC(base64.StdEncoding.EncodeToString([]byte(Convert(p, "krc", Options{}))))
		if err != nil || strings.Contains(text, "[language:") {
			t.Fatalf("KRC fallback/options: %q %v", text, err)
		}
	}
	if _, err := DecodeKRC(base64.StdEncoding.EncodeToString([]byte("nopegarbage"))); err == nil {
		t.Fatal("accepted invalid KRC header")
	}
}

func TestTokenDetectionAndFallback(t *testing.T) {
	for _, input := range []string{"\ufeff[ti:Song]\n[1000,1000]Hello (1000,500)world(1500,500)", "[4]Hello (1000,500)world(1500,500)"} {
		p := Payload{Lyric: input}
		if !HasWordTiming(p) {
			t.Fatal("word timing not detected")
		}
		if got := Convert(p, "lrc", Options{}); got != "[00:01.00]Hello world" {
			t.Fatalf("lrc = %q", got)
		}
		if got := Convert(p, "raw", Options{}); got != input {
			t.Fatal("raw changed")
		}
	}
	p := Payload{RawYRC: "invalid", RawQRC: "[1000,1000](Oh (1000,500)yeah)(1500,500)!"}
	if got := Convert(p, "txt", Options{}); got != "(Oh yeah)!" {
		t.Fatalf("fallback/parentheses = %q", got)
	}
}

func TestQRCXMLVariants(t *testing.T) {
	for _, input := range []string{
		`<Lyric_1 LyricContent='[1000,500]say &quot;hi&quot;(1000,500)' LyricType="1"/>`,
		`<Lyric_1 LyricType="1" LyricContent="[1000,500]say "hi"(1000,500)"/>`,
	} {
		if got := ExtractQRCLyricContent(input); got != `[1000,500]say "hi"(1000,500)` {
			t.Fatalf("content = %q", got)
		}
	}
	if got := ExtractQRCLyricContent(`<Lyric_1 LyricContent="a&#10;b&amp;quot;"/>`); got != "a\nb&quot;" {
		t.Fatalf("entity decoding = %q", got)
	}
}

func TestLRCRepeatedLinesAndBlankEnds(t *testing.T) {
	input := "[ti:Song]\n[00:01.00][00:05.000]chorus\n[00:02.00]\n[00:03.00]verse\n[00:06.00]"
	entries := parseLRCEntries(input)
	if len(entries) != 3 || entries[0].End != 2 || entries[1].End != 5 || entries[2].End != 6 {
		t.Fatalf("entries = %+v", entries)
	}
	yes := true
	got := Convert(Payload{Lyric: input, Translation: "[00:01.000][00:05.00]副歌"}, "lrc", Options{IncludeTranslation: &yes})
	if strings.Count(got, "副歌") != 2 {
		t.Fatalf("merge = %q", got)
	}
	if got := Convert(Payload{Lyric: input}, "txt", Options{}); strings.Contains(got, "[") {
		t.Fatalf("metadata/tags leaked: %q", got)
	}
	if got := Convert(Payload{Lyric: input}, "lqe", Options{}); !strings.Contains(got, "[lyrics: format@lrc,") {
		t.Fatal("LQE mislabels LRC as LYS")
	}
}

func TestSideTrackDoesNotBorrowTranslation(t *testing.T) {
	exact := alignSideTrack("[00:01.005]exact", []int{1000, 1005}, []string{"a", "b"})
	if !reflect.DeepEqual(exact, []string{"", "exact"}) {
		t.Fatalf("exact match was stolen: %#v", exact)
	}
	got := alignSideTrack("[00:01.000]one\n[00:01.500]two", []int{1000, 1200, 1500}, []string{"a", "b", "c"})
	if !reflect.DeepEqual(got, []string{"one", "", "two"}) {
		t.Fatalf("alignment = %#v", got)
	}
	got = alignSideTrack("[00:01.300]text", []int{1000, 3000}, []string{"作词：甲", "song"})
	if !reflect.DeepEqual(got, []string{"", ""}) {
		t.Fatalf("credit alignment = %#v", got)
	}
}

func TestShortSubtitleEndsAtBlankMarker(t *testing.T) {
	p := Payload{Lyric: "[00:01.000]short\n[00:01.100]\n[00:02.000]next"}
	if got := Convert(p, "srt", Options{}); !strings.Contains(got, "00:00:01,000 --> 00:00:01,100") {
		t.Fatalf("SRT = %q", got)
	}
	if got := Convert(p, "ass", Options{}); !strings.Contains(got, "0:00:01.00,0:00:01.10,") {
		t.Fatalf("ASS = %q", got)
	}
}

func TestKaraokeGapAndLeadIn(t *testing.T) {
	raw := "[1000,4000](1500,500,0)A(3000,500,0)B"
	p := Payload{RawYRC: raw}
	if got := Convert(p, "ass", Options{}); !strings.Contains(got, `{\k50}{\k50}A{\k100}{\k50}B`) {
		t.Fatalf("ASS gaps = %q", got)
	}
	if got := Convert(p, "spl", Options{}); got != "[00:01.00]<00:01.50>A<00:02.00><00:03.00>B<00:03.50>[00:05.00]" {
		t.Fatalf("SPL = %q", got)
	}
	if got := Convert(p, "elrc", Options{}); !strings.HasSuffix(got, "B<00:03.500>") {
		t.Fatalf("ELRC final boundary = %q", got)
	}
}

func TestReviewSideTrackAndSubtitleBoundaries(t *testing.T) {
	p := Payload{Lyric: "[00:01.00][00:05.00]歌", Roma: "[00:01.000][00:05.000]uta"}
	if got := Convert(p, "lrc", Options{IncludeRoma: true}); strings.Count(got, "uta") != 2 {
		t.Fatalf("repeated roma = %q", got)
	}
	if got := Convert(Payload{Translation: "[123:01.00]译文\n[123:02.00][123:03.00]//"}, "trans", Options{}); got != "[123:01.00]译文" {
		t.Fatalf("long translation = %q", got)
	}
	p = Payload{RawQRC: "[1001,150]短(1001,150)\n[3000,500]句(3000,500)"}
	if got := Convert(p, "srt", Options{}); !strings.Contains(got, "00:00:01,001 --> 00:00:01,151") {
		t.Fatalf("token SRT lost boundaries: %q", got)
	}
	p = Payload{Lyric: "[00:01.00]短\n[00:01.20]\n[00:03.00]句"}
	if got := Convert(p, "spl", Options{}); !strings.Contains(got, "[00:01.00]短[00:01.20]") {
		t.Fatalf("SPL lost blank end: %q", got)
	}
	p = Payload{RawYRC: "[1000,3000](1000,500,0)A(3000,500,0)B"}
	if got := Convert(p, "elrc", Options{}); !strings.Contains(got, "A<00:01.500><00:03.000>B") {
		t.Fatalf("ELRC lost gap: %q", got)
	}
}

func TestReviewInvalidNativeFallbackAndWordTiming(t *testing.T) {
	p := Payload{RawYRC: "invalid", RawQRC: "[1000,500]hello(1000,500)"}
	if got := Convert(p, "yrc", Options{}); got != "[1000,500](1000,500,0)hello" {
		t.Fatalf("invalid native prevented fallback: %q", got)
	}
	for _, raw := range []string{"[1000,500]line only", "[1000,500]zero(1000,0)"} {
		if HasWordTiming(Payload{RawQRC: raw}) {
			t.Fatalf("reported word timing for %q", raw)
		}
	}
}

func TestReviewEchoKeepsSideTracks(t *testing.T) {
	p := Payload{RawQRC: "[1000,3000]say hello(1000,3000)\n[2000,500]hello(2000,500)", Translation: "[00:01.00]说你好\n[00:02.00]你好"}
	for _, format := range []string{"ttml", "amjson"} {
		got := Convert(p, format, Options{})
		if !strings.Contains(got, "L2") || !strings.Contains(got, "你好") {
			t.Fatalf("%s lost echo translation: %s", format, got)
		}
		if strings.Count(got, "<p ") != 2 {
			t.Fatalf("%s incorrectly folded translated echo", format)
		}
	}
}

func TestReviewWordFormatRoundTrips(t *testing.T) {
	const raw = "[1000,1000](1000,300,0)你好 (1500,500,0)世界！\n[3000,1200](3000,500,0)héllo (3500,700,0)world"
	want := parseTokenLines(raw)
	p := Payload{RawYRC: raw}
	for _, format := range []string{"qrc", "lys", "ttml", "krc"} {
		t.Run(format, func(t *testing.T) {
			out := Convert(p, format, Options{})
			switch format {
			case "ttml":
				out = ttmlToTokenTrack(out)
			case "krc":
				decoded, err := DecodeKRC(base64.StdEncoding.EncodeToString([]byte(out)))
				if err != nil {
					t.Fatal(err)
				}
				out = ParseKRC(decoded).RawQRC
			}
			if got := parseTokenLines(out); !reflect.DeepEqual(got, want) {
				t.Fatalf("round trip = %+v; want %+v", got, want)
			}
		})
	}
}
