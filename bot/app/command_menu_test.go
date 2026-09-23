package app

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"unicode/utf8"

	"github.com/liuran001/MusicBot-Go/bot/i18n"
	"github.com/mymmrac/telego"
)

type recordingMenuClient struct {
	sets    []*telego.SetMyCommandsParams
	deletes []*telego.DeleteMyCommandsParams
	err     error
}

func (c *recordingMenuClient) SetMyCommands(_ context.Context, p *telego.SetMyCommandsParams) error {
	c.sets = append(c.sets, p)
	return c.err
}

func (c *recordingMenuClient) DeleteMyCommands(_ context.Context, p *telego.DeleteMyCommandsParams) error {
	c.deletes = append(c.deletes, p)
	return c.err
}

func TestLocalizedCommandMenus(t *testing.T) {
	if _, err := i18n.Init(); err != nil {
		t.Fatal(err)
	}
	for _, lang := range i18n.SupportedLanguages {
		for _, recognize := range []bool{false, true} {
			public := buildLocalizedCommands(i18n.For(lang), recognize, false)
			admin := buildLocalizedCommands(i18n.For(lang), recognize, true)
			if len(admin) != len(public)+3 || !reflect.DeepEqual(public, admin[:len(public)]) {
				t.Fatalf("%s: admin menu must preserve all public commands", lang)
			}
			seen := make(map[string]bool)
			for _, command := range admin {
				if seen[command.Command] {
					t.Fatalf("duplicate command %s", command.Command)
				}
				seen[command.Command] = true
				n := utf8.RuneCountInString(command.Description)
				if n < 1 || n > 256 {
					t.Fatalf("%s: invalid description length for %s", lang, command.Command)
				}
			}
			if seen["recognize"] != recognize || !seen["stats"] || !seen["users"] || !seen["broadcast"] {
				t.Fatalf("%s: unexpected command set: %v", lang, seen)
			}
			for _, command := range public {
				if command.Command == "stats" || command.Command == "users" || command.Command == "broadcast" {
					t.Fatal("admin command leaked to public menu")
				}
			}
			for _, spec := range activityCommandSpecs {
				if i18n.For(lang).T(spec.descKey) == spec.descKey {
					t.Fatalf("missing translation: %s/%s", lang, spec.descKey)
				}
			}
		}
	}
}

func TestSyncAdminCommandMenusScopesAndLanguages(t *testing.T) {
	if _, err := i18n.Init(); err != nil {
		t.Fatal(err)
	}
	client := &recordingMenuClient{}
	previous := map[int64]struct{}{101: {}, 202: {}, -1001: {}}
	current := map[int64]struct{}{202: {}, 303: {}, -1002: {}, 0: {}}
	if err := syncAdminCommandMenus(context.Background(), client, previous, current, false); err != nil {
		t.Fatal(err)
	}
	wantLangs := append([]string{""}, i18n.SupportedLanguages...)
	if len(client.sets) != 2*len(wantLangs) || len(client.deletes) != len(wantLangs) {
		t.Fatalf("unexpected request counts: set=%d delete=%d", len(client.sets), len(client.deletes))
	}
	seen := make(map[int64]map[string]bool)
	for _, params := range client.sets {
		scope, ok := params.Scope.(*telego.BotCommandScopeChat)
		if !ok || scope.Type != "chat" || (scope.ChatID.ID != 202 && scope.ChatID.ID != 303) {
			t.Fatalf("unexpected scope: %#v", params.Scope)
		}
		if seen[scope.ChatID.ID] == nil {
			seen[scope.ChatID.ID] = make(map[string]bool)
		}
		seen[scope.ChatID.ID][params.LanguageCode] = true
		lang := params.LanguageCode
		if lang == "" {
			lang = i18n.DefaultLanguage
		}
		want := buildLocalizedCommands(i18n.For(lang), false, true)
		if !reflect.DeepEqual(params.Commands, want) {
			t.Fatalf("incorrect commands for language %q", params.LanguageCode)
		}
	}
	for id, langs := range seen {
		for _, lang := range wantLangs {
			if !langs[lang] {
				t.Errorf("admin %d: missing language %q", id, lang)
			}
		}
	}
	deletedLangs := make(map[string]bool)
	for _, params := range client.deletes {
		scope, ok := params.Scope.(*telego.BotCommandScopeChat)
		if !ok || scope.Type != "chat" || scope.ChatID.ID != 101 {
			t.Fatalf("deleted wrong scope: %#v", params.Scope)
		}
		deletedLangs[params.LanguageCode] = true
	}
	for _, lang := range wantLangs {
		if !deletedLangs[lang] {
			t.Errorf("revoked admin: missing language %q", lang)
		}
	}
}

func TestSyncAdminCommandMenusErrors(t *testing.T) {
	wantErr := errors.New("telegram unavailable")
	client := &recordingMenuClient{err: wantErr}
	err := syncAdminCommandMenus(context.Background(), client, map[int64]struct{}{101: {}}, map[int64]struct{}{202: {}}, true)
	if !errors.Is(err, wantErr) {
		t.Fatalf("error not propagated: %v", err)
	}
	if len(client.sets) != len(i18n.SupportedLanguages)+1 || len(client.deletes) != len(i18n.SupportedLanguages)+1 {
		t.Fatal("failure prevented remaining menus from being attempted")
	}
	client = &recordingMenuClient{}
	if err := syncAdminCommandMenus(context.Background(), client, nil, nil, true); err != nil {
		t.Fatal(err)
	}
	if len(client.sets)+len(client.deletes) != 0 {
		t.Fatal("no admins should mean no scoped menu requests")
	}
}
