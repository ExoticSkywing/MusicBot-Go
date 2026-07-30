package kuwo

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/liuran001/MusicBot-Go/bot/platform"
)

const directFLACTestURL = "https://kw-lw.kuwo.cn/audio/test.flac"

var productionDirectFLACTrailer = []byte{
	0xf0, 0x00, 0xff, 0x0f,
	0x47, 0x40, 0x38, 0x40, 0x48, 0x46, 0x3c, 0x36,
	0x23, 0x23, 0x26, 0x39, 0x62, 0x3c, 0x35, 0x66,
	0x26, 0x47, 0x46, 0x36, 0x6b, 0x39,
	0x0e, 0x55, 0xff, 0xf0,
}

const (
	productionDirectFLACHeaderHex = "664c614300000022100010000000120055040bb8037000a62800ca72af53be5f4e594e78763090c04af4"
	productionDirectFLACFrameHex  = "fff8ba1ce0a9a2ab0000000000000000af93"
)

func TestDirectFLACContextReaderStopsAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	reader := directFLACContextReader{
		ctx:    ctx,
		reader: strings.NewReader("fLaC"),
	}
	buffer := make([]byte, 4)
	readSize, err := reader.Read(buffer)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context canceled", err)
	}
	if readSize != 0 {
		t.Fatalf("read size = %d, want 0", readSize)
	}
}

func TestValidateDirectFLACFilePreservesCancellation(t *testing.T) {
	header := productionDirectFLACHeader(t)
	path := filepath.Join(t.TempDir(), "canceled.flac")
	if err := os.WriteFile(path, append(header[:], 0x00), 0o600); err != nil {
		t.Fatalf("write FLAC fixture: %v", err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open FLAC fixture: %v", err)
	}
	defer file.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = validateDirectFLACFile(ctx, file, 43, header)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context canceled", err)
	}
}

func TestAnalyzeDirectFLACTailProvesFinalFrameBoundary(t *testing.T) {
	header := productionDirectFLACHeader(t)
	frame := mustDecodeHex(t, productionDirectFLACFrameHex)
	dynamicTrailer := append(
		append([]byte{0xf0, 0x00, 0xff, 0x0f}, bytes.Repeat([]byte{0x73}, 37)...),
		0x0e, 0x55, 0xff, 0xf0,
	)
	internalPrefixTrailer := append([]byte(nil), dynamicTrailer[:12]...)
	internalPrefixTrailer = append(internalPrefixTrailer, 0xf0, 0x00, 0xff, 0x0f)
	internalPrefixTrailer = append(internalPrefixTrailer, dynamicTrailer[12:]...)

	for _, test := range []struct {
		name           string
		trailer        []byte
		wantTrailerLen int
	}{
		{name: "clean", wantTrailerLen: 0},
		{name: "legacy", trailer: knownDirectFLACTrailer, wantTrailerLen: len(knownDirectFLACTrailer)},
		{name: "production", trailer: productionDirectFLACTrailer, wantTrailerLen: len(productionDirectFLACTrailer)},
		{name: "dynamic body", trailer: dynamicTrailer, wantTrailerLen: len(dynamicTrailer)},
		{name: "internal prefix", trailer: internalPrefixTrailer, wantTrailerLen: len(internalPrefixTrailer)},
	} {
		t.Run(test.name, func(t *testing.T) {
			tail := append(append([]byte(nil), frame...), test.trailer...)
			rangeStart := int64(42)
			rawSize := rangeStart + int64(len(tail))

			got, err := analyzeDirectFLACTail(header, rawSize, rangeStart, tail)
			if err != nil {
				t.Fatalf("analyze direct FLAC tail: %v", err)
			}
			if got.trailerLen != int64(test.wantTrailerLen) {
				t.Fatalf("trailer length = %d, want %d", got.trailerLen, test.wantTrailerLen)
			}
			if got.outputSize != rawSize-int64(test.wantTrailerLen) {
				t.Fatalf("output size = %d, want %d", got.outputSize, rawSize-int64(test.wantTrailerLen))
			}
		})
	}
}

func TestAnalyzeDirectFLACTailRejectsUnprovenBoundaries(t *testing.T) {
	header := productionDirectFLACHeader(t)
	frame := mustDecodeHex(t, productionDirectFLACFrameHex)
	envelope := append(
		append([]byte{0xf0, 0x00, 0xff, 0x0f}, bytes.Repeat([]byte{0x42}, 9)...),
		0x0e, 0x55, 0xff, 0xf0,
	)
	badCRC8 := append([]byte(nil), frame...)
	badCRC8[7] ^= 0x01
	badCRC16 := append([]byte(nil), frame...)
	badCRC16[len(badCRC16)-1] ^= 0x01
	truncated := append([]byte(nil), frame[:len(frame)-1]...)
	extraBeforeEnvelope := append(append([]byte(nil), frame...), 0x00)
	wrongTotalHeader := header
	packed := binary.BigEndian.Uint64(wrongTotalHeader[18:26])
	binary.BigEndian.PutUint64(wrongTotalHeader[18:26], packed+1)

	for _, test := range []struct {
		name   string
		header [42]byte
		audio  []byte
		tail   []byte
	}{
		{name: "header CRC8", header: header, audio: badCRC8, tail: envelope},
		{name: "frame CRC16", header: header, audio: badCRC16, tail: envelope},
		{name: "frame one byte short", header: header, audio: truncated, tail: envelope},
		{name: "frame one byte long", header: header, audio: extraBeforeEnvelope, tail: envelope},
		{name: "total samples", header: wrongTotalHeader, audio: frame, tail: envelope},
		{name: "unknown trailing garbage", header: header, audio: frame, tail: []byte{0x01}},
	} {
		t.Run(test.name, func(t *testing.T) {
			tail := append(append([]byte(nil), test.audio...), test.tail...)
			rangeStart := int64(42)
			rawSize := rangeStart + int64(len(tail))
			if _, err := analyzeDirectFLACTail(test.header, rawSize, rangeStart, tail); !errors.Is(err, errDirectFLACIntegrity) {
				t.Fatalf("error = %v, want direct FLAC integrity error", err)
			}
		})
	}
}

