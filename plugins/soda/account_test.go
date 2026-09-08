package soda

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestAccountStatusPublicPreviewDoesNotConfirmCookie(t *testing.T) {
	client := &Client{
		cookie: "sessionid=stale-test-value",
		httpClient: &http.Client{Timeout: time.Second, Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			var body string
			switch {
			case req.URL.Host == "www.douyin.com" && req.URL.Path == "/aweme/v1/web/user/profile/self/":
				body = `{}`
			case req.URL.Host == "www.douyin.com" && req.URL.Path == "/passport/web/account/info/":
				body = `{"data":{}}`
			case req.URL.Host == "beta-luna.douyin.com" && req.URL.Path == "/luna/h5/seo_track":
				body = `{"status_code":0,"seo_track":{"track":{"id":"7620326800652224539","name":"Public Preview"}},"track_player":{"url_player_info":"https://player.example.com/preview"}}`
			case req.URL.Host == "player.example.com" && req.URL.Path == "/preview":
				body = `{"Result":{"Data":{"PlayInfoList":[{"MainPlayUrl":"https://audio.example.com/preview.m4a","Size":1024,"Bitrate":128,"Format":"m4a","Quality":"higher"}]}}}`
			default:
				t.Fatalf("unexpected account probe request: %s", req.URL)
			}
			return sodaAccountTestResponse(req, body), nil
		})},
	}
	platform := NewPlatform(client)
	status, err := platform.AccountStatus(context.Background())
	if err != nil {
		t.Fatalf("AccountStatus() error = %v", err)
	}
	if status.LoggedIn {
		t.Fatalf("AccountStatus() LoggedIn = true for public preview: %+v", status)
	}
	if !strings.Contains(status.Summary, "登录态未确认") || !strings.Contains(status.Summary, "公开试听") {
		t.Fatalf("AccountStatus() summary = %q, want public-content warning", status.Summary)
	}

	checked, err := platform.CheckCookie(context.Background())
	if err != nil {
		t.Fatalf("CheckCookie() error = %v", err)
	}
	if checked.OK {
		t.Fatalf("CheckCookie() OK = true for public preview: %+v", checked)
	}
	if strings.Contains(checked.Message, "Hi-Res") {
		t.Fatalf("CheckCookie() made an audio-quality claim: %q", checked.Message)
	}
}

func TestCheckCookieSucceedsWithAuthenticatedProfile(t *testing.T) {
	client := &Client{
		cookie: "sessionid=valid-test-value",
		httpClient: &http.Client{Timeout: time.Second, Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.Host != "www.douyin.com" || req.URL.Path != "/aweme/v1/web/user/profile/self/" {
				t.Fatalf("unexpected authenticated profile request: %s", req.URL)
			}
			return sodaAccountTestResponse(req, `{"status_code":0,"user":{"uid":"12345678","nickname":"Tester","unique_id":"tester-id"}}`), nil
		})},
	}
	platform := NewPlatform(client)
	status, err := platform.AccountStatus(context.Background())
	if err != nil {
		t.Fatalf("AccountStatus() error = %v", err)
	}
	if !status.LoggedIn || status.UserID != "12345678" || status.Nickname != "Tester" {
		t.Fatalf("AccountStatus() = %+v, want authenticated profile", status)
	}

	checked, err := platform.CheckCookie(context.Background())
	if err != nil {
		t.Fatalf("CheckCookie() error = %v", err)
	}
	if !checked.OK || !strings.Contains(checked.Message, "账号登录状态已确认") {
		t.Fatalf("CheckCookie() = %+v, want confirmed login", checked)
	}
	if strings.Contains(checked.Message, "Hi-Res") {
		t.Fatalf("CheckCookie() made an audio-quality claim: %q", checked.Message)
	}
}

func sodaAccountTestResponse(req *http.Request, body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}
