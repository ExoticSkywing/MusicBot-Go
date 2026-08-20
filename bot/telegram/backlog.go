package telegram

import (
	"context"
	"fmt"

	"github.com/mymmrac/telego"
)

// PendingUpdateCount reports how many updates Telegram still holds for the bot.
// Telegram queues updates while the bot is offline and replays them all on the
// next getUpdates call, so this is the size of the burst a restart is about to
// receive.
func PendingUpdateCount(ctx context.Context, client *telego.Bot) (int, error) {
	if client == nil {
		return 0, fmt.Errorf("telegram client required")
	}
	info, err := client.GetWebhookInfo(ctx)
	if err != nil {
		return 0, fmt.Errorf("get webhook info: %w", err)
	}
	if info == nil {
		return 0, nil
	}
	return info.PendingUpdateCount, nil
}

// DropPendingUpdates acknowledges the queued backlog without handing it to the
// handlers and reports how many updates were discarded.
//
// Telegram exposes no "clear queue" call: an update stays queued until a
// getUpdates call requests an offset past it. Fetching with offset -1 returns
// just the newest update, and confirming one past its ID retires everything
// behind it in a single round trip, so the cost does not grow with the backlog.
func DropPendingUpdates(ctx context.Context, client *telego.Bot) (int, error) {
	if client == nil {
		return 0, fmt.Errorf("telegram client required")
	}

	pending, err := PendingUpdateCount(ctx, client)
	if err != nil {
		return 0, err
	}
	if pending == 0 {
		return 0, nil
	}

	// Offset -1 asks for the last update only, whatever the backlog size is.
	latest, err := client.GetUpdates(ctx, &telego.GetUpdatesParams{Offset: -1, Limit: 1})
	if err != nil {
		return 0, fmt.Errorf("peek latest update: %w", err)
	}
	if len(latest) == 0 {
		// The backlog drained between the two calls; nothing left to discard.
		return 0, nil
	}

	confirmed := latest[len(latest)-1].UpdateID + 1
	if _, err := client.GetUpdates(ctx, &telego.GetUpdatesParams{Offset: confirmed, Limit: 1}); err != nil {
		return 0, fmt.Errorf("confirm offset %d: %w", confirmed, err)
	}
	return pending, nil
}
