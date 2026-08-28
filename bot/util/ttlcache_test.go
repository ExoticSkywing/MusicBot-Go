package util

import (
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestTTLCacheStoreLoadDelete(t *testing.T) {
	c := NewTTLCache[string](time.Minute, 0)
	if _, ok := c.Load("missing"); ok {
		t.Fatal("empty cache returned a value")
	}
	c.Store("k", "v")
	got, ok := c.Load("k")
	if !ok || got != "v" {
		t.Fatalf("Load = %q, %v; want %q, true", got, ok, "v")
	}
	c.Store("k", "v2")
	if got, _ := c.Load("k"); got != "v2" {
		t.Fatalf("Load after overwrite = %q, want %q", got, "v2")
	}
	c.Delete("k")
	if _, ok := c.Load("k"); ok {
		t.Fatal("deleted key still readable")
	}
}

// backdate ages every entry past the TTL without sleeping, so expiry tests stay
// fast and deterministic.
func backdate[V any](c *TTLCache[V], by time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, entry := range c.entries {
		entry.storedAt = entry.storedAt.Add(-by)
		c.entries[k] = entry
	}
}

func TestTTLCacheExpiredKeyIsNotReturned(t *testing.T) {
	c := NewTTLCache[string](time.Minute, 0)
	c.Store("k", "v")
	backdate(c, time.Hour)

	if _, ok := c.Load("k"); ok {
		t.Fatal("expired key was returned")
	}
	if n := c.Len(); n != 0 {
		t.Fatalf("expired key was not reclaimed on Load: Len = %d, want 0", n)
	}
}

// TestTTLCacheSweepReclaimsExpiredEntries guards the amortised sweep: expired
// entries must still be dropped even when nobody ever reads them back.
func TestTTLCacheSweepReclaimsExpiredEntries(t *testing.T) {
	c := NewTTLCache[int](time.Minute, 0)
	for i := range 500 {
		c.Store("stale"+strconv.Itoa(i), i)
	}
	backdate(c, time.Hour)

	for i := range ttlCacheMinSweepOps + 1 {
		c.Store("fresh"+strconv.Itoa(i), i)
	}
	if n := c.Len(); n > ttlCacheMinSweepOps+1 {
		t.Fatalf("stale entries were not swept: Len = %d, want <= %d", n, ttlCacheMinSweepOps+1)
	}
	if _, ok := c.Load("fresh0"); !ok {
		t.Fatal("sweep dropped a live entry")
	}
}

// TestTTLCacheEnforcesEntryCap covers the abuse case that motivated the cap:
// every key is unique and the TTL is long, so nothing ever expires.
func TestTTLCacheEnforcesEntryCap(t *testing.T) {
	const capacity = 512
	c := NewTTLCache[int](6*time.Hour, capacity)
	for i := range capacity * 3 {
		c.Store("k"+strconv.Itoa(i), i)
	}
	if n := c.Len(); n > capacity {
		t.Fatalf("entry cap not enforced: Len = %d, want <= %d", n, capacity)
	}
	// Eviction drops the oldest slice, so the most recent write must survive.
	if _, ok := c.Load("k" + strconv.Itoa(capacity*3-1)); !ok {
		t.Fatal("newest entry was evicted")
	}
}

func TestTTLCacheDoCachesAndDeduplicates(t *testing.T) {
	c := NewTTLCache[string](time.Minute, 0)
	var calls atomic.Int64

	for range 5 {
		got, err := c.Do("k", func() (string, error) {
			calls.Add(1)
			return "v", nil
		})
		if err != nil || got != "v" {
			t.Fatalf("Do = %q, %v; want %q, nil", got, err, "v")
		}
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("loader ran %d times across 5 calls, want 1", n)
	}
}

func TestTTLCacheDoDoesNotCacheErrors(t *testing.T) {
	c := NewTTLCache[string](time.Minute, 0)
	wantErr := errors.New("boom")
	var calls atomic.Int64

	for range 3 {
		if _, err := c.Do("k", func() (string, error) {
			calls.Add(1)
			return "", wantErr
		}); !errors.Is(err, wantErr) {
			t.Fatalf("Do error = %v, want %v", err, wantErr)
		}
	}
	if n := calls.Load(); n != 3 {
		t.Fatalf("loader ran %d times, want 3: errors must not be cached", n)
	}
	if n := c.Len(); n != 0 {
		t.Fatalf("failed load populated the cache: Len = %d, want 0", n)
	}

	// A later success must still be cached.
	if got, err := c.Do("k", func() (string, error) { return "v", nil }); err != nil || got != "v" {
		t.Fatalf("Do after failures = %q, %v; want %q, nil", got, err, "v")
	}
	if _, ok := c.Load("k"); !ok {
		t.Fatal("successful load was not cached")
	}
}

func TestTTLCacheConcurrentAccess(t *testing.T) {
	c := NewTTLCache[int](time.Minute, 0)
	var wg sync.WaitGroup
	for w := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 500 {
				key := "k" + strconv.Itoa(w*500+i)
				c.Store(key, i)
				c.Load(key)
				_, _ = c.Do("shared", func() (int, error) { return 1, nil })
				if i%7 == 0 {
					c.Delete(key)
				}
			}
		}()
	}
	wg.Wait()
}