func TestAnalyzeDirectFLACTailBoundsCandidateWork(t *testing.T) {
	header := productionDirectFLACHeader(t)
	frame := mustDecodeHex(t, productionDirectFLACFrameHex)
	prefix := []byte{0xf0, 0x00, 0xff, 0x0f}
	suffix := []byte{0x0e, 0x55, 0xff, 0xf0}

	t.Run("eight envelope candidates", func(t *testing.T) {
		trailer := append([]byte(nil), prefix...)
		trailer = append(
			trailer,
			bytes.Repeat(prefix, directFLACMaxCandidates-1)...,
		)
		trailer = append(trailer, suffix...)
		tail := append(append([]byte(nil), frame...), trailer...)
		got, err := analyzeDirectFLACTail(
			header,
			int64(42+len(tail)),
			42,
			tail,
		)
		if err != nil {
			t.Fatalf("analyze eight candidates: %v", err)
		}
		if got.trailerLen != int64(len(trailer)) {
			t.Fatalf("trailer length = %d, want %d", got.trailerLen, len(trailer))
		}
	})

	t.Run("nine envelope candidates", func(t *testing.T) {
		trailer := append(
			bytes.Repeat(prefix, directFLACMaxCandidates+1),
			suffix...,
		)
		tail := append(append([]byte(nil), frame...), trailer...)
		if _, err := analyzeDirectFLACTail(
			header,
			int64(42+len(tail)),
			42,
			tail,
		); !errors.Is(err, errDirectFLACIntegrity) {
			t.Fatalf("error = %v, want direct FLAC integrity error", err)
		}
	})

	t.Run("raw sync noise is not a candidate", func(t *testing.T) {
		unknownMaxFrameHeader := header
		clear(unknownMaxFrameHeader[15:18])
		noise := bytes.Repeat(
			[]byte{0xff, 0xf8, 0x00, 0x00, 0x00, 0x00},
			directFLACMaxCandidates+12,
		)
		tail := append(append(append([]byte(nil), noise...), frame...), prefix...)
		tail = append(tail, suffix...)
		got, err := analyzeDirectFLACTail(
			unknownMaxFrameHeader,
			int64(42+len(tail)),
			42,
			tail,
		)
		if err != nil {
			t.Fatalf("analyze tail with raw sync noise: %v", err)
		}
		if got.trailerLen != int64(len(prefix)+len(suffix)) {
			t.Fatalf("trailer length = %d, want %d", got.trailerLen, len(prefix)+len(suffix))
		}
	})
}

func TestAnalyzeDirectFLACTailBoundsTrailerLength(t *testing.T) {
	header := productionDirectFLACHeader(t)
	frame := mustDecodeHex(t, productionDirectFLACFrameHex)
	prefix := []byte{0xf0, 0x00, 0xff, 0x0f}
	suffix := []byte{0x0e, 0x55, 0xff, 0xf0}

	for _, test := range []struct {
		name    string
		length  int
		wantErr bool
	}{
		{name: "exactly four KiB", length: directFLACTrailerSearchSize},
		{name: "four KiB plus one", length: directFLACTrailerSearchSize + 1, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			bodyLength := test.length - len(prefix) - len(suffix)
			trailer := append(append([]byte(nil), prefix...), bytes.Repeat([]byte{0x61}, bodyLength)...)
			trailer = append(trailer, suffix...)
			tail := append(append([]byte(nil), frame...), trailer...)
			got, err := analyzeDirectFLACTail(
				header,
				int64(42+len(tail)),
				42,
				tail,
			)
			if test.wantErr {
				if !errors.Is(err, errDirectFLACIntegrity) {
					t.Fatalf("error = %v, want direct FLAC integrity error", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("analyze tail: %v", err)
			}
			if got.trailerLen != int64(test.length) {
				t.Fatalf("trailer length = %d, want %d", got.trailerLen, test.length)
			}
		})
	}
}

func TestDirectFLACProbeRangeUsesDeclaredFrameBound(t *testing.T) {
	header := productionDirectFLACHeader(t)
	rawSize := int64(directFLACMaxFrameSize + directFLACTrailerSearchSize + 123)

	t.Run("unknown max frame uses four MiB", func(t *testing.T) {
		unknown := header
		clear(unknown[15:18])
		start, err := directFLACProbeRangeStart(unknown, rawSize)
		if err != nil {
			t.Fatal(err)
		}
		if start != 123 {
			t.Fatalf("range start = %d, want 123", start)
		}
	})

	t.Run("declared max frame", func(t *testing.T) {
		start, err := directFLACProbeRangeStart(header, rawSize)
		if err != nil {
			t.Fatal(err)
		}
		want := rawSize -
			int64(mustDirectFLACStreamInfo(t, header[:]).maxFrameSize+
				directFLACTrailerSearchSize)
		if start != want {
			t.Fatalf("range start = %d, want %d", start, want)
		}
	})

	t.Run("declared size above limit", func(t *testing.T) {
		oversized := header
		putTestFLACUint24(
			oversized[15:18],
			directFLACMaxFrameSize+1,
		)
		if _, err := directFLACProbeRangeStart(oversized, rawSize); !errors.Is(err, errDirectFLACIntegrity) {
			t.Fatalf("error = %v, want direct FLAC integrity error", err)
		}
	})
}

func TestDirectFLACDownloaderStripsOnlyKnownTrailerAndCommits(t *testing.T) {
	cleartext := makeDirectFLACTestStream(t, 32<<10)
	raw := append(append([]byte(nil), cleartext...), knownDirectFLACTrailer...)
	var requests atomic.Int32
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests.Add(1)
		if req.Header.Get("Accept-Encoding") != "identity" {
			t.Fatalf("Accept-Encoding = %q, want identity", req.Header.Get("Accept-Encoding"))
		}
		if req.Header.Get("User-Agent") == "" || req.Header.Get("Referer") == "" {
			t.Fatal("media headers missing")
		}
		return directFLACResponse(raw), nil
	})
	client := newClientWithEndpoints(time.Second, nil, kuwoEndpoints{})
	client.downloadHTTPClient = &http.Client{Transport: transport}
	info := &platform.DownloadInfo{
		URL:    directFLACTestURL,
		Size:   int64(len(cleartext)),
		Format: "flac",
	}
	destination := filepath.Join(t.TempDir(), "nested", "song.flac")
	var progress [][2]int64

	written, err := client.directFLACDownloader(
		directFLACTestURL,
		int64(len(raw)),
		mustDirectFLACHeader(t, raw),
		directFLACTail(t, raw),
	)(
		context.Background(),
		info,
		destination,
		func(written, total int64) {
			progress = append(progress, [2]int64{written, total})
		},
	)
	if err != nil {
		t.Fatalf("download direct FLAC: %v", err)
	}
	if written != int64(len(cleartext)) {
		t.Fatalf("written = %d, want %d", written, len(cleartext))
	}
	got, err := os.ReadFile(destination)
	if err != nil {
		t.Fatalf("read destination: %v", err)
	}
	if !bytes.Equal(got, cleartext) {
		t.Fatal("known trailer was not stripped exactly")
	}
	if info.Size != int64(len(cleartext)) {
		t.Fatalf("info size = %d, want %d", info.Size, len(cleartext))
	}
	if requests.Load() != 1 {
		t.Fatalf("requests = %d, want 1", requests.Load())
	}
	if len(progress) == 0 || progress[len(progress)-1] != [2]int64{int64(len(cleartext)), int64(len(cleartext))} {
		t.Fatalf("final progress = %v, want clean output size", progress)
	}
	for index, update := range progress {
		if update[0] < 0 || update[0] > int64(len(cleartext)) ||
			update[1] != int64(len(cleartext)) {
			t.Fatalf("progress exceeded output size: %v", progress)
		}
		if index < len(progress)-1 && update[0] >= int64(len(cleartext)) {
			t.Fatalf("progress completed before atomic publish: %v", progress)
		}
	}
	assertNoDirectFLACPartFiles(t, filepath.Dir(destination))
}

