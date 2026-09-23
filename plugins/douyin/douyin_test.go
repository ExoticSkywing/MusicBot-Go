package douyin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/liuran001/MusicBot-Go/bot/platform"
)

func TestURLMatcherMatchURL(t *testing.T) {
	matcher := NewURLMatcher()
	tests := []struct {
		url    string
		wantID string
		ok     bool
	}{
		{"https://www.douyin.com/music/7687226746303580947", "7687226746303580947", true},
		{"https://www.douyin.com/music/7687226746303580947/?previous_page=app_code_link", "7687226746303580947", true},
		{"https://www.iesdouyin.com/share/music/7687226746303580947?from_ssr=1", "7687226746303580947", true},
		{"https://m.douyin.com/share/music/7424808309091224357", "7424808309091224357", true},
		{"https://www.douyin.com/video/7463740532703907129", "", false},
		{"https://music.douyin.com/qishui/share/track?track_id=987654321", "", false},
		{"https://www.douyin.com/music/abc", "", false},
		{"https://fakedouyin.com/music/7687226746303580947", "", false},
	}
	for _, tt := range tests {
		gotID, gotOK := matcher.MatchURL(tt.url)
		if gotID != tt.wantID || gotOK != tt.ok {
			t.Fatalf("MatchURL(%q) = (%q,%v), want (%q,%v)", tt.url, gotID, gotOK, tt.wantID, tt.ok)
		}
	}
}

func TestTextMatcherMatchText(t *testing.T) {
	matcher := NewTextMatcher()
	tests := []struct {
		text   string
		wantID string
		ok     bool
	}{
		{"douyin:7687226746303580947", "7687226746303580947", true},
		{"抖音：7687226746303580947", "7687226746303580947", true},
		{"@小鱼儿嘤嘤创作的原声 https://www.douyin.com/music/7687226746303580947，快来听", "7687226746303580947", true},
		{"7687226746303580947", "", false},
		{"soda:7687226746303580947", "", false},
		{"https://www.douyin.com/video/7463740532703907129", "", false},
	}
	for _, tt := range tests {
		gotID, gotOK := matcher.MatchText(tt.text)
		if gotID != tt.wantID || gotOK != tt.ok {
			t.Fatalf("MatchText(%q) = (%q,%v), want (%q,%v)", tt.text, gotID, gotOK, tt.wantID, tt.ok)
		}
	}
}

// 精简自 2026-09 实测的 /aweme/v1/web/music/detail/ 响应。
const originalSoundResponse = `{"status_code":0,"msg":"success","music_info":{
	"id":7687226746303580947,"id_str":"7687226746303580947","mid":"7687226746303580947",
	"title":"@小鱼儿嘤嘤创作的原声","author":"小鱼儿嘤嘤","owner_nickname":"小鱼儿嘤嘤",
	"sec_uid":"MS4wLjABAAAAwZ0IZkEVZ22rfhY1bd_S3-eMlBJlnpEcsoKO_zGRtug",
	"duration":84,"is_original_sound":true,"is_pgc":false,"prevent_download":false,
	"cover_hd":{"uri":"1080x1080/aweme-avatar/x","url_list":["https://p3-pc.douyinpic.com/aweme/1080x1080/aweme-avatar/x.jpeg"]},
	"play_url":{"uri":"https://sf11-cdn-tos.douyinstatic.com/obj/ies-music/7687226786569964342.mp3","url_key":"7687226746303580947",
		"url_list":["https://sf11-cdn-tos.douyinstatic.com/obj/ies-music/7687226786569964342.mp3","https://sf6-cdn-tos.douyinstatic.com/obj/ies-music/7687226786569964342.mp3"]}
}}`

func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client := NewClient(server.Client(), 5*time.Second)
	client.detailURL = server.URL
	return client
}

