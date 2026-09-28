package migu

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/liuran001/MusicBot-Go/plugins/musiclib/internal/model"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestChargeAuditionIsMetadataOnlyAndNeverDownloaded(t *testing.T) {
	item := MiguSongItem{
		ContentID: "123", SongName: "Song", ChargeAuditions: "1", OldChargeAuditions: "1",
		AuditionsType: "03", AuditionsLength: 60,
		RateFormats: []miguRateFormat{{FormatType: "PQ", ResourceType: "2", Size: "3200000", FileType: "mp3"}},
	}
	m := &Migu{ctx: context.Background()}
	if song := m.convertItemToSong(item); song != nil {
		t.Fatalf("audition exposed in anonymous results: %#v", song)
	}
	song := m.convertItemToSongAllowPaid(item)
	if song == nil || !song.IsVIP || song.Extra["preview_only"] != "true" {
		t.Fatalf("audition metadata lost: %#v", song)
	}
	if _, err := m.GetDownloadURL(song); !errors.Is(err, model.ErrPreviewOnly) {
		t.Fatalf("download error = %v, want ErrPreviewOnly", err)
	}
}

func TestOnlyUntaggedHQIsMarkedForUpgrade(t *testing.T) {
	formats := func(hqTags ...string) []miguRateFormat {
		return []miguRateFormat{
			{FormatType: "PQ", ResourceType: "2", Size: "4000000", FileType: "mp3"},
			{FormatType: "HQ", ResourceType: "2", Size: "10000000", FileType: "mp3", ShowTag: hqTags},
		}
	}
	m := &Migu{ctx: context.Background()}
	free := MiguSongItem{ContentID: "1", SongName: "Song", RateFormats: formats()}
	if song := m.convertItemToSong(free); song == nil || song.Extra["hq_available"] != "true" {
		t.Fatalf("untagged HQ not marked: %#v", song)
	}
	vip := MiguSongItem{ContentID: "2", SongName: "Song", RateFormats: formats("vip")}
	for _, song := range []*model.Song{m.convertItemToSong(vip), m.convertItemToSongAllowPaid(vip)} {
		if song == nil || song.Extra["hq_available"] != "" {
			t.Fatalf("VIP HQ marked for upgrade: %#v", song)
		}
	}
}

func TestDownloadUpgradesToUntaggedHQ(t *testing.T) {
	const pqURL = "https://cdn.example/x/MP3_128_16_Stero/song.mp3?Key=k"
	const hqURL = "https://cdn.example/x/MP3_320_16_Stero/song.mp3?Key=k"
	cases := []struct {
		name        string
		hqAvailable bool
		hqStatus    int
		wantURL     string
		wantBitrate int
		wantProbe   bool
	}{
		{"upgraded", true, http.StatusPartialContent, hqURL, 320, true},
		{"missing on CDN", true, http.StatusNotFound, pqURL, 128, true},
		{"VIP HQ", false, 0, pqURL, 128, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			probed := false
			client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				resp := &http.Response{Header: make(http.Header), Body: io.NopCloser(strings.NewReader("")), Request: req}
				switch {
				case strings.HasSuffix(req.URL.Path, "/listenSong.do"):
					resp.StatusCode = http.StatusFound
					resp.Header.Set("Location", pqURL)
				case req.URL.String() == hqURL:
					probed = true
					if req.Header.Get("Range") != "bytes=0-0" {
						t.Error("probe requested the whole rendition")
					}
					resp.StatusCode = tc.hqStatus
					resp.Header.Set("Content-Type", "audio/mpeg")
				default:
					t.Fatalf("unexpected request: %s", req.URL)
				}
				return resp, nil
			})}
			extra := map[string]string{"content_id": "123", "resource_type": "E", "format_type": "SQ"}
			if tc.hqAvailable {
				extra["hq_available"] = "true"
			}
			song := &model.Song{Source: "migu", ID: "123|E|SQ", Ext: "flac", Extra: extra}
			got, err := (&Migu{ctx: context.Background(), client: client}).GetDownloadURL(song)
			if err != nil || got != tc.wantURL || song.Ext != "mp3" || song.Bitrate != tc.wantBitrate || probed != tc.wantProbe {
				t.Fatalf("url=%s ext=%s bitrate=%d probed=%v err=%v", got, song.Ext, song.Bitrate, probed, err)
			}
		})
	}
}

func TestDownloadRejectsPreviewRedirect(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusFound,
			Header:     http.Header{"Location": []string{"https://cdn.example/audition/song.mp3"}},
			Body:       io.NopCloser(strings.NewReader("")),
			Request:    req,
		}, nil
	})}
	m := &Migu{ctx: context.Background(), client: client}
	song := &model.Song{Source: "migu", ID: "123|2|PQ", Extra: map[string]string{"content_id": "123", "resource_type": "2", "format_type": "PQ"}}
	if _, err := m.GetDownloadURL(song); !errors.Is(err, model.ErrPreviewOnly) {
		t.Fatalf("preview redirect error = %v, want ErrPreviewOnly", err)
	}
}
