package all_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/liuran001/MusicBot-Go/bot/config"
	"github.com/liuran001/MusicBot-Go/bot/download"
	logpkg "github.com/liuran001/MusicBot-Go/bot/logger"
	"github.com/liuran001/MusicBot-Go/bot/platform"
	platformplugins "github.com/liuran001/MusicBot-Go/bot/platform/plugins"
	"go.senan.xyz/taglib"
	"gopkg.in/ini.v1"
)

// TestLiveConfiguredDownloads reads credentials only from an explicitly supplied
// server-side config. Factories receive private copies with background renewal
// disabled; no bot, database, scheduler or Telegram delivery is started.
// Run the test binary in a temporary container with production files mounted
// read-only and ffprobe available. Media and config copies are deleted by TempDir.
func TestLiveConfiguredDownloads(t *testing.T) {
	source := os.Getenv("MUSICBOT_LIVE_CONFIG")
	if source == "" {
		t.Skip("set MUSICBOT_LIVE_CONFIG to opt in to authenticated live downloads")
	}
	selected := "," + os.Getenv("MUSICBOT_LIVE_PLATFORMS") + ","
	for _, tc := range []struct{ name, query string }{
		{"kugou", "成都"}, {"migu", "成都"}, {"qianqian", "庄心妍"},
		{"fivesing", "成都"}, {"jamendo", "love"}, {"joox", "love"},
		{"spotify", "Mr. Brightside The Killers"}, {"applemusic", "Bad Guy Billie Eilish"},
		{"netease", "成都"}, {"qqmusic", "晴天"}, {"kuwo", "好运来"}, {"soda", "成都"},
	} {
		if selected != ",," && !strings.Contains(selected, ","+tc.name+",") {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			iniCfg, err := ini.Load(source)
			if err != nil {
				t.Fatal("LIVE: cannot read config")
			}
			var secrets []string
			for _, section := range iniCfg.Sections() {
				for _, key := range section.Keys() {
					if len(key.String()) > 8 {
						secrets = append(secrets, key.String())
					}
				}
				section.Key("auto_renew_enabled").SetValue("false")
				section.Key("concept_auto_refresh_enabled").SetValue("false")
			}
			// Use AAC's independent native decrypt path, avoiding the shared
			// single-session FairPlay wrapper used by production lossless jobs.
			iniCfg.Section("plugins.applemusic").Key("wrapper_host").SetValue("")
			wvd := iniCfg.Section("plugins.spotify").Key("wvd_path")
			if value := wvd.String(); value != "" && !filepath.IsAbs(value) {
				wvd.SetValue(filepath.Join(filepath.Dir(source), value))
			}
			copyPath := filepath.Join(dir, "config.ini")
			if err := iniCfg.SaveTo(copyPath); err != nil {
				t.Fatal("LIVE: cannot write private config copy")
			}
			if err := os.Chmod(copyPath, 0o600); err != nil {
				t.Fatal("LIVE: cannot protect config copy")
			}
			cfg, err := config.Load(copyPath)
			if err != nil {
				t.Fatal("LIVE: config validation failed")
			}
			logger, err := logpkg.NewWithSecrets("error", "json", false, secrets...)
			if err != nil {
				t.Fatal("LIVE: logger initialization failed")
			}
			defer logger.Close()
			factory, ok := platformplugins.Get(tc.name)
			if !ok {
				t.Fatal("LIVE: factory missing")
			}
			contribution, err := factory(cfg, logger)
			if err != nil || contribution == nil || contribution.Platform == nil {
				t.Fatal("LIVE: platform initialization failed")
			}
			p := contribution.Platform
			if closer, ok := p.(io.Closer); ok {
				defer closer.Close()
			}
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
			defer cancel()
			tracks, err := p.Search(ctx, tc.query, 3)
			if err != nil || len(tracks) == 0 {
				t.Fatalf("LIVE: search failed results=%d %s", len(tracks), liveErrorClass(err))
			}
			t.Logf("LIVE: search results=%d", len(tracks))
			service := download.NewDownloadService(download.DownloadServiceOptions{
				Timeout: 90 * time.Second, Proxy: cfg.GetString("DownloadProxy"),
				CheckMD5: true, MaxRetries: 1, EnableMultipart: true,
				MultipartConcurrency: 2, MultipartMinSize: 5 << 20,
			})
			for _, candidate := range tracks {
				track, err := p.GetTrack(ctx, candidate.ID)
				if err != nil || track == nil || track.Duration <= 0 {
					t.Logf("LIVE: metadata failed %s", liveErrorClass(err))
					continue
				}
				info, err := p.GetDownloadInfo(ctx, track.ID, platform.QualityHigh)
				if err != nil || info == nil {
					t.Logf("LIVE: media resolution failed %s", liveErrorClass(err))
					continue
				}
				if !regexp.MustCompile(`^[a-zA-Z0-9]{1,8}$`).MatchString(info.Format) || info.Size > 256<<20 {
					t.Log("LIVE: unsupported format or oversized sample")
					continue
				}
				media := filepath.Join(dir, "sample."+info.Format)
				n, err := service.Download(ctx, info, media, nil)
				if err != nil {
					t.Logf("LIVE: download failed %s", liveErrorClass(err))
					continue
				}
				actual, err := download.VerifyFullAudio(ctx, media, track.Duration)
				if err != nil {
					t.Logf("LIVE: completeness failed catalog=%s actual=%s %s", track.Duration, actual, liveErrorClass(err))
					// These selected fields contain media structure only, never
					// request URLs, credentials, tags or decryption keys.
					probe, probeErr := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-show_entries", "stream=codec_name,codec_type,sample_rate,duration:format=format_name,duration", "-of", "compact", media).Output()
					t.Logf("LIVE: media structure=%q probe_ok=%t bytes=%d", strings.TrimSpace(string(probe)), probeErr == nil, n)
					packets, _ := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-read_intervals", "%+0.1", "-select_streams", "a:0", "-show_entries", "packet=duration_time", "-of", "csv=p=0", media).Output()
					t.Logf("LIVE: initial packet durations=%q", strings.TrimSpace(string(packets)))
					continue
				}
				if err := taglib.WriteTags(media, map[string][]string{taglib.Title: {"MusicBot dependency smoke test"}}, 0); err != nil {
					t.Fatalf("LIVE: tag write failed %s", liveErrorClass(err))
				}
				tags, err := taglib.ReadTags(media)
				if err != nil || len(tags[taglib.Title]) != 1 || tags[taglib.Title][0] != "MusicBot dependency smoke test" {
					t.Fatal("LIVE: tag readback failed")
				}
				if _, err := download.VerifyFullAudio(ctx, media, track.Duration); err != nil {
					t.Fatal("LIVE: tagged audio failed completeness verification")
				}
				t.Logf("LIVE: full download and tag round-trip passed format=%s bytes=%d catalog=%s packets=%s", info.Format, n, track.Duration, actual)
				return
			}
			t.Fatal("LIVE: no full playable sample; download not verified")
		})
	}
}

// Do not print raw errors: upstream responses may contain cookies, bearer
// tokens, signed URLs or decrypted content keys. Only allow known error classes.
func liveErrorClass(err error) string {
	if err == nil {
		return "empty result"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, platform.ErrIncompleteAudio) {
		return "preview/incomplete audio rejected"
	}
	status := regexp.MustCompile(`(?i)(?:status(?: code)?|http|code)[=: ]+[0-9]{3,6}`).FindString(err.Error())
	return fmt.Sprintf("error_type=%T %s", err, status)
}
