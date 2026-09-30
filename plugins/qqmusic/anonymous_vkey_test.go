package qqmusic

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/liuran001/MusicBot-Go/bot/platform"
)

type anonymousTransport func(*http.Request) (*http.Response, error)

func (f anonymousTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestAnonymousVKeyFallback(t *testing.T) {
	for _, scenario := range []string{"full", "aac", "wrong filename", "api error", "authenticated", "canceled", "certificate", "account changed", "primary success"} {
		t.Run(scenario, func(t *testing.T) {
			c := NewClient("device=test", time.Second, nil, false, 0, nil)
			legacy := 0
			c.httpClient.Transport = anonymousTransport(func(req *http.Request) (*http.Response, error) {
				body := `{"code":0,"req":{"code":0,"data":{"midurlinfo":[]}}}`
				if strings.HasSuffix(req.URL.Path, "musics.fcg") {
					switch scenario {
					case "canceled":
						return nil, context.Canceled
					case "certificate":
						return nil, &tls.CertificateVerificationError{Err: errors.New("untrusted")}
					case "account changed":
						c.mu.Lock()
						c.cookie = "uin=new"
						c.mu.Unlock()
					case "primary success":
						body = `{"req":{"data":{"midurlinfo":[{"purl":"M500media.mp3?vkey=key","vkey":"key"}]}}}`
					}
				} else {
					legacy++
					if req.Header.Get("Cookie") != "" || req.Method != http.MethodGet {
						t.Fatal("fallback must be anonymous GET")
					}
					var payload map[string]any
					if err := json.Unmarshal([]byte(req.URL.Query().Get("data")), &payload); err != nil {
						t.Fatal(err)
					}
					call := payload["req_0"].(map[string]any)
					param := call["param"].(map[string]any)
					if call["method"] != "CgiGetVkey" || param["uin"] != "0" || param["filename"].([]any)[0] != "M500media.mp3" {
						t.Fatal("wrong fallback contract")
					}
					body = `{"code":0,"req_0":{"code":0,"data":{"midurlinfo":[{"filename":"M500media.mp3","purl":"M500media.mp3?vkey=k","vkey":"k"}]}}}`
					switch scenario {
					case "aac":
						body = strings.ReplaceAll(body, "M500media.mp3?vkey", "C400media.m4a?vkey")
					case "wrong filename":
						body = strings.ReplaceAll(body, "M500media", "M500other")
					case "api error":
						body = strings.Replace(body, `"code":0`, `"code":1000`, 1)
					}
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
			})
			auth := ""
			if scenario == "authenticated" {
				auth = "account-key"
			}
			got, err := c.GetVKey(context.Background(), "song", "media", "M500", "mp3", "0", auth)
			if scenario == "full" || scenario == "primary success" {
				if err != nil || got == "" {
					t.Fatalf("result=%q err=%v", got, err)
				}
			} else if err == nil || got != "" {
				t.Fatalf("accepted invalid result %q, %v", got, err)
			}
			if scenario == "aac" || scenario == "wrong filename" || scenario == "api error" {
				if !errors.Is(err, platform.ErrUnavailable) {
					t.Fatal(err)
				}
			}
			want := 0
			if scenario == "full" || scenario == "aac" || scenario == "wrong filename" || scenario == "api error" {
				want = 1
			}
			if legacy != want {
				t.Fatalf("legacy calls=%d want=%d", legacy, want)
			}
		})
	}
}
