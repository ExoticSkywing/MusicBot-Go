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
