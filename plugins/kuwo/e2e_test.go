package kuwo

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/liuran001/MusicBot-Go/bot/download"
	"github.com/liuran001/MusicBot-Go/bot/platform"
)

const (
	kuwoE2ETrackID      = "41378936"
	kuwoE2EHiResTrackID = "7149583"
	kuwoE2EPaidID       = "228908"
	kuwoE2EPlaylistID   = "2952464073"
)

var kuwoE2EURLPattern = regexp.MustCompile(`https?://[^\s"'<>]+`)

func requireKuwoE2E(t *testing.T) {
	t.Helper()
	if os.Getenv("KUWO_E2E") != "1" {
		t.Skip("set KUWO_E2E=1 to run live Kuwo contract tests")
	}
}

func TestKuwoE2E(t *testing.T) {
	requireKuwoE2E(t)

	// The direct lossless and Hi-Res fixtures total about 105 MB. Keep this
	// opt-in live test tolerant of slow CDN routes while production remains
	// governed by the caller's own context.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	client := NewClient(30*time.Second, nil)
	kuwoPlatform := NewPlatform(client)
	service := download.NewDownloadService(download.DownloadServiceOptions{
		Timeout:              90 * time.Second,
		CheckMD5:             false,
		MaxRetries:           1,
		EnableMultipart:      true,
		MultipartConcurrency: 4,
		MultipartMinSize:     5 << 20,
	})
	tempDir := t.TempDir()

	var detail *trackDetail
	var losslessInfo *platform.DownloadInfo
	var losslessPath string
	var hiResDetail *trackDetail
	var hiResInfo *platform.DownloadInfo
	var hiResPath string
	var webInfo *platform.DownloadInfo
	var webPath string

	runKuwoE2EStage(t, "A_Search", func(t *testing.T) {
		tracks, err := client.Search(ctx, "好运来", 10)
		if err != nil {
			t.Fatalf("search failed: %s", redactKuwoE2EError(err))
		}
		if len(tracks) == 0 {
			t.Fatal("search returned no tracks")
		}

		var matched *platform.Track
		for index := range tracks {
			track := &tracks[index]
			if track.Platform != "kuwo" || !isASCIIUnsignedDecimal(track.ID, 20) {
				continue
			}
			if !strings.Contains(kuwoE2ETrackText(*track), "好运来") {
				continue
			}
			if strings.TrimSpace(track.URL) == "" {
				continue
			}
			roundTripID, ok := NewURLMatcher().MatchURL(track.URL)
			if !ok || roundTripID != track.ID {
				continue
			}
			matched = track
			break
		}
		if matched == nil {
			t.Fatalf("none of %d search results satisfied the Kuwo identity/title/URL contract", len(tracks))
		}
		t.Logf("search contract matched rid=%s", matched.ID)
	})

	runKuwoE2EStage(t, "B_Official_URL_And_Detail", func(t *testing.T) {
		matcher := NewURLMatcher()
		matchedID, ok := matcher.MatchURL("https://www.kuwo.cn/play_detail/" + kuwoE2ETrackID)
		if !ok || matchedID != kuwoE2ETrackID {
			t.Fatalf("official URL matcher returned rid=%q matched=%t", matchedID, ok)
		}

		var err error
		detail, _, err = client.getTrackDetail(ctx, matchedID)
		if err != nil {
			t.Fatalf("track detail failed: %s", redactKuwoE2EError(err))
		}
		if detail == nil {
			t.Fatal("track detail is nil")
		}
		if detail.ID != kuwoE2ETrackID || detail.Platform != "kuwo" {
			t.Fatalf("track identity = %q/%q, want %q/kuwo", detail.Platform, detail.ID, kuwoE2ETrackID)
		}
		if strings.TrimSpace(detail.Title) == "" || !strings.Contains(detail.Title, "好运来") {
			t.Fatalf("track title does not identify the expected song")
		}
		if detail.Duration < 180*time.Second || detail.Duration > 260*time.Second {
			t.Fatalf("track duration = %s, want 180s..260s", detail.Duration)
		}
		roundTripID, ok := matcher.MatchURL(detail.URL)
		if !ok || roundTripID != detail.ID {
			t.Fatalf("canonical track URL did not round-trip to rid=%s", detail.ID)
		}
		t.Logf("detail contract matched rid=%s duration=%s", detail.ID, detail.Duration)
	})

	runKuwoE2EStage(t, "C1_Direct_2000_Lossless_Probe", func(t *testing.T) {
		var err error
		losslessInfo, err = client.GetDownloadInfo(ctx, kuwoE2ETrackID, platform.QualityLossless)
		if err != nil {
			t.Fatalf("lossless download info failed: %s", redactKuwoE2EError(err))
		}
		assertKuwoE2EDownloadInfo(
			t,
			kuwoE2ETrackID,
			losslessInfo,
			"flac",
			platform.QualityLossless,
		)
		assertKuwoE2EDirectFLACInfo(t, losslessInfo, 20<<20, 50<<20)
		if losslessInfo.Bitrate < 700 || losslessInfo.Bitrate > 1800 {
			t.Fatalf(
				"verified direct lossless average bitrate = %d kbps, want 700..1800",
				losslessInfo.Bitrate,
			)
		}
		logKuwoE2EMedia(t, kuwoE2ETrackID, losslessInfo)
	})

	runKuwoE2EStage(t, "C2_Download_Direct_2000_Lossless", func(t *testing.T) {
		losslessPath = filepath.Join(tempDir, "kuwo-e2e-lossless.flac")
		assertKuwoE2EFullDownload(t, ctx, service, losslessInfo, losslessPath)
	})

	runKuwoE2EStage(t, "C3_Verify_Direct_2000_Lossless", func(t *testing.T) {
		streamInfo := assertKuwoE2ECleanFLAC(
			t,
			losslessPath,
			detail.Duration,
			44100,
			16,
		)
		if streamInfo.sampleRate != 44100 ||
			streamInfo.bitsPerSample != 16 ||
			streamInfo.channels != 2 {
			t.Fatalf(
				"lossless STREAMINFO sampleRate=%d bits=%d channels=%d",
				streamInfo.sampleRate,
				streamInfo.bitsPerSample,
				streamInfo.channels,
			)
		}
		assertKuwoE2EFFmpegDecode(t, ctx, losslessPath)
		t.Logf("direct lossless FLAC sha256=%x", kuwoE2EFileSHA256(t, losslessPath))
	})

	runKuwoE2EStage(t, "D1_Direct_4000_HiRes_Probe", func(t *testing.T) {
		var err error
		hiResDetail, _, err = client.getTrackDetail(ctx, kuwoE2EHiResTrackID)
		if err != nil {
			t.Fatalf("Hi-Res fixture detail failed: %s", redactKuwoE2EError(err))
		}
		hiResInfo, err = client.GetDownloadInfo(
			ctx,
			kuwoE2EHiResTrackID,
			platform.QualityHiRes,
		)
		if err != nil {
			t.Fatalf("Hi-Res download info failed: %s", redactKuwoE2EError(err))
		}
		assertKuwoE2EDownloadInfo(
			t,
			kuwoE2EHiResTrackID,
			hiResInfo,
			"flac",
			platform.QualityHiRes,
		)
		assertKuwoE2EDirectFLACInfo(t, hiResInfo, 60<<20, 100<<20)
		if hiResInfo.Bitrate < 2000 || hiResInfo.Bitrate > 5000 {
			t.Fatalf(
				"verified direct Hi-Res average bitrate = %d kbps, want 2000..5000",
				hiResInfo.Bitrate,
			)
		}
		logKuwoE2EMedia(t, kuwoE2EHiResTrackID, hiResInfo)
	})

	runKuwoE2EStage(t, "D2_Download_Direct_4000_HiRes", func(t *testing.T) {
		hiResPath = filepath.Join(tempDir, "kuwo-e2e-hires.flac")
		assertKuwoE2EFullDownload(t, ctx, service, hiResInfo, hiResPath)
	})

	runKuwoE2EStage(t, "D3_Verify_Direct_4000_HiRes", func(t *testing.T) {
		streamInfo := assertKuwoE2ECleanFLAC(
			t,
			hiResPath,
			hiResDetail.Duration,
			96000,
			24,
		)
		if streamInfo.sampleRate < 96000 ||
			streamInfo.bitsPerSample < 24 ||
			streamInfo.channels != 2 {
			t.Fatalf(
				"Hi-Res STREAMINFO sampleRate=%d bits=%d channels=%d",
				streamInfo.sampleRate,
				streamInfo.bitsPerSample,
				streamInfo.channels,
			)
		}
		assertKuwoE2EFFmpegDecode(t, ctx, hiResPath)
		t.Logf("direct Hi-Res FLAC sha256=%x", kuwoE2EFileSHA256(t, hiResPath))
	})

	runKuwoE2EStage(t, "E1_Mobile_320", func(t *testing.T) {
		info, err := client.GetDownloadInfo(ctx, kuwoE2ETrackID, platform.QualityHigh)
		if err != nil {
			t.Fatalf("mobile 320 download info failed: %s", redactKuwoE2EError(err))
		}
		assertKuwoE2EDownloadInfo(t, kuwoE2ETrackID, info, "mp3", platform.QualityHigh)
		if info.Bitrate < 256 || info.Bitrate > 384 {
			t.Fatalf("mobile 320 verified average bitrate = %d kbps, want 256..384", info.Bitrate)
		}
		logKuwoE2EMedia(t, kuwoE2ETrackID, info)
		mediaPath := filepath.Join(tempDir, "kuwo-e2e-mobile-320.mp3")
		assertKuwoE2EFullDownload(t, ctx, service, info, mediaPath)
		assertKuwoE2ELocalMPEG(t, mediaPath)
		assertKuwoE2EFFmpegDecode(t, ctx, mediaPath)
	})

	runKuwoE2EStage(t, "E2_Mobile_128", func(t *testing.T) {
		candidates := mobileQualityCandidates(platform.QualityStandard)
		if len(candidates) != 1 ||
			candidates[0].br != "128kmp3" ||
			candidates[0].format != "mp3" ||
			candidates[0].bitrate != 128 ||
			candidates[0].quality != platform.QualityStandard {
			t.Fatal("standard quality no longer maps to the exact mobile 128 candidate")
		}
		info, err := client.resolveMobileDownload(ctx, detail, candidates[0])
		if err != nil {
			t.Fatalf("mobile 128 download info failed: %s", redactKuwoE2EError(err))
		}
		assertKuwoE2EDownloadInfo(t, kuwoE2ETrackID, info, "mp3", platform.QualityStandard)
		if info.Bitrate < 102 || info.Bitrate > 154 {
			t.Fatalf("mobile 128 verified average bitrate = %d kbps, want 102..154", info.Bitrate)
		}
		logKuwoE2EMedia(t, kuwoE2ETrackID, info)
		mediaPath := filepath.Join(tempDir, "kuwo-e2e-mobile-128.mp3")
		assertKuwoE2EFullDownload(t, ctx, service, info, mediaPath)
		assertKuwoE2ELocalMPEG(t, mediaPath)
		assertKuwoE2EFFmpegDecode(t, ctx, mediaPath)
	})

	runKuwoE2EStage(t, "F1_Forced_Web_MP3", func(t *testing.T) {
		var err error
		webInfo, err = client.resolveWebDownload(ctx, detail)
		if err != nil {
			t.Fatalf("forced Web MP3 download info failed: %s", redactKuwoE2EError(err))
		}
		if webInfo.Quality != platform.QualityStandard &&
			webInfo.Quality != platform.QualityHigh {
			t.Fatalf("Web MP3 quality = %s, want standard or high", webInfo.Quality)
		}
		assertKuwoE2EDownloadInfo(t, kuwoE2ETrackID, webInfo, "mp3", webInfo.Quality)
		if webInfo.Quality == platform.QualityStandard &&
			(webInfo.Bitrate < 102 || webInfo.Bitrate > 154) {
			t.Fatalf("Web standard verified average bitrate = %d kbps, want 102..154", webInfo.Bitrate)
		}
		if webInfo.Quality == platform.QualityHigh &&
			(webInfo.Bitrate < 256 || webInfo.Bitrate > 384) {
			t.Fatalf("Web high verified average bitrate = %d kbps, want 256..384", webInfo.Bitrate)
		}
		if webInfo.Size < 2<<20 || webInfo.Size > 10<<20 {
			t.Fatalf("Web 128 size = %d, want 2..10 MiB", webInfo.Size)
		}
		logKuwoE2EMedia(t, kuwoE2ETrackID, webInfo)
	})

	runKuwoE2EStage(t, "F2_DownloadService_Full_Web_MP3", func(t *testing.T) {
		webPath = filepath.Join(tempDir, "kuwo-e2e-web.mp3")
		assertKuwoE2EFullDownload(t, ctx, service, webInfo, webPath)
	})

	runKuwoE2EStage(t, "F3_Local_Web_MPEG_Frame", func(t *testing.T) {
		assertKuwoE2ELocalMPEG(t, webPath)
		assertKuwoE2EFFmpegDecode(t, ctx, webPath)
	})

	runKuwoE2EStage(t, "G_Paid_Or_Preview_Typed_Rejection", func(t *testing.T) {
		info, err := client.GetDownloadInfo(ctx, kuwoE2EPaidID, platform.QualityStandard)
		if info != nil {
			t.Fatal("paid/preview track unexpectedly returned download info")
		}
		if !errors.Is(err, platform.ErrUnavailable) {
			t.Fatalf("paid/preview error is not ErrUnavailable: %s", redactKuwoE2EError(err))
		}
		typedReason := errors.Is(err, errPaidTrack) ||
			errors.Is(err, errPreviewMedia) ||
			errors.Is(err, errTrackIdentityMismatch) ||
			errors.Is(err, errTrackDurationMismatch)
		if !typedReason {
			t.Fatalf("paid/preview rejection lacks a typed media reason: %s", redactKuwoE2EError(err))
		}
	})

	runKuwoE2EStage(t, "H_Lyrics_Main_Chain", func(t *testing.T) {
		lyrics, err := client.GetLyrics(ctx, kuwoE2ETrackID)
		if err != nil {
			t.Fatalf("lyrics failed: %s", redactKuwoE2EError(err))
		}
		if lyrics == nil || len(lyrics.Timestamped) == 0 {
			t.Fatal("lyrics contain no timestamped lines")
		}
		var previous time.Duration
		for index, line := range lyrics.Timestamped {
			if line.Time < 0 {
				t.Fatalf("lyric line %d has negative timestamp", index)
			}
			if index > 0 && line.Time < previous {
				t.Fatalf("lyric timestamps are not sorted at line %d", index)
			}
			if strings.TrimSpace(line.Text) == "" {
				t.Fatalf("lyric line %d is empty", index)
			}
			if wordTimingTagPattern.MatchString(line.Text) {
				t.Fatalf("lyric line %d still contains enhanced word timing tags", index)
			}
			previous = line.Time
		}
		if strings.TrimSpace(lyrics.Plain) == "" || !strings.Contains(lyrics.Plain, "好运来") {
			t.Fatal("plain lyrics do not identify the expected song")
		}
	})

	runKuwoE2EStage(t, "I_Playlist_Page_1_And_Page_2", func(t *testing.T) {
		firstContext := platform.WithPlaylistLimit(platform.WithPlaylistOffset(ctx, 0), 2)
		first, err := kuwoPlatform.GetPlaylist(firstContext, kuwoE2EPlaylistID)
		if err != nil {
			t.Fatalf("playlist page 1 failed: %s", redactKuwoE2EError(err))
		}
		assertKuwoE2EPlaylistPage(t, first, true)

		secondContext := platform.WithPlaylistLimit(platform.WithPlaylistOffset(ctx, 2), 2)
		second, err := kuwoPlatform.GetPlaylist(secondContext, kuwoE2EPlaylistID)
		if err != nil {
			t.Fatalf("playlist page 2 failed: %s", redactKuwoE2EError(err))
		}
		assertKuwoE2EPlaylistPage(t, second, true)

		firstIDs := make(map[string]struct{}, len(first.Tracks))
		for _, track := range first.Tracks {
			firstIDs[track.ID] = struct{}{}
		}
		if _, duplicate := firstIDs[second.Tracks[0].ID]; duplicate {
			t.Fatalf("playlist page 2 starts with an ID already present on page 1")
		}
	})
}