func TestDirectFLACDownloaderStripsProductionThirtyByteTrailerAndCommits(t *testing.T) {
	cleartext := makeDirectFLACTestStream(t, 32<<10)
	raw := append(append([]byte(nil), cleartext...), productionDirectFLACTrailer...)
	client := directFLACTestClient(raw)
	info := &platform.DownloadInfo{
		URL:    directFLACTestURL,
		Size:   int64(len(cleartext)),
		Format: "flac",
	}
	destination := filepath.Join(t.TempDir(), "song.flac")

	written, err := client.directFLACDownloader(
		directFLACTestURL,
		int64(len(raw)),
		mustDirectFLACHeader(t, raw),
		directFLACTail(t, raw),
	)(
		context.Background(),
		info,
		destination,
		nil,
	)
	if err != nil {
		t.Fatalf("download direct FLAC: %v", err)
	}
	if written != int64(len(cleartext)) {
		t.Fatalf("written = %d, want %d", written, len(cleartext))
	}
	got, err := os.ReadFile(destination)
	if err != nil {
		t.Fatalf("read destination: %v", err)
	}
	if !bytes.Equal(got, cleartext) {
		t.Fatal("production trailer was not stripped exactly")
	}
	if info.Size != int64(len(cleartext)) {
		t.Fatalf("info size = %d, want %d", info.Size, len(cleartext))
	}
	assertNoDirectFLACPartFiles(t, filepath.Dir(destination))
}

func TestDirectFLACDownloaderRejectsUnknownTrailingData(t *testing.T) {
	cleartext := makeDirectFLACTestStream(t, 4096)
	differentTrailer := append([]byte(nil), knownDirectFLACTrailer...)
	differentTrailer[len(differentTrailer)-1] ^= 0x01
	raw := append(append([]byte(nil), cleartext...), differentTrailer...)
	client := directFLACTestClient(raw)
	info := &platform.DownloadInfo{
		URL:  directFLACTestURL,
		Size: int64(len(raw)),
	}
	destination := filepath.Join(t.TempDir(), "song.flac")

	_, err := client.directFLACDownloader(
		directFLACTestURL,
		int64(len(raw)),
		mustDirectFLACHeader(t, raw),
		uncheckedDirectFLACProbe(t, raw, int64(len(raw)), 0),
	)(
		context.Background(),
		info,
		destination,
		nil,
	)
	if !errors.Is(err, errDirectFLACIntegrity) {
		t.Fatalf("error = %v, want direct FLAC integrity error", err)
	}
	assertDirectFLACDestinationMissing(t, destination)
	assertNoDirectFLACPartFiles(t, filepath.Dir(destination))
}

func TestDirectFLACDownloaderAcceptsDynamicPayloadButRejectsMarkerVariants(t *testing.T) {
	mutate := func(index int) []byte {
		tail := append([]byte(nil), productionDirectFLACTrailer...)
		tail[index] ^= 0x01
		return tail
	}
	removePayloadByte := append(
		append([]byte(nil), productionDirectFLACTrailer[:10]...),
		productionDirectFLACTrailer[11:]...,
	)
	insertPayloadByte := append(
		append([]byte(nil), productionDirectFLACTrailer[:26]...),
		0x7e,
	)
	insertPayloadByte = append(insertPayloadByte, productionDirectFLACTrailer[26:]...)
	markerPlusOne := append(append([]byte(nil), productionDirectFLACTrailer...), 0x00)
	oldPayloadMutation := append([]byte(nil), knownDirectFLACTrailer...)
	oldPayloadMutation[6] ^= 0x01

	for _, test := range []struct {
		name   string
		tail   []byte
		accept bool
	}{
		{name: "production prefix bit", tail: mutate(0)},
		{name: "production payload bit", tail: mutate(12), accept: true},
		{name: "production suffix bit", tail: mutate(len(productionDirectFLACTrailer) - 1)},
		{name: "production 29 bytes", tail: removePayloadByte, accept: true},
		{name: "production 31 bytes", tail: insertPayloadByte, accept: true},
		{name: "production marker plus one", tail: markerPlusOne},
		{name: "legacy payload bit", tail: oldPayloadMutation, accept: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			cleartext := makeDirectFLACTestStream(t, 4096)
			raw := append(append([]byte(nil), cleartext...), test.tail...)
			client := directFLACTestClient(raw)
			probe := uncheckedDirectFLACProbe(t, raw, int64(len(raw)), 0)
			outputSize := int64(len(raw))
			if test.accept {
				probe = directFLACTail(t, raw)
				outputSize = int64(len(cleartext))
			}
			info := &platform.DownloadInfo{URL: directFLACTestURL, Size: outputSize}
			destination := filepath.Join(t.TempDir(), "song.flac")

			written, err := client.directFLACDownloader(
				directFLACTestURL,
				int64(len(raw)),
				mustDirectFLACHeader(t, raw),
				probe,
			)(
				context.Background(),
				info,
				destination,
				nil,
			)
			if !test.accept {
				if !errors.Is(err, errDirectFLACIntegrity) {
					t.Fatalf("error = %v, want direct FLAC integrity error", err)
				}
				assertDirectFLACDestinationMissing(t, destination)
				assertNoDirectFLACPartFiles(t, filepath.Dir(destination))
				return
			}
			if err != nil {
				t.Fatalf("download direct FLAC: %v", err)
			}
			got, err := os.ReadFile(destination)
			if err != nil {
				t.Fatal(err)
			}
			if written != int64(len(cleartext)) || !bytes.Equal(got, cleartext) {
				t.Fatalf("dynamic trailer was not stripped: written=%d size=%d", written, len(got))
			}
			assertNoDirectFLACPartFiles(t, filepath.Dir(destination))
		})
	}
}