func benchmarkTTLCacheLoad(b *testing.B, n int) {
	c := NewTTLCache[string](6*time.Hour, n*2)
	for i := range n {
		c.Store("k"+strconv.Itoa(i), "v")
	}
	key := "k" + strconv.Itoa(n/2)
	b.ResetTimer()
	for range b.N {
		c.Load(key)
	}
}

func BenchmarkTTLCacheLoad100(b *testing.B) { benchmarkTTLCacheLoad(b, 100) }
func BenchmarkTTLCacheLoad1k(b *testing.B)  { benchmarkTTLCacheLoad(b, 1000) }
func BenchmarkTTLCacheLoad10k(b *testing.B) { benchmarkTTLCacheLoad(b, 10000) }

// BenchmarkTTLCacheInlineQuery models the real hot path: one inline query
// renders ~30 cards, each minting a fresh token, against a warm cache.
func BenchmarkTTLCacheInlineQuery(b *testing.B) {
	const warm = 10000
	c := NewTTLCache[string](30*time.Minute, 1<<20)
	for i := range warm {
		c.Store("warm"+strconv.Itoa(i), "v")
	}
	b.ResetTimer()
	for i := range b.N {
		for card := range 30 {
			c.Store("q"+strconv.Itoa(i)+"-"+strconv.Itoa(card), "v")
		}
	}
}

// TestTTLCacheEvictionSurvivesTiedTimestamps guards a coarse-clock hazard: when
// a burst of writes lands in one tick every entry shares a storedAt, and an
// eviction keyed only on "not newer than the cutoff" would drop the whole
// cache. For callback tokens that means every live button reporting itself
// expired, so eviction must respect its own budget.
func TestTTLCacheEvictionSurvivesTiedTimestamps(t *testing.T) {
	const capacity = 512
	c := NewTTLCache[int](6*time.Hour, capacity)
	for i := range capacity {
		c.Store("k"+strconv.Itoa(i), i)
	}

	// Collapse every timestamp onto one instant, as a coarse clock would.
	c.mu.Lock()
	shared := time.Now()
	for k, entry := range c.entries {
		entry.storedAt = shared
		c.entries[k] = entry
	}
	c.mu.Unlock()

	c.Store("overflow", -1)

	expected := capacity - capacity*ttlCacheEvictNumer/ttlCacheEvictDenom + 1
	if got := c.Len(); got != expected {
		t.Fatalf("Len after evicting from a tied-timestamp cache = %d, want %d", got, expected)
	}
	if _, ok := c.Load("overflow"); !ok {
		t.Fatal("the write that triggered eviction was itself evicted")
	}
}
