package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/liuran001/MusicBot-Go/bot/i18n"
	"github.com/mymmrac/telego"
	tu "github.com/mymmrac/telego/telegoutil"
)

var activityCommandSpecs = []localizedCommandSpec{
	{command: "stats", descKey: "activity_cmd_stats"},
	{command: "users", descKey: "activity_cmd_users"},
}

var broadcastCommandSpecs = []localizedCommandSpec{
	{command: "broadcast", descKey: "broadcast_cmd"},
}

func buildLocalizedCommands(loc *i18n.Localizer, enableRecognize, admin bool) []telego.BotCommand {
	commands := make([]telego.BotCommand, 0, len(botCommandSpecs)+len(activityCommandSpecs))
	appendSpecs := func(specs []localizedCommandSpec) {
		for _, spec := range specs {
			if spec.recognize && !enableRecognize {
				continue
			}
			commands = append(commands, telego.BotCommand{Command: spec.command, Description: loc.T(spec.descKey)})
		}
	}
	appendSpecs(botCommandSpecs)
	if admin {
		appendSpecs(activityCommandSpecs)
		appendSpecs(broadcastCommandSpecs)
	}
	return commands
}

type commandMenuClient interface {
	SetMyCommands(context.Context, *telego.SetMyCommandsParams) error
	DeleteMyCommands(context.Context, *telego.DeleteMyCommandsParams) error
}

// A chat-scoped menu replaces (not extends) the public menu. Publish a complete
// menu for each admin's private chat, including every supported language and
// the fallback. Never publish these commands to a group or global scope.
// Handler authorization remains mandatory regardless of menu visibility.
func syncAdminCommandMenus(ctx context.Context, client commandMenuClient, previous, current map[int64]struct{}, enableRecognize bool) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	languages := append([]string{""}, i18n.SupportedLanguages...)
	var errs []error
	for id := range previous {
		if _, retained := current[id]; retained || id <= 0 {
			continue
		}
		for _, lang := range languages {
			err := client.DeleteMyCommands(ctx, &telego.DeleteMyCommandsParams{
				Scope: tu.ScopeChat(tu.ID(id)), LanguageCode: lang,
			})
			if err != nil {
				errs = append(errs, fmt.Errorf("remove admin menu (%s): %w", lang, err))
			}
		}
	}
	for id := range current {
		if id <= 0 {
			continue
		}
		for _, lang := range languages {
			catalogLang := lang
			if catalogLang == "" {
				catalogLang = i18n.DefaultLanguage
			}
			err := client.SetMyCommands(ctx, &telego.SetMyCommandsParams{
				Scope: tu.ScopeChat(tu.ID(id)), LanguageCode: lang,
				Commands: buildLocalizedCommands(i18n.For(catalogLang), enableRecognize, true),
			})
			if err != nil {
				errs = append(errs, fmt.Errorf("publish admin menu (%s): %w", lang, err))
			}
		}
	}
	return errors.Join(errs...)
}

func (a *App) registerAdminCommands(ctx context.Context, previous map[int64]struct{}, enableRecognize bool) {
	if a.Telegram == nil {
		return
	}
	if err := syncAdminCommandMenus(ctx, a.Telegram.Client(), previous, a.AdminIDs, enableRecognize); err != nil && a.Logger != nil {
		a.Logger.Warn("failed to sync admin command menus", "error", err)
	}
}
