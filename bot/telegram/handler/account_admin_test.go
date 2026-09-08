package handler

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/liuran001/MusicBot-Go/bot/platform"
	"github.com/mymmrac/telego"
)

type loginSignTestPlatform struct {
	*stubPlatform
	message string
	called  bool
}

func (p *loginSignTestPlatform) Metadata() platform.Meta {
	return platform.Meta{
		Name:        "kugou",
		DisplayName: "酷狗音乐",
		Aliases:     []string{"kugou", "kg", "酷狗"},
	}
}

func (p *loginSignTestPlatform) SupportedLoginMethods() []string {
	return []string{"qr", "sign", "renew", "auto", "status"}
}

func (p *loginSignTestPlatform) SignIn(ctx context.Context) (string, error) {
	_ = ctx
	p.called = true
	return p.message, nil
}

func TestHandleAccountLoginDispatchesPlatformSign(t *testing.T) {
	manager := newStubManager()
	plat := &loginSignTestPlatform{stubPlatform: newStubPlatform("kugou"), message: "概念版签到成功"}
	manager.Register(plat)

	resp, err := handleAccountLogin(context.Background(), manager, "kugou sign")
	if err != nil {
		t.Fatalf("handleAccountLogin() error = %v", err)
	}
	if !plat.called {
		t.Fatal("expected SignIn to be called")
	}
	if resp == nil || !strings.Contains(resp.Text, "概念版签到成功") {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func TestHandleAccountLoginDispatchesGlobalSignAlias(t *testing.T) {
	manager := newStubManager()
	plat := &loginSignTestPlatform{stubPlatform: newStubPlatform("kugou"), message: "全局签到成功"}
	manager.Register(plat)

	resp, err := handleAccountLogin(context.Background(), manager, "sign kugou")
	if err != nil {
		t.Fatalf("handleAccountLogin() error = %v", err)
	}
	if !plat.called {
		t.Fatal("expected global SignIn to be called")
	}
	if resp == nil || !strings.Contains(resp.Text, "全局签到成功") {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func TestBuildPlatformLoginHelpIncludesSignExample(t *testing.T) {
	manager := newStubManager()
	plat := &loginSignTestPlatform{stubPlatform: newStubPlatform("kugou")}
	manager.Register(plat)

	text := buildPlatformLoginHelp(zhCtx(), manager, plat)
	for _, want := range []string{"支持: qr, sign, renew, auto, status", "/login kugou sign", "/login sign kugou"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected help contains %q, got: %s", want, text)
		}
	}
}

func TestHandleAccountLoginGlobalSignUsageOnExtraArgs(t *testing.T) {
	manager := newStubManager()
	plat := &loginSignTestPlatform{stubPlatform: newStubPlatform("kugou")}
	manager.Register(plat)

	resp, err := handleAccountLogin(context.Background(), manager, "sign kugou extra")
	if err != nil {
		t.Fatalf("handleAccountLogin() error = %v", err)
	}
	if plat.called {
		t.Fatal("expected SignIn not to be called on invalid global sign args")
	}
	if resp == nil || !strings.Contains(resp.Text, "/login sign <platform>") {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

type loginCookieOnlyTestPlatform struct {
	*stubPlatform
}

func (p *loginCookieOnlyTestPlatform) Metadata() platform.Meta {
	return platform.Meta{Name: "soda", DisplayName: "汽水音乐", Aliases: []string{"soda", "qs"}}
}

func (p *loginCookieOnlyTestPlatform) SupportedLoginMethods() []string {
	return []string{"cookie", "status"}
}

func TestBuildPlatformLoginHelpMatchesSupportedMethods(t *testing.T) {
	manager := newStubManager()
	plat := &loginCookieOnlyTestPlatform{stubPlatform: newStubPlatform("soda")}
	manager.Register(plat)

	text := buildPlatformLoginHelp(zhCtx(), manager, plat)
	if !strings.Contains(text, "/login soda cookie <cookie>") {
		t.Fatalf("expected cookie example, got: %s", text)
	}
	for _, unwanted := range []string{"/login soda renew", "/login soda auto on 21600", "/login soda sign", "/login soda qr"} {
		if strings.Contains(text, unwanted) {
			t.Fatalf("expected help not contains %q, got: %s", unwanted, text)
		}
	}
}

type loginVerificationTestPlatform struct {
	*stubPlatform
	result             platform.VerificationTest
	err                error
	called             int
	accountStatusCalls int
}

func (p *loginVerificationTestPlatform) Metadata() platform.Meta {
	return platform.Meta{
		Name:        "kugou",
		DisplayName: "酷狗音乐",
		Aliases:     []string{"kugou", "kg", "酷狗"},
	}
}

func (p *loginVerificationTestPlatform) SupportedLoginMethods() []string {
	return []string{"status", "verify-test"}
}

func (p *loginVerificationTestPlatform) StartVerificationTest(context.Context) (platform.VerificationTest, error) {
	p.called++
	return p.result, p.err
}

func (p *loginVerificationTestPlatform) AccountStatus(context.Context) (platform.AccountStatus, error) {
	p.accountStatusCalls++
	return platform.AccountStatus{
		UserID:   "account-user-secret",
		Nickname: "account-nickname-secret",
		Summary:  "account-summary-secret",
	}, nil
}

func TestHandleAccountLoginDispatchesVerificationTestWithoutAccountDetails(t *testing.T) {
	manager := newStubManager()
	verificationURL := "https://mbverify.obdo.cc/v/0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	plat := &loginVerificationTestPlatform{
		stubPlatform: newStubPlatform("kugou"),
		result: platform.VerificationTest{
			URL:       verificationURL,
			ExpiresAt: time.Now().Add(10 * time.Minute),
		},
	}
	manager.Register(plat)

	resp, err := handleAccountLogin(zhCtx(), manager, "kugou verify-test")
	if err != nil {
		t.Fatalf("handleAccountLogin() error = %v", err)
	}
	if plat.called != 1 {
		t.Fatalf("StartVerificationTest() calls = %d, want 1", plat.called)
	}
	if plat.accountStatusCalls != 0 {
		t.Fatalf("AccountStatus() calls = %d, want 0", plat.accountStatusCalls)
	}
	if resp == nil || !strings.Contains(resp.Text, verificationURL) || !strings.Contains(resp.Text, "验证码流程测试") || !strings.Contains(resp.Text, "不会操作账号") {
		t.Fatalf("unexpected response: %+v", resp)
	}
	for _, secret := range []string{"account-user-secret", "account-nickname-secret", "account-summary-secret"} {
		if strings.Contains(resp.Text, secret) {
			t.Fatalf("response exposed account detail %q: %s", secret, resp.Text)
		}
	}
}

func TestHandleAccountLoginVerificationTestFailsGenerically(t *testing.T) {
	tests := []struct {
		name   string
		result platform.VerificationTest
		err    error
	}{
		{name: "provider error", err: errors.New("provider account-secret token=must-not-leak")},
		{name: "insecure URL", result: platform.VerificationTest{URL: "http://mbverify.obdo.cc/v/test", ExpiresAt: time.Now().Add(time.Minute)}},
		{name: "expired URL", result: platform.VerificationTest{URL: "https://mbverify.obdo.cc/v/expired-secret", ExpiresAt: time.Now().Add(-time.Minute)}},
		{name: "URL credentials", result: platform.VerificationTest{URL: "https://account:secret@mbverify.obdo.cc/v/test", ExpiresAt: time.Now().Add(time.Minute)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager := newStubManager()
			plat := &loginVerificationTestPlatform{stubPlatform: newStubPlatform("kugou"), result: tt.result, err: tt.err}
			manager.Register(plat)

			resp, err := handleAccountLogin(zhCtx(), manager, "kugou verify-test")
			if err != nil {
				t.Fatalf("handleAccountLogin() error = %v", err)
			}
			if plat.called != 1 {
				t.Fatalf("StartVerificationTest() calls = %d, want 1", plat.called)
			}
			if resp == nil || resp.Text != "验证码流程测试暂时不可用，请稍后重试" {
				t.Fatalf("unexpected response: %+v", resp)
			}
			for _, secret := range []string{"provider account-secret", "must-not-leak", "expired-secret", "account:secret"} {
				if strings.Contains(resp.Text, secret) {
					t.Fatalf("generic failure exposed %q: %s", secret, resp.Text)
				}
			}
		})
	}
}

func TestHandleAccountLoginVerificationTestUnsupported(t *testing.T) {
	manager := newStubManager()
	manager.Register(newStubPlatform("kugou"))

	resp, err := handleAccountLogin(zhCtx(), manager, "kugou verify-test")
	if err != nil {
		t.Fatalf("handleAccountLogin() error = %v", err)
	}
	if resp == nil || !strings.Contains(resp.Text, "当前不支持验证码流程测试") {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func TestBuildPlatformLoginHelpIncludesVerificationTest(t *testing.T) {
	manager := newStubManager()
	plat := &loginVerificationTestPlatform{stubPlatform: newStubPlatform("kugou")}
	manager.Register(plat)

	text := buildPlatformLoginHelp(zhCtx(), manager, plat)
	for _, want := range []string{"verify-test", "/login kugou verify-test"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected help contains %q, got: %s", want, text)
		}
	}
}

func TestAdminLoginVerificationTestPermissionAndDelivery(t *testing.T) {
	manager := newStubManager()
	verificationURL := "https://mbverify.obdo.cc/v/0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	plat := &loginVerificationTestPlatform{
		stubPlatform: newStubPlatform("kugou"),
		result: platform.VerificationTest{
			URL:       verificationURL,
			ExpiresAt: time.Now().Add(10 * time.Minute),
		},
	}
	manager.Register(plat)
	bot, recorder := newVerificationTestBot(t)
	h := &AdminCommandHandler{AdminIDs: NewAdminSet(map[int64]struct{}{1: {}})}
	h.Commands = append(h.Commands, BuildAccountLoginCommand(manager))

	h.Handle(zhCtx(), bot, &telego.Update{Message: &telego.Message{
		MessageID: 7,
		Text:      "/login kugou verify-test",
		From:      &telego.User{ID: 2},
		Chat:      telego.Chat{ID: 1001, Type: "private"},
	}})

	if plat.called != 0 {
		t.Fatalf("non-admin triggered StartVerificationTest() %d time(s)", plat.called)
	}
	if calls := recorder.payloads("sendMessage"); len(calls) != 0 {
		t.Fatalf("non-admin received Telegram response: %#v", calls)
	}

	h.Handle(zhCtx(), bot, &telego.Update{Message: &telego.Message{
		MessageID: 8,
		Text:      "/login kugou verify-test",
		From:      &telego.User{ID: 1},
		Chat:      telego.Chat{ID: 1001, Type: "private"},
	}})

	if plat.called != 1 {
		t.Fatalf("admin triggered StartVerificationTest() %d time(s), want 1", plat.called)
	}
	calls := recorder.payloads("sendMessage")
	if len(calls) != 1 {
		t.Fatalf("admin Telegram responses = %d, want 1: %#v", len(calls), calls)
	}
	text, _ := calls[0]["text"].(string)
	if !strings.Contains(text, verificationURL) || !strings.Contains(text, "验证码流程测试") || !strings.Contains(text, "不会操作账号") {
		t.Fatalf("admin response did not preserve verification guidance and URL: %q", text)
	}
}