func runKuwoE2EStage(t *testing.T, name string, stage func(*testing.T)) {
	t.Helper()
	if !t.Run(name, stage) {
		t.FailNow()
	}
}

func kuwoE2ETrackText(track platform.Track) string {
	var builder strings.Builder
	builder.WriteString(track.Title)
	for _, artist := range track.Artists {
		builder.WriteByte(' ')
		builder.WriteString(artist.Name)
	}
	return builder.String()
}

func assertKuwoE2EDownloadInfo(
	t *testing.T,
	trackID string,
	info *platform.DownloadInfo,
	format string,
	quality platform.Quality,
) {
	t.Helper()
	if info == nil {
		t.Fatal("download info is nil")
	}
	if info.Format != format || info.Quality != quality {
		t.Fatalf(
			"download contract = format %q quality %s, want %q/%s",
			info.Format,
			info.Quality,
			format,
			quality,
		)
	}
	if info.Size <= 0 {
		t.Fatalf("download size = %d, want positive", info.Size)
	}
	if info.Bitrate <= 0 {
		t.Fatalf("download bitrate = %d, want positive", info.Bitrate)
	}
	parsed, err := url.Parse(info.URL)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" {
		t.Fatal("download URL is not a valid HTTPS media URL")
	}
	if strings.TrimSpace(kuwoE2EHeader(info.Headers, "User-Agent")) == "" ||
		strings.TrimSpace(kuwoE2EHeader(info.Headers, "Referer")) == "" {
		t.Fatal("download policy is missing User-Agent or Referer")
	}
	if info.ValidateURL == nil {
		t.Fatal("download URL validator is nil")
	}
	if err := info.ValidateURL(info.URL); err != nil {
		t.Fatalf("download URL validator rejected the acquired URL: %s", redactKuwoE2EError(err))
	}
	if info.ExpiresAt == nil {
		t.Fatal("download expiry is nil")
	}
	remaining := time.Until(*info.ExpiresAt)
	if remaining <= 0 || remaining > 15*time.Minute {
		t.Fatalf("download expiry remaining = %s, want 0..15m", remaining)
	}
	if !isASCIIUnsignedDecimal(trackID, 20) {
		t.Fatalf("invalid E2E track identity %q", trackID)
	}
}

