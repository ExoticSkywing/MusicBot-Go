package handler

import (
	"strings"
	"testing"

	"github.com/liuran001/MusicBot-Go/bot/platform"
)

func TestDetailedAccountStatusShowsThirdPartyAvailabilitySeparatelyFromLogin(t *testing.T) {
	text := renderDetailedAccountStatusesHTML(zhCtx(), []platform.AccountStatus{{
		DisplayName:              "酷狗音乐",
		LoggedIn:                 false,
		ThirdPartyAudioAvailable: true,
		Highlights:               []string{"音源策略：第三方优先", "调用顺序：jbsou → 酷狗官方"},
	}})

	for _, want := range []string{
		"✅ 酷狗音乐（第三方音源可用）",
		"状态: 第三方音源可用",
		"官方账号: 未登录",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("status text missing %q:\n%s", want, text)
		}
	}
}

func TestSafeAccountStatusTreatsThirdPartyFirstAsAvailable(t *testing.T) {
	text := renderSafeAccountStatuses(zhCtx(), []platform.AccountStatus{{
		DisplayName:              "酷狗音乐",
		ThirdPartyAudioAvailable: true,
	}})

	if !strings.Contains(text, "✅ 酷狗音乐：第三方音源可用") {
		t.Fatalf("unexpected safe status:\n%s", text)
	}
	if !strings.Contains(text, "已登录：0/1") {
		t.Fatalf("third-party availability must not be counted as an official login:\n%s", text)
	}
}

func TestAccountStatusShowsPublicPlatformWithoutLogin(t *testing.T) {
	text := renderDetailedAccountStatusesHTML(zhCtx(), []platform.AccountStatus{{
		DisplayName:     "酷我音乐",
		Available:       true,
		NoLoginRequired: true,
		Summary:         "- 状态: 官方音源可用（无需登录）",
	}})
	for _, want := range []string{
		"✅ 酷我音乐（无需登录即可使用）",
		"状态: 无需登录即可使用",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("status text missing %q:\n%s", want, text)
		}
	}
}
