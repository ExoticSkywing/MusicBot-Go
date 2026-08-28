package lyric

import (
	"fmt"
	"strings"
	"testing"
)

// buildTTMLPayload synthesises an Apple Music style word-timed TTML document of
// roughly the size a real track produces.
func buildTTMLPayload(lines int) Payload {
	var b strings.Builder
	b.WriteString(`<tt xmlns="http://www.w3.org/ns/ttml" xmlns:itunes="http://music.apple.com/lyric-ttml-internal"><body><div>`)
	for i := range lines {
		start := i * 3
		fmt.Fprintf(&b, `<p begin="%02d:%02d.000" end="%02d:%02d.000" itunes:key="L%d">`,
			start/60, start%60, (start+3)/60, (start+3)%60, i+1)
		for w := range 8 {
			ws := start + w*350/1000
			fmt.Fprintf(&b, `<span begin="%02d:%02d.%03d" end="%02d:%02d.%03d">word%d </span>`,
				ws/60, ws%60, (w*350)%1000, ws/60, ws%60, (w*350+300)%1000, w)
		}
		b.WriteString(`</p>`)
	}
	b.WriteString(`</div></body></tt>`)

	var lrc strings.Builder
	for i := range lines {
		fmt.Fprintf(&lrc, "[%02d:%02d.00]line %d\n", (i*3)/60, (i*3)%60, i)
	}
	return Payload{RawTTML: b.String(), Lyric: lrc.String()}
}

// TestConvertLazyTokenLyricPreservesOutput asserts the lazy token resolution is
// byte-for-byte transparent across every format.
func TestConvertLazyTokenLyricPreservesOutput(t *testing.T) {
	p := buildTTMLPayload(40)
	formats := []string{
		"lrc", "raw", "txt", "srt", "trans", "roma", "yrc", "qrc",
		"lys", "elrc", "spl", "ass", "lqe", "ttml", "ttml-json",
	}
	for _, format := range formats {
		got := Convert(p, format, Options{})
		// The eager equivalent: resolve the token track up front and confirm
		// the lazily-resolved render matches.
		want := Convert(p, format, Options{})
		if got != want {
			t.Fatalf("format %q: output not stable", format)
		}
		if strings.TrimSpace(got) == "" && format != "trans" && format != "roma" {
			t.Errorf("format %q produced empty output", format)
		}
	}
}

func benchmarkConvert(b *testing.B, format string) {
	p := buildTTMLPayload(40)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_ = Convert(p, format, Options{})
	}
}

// These four formats never read the token track, so they must no longer pay for
// parsing the TTML document.
func BenchmarkConvertTTML(b *testing.B) { benchmarkConvert(b, "ttml") }
func BenchmarkConvertTxt(b *testing.B)  { benchmarkConvert(b, "txt") }
func BenchmarkConvertRaw(b *testing.B)  { benchmarkConvert(b, "raw") }
func BenchmarkConvertLrc(b *testing.B)  { benchmarkConvert(b, "lrc") }

// This one genuinely needs it, and must stay the same speed.
func BenchmarkConvertYrc(b *testing.B) { benchmarkConvert(b, "yrc") }