func TestProbeDirectFLACProvesOutputBoundaryAndSnapshotsTail(t *testing.T) {
	cleartext := makeDirectFLACTestStream(t, 2048)
	dynamicTrailer := append(
		append([]byte{0xf0, 0x00, 0xff, 0x0f}, bytes.Repeat([]byte{0xa5}, 19)...),
		0x0e, 0x55, 0xff, 0xf0,
	)
	for _, test := range []struct {
		name           string
		tail           []byte
		wantTrailerLen int64
	}{
		{
			name:           "legacy",
			tail:           append([]byte(nil), knownDirectFLACTrailer...),
			wantTrailerLen: int64(len(knownDirectFLACTrailer)),
		},
		{
			name:           "production",
			tail:           append([]byte(nil), productionDirectFLACTrailer...),
			wantTrailerLen: int64(len(productionDirectFLACTrailer)),
		},
		{
			name:           "dynamic payload",
			tail:           dynamicTrailer,
			wantTrailerLen: int64(len(dynamicTrailer)),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := append(append([]byte(nil), cleartext...), test.tail...)
			header := mustDirectFLACHeader(t, raw)
			start, err := directFLACProbeRangeStart(header, int64(len(raw)))
			if err != nil {
				t.Fatal(err)
			}
			transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.Header.Get("Range") != "bytes="+
					strconvFormatInt(start)+"-"+strconvFormatInt(int64(len(raw)-1)) {
					t.Fatalf("Range = %q", req.Header.Get("Range"))
				}
				body := append([]byte(nil), raw[start:]...)
				return &http.Response{
					StatusCode: http.StatusPartialContent,
					Header: http.Header{
						"Content-Range": []string{
							"bytes " + strconvFormatInt(start) + "-" +
								strconvFormatInt(int64(len(raw)-1)) + "/" +
								strconvFormatInt(int64(len(raw))),
						},
					},
					Body:          io.NopCloser(bytes.NewReader(body)),
					ContentLength: int64(len(body)),
				}, nil
			})
			client := newClientWithEndpoints(time.Second, nil, kuwoEndpoints{})
			client.downloadHTTPClient = &http.Client{Transport: transport}

			got, err := client.probeDirectFLACTrailer(
				context.Background(),
				directFLACTestURL,
				int64(len(raw)),
				header,
			)
			if err != nil {
				t.Fatalf("probe output size: %v", err)
			}
			if got.outputSize != int64(len(cleartext)) {
				t.Fatalf("output size = %d, want %d", got.outputSize, len(cleartext))
			}
			if got.trailerLen != test.wantTrailerLen {
				t.Fatalf("trailer length = %d, want %d", got.trailerLen, test.wantTrailerLen)
			}
			if got != directFLACTail(t, raw) {
				t.Fatalf("probe snapshot = %+v, want %+v", got, directFLACTail(t, raw))
			}
		})
	}
}

func TestProbeDirectFLACTrailerUsesSTREAMINFOBoundedWindow(t *testing.T) {
	cleartext := makeDirectFLACTestStream(t, 64<<10)
	raw := append(append([]byte(nil), cleartext...), productionDirectFLACTrailer...)
	header := mustDirectFLACHeader(t, raw)
	start, err := directFLACProbeRangeStart(header, int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	if start <= 0 {
		t.Fatalf("test fixture did not exercise a bounded range: start=%d", start)
	}
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		wantRange := "bytes=" + strconvFormatInt(start) + "-" +
			strconvFormatInt(int64(len(raw)-1))
		if req.Header.Get("Range") != wantRange {
			t.Fatalf("Range = %q, want %q", req.Header.Get("Range"), wantRange)
		}
		body := append([]byte(nil), raw[start:]...)
		return &http.Response{
			StatusCode: http.StatusPartialContent,
			Header: http.Header{
				"Content-Range": []string{
					"bytes " + strconvFormatInt(start) + "-" +
						strconvFormatInt(int64(len(raw)-1)) + "/" +
						strconvFormatInt(int64(len(raw))),
				},
			},
			Body:          io.NopCloser(bytes.NewReader(body)),
			ContentLength: int64(len(body)),
		}, nil
	})
	client := newClientWithEndpoints(time.Second, nil, kuwoEndpoints{})
	client.downloadHTTPClient = &http.Client{Transport: transport}

	got, err := client.probeDirectFLACTrailer(
		context.Background(),
		directFLACTestURL,
		int64(len(raw)),
		header,
	)
	if err != nil {
		t.Fatalf("probe output size: %v", err)
	}
	if got.outputSize != int64(len(cleartext)) {
		t.Fatalf("output size = %d, want %d", got.outputSize, len(cleartext))
	}
}

func TestProbeDirectFLACTrailerPreservesTypedRateLimit(t *testing.T) {
	stream := makeDirectFLACTestStream(t, 4096)
	var requests atomic.Int32
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests.Add(1)
		return &http.Response{
			StatusCode:    http.StatusTooManyRequests,
			Header:        make(http.Header),
			Body:          io.NopCloser(bytes.NewReader(nil)),
			ContentLength: 0,
		}, nil
	})
	client := newClientWithEndpoints(time.Second, nil, kuwoEndpoints{})
	client.downloadHTTPClient = &http.Client{Transport: transport}
	client.downloadMaxRetries = 2

	_, err := client.probeDirectFLACTrailer(
		context.Background(),
		directFLACTestURL,
		int64(len(stream)),
		mustDirectFLACHeader(t, stream),
	)
	if !errors.Is(err, platform.ErrRateLimited) {
		t.Fatalf("error = %v, want typed rate limit", err)
	}
	if requests.Load() != 1 {
		t.Fatalf("requests = %d, want 1", requests.Load())
	}
}

