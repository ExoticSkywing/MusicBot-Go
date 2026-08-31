//go:build live

package soda

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/liuran001/MusicBot-Go/bot/httpproxy"
	"github.com/liuran001/MusicBot-Go/bot/platform"
)

// TestLiveSodaPCFlow is intentionally opt-in. It verifies the real signer,
// account, search, track, and player-info chain without downloading audio.
func TestLiveSodaPCFlow(t *testing.T) {
	if os.Getenv("SODA_LIVE_TEST") != "1" {
		t.Skip("set SODA_LIVE_TEST=1 to run the Soda production smoke test")
	}
	cookie := strings.TrimSpace(os.Getenv("SODA_LIVE_COOKIE"))
	signerURL := strings.TrimSpace(os.Getenv("SODA_LIVE_SIGNER_URL"))
	proxyHost := strings.TrimSpace(os.Getenv("SODA_LIVE_PROXY_HOST"))
	proxyPort, _ := strconv.Atoi(strings.TrimSpace(os.Getenv("SODA_LIVE_PROXY_PORT")))
	if cookie == "" || signerURL == "" || proxyHost == "" || proxyPort <= 0 {
		t.Fatal("SODA_LIVE_COOKIE, SODA_LIVE_SIGNER_URL, SODA_LIVE_PROXY_HOST, and SODA_LIVE_PROXY_PORT are required")
	}

	client := NewClient(cookie, 25*time.Second, nil)
	if err := client.SetAPIProxy(httpproxy.Config{
		Enabled: true,
		Type:    firstNonEmptyString(os.Getenv("SODA_LIVE_PROXY_TYPE"), "http"),
		Host:    proxyHost,
		Port:    proxyPort,
		Auth:    strings.TrimSpace(os.Getenv("SODA_LIVE_PROXY_AUTH")),
	}); err != nil {
		t.Fatalf("SetAPIProxy() error = %v", err)
	}
	if err := client.SetBDMSSigner(signerURL, strings.TrimSpace(os.Getenv("SODA_LIVE_SIGNER_TOKEN")), 10*time.Second); err != nil {
		t.Fatalf("SetBDMSSigner() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	profile, err := client.FetchSelfProfile(ctx)
	if err != nil {
		t.Fatalf("FetchSelfProfile() error = %v", err)
	}
	if profile == nil || strings.TrimSpace(profile.MyInfo.ID) == "" {
		t.Fatal("FetchSelfProfile() did not return the logged-in PC account")
	}

	tracks, err := client.Search(ctx, "周杰伦", 3)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(tracks) == 0 || strings.TrimSpace(tracks[0].ID) == "" {
		t.Fatal("Search() returned no tracks")
	}
	track, _, err := client.GetTrack(ctx, tracks[0].ID)
	if err != nil {
		t.Fatalf("GetTrack() error = %v", err)
	}
	if track == nil || track.ID != tracks[0].ID {
		t.Fatal("GetTrack() returned a mismatched track")
	}
	info, err := client.FetchDownloadInfo(ctx, tracks[0].ID, platform.QualityHigh)
	if err != nil {
		t.Fatalf("FetchDownloadInfo() error = %v", err)
	}
	if info == nil || strings.TrimSpace(info.URL) == "" || strings.TrimSpace(info.Headers["X-Soda-Play-Auth"]) == "" {
		t.Fatal("FetchDownloadInfo() returned incomplete player information")
	}
}