func TestClientGetTrackAndDownloadInfo(t *testing.T) {
	var requests atomic.Int32
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if got := r.URL.Query().Get("music_id"); got != "7687226746303580947" {
			t.Errorf("music_id = %q", got)
		}
		if got := r.URL.Query().Get("aid"); got != douyinWebAid {
			t.Errorf("aid = %q", got)
		}
		_, _ = w.Write([]byte(originalSoundResponse))
	})
	ctx := context.Background()

	track, err := client.GetTrack(ctx, "7687226746303580947")
	if err != nil {
		t.Fatalf("GetTrack: %v", err)
	}
	if track.ID != "7687226746303580947" || track.Platform != platformName || track.Title != "@小鱼儿嘤嘤创作的原声" {
		t.Fatalf("unexpected track: %+v", track)
	}
	if track.Duration != 84*time.Second {
		t.Fatalf("duration = %v", track.Duration)
	}
	if track.URL != "https://www.douyin.com/music/7687226746303580947" || !strings.HasSuffix(track.CoverURL, ".jpeg") {
		t.Fatalf("unexpected links: url=%q cover=%q", track.URL, track.CoverURL)
	}
	if len(track.Artists) != 1 || track.Artists[0].Name != "小鱼儿嘤嘤" || !strings.HasPrefix(track.Artists[0].URL, "https://www.douyin.com/user/MS4w") {
		t.Fatalf("unexpected artists: %+v", track.Artists)
	}

	info, err := client.FetchDownloadInfo(ctx, "7687226746303580947", platform.QualityHiRes)
	if err != nil {
		t.Fatalf("FetchDownloadInfo: %v", err)
	}
	if info.URL != "https://sf11-cdn-tos.douyinstatic.com/obj/ies-music/7687226786569964342.mp3" {
		t.Fatalf("url = %q", info.URL)
	}
	if len(info.CandidateURLs) != 1 || !strings.Contains(info.CandidateURLs[0], "sf6-cdn-tos") {
		t.Fatalf("candidates = %v", info.CandidateURLs)
	}
	if info.Format != "mp3" || info.Quality != platform.QualityStandard || info.Bitrate != douyinNominalBitrate {
		t.Fatalf("unexpected info: %+v", info)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("detail requests = %d, want 1 (cached)", got)
	}
}

func TestClientRejectsUnavailableMusic(t *testing.T) {
	tests := []struct {
		name string
		body string
		want error
	}{
		{"not found", `{"status_code":0,"music_info":null}`, platform.ErrNotFound},
		{"pgc clip", `{"status_code":0,"music_info":{"id_str":"7000000000000000001","is_pgc":true,"duration":60,"play_url":{"url_list":["https://sf6-cdn-tos.douyinstatic.com/obj/ies-music/a.mp3"]}}}`, platform.ErrIncompleteAudio},
		{"download disabled", `{"status_code":0,"music_info":{"id_str":"7000000000000000001","prevent_download":true,"duration":60,"play_url":{"url_list":["https://sf6-cdn-tos.douyinstatic.com/obj/ies-music/a.mp3"]}}}`, platform.ErrUnavailable},
		{"no play url", `{"status_code":0,"music_info":{"id_str":"7000000000000000001","duration":60,"play_url":{"url_list":[]}}}`, platform.ErrUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(tt.body))
			})
			_, err := client.FetchDownloadInfo(context.Background(), "7000000000000000001", platform.QualityStandard)
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestClientReportsUpstreamErrors(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	})
	if _, err := client.GetTrack(context.Background(), "7687226746303580947"); !errors.Is(err, platform.ErrRateLimited) {
		t.Fatalf("err = %v, want rate limited", err)
	}

	client = newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status_code":8,"status_msg":"blocked"}`))
	})
	if _, err := client.GetTrack(context.Background(), "7687226746303580947"); err == nil || !strings.Contains(err.Error(), "status_code=8") {
		t.Fatalf("err = %v, want status_code error", err)
	}
}

func TestPlatformMetadata(t *testing.T) {
	p := NewPlatform(NewClient(nil, 0))
	if p.SupportsSearch() || !p.SupportsDownload() {
		t.Fatalf("capabilities = %+v", p.Capabilities())
	}
	if hosts := p.ShortLinkHosts(); len(hosts) != 1 || hosts[0] != "v.douyin.com" {
		t.Fatalf("short link hosts = %v", hosts)
	}
	if _, err := p.Search(context.Background(), "x", 1); !errors.Is(err, platform.ErrUnsupported) {
		t.Fatalf("search err = %v", err)
	}
}
