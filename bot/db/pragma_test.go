package db

import (
	"io"
	"log/slog"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"

	logpkg "github.com/liuran001/MusicBot-Go/bot/logger"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func newPragmaTestRepo(t *testing.T) *Repository {
	t.Helper()

	cacheFile, err := os.CreateTemp("", "musicbot-pragma-cache-*.db")
	if err != nil {
		t.Fatalf("create temp cache db: %v", err)
	}
	cachePath := cacheFile.Name()
	_ = cacheFile.Close()
	t.Cleanup(func() { os.Remove(cachePath) })

	dataFile, err := os.CreateTemp("", "musicbot-pragma-data-*.db")
	if err != nil {
		t.Fatalf("create temp data db: %v", err)
	}
	dataPath := dataFile.Name()
	_ = dataFile.Close()
	t.Cleanup(func() { os.Remove(dataPath) })

	base := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
	repo, err := NewSQLiteRepository(cachePath, dataPath, logpkg.NewGormLogger(base, logger.Silent))
	if err != nil {
		t.Fatalf("new repo: %v", err)
	}
	return repo
}

func readPragma(t *testing.T, db *gorm.DB, name string) string {
	t.Helper()
	var value string
	if err := db.Raw("PRAGMA " + name).Scan(&value).Error; err != nil {
		t.Fatalf("read PRAGMA %s: %v", name, err)
	}
	return strings.TrimSpace(value)
}

func TestSQLiteDSNCarriesPragmas(t *testing.T) {
	dsn := sqliteDSN("cache.db")
	if !strings.HasPrefix(dsn, "cache.db?") {
		t.Fatalf("sqliteDSN(%q) = %q, want the path preserved with a query", "cache.db", dsn)
	}
	_, query, _ := strings.Cut(dsn, "?")
	values, err := url.ParseQuery(query)
	if err != nil {
		t.Fatalf("DSN query %q does not parse: %v", query, err)
	}
	for _, pragma := range sqlitePragmas {
		if !slices.Contains(values["_pragma"], pragma) {
			t.Errorf("DSN %q is missing pragma %q", dsn, pragma)
		}
	}

	// A path that already carries a query must keep it.
	withQuery := sqliteDSN("cache.db?mode=rw")
	if !strings.HasPrefix(withQuery, "cache.db?mode=rw&") {
		t.Fatalf("sqliteDSN dropped an existing query: %q", withQuery)
	}
}

// TestPragmasSurviveConnectionRecycling is the regression this change exists
// for. Applying pragmas with PRAGMA statements after Open configures only the
// connection the pool happened to hand out; once ConnMaxLifetime retired it,
// the replacement came up with driver defaults and silently lost synchronous,
// cache_size, temp_store and foreign_keys.
func TestPragmasSurviveConnectionRecycling(t *testing.T) {
	repo := newPragmaTestRepo(t)

	want := map[string]string{
		"journal_mode": "wal",
		"synchronous":  "1", // NORMAL
		"cache_size":   "-64000",
		"temp_store":   "2", // MEMORY
		"foreign_keys": "1", // ON
		"busy_timeout": "5000",
	}

	for _, target := range []struct {
		name string
		db   *gorm.DB
	}{{"cache", repo.cacheDB}, {"data", repo.dataDB}} {
		sqlDB, err := target.db.DB()
		if err != nil {
			t.Fatalf("%s: get sql.DB: %v", target.name, err)
		}

		for phase := range 2 {
			if phase == 1 {
				// Force every pooled connection to be discarded, so the next
				// query has to open a fresh one.
				sqlDB.SetMaxIdleConns(0)
				sqlDB.SetMaxIdleConns(1)
			}
			for pragma, expected := range want {
				got := strings.ToLower(readPragma(t, target.db, pragma))
				if got != expected {
					label := "initial connection"
					if phase == 1 {
						label = "connection after recycling"
					}
					t.Errorf("%s db, %s: PRAGMA %s = %q, want %q",
						target.name, label, pragma, got, expected)
				}
			}
		}
	}
}