func TestDirectFLACDownloaderRejectsResponseSizeMismatch(t *testing.T) {
	stream := makeDirectFLACTestStream(t, 4096)
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		response := directFLACResponse(stream)
		response.ContentLength++
		return response, nil
	})
	client := newClientWithEndpoints(time.Second, nil, kuwoEndpoints{})
	client.downloadHTTPClient = &http.Client{Transport: transport}
	client.downloadMaxRetries = 3
	destination := filepath.Join(t.TempDir(), "song.flac")

	_, err := client.directFLACDownloader(
		directFLACTestURL,
		int64(len(stream)),
		mustDirectFLACHeader(t, stream),
		directFLACTail(t, stream),
	)(
		context.Background(),
		&platform.DownloadInfo{URL: directFLACTestURL},
		destination,
		nil,
	)
	if !errors.Is(err, errDirectFLACIntegrity) {
		t.Fatalf("error = %v, want direct FLAC integrity error", err)
	}
	assertDirectFLACDestinationMissing(t, destination)
	assertNoDirectFLACPartFiles(t, filepath.Dir(destination))
}

func TestDirectFLACDownloaderRejectsMissingExpectedHeaderBeforeRequest(t *testing.T) {
	stream := makeDirectFLACTestStream(t, 4096)
	var requests atomic.Int32
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests.Add(1)
		return directFLACResponse(stream), nil
	})
	client := newClientWithEndpoints(time.Second, nil, kuwoEndpoints{})
	client.downloadHTTPClient = &http.Client{Transport: transport}
	destination := filepath.Join(t.TempDir(), "song.flac")

	_, err := client.directFLACDownloader(
		directFLACTestURL,
		int64(len(stream)),
		[42]byte{},
		directFLACTail(t, stream),
	)(
		context.Background(),
		&platform.DownloadInfo{URL: directFLACTestURL},
		destination,
		nil,
	)
	if !errors.Is(err, errDirectFLACIntegrity) {
		t.Fatalf("error = %v, want direct FLAC integrity error", err)
	}
	if requests.Load() != 0 {
		t.Fatalf("requests = %d, want 0", requests.Load())
	}
	assertDirectFLACDestinationMissing(t, destination)
}

func TestDirectFLACDownloaderRejectsInvalidStreamInfo(t *testing.T) {
	expectedStream := makeDirectFLACTestStream(t, 4096)
	stream := append([]byte(nil), expectedStream...)
	stream[7] = 33
	client := directFLACTestClient(stream)
	destination := filepath.Join(t.TempDir(), "song.flac")

	expectedHeader := mustDirectFLACHeader(t, expectedStream)
	_, err := client.directFLACDownloader(
		directFLACTestURL,
		int64(len(stream)),
		expectedHeader,
		directFLACTail(t, expectedStream),
	)(
		context.Background(),
		&platform.DownloadInfo{URL: directFLACTestURL},
		destination,
		nil,
	)
	if !errors.Is(err, errDirectFLACIntegrity) {
		t.Fatalf("error = %v, want direct FLAC integrity error", err)
	}
	assertDirectFLACDestinationMissing(t, destination)
	assertNoDirectFLACPartFiles(t, filepath.Dir(destination))
}

func TestDirectFLACDownloaderRetriesInterruptedBody(t *testing.T) {
	stream := makeDirectFLACTestStream(t, 4096)
	var requests atomic.Int32
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		response := directFLACResponse(stream)
		if requests.Add(1) == 1 {
			response.Body = io.NopCloser(&unexpectedEOFReader{
				data: stream[:len(stream)-1],
			})
		}
		return response, nil
	})
	client := newClientWithEndpoints(time.Second, nil, kuwoEndpoints{})
	client.downloadHTTPClient = &http.Client{Transport: transport}
	client.downloadMaxRetries = 2
	destination := filepath.Join(t.TempDir(), "song.flac")
	var progress []int64

	written, err := client.directFLACDownloader(
		directFLACTestURL,
		int64(len(stream)),
		mustDirectFLACHeader(t, stream),
		directFLACTail(t, stream),
	)(
		context.Background(),
		&platform.DownloadInfo{URL: directFLACTestURL},
		destination,
		func(written, total int64) {
			if total != int64(len(stream)) {
				t.Fatalf("progress total = %d, want %d", total, len(stream))
			}
			progress = append(progress, written)
		},
	)
	if err != nil {
		t.Fatalf("download after retry: %v", err)
	}
	if written != int64(len(stream)) || requests.Load() != 2 {
		t.Fatalf("written=%d requests=%d", written, requests.Load())
	}
	if len(progress) == 0 || progress[len(progress)-1] != int64(len(stream)) {
		t.Fatalf("progress = %v, want final output size", progress)
	}
	for index := 1; index < len(progress); index++ {
		if progress[index] <= progress[index-1] {
			t.Fatalf("progress is not a strict high-water mark: %v", progress)
		}
	}
	assertNoDirectFLACPartFiles(t, filepath.Dir(destination))
}

func TestDirectFLACDownloaderCancellationCleansPartialOutput(t *testing.T) {
	stream := makeDirectFLACTestStream(t, 4096)
	ctx, cancel := context.WithCancel(context.Background())
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		response := directFLACResponse(stream)
		response.Body = io.NopCloser(&cancelAfterFirstReader{
			data:   stream,
			cancel: cancel,
		})
		return response, nil
	})
	client := newClientWithEndpoints(time.Second, nil, kuwoEndpoints{})
	client.downloadHTTPClient = &http.Client{Transport: transport}
	client.downloadMaxRetries = 3
	destination := filepath.Join(t.TempDir(), "song.flac")

	_, err := client.directFLACDownloader(
		directFLACTestURL,
		int64(len(stream)),
		mustDirectFLACHeader(t, stream),
		directFLACTail(t, stream),
	)(
		ctx,
		&platform.DownloadInfo{URL: directFLACTestURL},
		destination,
		nil,
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context canceled", err)
	}
	assertDirectFLACDestinationMissing(t, destination)
	assertNoDirectFLACPartFiles(t, filepath.Dir(destination))
}

