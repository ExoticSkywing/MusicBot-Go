package download

import (
	"bufio"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/liuran001/MusicBot-Go/bot/platform"
)

func TestReadAudioPacketDuration(t *testing.T) {
	for _, tc := range []struct {
		input   string
		want    time.Duration
		invalid bool
	}{
		{"duration_time=0.25\nduration_time=0.75\n", time.Second, false},
		{"duration_time=0.5\nside_data_type=Skip Samples\nskip_samples=312\nduration_time=0.5\n", time.Second, false},
		{"duration_time=N/A\n", 0, true},
		{"duration_time=-1\n", 0, true},
		{"duration_time=NaN\n", 0, true},
		{"duration_time=+Inf\n", 0, true},
		{"duration_time=1e30\n", 0, true},
	} {
		got, err := readAudioPacketDuration(bufio.NewScanner(strings.NewReader(tc.input)))
		if (err != nil) != tc.invalid || got != tc.want {
			t.Fatalf("packets %q: got %v, %v; want %v, invalid=%v", tc.input, got, err, tc.want, tc.invalid)
		}
	}
}

func TestVerifyFullAudioFailsClosed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := VerifyFullAudio(ctx, "missing.mp3", time.Minute); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation = %v", err)
	}
	if _, err := VerifyFullAudio(context.Background(), "missing.mp3", 0); !errors.Is(err, platform.ErrIncompleteAudio) {
		t.Fatalf("missing catalog duration = %v", err)
	}
	t.Setenv("PATH", t.TempDir())
	if _, err := VerifyFullAudio(context.Background(), "missing.mp3", time.Minute); !errors.Is(err, platform.ErrIncompleteAudio) {
		t.Fatalf("missing ffprobe = %v", err)
	}
}

func TestVerifyFullAudioRealFiles(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg required for real audio fixtures")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe required")
	}
	for _, format := range []string{"mp3", "flac", "m4a", "ogg"} {
		t.Run(format, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "audio."+format)
			cmd := exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=3.9", "-y", path)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("generate %s: %v: %s", format, err, output)
			}
			if got, err := VerifyFullAudio(context.Background(), path, 4*time.Second); err != nil {
				t.Fatalf("valid short song rejected (%s): %v", got, err)
			}
			if _, err := VerifyFullAudio(context.Background(), path, 3*time.Second); err != nil {
				t.Fatalf("short song with catalog rounded down rejected: %v", err)
			}
			if _, err := VerifyFullAudio(context.Background(), path, 3*time.Minute); !errors.Is(err, platform.ErrIncompleteAudio) {
				t.Fatalf("preview accepted: %v", err)
			}
			if format == "mp3" {
				// Preserve the full-duration Xing header but drop half the frames.
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, data[:len(data)/2], 0600); err != nil {
					t.Fatal(err)
				}
				if _, err := VerifyFullAudio(context.Background(), path, 4*time.Second); !errors.Is(err, platform.ErrIncompleteAudio) {
					t.Fatalf("truncated MP3 with original header accepted: %v", err)
				}
			}
		})
	}
	path := filepath.Join(t.TempDir(), "invalid.mp3")
	if err := os.WriteFile(path, []byte("not audio"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyFullAudio(context.Background(), path, 3*time.Minute); !errors.Is(err, platform.ErrIncompleteAudio) {
		t.Fatalf("invalid media accepted: %v", err)
	}
}
