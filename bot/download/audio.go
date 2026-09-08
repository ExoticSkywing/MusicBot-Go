package download

import (
	"bufio"
	"context"
	"fmt"
	"math"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/liuran001/MusicBot-Go/bot/platform"
)

// VerifyFullAudio compares the audio actually present on disk with the full
// catalog duration. Container headers alone are insufficient: a truncated MP3
// can still advertise the original duration in its Xing header. Reading every
// audio packet also covers custom downloaders and decrypted streams.
// Missing catalog metadata or an unavailable probe fails closed.
func VerifyFullAudio(ctx context.Context, path string, expected time.Duration) (time.Duration, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if expected <= 0 {
		return 0, fmt.Errorf("%w: missing catalog duration", platform.ErrIncompleteAudio)
	}
	probe, err := exec.LookPath("ffprobe")
	if err != nil {
		return 0, fmt.Errorf("%w: ffprobe is required", platform.ErrIncompleteAudio)
	}
	probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(probeCtx, probe,
		"-v", "error", "-select_streams", "a:0", "-show_packets",
		"-show_entries", "packet=duration_time",
		"-of", "default=noprint_wrappers=1:nokey=0", path)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return 0, fmt.Errorf("%w: cannot start audio probe", platform.ErrIncompleteAudio)
	}
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("%w: cannot start audio probe", platform.ErrIncompleteAudio)
	}
	duration, scanErr := readAudioPacketDuration(bufio.NewScanner(stdout))
	if scanErr != nil {
		cancel()
	}
	waitErr := cmd.Wait()
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if scanErr != nil || waitErr != nil || duration <= 0 {
		return 0, fmt.Errorf("%w: cannot measure audio packets", platform.ErrIncompleteAudio)
	}
	// Keep the missing-audio tolerance bounded even for long tracks, and
	// proportional for naturally short songs. Catalogs often truncate to whole
	// seconds, so allow that rounding plus codec padding on the longer side.
	tolerance := min(3*time.Second, expected/20)
	longerTolerance := max(1500*time.Millisecond, tolerance)
	if duration < expected-tolerance || duration > expected+longerTolerance {
		return duration, fmt.Errorf("%w: audio duration %.3fs differs from catalog %.3fs",
			platform.ErrIncompleteAudio, duration.Seconds(), expected.Seconds())
	}
	return duration, nil
}

func readAudioPacketDuration(scanner *bufio.Scanner) (time.Duration, error) {
	var seconds float64
	for scanner.Scan() {
		value, ok := strings.CutPrefix(scanner.Text(), "duration_time=")
		if !ok {
			continue // Ignore optional packet side data (e.g. encoder padding).
		}
		duration, err := strconv.ParseFloat(value, 64)
		if err != nil || duration < 0 || math.IsNaN(duration) || math.IsInf(duration, 0) {
			return 0, fmt.Errorf("invalid packet duration")
		}
		seconds += duration
		if seconds >= float64(math.MaxInt64)/float64(time.Second) {
			return 0, fmt.Errorf("audio duration overflow")
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	return time.Duration(seconds * float64(time.Second)), nil
}