func kuwoE2EHeader(headers map[string]string, name string) string {
	for key, value := range headers {
		if strings.EqualFold(key, name) {
			return value
		}
	}
	return ""
}

func logKuwoE2EMedia(t *testing.T, trackID string, info *platform.DownloadInfo) {
	t.Helper()
	hostname := "[invalid-host]"
	if parsed, err := url.Parse(info.URL); err == nil && parsed.Hostname() != "" {
		hostname = parsed.Hostname()
	}
	t.Logf(
		"media contract rid=%s format=%s quality=%s bitrate=%d size=%d host=%s",
		trackID,
		info.Format,
		info.Quality,
		info.Bitrate,
		info.Size,
		hostname,
	)
}

func assertKuwoE2EDirectFLACInfo(
	t *testing.T,
	info *platform.DownloadInfo,
	minimumSize int64,
	maximumSize int64,
) {
	t.Helper()
	if info == nil || info.Downloader == nil {
		t.Fatal("direct FLAC is missing its verified custom downloader")
	}
	parsed, err := url.Parse(info.URL)
	if err != nil ||
		!strings.EqualFold(filepath.Ext(parsed.Path), ".flac") ||
		strings.EqualFold(filepath.Ext(parsed.Path), ".mflac") {
		t.Fatal("direct FLAC URL does not use the expected .flac container")
	}
	if info.Size < minimumSize || info.Size > maximumSize {
		t.Fatalf(
			"direct FLAC size = %d, want %d..%d",
			info.Size,
			minimumSize,
			maximumSize,
		)
	}
}

