package util

import (
	"slices"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

const (
	// ttlCacheMinSweepOps is the floor on how many operations must accumulate
	// before a full sweep is allowed. It keeps small caches from sweeping on
	// nearly every call.
	ttlCacheMinSweepOps = 256
	// TTLCacheDefaultMaxEntries bounds a cache even when nothing has expired
	// yet, so a caller that mints a fresh key per request cannot grow the map
	// without limit.
	TTLCacheDefaultMaxEntries = 16384
	// ttlCacheEvictNumer/Denom is the fraction of maxEntries dropped when the
	// cap is hit, so the O(n log n) trim is amortised over many inserts rather
	// than running on every insert once the cache is full.
	ttlCacheEvictNumer = 1
	ttlCacheEvictDenom = 8
)

// TTLCache is a concurrency-safe map whose entries expire after a fixed TTL.
//
// Lookups are O(1): only the requested key's own deadline is checked. Expired
// entries are reclaimed by a full sweep that runs at most once per cache-length
// operations, which keeps the sweep O(1) amortised rather than making every
// call pay an O(n) scan. A hard entry cap bounds memory for callers whose keys
// are never reused; when it is reached the oldest slice of entries is dropped.
type TTLCache[V any] struct {
	mu            sync.Mutex
	entries       map[string]ttlCacheEntry[V]
	ttl           time.Duration
	maxEntries    int
	opsSinceSweep int
	// sweepThreshold is frozen at each sweep. Deriving it from the live entry
	// count instead would let a growing cache outrun its own counter and defer
	// the sweep indefinitely.
	sweepThreshold int

	group singleflight.Group
}

type ttlCacheEntry[V any] struct {
	value    V
	storedAt time.Time
}

// NewTTLCache returns a cache holding entries for ttl. A maxEntries of zero or
// less selects TTLCacheDefaultMaxEntries.
func NewTTLCache[V any](ttl time.Duration, maxEntries int) *TTLCache[V] {
	if maxEntries <= 0 {
		maxEntries = TTLCacheDefaultMaxEntries
	}
	return &TTLCache[V]{
		entries:        make(map[string]ttlCacheEntry[V]),
		ttl:            ttl,
		maxEntries:     maxEntries,
		sweepThreshold: ttlCacheMinSweepOps,
	}
}

// maybeSweep drops every expired entry, but only once enough operations have
// accumulated since the last sweep. Callers must hold c.mu.
func (c *TTLCache[V]) maybeSweep(now time.Time) {
	c.opsSinceSweep++
	if c.opsSinceSweep < c.sweepThreshold {
		return
	}
	c.sweep(now)
}

// sweep drops every expired entry and rearms the next sweep for one full cache
// length from now. Callers must hold c.mu.
func (c *TTLCache[V]) sweep(now time.Time) {
	c.opsSinceSweep = 0
	for k, entry := range c.entries {
		if now.Sub(entry.storedAt) > c.ttl {
			delete(c.entries, k)
		}
	}
	c.sweepThreshold = max(len(c.entries), ttlCacheMinSweepOps)
}

// evictOldest drops the oldest slice of the cache once the entry cap is hit.
// Callers must hold c.mu.
func (c *TTLCache[V]) evictOldest(now time.Time) {
	c.sweep(now)
	if len(c.entries) < c.maxEntries {
		return
	}
	drop := c.maxEntries * ttlCacheEvictNumer / ttlCacheEvictDenom
	if drop < 1 {
		drop = 1
	}
	if drop > len(c.entries) {
		drop = len(c.entries)
	}
	stamps := make([]time.Time, 0, len(c.entries))
	for _, entry := range c.entries {
		stamps = append(stamps, entry.storedAt)
	}
	slices.SortFunc(stamps, func(a, b time.Time) int { return a.Compare(b) })
	cutoff := stamps[drop-1]
	// Stop at drop even if more entries share the cutoff timestamp. Coarse
	// clocks (Windows ticks at ~0.5-15ms) give a burst of writes identical
	// stamps, and an unbounded predicate would then evict the entire cache --
	// which for callback tokens means every live button reports itself expired.
	// Deleting during range is defined behaviour in Go.
	dropped := 0
	for k, entry := range c.entries {
		if dropped >= drop {
			break
		}
		if !entry.storedAt.After(cutoff) {
			delete(c.entries, k)
			dropped++
		}
	}
}

// Store adds or replaces an entry.
func (c *TTLCache[V]) Store(key string, value V) {
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.maybeSweep(now)
	if _, exists := c.entries[key]; !exists && len(c.entries) >= c.maxEntries {
		c.evictOldest(now)
	}
	c.entries[key] = ttlCacheEntry[V]{value: value, storedAt: now}
}

// Load returns the live value for key, if any.
func (c *TTLCache[V]) Load(key string) (V, bool) {
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.maybeSweep(now)
	entry, ok := c.entries[key]
	if !ok || now.Sub(entry.storedAt) > c.ttl {
		if ok {
			delete(c.entries, key)
		}
		var zero V
		return zero, false
	}
	return entry.value, true
}

// Delete removes an entry.
func (c *TTLCache[V]) Delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, key)
}

// Len reports the number of entries currently held, expired-but-not-yet-swept
// entries included. Intended for tests and diagnostics.
func (c *TTLCache[V]) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

// Do returns the cached value for key, otherwise calls load and caches a
// successful result. Concurrent misses on the same key share one load. Errors
// are returned to every waiter and are never cached.
func (c *TTLCache[V]) Do(key string, load func() (V, error)) (V, error) {
	if cached, ok := c.Load(key); ok {
		return cached, nil
	}
	result, err, _ := c.group.Do(key, func() (any, error) {
		// Re-check under the flight: a concurrent leader may have finished
		// between this goroutine's Load and its arrival here.
		if cached, ok := c.Load(key); ok {
			return cached, nil
		}
		value, err := load()
		if err != nil {
			return value, err
		}
		c.Store(key, value)
		return value, nil
	})
	if err != nil {
		var zero V
		if typed, ok := result.(V); ok {
			return typed, err
		}
		return zero, err
	}
	typed, _ := result.(V)
	return typed, nil
}
