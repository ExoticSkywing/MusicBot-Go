package handler

import (
	"strings"
	"testing"

	"github.com/liuran001/MusicBot-Go/bot/admincmd"
	"github.com/liuran001/MusicBot-Go/bot/i18n"
)

func TestBuildHelpTextPrioritizesDirectMessages(t *testing.T) {
	for _, lang := range i18n.SupportedLanguages {
		t.Run(lang, func(t *testing.T) {
			ctx := langCtx(lang)
			text := buildHelpText(ctx, stubManagerWithPlatforms(), false, nil, true, true)
			for _, key := range []string{"help_section_direct_examples", "help_example_song", "help_example_song_artist", "help_result_buttons_hint"} {
				if tr(ctx, key) == key {
					t.Fatalf("missing translation: %s", key)
				}
			}
			previous := -1
			for _, part := range []string{
				mdV2Replacer.Replace(tr(ctx, "help_intro")),
				mdV2Replacer.Replace(tr(ctx, "help_private_hint")),
				mdV2Replacer.Replace(tr(ctx, "help_section_direct_examples")),
				"`" + tr(ctx, "help_example_song") + "`",
				"`" + tr(ctx, "help_example_music") + "`",
				"`" + tr(ctx, "help_example_song_artist") + "`",
				"`https://music.163.com/song/1859603835`",
				mdV2Replacer.Replace(tr(ctx, "help_result_buttons_hint")),
				mdV2Replacer.Replace(tr(ctx, "help_section_commands")),
				mdV2Replacer.Replace(tr(ctx, "help_section_examples")),
				"`/music " + tr(ctx, "help_example_music") + "`",
				"`/music https://music.163.com/song/1859603835`",
				"`/search " + tr(ctx, "help_example_search") + "`",
				mdV2Replacer.Replace(tr(ctx, "help_section_params")),
			} {
				index := strings.Index(text, part)
				if index <= previous {
					t.Fatalf("missing or misplaced %q in help:\n%s", part, text)
				}
				previous = index
			}
			if strings.Count(text, mdV2Replacer.Replace(tr(ctx, "help_section_examples"))) != 1 {
				t.Fatal("command examples repeated")
			}
		})
	}
}

func TestBuildHelpTextGroupDoesNotPromiseCommandlessInput(t *testing.T) {
	for _, lang := range i18n.SupportedLanguages {
		ctx := langCtx(lang)
		text := buildHelpText(ctx, nil, false, nil, false, false)
		for _, key := range []string{"help_private_hint", "help_section_direct_examples", "help_result_buttons_hint"} {
			if strings.Contains(text, mdV2Replacer.Replace(tr(ctx, key))) {
				t.Errorf("%s: private-only guidance leaked into group help: %s", lang, key)
			}
		}
		if !strings.Contains(text, "`/music "+tr(ctx, "help_example_music")+"`") || strings.Contains(text, "`/recognize`") {
			t.Errorf("%s: command examples or recognition visibility changed", lang)
		}
	}
}

func TestBuildHelpTextIncludesAccountCommandsForAdmin(t *testing.T) {
	adminCommands := []admincmd.Command{
		{Name: "checkck", Description: "检查插件 Cookie 有效性"},
		{Name: "login", Description: "统一账号登录（qr/cookie/sign/renew/auto/help）"},
	}

	text := buildHelpText(zhCtx(), nil, true, adminCommands, false, true)

	if !strings.Contains(text, "管理员命令") {
		t.Fatalf("expected admin command section, got: %s", text)
	}
	if strings.Contains(text, "账号命令") {
		t.Fatalf("expected no separate account section, got: %s", text)
	}
	for _, cmd := range []string{"/login"} {
		if !strings.Contains(text, cmd) {
			t.Fatalf("expected help text contains %s, got: %s", cmd, text)
		}
	}
	if strings.Count(text, "/login") != 1 {
		t.Fatalf("expected /login appears once, got: %s", text)
	}
}

func TestBuildHelpTextDoesNotShowAccountCommandsForNonAdmin(t *testing.T) {
	adminCommands := []admincmd.Command{
		{Name: "login", Description: "统一账号登录（qr/cookie/sign/renew/auto/help）"},
	}

	text := buildHelpText(zhCtx(), nil, false, adminCommands, false, true)

	if strings.Contains(text, "账号命令") {
		t.Fatalf("expected non-admin help hides legacy account section, got: %s", text)
	}
	if strings.Contains(text, "/login") {
		t.Fatalf("expected non-admin help hides account commands, got: %s", text)
	}
}