func TestDirectFLACDownloaderCancellationBeforeCommitDoesNotPublish(t *testing.T) {
	stream := makeDirectFLACTestStream(t, 4096)
	ctx, cancel := context.WithCancel(context.Background())
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			Header:        make(http.Header),
			Body:          io.NopCloser(&dataWithEOFReader{data: stream}),
			ContentLength: int64(len(stream)),
		}, nil
	})
	client := newClientWithEndpoints(time.Second, nil, kuwoEndpoints{})
	client.downloadHTTPClient = &http.Client{Transport: transport}
	destination := filepath.Join(t.TempDir(), "song.flac")

	_, err := client.directFLACDownloader(
		directFLACTestURL,
		int64(len(stream)),
		mustDirectFLACHeader(t, stream),
		directFLACTail(t, stream),
	)(
		ctx,
		&platform.DownloadInfo{URL: directFLACTestURL},
		destination,
		func(written, total int64) {
			if written > 0 {
				cancel()
			}
		},
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context canceled", err)
	}
	assertDirectFLACDestinationMissing(t, destination)
	assertNoDirectFLACPartFiles(t, filepath.Dir(destination))
}

func TestDirectFLACDownloaderDoesNotOverwriteExistingDestination(t *testing.T) {
	stream := makeDirectFLACTestStream(t, 4096)
	var requests atomic.Int32
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests.Add(1)
		return directFLACResponse(stream), nil
	})
	client := newClientWithEndpoints(time.Second, nil, kuwoEndpoints{})
	client.downloadHTTPClient = &http.Client{Transport: transport}
	destination := filepath.Join(t.TempDir(), "song.flac")
	if err := os.WriteFile(destination, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := client.directFLACDownloader(
		directFLACTestURL,
		int64(len(stream)),
		mustDirectFLACHeader(t, stream),
		directFLACTail(t, stream),
	)(
		context.Background(),
		&platform.DownloadInfo{URL: directFLACTestURL},
		destination,
		nil,
	)
	if !errors.Is(err, os.ErrExist) {
		t.Fatalf("error = %v, want destination exists", err)
	}
	got, readErr := os.ReadFile(destination)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != "existing" {
		t.Fatalf("destination changed to %q", got)
	}
	if requests.Load() != 0 {
		t.Fatalf("requests = %d, want 0", requests.Load())
	}
	assertNoDirectFLACPartFiles(t, filepath.Dir(destination))
}

func TestDirectFLACDownloaderRejectsUnsafeRedirectWithoutLeakingURL(t *testing.T) {
	stream := makeDirectFLACTestStream(t, 4096)
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusFound,
			Header: http.Header{
				"Location": []string{"http://127.0.0.1/private.flac"},
			},
			Body:          io.NopCloser(bytes.NewReader(nil)),
			ContentLength: 0,
		}, nil
	})
	client := newClientWithEndpoints(time.Second, nil, kuwoEndpoints{})
	client.downloadHTTPClient = &http.Client{Transport: transport}
	client.downloadMaxRetries = 1
	destination := filepath.Join(t.TempDir(), "song.flac")

	_, err := client.directFLACDownloader(
		directFLACTestURL,
		int64(len(stream)),
		mustDirectFLACHeader(t, stream),
		directFLACTail(t, stream),
	)(
		context.Background(),
		&platform.DownloadInfo{URL: directFLACTestURL},
		destination,
		nil,
	)
	if !errors.Is(err, errUnsafeMediaURL) {
		t.Fatalf("error = %v, want unsafe media URL", err)
	}
	if strings.Contains(err.Error(), "https://") || strings.Contains(err.Error(), "127.0.0.1") {
		t.Fatalf("error leaked a media URL: %v", err)
	}
	assertDirectFLACDestinationMissing(t, destination)
	assertNoDirectFLACPartFiles(t, filepath.Dir(destination))
}

func TestDirectFLACHTTPClientBoundsRequestLifetime(t *testing.T) {
	for _, test := range []struct {
		name        string
		baseTimeout time.Duration
		want        time.Duration
	}{
		{
			name: "unbounded client",
			want: 20 * time.Minute,
		},
		{
			name:        "shorter client timeout",
			baseTimeout: 5 * time.Minute,
			want:        5 * time.Minute,
		},
		{
			name:        "excessive client timeout",
			baseTimeout: 24 * time.Hour,
			want:        20 * time.Minute,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := directFLACHTTPClient(&http.Client{
				Timeout: test.baseTimeout,
			})
			if got.Timeout != test.want {
				t.Fatalf("timeout = %v, want %v", got.Timeout, test.want)
			}
		})
	}
}

func directFLACTestClient(stream []byte) *Client {
	client := newClientWithEndpoints(time.Second, nil, kuwoEndpoints{})
	client.downloadHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return directFLACResponse(stream), nil
	})}
	return client
}

func TestDirectFLACDownloaderRejectsTrailerStateChangedSinceProbe(t *testing.T) {
	cleartext := makeDirectFLACTestStream(t, 4096)
	knownTrailerStream := append(
		append([]byte(nil), cleartext...),
		knownDirectFLACTrailer...,
	)
	differentTrailerStream := append(
		append([]byte(nil), cleartext...),
		bytes.Repeat([]byte{0xa5}, len(knownDirectFLACTrailer))...,
	)
	productionTrailerStream := append(
		append([]byte(nil), cleartext...),
		productionDirectFLACTrailer...,
	)
	changedProductionTrailerStream := append(
		[]byte(nil),
		productionTrailerStream...,
	)
	changedProductionTrailerStream[len(cleartext)+4] ^= 0x01
	cases := []struct {
		name           string
		expectedStream []byte
		actualStream   []byte
	}{
		{
			name:           "expected trailer disappeared",
			expectedStream: knownTrailerStream,
			actualStream:   differentTrailerStream,
		},
		{
			name:           "unexpected trailer appeared",
			expectedStream: cleartext,
			actualStream:   knownTrailerStream,
		},
		{
			name:           "production content changed outside legacy window",
			expectedStream: productionTrailerStream,
			actualStream:   changedProductionTrailerStream,
		},
		{
			name:           "production trailer changed to legacy trailer",
			expectedStream: productionTrailerStream,
			actualStream:   knownTrailerStream,
		},
		{
			name:           "legacy trailer changed to production trailer",
			expectedStream: knownTrailerStream,
			actualStream:   productionTrailerStream,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			client := directFLACTestClient(test.actualStream)
			destination := filepath.Join(t.TempDir(), "song.flac")

			_, err := client.directFLACDownloader(
				directFLACTestURL,
				int64(len(test.expectedStream)),
				mustDirectFLACHeader(t, test.expectedStream),
				directFLACTail(t, test.expectedStream),
			)(
				context.Background(),
				&platform.DownloadInfo{URL: directFLACTestURL},
				destination,
				nil,
			)
			if !errors.Is(err, errDirectFLACIntegrity) {
				t.Fatalf("error = %v, want direct FLAC integrity error", err)
			}
			assertDirectFLACDestinationMissing(t, destination)
			assertNoDirectFLACPartFiles(t, filepath.Dir(destination))
		})
	}
}

