package id3

import (
	"bytes"
	"crypto/sha256"
	"image"
	"image/color"
	"image/png"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"go.senan.xyz/taglib"
)

func TestRewriteLocalizedNamesPreservesAudioAndOtherTags(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg required for real audio fixtures")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe required for codec verification")
	}

	formats := []struct {
		name  string
		ext   string
		codec string
	}{
		{name: "mp3", ext: ".mp3", codec: "libmp3lame"},
		{name: "flac", ext: ".flac", codec: "flac"},
		{name: "m4a-aac", ext: ".m4a", codec: "aac"},
		{name: "m4a-alac", ext: ".m4a", codec: "alac"},
		{name: "m4a-eac3", ext: ".m4a", codec: "eac3"},
	}
	for _, format := range formats {
		t.Run(format.name, func(t *testing.T) {
			audioPath := filepath.Join(t.TempDir(), "audio"+format.ext)
			generateTaggedAudioFixture(t, ffmpeg, audioPath, format.codec)

			beforeTags, err := taglib.ReadTags(audioPath)
			if err != nil {
				t.Fatalf("read seeded tags: %v", err)
			}
			beforeCover, err := taglib.ReadImage(audioPath)
			if err != nil {
				t.Fatalf("read seeded cover: %v", err)
			}
			beforeCodec := probeAudioCodec(t, ffprobe, audioPath)
			beforeAudio := decodedAudioHash(t, ffmpeg, audioPath)

			service := NewID3Service(nil)
			if err := service.RewriteLocalizedNames(audioPath, "晴天", "周杰伦", "叶惠美"); err != nil {
				t.Fatalf("rewrite localized names: %v", err)
			}

			afterTags, err := taglib.ReadTags(audioPath)
			if err != nil {
				t.Fatalf("read rewritten tags: %v", err)
			}
			for key, want := range map[string][]string{
				taglib.Title:  {"晴天"},
				taglib.Artist: {"周杰伦"},
				taglib.Album:  {"叶惠美"},
			} {
				if !reflect.DeepEqual(afterTags[key], want) {
					t.Errorf("%s = %q, want %q", key, afterTags[key], want)
				}
			}
			for key, want := range beforeTags {
				if key == taglib.Title || key == taglib.Artist || key == taglib.Album {
					continue
				}
				if !reflect.DeepEqual(afterTags[key], want) {
					t.Errorf("non-name tag %s changed: got %q, want %q", key, afterTags[key], want)
				}
			}
			afterCover, err := taglib.ReadImage(audioPath)
			if err != nil {
				t.Fatalf("read rewritten cover: %v", err)
			}
			if !bytes.Equal(afterCover, beforeCover) {
				t.Error("embedded cover changed")
			}
			if afterCodec := probeAudioCodec(t, ffprobe, audioPath); afterCodec != beforeCodec {
				t.Errorf("codec changed: got %q, want %q", afterCodec, beforeCodec)
			}
			if afterAudio := decodedAudioHash(t, ffmpeg, audioPath); afterAudio != beforeAudio {
				t.Errorf("decoded audio changed: got %x, want %x", afterAudio, beforeAudio)
			}
		})
	}
}

func TestRewriteLocalizedNamesRejectsUnknownFormat(t *testing.T) {
	err := NewID3Service(nil).RewriteLocalizedNames(filepath.Join(t.TempDir(), "audio.wav"), "title", "artist", "album")
	if err == nil || !strings.Contains(err.Error(), "unsupported audio format") {
		t.Fatalf("error = %v, want unsupported audio format", err)
	}
}

func generateTaggedAudioFixture(t *testing.T, ffmpeg, audioPath, codec string) {
	t.Helper()
	args := []string{"-v", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=0.3", "-c:a", codec}
	if codec == "eac3" {
		args = append(args, "-f", "mp4")
	}
	cmd := exec.Command(ffmpeg, append(args, "-y", audioPath)...)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate %s fixture: %v: %s", codec, err, output)
	}
	seed := map[string][]string{
		taglib.Title:       {"Old Title"},
		taglib.Artist:      {"Old Artist"},
		taglib.Album:       {"Old Album"},
		taglib.AlbumArtist: {"Old Album Artist"},
		taglib.Date:        {"2020"},
		taglib.Genre:       {"Test Genre"},
		taglib.Comment:     {"Keep this comment"},
		taglib.Lyrics:      {"[00:00.00]Keep these lyrics"},
		"X-CACHE-KEY":      {"cache-value"},
	}
	if err := taglib.WriteTags(audioPath, seed, taglib.Clear); err != nil {
		t.Fatalf("seed tags: %v", err)
	}
	cover := tinyPNG(t)
	if err := taglib.WriteImage(audioPath, cover); err != nil {
		t.Fatalf("seed cover: %v", err)
	}
	tags, err := taglib.ReadTags(audioPath)
	if err != nil {
		t.Fatalf("read back seeded tags: %v", err)
	}
	for key, want := range seed {
		if !reflect.DeepEqual(tags[key], want) {
			t.Fatalf("fixture did not retain %s: got %q, want %q", key, tags[key], want)
		}
	}
}

func tinyPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.NRGBA{R: 0xff, A: 0xff})
	img.Set(1, 0, color.NRGBA{G: 0xff, A: 0xff})
	img.Set(0, 1, color.NRGBA{B: 0xff, A: 0xff})
	img.Set(1, 1, color.NRGBA{R: 0xff, G: 0xff, A: 0xff})
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		t.Fatalf("encode cover: %v", err)
	}
	return out.Bytes()
}

func probeAudioCodec(t *testing.T, ffprobe, audioPath string) string {
	t.Helper()
	cmd := exec.Command(ffprobe, "-v", "error", "-select_streams", "a:0", "-show_entries", "stream=codec_name", "-of", "default=nw=1:nk=1", audioPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("probe codec: %v: %s", err, output)
	}
	return strings.TrimSpace(string(output))
}

func decodedAudioHash(t *testing.T, ffmpeg, audioPath string) [sha256.Size]byte {
	t.Helper()
	cmd := exec.Command(ffmpeg, "-v", "error", "-i", audioPath, "-map", "0:a:0", "-c:a", "pcm_s16le", "-f", "s16le", "-")
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("decode audio: %v", err)
	}
	if len(output) == 0 {
		t.Fatal("decoded audio is empty")
	}
	return sha256.Sum256(output)
}
