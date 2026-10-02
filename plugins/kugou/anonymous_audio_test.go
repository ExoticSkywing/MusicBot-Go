package kugou

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/guohuiyuan/music-lib/model"
	"github.com/liuran001/MusicBot-Go/bot/platform"
)

func TestAnonymousAudioWithoutAccount(t *testing.T) {
	for _, scenario := range []string{"full", "preview flag", "short metadata", "short file", "html", "wrong hash", "invalid range", "authenticated", "canceled", "account changed"} {
		t.Run(scenario, func(t *testing.T) {
			const hash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			c := NewClient("device=test", nil)
			calls, cdns := 0, 0
			c.defaultHTTPClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.Header.Get("Cookie") != "" {
					t.Fatal("anonymous request leaked cookie")
				}
				if req.URL.Host == "m.kugou.com" {
					if req.URL.Query().Get("cmd") != "playInfo" || req.URL.Query().Get("hash") != hash {
						t.Fatal("wrong mobile contract")
					}
					data := map[string]any{"url": "https://audio.kugou.com/a.mp3", "hash": hash, "timeLength": 200, "bitRate": 128, "status": 1}
					switch scenario {
					case "preview flag":
						data["is_free_part"] = 1
					case "short metadata":
						data["timeLength"] = 30
					case "wrong hash":
						data["hash"] = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
					}
					body, _ := json.Marshal(data)
					return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body))}, nil
				}
				cdns++
				if scenario == "account changed" {
					c.cookie = "token=new-account"
				}
				size := 3200000
				if scenario == "short file" {
					size = 480000
				}
				body := []byte{0xff, 0xfb, 0x90, 0}
				if scenario == "html" {
					body = []byte("<html>")
				}
				rangeHeader := fmt.Sprintf("bytes 0-3/%d", size)
				if scenario == "invalid range" {
					rangeHeader = "bytes 10-13/3200000"
				}
				return &http.Response{StatusCode: 206, Header: http.Header{"Content-Range": {rangeHeader}}, Body: io.NopCloser(bytes.NewReader(body))}, nil
			})
			ctx := context.Background()
			if scenario == "authenticated" {
				c.cookie = "token=account"
			}
			if scenario == "canceled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			got, err := c.ResolveDownloadByQuality(ctx, &model.Song{ID: hash, Duration: 200}, platform.QualityLossless)
			if scenario == "full" {
				if err != nil || got.Bitrate != 128 || got.Size != 3200000 || got.Extra["resolved_quality"] != platform.QualityStandard.String() {
					t.Fatalf("got=%+v err=%v", got, err)
				}
			} else if err == nil {
				t.Fatalf("accepted %s", scenario)
			}
			if (strings.Contains(scenario, "metadata") || scenario == "preview flag" || scenario == "wrong hash") && cdns != 0 {
				t.Fatal("invalid metadata reached CDN")
			}
			if (scenario == "authenticated" || scenario == "canceled") && calls != 0 {
				t.Fatal("unexpected anonymous request")
			}
		})
	}
}