func assertKuwoE2ECleanFLAC(
	t *testing.T,
	mediaPath string,
	expectedDuration time.Duration,
	minimumSampleRate int,
	minimumBitsPerSample int,
) flacStreamInfo {
	t.Helper()
	file, err := os.Open(mediaPath)
	if err != nil {
		t.Fatalf("open direct FLAC: %s", redactKuwoE2EError(err))
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil {
		t.Fatalf("stat direct FLAC: %s", redactKuwoE2EError(err))
	}
	if stat.Size() <= int64(len(knownDirectFLACTrailer))+42 {
		t.Fatalf("direct FLAC is truncated at %d bytes", stat.Size())
	}
	header := make([]byte, 42)
	if _, err := io.ReadFull(file, header); err != nil {
		t.Fatalf("read direct FLAC header: %s", redactKuwoE2EError(err))
	}
	streamInfo, err := parseFLACStreamInfo(header)
	if err != nil {
		t.Fatalf("parse direct FLAC STREAMINFO: %s", redactKuwoE2EError(err))
	}
	if streamInfo.sampleRate < minimumSampleRate ||
		streamInfo.bitsPerSample < minimumBitsPerSample ||
		streamInfo.channels != 2 ||
		!durationsMatch(streamInfo.duration, expectedDuration) {
		t.Fatalf(
			"direct STREAMINFO sampleRate=%d bits=%d channels=%d duration=%s",
			streamInfo.sampleRate,
			streamInfo.bitsPerSample,
			streamInfo.channels,
			streamInfo.duration,
		)
	}
	tail := make([]byte, len(knownDirectFLACTrailer))
	if _, err := file.ReadAt(tail, stat.Size()-int64(len(tail))); err != nil {
		t.Fatalf("read direct FLAC tail: %s", redactKuwoE2EError(err))
	}
	if bytes.Equal(tail, knownDirectFLACTrailer) {
		t.Fatal("known Kuwo trailer remains after direct FLAC download")
	}
	t.Logf(
		"verified direct STREAMINFO sampleRate=%d bits=%d channels=%d duration=%s",
		streamInfo.sampleRate,
		streamInfo.bitsPerSample,
		streamInfo.channels,
		streamInfo.duration,
	)
	return streamInfo
}

func assertKuwoE2EFFmpegDecode(
	t *testing.T,
	ctx context.Context,
	mediaPath string,
) {
	t.Helper()
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Fatalf("ffmpeg is required for KUWO_E2E=1: %s", redactKuwoE2EError(err))
	}
	command := exec.CommandContext(
		ctx,
		ffmpegPath,
		"-nostdin",
		"-v", "error",
		"-xerror",
		"-i", mediaPath,
		"-f", "null",
		"-",
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf(
			"ffmpeg full media decode failed: %s; output tail=%q",
			redactKuwoE2EError(err),
			boundedKuwoE2EOutput(output),
		)
	}
	if len(bytes.TrimSpace(output)) != 0 {
		t.Fatalf("ffmpeg reported unexpected diagnostics: %q", boundedKuwoE2EOutput(output))
	}
}

