package applemusic

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/liuran001/MusicBot-Go/bot/platform"
)

type qualityRoundTripFunc func(*http.Request) (*http.Response, error)

func (f qualityRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func qualityTestResponse(req *http.Request, body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}

func TestGetDownloadInfoEnhancedQualityRequiresWrapper(t *testing.T) {
	for _, quality := range []platform.Quality{
		platform.QualityLossless,
		platform.QualityHiRes,
		platform.QualityAtmos,
	} {
		t.Run(quality.String(), func(t *testing.T) {
			var requests atomic.Int32
			client := &Client{
				httpClient: &http.Client{Transport: qualityRoundTripFunc(func(req *http.Request) (*http.Response, error) {
					requests.Add(1)
					return qualityTestResponse(req, `{"data":[{"id":"1","attributes":{}}]}`), nil
				})},
				developerToken:     "test-token",
				storefront:         "us",
				language:           "en-US",
				storefrontDetected: true,
			}

			_, err := client.GetDownloadInfo(context.Background(), "1", quality)
			if err == nil || !strings.Contains(err.Error(), "wrapper host not configured") {
				t.Fatalf("GetDownloadInfo(%v) error=%v, want wrapper configuration error", quality, err)
			}
			if got := requests.Load(); got != 1 {
				t.Fatalf("GetDownloadInfo(%v) made %d requests, want catalog check only", quality, got)
			}
		})
	}
}

func TestGetDownloadInfoLosslessDoesNotFallbackToAAC(t *testing.T) {
	const songResponse = `{"data":[{"id":"1","attributes":{"extendedAssetUrls":{"enhancedHls":"https://cdn.test/master.m3u8"}}}]}`
	const aacOnlyMaster = `#EXTM3U
#EXT-X-STREAM-INF:AVERAGE-BANDWIDTH=260342,BANDWIDTH=274168,CODECS="mp4a.40.2",AUDIO="audio-stereo-256"
aac.m3u8
`

	client := &Client{
		httpClient: &http.Client{Transport: qualityRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.Host == "cdn.test" {
				return qualityTestResponse(req, aacOnlyMaster), nil
			}
			return qualityTestResponse(req, songResponse), nil
		})},
		developerToken:     "test-token",
		storefront:         "us",
		language:           "en-US",
		storefrontDetected: true,
		wrapperHost:        "wrapper.test",
	}

	_, err := client.GetDownloadInfo(context.Background(), "1", platform.QualityLossless)
	if err == nil || !strings.Contains(err.Error(), "no suitable enhancedHls variant for quality lossless") {
		t.Fatalf("GetDownloadInfo(lossless) error=%v, want strict no-suitable-variant error", err)
	}
}
