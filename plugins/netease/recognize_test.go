package netease

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestRecognizeFingerprint exercises the full pure-Go fingerprint pipeline:
// ffmpeg decode -> afp.wasm ExtractQueryFP (via wazero+embind) -> base64.
// It is skipped when ffmpeg or the wasm module are unavailable so it stays
// green in minimal CI environments.
func TestRecognizeFingerprint(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available")
	}
	if len(afpWasm) == 0 {
		t.Fatal("afp.wasm not embedded into the binary")
	}

	audio, err := os.ReadFile(filepath.Join("testdata", "recognize_tone.mp3"))
	if err != nil {
		t.Skipf("testdata audio missing: %v", err)
	}

	svc := NewRecognizeService(0)
	if err := svc.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		if err := svc.Stop(); err != nil {
			t.Errorf("Stop: %v", err)
		}
	}()

	ctx := context.Background()

	pcm, err := decodePCM(ctx, audio)
	if err != nil {
		t.Fatalf("decodePCM: %v", err)
	}
	if len(pcm) < afpMinSamples {
		t.Fatalf("decoded PCM too short: got %d samples, need >= %d", len(pcm), afpMinSamples)
	}

	first, err := svc.encodeFingerprint(ctx, pcm)
	if err != nil {
		t.Fatalf("encodeFingerprint: %v", err)
	}
	if len(first) == 0 {
		t.Fatal("empty fingerprint")
	}

	// Golden value: byte-identical to the upstream ncm-audio-recognize JS
	// reference (Node.js encode()) run on the same audio. Guards against
	// regressions in the ffmpeg decode params or the WASM fingerprint path.
	const golden = "uLsYcabnjbmgFsxqyv/mkSfnvs84rKhuzILZ1sIWCKFTmQrAiALs19gmp0mTLoUfTcwwQmCt3e91anpdOf7iou5mRUD//+7NrXebqT/4I47s0AfFN2tAoKqNQC9UwFGzNe27ltsCVc56fDNkT45g6M/Fv5F/KSCbMvCInoxwcq+DFgKZ47VPrX0ndfdY+eBqeCjhX2L8sYfkA0VT8Rgiyg=="
	if first != golden {
		t.Fatalf("fingerprint mismatch:\n  got=%s\n want=%s", first, golden)
	}

	// The fingerprint must be deterministic across repeated calls on the same
	// engine instance (verifies WASM state is properly reset per call).
	second, err := svc.encodeFingerprint(ctx, pcm)
	if err != nil {
		t.Fatalf("encodeFingerprint (2nd): %v", err)
	}
	if first != second {
		t.Fatalf("fingerprint not deterministic:\n  1st=%s\n  2nd=%s", first, second)
	}

	t.Logf("fingerprint (%d base64 chars): %s", len(first), first)
}

// TestRecognizeAudioTooShort verifies the short-audio guard rejects input that
// cannot fill the fingerprint window without invoking the WASM module.
func TestRecognizeAudioTooShort(t *testing.T) {
	svc := NewRecognizeService(0)
	// Not started; a too-short PCM slice must fail before reaching the engine.
	_, err := svc.encodeFingerprint(context.Background(), make([]float32, 1000))
	if err == nil {
		t.Fatal("expected error for unstarted service / short input")
	}
}

// TestDecodePCMHonoursDecodeWindow proves the decode is bounded: a long upload
// must not be materialised in full, yet must still yield the samples the
// fingerprint reads. Before the -t bound, a 40-minute upload decoded to
// hundreds of megabytes of PCM only to have all but the first ten seconds
// discarded.
func TestDecodePCMHonoursDecodeWindow(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available")
	}

	// 120s of tone: an order of magnitude more than the decode window.
	const sourceSeconds = 120
	gen := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=120",
		"-f", "ogg", "-acodec", "libvorbis", "pipe:1")
	encoded, err := gen.Output()
	if err != nil {
		t.Skipf("cannot synthesise test audio: %v", err)
	}

	pcm, err := decodePCM(context.Background(), encoded)
	if err != nil {
		t.Fatalf("decodePCM: %v", err)
	}

	if len(pcm) < afpMinSamples {
		t.Fatalf("decoded %d samples, fingerprint needs at least %d", len(pcm), afpMinSamples)
	}
	// Allow one second of container slack above the requested window, but the
	// result must be nowhere near the full source length.
	maxExpected := (afpDecodeSeconds + 1) * afpSampleRate
	if len(pcm) > maxExpected {
		t.Fatalf("decoded %d samples (~%ds); decode window of %ds was not applied",
			len(pcm), len(pcm)/afpSampleRate, afpDecodeSeconds)
	}
	if len(pcm) >= sourceSeconds*afpSampleRate {
		t.Fatalf("decoded the entire %ds source (%d samples)", sourceSeconds, len(pcm))
	}
	t.Logf("decoded %d samples (~%.1fs) from a %ds source; window=%ds, needed=%d",
		len(pcm), float64(len(pcm))/afpSampleRate, sourceSeconds, afpDecodeSeconds, afpMinSamples)
}