func assertKuwoE2EFullDownload(
	t *testing.T,
	ctx context.Context,
	service *download.DownloadService,
	info *platform.DownloadInfo,
	destination string,
) {
	t.Helper()
	if service == nil || info == nil {
		t.Fatal("download service or DownloadInfo is nil")
	}
	expectedSize := info.Size
	if expectedSize <= 0 {
		t.Fatalf("download expected size = %d, want positive", expectedSize)
	}
	written, err := service.Download(ctx, info, destination, nil)
	if err != nil {
		t.Fatalf("DownloadService full download failed: %s", redactKuwoE2EError(err))
	}
	if written != expectedSize {
		t.Fatalf("DownloadService wrote %d bytes, want %d", written, expectedSize)
	}
	stat, err := os.Stat(destination)
	if err != nil {
		t.Fatalf("stat downloaded media: %s", redactKuwoE2EError(err))
	}
	if stat.Size() != expectedSize {
		t.Fatalf("downloaded file size = %d, want %d", stat.Size(), expectedSize)
	}
	if info.Size != expectedSize {
		t.Fatalf("DownloadInfo size mutated from %d to %d", expectedSize, info.Size)
	}
}

func assertKuwoE2ELocalMPEG(t *testing.T, mediaPath string) {
	t.Helper()
	file, err := os.Open(mediaPath)
	if err != nil {
		t.Fatalf("open downloaded MP3: %s", redactKuwoE2EError(err))
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil {
		t.Fatalf("stat downloaded MP3: %s", redactKuwoE2EError(err))
	}
	if stat.Size() < 10 {
		t.Fatalf("downloaded MP3 is truncated at %d bytes", stat.Size())
	}

	header := make([]byte, 10)
	if _, err := io.ReadFull(file, header); err != nil {
		t.Fatalf("read downloaded MP3 header: %s", redactKuwoE2EError(err))
	}
	if !bytes.Equal(header[:3], []byte("ID3")) {
		if !validMPEGHeader(header[:4]) {
			t.Fatal("downloaded MP3 does not start with a valid MPEG frame")
		}
		return
	}

	for _, value := range header[6:10] {
		if value&0x80 != 0 {
			t.Fatal("downloaded MP3 has an invalid ID3 syncsafe length")
		}
	}
	tagSize := int64(header[6])<<21 |
		int64(header[7])<<14 |
		int64(header[8])<<7 |
		int64(header[9])
	if tagSize > maxID3TagSize {
		t.Fatalf("downloaded MP3 ID3 tag = %d bytes, exceeds limit", tagSize)
	}
	footerSize := int64(0)
	if header[5]&0x10 != 0 {
		footerSize = 10
	}
	frameOffset := int64(10) + tagSize + footerSize
	if frameOffset < 10 || frameOffset > stat.Size()-4 {
		t.Fatalf("downloaded MP3 ID3 frame offset = %d is out of bounds", frameOffset)
	}
	frame := make([]byte, 4)
	if _, err := file.ReadAt(frame, frameOffset); err != nil {
		t.Fatalf("read downloaded MP3 first frame: %s", redactKuwoE2EError(err))
	}
	if !validMPEGHeader(frame) {
		t.Fatal("downloaded MP3 has an invalid MPEG frame after ID3")
	}
}

func kuwoE2EFileSHA256(t *testing.T, mediaPath string) [sha256.Size]byte {
	t.Helper()
	file, err := os.Open(mediaPath)
	if err != nil {
		t.Fatalf("open downloaded media for comparison: %s", redactKuwoE2EError(err))
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		t.Fatalf("hash downloaded media: %s", redactKuwoE2EError(err))
	}
	var result [sha256.Size]byte
	copy(result[:], digest.Sum(nil))
	return result
}

func assertKuwoE2EPlaylistPage(t *testing.T, playlist *platform.Playlist, requireTracks bool) {
	t.Helper()
	if playlist == nil {
		t.Fatal("playlist is nil")
	}
	if playlist.ID != kuwoE2EPlaylistID || playlist.Platform != "kuwo" {
		t.Fatalf(
			"playlist identity = %q/%q, want kuwo/%s",
			playlist.Platform,
			playlist.ID,
			kuwoE2EPlaylistID,
		)
	}
	if strings.TrimSpace(playlist.Title) == "" {
		t.Fatal("playlist title is empty")
	}
	if len(playlist.Tracks) > 2 {
		t.Fatalf("playlist page contains %d tracks, want at most 2", len(playlist.Tracks))
	}
	if requireTracks && len(playlist.Tracks) == 0 {
		t.Fatal("playlist page is empty")
	}
	if playlist.TrackCount <= len(playlist.Tracks) {
		t.Fatalf(
			"playlist total = %d, want greater than the page size %d",
			playlist.TrackCount,
			len(playlist.Tracks),
		)
	}
	for index, track := range playlist.Tracks {
		if !isASCIIUnsignedDecimal(track.ID, 20) || track.Platform != "kuwo" {
			t.Fatalf("playlist track %d has invalid identity/platform", index)
		}
	}
}

func redactKuwoE2EError(err error) string {
	if err == nil {
		return "<nil>"
	}
	return redactKuwoE2EText(err.Error())
}

func redactKuwoE2EText(value string) string {
	return kuwoE2EURLPattern.ReplaceAllString(value, "[redacted-url]")
}

func boundedKuwoE2EOutput(output []byte) string {
	const maximum = 4 << 10
	if len(output) > maximum {
		output = output[len(output)-maximum:]
	}
	return redactKuwoE2EText(string(output))
}
