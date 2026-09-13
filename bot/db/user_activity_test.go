package db

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/liuran001/MusicBot-Go/bot"
)

func TestUserActivityStartsEmptyAndSurvivesCacheClearAndRestart(t *testing.T) {
	dir := t.TempDir()
	cachePath, dataPath := filepath.Join(dir, "cache.db"), filepath.Join(dir, "data.db")
	repo, err := NewSQLiteRepository(cachePath, dataPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	ctx := context.Background()
	now := time.Now()
	if _, err := repo.GetUserSettings(ctx, 42); err != nil {
		t.Fatal(err)
	}
	if err := repo.Create(ctx, &bot.SongInfo{Platform: "netease", TrackID: "old", Quality: "high", FromUserID: 42}); err != nil {
		t.Fatal(err)
	}
	stats, err := repo.GetUserActivityStats(ctx, now)
	if err != nil || stats.TotalUsers != 0 {
		t.Fatalf("historical settings/cache must not become activity: %+v, %v", stats, err)
	}
	if err := repo.RecordUserActivity(ctx, 7, "tester", "Test User", now); err != nil {
		t.Fatal(err)
	}
	if err := repo.DeleteAll(ctx); err != nil {
		t.Fatal(err)
	}
	if err := repo.Close(); err != nil {
		t.Fatal(err)
	}
	repo, err = NewSQLiteRepository(cachePath, dataPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	page, err := repo.ListUserActivity(ctx, 1, 8)
	if err != nil || page.TotalUsers != 1 || len(page.Users) != 1 || page.Users[0].UserID != 7 || page.Users[0].RequestCount != 1 {
		t.Fatalf("lost activity after cache clear/restart: %+v, %v", page, err)
	}
}

func TestUserActivityUpsertIsAtomicAndKeepsTimeBounds(t *testing.T) {
	repo := newTempRepo(t)
	ctx := context.Background()
	base := time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC)
	if err := repo.RecordUserActivity(ctx, 42, "latest", "Latest Name", base.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	const n = 24
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs <- repo.RecordUserActivity(ctx, 42, "older", "Older Name", base.Add(time.Duration(i)*time.Second))
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	page, err := repo.ListUserActivity(ctx, 1, 8)
	if err != nil || len(page.Users) != 1 {
		t.Fatalf("list: %+v, %v", page, err)
	}
	u := page.Users[0]
	if u.RequestCount != n+1 || !u.FirstSeenAt.Equal(base) || !u.LastSeenAt.Equal(base.Add(time.Hour)) || u.Username != "latest" || u.DisplayName != "Latest Name" {
		t.Fatalf("out-of-order/concurrent upsert: %+v", u)
	}
	// A later profile can remove a username, rather than displaying a stale one.
	if err := repo.RecordUserActivity(ctx, 42, "", "  Updated\nName  ", base.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	page, _ = repo.ListUserActivity(ctx, 1, 8)
	if page.Users[0].Username != "" || page.Users[0].DisplayName != "Updated Name" {
		t.Fatalf("profile not refreshed: %+v", page.Users[0])
	}
}

func TestUserActivityCalendarWindowsAndPagination(t *testing.T) {
	repo := newTempRepo(t)
	ctx := context.Background()
	loc := time.FixedZone("Asia/Shanghai", 8*3600)
	now := time.Date(2026, 9, 13, 1, 0, 0, 0, loc)
	today := time.Date(2026, 9, 13, 0, 0, 0, 0, loc)
	times := []time.Time{now, today, today.Add(-time.Second), today.AddDate(0, 0, -6), today.AddDate(0, 0, -6).Add(-time.Second)}
	for i, at := range times {
		if err := repo.RecordUserActivity(ctx, int64(i+1), "", "", at); err != nil {
			t.Fatal(err)
		}
	}
	stats, err := repo.GetUserActivityStats(ctx, now)
	if err != nil || stats.TotalUsers != 5 || stats.ActiveToday != 2 || stats.Active7Days != 4 {
		t.Fatalf("calendar stats: %+v, %v", stats, err)
	}
	page, err := repo.ListUserActivity(ctx, 1, 2)
	if err != nil || page.TotalPages != 3 || page.TotalUsers != 5 || len(page.Users) != 2 || page.Users[0].UserID != 1 || page.Users[1].UserID != 2 {
		t.Fatalf("first page: %+v, %v", page, err)
	}
	page, err = repo.ListUserActivity(ctx, 999999, 2)
	if err != nil || page.Page != 3 || len(page.Users) != 1 || page.Users[0].UserID != 5 {
		t.Fatalf("clamped last page: %+v, %v", page, err)
	}
	for _, userID := range []int64{-1, 0} {
		if err := repo.RecordUserActivity(ctx, userID, "", "", now); err == nil {
			t.Fatal("invalid user ID accepted")
		}
	}
}
