package netease

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestRecognitionMiddleStart(t *testing.T) {
	tests := []struct {
		name, body string
		want       float64
		wantErr    bool
	}{
		{"audio duration", `{"streams":[{"duration":"266.32"}],"format":{"duration":"300"}}`, 130.16, false},
		{"container duration", `{"streams":[{}],"format":{"duration":"18"}}`, 6, false},
		{"unknown stream duration", `{"streams":[{"duration":"N/A"}],"format":{"duration":"18"}}`, 6, false},
		{"exactly six seconds", `{"streams":[{"duration":"6"}]}`, 0, false},
		{"short audio with long video", `{"streams":[{"duration":"5"}],"format":{"duration":"300"}}`, 0, true},
		{"no audio", `{"streams":[],"format":{"duration":"300"}}`, 0, true},
		{"unknown duration", `{"streams":[{}]}`, 0, true},
		{"NaN", `{"streams":[{"duration":"NaN"}]}`, 0, true},
		{"infinity", `{"streams":[{"duration":"+Inf"}]}`, 0, true},
		{"negative", `{"streams":[{"duration":"-10"}]}`, 0, true},
		{"zero", `{"streams":[{"duration":"0"}]}`, 0, true},
		{"invalid json", `invalid`, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := recognitionMiddleStart([]byte(tt.body))
			if (err != nil) != tt.wantErr || (!tt.wantErr && math.Abs(got-tt.want) > 0.000001) {
				t.Fatalf("start = %v, %v; want %v, error=%v", got, err, tt.want, tt.wantErr)
			}
		})
	}
}

func requireRecognitionMediaTools(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not available", tool)
		}
	}
}

// A WAV needs no optional ffmpeg encoders/filters. Only its middle six seconds
// are positive, so mistakenly selecting the opening is immediately detectable.
func recognitionMiddleWAV(seconds int) []byte {
	const rate = afpSampleRate
	dataSize := seconds * rate * 2
	var buf bytes.Buffer
	buf.WriteString("RIFF")
	_ = binary.Write(&buf, binary.LittleEndian, uint32(dataSize+36))
	buf.WriteString("WAVEfmt ")
	_ = binary.Write(&buf, binary.LittleEndian, uint32(16))
	for _, n := range []uint16{1, 1} {
		_ = binary.Write(&buf, binary.LittleEndian, n)
	}
	for _, n := range []uint32{rate, rate * 2} {
		_ = binary.Write(&buf, binary.LittleEndian, n)
	}
	for _, n := range []uint16{2, 16} {
		_ = binary.Write(&buf, binary.LittleEndian, n)
	}
	buf.WriteString("data")
	_ = binary.Write(&buf, binary.LittleEndian, uint32(dataSize))
	start := (seconds - afpDurationSec) * rate / 2
	for i := 0; i < seconds*rate; i++ {
		value := int16(-8192)
		if i >= start && i < start+afpDurationSec*rate {
			value = 8192
		}
		_ = binary.Write(&buf, binary.LittleEndian, value)
	}
	return buf.Bytes()
}

func TestDecodeMiddlePCM(t *testing.T) {
	requireRecognitionMediaTools(t)
	for _, seconds := range []int{6, 18, 60} {
		data := recognitionMiddleWAV(seconds)
		path := filepath.Join(t.TempDir(), "sample.wav")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		fromFile, err := decodeMiddlePCMFile(context.Background(), path)
		if err != nil {
			t.Fatal(err)
		}
		temp := t.TempDir()
		t.Setenv("TMPDIR", temp)
		fromBytes, err := decodeMiddlePCM(context.Background(), data)
		if err != nil {
			t.Fatal(err)
		}
		if len(fromFile) != afpFromSamples+afpDurationSec*afpSampleRate || len(fromBytes) != len(fromFile) {
			t.Fatalf("%ds source: unexpected sample counts %d / %d", seconds, len(fromFile), len(fromBytes))
		}
		for i, v := range fromFile {
			want := float32(0)
			if i >= afpFromSamples {
				want = 0.25
			}
			if v != want || fromBytes[i] != v {
				t.Fatalf("%ds source sample %d = %v / %v, want %v", seconds, i, v, fromBytes[i], want)
			}
		}
		entries, err := os.ReadDir(temp)
		if err != nil || len(entries) != 0 {
			t.Fatalf("temporary media was not cleaned: %v, %v", entries, err)
		}
	}
}

func TestDecodeMiddlePCMCleansUpOnFailure(t *testing.T) {
	requireRecognitionMediaTools(t)
	for _, data := range [][]byte{[]byte("not media"), recognitionMiddleWAV(5)} {
		temp := t.TempDir()
		t.Setenv("TMPDIR", temp)
		if _, err := decodeMiddlePCM(context.Background(), data); err == nil {
			t.Fatal("expected invalid or short media to fail")
		}
		entries, err := os.ReadDir(temp)
		if err != nil || len(entries) != 0 {
			t.Fatalf("temporary media was not cleaned: %v, %v", entries, err)
		}
	}
}

func TestRecognizeMiddleNoMatchDoesNotRetry(t *testing.T) {
	requireRecognitionMediaTools(t)
	svc := NewRecognizeService(0)
	if err := svc.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer svc.Stop()
	calls := 0
	svc.client.Transport = recognitionResponseTransport(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewBufferString(`{"code":200,"data":{"result":[]}}`))}, nil
	})
	data := recognitionMiddleWAV(18)
	path := filepath.Join(t.TempDir(), "sample.wav")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, fileMode := range []bool{false, true} {
		calls = 0
		var result *RecognizeResult
		var err error
		if fileMode {
			result, err = svc.RecognizeFile(context.Background(), path)
		} else {
			result, err = svc.Recognize(context.Background(), data)
		}
		mapped, err := mapRecognizeResult(result, err)
		if err != nil || mapped != nil || calls != 1 {
			t.Fatalf("fileMode=%v: got result=%+v err=%v calls=%d", fileMode, mapped, err, calls)
		}
	}
}
