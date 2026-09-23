package db

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/liuran001/MusicBot-Go/bot"
)

func TestBroadcastRecipientsDurableAndSeparateFromAnalytics(t *testing.T) {
	dir := t.TempDir()
	cache, data := filepath.Join(dir, "cache.db"), filepath.Join(dir, "data.db")
	repo, err := NewSQLiteRepository(cache, data, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	ctx := context.Background()
	if err := repo.RecordUserActivity(ctx, 1, "group_user", "Group User", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.GetUserSettings(ctx, 2); err != nil {
		t.Fatal(err)
	}
	check := func(want ...int64) {
		t.Helper()
		ids, err := repo.ListBroadcastRecipients(ctx)
		if err != nil || len(ids) != len(want) || (len(want) > 0 && !reflect.DeepEqual(ids, want)) {
			t.Fatalf("recipients: %v, %v; want %v", ids, err, want)
		}
	}
	check()
	for _, id := range []int64{3, 4, 5, 3} {
		if err := repo.RecordBroadcastRecipient(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	check(3, 4, 5)
	for _, id := range []int64{0, -1} {
		if err := repo.RecordBroadcastRecipient(ctx, id); err == nil {
			t.Fatal("accepted invalid private ID")
		}
	}
	if err := repo.SetPluginSetting(ctx, bot.PluginScopeUser, 4, bot.BroadcastSettingPlugin, bot.BroadcastSettingKey, "off"); err != nil {
		t.Fatal(err)
	}
	if err := repo.BlockBroadcastRecipient(ctx, 5); err != nil {
		t.Fatal(err)
	}
	check(3)
	for _, id := range []int64{1, 2, 4, 5} {
		if allowed, err := repo.CanBroadcastTo(ctx, id); err != nil || allowed {
			t.Fatalf("non-recipient %d allowed: %v %v", id, allowed, err)
		}
	}
	// Music-cache clearing and process restart must retain both recipient and
	// opt-out/block state. Existing analytics must remain unchanged.
	if err := repo.DeleteAll(ctx); err != nil {
		t.Fatal(err)
	}
	if err := repo.Close(); err != nil {
		t.Fatal(err)
	}
	repo, err = NewSQLiteRepository(cache, data, nil)
	if err != nil {
		t.Fatal(err)
	}
	check(3)
	for _, id := range []int64{4, 5} {
		if err := repo.RecordBroadcastRecipient(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	check(3, 5) // Private return unblocks 5, but never opts 4 back in.
	if err := repo.SetPluginSetting(ctx, bot.PluginScopeUser, 4, bot.BroadcastSettingPlugin, bot.BroadcastSettingKey, "on"); err != nil {
		t.Fatal(err)
	}
	check(3, 4, 5)
	stats, err := repo.GetUserActivityStats(ctx, time.Now())
	if err != nil || stats.TotalUsers != 1 {
		t.Fatalf("broadcast altered statistics: %+v %v", stats, err)
	}
}