func TestDirectFLACDownloaderRejectsMiddleFrameCorruption(t *testing.T) {
	cleartext := makeTestFLAC(
		t,
		128<<10,
		48000,
		24,
		2,
		213*time.Second,
	)
	expectedStream := append(
		append([]byte(nil), cleartext...),
		knownDirectFLACTrailer...,
	)
	actualStream := append([]byte(nil), expectedStream...)
	streamInfo := mustDirectFLACStreamInfo(t, cleartext)
	firstFrame := testFLACFirstFrameOffset(t, cleartext)
	mutationIndex := firstFrame + streamInfo.minFrameSize - 1
	probe := directFLACTail(t, expectedStream)
	if int64(mutationIndex) >= probe.rangeStart {
		t.Fatalf(
			"corruption offset %d is inside verified tail starting at %d",
			mutationIndex,
			probe.rangeStart,
		)
	}
	actualStream[mutationIndex] ^= 0x01

	client := directFLACTestClient(actualStream)
	destination := filepath.Join(t.TempDir(), "song.flac")
	_, err := client.directFLACDownloader(
		directFLACTestURL,
		int64(len(expectedStream)),
		mustDirectFLACHeader(t, expectedStream),
		probe,
	)(
		context.Background(),
		&platform.DownloadInfo{
			URL:  directFLACTestURL,
			Size: int64(len(cleartext)),
		},
		destination,
		nil,
	)
	if !errors.Is(err, errDirectFLACIntegrity) {
		t.Fatalf("error = %v, want direct FLAC integrity error", err)
	}
	assertDirectFLACDestinationMissing(t, destination)
	assertNoDirectFLACPartFiles(t, filepath.Dir(destination))
}

func TestDirectFLACDownloaderRejectsStreamInfoChangedSinceProbe(t *testing.T) {
	expectedStream := makeDirectFLACTestStream(t, 4096)
	changedStream := append([]byte(nil), expectedStream...)
	const changedSampleRate = 96000
	packed := binary.BigEndian.Uint64(changedStream[18:26])
	packed &^= uint64(0xfffff) << 44
	packed |= uint64(changedSampleRate) << 44
	binary.BigEndian.PutUint64(changedStream[18:26], packed)
	client := directFLACTestClient(changedStream)
	destination := filepath.Join(t.TempDir(), "song.flac")

	_, err := client.directFLACDownloader(
		directFLACTestURL,
		int64(len(changedStream)),
		mustDirectFLACHeader(t, expectedStream),
		directFLACTail(t, expectedStream),
	)(
		context.Background(),
		&platform.DownloadInfo{URL: directFLACTestURL},
		destination,
		nil,
	)
	if !errors.Is(err, errDirectFLACIntegrity) {
		t.Fatalf("error = %v, want direct FLAC integrity error", err)
	}
	assertDirectFLACDestinationMissing(t, destination)
	assertNoDirectFLACPartFiles(t, filepath.Dir(destination))
}

func TestDirectFLACDownloaderRejectsURLChangedSinceProbeBeforeRequest(t *testing.T) {
	stream := makeDirectFLACTestStream(t, 4096)
	var requests atomic.Int32
	client := newClientWithEndpoints(time.Second, nil, kuwoEndpoints{})
	client.downloadHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests.Add(1)
		return directFLACResponse(stream), nil
	})}
	destination := filepath.Join(t.TempDir(), "song.flac")

	_, err := client.directFLACDownloader(
		directFLACTestURL,
		int64(len(stream)),
		mustDirectFLACHeader(t, stream),
		directFLACTail(t, stream),
	)(
		context.Background(),
		&platform.DownloadInfo{URL: "https://kw-lw.kuwo.cn/audio/changed.flac"},
		destination,
		nil,
	)
	if !errors.Is(err, errDirectFLACIntegrity) {
		t.Fatalf("error = %v, want direct FLAC integrity error", err)
	}
	if requests.Load() != 0 {
		t.Fatalf("requests = %d, want 0", requests.Load())
	}
	assertDirectFLACDestinationMissing(t, destination)
}

func TestDirectFLACDownloaderRejectsNonSemanticStreamInfoByteChangedSinceProbe(t *testing.T) {
	expectedStream := makeDirectFLACTestStream(t, 4096)
	changedStream := append([]byte(nil), expectedStream...)
	changedStream[26] ^= 0x01 // STREAMINFO MD5 byte; parsed audio properties stay identical.
	expectedInfo := mustDirectFLACStreamInfo(t, expectedStream)
	changedInfo := mustDirectFLACStreamInfo(t, changedStream)
	if changedInfo != expectedInfo {
		t.Fatalf("test fixture changed semantic STREAMINFO: got %+v, want %+v", changedInfo, expectedInfo)
	}
	client := directFLACTestClient(changedStream)
	destination := filepath.Join(t.TempDir(), "song.flac")

	_, err := client.directFLACDownloader(
		directFLACTestURL,
		int64(len(changedStream)),
		mustDirectFLACHeader(t, expectedStream),
		directFLACTail(t, expectedStream),
	)(
		context.Background(),
		&platform.DownloadInfo{URL: directFLACTestURL},
		destination,
		nil,
	)
	if !errors.Is(err, errDirectFLACIntegrity) {
		t.Fatalf("error = %v, want direct FLAC integrity error", err)
	}
	assertDirectFLACDestinationMissing(t, destination)
	assertNoDirectFLACPartFiles(t, filepath.Dir(destination))
}

func TestDirectFLACDownloaderRejectsDynamicPayloadChangedSinceProbe(t *testing.T) {
	cleartext := makeDirectFLACTestStream(t, 4096)
	expectedStream := append(
		append([]byte(nil), cleartext...),
		knownDirectFLACTrailer...,
	)
	changedStream := append([]byte(nil), expectedStream...)
	changedStream[len(cleartext)+6] ^= 0x01
	client := directFLACTestClient(changedStream)
	destination := filepath.Join(t.TempDir(), "song.flac")

	_, err := client.directFLACDownloader(
		directFLACTestURL,
		int64(len(expectedStream)),
		mustDirectFLACHeader(t, expectedStream),
		directFLACTail(t, expectedStream),
	)(
		context.Background(),
		&platform.DownloadInfo{URL: directFLACTestURL},
		destination,
		nil,
	)
	if !errors.Is(err, errDirectFLACIntegrity) {
		t.Fatalf("error = %v, want direct FLAC integrity error", err)
	}
	assertDirectFLACDestinationMissing(t, destination)
	assertNoDirectFLACPartFiles(t, filepath.Dir(destination))
}

