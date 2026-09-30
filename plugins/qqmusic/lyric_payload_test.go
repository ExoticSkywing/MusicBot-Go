package qqmusic

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"strings"
	"testing"
)

type lyricResponseTransport string

func (body lyricResponseTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(body))), Request: r}, nil
}

func TestQRCResponseNumericTrackTimestamps(t *testing.T) {
	c := &Client{httpClient: &http.Client{Transport: lyricResponseTransport(`{"req_1":{"code":0,"data":{"qrc":0,"lyric":"WzAwOjAxLjAwXWhlbGxv","roma_t":123,"trans_t":456,"qrc_t":789}}}`)}}
	got, err := c.GetLyricsQRC(context.Background(), "test")
	if err != nil {
		t.Fatal(err)
	}
	if got.Lyric != "[00:01.00]hello" || got.Roma != "" {
		t.Fatalf("response = %+v", got)
	}
}

func TestDecodeLyricPayloadFormats(t *testing.T) {
	const lrc = "[00:01.00]hello"
	const qrc = `<QrcInfos><Lyric_1 LyricType="1" LyricContent="[1000,500]hello(1000,500)"/></QrcInfos>`
	for name, input := range map[string]string{
		"plain":          lrc,
		"base64":         base64.StdEncoding.EncodeToString([]byte(lrc)),
		"xml":            qrc,
		"base64_xml":     base64.StdEncoding.EncodeToString([]byte(qrc)),
		"line_timed_xml": `<QrcInfos><Lyric_1 LyricContent="[00:01.00]hello"/></QrcInfos>`,
	} {
		t.Run(name, func(t *testing.T) {
			if got := decodeLyricPayload(input); got != lrc {
				t.Fatalf("decoded = %q", got)
			}
		})
	}
}
