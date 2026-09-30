package lyric

import (
	"encoding/xml"
	"math"
	"strconv"
	"strings"
)

// Keep text and elements in document order: xml:",chardata" plus []span
// separates them and loses spaces/punctuation between word spans.
type ttmlNode struct {
	Name                  string
	Begin, End, Dur, Role string
	Parts                 []ttmlPart
}

type ttmlPart struct {
	Text string
	Node *ttmlNode
}

func (n *ttmlNode) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	n.Name = start.Name.Local
	for _, a := range start.Attr {
		switch a.Name.Local {
		case "begin":
			n.Begin = a.Value
		case "end":
			n.End = a.Value
		case "dur":
			n.Dur = a.Value
		case "role":
			n.Role = a.Value
		}
	}
	for {
		t, err := d.Token()
		if err != nil {
			return err
		}
		switch v := t.(type) {
		case xml.CharData:
			n.Parts = append(n.Parts, ttmlPart{Text: string(v)})
		case xml.StartElement:
			child := new(ttmlNode)
			if err := d.DecodeElement(child, &v); err != nil {
				return err
			}
			n.Parts = append(n.Parts, ttmlPart{Node: child})
		case xml.EndElement:
			return nil
		}
	}
}

func ttmlPrimary(n *ttmlNode) bool {
	for _, role := range strings.Fields(strings.ToLower(n.Role)) {
		switch role {
		case "x-translation", "x-roman", "x-bg":
			return false
		}
	}
	return true
}

// Apple/AMLL lyric timestamps are absolute song times. Untimed wrappers
// inherit their enclosing interval; this is not a general TTML layout engine.
func ttmlInterval(n *ttmlNode, start, end int) (int, int) {
	if n.Begin != "" {
		start = ttmlTimeToMs(n.Begin)
	}
	if n.End != "" {
		end = ttmlTimeToMs(n.End)
	} else if n.Dur != "" {
		end = start + ttmlTimeToMs(n.Dur)
	}
	return start, max(start, end)
}

func ttmlParagraphs(input string) []*ttmlNode {
	var root ttmlNode
	if xml.Unmarshal([]byte(input), &root) != nil {
		return nil
	}
	var out []*ttmlNode
	var walk func(*ttmlNode, bool)
	walk = func(n *ttmlNode, body bool) {
		body = body || n.Name == "body"
		if body && n.Name == "p" {
			out = append(out, n)
			return
		}
		for _, part := range n.Parts {
			if part.Node != nil {
				walk(part.Node, body)
			}
		}
	}
	walk(&root, false)
	return out
}

// ttmlWords preserves mixed content and descends through grouping spans.
// Whitespace-only text nodes attach to the previous word without extending
// its timing. Formatting newlines between elements are ignored.
func ttmlWords(n *ttmlNode, start, end int, words *[]tokenWord) {
	if !ttmlPrimary(n) {
		return
	}
	start, end = ttmlInterval(n, start, end)
	for _, part := range n.Parts {
		if part.Node != nil {
			if part.Node.Name == "br" {
				if len(*words) > 0 {
					(*words)[len(*words)-1].Text += " "
				}
			} else {
				ttmlWords(part.Node, start, end, words)
			}
			continue
		}
		text := part.Text
		if text == "" {
			continue
		}
		if strings.TrimSpace(text) == "" {
			if !strings.ContainsAny(text, "\r\n") && len(*words) > 0 {
				(*words)[len(*words)-1].Text += text
			}
			continue
		}
		text = strings.ReplaceAll(strings.ReplaceAll(text, "\r", ""), "\n", " ")
		*words = append(*words, tokenWord{Start: start, End: end, Text: text})
	}
}

func ttmlToTokenTrack(input string) string {
	var lines []string
	for _, p := range ttmlParagraphs(input) {
		start, end := ttmlInterval(p, 0, 0)
		var words []tokenWord
		ttmlWords(p, start, end, &words)
		if len(words) == 0 {
			continue
		}
		words[0].Text = strings.TrimLeft(words[0].Text, " \t\r\n")
		words[len(words)-1].Text = strings.TrimRight(words[len(words)-1].Text, " \t\r\n")
		if p.Begin == "" {
			start = words[0].Start
		}
		for _, w := range words {
			end = max(end, w.End)
		}
		var sb strings.Builder
		sb.WriteString("[" + itoa(start) + "," + itoa(max0(end-start)) + "]")
		for _, w := range words {
			sb.WriteString(w.Text + "(" + itoa(w.Start) + "," + itoa(max0(w.End-w.Start)) + ")")
		}
		lines = append(lines, sb.String())
	}
	return strings.Join(lines, "\n")
}

func ttmlHasWordSpans(input string) bool {
	var has func(*ttmlNode) bool
	has = func(n *ttmlNode) bool {
		if !ttmlPrimary(n) {
			return false
		}
		if n.Name == "span" && n.Begin != "" && (n.End != "" || n.Dur != "") {
			s, e := ttmlInterval(n, 0, 0)
			var words []tokenWord
			ttmlWords(n, s, e, &words)
			for _, w := range words {
				if w.End > w.Start && strings.TrimSpace(w.Text) != "" {
					return true
				}
			}
		}
		for _, p := range n.Parts {
			if p.Node != nil && has(p.Node) {
				return true
			}
		}
		return false
	}
	for _, p := range ttmlParagraphs(input) {
		if has(p) {
			return true
		}
	}
	return false
}

// Accept Apple clock values and TTML offset units that do not require an
// external frame/tick rate. Invalid or negative values resolve to zero.
func ttmlTimeToMs(v string) int {
	v = strings.TrimSpace(v)
	for _, unit := range []struct {
		suffix string
		scale  float64
	}{
		{"ms", 1}, {"h", 3600000}, {"m", 60000}, {"s", 1000},
	} {
		if strings.HasSuffix(v, unit.suffix) {
			f, err := strconv.ParseFloat(strings.TrimSuffix(v, unit.suffix), 64)
			if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f < 0 || f*unit.scale >= float64(math.MaxInt) {
				return 0
			}
			return int(math.Round(f * unit.scale))
		}
	}
	parts := strings.Split(v, ":")
	if len(parts) > 3 {
		return 0
	}
	total := 0.0
	for i, part := range parts {
		f, err := strconv.ParseFloat(part, 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f < 0 {
			return 0
		}
		if i < len(parts)-1 && f != math.Trunc(f) {
			return 0
		}
		total = total*60 + f
	}
	if total*1000 >= float64(math.MaxInt) {
		return 0
	}
	return int(math.Round(total * 1000))
}
