package kuwo

import (
	"bytes"
	"context"
	"encoding/binary"
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
	assertNoDirectFLACPartFiles(t, filepath.Dir(destination))
}

func TestDirectFLACDownloaderPreservesNonMatchingTrailer(t *testing.T) {
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
	got, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if written != int64(len(raw)) || !bytes.Equal(got, raw) {
		t.Fatalf("non-matching trailer changed: written=%d size=%d", written, len(got))
	}
	assertNoDirectFLACPartFiles(t, filepath.Dir(destination))
}

func TestProbeDirectFLACOutputSizeMatchesOnlyKnownTrailer(t *testing.T) {
	cleartext := makeDirectFLACTestStream(t, 2048)
	for _, test := range []struct {
		name string
		tail []byte
		want int64
	}{
		{
			name: "known",
			tail: append([]byte(nil), knownDirectFLACTrailer...),
			want: int64(len(cleartext)),
		},
		{
			name: "different",
			tail: append([]byte(nil), bytes.Repeat([]byte{0xa5}, len(knownDirectFLACTrailer))...),
			want: int64(len(cleartext) + len(knownDirectFLACTrailer)),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := append(append([]byte(nil), cleartext...), test.tail...)
			transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
				start := int64(len(raw) - len(test.tail))
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
			)
			if err != nil {
				t.Fatalf("probe output size: %v", err)
			}
			if got.outputSize != test.want {
				t.Fatalf("output size = %d, want %d", got.outputSize, test.want)
			}
			if got.strip != (test.name == "known") {
				t.Fatalf("strip = %t for %s trailer", got.strip, test.name)
			}
			if got.tail != directFLACTail(t, raw) {
				t.Fatalf("tail = %x, want %x", got.tail, test.tail)
			}
		})
	}
}

func TestProbeDirectFLACTrailerPreservesTypedRateLimit(t *testing.T) {
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
		4096,
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
	stream := makeDirectFLACTestStream(t, 4096)
	stream[7] = 33
	client := directFLACTestClient(stream)
	destination := filepath.Join(t.TempDir(), "song.flac")

	expectedHeader := mustDirectFLACHeader(t, makeDirectFLACTestStream(t, 4096))
	_, err := client.directFLACDownloader(
		directFLACTestURL,
		int64(len(stream)),
		expectedHeader,
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

func TestDirectFLACDownloaderRetriesInterruptedBody(t *testing.T) {
	stream := makeDirectFLACTestStream(t, 4096)
	var requests atomic.Int32
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		response := directFLACResponse(stream)
		if requests.Add(1) == 1 {
			response.Body = io.NopCloser(&unexpectedEOFReader{data: stream[:128]})
		}
		return response, nil
	})
	client := newClientWithEndpoints(time.Second, nil, kuwoEndpoints{})
	client.downloadHTTPClient = &http.Client{Transport: transport}
	client.downloadMaxRetries = 2
	destination := filepath.Join(t.TempDir(), "song.flac")

	written, err := client.directFLACDownloader(
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
	if err != nil {
		t.Fatalf("download after retry: %v", err)
	}
	if written != int64(len(stream)) || requests.Load() != 2 {
		t.Fatalf("written=%d requests=%d", written, requests.Load())
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
			expectedStream: differentTrailerStream,
			actualStream:   knownTrailerStream,
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

func TestDirectFLACDownloaderRejectsDifferentUnknownTrailerSinceProbe(t *testing.T) {
	cleartext := makeDirectFLACTestStream(t, 4096)
	expectedStream := append(
		append([]byte(nil), cleartext...),
		bytes.Repeat([]byte{0xa5}, len(knownDirectFLACTrailer))...,
	)
	changedStream := append(
		append([]byte(nil), cleartext...),
		bytes.Repeat([]byte{0x5a}, len(knownDirectFLACTrailer))...,
	)
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
	if size < 42 {
		t.Fatalf("test FLAC size = %d, want at least 42", size)
	}
	const (
		sampleRate    = 44100
		bitsPerSample = 16
		channels      = 2
	)
	stream := make([]byte, size)
	copy(stream, "fLaC")
	stream[4] = 0x80
	stream[7] = 34
	binary.BigEndian.PutUint16(stream[8:10], 4096)
	binary.BigEndian.PutUint16(stream[10:12], 4096)
	totalSamples := uint64(sampleRate * 60)
	packed := uint64(sampleRate)<<44 |
		uint64(channels-1)<<41 |
		uint64(bitsPerSample-1)<<36 |
		totalSamples
	binary.BigEndian.PutUint64(stream[18:26], packed)
	for index := 42; index < len(stream); index++ {
		stream[index] = byte(index*29 + 11)
	}
	return stream
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

func directFLACTail(t *testing.T, stream []byte) [15]byte {
	t.Helper()
	if len(stream) < len(knownDirectFLACTrailer) {
		t.Fatalf(
			"test FLAC size = %d, want at least %d",
			len(stream),
			len(knownDirectFLACTrailer),
		)
	}
	var tail [15]byte
	copy(tail[:], stream[len(stream)-len(tail):])
	return tail
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
