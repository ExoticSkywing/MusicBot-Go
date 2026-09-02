package youtubemusic

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/liuran001/MusicBot-Go/bot/platform"
)

func TestYouTubeMusicLiveDownloadInfo(t *testing.T) {
	if os.Getenv("MUSICBOT_YTM_LIVE_TEST") == "" {
		t.Skip("set MUSICBOT_YTM_LIVE_TEST=1 to run live YouTube Music checks")
	}
	client := NewClient("", 20*time.Second, nil)
	for _, videoID := range []string{"UQ8cXH7qbVU", "YQHsXMglC9A"} {
		t.Run(videoID, func(t *testing.T) {
			info, err := client.GetDownloadInfo(context.Background(), videoID, platform.QualityHigh)
			if err != nil {
				t.Fatalf("GetDownloadInfo() error = %v", err)
			}
			if info == nil || info.URL == "" || info.Size <= 0 {
				t.Fatalf("invalid download info: %+v", info)
			}
			req, err := http.NewRequest(http.MethodGet, info.URL, nil)
			if err != nil {
				t.Fatalf("build range request: %v", err)
			}
			for key, value := range info.Headers {
				req.Header.Set(key, value)
			}
			req.Header.Set("Range", "bytes=0-1023")
			resp, err := client.httpClient.Do(req)
			if err != nil {
				t.Fatalf("range request: %v", err)
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(io.LimitReader(resp.Body, 2048))
			if err != nil {
				t.Fatalf("read range response: %v", err)
			}
			if resp.StatusCode != http.StatusPartialContent || len(body) != 1024 {
				t.Fatalf("range response status=%d bytes=%d", resp.StatusCode, len(body))
			}
		})
	}
}

func TestPreferIPv6DialContextUsesIPv6WhenAvailable(t *testing.T) {
	var networks []string
	dial := preferIPv6DialContext(func(_ context.Context, network, _ string) (net.Conn, error) {
		networks = append(networks, network)
		left, right := net.Pipe()
		_ = right.Close()
		return left, nil
	})
	conn, err := dial(t.Context(), "tcp", "example.test:443")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_ = conn.Close()
	if !reflect.DeepEqual(networks, []string{"tcp6"}) {
		t.Fatalf("networks = %v, want [tcp6]", networks)
	}
}

func TestPreferIPv6DialContextFallsBackToIPv4(t *testing.T) {
	var networks []string
	dial := preferIPv6DialContext(func(_ context.Context, network, _ string) (net.Conn, error) {
		networks = append(networks, network)
		if network == "tcp6" {
			return nil, fmt.Errorf("IPv6 unavailable")
		}
		left, right := net.Pipe()
		_ = right.Close()
		return left, nil
	})
	conn, err := dial(t.Context(), "tcp", "example.test:443")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_ = conn.Close()
	if !reflect.DeepEqual(networks, []string{"tcp6", "tcp4"}) {
		t.Fatalf("networks = %v, want [tcp6 tcp4]", networks)
	}
}

func TestGetDownloadInfoPrefersVisionOS(t *testing.T) {
	var playerClients []string
	client := NewClient("", 0, nil)
	client.httpClient = &http.Client{Transport: youtubeMusicRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch {
		case req.Method == http.MethodGet && req.URL.Path == "/watch":
			return youtubeMusicTestResponse(req, http.StatusOK, `{"visitorData":"VISITOR"}`), nil
		case req.Method == http.MethodPost && req.URL.Path == "/youtubei/v1/player":
			playerClients = append(playerClients, req.Header.Get("X-YouTube-Client-Name"))
			if got := req.Header.Get("User-Agent"); got != visionOSUserAgent {
				t.Fatalf("VISIONOS player User-Agent = %q", got)
			}
			return youtubeMusicTestResponse(req, http.StatusOK, playableYouTubeMusicResponse("vision")), nil
		default:
			return nil, fmt.Errorf("unexpected request: %s %s", req.Method, req.URL.String())
		}
	})}

	info, err := client.GetDownloadInfo(context.Background(), "UQ8cXH7qbVU", platform.QualityHigh)
	if err != nil {
		t.Fatalf("GetDownloadInfo() error = %v", err)
	}
	if got := info.Headers["User-Agent"]; got != visionOSUserAgent {
		t.Fatalf("download User-Agent = %q, want VISIONOS", got)
	}
	if want := []string{visionOSClientNumber}; !reflect.DeepEqual(playerClients, want) {
		t.Fatalf("player clients = %v, want %v", playerClients, want)
	}
}

func TestGetDownloadInfoFallsBackToAndroidVR(t *testing.T) {
	var playerClients []string
	client := NewClient("", 0, nil)
	client.httpClient = &http.Client{Transport: youtubeMusicRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch {
		case req.Method == http.MethodGet && req.URL.Path == "/watch":
			return youtubeMusicTestResponse(req, http.StatusOK, `{"visitorData":"VISITOR"}`), nil
		case req.Method == http.MethodPost && req.URL.Path == "/youtubei/v1/player":
			playerClient := req.Header.Get("X-YouTube-Client-Name")
			playerClients = append(playerClients, playerClient)
			switch playerClient {
			case visionOSClientNumber:
				return youtubeMusicTestResponse(req, http.StatusOK, `{"playabilityStatus":{"status":"LOGIN_REQUIRED","reason":"Sign in to confirm you’re not a bot"}}`), nil
			case androidVRClientNumber:
				if got := req.Header.Get("User-Agent"); got != androidVRUserAgent {
					t.Fatalf("ANDROID_VR player User-Agent = %q", got)
				}
				return youtubeMusicTestResponse(req, http.StatusOK, playableYouTubeMusicResponse("android")), nil
			default:
				return nil, fmt.Errorf("unexpected player client %q", playerClient)
			}
		default:
			return nil, fmt.Errorf("unexpected request: %s %s", req.Method, req.URL.String())
		}
	})}

	info, err := client.GetDownloadInfo(context.Background(), "UQ8cXH7qbVU", platform.QualityHigh)
	if err != nil {
		t.Fatalf("GetDownloadInfo() error = %v", err)
	}
	if got := info.Headers["User-Agent"]; got != androidVRUserAgent {
		t.Fatalf("download User-Agent = %q, want ANDROID_VR", got)
	}
	if want := []string{visionOSClientNumber, androidVRClientNumber}; !reflect.DeepEqual(playerClients, want) {
		t.Fatalf("player clients = %v, want %v", playerClients, want)
	}
}

func playableYouTubeMusicResponse(streamID string) string {
	return fmt.Sprintf(`{
		"playabilityStatus":{"status":"OK"},
		"streamingData":{
			"expiresInSeconds":"3600",
			"adaptiveFormats":[{
				"url":"https://rr1---sn.googlevideo.com/videoplayback?id=%s",
				"mimeType":"audio/mp4; codecs=\"mp4a.40.2\"",
				"averageBitrate":129000,
				"contentLength":"12345"
			}]
		},
		"videoDetails":{
			"videoId":"UQ8cXH7qbVU",
			"title":"Test",
			"lengthSeconds":"10",
			"author":"Artist"
		}
	}`, streamID)
}
