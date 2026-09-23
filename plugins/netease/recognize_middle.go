package netease

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"strconv"
	"time"
)

// Remote Bot API uploads arrive as bytes. A short-lived, private file makes
// containers such as MP4 seekable without decoding the entire upload to PCM.
// The local Bot API path below needs no extra file or full-file read.
func decodeMiddlePCM(ctx context.Context, data []byte) ([]float32, error) {
	file, err := os.CreateTemp("", "musicbot-recognize-*")
	if err != nil {
		return nil, fmt.Errorf("recognize: create seekable input: %w", err)
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("recognize: write seekable input: %w", err)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("recognize: close seekable input: %w", err)
	}
	return decodeMiddlePCMFile(ctx, file.Name())
}

// decodeMiddlePCMFile selects exactly one six-second window centered on the
// first audio stream. There is deliberately no alternate-window retry.
func decodeMiddlePCMFile(ctx context.Context, path string) ([]float32, error) {
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	output, err := exec.CommandContext(probeCtx, "ffprobe",
		"-v", "error", "-select_streams", "a:0",
		"-show_entries", "stream=duration:format=duration", "-of", "json", path,
	).Output()
	if err != nil {
		return nil, fmt.Errorf("recognize: probe audio duration: %w", err)
	}
	start, err := recognitionMiddleStart(output)
	if err != nil {
		return nil, err
	}
	pcm, err := decodePCMWindow(ctx, path, nil, start, afpDurationSec)
	if err != nil {
		return nil, err
	}
	if len(pcm) < afpMinSamples-afpFromSamples {
		return nil, fmt.Errorf("recognize: decoded audio too short, need %ds", afpDurationSec)
	}
	// Keep the upstream WASM encoder and its golden fingerprint test unchanged.
	// It skips a four-second prefix; padding is not part of the fingerprint.
	padded := make([]float32, afpFromSamples+len(pcm))
	copy(padded[afpFromSamples:], pcm)
	return padded, nil
}

func recognitionMiddleStart(probeJSON []byte) (float64, error) {
	var probe struct {
		Streams []struct {
			Duration string `json:"duration"`
		} `json:"streams"`
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if err := json.Unmarshal(probeJSON, &probe); err != nil {
		return 0, fmt.Errorf("recognize: parse audio duration: %w", err)
	}
	if len(probe.Streams) == 0 {
		return 0, fmt.Errorf("recognize: media has no audio stream")
	}
	// Ogg/Matroska may expose duration only at container level. Prefer the
	// audio duration when present so a longer video track cannot skew it.
	for _, value := range []string{probe.Streams[0].Duration, probe.Format.Duration} {
		duration, err := strconv.ParseFloat(value, 64)
		if err != nil || math.IsNaN(duration) || math.IsInf(duration, 0) || duration <= 0 {
			continue
		}
		if duration < afpDurationSec {
			return 0, fmt.Errorf("recognize: audio too short, need at least %ds", afpDurationSec)
		}
		return (duration - afpDurationSec) / 2, nil
	}
	return 0, fmt.Errorf("recognize: audio duration unavailable")
}