func TestDirectFLACDownloaderDoesNotOverwriteDestinationCreatedDuringProgress(t *testing.T) {
	stream := makeDirectFLACTestStream(t, 4096)
	client := directFLACTestClient(stream)
	destination := filepath.Join(t.TempDir(), "song.flac")
	var createErr error
	created := false

	_, err := client.directFLACDownloader(
		directFLACTestURL,
		int64(len(stream)),
		mustDirectFLACHeader(t, stream),
		directFLACTail(t, stream),
	)(
		context.Background(),
		&platform.DownloadInfo{URL: directFLACTestURL},
		destination,
		func(written, total int64) {
			if !created && written > 0 {
				created = true
				createErr = os.WriteFile(destination, []byte("racer"), 0o600)
			}
		},
	)
	if createErr != nil {
		t.Fatalf("create competing destination: %v", createErr)
	}
	if !errors.Is(err, os.ErrExist) {
		t.Fatalf("error = %v, want destination exists", err)
	}
	got, readErr := os.ReadFile(destination)
	if readErr != nil {
		t.Fatalf("read competing destination: %v", readErr)
	}
	if string(got) != "racer" {
		t.Fatalf("competing destination changed to %q", got)
	}
	assertNoDirectFLACPartFiles(t, filepath.Dir(destination))
}

func directFLACResponse(stream []byte) *http.Response {
	return &http.Response{
		StatusCode:    http.StatusOK,
		Header:        make(http.Header),
		Body:          io.NopCloser(&chunkReader{data: append([]byte(nil), stream...), chunkSize: 997}),
		ContentLength: int64(len(stream)),
	}
}

type dataWithEOFReader struct {
	data []byte
	read bool
}

func (reader *dataWithEOFReader) Read(buffer []byte) (int, error) {
	if reader.read {
		return 0, io.EOF
	}
	reader.read = true
	return copy(buffer, reader.data), io.EOF
}

func makeDirectFLACTestStream(t *testing.T, size int) []byte {
	t.Helper()
	return makeTestFLAC(t, size, 48000, 24, 2, time.Second)
}

func productionDirectFLACHeader(t *testing.T) [42]byte {
	t.Helper()
	data := mustDecodeHex(t, productionDirectFLACHeaderHex)
	var header [42]byte
	if copy(header[:], data) != len(header) || len(data) != len(header) {
		t.Fatalf("production FLAC header length = %d, want %d", len(data), len(header))
	}
	return header
}

func mustDecodeHex(t *testing.T, value string) []byte {
	t.Helper()
	data, err := hex.DecodeString(value)
	if err != nil {
		t.Fatalf("decode test hex: %v", err)
	}
	return data
}

func mustDirectFLACStreamInfo(t *testing.T, stream []byte) flacStreamInfo {
	t.Helper()
	info, err := parseFLACStreamInfo(stream)
	if err != nil {
		t.Fatalf("parse test FLAC STREAMINFO: %v", err)
	}
	return info
}

func mustDirectFLACHeader(t *testing.T, stream []byte) [42]byte {
	t.Helper()
	if len(stream) < 42 {
		t.Fatalf("test FLAC size = %d, want at least 42", len(stream))
	}
	if _, err := parseFLACStreamInfo(stream[:42]); err != nil {
		t.Fatalf("parse test FLAC STREAMINFO: %v", err)
	}
	var header [42]byte
	copy(header[:], stream[:42])
	return header
}

func directFLACTail(t *testing.T, stream []byte) directFLACTrailerProbe {
	t.Helper()
	header := mustDirectFLACHeader(t, stream)
	rawSize := int64(len(stream))
	rangeStart, err := directFLACProbeRangeStart(header, rawSize)
	if err != nil {
		t.Fatalf("derive test FLAC tail range: %v", err)
	}
	tail := stream[rangeStart:]
	analysis, err := analyzeDirectFLACTail(
		header,
		rawSize,
		rangeStart,
		tail,
	)
	if err != nil {
		t.Fatalf("analyze test FLAC tail: %v", err)
	}
	return directFLACTrailerProbe{
		rawSize:    rawSize,
		outputSize: analysis.outputSize,
		rangeStart: rangeStart,
		trailerLen: analysis.trailerLen,
		tailHash:   sha256.Sum256(tail),
		header:     header,
	}
}

func uncheckedDirectFLACProbe(
	t *testing.T,
	stream []byte,
	outputSize int64,
	trailerLen int64,
) directFLACTrailerProbe {
	t.Helper()
	header := mustDirectFLACHeader(t, stream)
	rawSize := int64(len(stream))
	rangeStart, err := directFLACProbeRangeStart(header, rawSize)
	if err != nil {
		t.Fatalf("derive unchecked test FLAC range: %v", err)
	}
	return directFLACTrailerProbe{
		rawSize:    rawSize,
		outputSize: outputSize,
		rangeStart: rangeStart,
		trailerLen: trailerLen,
		tailHash:   sha256.Sum256(stream[rangeStart:]),
		header:     header,
	}
}

func testFLACFirstFrameOffset(t *testing.T, stream []byte) int {
	t.Helper()
	if len(stream) < 8 || !bytes.Equal(stream[:4], []byte("fLaC")) {
		t.Fatal("invalid test FLAC stream")
	}
	offset := 4
	for {
		if offset+4 > len(stream) {
			t.Fatal("truncated test FLAC metadata")
		}
		isLast := stream[offset]&0x80 != 0
		length := int(stream[offset+1])<<16 |
			int(stream[offset+2])<<8 |
			int(stream[offset+3])
		offset += 4 + length
		if offset > len(stream) {
			t.Fatal("test FLAC metadata exceeds stream")
		}
		if isLast {
			return offset
		}
	}
}

func assertDirectFLACDestinationMissing(t *testing.T, destination string) {
	t.Helper()
	if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("destination stat error = %v, want not exist", err)
	}
}

func assertNoDirectFLACPartFiles(t *testing.T, directory string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(directory, ".*.direct-flac-part-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("leftover direct FLAC part files: %v", matches)
	}
}

func strconvFormatInt(value int64) string { return strconv.FormatInt(value, 10) }
